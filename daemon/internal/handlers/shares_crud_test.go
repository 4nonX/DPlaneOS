package handlers

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestShareCreateValidation(t *testing.T) {
	h := NewShareCRUDHandler(nil, filepath.Join(t.TempDir(), "smb.conf"))
	fakeCommands(t, nil)
	for _, c := range []struct {
		name string
		body map[string]any
	}{
		{"missing path", map[string]any{"action": "create", "name": "media"}},
		{"bad name", map[string]any{"action": "create", "name": "me dia;", "path": "/mnt/tank/media"}},
		{"relative path", map[string]any{"action": "create", "name": "media", "path": "mnt/tank"}},
		{"bad host list", map[string]any{"action": "create", "name": "media", "path": "/mnt/tank", "hosts_allow": "10.0.0.0/8\n[global]"}},
		{"unknown action", map[string]any{"action": "explode"}},
	} {
		r := call(t, h.HandleShares, req{method: "POST", path: "/api/shares", body: c.body})
		if r.code != 400 {
			t.Errorf("%s: want 400, got %s", c.name, r)
		}
	}
	if r := call(t, h.HandleShares, req{method: "PATCH", path: "/api/shares"}); r.code != 405 {
		t.Errorf("PATCH: %s", r)
	}
}

func TestShareLifecycle(t *testing.T) {
	db := testDB(t)
	conf := filepath.Join(t.TempDir(), "smb.conf")
	h := NewShareCRUDHandler(db, conf)
	cmds := fakeCommands(t, nil)

	r := call(t, h.HandleShares, req{method: "POST", path: "/api/shares", body: map[string]any{
		"action": "create", "name": "media", "path": "/mnt/tank/media/", "comment": "Films", "read_only": true,
	}})
	if !r.ok() {
		t.Fatalf("create: %s", r)
	}
	id := int(r.body["id"].(float64))
	b, err := os.ReadFile(conf)
	if err != nil {
		t.Fatalf("smb.conf not written: %v", err)
	}
	for _, want := range []string{"[media]", "path = /mnt/tank/media", "comment = Films", "read only = yes"} {
		if !strings.Contains(string(b), want) {
			t.Errorf("smb.conf lacks %q:\n%s", want, b)
		}
	}
	if !cmds.ran("smbcontrol", "all", "reload-config") {
		t.Errorf("Samba not reloaded: %v", cmds.keys())
	}

	if r := call(t, h.HandleShares, req{method: "POST", path: "/api/shares", body: map[string]any{
		"action": "create", "name": "media", "path": "/mnt/tank/other",
	}}); r.code != 409 {
		t.Errorf("duplicate name: %s", r)
	}

	list := call(t, h.HandleShares, req{path: "/api/shares"})
	if !list.ok() || !strings.Contains(list.raw, `"media"`) {
		t.Errorf("list: %s", list)
	}

	if r := call(t, h.HandleShares, req{method: "POST", path: "/api/shares", body: map[string]any{
		"action": "update", "id": id, "comment": "Movies",
	}}); !r.ok() {
		t.Fatalf("update: %s", r)
	}
	b, _ = os.ReadFile(conf)
	if !strings.Contains(string(b), "comment = Movies") {
		t.Errorf("update not in smb.conf:\n%s", b)
	}

	if r := call(t, h.HandleShares, req{method: "POST", path: "/api/shares", body: map[string]any{
		"action": "delete", "id": id,
	}}); !r.ok() {
		t.Fatalf("delete: %s", r)
	}
	b, _ = os.ReadFile(conf)
	if strings.Contains(string(b), "[media]") {
		t.Errorf("deleted share still in smb.conf:\n%s", b)
	}
}

func TestShareSambaReloadFailureIsReported(t *testing.T) {
	db := testDB(t)
	h := NewShareCRUDHandler(db, filepath.Join(t.TempDir(), "smb.conf"))
	fakeCommands(t, map[string]func([]string) ([]byte, error){"smbcontrol": fail("smbd not running")})
	r := call(t, h.HandleShares, req{method: "POST", path: "/api/shares", body: map[string]any{
		"action": "create", "name": "docs", "path": "/mnt/tank/docs",
	}})
	if r.ok() || !strings.Contains(r.raw, "Saved, but Samba was not updated") {
		t.Errorf("a failed reload must be reported: %s", r)
	}
}

func TestDisconnectSMBSession(t *testing.T) {
	h := NewShareCRUDHandler(nil, "")
	cmds := fakeCommands(t, nil)
	if r := call(t, h.DisconnectSMBSession, req{method: "POST", body: map[string]any{"id": "12; rm -rf /"}}); r.code != 400 {
		t.Errorf("non-numeric id: %s", r)
	}
	if r := call(t, h.DisconnectSMBSession, req{method: "POST", body: map[string]any{}}); r.code != 400 {
		t.Errorf("missing id: %s", r)
	}
	if r := call(t, h.DisconnectSMBSession, req{method: "POST", body: map[string]any{"id": "4242"}}); !r.ok() {
		t.Errorf("disconnect: %s", r)
	}
	if !cmds.ran("smbcontrol", "4242", "shutdown") {
		t.Errorf("smbcontrol not called: %v", cmds.keys())
	}
}
