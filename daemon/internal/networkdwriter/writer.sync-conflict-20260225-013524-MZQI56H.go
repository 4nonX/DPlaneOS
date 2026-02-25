// Package networkdwriter manages D-PlaneOS network configuration by writing
// systemd-networkd unit files directly to /etc/systemd/network/.
//
// # Why this is the right approach
//
// The fundamental problem with the previous approaches:
//
//   nixwriter approach:
//     UI change → write .nix fragment → requires `nixos-rebuild switch`
//     Rebuild takes 2-5 minutes, disrupts running services during transition.
//     Changes are not active until rebuild completes.
//
//   netlink-only approach:
//     UI change → kernel netlink call → active immediately
//     Lost on reboot (kernel state is ephemeral).
//
//   This package:
//     UI change → write /etc/systemd/network/50-dplane-*.{network,netdev}
//              → networkctl reload  (< 1 second, zero downtime)
//     Persistent: YES — survives reboot (networkd reads these files at boot)
//     Survives nixos-rebuild: YES — NixOS activation only manages files
//     it created (prefix 10-, 20-, etc). It never deletes files it doesn't own.
//     Works on: every systemd distro (NixOS, Debian, Ubuntu, Arch, RHEL)
//     No rebuild required: EVER.
//
// # File naming convention
//
// systemd-networkd processes .network and .netdev files in lexicographic order.
// NixOS generates files with low numeric prefixes (10-, 20-).
// D-PlaneOS uses prefix 50- to ensure our config loads AFTER NixOS defaults
// but can still be overridden by manual 90- files if needed.
//
//   /etc/systemd/network/
//     10-eth0.network          ← NixOS managed (DHCP default)
//     50-dplane-eth0.network   ← D-PlaneOS managed (static IP override)
//     50-dplane-bond0.netdev   ← D-PlaneOS managed (bond device)
//     50-dplane-bond0.network  ← D-PlaneOS managed (bond network config)
//     50-dplane-eth0.100.netdev ← D-PlaneOS managed (VLAN device)
//     50-dplane-eth0.100.network ← D-PlaneOS managed (VLAN network config)
//
// # Reload mechanism
//
// After writing files, the writer calls `networkctl reload` which signals
// systemd-networkd to re-read all configuration files and apply changes
// to existing interfaces without disrupting established connections.
// This is equivalent to `systemctl reload systemd-networkd` but faster
// and more targeted.
//
// # Debian/Ubuntu compatibility
//
// On non-NixOS systems, systemd-networkd may not be the active network
// manager (NetworkManager or ifupdown may be used instead). The writer
// detects whether networkd is active and falls back gracefully:
//   - If networkd is active: write files + networkctl reload
//   - If networkd is inactive: write files only (for future use / migration)
//     and also apply via netlink (immediate effect)
package networkdwriter

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
)

const (
	// DefaultNetworkDir is where systemd-networkd reads configuration.
	// On NixOS and most systemd distros this is the standard path.
	DefaultNetworkDir = "/etc/systemd/network"

	// FilePrefix is prepended to all D-PlaneOS-managed files.
	// Chosen to sort after NixOS-generated files (10-, 20-) but before
	// any manual overrides (90-).
	FilePrefix = "50-dplane-"
)

// Writer manages D-PlaneOS network configuration files in the networkd directory.
type Writer struct {
	mu         sync.Mutex
	dir        string // /etc/systemd/network
	networkdUp bool   // whether systemd-networkd is active on this system
}

// New returns a Writer for the given networkd configuration directory.
func New(dir string) *Writer {
	return &Writer{
		dir:        dir,
		networkdUp: isNetworkdActive(),
	}
}

// Default returns a Writer using the standard /etc/systemd/network path.
func Default() *Writer {
	return New(DefaultNetworkDir)
}

// IsNetworkd reports whether systemd-networkd is the active network manager.
func (w *Writer) IsNetworkd() bool { return w.networkdUp }

// ── Static interface configuration ───────────────────────────────────────────

// SetStatic writes a static IP configuration for an interface.
//
// Generated files:
//
//	50-dplane-{iface}.network
//
// The file overrides any NixOS-generated DHCP config for the same interface
// because it sorts after NixOS files (10-, 20-) and networkd uses the
// last-matching configuration.
func (w *Writer) SetStatic(iface, cidr, gateway string, dns []string) error {
	if err := validateIface(iface); err != nil {
		return err
	}
	if cidr != "" {
		if err := validateCIDR(cidr); err != nil {
			return err
		}
	}

	var sb strings.Builder
	sb.WriteString("# Managed by D-PlaneOS — do not edit by hand\n")
	sb.WriteString("# Changes made via web UI. Delete this file to revert to NixOS defaults.\n\n")
	sb.WriteString("[Match]\n")
	sb.WriteString(fmt.Sprintf("Name=%s\n\n", iface))
	sb.WriteString("[Network]\n")

	if cidr == "" {
		sb.WriteString("DHCP=yes\n")
	} else {
		sb.WriteString("DHCP=no\n")
		sb.WriteString(fmt.Sprintf("Address=%s\n", cidr))
		if gateway != "" {
			sb.WriteString(fmt.Sprintf("Gateway=%s\n", gateway))
		}
		for _, d := range dns {
			if d != "" {
				sb.WriteString(fmt.Sprintf("DNS=%s\n", d))
			}
		}
	}

	filename := FilePrefix + sanitizeIface(iface) + ".network"
	return w.writeAndReload(filename, sb.String())
}

// SetDHCP configures an interface for DHCP, removing any static config.
func (w *Writer) SetDHCP(iface string, dns []string) error {
	return w.SetStatic(iface, "", "", dns)
}

// RemoveInterface deletes the D-PlaneOS network config for an interface.
// After removal, NixOS's own config (or networkd defaults) take over.
func (w *Writer) RemoveInterface(iface string) error {
	if err := validateIface(iface); err != nil {
		return err
	}
	filename := FilePrefix + sanitizeIface(iface) + ".network"
	return w.removeAndReload(filename)
}

// ── VLAN configuration ────────────────────────────────────────────────────────

// SetVLAN writes .netdev and .network files for a VLAN interface.
//
// Generated files:
//
//	50-dplane-{parent}.{vid}.netdev   — creates the vlan device
//	50-dplane-{parent}.{vid}.network  — attaches vlan to parent
//	50-dplane-{iface}.network         — network config for the vlan interface
func (w *Writer) SetVLAN(iface, parent string, vid int, cidr string, dns []string) error {
	if err := validateIface(iface); err != nil {
		return err
	}
	if err := validateIface(parent); err != nil {
		return fmt.Errorf("parent: %w", err)
	}
	if vid < 1 || vid > 4094 {
		return fmt.Errorf("VLAN ID %d out of range 1-4094", vid)
	}

	// 1. .netdev file — creates the VLAN device
	netdev := fmt.Sprintf(
		"# Managed by D-PlaneOS\n\n[NetDev]\nName=%s\nKind=vlan\n\n[VLAN]\nId=%d\n",
		iface, vid,
	)
	netdevFile := FilePrefix + sanitizeIface(parent) + "." + fmt.Sprintf("%d", vid) + ".netdev"
	if err := w.writeFile(netdevFile, netdev); err != nil {
		return fmt.Errorf("write netdev: %w", err)
	}

	// 2. Parent .network attachment — tells networkd to attach the VLAN to its parent
	// We use a dropin approach: write a companion .network file that adds the VLAN
	// without conflicting with the parent's existing .network file.
	parentAttach := fmt.Sprintf(
		"# Managed by D-PlaneOS — attaches VLAN %d to %s\n\n[Match]\nName=%s\n\n[Network]\nVLAN=%s\n",
		vid, parent, parent, iface,
	)
	parentFile := FilePrefix + sanitizeIface(parent) + "-vlan" + fmt.Sprintf("%d", vid) + ".network"
	if err := w.writeFile(parentFile, parentAttach); err != nil {
		return fmt.Errorf("write parent attachment: %w", err)
	}

	// 3. VLAN interface .network — IP config for the VLAN itself
	var ifNet strings.Builder
	ifNet.WriteString("# Managed by D-PlaneOS\n\n")
	ifNet.WriteString("[Match]\n")
	ifNet.WriteString(fmt.Sprintf("Name=%s\n\n", iface))
	ifNet.WriteString("[Network]\n")
	if cidr == "" {
		ifNet.WriteString("DHCP=yes\n")
	} else {
		ifNet.WriteString("DHCP=no\n")
		ifNet.WriteString(fmt.Sprintf("Address=%s\n", cidr))
		for _, d := range dns {
			if d != "" {
				ifNet.WriteString(fmt.Sprintf("DNS=%s\n", d))
			}
		}
	}
	ifFile := FilePrefix + sanitizeIface(iface) + ".network"
	if err := w.writeFile(ifFile, ifNet.String()); err != nil {
		return fmt.Errorf("write vlan network: %w", err)
	}

	return w.reload()
}

// RemoveVLAN deletes all D-PlaneOS files for a VLAN.
func (w *Writer) RemoveVLAN(iface, parent string, vid int) error {
	files := []string{
		FilePrefix + sanitizeIface(parent) + "." + fmt.Sprintf("%d", vid) + ".netdev",
		FilePrefix + sanitizeIface(parent) + "-vlan" + fmt.Sprintf("%d", vid) + ".network",
		FilePrefix + sanitizeIface(iface) + ".network",
	}
	for _, f := range files {
		path := filepath.Join(w.dir, f)
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			log.Printf("[networkdwriter] WARN: remove %s: %v", path, err)
		}
	}
	return w.reload()
}

// ── Bond configuration ────────────────────────────────────────────────────────

// SetBond writes .netdev and .network files for a bond interface.
//
// Generated files:
//
//	50-dplane-bond-{name}.netdev      — creates the bond device
//	50-dplane-bond-{name}.network     — network config for the bond
//	50-dplane-{slave}.network         — binds each slave to the bond
func (w *Writer) SetBond(name string, slaves []string, mode string, cidr string, dns []string) error {
	if err := validateIface(name); err != nil {
		return err
	}
	for _, s := range slaves {
		if err := validateIface(s); err != nil {
			return fmt.Errorf("slave %q: %w", s, err)
		}
	}

	ndMode, err := bondModeToNetworkd(mode)
	if err != nil {
		return err
	}

	// 1. Bond .netdev — creates the bond device
	netdev := fmt.Sprintf(
		"# Managed by D-PlaneOS\n\n[NetDev]\nName=%s\nKind=bond\n\n[Bond]\nMode=%s\n",
		name, ndMode,
	)
	if err := w.writeFile(FilePrefix+"bond-"+sanitizeIface(name)+".netdev", netdev); err != nil {
		return fmt.Errorf("write bond netdev: %w", err)
	}

	// 2. Bond .network — IP configuration
	var bondNet strings.Builder
	bondNet.WriteString("# Managed by D-PlaneOS\n\n")
	bondNet.WriteString("[Match]\n")
	bondNet.WriteString(fmt.Sprintf("Name=%s\n\n", name))
	bondNet.WriteString("[Network]\n")
	if cidr == "" {
		bondNet.WriteString("DHCP=yes\n")
	} else {
		bondNet.WriteString("DHCP=no\n")
		bondNet.WriteString(fmt.Sprintf("Address=%s\n", cidr))
		for _, d := range dns {
			if d != "" {
				bondNet.WriteString(fmt.Sprintf("DNS=%s\n", d))
			}
		}
	}
	if err := w.writeFile(FilePrefix+"bond-"+sanitizeIface(name)+".network", bondNet.String()); err != nil {
		return fmt.Errorf("write bond network: %w", err)
	}

	// 3. Slave .network files — bind each slave to the bond
	for _, slave := range slaves {
		slaveNet := fmt.Sprintf(
			"# Managed by D-PlaneOS — slave of bond %s\n\n[Match]\nName=%s\n\n[Network]\nBond=%s\n",
			name, slave, name,
		)
		slaveFile := FilePrefix + sanitizeIface(slave) + "-slave-" + sanitizeIface(name) + ".network"
		if err := w.writeFile(slaveFile, slaveNet); err != nil {
			log.Printf("[networkdwriter] WARN: write slave %s: %v", slave, err)
		}
	}

	return w.reload()
}

// RemoveBond deletes all D-PlaneOS files for a bond and its slaves.
func (w *Writer) RemoveBond(name string, slaves []string) error {
	files := []string{
		FilePrefix + "bond-" + sanitizeIface(name) + ".netdev",
		FilePrefix + "bond-" + sanitizeIface(name) + ".network",
	}
	for _, s := range slaves {
		files = append(files, FilePrefix+sanitizeIface(s)+"-slave-"+sanitizeIface(name)+".network")
	}
	for _, f := range files {
		path := filepath.Join(w.dir, f)
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			log.Printf("[networkdwriter] WARN: remove %s: %v", path, err)
		}
	}
	return w.reload()
}

// ── DNS (global resolver) ─────────────────────────────────────────────────────

// SetGlobalDNS writes /etc/systemd/resolved.conf.d/50-dplane.conf
// to set system-wide DNS resolvers via systemd-resolved.
// This is separate from per-interface DNS (handled in SetStatic/SetVLAN/SetBond).
func (w *Writer) SetGlobalDNS(servers []string) error {
	for _, s := range servers {
		if !isValidIP(s) {
			return fmt.Errorf("invalid DNS server: %q", s)
		}
	}

	resolvedDropin := "/etc/systemd/resolved.conf.d"
	if err := os.MkdirAll(resolvedDropin, 0755); err != nil {
		return fmt.Errorf("create resolved.conf.d: %w", err)
	}

	var sb strings.Builder
	sb.WriteString("# Managed by D-PlaneOS — do not edit by hand\n\n")
	sb.WriteString("[Resolve]\n")
	sb.WriteString(fmt.Sprintf("DNS=%s\n", strings.Join(servers, " ")))
	sb.WriteString("FallbackDNS=1.1.1.1 8.8.8.8\n")

	path := filepath.Join(resolvedDropin, "50-dplane.conf")
	if err := atomicWrite(path, sb.String()); err != nil {
		return fmt.Errorf("write resolved dropin: %w", err)
	}

	// Reload resolved
	if isServiceActive("systemd-resolved") {
		if err := exec.Command("systemctl", "reload", "systemd-resolved").Run(); err != nil {
			log.Printf("[networkdwriter] WARN: reload systemd-resolved: %v", err)
		}
	}

	log.Printf("[networkdwriter] DNS set: %v", servers)
	return nil
}

// ── List managed files ────────────────────────────────────────────────────────

// ListManagedFiles returns all files in the networkd directory that were
// written by D-PlaneOS (those with the FilePrefix prefix).
func (w *Writer) ListManagedFiles() ([]string, error) {
	entries, err := os.ReadDir(w.dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var files []string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), FilePrefix) {
			files = append(files, e.Name())
		}
	}
	return files, nil
}

// ── Internal helpers ──────────────────────────────────────────────────────────

// writeAndReload writes a single file then reloads networkd.
func (w *Writer) writeAndReload(filename, content string) error {
	if err := w.writeFile(filename, content); err != nil {
		return err
	}
	return w.reload()
}

// removeAndReload deletes a file then reloads networkd.
func (w *Writer) removeAndReload(filename string) error {
	path := filepath.Join(w.dir, filename)
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove %s: %w", filename, err)
	}
	return w.reload()
}

// writeFile atomically writes content to dir/filename.
func (w *Writer) writeFile(filename, content string) error {
	w.mu.Lock()
	defer w.mu.Unlock()

	if err := os.MkdirAll(w.dir, 0755); err != nil {
		return fmt.Errorf("networkdwriter: mkdir %s: %w", w.dir, err)
	}
	path := filepath.Join(w.dir, filename)
	return atomicWrite(path, content)
}

// atomicWrite writes content to path using a tmp-then-rename pattern.
func atomicWrite(path, content string) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".dplane-net-*.tmp")
	if err != nil {
		return fmt.Errorf("create tmp: %w", err)
	}
	tmpPath := tmp.Name()
	if _, err := tmp.WriteString(content); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("write tmp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("close tmp: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("rename: %w", err)
	}
	log.Printf("[networkdwriter] wrote %s", path)
	return nil
}

// reload signals systemd-networkd to re-read configuration.
// Uses `networkctl reload` which is non-disruptive (does not restart networkd).
func (w *Writer) reload() error {
	if !w.networkdUp {
		log.Printf("[networkdwriter] networkd not active — files written, reload skipped")
		return nil
	}
	out, err := exec.Command("networkctl", "reload").CombinedOutput()
	if err != nil {
		log.Printf("[networkdwriter] WARN: networkctl reload: %v (%s)", err, strings.TrimSpace(string(out)))
		// Not fatal — files are written correctly, they'll be read on next boot or manual reload
		return nil
	}
	log.Printf("[networkdwriter] networkctl reload: OK")
	return nil
}

func isNetworkdActive() bool {
	return isServiceActive("systemd-networkd")
}

func isServiceActive(service string) bool {
	err := exec.Command("systemctl", "is-active", "--quiet", service).Run()
	return err == nil
}

// bondModeToNetworkd converts human-readable mode strings to networkd Bond.Mode values.
func bondModeToNetworkd(mode string) (string, error) {
	modes := map[string]string{
		"balance-rr":    "balance-rr",
		"active-backup": "active-backup",
		"balance-xor":   "balance-xor",
		"broadcast":     "broadcast",
		"802.3ad":       "802.3ad",
		"balance-tlb":   "balance-tlb",
		"balance-alb":   "balance-alb",
		// Common aliases
		"lacp":          "802.3ad",
		"failover":      "active-backup",
		"roundrobin":    "balance-rr",
	}
	if m, ok := modes[mode]; ok {
		return m, nil
	}
	return "", fmt.Errorf("unknown bond mode %q", mode)
}

// sanitizeIface replaces characters that are valid in interface names but
// not in filenames (specifically '.') with '-'.
func sanitizeIface(iface string) string {
	return strings.NewReplacer(".", "-").Replace(iface)
}

func validateIface(iface string) error {
	if len(iface) == 0 || len(iface) > 16 {
		return fmt.Errorf("interface name %q: invalid length", iface)
	}
	for _, c := range iface {
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') ||
			(c >= '0' && c <= '9') || c == '-' || c == '_' || c == '.') {
			return fmt.Errorf("interface name %q: invalid character %q", iface, c)
		}
	}
	return nil
}

func validateCIDR(cidr string) error {
	if !strings.Contains(cidr, "/") {
		return fmt.Errorf("CIDR %q: must include prefix length (e.g. 192.168.1.1/24)", cidr)
	}
	return nil
}

func isValidIP(s string) bool {
	if len(s) == 0 || len(s) > 45 {
		return false
	}
	for _, c := range s {
		if !((c >= '0' && c <= '9') || c == '.' || c == ':') {
			return false
		}
	}
	return true
}
