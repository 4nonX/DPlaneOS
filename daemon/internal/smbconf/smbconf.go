// Package smbconf renders the Samba share configuration from the smb_shares
// table. It is the single renderer for both the share CRUD handlers and the
// GitOps reconciler, so a GitOps apply produces the same file as the UI.
//
// On NixOS, modules/samba.nix owns [global] and pulls this file in with
// `include = /var/lib/dplaneos/smb-shares.conf` from inside [global]. The
// file therefore contains share stanzas only and ends by re-opening [global],
// so whatever global keys NixOS renders after the include line still land in
// [global] rather than in the last share. Elsewhere the file is the complete
// smb.conf and starts with its own [global].
package smbconf

import (
	"database/sql"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

// Share is one row of smb_shares, as rendered into a share stanza.
type Share struct {
	Name          string `json:"name"`
	Path          string `json:"path"`
	Comment       string `json:"comment"`
	Browsable     bool   `json:"browsable"`
	ReadOnly      bool   `json:"read_only"`
	GuestOK       bool   `json:"guest_ok"`
	ValidUsers    string `json:"valid_users"`
	WriteList     string `json:"write_list"`
	CreateMask    string `json:"create_mask"`
	DirectoryMask string `json:"directory_mask"`

	// TimeMachine makes the share a macOS Time Machine target.
	TimeMachine bool `json:"time_machine"`
	// TimeMachineQuota caps the backup size (e.g. "500G"); empty means no cap.
	TimeMachineQuota string `json:"time_machine_quota"`
	// ShadowCopy exposes ZFS snapshots as Windows "Previous Versions".
	ShadowCopy bool `json:"shadow_copy"`
	// RecycleBin moves deleted files to .recycle/<user> instead of deleting them.
	RecycleBin bool `json:"recycle_bin"`
	// HostsAllow / HostsDeny are space-separated IPs, CIDRs or host names.
	HostsAllow string `json:"hosts_allow"`
	HostsDeny  string `json:"hosts_deny"`
}

// Options are the settings outside smb_shares that affect rendering.
type Options struct {
	// NixOSManaged: NixOS owns [global]; render share stanzas only.
	NixOSManaged bool
	// AppleExtensions is the global "macOS support" toggle (settings key
	// smb_time_machine). Apple extensions are also enabled automatically
	// when any share is a Time Machine target.
	AppleExtensions bool
	// ExtraGlobal is appended to [global] when not NixOS-managed.
	ExtraGlobal string
}

// ShareColumns lists the smb_shares columns in the order Scan expects.
const ShareColumns = "name, path, comment, browsable, read_only, guest_ok, valid_users, write_list, " +
	"create_mask, directory_mask, time_machine, time_machine_quota, shadow_copy, recycle_bin, hosts_allow, hosts_deny"

// Scan reads one row selected with ShareColumns.
func Scan(row interface{ Scan(...any) error }) (Share, error) {
	var s Share
	var browsable, readOnly, guestOK, tm, sc, rb int
	err := row.Scan(&s.Name, &s.Path, &s.Comment, &browsable, &readOnly, &guestOK, &s.ValidUsers, &s.WriteList,
		&s.CreateMask, &s.DirectoryMask, &tm, &s.TimeMachineQuota, &sc, &rb, &s.HostsAllow, &s.HostsDeny)
	s.Browsable, s.ReadOnly, s.GuestOK = browsable == 1, readOnly == 1, guestOK == 1
	s.TimeMachine, s.ShadowCopy, s.RecycleBin = tm == 1, sc == 1, rb == 1
	return s, err
}

// LoadEnabled returns all enabled shares, ordered by name.
func LoadEnabled(db *sql.DB) ([]Share, error) {
	rows, err := db.Query(`SELECT ` + ShareColumns + ` FROM smb_shares WHERE enabled = 1 ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var shares []Share
	for rows.Next() {
		s, err := Scan(rows)
		if err != nil {
			return nil, err
		}
		shares = append(shares, s)
	}
	return shares, rows.Err()
}

// LoadOptions reads the global options from the settings table.
func LoadOptions(db *sql.DB) Options {
	var apple int
	var extra string
	db.QueryRow(`SELECT COALESCE(value,'0') FROM settings WHERE key='smb_time_machine'`).Scan(&apple) //nolint:errcheck
	db.QueryRow(`SELECT COALESCE(value,'') FROM settings WHERE key='smb_extra_global'`).Scan(&extra) //nolint:errcheck
	return Options{NixOSManaged: IsNixOS(), AppleExtensions: apple == 1, ExtraGlobal: extra}
}

// IsNixOS reports whether this host is NixOS (same check as nixwriter).
func IsNixOS() bool {
	_, err := os.Stat("/etc/NIXOS")
	return err == nil
}

// AppleEnabled reports whether vfs_fruit must be loaded. macOS negotiates the
// Apple SMB2 extensions (AAPL) on the first share it connects to, so once any
// share needs fruit, every share loads it.
func AppleEnabled(o Options, shares []Share) bool {
	if o.AppleExtensions {
		return true
	}
	for _, s := range shares {
		if s.TimeMachine {
			return true
		}
	}
	return false
}

// TimeMachineShares returns the names of Time Machine target shares.
func TimeMachineShares(shares []Share) []string {
	var names []string
	for _, s := range shares {
		if s.TimeMachine {
			names = append(names, Sanitize(s.Name))
		}
	}
	return names
}

// Render produces the configuration file content.
func Render(o Options, shares []Share) string {
	apple := AppleEnabled(o, shares)
	var b strings.Builder

	if o.NixOSManaged {
		b.WriteString("# Managed by dplaned: share stanzas only. [global] is owned by NixOS\n")
		b.WriteString("# (modules/samba.nix), which includes this file.\n\n")
	} else {
		b.WriteString("[global]\n")
		b.WriteString("   workgroup = WORKGROUP\n")
		b.WriteString("   server string = DPlaneOS NAS\n")
		b.WriteString("   security = user\n")
		b.WriteString("   map to guest = Bad User\n")
		b.WriteString("   log file = /var/log/samba/log.%m\n")
		b.WriteString("   max log size = 1000\n")
		if apple {
			b.WriteString("   # macOS support (Apple SMB2 extensions)\n")
			b.WriteString("   fruit:model = MacSamba\n")
			b.WriteString("   fruit:posix_rename = yes\n")
			b.WriteString("   fruit:nfs_aces = no\n")
		}
		if o.ExtraGlobal != "" {
			b.WriteString("   # Custom global parameters\n")
			for _, line := range strings.Split(o.ExtraGlobal, "\n") {
				t := strings.TrimSpace(line)
				if t == "" {
					continue
				}
				// Block directives that could escape [global] or pull in other files.
				if strings.Contains(strings.ToLower(t), "include") || strings.Contains(t, "[") {
					continue
				}
				b.WriteString("   " + t + "\n")
			}
		}
		b.WriteString("\n")
	}

	for _, s := range shares {
		renderShare(&b, s, apple)
	}

	if o.NixOSManaged {
		b.WriteString("# Re-open [global] for the NixOS keys that follow the include line.\n")
		b.WriteString("[global]\n")
	}
	return b.String()
}

func renderShare(b *strings.Builder, s Share, apple bool) {
	fmt.Fprintf(b, "[%s]\n", Sanitize(s.Name))
	fmt.Fprintf(b, "   path = %s\n", Sanitize(s.Path))
	if c := Sanitize(s.Comment); c != "" {
		fmt.Fprintf(b, "   comment = %s\n", c)
	}
	fmt.Fprintf(b, "   browsable = %s\n", yesNo(s.Browsable))
	fmt.Fprintf(b, "   read only = %s\n", yesNo(s.ReadOnly))
	if s.GuestOK {
		b.WriteString("   guest ok = yes\n")
	}
	if v := Sanitize(s.ValidUsers); v != "" {
		fmt.Fprintf(b, "   valid users = %s\n", v)
	}
	if v := Sanitize(s.WriteList); v != "" {
		fmt.Fprintf(b, "   write list = %s\n", v)
	}
	fmt.Fprintf(b, "   create mask = %s\n", orDefault(maskOrEmpty(s.CreateMask), "0664"))
	fmt.Fprintf(b, "   directory mask = %s\n", orDefault(maskOrEmpty(s.DirectoryMask), "0775"))
	if v := hostListOrEmpty(s.HostsAllow); v != "" {
		fmt.Fprintf(b, "   hosts allow = %s\n", v)
	}
	if v := hostListOrEmpty(s.HostsDeny); v != "" {
		fmt.Fprintf(b, "   hosts deny = %s\n", v)
	}

	var vfs []string
	if apple {
		vfs = append(vfs, "catia", "fruit", "streams_xattr")
	}
	if s.ShadowCopy {
		vfs = append(vfs, "shadow_copy2")
	}
	if s.RecycleBin {
		vfs = append(vfs, "recycle")
	}
	if len(vfs) > 0 {
		fmt.Fprintf(b, "   vfs objects = %s\n", strings.Join(vfs, " "))
	}

	if apple {
		b.WriteString("   fruit:metadata = stream\n")
		b.WriteString("   fruit:veto_appledouble = no\n")
		b.WriteString("   fruit:wipe_intentionally_left_blank_rfork = yes\n")
		b.WriteString("   fruit:delete_empty_adfiles = yes\n")
	}
	if s.TimeMachine {
		b.WriteString("   fruit:time machine = yes\n")
		if ValidateTimeMachineQuota(s.TimeMachineQuota) == nil && s.TimeMachineQuota != "" {
			fmt.Fprintf(b, "   fruit:time machine max size = %s\n", s.TimeMachineQuota)
		}
	}
	if s.ShadowCopy {
		b.WriteString("   shadow:snapdir = .zfs/snapshot\n")
		b.WriteString("   shadow:sort = desc\n")
		// Strip the frequency prefix (e.g. "auto-daily-") then parse the date.
		b.WriteString("   shadow:snapprefix = ^auto-[a-z]+-\n")
		b.WriteString("   shadow:format = %Y%m%d-%H%M\n")
	}
	if s.RecycleBin {
		b.WriteString("   recycle:repository = .recycle/%U\n")
		b.WriteString("   recycle:keeptree = yes\n")
		b.WriteString("   recycle:versions = yes\n")
		b.WriteString("   recycle:touch = yes\n")
		b.WriteString("   recycle:directory_mode = 0770\n")
	}
	b.WriteString("\n")
}

// WriteAtomic writes content to path via a temp file and rename.
func WriteAtomic(path string, content []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(content); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

const (
	avahiServicePathEtc   = "/etc/avahi/services/dplaneos-timemachine.service"
	avahiServicePathNixOS = "/var/lib/dplaneos/avahi/dplaneos-timemachine.service"
)

// AvahiPath is where the Time Machine Bonjour advertisement is written. On
// NixOS /etc is read-only; modules/samba.nix links the /etc/avahi/services
// entry to a file under /var/lib/dplaneos.
func AvahiPath() string {
	if IsNixOS() {
		return avahiServicePathNixOS
	}
	return avahiServicePathEtc
}

// SyncAvahi writes the Bonjour advertisement for the Time Machine target
// shares and asks avahi to reload. With no targets the file stays valid but
// has no _adisk record. Failures are logged only: Samba keeps working, Macs
// just will not auto-discover the target.
func SyncAvahi(tmShares []string) {
	path := AvahiPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		log.Printf("WARN: avahi services dir: %v", err)
		return
	}
	if err := WriteAtomic(path, []byte(AvahiTimeMachineXML(tmShares))); err != nil {
		log.Printf("WARN: avahi service write: %v", err)
		return
	}
	if out, err := exec.Command("avahi-daemon", "--reload").CombinedOutput(); err != nil {
		log.Printf("WARN: avahi-daemon --reload: %v %s", err, strings.TrimSpace(string(out)))
	}
}

// AvahiTimeMachineXML is the Bonjour service file that lets Macs discover the
// Time Machine shares. Each target share is advertised as one dkN record; the
// adVN value must match the share name.
func AvahiTimeMachineXML(tmShares []string) string {
	var b strings.Builder
	b.WriteString("<?xml version=\"1.0\" standalone='no'?>\n")
	b.WriteString("<!DOCTYPE service-group SYSTEM \"avahi-service.dtd\">\n")
	b.WriteString("<service-group>\n")
	b.WriteString("  <name replace-wildcards=\"yes\">%h</name>\n")
	if len(tmShares) > 0 {
		b.WriteString("  <service>\n")
		b.WriteString("    <type>_adisk._tcp</type>\n")
		b.WriteString("    <port>9</port>\n")
		b.WriteString("    <txt-record>sys=waMa=0,adVF=0x100</txt-record>\n")
		for i, name := range tmShares {
			fmt.Fprintf(&b, "    <txt-record>dk%d=adVN=%s,adVF=0x82</txt-record>\n", i, xmlEscape(name))
		}
		b.WriteString("  </service>\n")
	}
	b.WriteString("  <service>\n")
	b.WriteString("    <type>_device-info._tcp</type>\n")
	b.WriteString("    <port>0</port>\n")
	b.WriteString("    <txt-record>model=MacSamba</txt-record>\n")
	b.WriteString("  </service>\n")
	b.WriteString("</service-group>\n")
	return b.String()
}

// Sanitize removes characters that could break smb.conf structure.
func Sanitize(val string) string {
	val = strings.ReplaceAll(val, "\n", " ")
	val = strings.ReplaceAll(val, "\r", " ")
	val = strings.ReplaceAll(val, "[", "(")
	val = strings.ReplaceAll(val, "]", ")")
	val = strings.ReplaceAll(val, "=", ":")
	return strings.TrimSpace(val)
}

var (
	hostTokenRe = regexp.MustCompile(`^[A-Za-z0-9.:/*_-]{1,253}$`)
	tmQuotaRe   = regexp.MustCompile(`^[1-9][0-9]{0,8}[KMGT]$`)
	maskRe      = regexp.MustCompile(`^0?[0-7]{3,4}$`)
)

// ValidateHostList checks a space- or comma-separated list of IPs, CIDRs,
// host names or wildcards (e.g. "192.168.1.0/24 10.0.0.5 nas.lan"). The
// keyword EXCEPT is allowed between entries, as in smb.conf.
func ValidateHostList(list string) error {
	for _, tok := range splitHosts(list) {
		if tok == "EXCEPT" {
			continue
		}
		if !hostTokenRe.MatchString(tok) {
			return fmt.Errorf("invalid host entry %q (use IPs, CIDRs or host names)", tok)
		}
	}
	return nil
}

// ValidateTimeMachineQuota checks a Time Machine size cap such as "500G" or "2T".
func ValidateTimeMachineQuota(q string) error {
	if q == "" || tmQuotaRe.MatchString(q) {
		return nil
	}
	return fmt.Errorf("invalid Time Machine quota %q (use a number with K, M, G or T, e.g. 500G)", q)
}

func splitHosts(list string) []string {
	return strings.Fields(strings.ReplaceAll(list, ",", " "))
}

func hostListOrEmpty(list string) string {
	if ValidateHostList(list) != nil {
		return ""
	}
	return strings.Join(splitHosts(list), " ")
}

func maskOrEmpty(m string) string {
	if maskRe.MatchString(m) {
		return m
	}
	return ""
}

func yesNo(v bool) string {
	if v {
		return "yes"
	}
	return "no"
}

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

func xmlEscape(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", "\"", "&quot;", "'", "&apos;")
	return r.Replace(s)
}
