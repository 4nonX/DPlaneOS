package nvmet

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

// fakeConfigfs makes a temporary directory behave like the kernel's nvmet
// configfs as far as Apply uses it: mkdir fills a directory with its
// attribute files, a port's name must be a number, attribute files cannot
// be created, an object that is linked somewhere cannot be removed, and a
// namespace cannot be changed once enabled. Links are plain files (the
// tests also run where symbolic links need privileges).
type fakeConfigfs struct {
	t    *testing.T
	root string
	log  []string // what was done, for "nothing was touched" checks
}

func newFake(t *testing.T) *fakeConfigfs {
	f := &fakeConfigfs{t: t, root: filepath.Join(t.TempDir(), "nvmet")}
	for _, d := range []string{"subsystems", "ports", "hosts"} {
		if err := os.MkdirAll(filepath.Join(f.root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	oldMk, oldRm, oldLn, oldDev := mkdir, rmdir, link, deviceOK
	mkdir, rmdir, link = f.mkdir, f.rmdir, f.link
	deviceOK = func(string) error { return nil }
	t.Cleanup(func() { mkdir, rmdir, link, deviceOK = oldMk, oldRm, oldLn, oldDev })
	return f
}

func (f *fakeConfigfs) rel(p string) string {
	r, _ := filepath.Rel(f.root, p)
	return filepath.ToSlash(r)
}

func (f *fakeConfigfs) mkdir(p string) error {
	parent := filepath.Base(filepath.Dir(p))
	files := map[string]string{}
	var dirs []string
	switch parent {
	case "subsystems":
		files = map[string]string{"attr_allow_any_host": "0\n", "attr_serial": "random-serial\n"}
		dirs = []string{"namespaces", "allowed_hosts"}
	case "namespaces":
		if _, err := strconv.Atoi(filepath.Base(p)); err != nil {
			return errors.New("invalid argument")
		}
		files = map[string]string{"device_path": "\n", "device_uuid": "random-uuid\n", "device_nguid": "random-nguid\n", "enable": "0\n"}
	case "ports":
		if _, err := strconv.Atoi(filepath.Base(p)); err != nil {
			return errors.New("invalid argument") // the kernel wants a port number
		}
		files = map[string]string{"addr_trtype": "\n", "addr_adrfam": "\n", "addr_traddr": "\n", "addr_trsvcid": "\n"}
		dirs = []string{"subsystems"}
	case "hosts":
	default:
		return fmt.Errorf("mkdir not permitted in %s", parent)
	}
	if err := os.Mkdir(p, 0o755); err != nil {
		return err
	}
	for name, v := range files {
		if err := os.WriteFile(filepath.Join(p, name), []byte(v), 0o644); err != nil {
			return err
		}
	}
	for _, d := range dirs {
		if err := os.Mkdir(filepath.Join(p, d), 0o755); err != nil {
			return err
		}
	}
	f.log = append(f.log, "mkdir "+f.rel(p))
	return nil
}

func (f *fakeConfigfs) link(target, l string) error {
	if st, err := os.Stat(target); err != nil || !st.IsDir() {
		return fmt.Errorf("link target %s does not exist", target)
	}
	holder := filepath.Dir(filepath.Dir(l)) // the port, or the subsystem
	if filepath.Base(filepath.Dir(l)) == "allowed_hosts" && readAttr(filepath.Join(holder, "attr_allow_any_host")) == "1" {
		return errors.New("can't add hosts when allow_any_host is set")
	}
	f.log = append(f.log, "link "+f.rel(l))
	return os.WriteFile(l, []byte(target), 0o644)
}

func (f *fakeConfigfs) rmdir(p string) error {
	name := filepath.Base(p)
	switch filepath.Base(filepath.Dir(p)) {
	case "subsystems":
		if len(dirNames(filepath.Join(p, "namespaces"))) > 0 || len(dirNames(filepath.Join(p, "allowed_hosts"))) > 0 {
			return errors.New("directory not empty")
		}
		for _, port := range dirNames(filepath.Join(f.root, "ports")) {
			if _, err := os.Lstat(filepath.Join(f.root, "ports", port, "subsystems", name)); err == nil {
				return errors.New("device or resource busy")
			}
		}
	case "ports":
		if len(dirNames(filepath.Join(p, "subsystems"))) > 0 {
			return errors.New("directory not empty")
		}
	case "hosts":
		for _, ss := range dirNames(filepath.Join(f.root, "subsystems")) {
			if _, err := os.Lstat(filepath.Join(f.root, "subsystems", ss, "allowed_hosts", name)); err == nil {
				return errors.New("device or resource busy")
			}
		}
	}
	f.log = append(f.log, "rmdir "+f.rel(p))
	return os.RemoveAll(p)
}

func (f *fakeConfigfs) attr(parts ...string) string {
	return readAttr(filepath.Join(append([]string{f.root}, parts...)...))
}

func (f *fakeConfigfs) exists(parts ...string) bool {
	_, err := os.Lstat(filepath.Join(append([]string{f.root}, parts...)...))
	return err == nil
}

func (f *fakeConfigfs) apply(exports ...Export) error {
	f.t.Helper()
	list := make([]Export, len(exports))
	copy(list, exports)
	for i := range list {
		e := &list[i]
		e.SubsystemNQN = realName(e.SubsystemNQN)
		hosts := make([]string, len(e.HostNQNs))
		for j, h := range e.HostNQNs {
			hosts[j] = realName(h)
		}
		e.HostNQNs = hosts
		if err := ValidateSpec(e); err != nil {
			f.t.Fatal(err)
		}
		e.SubsystemNQN = fsName(e.SubsystemNQN)
		for j := range e.HostNQNs {
			e.HostNQNs[j] = fsName(e.HostNQNs[j])
		}
	}
	f.log = nil
	return reconcile(f.root, list)
}

// Names as they are used in the tests. An NQN has a colon, which a file
// name on Windows cannot have: there (developer machines only; the target
// is Linux) the names are spelled without it after validation.
var (
	nqnA  = fsName("nqn.2026-01.lan.nas:a")
	nqnB  = fsName("nqn.2026-01.lan.nas:b")
	hostX = fsName("nqn.2014-08.org.nvmexpress:uuid:11111111-2222-3333-4444-555555555555")
	hostY = fsName("nqn.2014-08.org.nvmexpress:uuid:66666666-7777-8888-9999-000000000000")
)

func fsName(s string) string {
	if runtime.GOOS == "windows" {
		return strings.ReplaceAll(s, ":", "_")
	}
	return s
}

func realName(s string) string {
	if runtime.GOOS != "windows" {
		return s
	}
	// "nqn.2026-01.lan.nas_a" -> "nqn.2026-01.lan.nas:a" (first underscore).
	return strings.Replace(s, "_", ":", 1)
}

func TestApplyCreatesWhatTheKernelExpects(t *testing.T) {
	f := newFake(t)
	if err := f.apply(Export{SubsystemNQN: nqnA, Zvol: "tank/a", AllowAnyHost: true}); err != nil {
		t.Fatal(err)
	}
	// The subsystem's directory is its NQN; the port's is a number.
	if !f.exists("subsystems", nqnA) || !f.exists("ports", "1") {
		t.Fatalf("subsystem or port missing: %v", f.log)
	}
	id := IdentityOf(nqnA, 1)
	for path, want := range map[string]string{
		"subsystems/" + nqnA + "/attr_allow_any_host":       "1",
		"subsystems/" + nqnA + "/attr_serial":               id.Serial,
		"subsystems/" + nqnA + "/namespaces/1/device_path":  "/dev/zvol/tank/a",
		"subsystems/" + nqnA + "/namespaces/1/device_uuid":  id.UUID,
		"subsystems/" + nqnA + "/namespaces/1/device_nguid": id.NGUID,
		"subsystems/" + nqnA + "/namespaces/1/enable":       "1",
		"ports/1/addr_trtype":                               "tcp",
		"ports/1/addr_adrfam":                               "ipv4",
		"ports/1/addr_traddr":                               "0.0.0.0",
		"ports/1/addr_trsvcid":                              "4420",
	} {
		if got := f.attr(strings.Split(path, "/")...); got != want {
			t.Errorf("%s = %q, want %q", path, got, want)
		}
	}
	if !f.exists("ports", "1", "subsystems", nqnA) {
		t.Fatal("the subsystem is not linked into its port: nothing listens")
	}
}

func TestIdentityIsStableAndDistinct(t *testing.T) {
	a, again, b := IdentityOf(nqnA, 1), IdentityOf(nqnA, 1), IdentityOf(nqnB, 1)
	if a != again {
		t.Fatal("the identity of an export changed between two calls")
	}
	if a.UUID == b.UUID || a.NGUID == b.NGUID || a.Serial == b.Serial {
		t.Fatal("two exports share an identity")
	}
	if len(a.Serial) != 20 || len(a.NGUID) != 32 || len(a.UUID) != 36 {
		t.Fatalf("identity has the wrong shape: %+v", a)
	}
}

func TestApplyLeavesAnUnchangedExportAlone(t *testing.T) {
	f := newFake(t)
	a := Export{SubsystemNQN: nqnA, Zvol: "tank/a", AllowAnyHost: true}
	if err := f.apply(a); err != nil {
		t.Fatal(err)
	}
	// A second export is added: the first keeps its initiators, so nothing
	// of it may be removed or made again.
	if err := f.apply(a, Export{SubsystemNQN: nqnB, Zvol: "tank/b", AllowAnyHost: true}); err != nil {
		t.Fatal(err)
	}
	for _, l := range f.log {
		if strings.Contains(l, nqnA) {
			t.Fatalf("the unchanged export was touched: %s", l)
		}
	}
	// Both are served on the one port.
	if !f.exists("ports", "1", "subsystems", nqnA) || !f.exists("ports", "1", "subsystems", nqnB) || f.exists("ports", "2") {
		t.Fatalf("ports are not as expected: %v", f.log)
	}
	// Applying the same again does nothing at all.
	if err := f.apply(a, Export{SubsystemNQN: nqnB, Zvol: "tank/b", AllowAnyHost: true}); err != nil {
		t.Fatal(err)
	}
	if len(f.log) != 0 {
		t.Fatalf("applying an unchanged list changed something: %v", f.log)
	}
}

func TestApplyRemovesWhatIsNoLongerWanted(t *testing.T) {
	f := newFake(t)
	a := Export{SubsystemNQN: nqnA, Zvol: "tank/a", HostNQNs: []string{hostX}}
	b := Export{SubsystemNQN: nqnB, Zvol: "tank/b", AllowAnyHost: true, ListenPort: 4421}
	if err := f.apply(a, b); err != nil {
		t.Fatal(err)
	}
	if !f.exists("ports", "2") || !f.exists("hosts", hostX) {
		t.Fatalf("second port or host missing: %v", f.log)
	}
	if err := f.apply(b); err != nil {
		t.Fatal(err)
	}
	if f.exists("subsystems", nqnA) || f.exists("hosts", hostX) {
		t.Fatal("a removed export left its subsystem or host behind")
	}
	// Its port is gone too (nothing is served there); the other one stays.
	if f.exists("ports", "1") || !f.exists("ports", "2", "subsystems", nqnB) {
		t.Fatalf("ports are not as expected: %v", f.log)
	}
	if err := f.apply(); err != nil {
		t.Fatal(err)
	}
	if n := len(dirNames(filepath.Join(f.root, "subsystems"))) + len(dirNames(filepath.Join(f.root, "ports"))) + len(dirNames(filepath.Join(f.root, "hosts"))); n != 0 {
		t.Fatalf("an empty list left %d object(s) behind", n)
	}
}

func TestApplyChangesHostsWithoutRebuilding(t *testing.T) {
	f := newFake(t)
	if err := f.apply(Export{SubsystemNQN: nqnA, Zvol: "tank/a", HostNQNs: []string{hostX}}); err != nil {
		t.Fatal(err)
	}
	if f.attr("subsystems", nqnA, "attr_allow_any_host") != "0" || !f.exists("subsystems", nqnA, "allowed_hosts", hostX) {
		t.Fatal("the host list was not applied")
	}
	// Another host instead: the namespace (and the initiators) stay.
	if err := f.apply(Export{SubsystemNQN: nqnA, Zvol: "tank/a", HostNQNs: []string{hostY}}); err != nil {
		t.Fatal(err)
	}
	if f.exists("subsystems", nqnA, "allowed_hosts", hostX) || !f.exists("subsystems", nqnA, "allowed_hosts", hostY) || f.exists("hosts", hostX) {
		t.Fatalf("host list not replaced: %v", f.log)
	}
	for _, l := range f.log {
		if strings.Contains(l, "namespaces") || l == "rmdir subsystems/"+nqnA {
			t.Fatalf("changing the hosts rebuilt the export: %s", l)
		}
	}
	// To "any host" (the kernel wants the list empty first) and back.
	if err := f.apply(Export{SubsystemNQN: nqnA, Zvol: "tank/a", AllowAnyHost: true}); err != nil {
		t.Fatal(err)
	}
	if f.attr("subsystems", nqnA, "attr_allow_any_host") != "1" || len(dirNames(filepath.Join(f.root, "subsystems", nqnA, "allowed_hosts"))) != 0 {
		t.Fatal("any host: not applied")
	}
	if err := f.apply(Export{SubsystemNQN: nqnA, Zvol: "tank/a", HostNQNs: []string{hostX}}); err != nil {
		t.Fatal(err)
	}
	if f.attr("subsystems", nqnA, "attr_allow_any_host") != "0" || !f.exists("subsystems", nqnA, "allowed_hosts", hostX) {
		t.Fatal("back to a host list: not applied")
	}
}

func TestApplyRebuildsWhenTheVolumeChanges(t *testing.T) {
	f := newFake(t)
	if err := f.apply(Export{SubsystemNQN: nqnA, Zvol: "tank/a", AllowAnyHost: true}); err != nil {
		t.Fatal(err)
	}
	if err := f.apply(Export{SubsystemNQN: nqnA, Zvol: "tank/other", AllowAnyHost: true}); err != nil {
		t.Fatal(err)
	}
	if got := f.attr("subsystems", nqnA, "namespaces", "1", "device_path"); got != "/dev/zvol/tank/other" {
		t.Fatalf("device_path = %q", got)
	}
	if !f.exists("ports", "1", "subsystems", nqnA) {
		t.Fatal("the rebuilt export is not served")
	}
}

func TestApplyMovesAnExportToAnotherAddress(t *testing.T) {
	f := newFake(t)
	if err := f.apply(Export{SubsystemNQN: nqnA, Zvol: "tank/a", AllowAnyHost: true}); err != nil {
		t.Fatal(err)
	}
	if err := f.apply(Export{SubsystemNQN: nqnA, Zvol: "tank/a", AllowAnyHost: true, ListenAddr: "192.168.1.50"}); err != nil {
		t.Fatal(err)
	}
	ports := dirNames(filepath.Join(f.root, "ports"))
	if len(ports) != 1 || f.attr("ports", ports[0], "addr_traddr") != "192.168.1.50" || !f.exists("ports", ports[0], "subsystems", nqnA) {
		t.Fatalf("ports after the move: %v (%v)", ports, f.log)
	}
	// IPv6 addresses name their family.
	if err := f.apply(Export{SubsystemNQN: nqnA, Zvol: "tank/a", AllowAnyHost: true, ListenAddr: "fd00::50"}); err != nil {
		t.Fatal(err)
	}
	ports = dirNames(filepath.Join(f.root, "ports"))
	if len(ports) != 1 || f.attr("ports", ports[0], "addr_adrfam") != "ipv6" {
		t.Fatalf("IPv6 port: %v", ports)
	}
}

func TestApplyWithAVolumeThatIsNotThere(t *testing.T) {
	f := newFake(t)
	a := Export{SubsystemNQN: nqnA, Zvol: "tank/a", AllowAnyHost: true}
	b := Export{SubsystemNQN: nqnB, Zvol: "gone/b", AllowAnyHost: true}
	deviceOK = func(dev string) error {
		if strings.HasPrefix(dev, "/dev/zvol/gone/") {
			return errors.New("no such device")
		}
		return nil
	}
	// The export whose volume is missing is reported; the other is served.
	err := f.apply(a, b)
	if err == nil || !strings.Contains(err.Error(), nqnB) {
		t.Fatalf("missing volume not reported: %v", err)
	}
	if !f.exists("ports", "1", "subsystems", nqnA) || f.exists("subsystems", nqnB) {
		t.Fatal("the export with a volume is not served, or the one without was made")
	}
	// An export that is being served is not taken down because its volume
	// cannot be seen for a moment.
	deviceOK = func(string) error { return errors.New("no such device") }
	if err := f.apply(a); err != nil {
		t.Fatal(err)
	}
	if len(f.log) != 0 || !f.exists("ports", "1", "subsystems", nqnA) {
		t.Fatalf("a served export was touched while its volume was not visible: %v", f.log)
	}
}

func TestValidateSpecRejectsWhatTheKernelWould(t *testing.T) {
	for name, e := range map[string]Export{
		"address is a name": {SubsystemNQN: "nqn.2026-01.lan.nas:a", Zvol: "tank/a", AllowAnyHost: true, ListenAddr: "nas.lan"},
		"rdma":              {SubsystemNQN: "nqn.2026-01.lan.nas:a", Zvol: "tank/a", AllowAnyHost: true, Transport: "rdma"},
		"path as volume":    {SubsystemNQN: "nqn.2026-01.lan.nas:a", Zvol: "/dev/sda", AllowAnyHost: true},
		"bad host":          {SubsystemNQN: "nqn.2026-01.lan.nas:a", Zvol: "tank/a", HostNQNs: []string{"host1"}},
		"long nqn":          {SubsystemNQN: "nqn.2026-01.lan.nas:" + strings.Repeat("x", 220), Zvol: "tank/a", AllowAnyHost: true},
	} {
		if err := ValidateSpec(&e); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}
