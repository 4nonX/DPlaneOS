package handlers

import (
	"strings"
	"testing"
)

const testIQN = "iqn.2026-10.lan.nas:lun0"

func TestISCSIValidators(t *testing.T) {
	if validateBackingDev("/dev/zvol/tank/lun0") != nil || validateBackingDev("/dev/zvol/tank/vols/db-01") != nil {
		t.Error("zvol rejected")
	}
	for _, bad := range []string{"/dev/sda", "/dev/zvol/../sda", "/etc/shadow", "/dev/zvol/tank/x y", "/dev/zvol/"} {
		if validateBackingDev(bad) == nil {
			t.Errorf("backing device %q accepted", bad)
		}
	}
	if validateCHAP("", "") != nil || validateCHAP("initiator1", "Sup3r-Secret-123") != nil {
		t.Error("valid CHAP rejected")
	}
	for _, c := range [][2]string{{"u", "short"}, {"bad user", "Sup3r-Secret-123"}, {"u", "has a space in it"}, {"", "Sup3r-Secret-123"}} {
		if validateCHAP(c[0], c[1]) == nil {
			t.Errorf("CHAP %q/%q accepted", c[0], c[1])
		}
	}
}

func TestCreateISCSITargetValidation(t *testing.T) {
	cmds := fakeCommands(t, nil)
	for name, body := range map[string]map[string]any{
		"bad iqn":    {"iqn": "not-an-iqn", "backing_dev": "/dev/zvol/tank/lun0"},
		"no device":  {"iqn": testIQN},
		"whole disk": {"iqn": testIQN, "backing_dev": "/dev/sda"},
		"bad portal": {"iqn": testIQN, "backing_dev": "/dev/zvol/tank/lun0", "portal_ip": "0.0.0.0;reboot"},
		"bad port":   {"iqn": testIQN, "backing_dev": "/dev/zvol/tank/lun0", "portal_port": 70000},
	} {
		if r := call(t, CreateISCSITarget, req{method: "POST", body: body}); r.code != 400 {
			t.Errorf("%s: %s", name, r)
		}
	}
	if len(cmds.keys()) != 0 {
		t.Errorf("commands ran for invalid requests: %v", cmds.keys())
	}
}

func TestCreateISCSITarget(t *testing.T) {
	cmds := fakeCommands(t, nil)
	r := call(t, CreateISCSITarget, req{method: "POST", body: map[string]any{"iqn": testIQN, "backing_dev": "/dev/zvol/tank/lun0", "require_chap": true}})
	if !r.ok() {
		t.Fatalf("create: %s", r)
	}
	tpg := "/iscsi/" + testIQN + "/tpg1"
	for _, want := range [][]string{
		{"/iscsi", "create", testIQN},
		{"/backstores/block", "create"},
		{tpg + "/portals", "create", "0.0.0.0:3260"},
		{tpg, "set", "attribute", "authentication=1"},
		{tpg, "enable"},
		{"/", "saveconfig"},
	} {
		if !cmds.ran("targetcli", want...) {
			t.Errorf("targetcli %v not run: %v", want, cmds.keys())
		}
	}
}

func TestCreateISCSITargetCleansUpOnAuthFailure(t *testing.T) {
	cmds := fakeCommands(t, map[string]func([]string) ([]byte, error){
		"targetcli": func(a []string) ([]byte, error) {
			if len(a) > 3 && a[1] == "set" && strings.HasPrefix(a[3], "authentication=") {
				return fail("cannot set attribute")(a)
			}
			return nil, nil
		},
	})
	r := call(t, CreateISCSITarget, req{method: "POST", body: map[string]any{"iqn": testIQN, "backing_dev": "/dev/zvol/tank/lun0", "require_chap": true}})
	if r.code != 500 {
		t.Fatalf("a target without its authentication mode must fail: %s", r)
	}
	if !cmds.ran("targetcli", "/iscsi", "delete", testIQN) {
		t.Errorf("half-built target not removed: %v", cmds.keys())
	}
	if cmds.ran("targetcli", "/iscsi/"+testIQN+"/tpg1", "enable") {
		t.Error("target enabled without authentication")
	}
}

func TestCreateISCSITargetSaveFailureIsReported(t *testing.T) {
	fakeCommands(t, map[string]func([]string) ([]byte, error){
		"targetcli": func(a []string) ([]byte, error) {
			if len(a) > 1 && a[1] == "saveconfig" {
				return fail("disk full")(a)
			}
			return nil, nil
		},
	})
	r := call(t, CreateISCSITarget, req{method: "POST", body: map[string]any{"iqn": testIQN, "backing_dev": "/dev/zvol/tank/lun0"}})
	if r.ok() || !strings.Contains(r.raw, "next reboot") {
		t.Errorf("a failed saveconfig must be reported: %s", r)
	}
}

func TestAddISCSIACL(t *testing.T) {
	init := "iqn.2026-10.lan.client:host1"
	if r := call(t, AddISCSIACL, req{method: "POST", body: map[string]any{"target_iqn": testIQN, "initiator_iqn": init, "chap_user": "u", "chap_pass": "short"}}); r.code != 400 {
		t.Errorf("weak CHAP secret: %s", r)
	}
	cmds := fakeCommands(t, nil)
	r := call(t, AddISCSIACL, req{method: "POST", body: map[string]any{"target_iqn": testIQN, "initiator_iqn": init, "chap_user": "host1", "chap_pass": "Sup3r-Secret-123"}})
	if !r.ok() {
		t.Fatalf("add: %s", r)
	}
	acl := "/iscsi/" + testIQN + "/tpg1/acls"
	if !cmds.ran("targetcli", acl, "create", init) || !cmds.ran("targetcli", acl+"/"+init, "set", "auth", "userid=host1") {
		t.Errorf("ACL/CHAP commands: %v", cmds.keys())
	}

	// CHAP that cannot be set leaves no unauthenticated ACL behind.
	cmds = fakeCommands(t, map[string]func([]string) ([]byte, error){
		"targetcli": func(a []string) ([]byte, error) {
			if len(a) > 3 && a[2] == "auth" {
				return fail("no")(a)
			}
			return nil, nil
		},
	})
	r = call(t, AddISCSIACL, req{method: "POST", body: map[string]any{"target_iqn": testIQN, "initiator_iqn": init, "chap_user": "host1", "chap_pass": "Sup3r-Secret-123"}})
	if r.code != 500 || !cmds.ran("targetcli", acl, "delete", init) {
		t.Errorf("failed CHAP: %s %v", r, cmds.keys())
	}
}

func TestDeleteISCSITarget(t *testing.T) {
	cmds := fakeCommands(t, nil)
	if r := call(t, DeleteISCSITarget, req{method: "DELETE", path: "/api/iscsi/targets/bad;rm"}); r.code != 400 {
		t.Errorf("bad iqn: %s", r)
	}
	if r := call(t, DeleteISCSITarget, req{method: "DELETE", path: "/api/iscsi/targets/" + testIQN}); !r.ok() {
		t.Fatalf("delete: %s", r)
	}
	if !cmds.ran("targetcli", "/iscsi", "delete", testIQN) {
		t.Errorf("not deleted: %v", cmds.keys())
	}
}

// The web UI sends the zvol's dataset name, not a device path.
func TestCreateISCSITargetFromZvolName(t *testing.T) {
	cmds := fakeCommands(t, nil)
	r := call(t, CreateISCSITarget, req{method: "POST", body: map[string]any{"iqn": testIQN, "zvol": "tank/zvols/target0"}})
	if !r.ok() {
		t.Fatalf("create from zvol name: %s", r)
	}
	if !cmds.ran("targetcli", "/backstores/block", "create", sanitizeForTargetcli(testIQN), "/dev/zvol/tank/zvols/target0") {
		t.Errorf("storage object: %v", cmds.keys())
	}
	if r := call(t, UpdateISCSITarget, req{method: "POST", body: map[string]any{"iqn": testIQN, "zvol": "../../sda"}}); r.code != 400 {
		t.Errorf("traversal in zvol name: %s", r)
	}
}

// Real targetcli output: tree markers before the IQNs.
const targetcliISCSILs = `o- iscsi .............................................. [Targets: 2]
  o- iqn.2026-10.lan.nas:lun0 ............................... [TPGs: 1]
  o- iqn.2026-10.lan.nas:lun1 ............................... [TPGs: 1]
`

const targetcliACLsLs = `o- acls .................................................. [ACLs: 1]
  o- iqn.2026-10.lan.client:host1 ................... [Mapped LUNs: 1]
`

func TestISCSIListsParseTargetcli(t *testing.T) {
	fakeCommands(t, map[string]func([]string) ([]byte, error){
		"targetcli": func(a []string) ([]byte, error) {
			if a[0] == "/iscsi" {
				return []byte(targetcliISCSILs), nil
			}
			if strings.HasSuffix(a[0], "/acls") {
				return []byte(targetcliACLsLs), nil
			}
			return nil, nil
		},
		"systemctl": out("active\n"),
	})
	r := call(t, GetISCSITargets, req{})
	if !r.ok() || !strings.Contains(r.raw, "iqn.2026-10.lan.nas:lun0") || !strings.Contains(r.raw, "lun1") {
		t.Errorf("targets: %s", r)
	}
	// Without ?target=: ACLs of every target, with the fields the page reads.
	r = call(t, GetISCSIACLs, req{path: "/api/iscsi/acls"})
	if !r.ok() || strings.Count(r.raw, `"initiator":"iqn.2026-10.lan.client:host1"`) != 2 || !strings.Contains(r.raw, `"iqn":"iqn.2026-10.lan.nas:lun1"`) {
		t.Errorf("acls: %s", r)
	}
	r = call(t, GetISCSIStatus, req{})
	if r.body["target_count"] != float64(2) {
		t.Errorf("status: %s", r)
	}
}
