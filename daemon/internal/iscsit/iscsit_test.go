package iscsit

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// fakeLIO stands in for targetcli and the kernel: the commands Apply runs
// change a directory tree shaped like the target's configfs, which Apply
// reads back.
type fakeLIO struct {
	t    *testing.T
	root string
	cmds []string
	fail func(cmd string) bool // commands that fail
}

func newFake(t *testing.T) *fakeLIO {
	if runtime.GOOS == "windows" {
		t.Skip("target names contain a colon, which a file name on Windows cannot have")
	}
	f := &fakeLIO{t: t, root: t.TempDir()}
	for _, d := range []string{"iscsi", "core/iblock_0"} {
		if err := os.MkdirAll(filepath.Join(f.root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// What the kernel keeps next to the storage objects of an adapter, and
	// next to the targets: not storage objects, not targets.
	f.write("HBA Index: 0", "core", "iblock_0", "hba_info")
	f.write("0", "core", "iblock_0", "hba_mode")
	f.write("Datera Inc. iSCSI Target", "iscsi", "lio_version")
	if err := os.MkdirAll(filepath.Join(f.root, "iscsi", "discovery_auth"), 0o755); err != nil {
		t.Fatal(err)
	}
	oldRoot, oldRun, oldDev := Configfs, Run, deviceOK
	Configfs, Run = f.root, f.run
	deviceOK = func(string) error { return nil }
	t.Cleanup(func() { Configfs, Run, deviceOK = oldRoot, oldRun, oldDev })
	return f
}

func (f *fakeLIO) write(value string, parts ...string) {
	p := filepath.Join(append([]string{f.root}, parts...)...)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(value+"\n"), 0o644); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fakeLIO) run(args ...string) (string, error) {
	cmd := strings.Join(args, " ")
	f.cmds = append(f.cmds, cmd)
	if f.fail != nil && f.fail(cmd) {
		return "it did not work\n", errors.New("exit status 1")
	}
	kv := func(key string) string {
		for _, a := range args {
			if strings.HasPrefix(a, key+"=") {
				return strings.TrimPrefix(a, key+"=")
			}
		}
		return ""
	}
	path, verb := args[0], args[1]
	switch {
	case path == "/backstores/block" && verb == "create":
		f.write(kv("dev"), "core", "iblock_0", kv("name"), "udev_path")
		f.write(kv("wwn"), "core", "iblock_0", kv("name"), "wwn", "vpd_unit_serial")
	case path == "/backstores/block" && verb == "delete":
		for _, iqn := range InKernel() {
			if readAttr(filepath.Join(tpgDir(iqn), "lun", "lun_0", "store")) == args[2] {
				return "storage object is in use\n", errors.New("exit status 1")
			}
		}
		os.RemoveAll(filepath.Join(f.root, "core", "iblock_0", args[2]))
	case path == "/iscsi" && verb == "create":
		iqn := args[2]
		for _, d := range []string{"np/0.0.0.0:3260", "lun", "acls"} {
			os.MkdirAll(filepath.Join(tpgDir(iqn), d), 0o755)
		}
		f.write("0", "iscsi", iqn, "tpgt_1", "attrib", "authentication")
		f.write("0", "iscsi", iqn, "tpgt_1", "enable")
	case path == "/iscsi" && verb == "delete":
		os.RemoveAll(filepath.Join(f.root, "iscsi", args[2]))
	case strings.HasSuffix(path, "/tpg1/luns") && verb == "create":
		f.write(strings.TrimPrefix(args[2], "/backstores/block/"), "iscsi", iqnOf(path), "tpgt_1", "lun", "lun_0", "store")
	case strings.HasSuffix(path, "/tpg1/portals") && verb == "create":
		os.MkdirAll(filepath.Join(tpgDir(iqnOf(path)), "np", args[2]+":"+args[3]), 0o755)
	case strings.HasSuffix(path, "/tpg1/portals") && verb == "delete":
		os.RemoveAll(filepath.Join(tpgDir(iqnOf(path)), "np", args[2]+":"+args[3]))
	case strings.HasSuffix(path, "/tpg1") && verb == "set":
		f.write(kv("authentication"), "iscsi", iqnOf(path), "tpgt_1", "attrib", "authentication")
	case strings.HasSuffix(path, "/tpg1") && verb == "enable":
		f.write("1", "iscsi", iqnOf(path), "tpgt_1", "enable")
	case strings.HasSuffix(path, "/tpg1/acls") && verb == "create":
		iqn := iqnOf(path)
		f.write("", "iscsi", iqn, "tpgt_1", "acls", args[2], "auth", "userid")
		f.write("", "iscsi", iqn, "tpgt_1", "acls", args[2], "auth", "password")
		f.write("No active iSCSI Session for Initiator Endpoint: "+args[2], "iscsi", iqn, "tpgt_1", "acls", args[2], "info")
	case strings.HasSuffix(path, "/tpg1/acls") && verb == "delete":
		os.RemoveAll(filepath.Join(tpgDir(iqnOf(path)), "acls", args[2]))
	case strings.Contains(path, "/tpg1/acls/") && verb == "set":
		init := path[strings.LastIndex(path, "/")+1:]
		for _, k := range []string{"userid", "password"} {
			if v := kv(k); v != "" {
				f.write(v, "iscsi", iqnOf(path), "tpgt_1", "acls", init, "auth", k)
			}
		}
	default:
		f.t.Fatalf("the fake does not know: targetcli %s", cmd)
	}
	return "", nil
}

// iqnOf takes the IQN out of "/iscsi/<iqn>/tpg1...".
func iqnOf(path string) string {
	return strings.Split(strings.TrimPrefix(path, "/iscsi/"), "/")[0]
}

func (f *fakeLIO) ran(prefix string) bool {
	for _, c := range f.cmds {
		if strings.HasPrefix(c, prefix) {
			return true
		}
	}
	return false
}

const (
	iqnA  = "iqn.2026-01.lan.nas:a"
	iqnB  = "iqn.2026-01.lan.nas:b"
	initX = "iqn.2026-01.lan.client:x"
	initY = "iqn.2026-01.lan.client:y"
	chap  = "Sup3r-Secret-123"
)

func TestApplyCreatesATarget(t *testing.T) {
	f := newFake(t)
	a := Target{IQN: iqnA, Zvol: "tank/a", RequireCHAP: true, ACLs: []ACL{{InitiatorIQN: initX, CHAPUser: "x", CHAPPass: chap}}}
	if err := Apply([]Target{a}); err != nil {
		t.Fatal(err)
	}
	tpg := tpgDir(iqnA)
	if got := storeDevice(StoreName(iqnA)); got != "/dev/zvol/tank/a" {
		t.Errorf("storage object serves %q", got)
	}
	// The LUN's identity is the target's, not a random one.
	if got := readAttr(filepath.Join(f.root, "core", "iblock_0", StoreName(iqnA), "wwn", "vpd_unit_serial")); got != Serial(iqnA) {
		t.Errorf("unit serial = %q, want %q", got, Serial(iqnA))
	}
	if p := Portals(iqnA); len(p) != 1 || p[0] != "0.0.0.0:3260" {
		t.Errorf("portals = %v", p)
	}
	if readAttr(filepath.Join(tpg, "attrib", "authentication")) != "1" || readAttr(filepath.Join(tpg, "enable")) != "1" {
		t.Error("the target is not enabled with CHAP required")
	}
	if readAttr(filepath.Join(tpg, "acls", initX, "auth", "userid")) != "x" || readAttr(filepath.Join(tpg, "acls", initX, "auth", "password")) != chap {
		t.Error("the initiator's CHAP credentials were not set")
	}
	// Applying the same list again runs nothing.
	f.cmds = nil
	if err := Apply([]Target{a}); err != nil {
		t.Fatal(err)
	}
	if len(f.cmds) != 0 {
		t.Fatalf("an unchanged list ran commands: %v", f.cmds)
	}
}

func TestSerialIsStable(t *testing.T) {
	if Serial(iqnA) != Serial(iqnA) || Serial(iqnA) == Serial(iqnB) || len(Serial(iqnA)) != 36 {
		t.Fatalf("serials: %q %q", Serial(iqnA), Serial(iqnB))
	}
}

func TestApplyAnotherPortal(t *testing.T) {
	f := newFake(t)
	if err := Apply([]Target{{IQN: iqnA, Zvol: "tank/a", PortalIP: "192.168.1.50"}}); err != nil {
		t.Fatal(err)
	}
	if p := Portals(iqnA); len(p) != 1 || p[0] != "192.168.1.50:3260" {
		t.Fatalf("portals = %v (%v)", p, f.cmds)
	}
}

func TestApplyLeavesOtherTargetsAlone(t *testing.T) {
	f := newFake(t)
	a := Target{IQN: iqnA, Zvol: "tank/a", ACLs: []ACL{{InitiatorIQN: initX}}}
	b := Target{IQN: iqnB, Zvol: "tank/b"}
	if err := Apply([]Target{a}); err != nil {
		t.Fatal(err)
	}
	f.cmds = nil
	if err := Apply([]Target{a, b}); err != nil {
		t.Fatal(err)
	}
	for _, c := range f.cmds {
		if strings.Contains(c, iqnA) || strings.Contains(c, StoreName(iqnA)) {
			t.Fatalf("adding a target touched another: %s", c)
		}
	}
	// Removing one removes its storage object too (it holds the volume open).
	f.cmds = nil
	if err := Apply([]Target{a}); err != nil {
		t.Fatal(err)
	}
	if Active(iqnB) || storeDevice(StoreName(iqnB)) != "" {
		t.Fatalf("a removed target left something behind: %v", f.cmds)
	}
	if !Active(iqnA) {
		t.Fatal("the other target is gone")
	}
	if err := Apply(nil); err != nil {
		t.Fatal(err)
	}
	if len(InKernel()) != 0 || len(storeNames()) != 0 {
		t.Fatal("an empty list left targets or storage objects behind")
	}
}

func TestApplyChangesTheAccessListInPlace(t *testing.T) {
	f := newFake(t)
	a := Target{IQN: iqnA, Zvol: "tank/a", ACLs: []ACL{{InitiatorIQN: initX, CHAPUser: "x", CHAPPass: chap}}}
	if err := Apply([]Target{a}); err != nil {
		t.Fatal(err)
	}
	// A second initiator: the first is not touched, the target not rebuilt.
	f.cmds = nil
	a.ACLs = append(a.ACLs, ACL{InitiatorIQN: initY})
	if err := Apply([]Target{a}); err != nil {
		t.Fatal(err)
	}
	if f.ran("/iscsi delete") || f.ran("/iscsi create") {
		t.Fatalf("an access list change rebuilt the target: %v", f.cmds)
	}
	for _, c := range f.cmds {
		if strings.Contains(c, initX) {
			t.Fatalf("the unchanged initiator was touched: %s", c)
		}
	}
	acls := filepath.Join(tpgDir(iqnA), "acls")
	if len(dirNames(acls)) != 2 {
		t.Fatalf("acls = %v", dirNames(acls))
	}
	// Another secret for the first; the second is removed.
	a.ACLs = []ACL{{InitiatorIQN: initX, CHAPUser: "x", CHAPPass: "An0ther-Secret-456"}}
	if err := Apply([]Target{a}); err != nil {
		t.Fatal(err)
	}
	if got := dirNames(acls); len(got) != 1 || got[0] != initX {
		t.Fatalf("acls = %v", got)
	}
	if readAttr(filepath.Join(acls, initX, "auth", "password")) != "An0ther-Secret-456" {
		t.Fatal("the new secret was not set")
	}
}

func TestApplyRebuildsWhenTheVolumeOrModeChanges(t *testing.T) {
	f := newFake(t)
	a := Target{IQN: iqnA, Zvol: "tank/a", ACLs: []ACL{{InitiatorIQN: initX, CHAPUser: "x", CHAPPass: chap}}}
	if err := Apply([]Target{a}); err != nil {
		t.Fatal(err)
	}
	a.Zvol = "tank/other"
	if err := Apply([]Target{a}); err != nil {
		t.Fatal(err)
	}
	if got := storeDevice(StoreName(iqnA)); got != "/dev/zvol/tank/other" {
		t.Fatalf("after changing the volume it serves %q (%v)", got, f.cmds)
	}
	a.RequireCHAP = true
	if err := Apply([]Target{a}); err != nil {
		t.Fatal(err)
	}
	if readAttr(filepath.Join(tpgDir(iqnA), "attrib", "authentication")) != "1" {
		t.Fatal("CHAP was not switched on")
	}
	// The access list is there again after each rebuild.
	if got := dirNames(filepath.Join(tpgDir(iqnA), "acls")); len(got) != 1 {
		t.Fatalf("acls after the rebuild = %v", got)
	}
}

func TestApplyDoesNotLeaveAHalfMadeTarget(t *testing.T) {
	f := newFake(t)
	f.fail = func(cmd string) bool { return strings.Contains(cmd, "authentication=") }
	err := Apply([]Target{{IQN: iqnA, Zvol: "tank/a", RequireCHAP: true}})
	if err == nil || !strings.Contains(err.Error(), "authentication mode") {
		t.Fatalf("a failed authentication mode was not reported: %v", err)
	}
	if Active(iqnA) || storeDevice(StoreName(iqnA)) != "" {
		t.Fatal("a target without its authentication mode was left behind")
	}
	if f.ran("/iscsi/" + iqnA + "/tpg1 enable") {
		t.Fatal("the target was enabled without its authentication mode")
	}
}

func TestApplyDoesNotAllowAnInitiatorWithoutItsCHAP(t *testing.T) {
	f := newFake(t)
	f.fail = func(cmd string) bool { return strings.Contains(cmd, "set auth password=") }
	err := Apply([]Target{{IQN: iqnA, Zvol: "tank/a", ACLs: []ACL{{InitiatorIQN: initX, CHAPUser: "x", CHAPPass: chap}}}})
	if err == nil {
		t.Fatal("a CHAP secret that could not be set was not reported")
	}
	if got := dirNames(filepath.Join(tpgDir(iqnA), "acls")); len(got) != 0 {
		t.Fatalf("the initiator is allowed without its secret: %v", got)
	}
}

func TestApplyWithAVolumeThatIsNotThere(t *testing.T) {
	f := newFake(t)
	a := Target{IQN: iqnA, Zvol: "tank/a"}
	b := Target{IQN: iqnB, Zvol: "gone/b"}
	deviceOK = func(dev string) error {
		if strings.HasPrefix(dev, "/dev/zvol/gone/") {
			return fmt.Errorf("volume device %s: no such file", dev)
		}
		return nil
	}
	err := Apply([]Target{a, b})
	if err == nil || !strings.Contains(err.Error(), iqnB) {
		t.Fatalf("the missing volume was not reported: %v", err)
	}
	if !Active(iqnA) || Active(iqnB) {
		t.Fatal("the target with a volume is not served, or the one without was made")
	}
	// A served target is not taken down while its volume cannot be seen.
	deviceOK = func(string) error { return errors.New("no such file") }
	f.cmds = nil
	a.PortalIP = "192.168.1.50" // even when it would have to be rebuilt
	if err := Apply([]Target{a}); err == nil {
		t.Fatal("not reported")
	}
	if !Active(iqnA) || f.ran("/iscsi delete") {
		t.Fatalf("a served target was removed while its volume was not visible: %v", f.cmds)
	}
}

func TestValidate(t *testing.T) {
	ok := Target{IQN: iqnA, Zvol: "/dev/zvol/tank/vols/db-01"}
	if err := Validate(&ok); err != nil {
		t.Fatal(err)
	}
	if ok.Zvol != "tank/vols/db-01" || ok.PortalIP != "0.0.0.0" || ok.PortalPort != 3260 || ok.Device() != "/dev/zvol/tank/vols/db-01" {
		t.Fatalf("defaults: %+v", ok)
	}
	for name, bad := range map[string]Target{
		"iqn":              {IQN: "not-an-iqn", Zvol: "tank/a"},
		"whole disk":       {IQN: iqnA, Zvol: "/dev/sda"},
		"pool itself":      {IQN: iqnA, Zvol: "tank"},
		"dots":             {IQN: iqnA, Zvol: "tank/../sda"},
		"space":            {IQN: iqnA, Zvol: "tank/x y"},
		"portal":           {IQN: iqnA, Zvol: "tank/a", PortalIP: "0.0.0.0;reboot"},
		"port":             {IQN: iqnA, Zvol: "tank/a", PortalPort: 70000},
		"initiator":        {IQN: iqnA, Zvol: "tank/a", ACLs: []ACL{{InitiatorIQN: "ALL"}}},
		"initiator twice":  {IQN: iqnA, Zvol: "tank/a", ACLs: []ACL{{InitiatorIQN: initX}, {InitiatorIQN: initX}}},
		"weak secret":      {IQN: iqnA, Zvol: "tank/a", ACLs: []ACL{{InitiatorIQN: initX, CHAPUser: "x", CHAPPass: "short"}}},
		"secret, no user":  {IQN: iqnA, Zvol: "tank/a", ACLs: []ACL{{InitiatorIQN: initX, CHAPPass: chap}}},
		"chap, no secrets": {IQN: iqnA, Zvol: "tank/a", RequireCHAP: true, ACLs: []ACL{{InitiatorIQN: initX}}},
	} {
		if err := Validate(&bad); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestStoreNames(t *testing.T) {
	long1 := "iqn.2026-01.lan.nas:" + strings.Repeat("a", 80) + "1"
	long2 := "iqn.2026-01.lan.other:" + strings.Repeat("a", 80) + "1"
	if len(StoreName(long1)) > 64 || StoreName(long1) == StoreName(long2) {
		t.Fatalf("long names: %q %q", StoreName(long1), StoreName(long2))
	}
	if StoreName(iqnA) != "iqn_2026-01_lan_nas_a" {
		t.Fatalf("StoreName = %q", StoreName(iqnA))
	}
}

func TestSaveAndLoad(t *testing.T) {
	path := filepath.Join(t.TempDir(), "targets.json")
	if list, err := Load(path); err != nil || list != nil {
		t.Fatalf("a missing file: %v %v", list, err)
	}
	in := []Target{{IQN: iqnA, Zvol: "tank/a", PortalIP: "0.0.0.0", PortalPort: 3260, ACLs: []ACL{{InitiatorIQN: initX, CHAPUser: "x", CHAPPass: chap}}}}
	if err := Save(path, in); err != nil {
		t.Fatal(err)
	}
	out, err := Load(path)
	if err != nil || len(out) != 1 || out[0].ACLs[0].CHAPPass != chap {
		t.Fatalf("load: %+v %v", out, err)
	}
	if st, err := os.Stat(path); err == nil && runtime.GOOS != "windows" && st.Mode().Perm() != 0o600 {
		t.Fatalf("the file with CHAP secrets is readable by others: %v", st.Mode())
	}
}
