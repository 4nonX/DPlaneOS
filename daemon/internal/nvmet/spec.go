// Package nvmet configures the Linux kernel NVMe target (nvmet) from a list
// of exports: each export is one NVMe subsystem with one namespace, a ZFS
// volume, served over NVMe/TCP.
package nvmet

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net"
	"os"
	"regexp"
	"strings"
)

// TargetsFile is the saved list of exports (API and GitOps).
var TargetsFile = "/var/lib/dplaneos/nvmet-targets.json"

// ConfigfsRoot is where configfs is mounted.
const ConfigfsRoot = "/sys/kernel/config"

// ApplySaved applies the saved list. A cluster node replaces it by a
// function that applies only what this node serves (exports of the volumes
// of a storage group follow the group).
var ApplySaved = func() error {
	list, err := LoadExports(TargetsFile)
	if err != nil {
		return err
	}
	return Apply(list)
}

// Export describes one NVMe subsystem backed by a ZFS volume.
type Export struct {
	SubsystemNQN string   `json:"subsystem_nqn"`
	Zvol         string   `json:"zvol"` // dataset name, e.g. tank/vol (not a /dev path)
	Transport    string   `json:"transport,omitempty"`
	ListenAddr   string   `json:"listen_addr,omitempty"`
	ListenPort   int      `json:"listen_port,omitempty"`
	NamespaceID  int      `json:"namespace_id,omitempty"`
	AllowAnyHost bool     `json:"allow_any_host,omitempty"`
	HostNQNs     []string `json:"host_nqns,omitempty"`
}

var nqnRegex = regexp.MustCompile(`^nqn\.[0-9]{4}-[0-9]{2}\.[a-z0-9][a-z0-9\-\.]*[a-z0-9]:[a-zA-Z0-9_\-.:]+$`)

// The kernel limits an NQN to 223 bytes.
const maxNQN = 223

// ValidateSpec checks an export and fills in its defaults, without looking
// at the volume (which need not be there when state.yaml is parsed).
func ValidateSpec(e *Export) error {
	if e == nil {
		return fmt.Errorf("export is nil")
	}
	if !nqnRegex.MatchString(e.SubsystemNQN) || len(e.SubsystemNQN) > maxNQN {
		return fmt.Errorf("invalid subsystem_nqn (expected nqn.YYYY-MM.domain:id)")
	}
	z := strings.TrimSpace(e.Zvol)
	if z == "" || strings.Contains(z, "..") || strings.HasPrefix(z, "/") {
		return fmt.Errorf("invalid zvol dataset name %q", e.Zvol)
	}
	e.Zvol = z
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
	if net.ParseIP(addr) == nil {
		return fmt.Errorf("listen_addr %q is not an IP address", e.ListenAddr)
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
		for i, h := range e.HostNQNs {
			h = strings.TrimSpace(h)
			if !nqnRegex.MatchString(h) || len(h) > maxNQN {
				return fmt.Errorf("invalid host_nqn %q", h)
			}
			e.HostNQNs[i] = h
		}
	}
	return nil
}

// ValidateExport is ValidateSpec, and the volume's device must exist.
func ValidateExport(e *Export) error {
	if err := ValidateSpec(e); err != nil {
		return err
	}
	return deviceThere(ZvolDevicePath(e.Zvol))
}

func deviceThere(dev string) error {
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

// Identity is what an initiator recognises a namespace by. The kernel makes
// these up at random for a new subsystem; an initiator that reconnects after
// the target restarted, or after the export moved to another node, would
// then find "a different disk" under the same name and drop the one it had
// in use. They are derived from the export's name instead: the same on
// every node and after every restart.
type Identity struct {
	Serial string // subsystem serial number, 20 characters
	UUID   string // namespace UUID
	NGUID  string // namespace NGUID, 32 hex digits
}

// IdentityOf returns the identity of an export's namespace.
func IdentityOf(nqn string, nsid int) Identity {
	ser := sha256.Sum256([]byte("dplaneos nvme serial\x00" + nqn))
	ns := sha256.Sum256([]byte(fmt.Sprintf("dplaneos nvme namespace\x00%s\x00%d", nqn, nsid)))
	u := make([]byte, 16)
	copy(u, ns[:16])
	u[6] = (u[6] & 0x0f) | 0x80 // version 8: a UUID of the application's own making
	u[8] = (u[8] & 0x3f) | 0x80 // RFC 4122 variant
	h := hex.EncodeToString(u)
	return Identity{
		Serial: hex.EncodeToString(ser[:10]),
		UUID:   h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32],
		NGUID:  hex.EncodeToString(ns[16:32]),
	}
}
