// Package nixwriter bridges D-PlaneOS's imperative management layer with
// NixOS's declarative configuration system.
//
// # v5.0 Architecture: JSON-to-Nix Bridge
//
// The previous approach ("The Surgeon") wrote raw Nix syntax stanzas from Go
// string templates. This was fragile: any special character, missing escape, or
// unexpected value could produce a file that fails nix-instantiate --parse,
// silently breaking the next nixos-rebuild.
//
// The v5 approach:
//   - The daemon writes ONE file: /var/lib/dplaneos/dplane-state.json
//     This is pure JSON produced by encoding/json.Marshal — 100% safe by construction.
//   - /etc/nixos/dplane-generated.nix is a STATIC file installed once by
//     setup-nixos.sh. It never changes. It contains:
//     let s = builtins.fromJSON (builtins.readFile /var/lib/dplaneos/dplane-state.json);
//     and maps JSON keys to NixOS module options using `s.key or default` guards.
//   - Because the .nix file is static and contains no interpolated values,
//     there is NO syntax error risk from daemon writes, ever.
//
// Caller API is identical to v4 — handlers call Set*() methods and never
// know or care whether a .nix file or a .json file is being written.
package nixwriter

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

const (
	// StateJSONPath is the only file the daemon ever writes for NixOS state.
	// The static dplane-generated.nix reads this file via builtins.fromJSON.
	StateJSONPath = "/var/lib/dplaneos/dplane-state.json"

	// GeneratedNixPath is where setup-nixos.sh installs the static bridge file.
	// The daemon does NOT write to this path in v5.
	GeneratedNixPath = "/etc/nixos/dplane-generated.nix"
)

// DPlaneState is the complete set of system settings that D-PlaneOS can manage
// on a NixOS host. It is serialised to JSON and read by dplane-generated.nix.
//
// All fields use `omitempty` so unset values are absent from JSON, and the Nix
// `s.key or default` guard in dplane-generated.nix supplies the NixOS default.
//
// Field naming: use snake_case to match the Nix key names in dplane-generated.nix.
type DPlaneState struct {
	// ── System ────────────────────────────────────────────────────────────────
	Hostname string `json:"hostname,omitempty"`
	Timezone string `json:"timezone,omitempty"`

	// ── Network ───────────────────────────────────────────────────────────────
	DNSServers []string `json:"dns_servers,omitempty"`
	NTPServers []string `json:"ntp_servers,omitempty"`

	// Firewall: complete allowed port lists. Nil = "use NixOS defaults".
	// Set to an explicit empty list to open no ports (beyond NixOS defaults).
	FirewallTCP []int `json:"firewall_tcp,omitempty"`
	FirewallUDP []int `json:"firewall_udp,omitempty"`

	// Static interface configs. Key = interface name (e.g. "eth0").
	NetworkStatics map[string]NetworkStaticEntry `json:"network_statics,omitempty"`

	// Bond devices. Key = bond name.
	NetworkBonds map[string]NetworkBondEntry `json:"network_bonds,omitempty"`

	// VLAN devices. Key = vlan interface name (e.g. "eth0.10").
	NetworkVLANs map[string]NetworkVLANEntry `json:"network_vlans,omitempty"`

	// ── Samba ────────────────────────────────────────────────────────────────
	SambaWorkgroup    string `json:"samba_workgroup,omitempty"`
	SambaServerString string `json:"samba_server_string,omitempty"`
	SambaTimeMachine  bool   `json:"samba_time_machine,omitempty"`
	SambaAllowGuest   bool   `json:"samba_allow_guest,omitempty"`
	SambaExtraGlobal  string `json:"samba_extra_global,omitempty"`
}

// NetworkStaticEntry describes one statically-configured interface.
type NetworkStaticEntry struct {
	CIDR    string `json:"cidr"`    // e.g. "192.168.1.10/24"
	Gateway string `json:"gateway"` // e.g. "192.168.1.1", may be ""
}

// NetworkBondEntry describes a bonding interface.
type NetworkBondEntry struct {
	Slaves []string `json:"slaves"`
	Mode   string   `json:"mode"` // e.g. "802.3ad"
}

// NetworkVLANEntry describes a VLAN sub-interface.
type NetworkVLANEntry struct {
	Parent string `json:"parent"` // parent interface, e.g. "eth0"
	VID    int    `json:"vid"`    // VLAN ID 1-4094
}

// Writer manages the dplane-state.json file.
// On non-NixOS systems all writes are no-ops — safe to use everywhere.
type Writer struct {
	mu    sync.Mutex
	path  string
	nixOS bool
	state DPlaneState
}

// New returns a Writer targeting jsonPath.
// If the system is not NixOS, all writes are no-ops.
func New(jsonPath string) *Writer {
	return &Writer{
		path:  jsonPath,
		nixOS: isNixOS(),
	}
}

// Default returns a Writer using the standard StateJSONPath.
func Default() *Writer {
	return New(StateJSONPath)
}

// DefaultWriter is an alias for Default(), preserving the v4 call site.
func DefaultWriter() *Writer {
	return Default()
}

func isNixOS() bool {
	_, err := os.Stat("/etc/NIXOS")
	return err == nil
}

// IsNixOS reports whether this system is NixOS.
func (w *Writer) IsNixOS() bool { return w.nixOS }
 
// State returns a copy of the current in-memory state.
func (w *Writer) State() DPlaneState {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.state
}

// LoadFromDisk reads an existing dplane-state.json into memory.
// Called at daemon startup so in-memory state matches the file on disk.
// If the file doesn't exist yet, this is a no-op (state starts as zero value).
func (w *Writer) LoadFromDisk() error {
	if !w.nixOS {
		return nil
	}
	data, err := os.ReadFile(w.path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("nixwriter: load state: %w", err)
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return json.Unmarshal(data, &w.state)
}

// ── Network setters ──────────────────────────────────────────────────────────

// SetStaticInterface records a static IP assignment for an interface.
// Pass cidr="" to configure DHCP (removes any static config for the interface).
func (w *Writer) SetStaticInterface(iface, cidr, gateway string) error {
	if !validateIface(iface) {
		return fmt.Errorf("invalid interface name: %q", iface)
	}
	if cidr != "" && !validateCIDR(cidr) {
		return fmt.Errorf("invalid CIDR: %q", cidr)
	}
	w.mu.Lock()
	if w.state.NetworkStatics == nil {
		w.state.NetworkStatics = make(map[string]NetworkStaticEntry)
	}
	if cidr == "" {
		delete(w.state.NetworkStatics, iface)
	} else {
		w.state.NetworkStatics[iface] = NetworkStaticEntry{CIDR: cidr, Gateway: gateway}
	}
	w.mu.Unlock()
	return w.flush()
}

// RemoveStaticInterface removes the static IP config for an interface.
func (w *Writer) RemoveStaticInterface(iface string) error {
	return w.SetStaticInterface(iface, "", "")
}

// SetBond records a bond interface configuration.
func (w *Writer) SetBond(name string, slaves []string, mode string) error {
	if !validateIface(name) {
		return fmt.Errorf("invalid bond name: %q", name)
	}
	for _, s := range slaves {
		if !validateIface(s) {
			return fmt.Errorf("invalid slave interface: %q", s)
		}
	}
	validModes := map[string]bool{
		"balance-rr": true, "active-backup": true, "balance-xor": true,
		"broadcast": true, "802.3ad": true, "balance-tlb": true, "balance-alb": true,
	}
	if !validModes[mode] {
		return fmt.Errorf("unknown bond mode: %q", mode)
	}
	w.mu.Lock()
	if w.state.NetworkBonds == nil {
		w.state.NetworkBonds = make(map[string]NetworkBondEntry)
	}
	w.state.NetworkBonds[name] = NetworkBondEntry{Slaves: slaves, Mode: mode}
	w.mu.Unlock()
	return w.flush()
}

// RemoveBond removes a bond interface configuration.
func (w *Writer) RemoveBond(name string) error {
	w.mu.Lock()
	delete(w.state.NetworkBonds, name)
	w.mu.Unlock()
	return w.flush()
}

// SetVLAN records a VLAN sub-interface configuration.
func (w *Writer) SetVLAN(ifName, parent string, vid int) error {
	if !validateIface(ifName) || !validateIface(parent) {
		return fmt.Errorf("invalid interface name")
	}
	if vid < 1 || vid > 4094 {
		return fmt.Errorf("VLAN ID %d out of range 1–4094", vid)
	}
	w.mu.Lock()
	if w.state.NetworkVLANs == nil {
		w.state.NetworkVLANs = make(map[string]NetworkVLANEntry)
	}
	w.state.NetworkVLANs[ifName] = NetworkVLANEntry{Parent: parent, VID: vid}
	w.mu.Unlock()
	return w.flush()
}

// RemoveVLAN removes a VLAN sub-interface configuration.
func (w *Writer) RemoveVLAN(ifName string) error {
	w.mu.Lock()
	delete(w.state.NetworkVLANs, ifName)
	w.mu.Unlock()
	return w.flush()
}

// ── System setters ───────────────────────────────────────────────────────────

// SetDNS records persistent DNS server configuration.
func (w *Writer) SetDNS(servers []string) error {
	for _, s := range servers {
		if !validateIP(s) {
			return fmt.Errorf("invalid DNS server IP: %q", s)
		}
	}
	w.mu.Lock()
	w.state.DNSServers = servers
	w.mu.Unlock()
	return w.flush()
}

// SetHostname records the system hostname.
func (w *Writer) SetHostname(name string) error {
	if !validateHostname(name) {
		return fmt.Errorf("invalid hostname: %q", name)
	}
	w.mu.Lock()
	w.state.Hostname = name
	w.mu.Unlock()
	return w.flush()
}

// SetNTP records NTP server configuration.
func (w *Writer) SetNTP(servers []string) error {
	w.mu.Lock()
	w.state.NTPServers = servers
	w.mu.Unlock()
	return w.flush()
}

// SetFirewallPorts records allowed TCP/UDP ports.
// Pass the complete desired lists — this replaces the previous values entirely.
func (w *Writer) SetFirewallPorts(tcpPorts, udpPorts []int) error {
	w.mu.Lock()
	w.state.FirewallTCP = tcpPorts
	w.state.FirewallUDP = udpPorts
	w.mu.Unlock()
	return w.flush()
}

// SetTimezone records the system timezone.
func (w *Writer) SetTimezone(tz string) error {
	if strings.ContainsAny(tz, ";|&$`\\'\"") || len(tz) > 64 {
		return fmt.Errorf("invalid timezone: %q", tz)
	}
	w.mu.Lock()
	w.state.Timezone = tz
	w.mu.Unlock()
	return w.flush()
}

// ── Samba setter ─────────────────────────────────────────────────────────────

// SambaGlobalOpts holds global Samba settings. Identical to v4 struct.
type SambaGlobalOpts struct {
	Workgroup    string
	ServerString string
	TimeMachine  bool
	AllowGuest   bool
	ExtraGlobal  string
}

// SetSambaGlobals records global Samba settings.
func (w *Writer) SetSambaGlobals(opts SambaGlobalOpts) error {
	w.mu.Lock()
	w.state.SambaWorkgroup = opts.Workgroup
	w.state.SambaServerString = opts.ServerString
	w.state.SambaTimeMachine = opts.TimeMachine
	w.state.SambaAllowGuest = opts.AllowGuest
	w.state.SambaExtraGlobal = opts.ExtraGlobal
	w.mu.Unlock()
	return w.flush()
}

// ── Core write mechanics ─────────────────────────────────────────────────────

// flush serialises the current state to dplane-state.json atomically.
// On non-NixOS systems this is a deliberate no-op.
func (w *Writer) flush() error {
	if !w.nixOS {
		return nil
	}

	w.mu.Lock()
	data, err := json.MarshalIndent(w.state, "", "  ")
	w.mu.Unlock()
	if err != nil {
		return fmt.Errorf("nixwriter: marshal state: %w", err)
	}

	dir := filepath.Dir(w.path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("nixwriter: mkdir: %w", err)
	}

	// Atomic write via rename — no partial state ever visible on disk
	tmp, err := os.CreateTemp(dir, ".dplane-state-*.json.tmp")
	if err != nil {
		return fmt.Errorf("nixwriter: create tmp: %w", err)
	}
	tmpPath := tmp.Name()

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("nixwriter: write tmp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("nixwriter: close tmp: %w", err)
	}
	if err := os.Rename(tmpPath, w.path); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("nixwriter: rename: %w", err)
	}

	return nil
}

// ── Validation helpers ────────────────────────────────────────────────────────

func validateIface(s string) bool {
	if len(s) == 0 || len(s) > 16 {
		return false
	}
	for _, c := range s {
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') ||
			(c >= '0' && c <= '9') || c == '-' || c == '_' || c == '.') {
			return false
		}
	}
	return true
}

func validateIP(s string) bool {
	return net.ParseIP(s) != nil
}

func validateCIDR(s string) bool {
	_, _, err := net.ParseCIDR(s)
	return err == nil
}

func validateHostname(s string) bool {
	if len(s) == 0 || len(s) > 63 {
		return false
	}
	for _, c := range s {
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') ||
			(c >= '0' && c <= '9') || c == '-') {
			return false
		}
	}
	return true
}
