// Package nvmet configures the Linux kernel NVMe-oF target (nvmet) from a declarative spec.
package nvmet

import (
	"fmt"
	"os"
	"regexp"
	"strings"
)

const (
	// TargetsFile is the persisted JSON list of exports (API + GitOps).
	TargetsFile = "/var/lib/dplaneos/nvmet-targets.json"
	// ConfigfsRoot is where configfs is mounted.
	ConfigfsRoot = "/sys/kernel/config"
)

// ANAState is the Asymmetric Namespace Access state for a namespace group.
// See NVMe Base Specification §8.20 and Linux nvmet configfs docs.
type ANAState string

const (
	ANAOptimized    ANAState = "optimized"     // Active/Optimized  - primary path
	ANANonOptimized ANAState = "non-optimized" // Active/Non-Optimized - secondary path
	ANAStandby      ANAState = "standby"       // path is available but idle (HA standby node)
	ANAInaccessible ANAState = "inaccessible"  // path is unavailable
)

// ANAGroup maps a namespace to an ANA group with a specified access state.
// When ANAEnabled is true on an Export, each namespace is placed into a group
// so multi-path NVMe hosts can route I/O to the optimal controller.
// On the primary HA node set State = ANAOptimized; on the standby set State = ANAStandby.
type ANAGroup struct {
	GroupID     int      `json:"group_id"`     // 1-based ANA group identifier
	NamespaceID int      `json:"namespace_id"` // matches Export.NamespaceID
	State       ANAState `json:"state"`        // ANA access state for this group
}

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
	// ANA (Asymmetric Namespace Access) enables multi-path I/O path optimization.
	// When true, each namespace is placed in an ANA group so NVMe/multipath hosts
	// can prefer the optimized path and gracefully fall back on failover.
	ANAEnabled bool       `json:"ana_enabled,omitempty"`
	ANAGroups  []ANAGroup `json:"ana_groups,omitempty"`
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
