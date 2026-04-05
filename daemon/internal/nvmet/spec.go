// Package nvmet configures the Linux kernel NVMe-oF target (nvmet) from a declarative spec.
package nvmet

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

const (
	// TargetsFile is the persisted JSON list of exports (API + GitOps).
	TargetsFile = "/var/lib/dplaneos/nvmet-targets.json"
	// ConfigfsRoot is where configfs is mounted.
	ConfigfsRoot = "/sys/kernel/config"
)

// Export describes one NVMe subsystem backed by a ZFS zvol, exported over NVMe/TCP.
type Export struct {
	SubsystemNQN string   `json:"subsystem_nqn"`
	Zvol         string   `json:"zvol"` // ZFS dataset name e.g. tank/vol (not /dev path)
	Transport    string   `json:"transport,omitempty"`
	ListenAddr   string   `json:"listen_addr,omitempty"`
	ListenPort   int      `json:"listen_port,omitempty"`
	NamespaceID  int      `json:"namespace_id,omitempty"`
	AllowAnyHost bool     `json:"allow_any_host,omitempty"`
	HostNQNs     []string `json:"host_nqns,omitempty"`
}

var nqnRegex = regexp.MustCompile(`^nqn\.[0-9]{4}-[0-9]{2}\.[a-z0-9][a-z0-9\-\.]*[a-z0-9]:[a-zA-Z0-9_\-.:]+$`)

// ValidateSpec checks NQN, transport, and ports without requiring the zvol to exist (GitOps parse-time).
func ValidateSpec(e *Export) error {
	if e == nil {
		return fmt.Errorf("export is nil")
	}
	if !nqnRegex.MatchString(e.SubsystemNQN) {
		return fmt.Errorf("invalid subsystem_nqn (expected nqn.YYYY-MM.domain:id)")
	}
	z := strings.TrimSpace(e.Zvol)
	if z == "" || strings.Contains(z, "..") || strings.HasPrefix(z, "/") {
		return fmt.Errorf("invalid zvol dataset name %q", e.Zvol)
	}
	t := strings.ToLower(strings.TrimSpace(e.Transport))
	if t == "" {
		t = "tcp"
	}
	if t != "tcp" {
		return fmt.Errorf("only transport \"tcp\" is supported (got %q)", e.Transport)
	}
	e.Transport = t
	addr := strings.TrimSpace(e.ListenAddr)
	if addr == "" {
		addr = "0.0.0.0"
	}
	e.ListenAddr = addr
	if e.ListenPort <= 0 {
		e.ListenPort = 4420
	}
	if e.ListenPort > 65535 {
		return fmt.Errorf("invalid listen_port")
	}
	if e.NamespaceID <= 0 {
		e.NamespaceID = 1
	}
	if e.NamespaceID > 1024 {
		return fmt.Errorf("namespace_id out of range")
	}
	if !e.AllowAnyHost {
		if len(e.HostNQNs) == 0 {
			return fmt.Errorf("host_nqns required when allow_any_host is false")
		}
		for _, h := range e.HostNQNs {
			if !nqnRegex.MatchString(strings.TrimSpace(h)) {
				return fmt.Errorf("invalid host_nqn %q", h)
			}
		}
	}
	return nil
}

// ValidateExport checks fields and ensures the zvol device exists (apply-time).
func ValidateExport(e *Export) error {
	if err := ValidateSpec(e); err != nil {
		return err
	}
	dev := ZvolDevicePath(strings.TrimSpace(e.Zvol))
	st, err := os.Stat(dev)
	if err != nil {
		return fmt.Errorf("zvol device %s: %w", dev, err)
	}
	if st.Mode()&os.ModeDevice == 0 {
		return fmt.Errorf("%s is not a device file", dev)
	}
	return nil
}

// ZvolDevicePath returns the /dev/zvol path for a dataset name.
func ZvolDevicePath(dataset string) string {
	return "/dev/zvol/" + strings.TrimSpace(dataset)
}

func slug(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:4])
}

func subsysDirName(nqn string) string {
	return "dplane-ss-" + slug(nqn)
}

func portDirName(transport, addr string, port int) string {
	key := fmt.Sprintf("%s|%s|%d", transport, addr, port)
	return "dplane-p-" + slug(key)
}

func hostDirName(hostNQN string) string {
	return "dplane-h-" + slug(hostNQN)
}

func nvmetRoot() string {
	return filepath.Join(ConfigfsRoot, "nvmet")
}
