package handlers

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"dplaned/internal/nvmet"
	"dplaned/internal/sysapply"
)

// iSCSI and NVMe-oF targets live in the kernel, which forgets them at a
// reboot. Their configuration is saved (targetcli's saveconfig.json, the
// NVMe-oF export list); this file puts it back when the daemon starts, and
// keeps the firewall open for exactly the ports that are served.

const iscsiSaveFile = "/etc/target/saveconfig.json"

// openServicePorts opens the ports of a service in the firewall (none:
// closes them). Only an installed system has a firewall to tell.
func openServicePorts(service string, ports []string) error {
	if !IsNixOS() {
		return nil
	}
	return (sysapply.Env{}).SetServicePorts(service, ports)
}

// ── iSCSI ────────────────────────────────────────────────────────────────────

// iscsiTargetsInKernel lists the IQNs LIO currently serves.
func iscsiTargetsInKernel() []string {
	entries, err := os.ReadDir("/sys/kernel/config/target/iscsi")
	if err != nil {
		return nil
	}
	var iqns []string
	for _, e := range entries {
		if e.IsDir() && (strings.HasPrefix(e.Name(), "iqn.") || strings.HasPrefix(e.Name(), "eui.") || strings.HasPrefix(e.Name(), "naa.")) {
			iqns = append(iqns, e.Name())
		}
	}
	return iqns
}

// iscsiPortalPorts lists the TCP ports the targets in the kernel listen on.
func iscsiPortalPorts() []string {
	seen := map[string]bool{}
	matches, _ := filepath.Glob("/sys/kernel/config/target/iscsi/*/tpgt_*/np/*")
	for _, m := range matches {
		// "0.0.0.0:3260" or "[::]:3260"
		name := filepath.Base(m)
		if i := strings.LastIndex(name, ":"); i >= 0 {
			seen["tcp "+name[i+1:]] = true
		}
	}
	ports := make([]string, 0, len(seen))
	for p := range seen {
		ports = append(ports, p)
	}
	sort.Strings(ports)
	return ports
}

// syncISCSIFirewall opens the portal ports of the targets that exist.
func syncISCSIFirewall() error {
	return openServicePorts("iscsi", iscsiPortalPorts())
}

// RestoreISCSI recreates the saved iSCSI targets after a reboot. A kernel
// that already has targets (the daemon was only restarted) is left alone:
// restoring would cut the sessions of connected initiators.
func RestoreISCSI() {
	if _, err := os.Stat(iscsiSaveFile); err != nil {
		return
	}
	if len(iscsiTargetsInKernel()) == 0 {
		// The backing zvols appear a moment after their pool is imported.
		var lastErr error
		for attempt := 0; attempt < 6; attempt++ {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			out, err := exec.CommandContext(ctx, "targetcli", "restoreconfig", "savefile="+iscsiSaveFile, "clear_existing=true").CombinedOutput()
			cancel()
			text := strings.TrimSpace(string(out))
			// targetcli exits 0 even when it could not restore everything.
			if err == nil && !strings.Contains(text, "errors") && !strings.Contains(text, "Could not") {
				lastErr = nil
				break
			}
			lastErr = fmt.Errorf("%v %s", err, text)
			time.Sleep(10 * time.Second)
		}
		if lastErr != nil {
			log.Printf("ISCSI: the saved targets could not be restored completely: %v", lastErr)
		} else if n := len(iscsiTargetsInKernel()); n > 0 {
			log.Printf("ISCSI: %d saved target(s) restored", n)
		}
	}
	if err := syncISCSIFirewall(); err != nil {
		log.Printf("ISCSI: firewall: %v", err)
	}
}

// ── NVMe-oF ──────────────────────────────────────────────────────────────────

func syncNVMeFirewall(exports []nvmet.Export) error {
	seen := map[string]bool{}
	for _, e := range exports {
		port := e.ListenPort
		if port <= 0 {
			port = 4420
		}
		seen[fmt.Sprintf("tcp %d", port)] = true
	}
	ports := make([]string, 0, len(seen))
	for p := range seen {
		ports = append(ports, p)
	}
	sort.Strings(ports)
	return openServicePorts("nvmeof", ports)
}

// RestoreNVMeTargets applies the saved NVMe-oF exports after a reboot (the
// kernel's target configuration does not survive one).
func RestoreNVMeTargets() {
	list, err := nvmet.LoadExports(nvmet.TargetsFile)
	if err != nil {
		if !os.IsNotExist(err) {
			log.Printf("NVMEOF: reading the saved exports: %v", err)
		}
		return
	}
	if len(list) > 0 {
		var applyErr error
		for attempt := 0; attempt < 6; attempt++ {
			if applyErr = nvmet.Apply(list); applyErr == nil {
				break
			}
			time.Sleep(10 * time.Second) // zvols not there yet
		}
		if applyErr != nil {
			log.Printf("NVMEOF: the saved exports could not be applied: %v", applyErr)
		} else {
			log.Printf("NVMEOF: %d saved export(s) applied", len(list))
		}
	}
	if err := syncNVMeFirewall(list); err != nil {
		log.Printf("NVMEOF: firewall: %v", err)
	}
}
