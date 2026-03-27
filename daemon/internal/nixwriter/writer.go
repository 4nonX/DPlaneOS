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
//     This is pure JSON produced by encoding/json.Marshal - 100% safe by construction.
//   - /etc/nixos/dplane-generated.nix is a STATIC file installed once by
//     setup-nixos.sh. It never changes. It contains:
//     let s = builtins.fromJSON (builtins.readFile /var/lib/dplaneos/dplane-state.json);
//     and maps JSON keys to NixOS module options using `s.key or default` guards.
//   - Because the .nix file is static and contains no interpolated values,
//     there is NO syntax error risk from daemon writes, ever.
//
// Caller API is identical to v4 - handlers call Set*() methods and never
// know or care whether a .nix file or a .json file is being written.
package nixwriter
 
import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"crypto/sha256"
)
 
const (
	// StateJSONPath is the only file the daemon ever writes for NixOS state.
	// The static dplane-generated.nix reads this file via builtins.fromJSON.
	StateJSONPath = "/var/lib/dplaneos/dplane-state.json"
 
	// GeneratedNixPath is where setup-nixos.sh installs the static bridge file.
	// The daemon does NOT write to this path in v5.
	GeneratedNixPath = "/etc/nixos/dplane-generated.nix"

	// AppliedChecksumPath stores the SHA256 of the dplane-state.json that
	// was last successfully applied via nixos-rebuild.
	AppliedChecksumPath = "/var/lib/dplaneos/applied-checksum"

	// AppliedStatePath stores a copy of the dplane-state.json that was last applied.
	AppliedStatePath = "/var/lib/dplaneos/dplane-state.applied.json"
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
	SambaWorkgroup        string `json:"samba_workgroup,omitempty"`
	SambaServerString     string `json:"samba_server_string,omitempty"`
	SambaTimeMachine      bool   `json:"samba_time_machine,omitempty"`
	SambaAllowGuest       bool   `json:"samba_allow_guest,omitempty"`
	SambaExtraGlobal      string `json:"samba_extra_global,omitempty"`
	SambaSecurityMode     string `json:"samba_security_mode,omitempty"`
	SambaRealm            string `json:"samba_realm,omitempty"`
	SambaDomainController string `json:"samba_domain_controller,omitempty"`
 
	// ── High Availability ──────────────────────────────────────────────────────
	HAEnable bool `json:"ha_enable,omitempty"`
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
// On non-NixOS systems all writes are no-ops - safe to use everywhere.
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

// IsDirty checks if the intent (json on disk) is ahead of the applied state.
func (w *Writer) IsDirty() (bool, error) {
	if !w.nixOS {
		return false, nil
	}

	// 1. Get checksum of current state file
	currentJSON, err := os.ReadFile(w.path)
	if os.IsNotExist(err) {
		return false, nil // no state = not dirty
	}
	if err != nil {
		return false, fmt.Errorf("read state: %w", err)
	}
	currentCS := fmt.Sprintf("%x", sha256.Sum256(currentJSON))

	// 2. Get checksum of last applied state
	appliedCS, err := os.ReadFile(AppliedChecksumPath)
	if os.IsNotExist(err) {
		return true, nil // has state but no applied record = dirty
	}
	if err != nil {
		return false, fmt.Errorf("read applied-checksum: %w", err)
	}

	return currentCS != strings.TrimSpace(string(appliedCS)), nil
}

// MarkApplied records that the current state on disk has been successfully
// activated by nixos-rebuild.
func (w *Writer) MarkApplied() error {
	if !w.nixOS {
		return nil
	}

	currentJSON, err := os.ReadFile(w.path)
	if err != nil {
		return fmt.Errorf("read state for marking: %w", err)
	}
	currentCS := fmt.Sprintf("%x", sha256.Sum256(currentJSON))

	if err := os.WriteFile(AppliedChecksumPath, []byte(currentCS), 0644); err != nil {
		return err
	}

	// Also save a copy of the state itself for diff purposes
	return os.WriteFile(AppliedStatePath, currentJSON, 0644)
}

// Change represents a single property change in the declarative state.
type Change struct {
	Path string      `json:"path"`
	From interface{} `json:"from"`
	To   interface{} `json:"to"`
	Op   string      `json:"op"` // "add", "remove", "modify"
}

// DiffIntent calculates the differences between the current in-memory state
// and the last applied state on disk.
func (w *Writer) DiffIntent() ([]Change, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	var appliedState DPlaneState
	data, err := os.ReadFile(AppliedStatePath)
	if err == nil {
		if err := json.Unmarshal(data, &appliedState); err != nil {
			return nil, fmt.Errorf("diff: unmarshal applied state: %v", err)
		}
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("diff: read applied state: %v", err)
	}
	// if file doesn't exist, appliedState is empty - we'll show everything as added

	var changes []Change

	// Helper to compare integer slices (Firewall)
	compareInts := func(path string, from, to []int) {
		fMap := make(map[int]bool)
		for _, x := range from { fMap[x] = true }
		tMap := make(map[int]bool)
		for _, x := range to { tMap[x] = true }

		for x := range tMap {
			if !fMap[x] {
				changes = append(changes, Change{Path: path, To: x, Op: "add"})
			}
		}
		for x := range fMap {
			if !tMap[x] {
				changes = append(changes, Change{Path: path, From: x, Op: "remove"})
			}
		}
	}

	// System
	if w.state.Hostname != appliedState.Hostname {
		changes = append(changes, Change{Path: "system.hostname", From: appliedState.Hostname, To: w.state.Hostname, Op: "modify"})
	}
	if w.state.Timezone != appliedState.Timezone {
		changes = append(changes, Change{Path: "system.timezone", From: appliedState.Timezone, To: w.state.Timezone, Op: "modify"})
	}

	// Firewall
	compareInts("firewall.tcp", appliedState.FirewallTCP, w.state.FirewallTCP)
	compareInts("firewall.udp", appliedState.FirewallUDP, w.state.FirewallUDP)

	// High Availability
	if w.state.HAEnable != appliedState.HAEnable {
		changes = append(changes, Change{Path: "system.ha_enable", From: appliedState.HAEnable, To: w.state.HAEnable, Op: "modify"})
	}

	// This list is not exhaustive but covers the primary enterprise hardening vectors.
	// For a production system, use reflection or a generic JSON diff library.

	return changes, nil
}
 
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
	defer w.mu.Unlock()
	if w.state.NetworkStatics == nil {
		w.state.NetworkStatics = make(map[string]NetworkStaticEntry)
	}
	if cidr == "" {
		delete(w.state.NetworkStatics, iface)
	} else {
		w.state.NetworkStatics[iface] = NetworkStaticEntry{CIDR: cidr, Gateway: gateway}
	}
	return w.flushLocked()
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
	defer w.mu.Unlock()
	if w.state.NetworkBonds == nil {
		w.state.NetworkBonds = make(map[string]NetworkBondEntry)
	}
	w.state.NetworkBonds[name] = NetworkBondEntry{Slaves: slaves, Mode: mode}
	return w.flushLocked()
}
 
// RemoveBond removes a bond interface configuration.
func (w *Writer) RemoveBond(name string) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	delete(w.state.NetworkBonds, name)
	return w.flushLocked()
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
	defer w.mu.Unlock()
	if w.state.NetworkVLANs == nil {
		w.state.NetworkVLANs = make(map[string]NetworkVLANEntry)
	}
	w.state.NetworkVLANs[ifName] = NetworkVLANEntry{Parent: parent, VID: vid}
	return w.flushLocked()
}
 
// RemoveVLAN removes a VLAN sub-interface configuration.
func (w *Writer) RemoveVLAN(ifName string) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	delete(w.state.NetworkVLANs, ifName)
	return w.flushLocked()
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
	defer w.mu.Unlock()
	sort.Strings(servers)
	w.state.DNSServers = servers
	return w.flushLocked()
}
 
// SetHostname records the system hostname.
func (w *Writer) SetHostname(name string) error {
	if !validateHostname(name) {
		return fmt.Errorf("invalid hostname: %q", name)
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.state.Hostname = name
	return w.flushLocked()
}
 
// SetNTP records NTP server configuration.
func (w *Writer) SetNTP(servers []string) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	sort.Strings(servers)
	w.state.NTPServers = servers
	return w.flushLocked()
}
 
// SetFirewallPorts records allowed TCP/UDP ports.
// Pass the complete desired lists - this replaces the previous values entirely.
func (w *Writer) SetFirewallPorts(tcpPorts, udpPorts []int) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	sort.Ints(tcpPorts)
	sort.Ints(udpPorts)
	w.state.FirewallTCP = tcpPorts
	w.state.FirewallUDP = udpPorts
	return w.flushLocked()
}
 
// SetTimezone records the system timezone.
func (w *Writer) SetTimezone(tz string) error {
	if strings.ContainsAny(tz, ";|&$`\\'\"") || len(tz) > 64 {
		return fmt.Errorf("invalid timezone: %q", tz)
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.state.Timezone = tz
	return w.flushLocked()
}
 
// ── Samba setter ─────────────────────────────────────────────────────────────
 
// SambaGlobalOpts holds global Samba settings.
type SambaGlobalOpts struct {
	Workgroup        string
	ServerString     string
	TimeMachine      bool
	AllowGuest       bool
	ExtraGlobal      string
	SecurityMode     string // "user" or "ads"
	Realm            string
	DomainController string
}
 
// SetSambaGlobals records global Samba settings.
func (w *Writer) SetSambaGlobals(opts SambaGlobalOpts) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.state.SambaWorkgroup = opts.Workgroup
	w.state.SambaServerString = opts.ServerString
	w.state.SambaTimeMachine = opts.TimeMachine
	w.state.SambaAllowGuest = opts.AllowGuest
	w.state.SambaExtraGlobal = opts.ExtraGlobal
	w.state.SambaSecurityMode = opts.SecurityMode
	w.state.SambaRealm = opts.Realm
	w.state.SambaDomainController = opts.DomainController
	return w.flushLocked()
}
 
// ── HA setter ────────────────────────────────────────────────────────────────
 
// SetHA enables or disables the Patroni/HAProxy High Availability stack.
func (w *Writer) SetHA(enable bool) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.state.HAEnable = enable
	return w.flushLocked()
}
 
// ── Core write mechanics ─────────────────────────────────────────────────────
 
// flush serialises the current state to dplane-state.json atomically.
// On non-NixOS systems this is a deliberate no-op.
func (w *Writer) flush() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.flushLocked()
}
 
func (w *Writer) flushLocked() error {
	if !w.nixOS {
		return nil
	}
 
	data, err := json.MarshalIndent(w.state, "", "  ")
	if err != nil {
		return fmt.Errorf("nixwriter: marshal state: %w", err)
	}
 
	dir := filepath.Dir(w.path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("nixwriter: mkdir: %w", err)
	}
 
	// Atomic write via rename - no partial state ever visible on disk
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
 
	// fsync for reliability
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("nixwriter: sync tmp: %w", err)
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
