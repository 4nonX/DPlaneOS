package networkdwriter

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

type fakeSys struct {
	links   map[string]bool
	deleted []string
	ran     []string
}

func testWriter(t *testing.T) (*Writer, *fakeSys) {
	t.Helper()
	dir := t.TempDir()
	fs := &fakeSys{links: map[string]bool{"eth0": true, "eth1": true, "eth2": true}}
	w := &Writer{
		dir:          filepath.Join(dir, "network"),
		statePath:    filepath.Join(dir, "state.json"),
		resolvedPath: filepath.Join(dir, "resolved.conf.d", "50-dplane.conf"),
		networkdUp:   true,
	}
	w.run = func(name string, args ...string) ([]byte, error) {
		cmd := name + " " + strings.Join(args, " ")
		fs.ran = append(fs.ran, cmd)
		// networkd creates the bonds that have a netdev file.
		if cmd == "networkctl reload" || cmd == "systemctl restart systemd-networkd.service" {
			es, _ := os.ReadDir(w.dir)
			for _, e := range es {
				if n, ok := strings.CutSuffix(strings.TrimPrefix(e.Name(), FilePrefix), ".netdev"); ok && strings.HasPrefix(n, "bond") {
					fs.links[n] = true
				}
			}
		}
		return nil, nil
	}
	w.linkExists = func(n string) bool { return fs.links[n] }
	w.linkDel = func(n string) error { fs.deleted = append(fs.deleted, n); delete(fs.links, n); return nil }
	return w, fs
}

func read(t *testing.T, w *Writer, f string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(w.dir, f))
	if err != nil {
		t.Fatalf("read %s: %v", f, err)
	}
	return string(b)
}

func files(t *testing.T, w *Writer) []string {
	t.Helper()
	es, _ := os.ReadDir(w.dir)
	var out []string
	for _, e := range es {
		out = append(out, e.Name())
	}
	return out
}

// A VLAN goes into its parent's one file next to the parent's address: with
// a separate file (as before) networkd used that file alone and the parent
// lost its address.
func TestRenderVLANKeepsParentAddress(t *testing.T) {
	s := State{
		Interfaces: map[string]Iface{"eth0": {Addresses: []string{"192.168.1.10/24"}, Gateway: "192.168.1.1"}},
		VLANs:      map[string]VLAN{"eth0.100": {Parent: "eth0", ID: 100}},
	}
	f := s.render()
	eth0 := f["05-dplane-eth0.network"]
	for _, want := range []string{"Name=eth0", "Address=192.168.1.10/24", "Gateway=192.168.1.1", "VLAN=eth0.100", "DHCP=no"} {
		if !strings.Contains(eth0, want) {
			t.Errorf("eth0 file lacks %q:\n%s", want, eth0)
		}
	}
	if !strings.Contains(f["05-dplane-eth0.100.netdev"], "Kind=vlan") || !strings.Contains(f["05-dplane-eth0.100.netdev"], "Id=100") {
		t.Errorf("netdev: %s", f["05-dplane-eth0.100.netdev"])
	}
	if len(f) != 3 { // eth0.network, eth0.100.netdev, eth0.100.network
		t.Errorf("files: %v", f)
	}
}

func TestRenderBond(t *testing.T) {
	s := State{
		Interfaces: map[string]Iface{"bond0": {DHCP: true}},
		Bonds:      map[string]Bond{"bond0": {Mode: "active-backup", Slaves: []string{"eth1", "eth2"}}},
	}
	f := s.render()
	if !strings.Contains(f["05-dplane-bond0.netdev"], "Mode=active-backup") || !strings.Contains(f["05-dplane-bond0.netdev"], "MIIMonitorSec=100ms") {
		t.Errorf("netdev: %s", f["05-dplane-bond0.netdev"])
	}
	if c := f["05-dplane-eth1.network"]; !strings.Contains(c, "Bond=bond0") || strings.Contains(c, "DHCP") {
		t.Errorf("member: %s", c)
	}
	if !strings.Contains(f["05-dplane-bond0.network"], "DHCP=yes") {
		t.Errorf("bond network: %s", f["05-dplane-bond0.network"])
	}
}

func TestValidate(t *testing.T) {
	bad := []State{
		{Interfaces: map[string]Iface{"eth0": {Addresses: []string{"10.0.0.1"}}}},
		{Interfaces: map[string]Iface{"eth0": {Gateway: "nope"}}},
		{Bonds: map[string]Bond{"bond0": {Mode: "lacp-ish", Slaves: []string{"eth1"}}}},
		{Bonds: map[string]Bond{"bond0": {Mode: "802.3ad"}}},
		{Bonds: map[string]Bond{"b0": {Mode: "802.3ad", Slaves: []string{"eth1"}}, "b1": {Mode: "802.3ad", Slaves: []string{"eth1"}}}},
		{Bonds: map[string]Bond{"bond0": {Mode: "802.3ad", Slaves: []string{"eth1"}}}, Interfaces: map[string]Iface{"eth1": {DHCP: true}}},
		{Bonds: map[string]Bond{"bond0": {Mode: "802.3ad", Slaves: []string{"eth1"}}}, VLANs: map[string]VLAN{"eth1.5": {Parent: "eth1", ID: 5}}},
		{VLANs: map[string]VLAN{"v1": {Parent: "eth0", ID: 5}, "v2": {Parent: "eth0", ID: 5}}},
		{VLANs: map[string]VLAN{"v1": {Parent: "eth0", ID: 5000}}},
		{DNS: DNS{Servers: []string{"dns.example"}}},
		{DNS: DNS{Servers: []string{"1.1.1.1"}, Search: []string{"bad domain"}}},
		{Interfaces: map[string]Iface{"eth0;rm": {}}},
	}
	for i, s := range bad {
		if s.Validate() == nil {
			t.Errorf("%d: accepted %+v", i, s)
		}
	}
	good := State{
		Interfaces: map[string]Iface{"bond0": {Addresses: []string{"10.0.0.2/24", "fd00::2/64"}, Routes: []Route{{Destination: "10.9.0.0/16", Gateway: "10.0.0.254"}}}},
		Bonds:      map[string]Bond{"bond0": {Mode: "802.3ad", Slaves: []string{"eth1", "eth2"}}},
		VLANs:      map[string]VLAN{"bond0.20": {Parent: "bond0", ID: 20}},
		DNS:        DNS{Servers: []string{"10.0.0.1"}, Search: []string{"lan", "corp.example."}},
	}
	if err := good.Validate(); err != nil {
		t.Errorf("good state: %v", err)
	}
}

// Apply writes the files, removes the ones no longer needed (also those of
// earlier versions), deletes devices that were removed or changed, and has
// networkd reload and reconfigure the interfaces that changed.
func TestApply(t *testing.T) {
	w, fs := testWriter(t)
	os.MkdirAll(w.dir, 0o755)
	os.WriteFile(filepath.Join(w.dir, "10-eth0.network"), []byte("distro"), 0o644)

	s1 := State{
		Interfaces: map[string]Iface{"bond0": {Addresses: []string{"10.0.0.2/24"}}},
		Bonds:      map[string]Bond{"bond0": {Mode: "active-backup", Slaves: []string{"eth1", "eth2"}}},
		DNS:        DNS{Servers: []string{"9.9.9.9"}, Search: []string{"lan"}},
	}
	if err := w.Apply(s1); err != nil {
		t.Fatal(err)
	}
	if got := files(t, w); !slices.Equal(got, []string{"05-dplane-bond0.netdev", "05-dplane-bond0.network", "05-dplane-eth1.network", "05-dplane-eth2.network", "10-eth0.network"}) {
		t.Errorf("files: %v", got)
	}
	if !slices.Contains(fs.ran, "networkctl reload") || !slices.Contains(fs.ran, "networkctl reconfigure bond0 eth1 eth2") {
		t.Errorf("commands: %v", fs.ran)
	}
	if b, _ := os.ReadFile(w.resolvedPath); !strings.Contains(string(b), "DNS=9.9.9.9") || !strings.Contains(string(b), "Domains=lan") {
		t.Errorf("resolved: %s", b)
	}
	if !slices.Contains(fs.ran, "systemctl try-restart systemd-resolved.service") {
		t.Errorf("resolved not restarted: %v", fs.ran)
	}

	// Mode change: networkd would keep the old device, so it is deleted.
	fs.links["bond0"], fs.ran = true, nil
	s2 := s1.Clone()
	s2.Bonds["bond0"] = Bond{Mode: "802.3ad", Slaves: []string{"eth1", "eth2"}}
	if err := w.Apply(s2); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(fs.deleted, []string{"bond0"}) {
		t.Errorf("deleted: %v", fs.deleted)
	}
	if !slices.Contains(fs.ran, "systemctl restart systemd-networkd.service") || !slices.Contains(fs.ran, "networkctl reconfigure bond0 eth1 eth2") {
		t.Errorf("recreated bond: %v", fs.ran)
	}
	if slices.Contains(fs.ran, "systemctl try-restart systemd-resolved.service") {
		t.Error("resolved restarted although DNS did not change")
	}

	// Removing the bond: device deleted, member files removed, members reconfigured.
	fs.links["bond0"], fs.deleted, fs.ran = true, nil, nil
	if err := w.Apply(State{}); err != nil {
		t.Fatal(err)
	}
	if got := files(t, w); !slices.Equal(got, []string{"10-eth0.network"}) {
		t.Errorf("files after removal: %v", got)
	}
	if !slices.Equal(fs.deleted, []string{"bond0"}) || !slices.Contains(fs.ran, "networkctl reconfigure eth1 eth2") {
		t.Errorf("deleted %v ran %v", fs.deleted, fs.ran)
	}
	if _, err := os.Stat(w.resolvedPath); !os.IsNotExist(err) {
		t.Error("resolved drop-in kept")
	}

	// The state is saved and read back by a new writer.
	w.Apply(s1)
	w2, _ := testWriter(t)
	w2.statePath, w2.dir = w.statePath, w.dir
	if got := w2.State(); len(got.Bonds) != 1 || got.Interfaces["bond0"].Addresses[0] != "10.0.0.2/24" {
		t.Errorf("reloaded state: %+v", got)
	}
}

func TestPendingRevertAndConfirm(t *testing.T) {
	w, _ := testWriter(t)
	base := State{Interfaces: map[string]Iface{"eth0": {DHCP: true}}}
	if err := w.Apply(base); err != nil {
		t.Fatal(err)
	}
	next := State{Interfaces: map[string]Iface{"eth0": {Addresses: []string{"10.1.1.1/24"}}}}
	if err := w.ApplyPending(next, time.Now().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(read(t, w, "05-dplane-eth0.network"), "10.1.1.1/24") {
		t.Fatal("change not applied")
	}
	if err := w.ApplyPending(base, time.Now()); err != ErrPending {
		t.Errorf("second change: %v", err)
	}
	if ok, _ := w.HasPending(); !ok {
		t.Error("no pending change")
	}
	// A new writer (the daemon restarted) reverts it.
	w2, _ := testWriter(t)
	w2.statePath, w2.dir, w2.resolvedPath = w.statePath, w.dir, w.resolvedPath
	if err := w2.RevertPending(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(read(t, w2, "05-dplane-eth0.network"), "DHCP=yes") {
		t.Error("not reverted")
	}
	if ok, _ := w2.HasPending(); ok {
		t.Error("still pending")
	}

	if err := w2.ApplyPending(next, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := w2.Confirm(); err != nil {
		t.Fatal(err)
	}
	if err := w2.RevertPending(); err != ErrNothingPending {
		t.Errorf("revert after confirm: %v", err)
	}
	if !strings.Contains(read(t, w2, "05-dplane-eth0.network"), "10.1.1.1/24") {
		t.Error("confirmed change lost")
	}
}

// Files of earlier versions are imported once and then replaced.
func TestImportLegacy(t *testing.T) {
	w, _ := testWriter(t)
	os.MkdirAll(w.dir, 0o755)
	write := func(n, c string) { os.WriteFile(filepath.Join(w.dir, n), []byte(c), 0o644) }
	write("50-dplane-eth0.network", "# Managed\n[Match]\nName=eth0\n\n[Network]\nDHCP=no\nAddress=192.168.1.5/24\nGateway=192.168.1.1\n")
	write("50-dplane-eth0.20.netdev", "[NetDev]\nName=eth0.20\nKind=vlan\n\n[VLAN]\nId=20\n")
	write("50-dplane-eth0-vlan20.network", "[Match]\nName=eth0\n\n[Network]\nVLAN=eth0.20\n")
	write("50-dplane-bond-bond0.netdev", "[NetDev]\nName=bond0\nKind=bond\n\n[Bond]\nMode=802.3ad\n")
	write("50-dplane-bond-bond0.network", "[Match]\nName=bond0\n\n[Network]\nDHCP=yes\n")
	write("50-dplane-eth1-slave-bond0.network", "[Match]\nName=eth1\n\n[Network]\nBond=bond0\n")
	s := w.State()
	if s.Interfaces["eth0"].Addresses[0] != "192.168.1.5/24" || s.VLANs["eth0.20"] != (VLAN{Parent: "eth0", ID: 20}) ||
		s.Bonds["bond0"].Mode != "802.3ad" || !slices.Equal(s.Bonds["bond0"].Slaves, []string{"eth1"}) || !s.Interfaces["bond0"].DHCP {
		t.Fatalf("imported: %+v", s)
	}
	if err := w.Apply(s); err != nil {
		t.Fatal(err)
	}
	for _, f := range files(t, w) {
		if strings.HasPrefix(f, "50-dplane-") {
			t.Errorf("legacy file left: %s", f)
		}
	}
	if c := read(t, w, "05-dplane-eth0.network"); !strings.Contains(c, "Address=192.168.1.5/24") || !strings.Contains(c, "VLAN=eth0.20") {
		t.Errorf("eth0: %s", c)
	}
}

// Without a state file the current files are read back (nothing is lost).
func TestImportCurrentFiles(t *testing.T) {
	s := State{
		Interfaces: map[string]Iface{"eth0": {Addresses: []string{"10.0.0.2/24"}, Gateway: "10.0.0.1"}, "bond0": {DHCP: true}},
		Bonds:      map[string]Bond{"bond0": {Mode: "802.3ad", Slaves: []string{"eth1", "eth2"}}},
		VLANs:      map[string]VLAN{"eth0.7": {Parent: "eth0", ID: 7}},
	}
	dir := t.TempDir()
	for f, c := range s.render() {
		os.WriteFile(filepath.Join(dir, f), []byte(c), 0o644)
	}
	got := importLegacy(dir)
	if got.Interfaces["eth0"].Gateway != "10.0.0.1" || !got.Interfaces["bond0"].DHCP || got.VLANs["eth0.7"] != (VLAN{Parent: "eth0", ID: 7}) ||
		len(got.Bonds["bond0"].Slaves) != 2 || got.Bonds["bond0"].Mode != "802.3ad" {
		t.Errorf("imported: %+v", got)
	}
}
