package netconf

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"dplaned/internal/reconciler"
	"dplaned/internal/testdb"
)

func keys(m map[string]string) string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return strings.Join(out, " ")
}

func TestRenderOneFilePerInterface(t *testing.T) {
	files, err := Render(Config{
		Interfaces: []Interface{
			{Name: "eth0", CIDR: "192.168.1.10/24", Gateway: "192.168.1.1"},
			{Name: "eth0.100", CIDR: "10.0.100.1/24"},
			{Name: "eth3", DHCP: true},
		},
		Bonds: []Bond{{Name: "bond0", Mode: "lacp", Slaves: []string{"eth1", "eth2"}}},
		VLANs: []VLAN{{Name: "eth0.100", Parent: "eth0", VID: 100}, {Name: "eth0.200", Parent: "eth0", VID: 200}, {Name: "bond0.7", Parent: "bond0", VID: 7}},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := "10-dplane-bond0.7.netdev 10-dplane-bond0.7.network 10-dplane-bond0.netdev 10-dplane-bond0.network " +
		"10-dplane-eth0.100.netdev 10-dplane-eth0.100.network 10-dplane-eth0.200.netdev 10-dplane-eth0.200.network " +
		"10-dplane-eth0.network 10-dplane-eth1.network 10-dplane-eth2.network 10-dplane-eth3.network"
	if got := keys(files); got != want {
		t.Fatalf("files:\n got %s\nwant %s", got, want)
	}
	has := func(file string, parts ...string) {
		t.Helper()
		for _, p := range parts {
			if !strings.Contains(files[file], p) {
				t.Errorf("%s lacks %q:\n%s", file, p, files[file])
			}
		}
	}
	lacks := func(file string, parts ...string) {
		t.Helper()
		for _, p := range parts {
			if strings.Contains(files[file], p) {
				t.Errorf("%s contains %q:\n%s", file, p, files[file])
			}
		}
	}
	// The parent's one file carries its address and both VLANs.
	has("10-dplane-eth0.network", "Name=eth0\n", "DHCP=no\n", "Address=192.168.1.10/24\n", "Gateway=192.168.1.1\n", "VLAN=eth0.100\n", "VLAN=eth0.200\n")
	has("10-dplane-eth0.100.network", "Address=10.0.100.1/24\n")
	has("10-dplane-eth0.100.netdev", "Kind=vlan\n", "Id=100\n")
	// A VLAN without address does not ask for one.
	has("10-dplane-eth0.200.network", "DHCP=no\n", "LinkLocalAddressing=no\n")
	// Members only join the bond.
	has("10-dplane-eth1.network", "Bond=bond0\n")
	lacks("10-dplane-eth1.network", "DHCP", "Address")
	// A bond without address uses DHCP, and monitors its links.
	has("10-dplane-bond0.network", "DHCP=yes\n", "VLAN=bond0.7\n")
	has("10-dplane-bond0.netdev", "Kind=bond\n", "Mode=802.3ad\n", "MIIMonitorSec=100ms\n")
	has("10-dplane-eth3.network", "DHCP=yes\n")
}

func TestRenderRejectsBadInput(t *testing.T) {
	for name, c := range map[string]Config{
		"name with newline": {Interfaces: []Interface{{Name: "eth0\n[Network]", DHCP: true}}},
		"name with slash":   {Interfaces: []Interface{{Name: "../x", DHCP: true}}},
		"bad address":       {Interfaces: []Interface{{Name: "eth0", CIDR: "1.2.3.4"}}},
		"bad gateway":       {Interfaces: []Interface{{Name: "eth0", CIDR: "1.2.3.4/24", Gateway: "x\nDNS=6.6.6.6"}}},
		"bad mode":          {Bonds: []Bond{{Name: "bond0", Mode: "fast"}}},
		"two bonds":         {Bonds: []Bond{{Name: "bond0", Mode: "lacp", Slaves: []string{"eth1"}}, {Name: "bond1", Mode: "lacp", Slaves: []string{"eth1"}}}},
		"vlan id":           {VLANs: []VLAN{{Name: "eth0.5000", Parent: "eth0", VID: 5000}}},
	} {
		if _, err := Render(c); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if _, err := RenderDNS([]string{"1.1.1.1\nDomains=evil"}, nil); err == nil {
		t.Error("DNS server with newline accepted")
	}
	if _, err := RenderDNS(nil, []string{"lan evil"}); err == nil {
		t.Error("search domain with space accepted")
	}
	if s, _ := RenderDNS(nil, nil); s != "" {
		t.Errorf("no servers must give no drop-in: %q", s)
	}
}

func ls(t *testing.T, dir string) string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	var out []string
	for _, e := range entries {
		out = append(out, e.Name())
	}
	return strings.Join(out, " ")
}

func TestApplyFromDatabase(t *testing.T) {
	db := testdb.Open(t)
	if err := reconciler.EnsureSchema(db); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	var reloads []string
	target := Target{
		Persist: filepath.Join(root, "persist"), Live: filepath.Join(root, "live"), Resolved: filepath.Join(root, "resolved"),
		Reload: func(network, dns bool) error {
			reloads = append(reloads, map[bool]string{true: "net"}[network]+map[bool]string{true: "dns"}[dns])
			return nil
		},
	}
	// Files of others in the same directories are never touched.
	for _, f := range []string{"live/99-ethernet-default-dhcp.network", "live/50-other.netdev", "resolved/20-other.conf"} {
		os.MkdirAll(filepath.Dir(filepath.Join(root, f)), 0755)
		os.WriteFile(filepath.Join(root, f), []byte("x"), 0644)
	}
	apply := func(tg Target) bool {
		t.Helper()
		cfg, err := Load(db)
		if err != nil {
			t.Fatal(err)
		}
		changed, err := tg.Apply(cfg)
		if err != nil {
			t.Fatal(err)
		}
		return changed
	}

	// Nothing configured: nothing written, nobody notified.
	if apply(target) || len(reloads) != 0 || ls(t, target.Persist) != "" {
		t.Fatalf("empty configuration wrote something: %v %q", reloads, ls(t, target.Persist))
	}

	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(reconciler.SaveStaticIP(db, "eth0", "192.168.1.10/24", "192.168.1.1"))
	must(reconciler.SaveVLAN(db, "eth0.100", "eth0", 100))
	must(reconciler.SaveBond(db, "bond0", []string{"eth1", "eth2"}, "active-backup"))
	must(SaveDNS(db, []string{"9.9.9.9", "1.1.1.1"}, []string{"lan"}))
	if !apply(target) {
		t.Fatal("no change reported")
	}
	want := "10-dplane-bond0.netdev 10-dplane-bond0.network 10-dplane-eth0.100.netdev 10-dplane-eth0.100.network 10-dplane-eth0.network 10-dplane-eth1.network 10-dplane-eth2.network"
	if got := ls(t, target.Live); got != want+" 50-other.netdev 99-ethernet-default-dhcp.network" {
		t.Errorf("live: %s", got)
	}
	if got := ls(t, target.Persist); got != want[:strings.Index(want, " 10-dplane-eth0.100.netdev")]+" 10-dplane-dns.conf"+want[strings.Index(want, " 10-dplane-eth0.100.netdev"):] {
		t.Errorf("persist: %s", got)
	}
	dns, _ := os.ReadFile(filepath.Join(target.Resolved, "10-dplane-dns.conf"))
	if !strings.Contains(string(dns), "DNS=9.9.9.9 1.1.1.1\n") || !strings.Contains(string(dns), "Domains=lan\n") {
		t.Errorf("dns drop-in: %s", dns)
	}
	if len(reloads) != 1 || reloads[0] != "netdns" {
		t.Errorf("reloads: %v", reloads)
	}

	// Unchanged: no write, no reload.
	if apply(target) || len(reloads) != 1 {
		t.Errorf("second apply changed something: %v", reloads)
	}

	// Deleting the VLAN and the bond leaves nothing of them behind, in
	// either directory; the members' files go too (they fall back to the
	// system default).
	must(reconciler.DeleteVLAN(db, "eth0.100"))
	must(reconciler.DeleteBond(db, "bond0"))
	must(SaveDNS(db, nil, nil))
	apply(target)
	if got := ls(t, target.Live); got != "10-dplane-eth0.network 50-other.netdev 99-ethernet-default-dhcp.network" {
		t.Errorf("live after delete: %s", got)
	}
	if got := ls(t, target.Persist); got != "10-dplane-eth0.network" {
		t.Errorf("persist after delete: %s", got)
	}
	if got := ls(t, target.Resolved); got != "20-other.conf" {
		t.Errorf("resolved after delete: %s", got)
	}
	eth0, _ := os.ReadFile(filepath.Join(target.Live, "10-dplane-eth0.network"))
	if strings.Contains(string(eth0), "VLAN=") {
		t.Errorf("parent still names the VLAN:\n%s", eth0)
	}
	if reloads[len(reloads)-1] != "netdns" {
		t.Errorf("reloads: %v", reloads)
	}

	// A change that is being tried is not written to the persistent copy.
	trial := target
	trial.Persist = ""
	cfg, _ := Load(db)
	if _, err := trial.Apply(cfg.With(Interface{Name: "eth0", DHCP: true})); err != nil {
		t.Fatal(err)
	}
	live, _ := os.ReadFile(filepath.Join(target.Live, "10-dplane-eth0.network"))
	kept, _ := os.ReadFile(filepath.Join(target.Persist, "10-dplane-eth0.network"))
	if !strings.Contains(string(live), "DHCP=yes") || !strings.Contains(string(kept), "Address=192.168.1.10/24") {
		t.Errorf("trial:\nlive %s\npersist %s", live, kept)
	}
	// Reverting is applying the saved configuration again.
	apply(trial)
	live, _ = os.ReadFile(filepath.Join(target.Live, "10-dplane-eth0.network"))
	if string(live) != string(kept) {
		t.Errorf("revert: %s", live)
	}
}
