// Package netconf turns the network configuration stored in the database
// (interfaces, bonds, VLANs, DNS servers) into systemd-networkd files.
//
// The database is the single source: every change re-renders the complete
// set of files, so nothing is left behind when a VLAN or bond is deleted and
// no two files ever describe the same interface.
//
// Files are written to a persistent directory (PersistDir, read at boot by
// the dplaneos-network service before systemd-networkd starts) and to
// /run/systemd/network, where networkd picks them up. They are named
// "10-dplane-<interface>": networkd uses the first file that matches an
// interface, so they take precedence over the system's own default
// ("99-ethernet-default-dhcp": DHCP on every wired interface), which keeps
// applying to every interface not configured here.
package netconf

import (
	"database/sql"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

const (
	// Prefix of every file this package owns.
	Prefix = "10-dplane-"

	PersistDir  = "/var/lib/dplaneos/network"
	LiveDir     = "/run/systemd/network"
	ResolvedDir = "/run/systemd/resolved.conf.d"

	// resolvedFile is the name of the DNS drop-in, in PersistDir and ResolvedDir.
	resolvedFile = Prefix + "dns.conf"
)

// Interface is the address configuration of one interface.
type Interface struct {
	Name    string
	DHCP    bool
	CIDR    string // static address with prefix length
	Gateway string
}

// Bond is a bonded interface and its members.
type Bond struct {
	Name   string
	Mode   string
	Slaves []string
}

// VLAN is a VLAN interface on a parent interface.
type VLAN struct {
	Name   string
	Parent string
	VID    int
}

// Config is the whole network configuration managed by DPlaneOS.
type Config struct {
	Interfaces []Interface
	Bonds      []Bond
	VLANs      []VLAN
	DNS        []string // servers
	Search     []string // search domains
}

// Load reads the configuration from the database.
func Load(db *sql.DB) (Config, error) {
	var c Config
	rows, err := db.Query(`SELECT interface, type, cidr, gateway FROM network_interfaces ORDER BY interface`)
	if err != nil {
		return c, err
	}
	for rows.Next() {
		var i Interface
		var typ string
		if err := rows.Scan(&i.Name, &typ, &i.CIDR, &i.Gateway); err != nil {
			rows.Close()
			return c, err
		}
		i.DHCP = typ != "static" || i.CIDR == ""
		c.Interfaces = append(c.Interfaces, i)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return c, err
	}

	rows, err = db.Query(`SELECT name, slaves, mode FROM network_bonds ORDER BY name`)
	if err != nil {
		return c, err
	}
	for rows.Next() {
		var b Bond
		var slaves string
		if err := rows.Scan(&b.Name, &slaves, &b.Mode); err != nil {
			rows.Close()
			return c, err
		}
		if slaves != "" {
			b.Slaves = strings.Split(slaves, ",")
		}
		c.Bonds = append(c.Bonds, b)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return c, err
	}

	rows, err = db.Query(`SELECT name, parent, vid FROM network_vlans ORDER BY name`)
	if err != nil {
		return c, err
	}
	for rows.Next() {
		var v VLAN
		if err := rows.Scan(&v.Name, &v.Parent, &v.VID); err != nil {
			rows.Close()
			return c, err
		}
		c.VLANs = append(c.VLANs, v)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return c, err
	}

	rows, err = db.Query(`SELECT kind, value FROM network_dns ORDER BY kind, position`)
	if err != nil {
		return c, err
	}
	defer rows.Close()
	for rows.Next() {
		var kind, v string
		if err := rows.Scan(&kind, &v); err != nil {
			return c, err
		}
		if kind == "search" {
			c.Search = append(c.Search, v)
		} else {
			c.DNS = append(c.DNS, v)
		}
	}
	return c, rows.Err()
}

// SaveDNS replaces the DNS servers and search domains in the database.
func SaveDNS(db *sql.DB, servers, search []string) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck
	if _, err := tx.Exec(`DELETE FROM network_dns`); err != nil {
		return err
	}
	for i, s := range servers {
		if _, err := tx.Exec(`INSERT INTO network_dns (kind, position, value) VALUES ('server', $1, $2)`, i, s); err != nil {
			return err
		}
	}
	for i, s := range search {
		if _, err := tx.Exec(`INSERT INTO network_dns (kind, position, value) VALUES ('search', $1, $2)`, i, s); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// Save replaces the whole configuration in the database (a configuration
// that comes from outside: a Git repository, another node). It is checked
// first: one that cannot be rendered is not saved.
func Save(db *sql.DB, c Config) error {
	if _, err := Render(c); err != nil {
		return err
	}
	if _, err := RenderDNS(c.DNS, c.Search); err != nil {
		return err
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck
	for _, t := range []string{"network_interfaces", "network_bonds", "network_vlans", "network_dns"} {
		if _, err := tx.Exec(`DELETE FROM ` + t); err != nil {
			return err
		}
	}
	for _, i := range c.Interfaces {
		typ, cidr, gw := "static", i.CIDR, i.Gateway
		if i.DHCP || i.CIDR == "" {
			typ, cidr, gw = "dhcp", "", ""
		}
		if _, err := tx.Exec(`INSERT INTO network_interfaces (interface, type, cidr, gateway, updated_at)
			VALUES ($1, $2, $3, $4, NOW())`, i.Name, typ, cidr, gw); err != nil {
			return err
		}
	}
	for _, b := range c.Bonds {
		if _, err := tx.Exec(`INSERT INTO network_bonds (name, slaves, mode, updated_at) VALUES ($1, $2, $3, NOW())`,
			b.Name, strings.Join(b.Slaves, ","), b.Mode); err != nil {
			return err
		}
	}
	for _, v := range c.VLANs {
		if _, err := tx.Exec(`INSERT INTO network_vlans (name, parent, vid, updated_at) VALUES ($1, $2, $3, NOW())`,
			v.Name, v.Parent, v.VID); err != nil {
			return err
		}
	}
	for i, s := range c.DNS {
		if _, err := tx.Exec(`INSERT INTO network_dns (kind, position, value) VALUES ('server', $1, $2)`, i, s); err != nil {
			return err
		}
	}
	for i, s := range c.Search {
		if _, err := tx.Exec(`INSERT INTO network_dns (kind, position, value) VALUES ('search', $1, $2)`, i, s); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ApplyDB renders the configuration saved in the database and makes
// systemd-networkd and systemd-resolved use it.
func ApplyDB(db *sql.DB) error {
	cfg, err := Load(db)
	if err != nil {
		return fmt.Errorf("reading the network configuration: %w", err)
	}
	if !Active() {
		// Not managed by systemd-networkd: nothing would read the files.
		return nil
	}
	_, err = System().Apply(cfg)
	return err
}

// With returns a copy of the configuration with the address configuration
// of one interface replaced (a change that is tried before it is saved).
func (c Config) With(i Interface) Config {
	out := c
	out.Interfaces = nil
	for _, old := range c.Interfaces {
		if old.Name != i.Name {
			out.Interfaces = append(out.Interfaces, old)
		}
	}
	out.Interfaces = append(out.Interfaces, i)
	return out
}

var bondModes = map[string]string{
	"balance-rr": "balance-rr", "active-backup": "active-backup", "balance-xor": "balance-xor",
	"broadcast": "broadcast", "802.3ad": "802.3ad", "balance-tlb": "balance-tlb", "balance-alb": "balance-alb",
	"lacp": "802.3ad", "failover": "active-backup", "roundrobin": "balance-rr",
}

// ValidName reports whether s can be an interface name in a networkd file.
func ValidName(s string) bool {
	if s == "" || len(s) > 15 || s == "." || s == ".." {
		return false
	}
	for _, c := range s {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == '.') {
			return false
		}
	}
	return true
}

const header = "# Written by DPlaneOS from its database. Changes made here are overwritten;\n# use the web interface (Network).\n\n"

// Render returns the networkd files for the configuration, by file name.
// Every interface the configuration mentions gets exactly one .network
// file; bonds and VLANs get a .netdev file in addition.
func Render(c Config) (map[string]string, error) {
	files := map[string]string{}

	iface := map[string]Interface{}
	for _, i := range c.Interfaces {
		if !ValidName(i.Name) {
			return nil, fmt.Errorf("interface name %q is not valid", i.Name)
		}
		if !i.DHCP {
			if _, _, err := net.ParseCIDR(i.CIDR); err != nil {
				return nil, fmt.Errorf("%s: address %q is not valid", i.Name, i.CIDR)
			}
			if i.Gateway != "" && net.ParseIP(i.Gateway) == nil {
				return nil, fmt.Errorf("%s: gateway %q is not valid", i.Name, i.Gateway)
			}
		}
		iface[i.Name] = i
	}

	master := map[string]string{} // member -> bond
	isBond := map[string]bool{}
	for _, b := range c.Bonds {
		mode, ok := bondModes[b.Mode]
		if !ok {
			return nil, fmt.Errorf("bond %s: unknown mode %q", b.Name, b.Mode)
		}
		if !ValidName(b.Name) {
			return nil, fmt.Errorf("bond name %q is not valid", b.Name)
		}
		isBond[b.Name] = true
		for _, s := range b.Slaves {
			if !ValidName(s) {
				return nil, fmt.Errorf("bond %s: member name %q is not valid", b.Name, s)
			}
			if other, taken := master[s]; taken {
				return nil, fmt.Errorf("%s is a member of both %s and %s", s, other, b.Name)
			}
			master[s] = b.Name
		}
		// MIIMonitorSec: without it the bond never notices a dead link.
		netdev := header + "[NetDev]\nName=" + b.Name + "\nKind=bond\n\n[Bond]\nMode=" + mode + "\nMIIMonitorSec=100ms\n"
		if mode == "802.3ad" {
			netdev += "LACPTransmitRate=fast\nTransmitHashPolicy=layer3+4\n"
		}
		files[Prefix+b.Name+".netdev"] = netdev
	}

	vlansOn := map[string][]string{} // parent -> VLAN interfaces
	isVLAN := map[string]bool{}
	for _, v := range c.VLANs {
		if !ValidName(v.Name) || !ValidName(v.Parent) {
			return nil, fmt.Errorf("VLAN %q on %q: name is not valid", v.Name, v.Parent)
		}
		if v.VID < 1 || v.VID > 4094 {
			return nil, fmt.Errorf("VLAN %s: ID %d is out of range", v.Name, v.VID)
		}
		isVLAN[v.Name] = true
		vlansOn[v.Parent] = append(vlansOn[v.Parent], v.Name)
		files[Prefix+v.Name+".netdev"] = header + "[NetDev]\nName=" + v.Name + "\nKind=vlan\n\n[VLAN]\nId=" + fmt.Sprint(v.VID) + "\n"
	}

	names := map[string]bool{}
	for n := range iface {
		names[n] = true
	}
	for n := range isBond {
		names[n] = true
	}
	for n := range master {
		names[n] = true
	}
	for n := range isVLAN {
		names[n] = true
	}
	for n := range vlansOn {
		names[n] = true
	}

	for name := range names {
		var b strings.Builder
		b.WriteString(header + "[Match]\nName=" + name + "\n\n[Network]\n")
		if bond, member := master[name]; member {
			// A bond member carries no addresses of its own.
			b.WriteString("Bond=" + bond + "\n")
			files[Prefix+name+".network"] = b.String()
			continue
		}
		vl := vlansOn[name]
		sort.Strings(vl)
		for _, v := range vl {
			b.WriteString("VLAN=" + v + "\n")
		}
		cfg, configured := iface[name]
		switch {
		case configured && !cfg.DHCP:
			b.WriteString("DHCP=no\nAddress=" + cfg.CIDR + "\n")
			if cfg.Gateway != "" {
				b.WriteString("Gateway=" + cfg.Gateway + "\n")
			}
		case configured:
			b.WriteString("DHCP=yes\n")
		case isVLAN[name]:
			// A VLAN without an address of its own (containers, bridges
			// attach to it) asks nobody for one.
			b.WriteString("DHCP=no\nLinkLocalAddressing=no\n")
		default:
			// A bond, or the parent of a VLAN, that was given no address:
			// DHCP, like every interface that is not configured here.
			b.WriteString("DHCP=yes\n")
		}
		files[Prefix+name+".network"] = b.String()
	}
	return files, nil
}

// RenderDNS returns the systemd-resolved drop-in for the DNS servers and
// search domains, or "" when none are set (what DHCP announces is used).
func RenderDNS(servers, search []string) (string, error) {
	if len(servers) == 0 && len(search) == 0 {
		return "", nil
	}
	out := header + "[Resolve]\n"
	if len(servers) > 0 {
		for _, s := range servers {
			if net.ParseIP(s) == nil {
				return "", fmt.Errorf("DNS server %q is not an IP address", s)
			}
		}
		out += "DNS=" + strings.Join(servers, " ") + "\n"
	}
	if len(search) > 0 {
		for _, d := range search {
			if !validDomain(d) {
				return "", fmt.Errorf("search domain %q is not valid", d)
			}
		}
		out += "Domains=" + strings.Join(search, " ") + "\n"
	}
	return out, nil
}

func validDomain(d string) bool {
	if d == "" || len(d) > 253 {
		return false
	}
	for _, c := range d {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '.') {
			return false
		}
	}
	return true
}

// ── Writing ──────────────────────────────────────────────────────────────────

// Target is where the files go. The zero value is not usable; see System.
type Target struct {
	Persist  string // survives reboots; "" to skip (a change being tried)
	Live     string // read by systemd-networkd
	Resolved string // drop-in directory of systemd-resolved

	// Reload makes networkd (network = true) or resolved pick up changed
	// files. Nil: nothing to notify (tests).
	Reload func(network, dns bool) error
}

// System is the running system's target.
func System() Target {
	return Target{Persist: PersistDir, Live: LiveDir, Resolved: ResolvedDir, Reload: reloadServices}
}

// Trial is System without the persistent copy: for a change that is
// reverted unless confirmed. A restart then comes back with the saved state.
func Trial() Target {
	t := System()
	t.Persist = ""
	return t
}

var applyMu sync.Mutex

// Apply writes the files of the configuration, removes the files of
// interfaces that are no longer in it, and tells the services. It reports
// whether anything changed.
func (t Target) Apply(c Config) (changed bool, err error) {
	files, err := Render(c)
	if err != nil {
		return false, err
	}
	dns, err := RenderDNS(c.DNS, c.Search)
	if err != nil {
		return false, err
	}
	applyMu.Lock()
	defer applyMu.Unlock()

	networkChanged, dnsChanged := false, false
	for _, dir := range []string{t.Persist, t.Live} {
		if dir == "" {
			continue
		}
		ch, err := syncDir(dir, files, func(name string) bool {
			return strings.HasPrefix(name, Prefix) && (strings.HasSuffix(name, ".network") || strings.HasSuffix(name, ".netdev"))
		})
		if err != nil {
			return false, err
		}
		if dir == t.Live && ch {
			networkChanged = true
		}
	}
	dnsFiles := map[string]string{}
	if dns != "" {
		dnsFiles[resolvedFile] = dns
	}
	for _, dir := range []string{t.Persist, t.Resolved} {
		if dir == "" {
			continue
		}
		ch, err := syncDir(dir, dnsFiles, func(name string) bool { return name == resolvedFile })
		if err != nil {
			return false, err
		}
		if dir == t.Resolved && ch {
			dnsChanged = true
		}
	}
	if (networkChanged || dnsChanged) && t.Reload != nil {
		if err := t.Reload(networkChanged, dnsChanged); err != nil {
			return true, err
		}
	}
	return networkChanged || dnsChanged, nil
}

// syncDir makes the files that mine() selects in dir equal to want.
func syncDir(dir string, want map[string]string, mine func(name string) bool) (changed bool, err error) {
	if err := os.MkdirAll(dir, 0755); err != nil {
		return false, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false, err
	}
	for _, e := range entries {
		if _, keep := want[e.Name()]; keep || !mine(e.Name()) {
			continue
		}
		if err := os.Remove(filepath.Join(dir, e.Name())); err != nil {
			return changed, err
		}
		changed = true
	}
	for name, content := range want {
		path := filepath.Join(dir, name)
		if old, err := os.ReadFile(path); err == nil && string(old) == content {
			continue
		}
		tmp := path + ".tmp"
		// networkd runs as its own user: the files must be readable by it.
		if err := os.WriteFile(tmp, []byte(content), 0644); err != nil {
			return changed, err
		}
		if err := os.Rename(tmp, path); err != nil {
			os.Remove(tmp)
			return changed, err
		}
		changed = true
	}
	return changed, nil
}

func reloadServices(network, dns bool) error {
	if network {
		if out, err := exec.Command("networkctl", "reload").CombinedOutput(); err != nil {
			return fmt.Errorf("the files are written, but networkctl reload failed: %v (%s)", err, strings.TrimSpace(string(out)))
		}
	}
	if dns {
		if out, err := exec.Command("systemctl", "try-reload-or-restart", "systemd-resolved").CombinedOutput(); err != nil {
			return fmt.Errorf("the DNS servers are written, but systemd-resolved did not reload: %v (%s)", err, strings.TrimSpace(string(out)))
		}
	}
	return nil
}

// Reconfigure makes networkd apply the current files to an interface again
// (addresses and routes that are no longer configured are dropped).
func Reconfigure(iface string) error {
	if out, err := exec.Command("networkctl", "reconfigure", iface).CombinedOutput(); err != nil {
		return fmt.Errorf("networkctl reconfigure %s: %v (%s)", iface, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// Active reports whether systemd-networkd manages this system's network.
func Active() bool {
	return exec.Command("systemctl", "is-active", "--quiet", "systemd-networkd").Run() == nil
}
