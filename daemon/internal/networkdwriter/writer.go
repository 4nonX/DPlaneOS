// Package networkdwriter keeps DPlaneOS's network configuration as
// systemd-networkd files in /etc/systemd/network.
//
// The desired configuration is one State (interfaces, bonds, VLANs, DNS). It
// is saved as JSON and rendered into one .network file per interface it
// touches (address or DHCP, gateway, routes, its VLANs, or the bond it is part
// of) and one .netdev file per bond or VLAN. Every change renders the whole
// state, so a rollback is applying the previous state again.
//
// networkd applies the FIRST .network file (in lexical order) that matches an
// interface and ignores the rest. The files are named 05-dplane-<name>, so
// they come before the distribution's (NixOS 10-<name>, its 99- DHCP
// fallback, netplan's 10-netplan-*). An interface has exactly one DPlaneOS
// file: its VLANs and its bond membership go into that file, not into extra
// files that would replace its address configuration.
//
// networkd creates missing netdevs on reload but never changes or removes an
// existing one, so a bond or VLAN that is removed or changed is deleted here
// first and created again from its new file.
package networkdwriter

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	// DefaultNetworkDir is where systemd-networkd reads configuration.
	DefaultNetworkDir = "/etc/systemd/network"

	// FilePrefix sorts before the distribution's files (see the package doc).
	FilePrefix = "05-dplane-"

	// legacyPrefix: files of earlier versions (one file per feature); they
	// sorted after the distribution's files and were ignored by networkd for
	// any interface the distribution configured. Imported once, then removed.
	legacyPrefix = "50-dplane-"

	// DefaultStatePath holds the desired State as JSON.
	DefaultStatePath = "/var/lib/dplaneos/network-state.json"

	// DefaultResolvedDropin sets the global DNS servers and search domains.
	DefaultResolvedDropin = "/etc/systemd/resolved.conf.d/50-dplane.conf"
)

// Route is a static route on an interface.
type Route struct {
	Destination string `json:"destination"` // CIDR, e.g. 10.20.0.0/16
	Gateway     string `json:"gateway"`
}

// Iface is the address configuration of one interface.
type Iface struct {
	DHCP      bool     `json:"dhcp,omitempty"`
	Addresses []string `json:"addresses,omitempty"` // CIDR
	Gateway   string   `json:"gateway,omitempty"`
	Routes    []Route  `json:"routes,omitempty"`
}

// Bond is a bond device and its member interfaces.
type Bond struct {
	Mode   string   `json:"mode"`
	Slaves []string `json:"slaves"`
}

// VLAN is an 802.1Q device on a parent interface.
type VLAN struct {
	Parent string `json:"parent"`
	ID     int    `json:"id"`
}

// DNS is the global resolver configuration (systemd-resolved).
type DNS struct {
	Servers []string `json:"servers,omitempty"`
	Search  []string `json:"search,omitempty"`
}

// State is DPlaneOS's whole network configuration. Interfaces that appear
// nowhere in it keep the distribution's configuration.
type State struct {
	Interfaces map[string]Iface `json:"interfaces,omitempty"`
	Bonds      map[string]Bond  `json:"bonds,omitempty"`
	VLANs      map[string]VLAN  `json:"vlans,omitempty"`
	DNS        DNS              `json:"dns"`
}

// Clone returns a deep copy.
func (s State) Clone() State {
	var c State
	b, _ := json.Marshal(s)
	_ = json.Unmarshal(b, &c)
	return c
}

// BondOf returns the bond an interface is a member of, or "".
func (s State) BondOf(iface string) string {
	for name, b := range s.Bonds {
		if slices.Contains(b.Slaves, iface) {
			return name
		}
	}
	return ""
}

// Mentions reports whether the state configures the interface in any way.
func (s State) Mentions(iface string) bool {
	if _, ok := s.Interfaces[iface]; ok {
		return true
	}
	if _, ok := s.Bonds[iface]; ok {
		return true
	}
	if _, ok := s.VLANs[iface]; ok {
		return true
	}
	if s.BondOf(iface) != "" {
		return true
	}
	for _, v := range s.VLANs {
		if v.Parent == iface {
			return true
		}
	}
	return false
}

var bondModes = map[string]bool{
	"balance-rr": true, "active-backup": true, "balance-xor": true, "broadcast": true,
	"802.3ad": true, "balance-tlb": true, "balance-alb": true,
}

// Validate checks the state for values networkd would reject and for
// combinations that cannot work.
func (s State) Validate() error {
	for name, ifc := range s.Interfaces {
		if err := ValidateIface(name); err != nil {
			return err
		}
		for _, a := range ifc.Addresses {
			if _, _, err := net.ParseCIDR(a); err != nil {
				return fmt.Errorf("%s: address %q: use address/prefix, e.g. 192.168.1.10/24", name, a)
			}
		}
		if ifc.Gateway != "" && net.ParseIP(ifc.Gateway) == nil {
			return fmt.Errorf("%s: invalid gateway %q", name, ifc.Gateway)
		}
		for _, r := range ifc.Routes {
			if _, _, err := net.ParseCIDR(r.Destination); err != nil {
				return fmt.Errorf("%s: route destination %q: use network/prefix", name, r.Destination)
			}
			if net.ParseIP(r.Gateway) == nil {
				return fmt.Errorf("%s: route gateway %q is not an IP address", name, r.Gateway)
			}
		}
		if b := s.BondOf(name); b != "" && (ifc.DHCP || len(ifc.Addresses) > 0 || ifc.Gateway != "" || len(ifc.Routes) > 0) {
			return fmt.Errorf("%s is part of bond %s: configure the address on %s", name, b, b)
		}
	}
	member := map[string]string{}
	for name, b := range s.Bonds {
		if err := ValidateIface(name); err != nil {
			return err
		}
		if !bondModes[b.Mode] {
			return fmt.Errorf("bond %s: unknown mode %q", name, b.Mode)
		}
		if len(b.Slaves) == 0 {
			return fmt.Errorf("bond %s needs at least one interface", name)
		}
		for _, sl := range b.Slaves {
			if err := ValidateIface(sl); err != nil {
				return err
			}
			if other, ok := member[sl]; ok {
				return fmt.Errorf("%s is in bonds %s and %s", sl, other, name)
			}
			member[sl] = name
			if _, ok := s.Bonds[sl]; ok {
				return fmt.Errorf("bond %s cannot contain bond %s", name, sl)
			}
			if _, ok := s.VLANs[sl]; ok {
				return fmt.Errorf("bond %s cannot contain VLAN %s", name, sl)
			}
		}
	}
	ids := map[string]string{}
	for name, v := range s.VLANs {
		if err := ValidateIface(name); err != nil {
			return err
		}
		if err := ValidateIface(v.Parent); err != nil {
			return fmt.Errorf("VLAN %s parent: %w", name, err)
		}
		if v.ID < 1 || v.ID > 4094 {
			return fmt.Errorf("VLAN %s: ID %d out of range 1-4094", name, v.ID)
		}
		if b := s.BondOf(v.Parent); b != "" {
			return fmt.Errorf("VLAN %s: %s is part of bond %s; put the VLAN on %s", name, v.Parent, b, b)
		}
		if _, ok := s.Bonds[name]; ok {
			return fmt.Errorf("%s is both a bond and a VLAN", name)
		}
		key := fmt.Sprintf("%s/%d", v.Parent, v.ID)
		if other, ok := ids[key]; ok {
			return fmt.Errorf("VLAN %d on %s exists twice (%s, %s)", v.ID, v.Parent, other, name)
		}
		ids[key] = name
	}
	for _, d := range s.DNS.Servers {
		if net.ParseIP(d) == nil {
			return fmt.Errorf("invalid DNS server %q", d)
		}
	}
	for _, d := range s.DNS.Search {
		if !validDomain(d) {
			return fmt.Errorf("invalid search domain %q", d)
		}
	}
	return nil
}

func validDomain(d string) bool {
	if d == "" || len(d) > 253 {
		return false
	}
	for _, l := range strings.Split(strings.TrimSuffix(d, "."), ".") {
		if l == "" || len(l) > 63 {
			return false
		}
		for _, c := range l {
			if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
				return false
			}
		}
	}
	return true
}

// render returns the networkd files of the state (file name → content).
func (s State) render() map[string]string {
	files := map[string]string{}
	names := map[string]bool{}
	for n := range s.Interfaces {
		names[n] = true
	}
	for n, b := range s.Bonds {
		names[n] = true
		for _, sl := range b.Slaves {
			names[sl] = true
		}
		files[FilePrefix+n+".netdev"] = "# Managed by DPlaneOS - do not edit\n\n[NetDev]\nName=" + n + "\nKind=bond\n\n[Bond]\nMode=" + b.Mode +
			"\n# Without link monitoring a failed member is never noticed.\nMIIMonitorSec=100ms\n"
	}
	vlansOn := map[string][]string{}
	for n, v := range s.VLANs {
		names[n] = true
		names[v.Parent] = true
		vlansOn[v.Parent] = append(vlansOn[v.Parent], n)
		files[FilePrefix+n+".netdev"] = fmt.Sprintf("# Managed by DPlaneOS - do not edit\n\n[NetDev]\nName=%s\nKind=vlan\n\n[VLAN]\nId=%d\n", n, v.ID)
	}
	for n := range names {
		var b strings.Builder
		b.WriteString("# Managed by DPlaneOS - do not edit\n\n[Match]\nName=" + n + "\n\n[Network]\n")
		if bond := s.BondOf(n); bond != "" {
			b.WriteString("Bond=" + bond + "\n")
			files[FilePrefix+n+".network"] = b.String()
			continue
		}
		ifc := s.Interfaces[n]
		if ifc.DHCP {
			b.WriteString("DHCP=yes\n")
		} else {
			b.WriteString("DHCP=no\n")
		}
		for _, a := range ifc.Addresses {
			b.WriteString("Address=" + a + "\n")
		}
		if ifc.Gateway != "" {
			b.WriteString("Gateway=" + ifc.Gateway + "\n")
		}
		vl := vlansOn[n]
		sort.Strings(vl)
		for _, v := range vl {
			b.WriteString("VLAN=" + v + "\n")
		}
		for _, r := range ifc.Routes {
			b.WriteString("\n[Route]\nDestination=" + r.Destination + "\nGateway=" + r.Gateway + "\n")
		}
		files[FilePrefix+n+".network"] = b.String()
	}
	return files
}

func (d DNS) render() string {
	if len(d.Servers) == 0 && len(d.Search) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("# Managed by DPlaneOS - do not edit\n\n[Resolve]\n")
	if len(d.Servers) > 0 {
		b.WriteString("DNS=" + strings.Join(d.Servers, " ") + "\n")
	}
	if len(d.Search) > 0 {
		b.WriteString("Domains=" + strings.Join(d.Search, " ") + "\n")
	}
	return b.String()
}

// Writer applies States to the system.
type Writer struct {
	mu           sync.Mutex
	dir          string
	statePath    string
	resolvedPath string
	networkdUp   bool

	// System access; replaced in tests.
	run        func(name string, args ...string) ([]byte, error)
	linkExists func(name string) bool
	linkDel    func(name string) error

	state  State
	loaded bool
}

// New returns a Writer for the given networkd directory with the default
// state and resolved paths.
func New(dir string) *Writer {
	return &Writer{
		dir:          dir,
		statePath:    DefaultStatePath,
		resolvedPath: DefaultResolvedDropin,
		networkdUp:   isServiceActive("systemd-networkd"),
		run: func(name string, args ...string) ([]byte, error) {
			return exec.Command(name, args...).CombinedOutput()
		},
		linkExists: func(name string) bool { _, err := net.InterfaceByName(name); return err == nil },
		linkDel: func(name string) error {
			if out, err := exec.Command("ip", "link", "delete", name).CombinedOutput(); err != nil {
				return fmt.Errorf("ip link delete %s: %v (%s)", name, err, strings.TrimSpace(string(out)))
			}
			return nil
		},
	}
}

var (
	sharedOnce sync.Once
	shared     *Writer
)

// Default returns the process-wide Writer for the standard paths. There is
// one, so the API and GitOps work on the same state.
func Default() *Writer {
	sharedOnce.Do(func() { shared = New(DefaultNetworkDir) })
	return shared
}

// IsNetworkd reports whether systemd-networkd is the active network manager.
func (w *Writer) IsNetworkd() bool { return w.networkdUp }

// State returns a copy of the current desired state.
func (w *Writer) State() State {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.load()
	return w.state.Clone()
}

func (w *Writer) load() {
	if w.loaded {
		return
	}
	w.loaded = true
	b, err := os.ReadFile(w.statePath)
	if err == nil {
		if err := json.Unmarshal(b, &w.state); err != nil {
			log.Printf("[networkdwriter] %s unreadable (%v); starting empty", w.statePath, err)
		}
		return
	}
	w.state = importLegacy(w.dir)
	if len(w.state.Interfaces)+len(w.state.Bonds)+len(w.state.VLANs) > 0 {
		log.Printf("[networkdwriter] imported the configuration of earlier versions from %s", w.dir)
	}
}

// Apply makes next the configuration: writes its files, removes files it no
// longer has, deletes bonds and VLANs that were removed or changed, saves the
// state and has networkd and resolved apply it.
func (w *Writer) Apply(next State) error {
	if err := next.Validate(); err != nil {
		return err
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.load()
	return w.apply(w.state, next)
}

// Sync writes the files of the saved state again where they are missing or
// differ (an OTA update replaces /etc outside the persisted network
// directory, e.g. the resolved drop-in) and converts the files of earlier
// versions. Run at daemon start.
func (w *Writer) Sync() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.load()
	return w.apply(w.state, w.state)
}

func (w *Writer) apply(prev, next State) error {
	files := next.render()
	old := map[string]string{}
	entries, err := os.ReadDir(w.dir)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	for _, e := range entries {
		n := e.Name()
		if strings.HasPrefix(n, FilePrefix) || strings.HasPrefix(n, legacyPrefix) {
			b, _ := os.ReadFile(filepath.Join(w.dir, n))
			old[n] = string(b)
		}
	}

	// Interfaces whose configuration changes: reconfigured after the reload.
	affected := map[string]bool{}
	ifaceOf := func(file string) string {
		n := strings.TrimPrefix(strings.TrimPrefix(file, FilePrefix), legacyPrefix)
		return strings.TrimSuffix(strings.TrimSuffix(n, ".network"), ".netdev")
	}
	for f, c := range files {
		if old[f] != c {
			affected[ifaceOf(f)] = true
		}
	}
	for f := range old {
		if _, ok := files[f]; !ok {
			affected[ifaceOf(f)] = true
		}
	}

	// Netdevs removed or changed: networkd does not touch an existing device.
	// The members of a recreated bond and the parent of a recreated VLAN are
	// reconfigured too: their files did not change, but they must join the
	// new device.
	var errs []error
	recreate := false
	for _, dev := range changedDevices(prev, next) {
		_, isBond := next.Bonds[dev]
		_, isVLAN := next.VLANs[dev]
		recreate = recreate || isBond || isVLAN
		if w.linkExists(dev) {
			if err := w.linkDel(dev); err != nil {
				errs = append(errs, err)
			}
		}
		for _, sl := range next.Bonds[dev].Slaves {
			affected[sl] = true
		}
		if v, ok := next.VLANs[dev]; ok {
			affected[v.Parent] = true
		}
	}

	if err := os.MkdirAll(w.dir, 0o755); err != nil {
		return fmt.Errorf("networkdwriter: %w", err)
	}
	for f, c := range files {
		if old[f] == c {
			continue
		}
		if err := atomicWrite(filepath.Join(w.dir, f), c, 0o644); err != nil {
			errs = append(errs, err)
		}
	}
	for f := range old {
		if _, ok := files[f]; !ok {
			if err := os.Remove(filepath.Join(w.dir, f)); err != nil && !os.IsNotExist(err) {
				errs = append(errs, err)
			}
		}
	}
	if err := w.saveState(next); err != nil {
		errs = append(errs, err)
	}
	w.state = next.Clone()
	if err := w.applyDNS(next.DNS); err != nil {
		errs = append(errs, err)
	}

	// A device deleted here and defined again (bond mode, VLAN ID): after a
	// reload networkd creates it, but its members stay "configuring" and never
	// join it (systemd 255). A restart of networkd sets everything up from
	// the files; addresses on other interfaces are kept across the restart.
	reloadCmd := []string{"networkctl", "reload"}
	if recreate {
		reloadCmd = []string{"systemctl", "restart", "systemd-networkd.service"}
	}
	if w.networkdUp && (len(affected) > 0 || recreate) {
		if out, err := w.run(reloadCmd[0], reloadCmd[1:]...); err != nil {
			errs = append(errs, fmt.Errorf("%s: %v (%s)", strings.Join(reloadCmd, " "), err, strings.TrimSpace(string(out))))
		} else {
			// networkd creates bonds asynchronously after the reload; a
			// member reconfigured before its bond exists does not join it.
			for i := 0; i < 50; i++ {
				missing := false
				for n := range next.Bonds {
					if !w.linkExists(n) {
						missing = true
					}
				}
				if !missing {
					break
				}
				time.Sleep(100 * time.Millisecond)
			}
			var links []string
			for n := range affected {
				if w.linkExists(n) {
					links = append(links, n)
				}
			}
			sort.Strings(links)
			if len(links) > 0 {
				if out, err := w.run("networkctl", append([]string{"reconfigure"}, links...)...); err != nil {
					errs = append(errs, fmt.Errorf("networkctl reconfigure: %v (%s)", err, strings.TrimSpace(string(out))))
				}
			}
		}
	}
	return errors.Join(errs...)
}

// changedDevices: bonds and VLANs of prev that next removes or defines
// differently (mode, ID, parent, or a VLAN that turned into a bond).
func changedDevices(prev, next State) []string {
	var out []string
	for n, b := range prev.Bonds {
		if nb, ok := next.Bonds[n]; !ok || nb.Mode != b.Mode {
			out = append(out, n)
		}
	}
	for n, v := range prev.VLANs {
		if nv, ok := next.VLANs[n]; !ok || nv != v {
			out = append(out, n)
		}
	}
	sort.Strings(out)
	return out
}

func (w *Writer) saveState(s State) error {
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(w.statePath), 0o755); err != nil {
		return err
	}
	return atomicWrite(w.statePath, string(b)+"\n", 0o600)
}

// applyDNS writes (or removes) the resolved drop-in and restarts resolved when
// it changed. systemd-resolved has no reload in most versions, so "systemctl
// reload" failed and the servers were never used.
func (w *Writer) applyDNS(d DNS) error {
	content := d.render()
	cur, err := os.ReadFile(w.resolvedPath)
	exists := err == nil
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if (content == "" && !exists) || (exists && string(cur) == content) {
		return nil
	}
	if content == "" {
		if err := os.Remove(w.resolvedPath); err != nil && !os.IsNotExist(err) {
			return err
		}
	} else {
		if err := os.MkdirAll(filepath.Dir(w.resolvedPath), 0o755); err != nil {
			return err
		}
		if err := atomicWrite(w.resolvedPath, content, 0o644); err != nil {
			return err
		}
	}
	if out, err := w.run("systemctl", "try-restart", "systemd-resolved.service"); err != nil {
		return fmt.Errorf("restart systemd-resolved: %v (%s)", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// ── Pending changes ───────────────────────────────────────────────────────────
// A change is applied at once and kept only when confirmed. The previous state
// is saved next to the state file until then, so an unconfirmed change is
// also reverted when the daemon restarts in between (or the machine reboots:
// networkd brings up the new configuration, the daemon reverts it at start).

type pending struct {
	Previous State     `json:"previous"`
	Deadline time.Time `json:"deadline"`
}

func (w *Writer) pendingPath() string { return w.statePath + ".pending" }

// ApplyPending applies next and records the current state to revert to. It
// fails when a change is already waiting for confirmation.
func (w *Writer) ApplyPending(next State, deadline time.Time) error {
	if err := next.Validate(); err != nil {
		return err
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.load()
	if _, err := os.Stat(w.pendingPath()); err == nil {
		return ErrPending
	}
	b, err := json.Marshal(pending{Previous: w.state, Deadline: deadline})
	if err != nil {
		return err
	}
	if err := atomicWrite(w.pendingPath(), string(b), 0o600); err != nil {
		return err
	}
	if err := w.apply(w.state, next); err != nil {
		// Partly applied: go back now instead of waiting for the timeout.
		var p pending
		_ = json.Unmarshal(b, &p)
		if rerr := w.apply(w.state, p.Previous); rerr != nil {
			log.Printf("[networkdwriter] revert after failed apply: %v", rerr)
		}
		os.Remove(w.pendingPath())
		return err
	}
	return nil
}

// ErrPending: another change waits for confirmation.
var ErrPending = errors.New("another network change is waiting for confirmation; confirm it or wait until it is reverted")

// HasPending reports whether a change waits for confirmation, and until when.
func (w *Writer) HasPending() (bool, time.Time) {
	b, err := os.ReadFile(w.pendingPath())
	if err != nil {
		return false, time.Time{}
	}
	var p pending
	_ = json.Unmarshal(b, &p)
	return true, p.Deadline
}

// Confirm keeps the pending change.
func (w *Writer) Confirm() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := os.Remove(w.pendingPath()); err != nil {
		if os.IsNotExist(err) {
			return ErrNothingPending
		}
		return err
	}
	return nil
}

// ErrNothingPending: there is no change to confirm (it may have been reverted).
var ErrNothingPending = errors.New("no network change is waiting for confirmation (it may already have been reverted)")

// RevertPending applies the state from before the pending change.
func (w *Writer) RevertPending() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	b, err := os.ReadFile(w.pendingPath())
	if err != nil {
		if os.IsNotExist(err) {
			return ErrNothingPending
		}
		return err
	}
	var p pending
	if err := json.Unmarshal(b, &p); err != nil {
		os.Remove(w.pendingPath())
		return fmt.Errorf("pending change unreadable: %w", err)
	}
	w.load()
	err = w.apply(w.state, p.Previous)
	os.Remove(w.pendingPath())
	return err
}

// ── Live configuration ────────────────────────────────────────────────────────

// LiveConfig reads how an interface is configured now (for an interface the
// state does not have yet, e.g. the parent of a first VLAN): DHCP when it has
// a dynamic address, else its global addresses and the default gateway on it.
func (w *Writer) LiveConfig(iface string) (Iface, error) {
	out, err := w.run("ip", "-j", "addr", "show", "dev", iface)
	if err != nil {
		return Iface{}, fmt.Errorf("ip addr show %s: %v (%s)", iface, err, strings.TrimSpace(string(out)))
	}
	var links []struct {
		AddrInfo []struct {
			Family    string `json:"family"`
			Local     string `json:"local"`
			Prefixlen int    `json:"prefixlen"`
			Scope     string `json:"scope"`
			Dynamic   bool   `json:"dynamic"`
		} `json:"addr_info"`
	}
	if err := json.Unmarshal(out, &links); err != nil {
		return Iface{}, fmt.Errorf("ip addr show %s: %w", iface, err)
	}
	var ifc Iface
	for _, l := range links {
		for _, a := range l.AddrInfo {
			if a.Scope != "global" {
				continue
			}
			if a.Dynamic {
				ifc.DHCP = true
				continue
			}
			ifc.Addresses = append(ifc.Addresses, fmt.Sprintf("%s/%d", a.Local, a.Prefixlen))
		}
	}
	if len(ifc.Addresses) > 0 {
		if out, err := w.run("ip", "-j", "route", "show", "default", "dev", iface); err == nil {
			var routes []struct {
				Gateway  string `json:"gateway"`
				Protocol string `json:"protocol"`
			}
			if json.Unmarshal(out, &routes) == nil {
				for _, r := range routes {
					if r.Gateway != "" && r.Protocol != "dhcp" {
						ifc.Gateway = r.Gateway
						break
					}
				}
			}
		}
	}
	return ifc, nil
}

// LiveDHCP returns the interfaces that have a dynamic (DHCP) address now.
func (w *Writer) LiveDHCP() map[string]bool {
	out, err := w.run("ip", "-j", "addr", "show")
	res := map[string]bool{}
	if err != nil {
		return res
	}
	var links []struct {
		Ifname   string `json:"ifname"`
		AddrInfo []struct {
			Dynamic bool `json:"dynamic"`
		} `json:"addr_info"`
	}
	if json.Unmarshal(out, &links) != nil {
		return res
	}
	for _, l := range links {
		for _, a := range l.AddrInfo {
			if a.Dynamic {
				res[l.Ifname] = true
			}
		}
	}
	return res
}

// ── Legacy import ─────────────────────────────────────────────────────────────

// importLegacy reads DPlaneOS files without a state file: those earlier
// versions wrote (50-dplane-*), or current ones whose state file is gone: static
// or DHCP interfaces, VLANs and bonds.
func importLegacy(dir string) State {
	s := State{Interfaces: map[string]Iface{}, Bonds: map[string]Bond{}, VLANs: map[string]VLAN{}}
	entries, _ := os.ReadDir(dir)
	type netFile struct {
		name, bond string
		ifc        Iface
		vlans      []string
	}
	var nets []netFile
	for _, e := range entries {
		if !strings.HasPrefix(e.Name(), legacyPrefix) && !strings.HasPrefix(e.Name(), FilePrefix) {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		kv := map[string][]string{}
		for _, l := range strings.Split(string(b), "\n") {
			k, v, ok := strings.Cut(strings.TrimSpace(l), "=")
			if ok && !strings.HasPrefix(k, "#") {
				kv[k] = append(kv[k], v)
			}
		}
		first := func(k string) string {
			if len(kv[k]) > 0 {
				return kv[k][0]
			}
			return ""
		}
		switch {
		case strings.HasSuffix(e.Name(), ".netdev") && first("Kind") == "bond":
			s.Bonds[first("Name")] = Bond{Mode: first("Mode")}
		case strings.HasSuffix(e.Name(), ".netdev") && first("Kind") == "vlan":
			var id int
			fmt.Sscanf(first("Id"), "%d", &id)
			s.VLANs[first("Name")] = VLAN{ID: id}
		case strings.HasSuffix(e.Name(), ".network"):
			nf := netFile{name: first("Name"), bond: first("Bond"), vlans: kv["VLAN"]}
			nf.ifc.DHCP = first("DHCP") == "yes"
			nf.ifc.Addresses = kv["Address"]
			nf.ifc.Gateway = first("Gateway")
			nets = append(nets, nf)
		}
	}
	for _, nf := range nets {
		if nf.bond != "" {
			if b, ok := s.Bonds[nf.bond]; ok {
				b.Slaves = append(b.Slaves, nf.name)
				s.Bonds[nf.bond] = b
			}
			continue
		}
		for _, v := range nf.vlans {
			if vl, ok := s.VLANs[v]; ok {
				vl.Parent = nf.name
				s.VLANs[v] = vl
			}
		}
		if nf.name != "" && (nf.ifc.DHCP || len(nf.ifc.Addresses) > 0 || nf.ifc.Gateway != "") {
			s.Interfaces[nf.name] = nf.ifc
		}
	}
	for n, v := range s.VLANs {
		if v.Parent == "" || v.ID == 0 {
			delete(s.VLANs, n)
		}
	}
	for n, b := range s.Bonds {
		if len(b.Slaves) == 0 || !bondModes[b.Mode] {
			delete(s.Bonds, n)
		}
	}
	// A legacy VLAN parent had its own configuration in another file; keep
	// DHCP when it had none (it lost its address with the old layout anyway).
	for _, v := range s.VLANs {
		if _, ok := s.Interfaces[v.Parent]; !ok && s.BondOf(v.Parent) == "" {
			if _, isBond := s.Bonds[v.Parent]; !isBond {
				s.Interfaces[v.Parent] = Iface{DHCP: true}
			}
		}
	}
	if s.Validate() != nil {
		log.Printf("[networkdwriter] earlier configuration in %s is inconsistent; not imported", dir)
		return State{}
	}
	return s
}

// ── Helpers ───────────────────────────────────────────────────────────────────

// atomicWrite writes content to path using a tmp-then-rename pattern.
func atomicWrite(path, content string, mode os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".dplane-net-*.tmp")
	if err != nil {
		return fmt.Errorf("create tmp: %w", err)
	}
	tmpPath := tmp.Name()
	if _, err := tmp.WriteString(content); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("write tmp: %w", err)
	}
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("close tmp: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("rename: %w", err)
	}
	return nil
}

func isServiceActive(service string) bool {
	return exec.Command("systemctl", "is-active", "--quiet", service).Run() == nil
}

// ValidateIface checks an interface name (kernel limit 15 characters).
func ValidateIface(iface string) error {
	if len(iface) == 0 || len(iface) > 15 {
		return fmt.Errorf("interface name %q: invalid length", iface)
	}
	for _, c := range iface {
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') ||
			(c >= '0' && c <= '9') || c == '-' || c == '_' || c == '.') {
			return fmt.Errorf("interface name %q: invalid character %q", iface, c)
		}
	}
	if iface == "." || iface == ".." {
		return fmt.Errorf("interface name %q: invalid", iface)
	}
	return nil
}
