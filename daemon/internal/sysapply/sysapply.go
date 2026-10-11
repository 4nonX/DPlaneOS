// Package sysapply makes the running system match the system settings saved
// by the web interface (internal/nixwriter's state file): host name, time
// zone, NTP servers, kernel tuning, firewall ports.
//
// An installed appliance is an image: nothing on it can rebuild the system
// configuration. Settings therefore take effect here, directly, and again at
// every boot (dplaned -apply-system-settings, run by
// dplaneos-system-settings.service before the network comes up).
//
// Every step is idempotent and only touches what differs.
package sysapply

import (
	"crypto/tls"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"dplaned/internal/nixwriter"
)

// DefaultHostname is the name of a system that was never given one.
const DefaultHostname = "dplaneos"

// FirewallRulesFile holds the rules set in the web interface, one per line:
// "allow tcp 8080 any" or "deny tcp 22 10.0.0.0/8", deny rules first. The
// firewall reads it whenever it starts or reloads
// (networking.firewall.extraCommands in nixos/module.nix).
//
// FirewallDisabledFile exists while the firewall is switched off; the
// firewall unit does not start then.
const (
	FirewallRulesFile    = "/var/lib/dplaneos/system/firewall-rules"
	FirewallDisabledFile = "/var/lib/dplaneos/system/firewall-disabled"
)

// WebCertFile and WebKeyFile are what nginx serves the web interface with
// on port 443. dplaneos-web-cert.service puts a self-signed certificate
// there at first boot; activating a certificate (Certificates page) replaces
// their content.
const (
	WebCertFile = "/var/lib/dplaneos/tls/web.crt"
	WebKeyFile  = "/var/lib/dplaneos/tls/web.key"
)

// Env is where Apply works; the zero value is the running system.
type Env struct {
	// Root is prepended to every path (tests).
	Root string
	// Run executes a command; nil runs it on the system.
	Run func(name string, args ...string) error
}

func (e Env) path(p string) string { return filepath.Join(e.Root, p) }

func (e Env) run(name string, args ...string) error {
	if e.Run != nil {
		return e.Run(name, args...)
	}
	out, err := exec.Command(name, args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %s: %v (%s)", name, strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}

// Apply makes the system match the settings. It applies everything it can
// and returns the steps that failed.
func (e Env) Apply(st nixwriter.DPlaneState) error {
	var errs []error
	step := func(what string, err error) {
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", what, err))
		}
	}
	step("host name", e.hostname(st.Hostname))
	step("time zone", e.timezone(st.Timezone))
	step("NTP servers", e.ntp(st.NTPServers))
	step("kernel settings", e.sysctl(st.Sysctl))
	step("ZFS cache limit", e.arcMax(st.ZFSArcMax))
	step("firewall", e.firewall(st.FirewallList(), st.FirewallDisabled))
	step("web certificate", e.webCertificate(st.TLSCert, st.TLSKey))
	step("SSH", e.ssh(st.SSHPort, st.SSHPasswordAuth, st.SSHPermitRootLogin))
	step("UPS", e.ups(st.UPS()))
	return errors.Join(errs...)
}

// writeIfChanged writes content to path (atomically) unless it is already
// there, and reports whether it wrote.
func writeIfChanged(path, content string, mode os.FileMode) (bool, error) {
	if old, err := os.ReadFile(path); err == nil && string(old) == content {
		return false, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return false, err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(content), mode); err != nil {
		return false, err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return false, err
	}
	return true, nil
}

func removeIfPresent(path string) (bool, error) {
	err := os.Remove(path)
	if os.IsNotExist(err) {
		return false, nil
	}
	return err == nil, err
}

// hostname sets the kernel's host name and /etc/hostname. A system without
// a saved name keeps the one it has, unless that is the image's placeholder.
func (e Env) hostname(name string) error {
	current := ""
	if b, err := os.ReadFile(e.path("/proc/sys/kernel/hostname")); err == nil {
		current = strings.TrimSpace(string(b))
	}
	if name == "" {
		switch current {
		case "", "(none)", "localhost", "nixos":
			name = DefaultHostname
		default:
			name = current
		}
	}
	if _, err := writeIfChanged(e.path("/etc/hostname"), name+"\n", 0644); err != nil {
		return err
	}
	if current == name {
		return nil
	}
	if err := os.WriteFile(e.path("/proc/sys/kernel/hostname"), []byte(name), 0644); err != nil {
		return err
	}
	// Services that announce the name pick it up when restarted. They may
	// not be running (early boot): not an error.
	_ = e.run("systemctl", "--no-block", "try-restart", "avahi-daemon.service", "samba-nmbd.service", "samba-smbd.service")
	return nil
}

// timezone points /etc/localtime at the zone.
func (e Env) timezone(tz string) error {
	if tz == "" {
		return nil
	}
	if strings.Contains(tz, "..") || strings.HasPrefix(tz, "/") {
		return fmt.Errorf("invalid time zone %q", tz)
	}
	target := "/etc/zoneinfo/" + tz
	if fi, err := os.Stat(e.path(target)); err != nil || fi.IsDir() {
		return fmt.Errorf("unknown time zone %q", tz)
	}
	link := e.path("/etc/localtime")
	if cur, err := os.Readlink(link); err == nil && cur == target {
		return nil
	}
	tmp := link + ".tmp"
	os.Remove(tmp)
	if err := os.Symlink(target, tmp); err != nil {
		return err
	}
	if err := os.Rename(tmp, link); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

// ntp writes a systemd-timesyncd drop-in; without servers the system's own
// default pool applies.
func (e Env) ntp(servers []string) error {
	file := e.path("/run/systemd/timesyncd.conf.d/10-dplane.conf")
	var changed bool
	var err error
	if len(servers) == 0 {
		changed, err = removeIfPresent(file)
	} else {
		for _, s := range servers {
			if s == "" || strings.ContainsAny(s, " \t\n\r#=;") {
				return fmt.Errorf("invalid NTP server %q", s)
			}
		}
		changed, err = writeIfChanged(file, "# Written by DPlaneOS (Settings: time servers).\n[Time]\nNTP="+strings.Join(servers, " ")+"\n", 0644)
	}
	if err != nil || !changed {
		return err
	}
	_ = e.run("systemctl", "--no-block", "try-restart", "systemd-timesyncd.service")
	return nil
}

// sysctl writes the values to /proc/sys.
func (e Env) sysctl(values map[string]string) error {
	keys := make([]string, 0, len(values))
	for k := range values {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var errs []error
	for _, k := range keys {
		if strings.Contains(k, "/") || strings.Contains(k, "..") {
			errs = append(errs, fmt.Errorf("invalid key %q", k))
			continue
		}
		p := e.path("/proc/sys/" + strings.ReplaceAll(k, ".", "/"))
		if cur, err := os.ReadFile(p); err == nil && strings.Join(strings.Fields(string(cur)), " ") == strings.Join(strings.Fields(values[k]), " ") {
			continue
		}
		if err := os.WriteFile(p, []byte(values[k]), 0644); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", k, err))
		}
	}
	return errors.Join(errs...)
}

// arcMax sets the ZFS ARC size limit (0: ZFS's own default).
func (e Env) arcMax(bytes int64) error {
	p := e.path("/sys/module/zfs/parameters/zfs_arc_max")
	cur, err := os.ReadFile(p)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // module not loaded (yet): applied again when the daemon starts
		}
		return err
	}
	want := strconv.FormatInt(bytes, 10)
	if strings.TrimSpace(string(cur)) == want {
		return nil
	}
	return os.WriteFile(p, []byte(want), 0644)
}

// firewall writes the rules and makes the firewall match: running with
// them, or stopped.
func (e Env) firewall(rules []nixwriter.FirewallRule, disabled bool) error {
	var b strings.Builder
	for _, r := range rules {
		n, err := r.Normalize()
		if err != nil {
			return err
		}
		from := n.From
		if from == "" {
			from = "any"
		}
		fmt.Fprintf(&b, "%s %s %s %s\n", n.Action, n.Proto, n.Ports(), from)
	}
	changed, err := writeIfChanged(e.path(FirewallRulesFile), b.String(), 0644)
	if err != nil {
		return err
	}
	flag := e.path(FirewallDisabledFile)
	if disabled {
		if _, err := writeIfChanged(flag, "The firewall is switched off in the web interface.\n", 0644); err != nil {
			return err
		}
	} else if _, err := removeIfPresent(flag); err != nil {
		return err
	}

	// Early boot: systemd starts the firewall itself (unless the flag is
	// there), and it reads the file then.
	if e.run("systemctl", "is-active", "--quiet", "multi-user.target") != nil &&
		e.run("systemctl", "is-active", "--quiet", "network.target") != nil {
		return nil
	}
	active := e.run("systemctl", "is-active", "--quiet", "firewall.service") == nil
	switch {
	case disabled && active:
		return e.run("systemctl", "stop", "firewall.service")
	case !disabled && !active:
		return e.run("systemctl", "start", "firewall.service")
	case !disabled && changed:
		return e.run("systemctl", "reload", "firewall.service")
	}
	return nil
}

// FirewallServicesDir holds one file per service that the web interface
// switches on and that needs ports of its own ("tcp 21", "tcp 40000:40100").
const FirewallServicesDir = "/var/lib/dplaneos/system/firewall-services.d"

var servicePortRe = regexp.MustCompile(`^(tcp|udp) [0-9]{1,5}(:[0-9]{1,5})?$`)

// SetServicePorts opens the ports a service needs while it is switched on
// ("tcp 21", "tcp 40000:40100"), or closes them again (no ports).
func (e Env) SetServicePorts(service string, ports []string) error {
	if service == "" || strings.ContainsAny(service, "/. \n") {
		return fmt.Errorf("invalid service name %q", service)
	}
	file := filepath.Join(e.path(FirewallServicesDir), service)
	var changed bool
	var err error
	if len(ports) == 0 {
		changed, err = removeIfPresent(file)
	} else {
		for _, p := range ports {
			if !servicePortRe.MatchString(p) {
				return fmt.Errorf("invalid port %q", p)
			}
		}
		changed, err = writeIfChanged(file, strings.Join(ports, "\n")+"\n", 0644)
	}
	if err != nil || !changed {
		return err
	}
	if e.run("systemctl", "is-active", "--quiet", "firewall.service") != nil {
		return nil // reads the file when it starts
	}
	return e.run("systemctl", "reload", "firewall.service")
}

// webCertificate makes nginx serve the activated certificate: its content
// is copied to WebCertFile and WebKeyFile (so a renewed certificate is
// picked up the next time this runs) and nginx reloads. A certificate and
// key that do not belong together are refused before anything is touched.
func (e Env) webCertificate(certPath, keyPath string) error {
	if certPath == "" || keyPath == "" {
		return nil // the certificate in place (self-signed at first) stays
	}
	cert, err := os.ReadFile(e.path(certPath))
	if err != nil {
		return err
	}
	key, err := os.ReadFile(e.path(keyPath))
	if err != nil {
		return err
	}
	if _, err := tls.X509KeyPair(cert, key); err != nil {
		return fmt.Errorf("%s and %s are not a usable certificate and key: %w", certPath, keyPath, err)
	}
	keyChanged, err := writeIfChanged(e.path(WebKeyFile), string(key), 0640)
	if err != nil {
		return err
	}
	certChanged, err := writeIfChanged(e.path(WebCertFile), string(cert), 0644)
	if err != nil {
		return err
	}
	// nginx runs as its own user and reads the key through its group.
	if g, err := user.LookupGroup("nginx"); err == nil {
		if gid, err := strconv.Atoi(g.Gid); err == nil {
			_ = os.Chown(e.path(WebKeyFile), 0, gid)
			_ = os.Chown(filepath.Dir(e.path(WebKeyFile)), 0, gid)
		}
	}
	if !keyChanged && !certChanged {
		return nil
	}
	if e.run("systemctl", "is-active", "--quiet", "nginx.service") != nil {
		return nil // reads the files when it starts
	}
	return e.run("systemctl", "reload", "nginx.service")
}

// SSHConfigFile is read by sshd before the system's own settings (an
// Include at the top of sshd_config, nixos/module.nix), so what is set here
// takes precedence.
const SSHConfigFile = "/var/lib/dplaneos/system/sshd_config.d/10-dplane.conf"

// ssh writes the SSH daemon settings, has sshd check them, reloads it, and
// opens the port in the firewall. Settings sshd does not accept are taken
// back: a broken configuration would lock everybody out at the next restart.
func (e Env) ssh(port int, passwordAuth *bool, permitRootLogin string) error {
	var b strings.Builder
	b.WriteString("# Written by DPlaneOS (SSH page).\n")
	listen := 22
	if port != 0 {
		if port < 1 || port > 65535 {
			return fmt.Errorf("invalid port %d", port)
		}
		listen = port
	}
	// Always written: the system configuration names no port of its own,
	// so that this one replaces 22 instead of being added to it.
	fmt.Fprintf(&b, "Port %d\n", listen)
	if passwordAuth != nil {
		v := "no"
		if *passwordAuth {
			v = "yes"
		}
		// Both, or neither: keyboard-interactive is password sign-in through PAM.
		fmt.Fprintf(&b, "PasswordAuthentication %s\nKbdInteractiveAuthentication %s\n", v, v)
	}
	switch permitRootLogin {
	case "":
	case "yes", "no", "prohibit-password", "forced-commands-only":
		fmt.Fprintf(&b, "PermitRootLogin %s\n", permitRootLogin)
	default:
		return fmt.Errorf("invalid PermitRootLogin value %q", permitRootLogin)
	}

	file := e.path(SSHConfigFile)
	previous, readErr := os.ReadFile(file)
	changed, err := writeIfChanged(file, b.String(), 0644)
	if err != nil {
		return err
	}
	// The firewall follows the port (and reads the file itself at boot).
	if err := e.SetServicePorts("ssh", []string{fmt.Sprintf("tcp %d", listen)}); err != nil {
		return err
	}
	if !changed || e.run("systemctl", "is-active", "--quiet", "sshd.service") != nil {
		return nil // not running (early boot): it reads the file when it starts
	}
	if err := e.run("sshd", "-t"); err != nil {
		if readErr == nil {
			_, _ = writeIfChanged(file, string(previous), 0644)
		} else {
			os.Remove(file)
		}
		return fmt.Errorf("sshd does not accept the settings (not applied): %w", err)
	}
	return e.run("systemctl", "reload", "sshd.service")
}
