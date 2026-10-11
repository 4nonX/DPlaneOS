package sysapply

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"dplaned/internal/nixwriter"
)

type fakeSystem struct {
	root   string
	active map[string]bool
	cmds   []string
}

func newFake(t *testing.T) (*fakeSystem, Env) {
	t.Helper()
	f := &fakeSystem{root: t.TempDir(), active: map[string]bool{"multi-user.target": true, "firewall.service": true}}
	for path, content := range map[string]string{
		"/proc/sys/kernel/hostname":              "nixos\n",
		"/proc/sys/vm/swappiness":                "60\n",
		"/proc/sys/net/ipv4/tcp_rmem":            "4096\t131072\t6291456\n",
		"/sys/module/zfs/parameters/zfs_arc_max": "0\n",
		"/etc/zoneinfo/Europe/Berlin":            "tz",
		"/etc/zoneinfo/UTC":                      "tz",
	} {
		p := filepath.Join(f.root, path)
		if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}
	return f, Env{Root: f.root, Run: f.run}
}

func (f *fakeSystem) run(name string, args ...string) error {
	line := name + " " + strings.Join(args, " ")
	if name == "systemctl" && args[0] == "is-active" {
		if f.active[args[len(args)-1]] {
			return nil
		}
		return errors.New("inactive")
	}
	f.cmds = append(f.cmds, line)
	if name == "systemctl" {
		switch args[0] {
		case "stop":
			f.active[args[1]] = false
		case "start":
			f.active[args[1]] = true
		}
	}
	return nil
}

func (f *fakeSystem) read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(f.root, path))
	if err != nil {
		return "<" + err.Error() + ">"
	}
	return string(b)
}

func (f *fakeSystem) ran(prefix string) int {
	n := 0
	for _, c := range f.cmds {
		if strings.HasPrefix(c, prefix) {
			n++
		}
	}
	return n
}

func TestApplyDefaultsOnAFreshSystem(t *testing.T) {
	f, env := newFake(t)
	if err := env.Apply(nixwriter.DPlaneState{}); err != nil {
		t.Fatal(err)
	}
	// The image's placeholder name is replaced; nothing else is touched.
	if got := f.read(t, "/proc/sys/kernel/hostname"); got != "dplaneos" {
		t.Errorf("hostname: %q", got)
	}
	if got := f.read(t, "/etc/hostname"); got != "dplaneos\n" {
		t.Errorf("/etc/hostname: %q", got)
	}
	if _, err := os.Lstat(filepath.Join(f.root, "/etc/localtime")); err == nil {
		t.Error("a time zone was set although none is saved")
	}
	if got := f.read(t, "/proc/sys/vm/swappiness"); got != "60\n" {
		t.Errorf("swappiness touched: %q", got)
	}
	if got := f.read(t, FirewallRulesFile); got != "" {
		t.Errorf("firewall rules: %q", got)
	}
	if _, err := os.Stat(filepath.Join(f.root, FirewallDisabledFile)); err == nil {
		t.Error("firewall disabled without being asked")
	}
}

func TestApplySettingsAndOnlyWhatChanged(t *testing.T) {
	f, env := newFake(t)
	st := nixwriter.DPlaneState{
		Hostname:    "nas1",
		Timezone:    "Europe/Berlin",
		NTPServers:  []string{"time.example.org", "10.0.0.1"},
		Sysctl:      map[string]string{"vm.swappiness": "10", "net.ipv4.tcp_rmem": "4096 131072 6291456"},
		ZFSArcMax:   8 << 30,
		FirewallTCP: []int{8080, 8443},
		FirewallUDP: []int{51820},
		FirewallRules: []nixwriter.FirewallRule{
			{Action: "allow", Proto: "tcp", Port: 5432, From: "10.0.0.0/24"},
			{Action: "deny", Proto: "tcp", Port: 22, From: "203.0.113.7"},
			{Action: "allow", Proto: "tcp", Port: 6000, PortEnd: 6010},
		},
	}
	if err := env.Apply(st); err != nil {
		t.Fatal(err)
	}
	if got := f.read(t, "/proc/sys/kernel/hostname"); got != "nas1" {
		t.Errorf("hostname: %q", got)
	}
	if got, _ := os.Readlink(filepath.Join(f.root, "/etc/localtime")); filepath.ToSlash(got) != "/etc/zoneinfo/Europe/Berlin" {
		t.Errorf("localtime -> %q", got)
	}
	if got := f.read(t, "/run/systemd/timesyncd.conf.d/10-dplane.conf"); !strings.Contains(got, "[Time]\nNTP=time.example.org 10.0.0.1\n") {
		t.Errorf("timesyncd drop-in: %q", got)
	}
	if got := f.read(t, "/proc/sys/vm/swappiness"); got != "10" {
		t.Errorf("swappiness: %q", got)
	}
	// Same value, different whitespace: left alone.
	if got := f.read(t, "/proc/sys/net/ipv4/tcp_rmem"); got != "4096\t131072\t6291456\n" {
		t.Errorf("tcp_rmem rewritten: %q", got)
	}
	if got := f.read(t, "/sys/module/zfs/parameters/zfs_arc_max"); got != "8589934592" {
		t.Errorf("zfs_arc_max: %q", got)
	}
	// Deny rules first.
	want := "deny tcp 22 203.0.113.7\nallow tcp 5432 10.0.0.0/24\nallow tcp 6000:6010 any\nallow tcp 8080 any\nallow tcp 8443 any\nallow udp 51820 any\n"
	if got := f.read(t, FirewallRulesFile); got != want {
		t.Errorf("firewall rules:\n%s\nwant\n%s", got, want)
	}
	if f.ran("systemctl reload firewall.service") < 1 || f.ran("systemctl --no-block try-restart systemd-timesyncd.service") != 1 {
		t.Errorf("commands: %v", f.cmds)
	}

	// Applying the same settings again does nothing at all.
	f.cmds = nil
	if err := env.Apply(st); err != nil {
		t.Fatal(err)
	}
	if len(f.cmds) != 0 {
		t.Errorf("second apply ran: %v", f.cmds)
	}

	// Switching the firewall off stops it and keeps it off at boot;
	// switching it on again starts it.
	st.FirewallDisabled = true
	if err := env.Apply(st); err != nil {
		t.Fatal(err)
	}
	if f.active["firewall.service"] || f.read(t, FirewallDisabledFile) == "" {
		t.Errorf("firewall not switched off: %v", f.cmds)
	}
	st.FirewallDisabled = false
	if err := env.Apply(st); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(f.root, FirewallDisabledFile)); err == nil || !f.active["firewall.service"] {
		t.Errorf("firewall not switched on again: %v", f.cmds)
	}

	// Removing the NTP servers removes the drop-in.
	st.NTPServers = nil
	if err := env.Apply(st); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(f.root, "/run/systemd/timesyncd.conf.d/10-dplane.conf")); err == nil {
		t.Error("NTP drop-in still there")
	}
}

func TestApplyReportsWhatFailedAndDoesTheRest(t *testing.T) {
	f, env := newFake(t)
	err := env.Apply(nixwriter.DPlaneState{
		Hostname: "nas1",
		Timezone: "Mars/Olympus",
		Sysctl:   map[string]string{"no.such.key": "1", "vm.swappiness": "5"},
		FirewallRules: []nixwriter.FirewallRule{
			{Action: "allow", Proto: "tcp", Port: 80, From: "not a network"},
		},
	})
	if err == nil {
		t.Fatal("no error")
	}
	for _, part := range []string{"time zone", "Mars/Olympus", "no.such.key", "firewall"} {
		if !strings.Contains(err.Error(), part) {
			t.Errorf("error lacks %q: %v", part, err)
		}
	}
	if got := f.read(t, "/proc/sys/kernel/hostname"); got != "nas1" {
		t.Errorf("hostname not applied: %q", got)
	}
	if got := f.read(t, "/proc/sys/vm/swappiness"); got != "5" {
		t.Errorf("swappiness not applied: %q", got)
	}
}

func TestEarlyBootLeavesTheFirewallToSystemd(t *testing.T) {
	f, env := newFake(t)
	f.active = map[string]bool{} // nothing is up yet
	if err := env.Apply(nixwriter.DPlaneState{FirewallTCP: []int{8080}}); err != nil {
		t.Fatal(err)
	}
	if got := f.read(t, FirewallRulesFile); got != "allow tcp 8080 any\n" {
		t.Errorf("rules: %q", got)
	}
	if f.ran("systemctl start") != 0 || f.ran("systemctl reload") != 0 {
		t.Errorf("early boot must not start services: %v", f.cmds)
	}
}

func TestServicePorts(t *testing.T) {
	f, env := newFake(t)
	if err := env.SetServicePorts("ftp", []string{"tcp 21", "tcp 40000:40100"}); err != nil {
		t.Fatal(err)
	}
	if got := f.read(t, FirewallServicesDir+"/ftp"); got != "tcp 21\ntcp 40000:40100\n" {
		t.Errorf("file: %q", got)
	}
	if f.ran("systemctl reload firewall.service") != 1 {
		t.Errorf("no reload: %v", f.cmds)
	}
	if err := env.SetServicePorts("ftp", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(f.root, FirewallServicesDir, "ftp")); err == nil {
		t.Error("file still there")
	}
	for _, bad := range [][]string{{"tcp 21; reboot"}, {"icmp 1"}, {"tcp"}} {
		if err := env.SetServicePorts("x", bad); err == nil {
			t.Errorf("%v accepted", bad)
		}
	}
	if err := env.SetServicePorts("../x", []string{"tcp 1"}); err == nil {
		t.Error("path in service name accepted")
	}
}

func TestSSHSettings(t *testing.T) {
	f, env := newFake(t)
	f.active["sshd.service"] = true
	read := func() string { return f.read(t, SSHConfigFile) }

	// Nothing set: port 22, opened in the firewall, the rest left to the system.
	if err := env.Apply(nixwriter.DPlaneState{}); err != nil {
		t.Fatal(err)
	}
	if got := read(); !strings.Contains(got, "Port 22\n") || strings.Contains(got, "PasswordAuthentication") || strings.Contains(got, "PermitRootLogin") {
		t.Errorf("default file: %q", got)
	}
	if got := f.read(t, FirewallServicesDir+"/ssh"); got != "tcp 22\n" {
		t.Errorf("firewall: %q", got)
	}

	yes := true
	f.cmds = nil
	if err := env.Apply(nixwriter.DPlaneState{SSHPort: 2222, SSHPasswordAuth: &yes, SSHPermitRootLogin: "prohibit-password"}); err != nil {
		t.Fatal(err)
	}
	got := read()
	for _, want := range []string{"Port 2222\n", "PasswordAuthentication yes\n", "KbdInteractiveAuthentication yes\n", "PermitRootLogin prohibit-password\n"} {
		if !strings.Contains(got, want) {
			t.Errorf("file lacks %q: %q", want, got)
		}
	}
	if strings.Contains(got, "Port 22\n") {
		t.Errorf("the old port is still listed: %q", got)
	}
	if got := f.read(t, FirewallServicesDir+"/ssh"); got != "tcp 2222\n" {
		t.Errorf("firewall: %q", got)
	}
	if f.ran("sshd -t") != 1 || f.ran("systemctl reload sshd.service") != 1 {
		t.Errorf("commands: %v", f.cmds)
	}

	// Settings sshd refuses are taken back.
	before := read()
	env.Run = func(name string, args ...string) error {
		if name == "sshd" {
			return errors.New("bad configuration")
		}
		return f.run(name, args...)
	}
	err := env.Apply(nixwriter.DPlaneState{SSHPort: 2200})
	if err == nil || !strings.Contains(err.Error(), "sshd does not accept") {
		t.Fatalf("error: %v", err)
	}
	if read() != before {
		t.Errorf("refused settings were kept: %q", read())
	}
	if err := env.Apply(nixwriter.DPlaneState{SSHPermitRootLogin: "maybe"}); err == nil {
		t.Error("invalid PermitRootLogin accepted")
	}
}

func TestUPS(t *testing.T) {
	f, env := newFake(t)
	nut := func(name string) string { return f.read(t, NUTDir+"/"+name) }
	gone := func(name string) bool {
		_, err := os.Stat(filepath.Join(f.root, NUTDir, name))
		return err != nil
	}

	// No UPS: no configuration, no service.
	if err := env.Apply(nixwriter.DPlaneState{}); err != nil {
		t.Fatal(err)
	}
	if !gone("ups.conf") || !gone("upsmon.conf") || f.ran("systemctl restart dplaneos-ups") != 0 {
		t.Fatalf("something was set up without a UPS: %v", f.cmds)
	}

	// A UPS on USB: driver, local server, monitor.
	f.cmds = nil
	st := nixwriter.DPlaneState{UPSMode: "usb", UPSLowBattery: 25, UPSFinalDelay: 15, UPSAction: "shutdown"}
	if err := env.Apply(st); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"[ups]\n", "driver = usbhid-ups\n", "port = auto\n", "override.battery.charge.low = 25\n", "ignorelb\n"} {
		if !strings.Contains(nut("ups.conf"), want) {
			t.Errorf("ups.conf lacks %q:\n%s", want, nut("ups.conf"))
		}
	}
	mon := nut("upsmon.conf")
	for _, want := range []string{"MONITOR ups@localhost 1 upsmon ", " primary\n", "FINALDELAY 15\n", `SHUTDOWNCMD "/run/current-system/sw/bin/systemctl poweroff"`} {
		if !strings.Contains(mon, want) {
			t.Errorf("upsmon.conf lacks %q:\n%s", want, mon)
		}
	}
	if !strings.Contains(nut("upsd.conf"), "LISTEN 127.0.0.1 3493") {
		t.Errorf("the local server must not listen on the network:\n%s", nut("upsd.conf"))
	}
	want := []string{"systemctl restart dplaneos-upsdrv.service", "systemctl restart dplaneos-upsd.service", "systemctl restart dplaneos-upsmon.service"}
	if strings.Join(f.cmds, "|") != strings.Join(want, "|") {
		t.Errorf("commands: %v", f.cmds)
	}
	for _, u := range []string{"dplaneos-upsdrv.service", "dplaneos-upsd.service", "dplaneos-upsmon.service"} {
		f.active[u] = true
	}

	// Only the policy changes: the driver keeps running.
	f.cmds = nil
	st.UPSFinalDelay, st.UPSAction = 60, "hibernate"
	if err := env.Apply(st); err != nil {
		t.Fatal(err)
	}
	if f.ran("systemctl restart dplaneos-upsdrv") != 0 || f.ran("systemctl restart dplaneos-upsmon.service") != 1 {
		t.Errorf("commands: %v", f.cmds)
	}
	if !strings.Contains(nut("upsmon.conf"), "systemctl hibernate") {
		t.Errorf("action not applied:\n%s", nut("upsmon.conf"))
	}

	// A UPS on another machine: only the monitor, as secondary.
	f.cmds = nil
	st = nixwriter.DPlaneState{UPSMode: "remote", UPSName: "rack", UPSHost: "10.0.0.5", UPSUser: "monuser", UPSPassword: "s3cret", UPSFinalDelay: 5}
	if err := env.Apply(st); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(nut("upsmon.conf"), "MONITOR rack@10.0.0.5 1 monuser s3cret secondary\n") || !gone("ups.conf") || !gone("upsd.conf") {
		t.Errorf("remote: %s", nut("upsmon.conf"))
	}
	if f.ran("systemctl stop dplaneos-upsdrv.service") != 1 || f.ran("systemctl stop dplaneos-upsd.service") != 1 {
		t.Errorf("driver and server keep running: %v", f.cmds)
	}

	// Values that would break out of the file are refused.
	for _, bad := range []nixwriter.DPlaneState{
		{UPSMode: "remote", UPSName: "rack", UPSHost: "10.0.0.5", UPSUser: "u", UPSPassword: "a b"},
		{UPSMode: "remote", UPSName: "rack", UPSHost: "10.0.0.5\nSHUTDOWNCMD x", UPSUser: "u", UPSPassword: "p"},
		{UPSMode: "driver", UPSDriver: "apcsmart", UPSPort: "/dev/ttyS0\n[evil]"},
		{UPSMode: "driver", UPSDriver: "x; reboot", UPSPort: "auto"},
		{UPSMode: "usb", UPSName: "a b"},
		{UPSMode: "carrier-pigeon"},
	} {
		if err := env.Apply(bad); err == nil || !strings.Contains(err.Error(), "UPS") {
			t.Errorf("%+v accepted: %v", bad, err)
		}
	}

	// Off again: everything stops and the configuration is gone.
	f.cmds = nil
	if err := env.Apply(nixwriter.DPlaneState{}); err != nil {
		t.Fatal(err)
	}
	if !gone("upsmon.conf") || f.ran("systemctl stop dplaneos-upsmon.service") != 1 {
		t.Errorf("not switched off: %v", f.cmds)
	}
}
