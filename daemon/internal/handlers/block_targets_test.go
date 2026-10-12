package handlers

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"dplaned/internal/iscsit"
	"dplaned/internal/nvmet"
)

const (
	testIQN  = "iqn.2026-10.lan.nas:lun0"
	testInit = "iqn.2026-10.lan.client:host1"
	testNQN  = "nqn.2026-10.lan.nas:nv0"
	testCHAP = "Sup3r-Secret-123"
)

// blockTargetsEnv gives the handlers lists of their own, a kernel that takes
// (or refuses) what is applied, and volumes that exist.
type blockTargetsEnv struct {
	iscsiApplied int
	nvmeApplied  int
	fail         error // what applying answers
}

func newBlockTargetsEnv(t *testing.T) *blockTargetsEnv {
	t.Helper()
	e := &blockTargetsEnv{}
	dir := t.TempDir()
	oldI, oldN, oldIA, oldNA, oldVol := iscsit.TargetsFile, nvmet.TargetsFile, iscsit.ApplySaved, nvmet.ApplySaved, volumeHere
	iscsit.TargetsFile = filepath.Join(dir, "iscsi.json")
	nvmet.TargetsFile = filepath.Join(dir, "nvme.json")
	iscsit.ApplySaved = func() error { e.iscsiApplied++; return e.fail }
	nvmet.ApplySaved = func() error { e.nvmeApplied++; return e.fail }
	volumeHere = func(zvol string) bool { return strings.HasPrefix(zvol, "tank/") }
	t.Cleanup(func() {
		iscsit.TargetsFile, nvmet.TargetsFile, iscsit.ApplySaved, nvmet.ApplySaved, volumeHere = oldI, oldN, oldIA, oldNA, oldVol
	})
	return e
}

func savedISCSI(t *testing.T) []iscsit.Target {
	t.Helper()
	list, err := iscsit.Load(iscsit.TargetsFile)
	if err != nil {
		t.Fatal(err)
	}
	return list
}

func savedNVMe(t *testing.T) []nvmet.Export {
	t.Helper()
	list, err := nvmet.LoadExports(nvmet.TargetsFile)
	if err != nil {
		t.Fatal(err)
	}
	return list
}

func TestCreateISCSITargetValidation(t *testing.T) {
	e := newBlockTargetsEnv(t)
	for name, body := range map[string]map[string]any{
		"bad iqn":        {"iqn": "not-an-iqn", "zvol": "tank/lun0"},
		"no volume":      {"iqn": testIQN},
		"whole disk":     {"iqn": testIQN, "backing_dev": "/dev/sda"},
		"traversal":      {"iqn": testIQN, "zvol": "../../sda"},
		"bad portal":     {"iqn": testIQN, "zvol": "tank/lun0", "portal_ip": "0.0.0.0;reboot"},
		"bad port":       {"iqn": testIQN, "zvol": "tank/lun0", "portal_port": 70000},
		"volume missing": {"iqn": testIQN, "zvol": "elsewhere/lun0"},
	} {
		if r := call(t, CreateISCSITarget, req{method: "POST", body: body}); r.code != 400 {
			t.Errorf("%s: %s", name, r)
		}
	}
	if e.iscsiApplied != 0 || len(savedISCSI(t)) != 0 {
		t.Error("an invalid request changed something")
	}
}

func TestISCSITargetLifecycle(t *testing.T) {
	e := newBlockTargetsEnv(t)
	// The web UI sends the volume's dataset name; a device path works too.
	if r := call(t, CreateISCSITarget, req{method: "POST", body: map[string]any{"iqn": testIQN, "backing_dev": "/dev/zvol/tank/lun0", "require_chap": true}}); !r.ok() {
		t.Fatalf("create: %s", r)
	}
	list := savedISCSI(t)
	if len(list) != 1 || list[0].Zvol != "tank/lun0" || !list[0].RequireCHAP || list[0].PortalIP != "0.0.0.0" || list[0].PortalPort != 3260 {
		t.Fatalf("saved: %+v", list)
	}
	if e.iscsiApplied != 1 {
		t.Fatalf("applied %d times", e.iscsiApplied)
	}
	// The same name, or the same volume, a second time.
	if r := call(t, CreateISCSITarget, req{method: "POST", body: map[string]any{"iqn": testIQN, "zvol": "tank/other"}}); r.code != 409 {
		t.Errorf("same IQN twice: %s", r)
	}
	if r := call(t, CreateISCSITarget, req{method: "POST", body: map[string]any{"iqn": "iqn.2026-10.lan.nas:lun1", "zvol": "tank/lun0"}}); r.code != 409 {
		t.Errorf("same volume twice: %s", r)
	}

	// The target requires CHAP: an initiator without a secret is refused.
	if r := call(t, AddISCSIACL, req{method: "POST", body: map[string]any{"target_iqn": testIQN, "initiator_iqn": testInit}}); r.code != 400 || !strings.Contains(r.raw, "CHAP") {
		t.Errorf("initiator without a secret on a CHAP target: %s", r)
	}
	if r := call(t, AddISCSIACL, req{method: "POST", body: map[string]any{"target_iqn": testIQN, "initiator_iqn": testInit, "chap_user": "u", "chap_pass": "short"}}); r.code != 400 {
		t.Errorf("weak CHAP secret: %s", r)
	}
	if r := call(t, AddISCSIACL, req{method: "POST", body: map[string]any{"target_iqn": testIQN, "initiator_iqn": "ALL"}}); r.code != 400 {
		t.Errorf("an initiator that is not an IQN: %s", r)
	}
	if r := call(t, AddISCSIACL, req{method: "POST", body: map[string]any{"target_iqn": "iqn.2026-10.lan.nas:none", "initiator_iqn": testInit, "chap_user": "host1", "chap_pass": testCHAP}}); r.code != 404 {
		t.Errorf("unknown target: %s", r)
	}
	if r := call(t, AddISCSIACL, req{method: "POST", body: map[string]any{"target_iqn": testIQN, "initiator_iqn": testInit, "chap_user": "host1", "chap_pass": testCHAP}}); !r.ok() {
		t.Fatalf("add initiator: %s", r)
	}
	// Again with another secret: replaced, not listed twice.
	if r := call(t, AddISCSIACL, req{method: "POST", body: map[string]any{"target_iqn": testIQN, "initiator_iqn": testInit, "chap_user": "host1", "chap_pass": "An0ther-Secret-456"}}); !r.ok() {
		t.Fatalf("change the secret: %s", r)
	}
	list = savedISCSI(t)
	if len(list[0].ACLs) != 1 || list[0].ACLs[0].CHAPPass != "An0ther-Secret-456" {
		t.Fatalf("access list: %+v", list[0].ACLs)
	}

	// Lists: what the page reads, and never a secret.
	r := call(t, GetISCSITargets, req{})
	if !r.ok() || !strings.Contains(r.raw, `"zvol":"tank/lun0"`) || !strings.Contains(r.raw, `"require_chap":true`) || !strings.Contains(r.raw, `"active":false`) {
		t.Errorf("targets: %s", r)
	}
	acls := call(t, GetISCSIACLs, req{path: "/api/iscsi/acls"})
	if !acls.ok() || !strings.Contains(acls.raw, `"initiator_iqn":"`+testInit+`"`) || !strings.Contains(acls.raw, `"target_iqn":"`+testIQN+`"`) {
		t.Errorf("acls: %s", acls)
	}
	for _, out := range []string{r.raw, acls.raw} {
		if strings.Contains(out, "Secret") || strings.Contains(out, "chap_pass") {
			t.Errorf("a CHAP secret left through the API: %s", out)
		}
	}
	if st := call(t, GetISCSIStatus, req{}); st.body["target_count"] != float64(1) || st.body["served"] != float64(0) {
		t.Errorf("status: %s", st)
	}

	// Another volume: the access list stays.
	if r := call(t, UpdateISCSITarget, req{method: "POST", body: map[string]any{"iqn": testIQN, "zvol": "tank/lun9", "require_chap": true}}); !r.ok() {
		t.Fatalf("update: %s", r)
	}
	list = savedISCSI(t)
	if list[0].Zvol != "tank/lun9" || len(list[0].ACLs) != 1 {
		t.Fatalf("after the update: %+v", list)
	}
	if r := call(t, UpdateISCSITarget, req{method: "POST", body: map[string]any{"iqn": "iqn.2026-10.lan.nas:none", "zvol": "tank/x"}}); r.code != 404 {
		t.Errorf("update of an unknown target: %s", r)
	}

	if r := call(t, DeleteISCSIACL, req{method: "DELETE", body: map[string]any{"target_iqn": testIQN, "initiator_iqn": "iqn.2026-10.lan.client:other"}}); r.code != 404 {
		t.Errorf("removing an initiator that is not listed: %s", r)
	}
	if r := call(t, DeleteISCSIACL, req{method: "DELETE", body: map[string]any{"target_iqn": testIQN, "initiator_iqn": testInit}}); !r.ok() {
		t.Fatalf("remove initiator: %s", r)
	}
	if r := call(t, DeleteISCSITarget, req{method: "DELETE", path: "/api/iscsi/targets/bad;rm"}); r.code != 400 {
		t.Errorf("bad iqn: %s", r)
	}
	if r := call(t, DeleteISCSITarget, req{method: "DELETE", path: "/api/iscsi/targets/" + testIQN}); !r.ok() {
		t.Fatalf("delete: %s", r)
	}
	if r := call(t, DeleteISCSITarget, req{method: "DELETE", path: "/api/iscsi/targets/" + testIQN}); r.code != 404 {
		t.Errorf("delete twice: %s", r)
	}
	if len(savedISCSI(t)) != 0 {
		t.Error("the deleted target is still saved")
	}
}

// Switching CHAP on for a target whose initiators have no secrets would lock
// them all out without saying so.
func TestISCSIRequireCHAPNeedsSecrets(t *testing.T) {
	newBlockTargetsEnv(t)
	if r := call(t, CreateISCSITarget, req{method: "POST", body: map[string]any{"iqn": testIQN, "zvol": "tank/lun0"}}); !r.ok() {
		t.Fatalf("create: %s", r)
	}
	if r := call(t, AddISCSIACL, req{method: "POST", body: map[string]any{"target_iqn": testIQN, "initiator_iqn": testInit}}); !r.ok() {
		t.Fatalf("add: %s", r)
	}
	r := call(t, UpdateISCSITarget, req{method: "POST", body: map[string]any{"iqn": testIQN, "zvol": "tank/lun0", "require_chap": true}})
	if r.code != 400 || !strings.Contains(r.raw, "CHAP") {
		t.Fatalf("CHAP switched on over an initiator without a secret: %s", r)
	}
	if savedISCSI(t)[0].RequireCHAP {
		t.Error("the refused change was saved")
	}
}

// A target the kernel did not take is not left in the list as if it existed.
func TestISCSIChangeThatFailsIsTakenBack(t *testing.T) {
	e := newBlockTargetsEnv(t)
	e.fail = errors.New(testIQN + ": creating the target: it did not work")
	r := call(t, CreateISCSITarget, req{method: "POST", body: map[string]any{"iqn": testIQN, "zvol": "tank/lun0"}})
	if r.code != 500 || !strings.Contains(r.raw, "nothing was changed") {
		t.Fatalf("a failed create: %s", r)
	}
	if len(savedISCSI(t)) != 0 {
		t.Fatal("the target that could not be created is in the list")
	}
	if e.iscsiApplied != 2 {
		t.Errorf("the previous list was not applied again (applied %d times)", e.iscsiApplied)
	}
	// A problem with another target does not fail this one.
	e.fail = errors.New("iqn.2026-10.lan.nas:broken: volume device /dev/zvol/gone/x: no such file")
	if r := call(t, CreateISCSITarget, req{method: "POST", body: map[string]any{"iqn": testIQN, "zvol": "tank/lun0"}}); !r.ok() {
		t.Fatalf("create next to a broken target: %s", r)
	}
	if len(savedISCSI(t)) != 1 {
		t.Fatal("not saved")
	}
}

func TestNVMeExportLifecycle(t *testing.T) {
	e := newBlockTargetsEnv(t)
	for name, body := range map[string]map[string]any{
		"bad nqn":        {"subsystem_nqn": "nv0", "zvol": "tank/nv0", "allow_any_host": true},
		"no hosts":       {"subsystem_nqn": testNQN, "zvol": "tank/nv0"},
		"bad host":       {"subsystem_nqn": testNQN, "zvol": "tank/nv0", "host_nqns": []string{"host1"}},
		"address a name": {"subsystem_nqn": testNQN, "zvol": "tank/nv0", "allow_any_host": true, "listen_addr": "nas.lan"},
		"volume missing": {"subsystem_nqn": testNQN, "zvol": "elsewhere/nv0", "allow_any_host": true},
	} {
		if r := call(t, CreateNVMeTarget, req{method: "POST", body: body}); r.code != 400 {
			t.Errorf("%s: %s", name, r)
		}
	}
	if e.nvmeApplied != 0 {
		t.Error("an invalid request was applied")
	}
	host := "nqn.2014-08.org.nvmexpress:uuid:11111111-2222-3333-4444-555555555555"
	if r := call(t, CreateNVMeTarget, req{method: "POST", body: map[string]any{"subsystem_nqn": testNQN, "zvol": "tank/nv0", "host_nqns": []string{host}}}); !r.ok() {
		t.Fatalf("create: %s", r)
	}
	list := savedNVMe(t)
	if len(list) != 1 || list[0].ListenPort != 4420 || list[0].ListenAddr != "0.0.0.0" || list[0].Transport != "tcp" || list[0].NamespaceID != 1 {
		t.Fatalf("saved: %+v", list)
	}
	if r := call(t, CreateNVMeTarget, req{method: "POST", body: map[string]any{"subsystem_nqn": testNQN, "zvol": "tank/nv1", "allow_any_host": true}}); r.code != 409 {
		t.Errorf("same NQN twice: %s", r)
	}
	if r := call(t, CreateNVMeTarget, req{method: "POST", body: map[string]any{"subsystem_nqn": "nqn.2026-10.lan.nas:nv1", "zvol": "tank/nv0", "allow_any_host": true}}); r.code != 409 {
		t.Errorf("same volume twice: %s", r)
	}
	r := call(t, ListNVMeTargets, req{})
	if !r.ok() || !strings.Contains(r.raw, `"subsystem_nqn":"`+testNQN+`"`) || !strings.Contains(r.raw, `"active":false`) || !strings.Contains(r.raw, host) {
		t.Errorf("list: %s", r)
	}
	if r := call(t, UpdateNVMeTarget, req{method: "PUT", body: map[string]any{"subsystem_nqn": testNQN, "zvol": "tank/nv0", "allow_any_host": true}}); !r.ok() {
		t.Fatalf("update: %s", r)
	}
	if list := savedNVMe(t); !list[0].AllowAnyHost {
		t.Fatalf("after the update: %+v", list)
	}
	if r := call(t, UpdateNVMeTarget, req{method: "PUT", body: map[string]any{"subsystem_nqn": "nqn.2026-10.lan.nas:none", "zvol": "tank/x", "allow_any_host": true}}); r.code != 404 {
		t.Errorf("update of an unknown export: %s", r)
	}

	// A change the kernel does not take is taken back.
	e.fail = errors.New(testNQN + ": serving on 0.0.0.0 port 4420: address in use")
	r = call(t, DeleteNVMeTarget, req{method: "DELETE", path: "/api/nvmet/targets?subsystem_nqn=" + testNQN})
	if r.code != 500 || len(savedNVMe(t)) != 1 {
		t.Fatalf("a failed delete: %s (saved: %d)", r, len(savedNVMe(t)))
	}
	e.fail = nil
	if r := call(t, DeleteNVMeTarget, req{method: "DELETE", path: "/api/nvmet/targets?subsystem_nqn=" + testNQN}); !r.ok() {
		t.Fatalf("delete: %s", r)
	}
	if r := call(t, DeleteNVMeTarget, req{method: "DELETE", path: "/api/nvmet/targets?subsystem_nqn=" + testNQN}); r.code != 404 {
		t.Errorf("delete twice: %s", r)
	}
	if r := call(t, DeleteNVMeTarget, req{method: "DELETE", path: "/api/nvmet/targets"}); r.code != 400 {
		t.Errorf("delete without a name: %s", r)
	}
}
