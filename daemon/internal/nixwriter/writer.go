// Package nixwriter is the store of the system settings made in the web
// interface: host name, time zone, NTP servers, firewall rules, kernel
// tuning, the web certificate, SSH daemon and UPS settings, Samba's
// directory membership.
//
// The settings live in one JSON file (StateJSONPath). Every setter writes the
// whole file atomically and then has the running system brought in line with
// it (the Apply hook, internal/sysapply); the same happens at every boot. An
// installed system is an image, so nothing here waits for a rebuild.
//
// (The name is historical: the file used to be read by a generated NixOS
// module at rebuild time.)
package nixwriter

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
)

const (
	// StateJSONPath is the settings file.
	StateJSONPath = "/var/lib/dplaneos/dplane-state.json"
)

// DPlaneState is the complete set of system settings, as stored.
//
// Unset values are left out of the file (omitempty).
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
	// FirewallRules are the rules that are more than an open port: allowed
	// from one source only, or denied. See FirewallList.
	FirewallRules []FirewallRule `json:"firewall_rules,omitempty"`
	// FirewallDisabled switches the firewall off altogether.
	FirewallDisabled bool `json:"firewall_disabled,omitempty"`

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

	// ── SSH Daemon ────────────────────────────────────────────────────────────
	// SSHPort overrides the sshd listen port. 0 = unset (NixOS default: 22).
	SSHPort int `json:"ssh_port,omitempty"`
	// SSHPasswordAuth controls PasswordAuthentication. nil = unset (NixOS default).
	SSHPasswordAuth *bool `json:"ssh_password_auth,omitempty"`
	// SSHPermitRootLogin maps to PermitRootLogin. "" = unset (NixOS default).
	// Valid values: "yes", "no", "prohibit-password", "forced-commands-only".
	SSHPermitRootLogin string `json:"ssh_permit_root_login,omitempty"`

	// ── High Availability ──────────────────────────────────────────────────────

	// ── Active Directory / IDMAP ───────────────────────────────────────────────
	// WinbindEnable activates the winbindd service and configures Samba for
	// ADS security mode. Set automatically when an AD domain is joined.
	WinbindEnable bool `json:"winbind_enable,omitempty"`
	// IDMAPDomains lists per-forest UID/GID range mappings. Each entry maps a
	// domain short name (or "*" for catch-all) to an idmap backend and range.
	// Drives "idmap config <name> : backend/range" lines in smb.conf.
	IDMAPDomains []IDMAPDomain `json:"idmap_domains,omitempty"`

	// ── Kernel tuning (Settings → Tuning) ──────────────────────────────────────
	// ZFSArcMax is the ARC size limit in bytes (boot.kernelParams zfs.zfs_arc_max).
	ZFSArcMax int64 `json:"zfs_arc_max,omitempty"`
	// Sysctl maps sysctl keys to values (boot.kernel.sysctl), e.g. vm.swappiness.
	Sysctl map[string]string `json:"sysctl,omitempty"`

	// ── Web UI TLS (Certificates → Activate) ───────────────────────────────────
	TLSCert string `json:"tls_cert,omitempty"`
	TLSKey  string `json:"tls_key,omitempty"`

	// ── UPS (UPS page): connection and shutdown policy, see UPSConfig ─────────
	UPSMode       string `json:"ups_mode,omitempty"` // "", usb, driver, remote
	UPSName       string `json:"ups_name,omitempty"`
	UPSDriver     string `json:"ups_driver,omitempty"`
	UPSPort       string `json:"ups_port,omitempty"`
	UPSHost       string `json:"ups_host,omitempty"`
	UPSUser       string `json:"ups_user,omitempty"`
	UPSPassword   string `json:"ups_password,omitempty"` // of the NUT server (remote); the file is root-only
	UPSFinalDelay int    `json:"ups_final_delay,omitempty"`
	UPSLowBattery int    `json:"ups_low_battery,omitempty"`
	UPSAction     string `json:"ups_action,omitempty"` // shutdown | hibernate
}

// IDMAPDomain describes one Active Directory forest's UID/GID mapping.
// Name is the domain short name (e.g. "CORP") or "*" for the catch-all backend.
// Backend must be one of: "rid", "ad", "tdb", "autorid".
type IDMAPDomain struct {
	Name    string `json:"name"`    // domain short name or "*"
	Backend string `json:"backend"` // rid, ad, tdb, autorid
	Low     int    `json:"low"`     // lowest UID/GID
	High    int    `json:"high"`    // highest UID/GID
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
	mu      sync.Mutex
	path    string
	nixOS   bool
	state   DPlaneState
	loadErr error // the state file exists but could not be read
}

// New returns a Writer targeting jsonPath.
// If the system is not NixOS, all writes are no-ops.
func New(jsonPath string) *Writer {
	return &Writer{
		path:  jsonPath,
		nixOS: isNixOS(),
	}
}

var (
	defaultOnce   sync.Once
	defaultWriter *Writer
)

// Default returns the process's one Writer for StateJSONPath, loaded from
// disk. There must be only one: every setter writes the whole state, so a
// second instance with its own (older or empty) copy would wipe what the
// first one saved.
func Default() *Writer {
	defaultOnce.Do(func() {
		defaultWriter = New(StateJSONPath)
		if err := defaultWriter.LoadFromDisk(); err != nil {
			// Refuse to write over a state file that cannot be read.
			defaultWriter.loadErr = err
		}
	})
	return defaultWriter
}

// DefaultWriter is Default.
func DefaultWriter() *Writer {
	return Default()
}

// Apply, when set, makes the running system match the state. It is called
// after every change has been written (internal/sysapply); its error is
// returned by the setter, so "saved but not in effect" is never reported as
// success.
var Apply func(DPlaneState) error

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
	for _, p := range append(append([]int(nil), tcpPorts...), udpPorts...) {
		if p < 1 || p > 65535 {
			return fmt.Errorf("port %d is out of range 1-65535", p)
		}
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	list := append([]FirewallRule(nil), w.state.FirewallRules...)
	for _, p := range tcpPorts {
		list = append(list, FirewallRule{Action: "allow", Proto: "tcp", Port: p})
	}
	for _, p := range udpPorts {
		list = append(list, FirewallRule{Action: "allow", Proto: "udp", Port: p})
	}
	w.setFirewallListLocked(list)
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

// SetZFSArcMax records the ARC size limit in bytes (0 clears it).
func (w *Writer) SetZFSArcMax(bytes int64) error {
	if bytes < 0 {
		return fmt.Errorf("invalid zfs_arc_max: %d", bytes)
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.state.ZFSArcMax = bytes
	return w.flushLocked()
}

var sysctlKeyRe = regexp.MustCompile(`^[a-z0-9_]+(\.[a-z0-9_-]+)+$`)
var sysctlValRe = regexp.MustCompile(`^[0-9A-Za-z_. -]{1,64}$`)

// SetSysctl records one sysctl value (an empty value removes the key).
func (w *Writer) SetSysctl(key, value string) error {
	if !sysctlKeyRe.MatchString(key) {
		return fmt.Errorf("invalid sysctl key: %q", key)
	}
	if value != "" && !sysctlValRe.MatchString(value) {
		return fmt.Errorf("invalid sysctl value for %s: %q", key, value)
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.state.Sysctl == nil {
		w.state.Sysctl = map[string]string{}
	}
	if value == "" {
		delete(w.state.Sysctl, key)
	} else {
		w.state.Sysctl[key] = value
	}
	return w.flushLocked()
}

// SetTLS records the certificate nginx serves the web UI with (empty clears it).
func (w *Writer) SetTLS(certPath, keyPath string) error {
	for _, p := range []string{certPath, keyPath} {
		if p != "" && (!strings.HasPrefix(p, "/") || strings.ContainsAny(p, ";|&$`\\'\" \n")) {
			return fmt.Errorf("invalid TLS path: %q", p)
		}
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.state.TLSCert, w.state.TLSKey = certPath, keyPath
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
	SecurityMode     string // "user" or "ads"; "" leaves mode, realm and controller as they are
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
	// The directory membership is only changed by a caller that names a
	// mode: saving the workgroup must not take a domain member out of its
	// domain.
	if opts.SecurityMode != "" {
		w.state.SambaSecurityMode = opts.SecurityMode
		w.state.SambaRealm = opts.Realm
		w.state.SambaDomainController = opts.DomainController
	}
	return w.flushLocked()
}

// ── HA setter ────────────────────────────────────────────────────────────────

// ── IDMAP / AD setter ─────────────────────────────────────────────────────────

var validIDMAPBackends = map[string]bool{
	"rid": true, "ad": true, "tdb": true, "autorid": true,
}

// SetIDMAP records winbind/IDMAP state for multi-forest Active Directory.
// enable should be true whenever any AD domain is joined.
// domains must contain at least one entry (typically a "*" catch-all plus one
// per joined forest). Duplicate Names are rejected.
func (w *Writer) SetIDMAP(enable bool, domains []IDMAPDomain) error {
	seen := make(map[string]bool, len(domains))
	for _, d := range domains {
		if d.Name == "" {
			return fmt.Errorf("IDMAP domain name cannot be empty")
		}
		if !validIDMAPBackends[d.Backend] {
			return fmt.Errorf("invalid IDMAP backend %q for domain %q", d.Backend, d.Name)
		}
		if d.Low < 0 || d.High <= d.Low {
			return fmt.Errorf("invalid IDMAP range [%d-%d] for domain %q", d.Low, d.High, d.Name)
		}
		if seen[d.Name] {
			return fmt.Errorf("duplicate IDMAP domain %q", d.Name)
		}
		seen[d.Name] = true
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.state.WinbindEnable = enable
	w.state.IDMAPDomains = domains
	return w.flushLocked()
}

// ── SSH Daemon setter ─────────────────────────────────────────────────────────

// SSHDaemonOpts holds SSH daemon NixOS settings.
type SSHDaemonOpts struct {
	// Port: 0 means "leave at NixOS default" (removes the field from JSON).
	Port int
	// PasswordAuth: nil means "leave at NixOS default".
	PasswordAuth *bool
	// PermitRootLogin: "" means "leave at NixOS default".
	// Valid: "yes", "no", "prohibit-password", "forced-commands-only".
	PermitRootLogin string
}

var validPermitRootLogin = map[string]bool{
	"":                     true,
	"yes":                  true,
	"no":                   true,
	"prohibit-password":    true,
	"forced-commands-only": true,
}

// SetSSHDaemon records SSH daemon settings. Invalid values are rejected.
func (w *Writer) SetSSHDaemon(opts SSHDaemonOpts) error {
	if opts.Port != 0 && (opts.Port < 1 || opts.Port > 65535) {
		return fmt.Errorf("ssh port %d out of range 1-65535", opts.Port)
	}
	if !validPermitRootLogin[opts.PermitRootLogin] {
		return fmt.Errorf("invalid PermitRootLogin value %q", opts.PermitRootLogin)
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.state.SSHPort = opts.Port
	w.state.SSHPasswordAuth = opts.PasswordAuth
	w.state.SSHPermitRootLogin = opts.PermitRootLogin
	return w.flushLocked()
}

// ── Core write mechanics ─────────────────────────────────────────────────────

func (w *Writer) flushLocked() error {
	if !w.nixOS {
		return nil
	}
	if w.loadErr != nil {
		return fmt.Errorf("the saved system settings (%s) cannot be read, not overwriting them: %w", w.path, w.loadErr)
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

	if Apply != nil && w == defaultWriter {
		if err := Apply(w.state); err != nil {
			return fmt.Errorf("saved, but not in effect yet: %w", err)
		}
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

func boolPtrEqual(a, b *bool) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	return *a == *b
}
