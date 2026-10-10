package handlers

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestNFSValidators(t *testing.T) {
	root := shareRoot(t, "export")
	if validateNFSPath("relative/path") == nil || validateNFSPath("/mnt/../etc") == nil || validateNFSPath("/does/not/exist-xyz") == nil {
		t.Error("bad paths accepted")
	}
	if validateNFSPath(t.TempDir()) == nil {
		t.Error("a folder outside the file roots was accepted")
	}
	if err := validateNFSPath(filepath.Join(root, "export")); err != nil {
		t.Errorf("existing path: %v", err)
	}
	if os.Symlink(filepath.Join(root, "export"), filepath.Join(root, "link")) == nil && validateNFSPath(filepath.Join(root, "link")) == nil {
		t.Error("a symlinked path was accepted")
	}
	for _, ok := range []string{"*", "192.168.1.0/24", "10.0.0.1 nas2.lan", "*.example.com"} {
		if validateNFSClients(ok) != nil {
			t.Errorf("client %q rejected", ok)
		}
	}
	// Whitespace separates clients (each becomes its own exports line), so
	// only characters that could break out of a client spec are refused.
	for _, bad := range []string{"host(rw)", "a;b", "x,y(rw)"} {
		if validateNFSClients(bad) == nil {
			t.Errorf("client %q accepted", bad)
		}
	}
	if validateNFSOptions("rw,sync,anonuid=1000") != nil {
		t.Error("valid options rejected")
	}
	if validateNFSOptions("rw) *(rw") == nil || validateNFSOptions("rw\n/etc *(rw)") == nil {
		t.Error("option injection accepted")
	}
}

// nfsEnv: a database, an exports file in a temp dir, and an existing export path.
func nfsEnv(t *testing.T, answers map[string]func([]string) ([]byte, error)) (*NFSHandler, string, string, *fakeCmds) {
	db := testDB(t)
	dir := t.TempDir()
	prev := nfsExportsPath
	nfsExportsPath = filepath.Join(dir, "exports")
	t.Cleanup(func() { nfsExportsPath = prev })
	exp := filepath.Join(shareRoot(t, "export"), "export")
	return NewNFSHandler(db), nfsExportsPath, exp, fakeCommands(t, answers)
}

func TestNFSExportLifecycle(t *testing.T) {
	h, exports, exp, cmds := nfsEnv(t, nil)
	r := call(t, h.CreateNFSExport, req{method: "POST", body: map[string]any{"path": exp, "clients": "192.168.1.0/24 10.0.0.5"}})
	if !r.ok() {
		t.Fatalf("create: %s", r)
	}
	id := int(r.body["id"].(float64))
	b, _ := os.ReadFile(exports)
	for _, want := range []string{exp + "\t192.168.1.0/24(rw,sync,no_subtree_check,root_squash)", exp + "\t10.0.0.5("} {
		if !strings.Contains(string(b), want) {
			t.Errorf("exports lacks %q:\n%s", want, b)
		}
	}
	if !cmds.ran("exportfs", "-ra") {
		t.Errorf("exportfs not run: %v", cmds.keys())
	}

	path := "/api/nfs/exports/" + strconv.Itoa(id)
	if r := call(t, h.UpdateNFSExport, req{method: "POST", path: path + "/update", body: map[string]any{"enabled": false}}); !r.ok() {
		t.Fatalf("disable: %s", r)
	}
	b, _ = os.ReadFile(exports)
	if strings.Contains(string(b), "192.168.1.0/24") {
		t.Errorf("a disabled export is still exported:\n%s", b)
	}
	if r := call(t, h.UpdateNFSExport, req{method: "POST", path: path + "/update", body: map[string]any{"options": "rw) *(rw"}}); r.code != 400 {
		t.Errorf("option injection on update: %s", r)
	}
	if r := call(t, h.DeleteNFSExport, req{method: "DELETE", path: path}); !r.ok() {
		t.Fatalf("delete: %s", r)
	}
	list := call(t, h.ListNFSExports, req{})
	if strings.Contains(list.raw, `"id":`+strconv.Itoa(id)+`,`) {
		t.Errorf("deleted export still listed: %s", list)
	}
	if r := call(t, h.DeleteNFSExport, req{method: "DELETE", path: "/api/nfs/exports/abc"}); r.code != 400 {
		t.Errorf("bad id: %s", r)
	}
}

func TestNFSApplyFailureIsReported(t *testing.T) {
	h, _, exp, _ := nfsEnv(t, map[string]func([]string) ([]byte, error){"exportfs": fail("exportfs: /etc/exports:1: unknown keyword")})
	r := call(t, h.CreateNFSExport, req{method: "POST", body: map[string]any{"path": exp}})
	if r.ok() || !strings.Contains(r.raw, "Saved, but NFS was not updated") {
		t.Errorf("a failed exportfs must be reported, not \"applied\": %s", r)
	}
}

func TestNFSNotInstalled(t *testing.T) {
	h := NewNFSHandler(nil)
	fakeCommands(t, map[string]func([]string) ([]byte, error){"which": fail("")})
	if r := call(t, h.CreateNFSExport, req{method: "POST", body: map[string]any{"path": "/"}}); r.code != 503 {
		t.Errorf("without exportfs: %s", r)
	}
}

func TestNFSCreateDisabled(t *testing.T) {
	h, exports, exp, _ := nfsEnv(t, nil)
	if r := call(t, h.CreateNFSExport, req{method: "POST", body: map[string]any{"path": exp, "clients": "10.9.9.9", "enabled": false}}); !r.ok() {
		t.Fatalf("create: %s", r)
	}
	b, _ := os.ReadFile(exports)
	if strings.Contains(string(b), "10.9.9.9") {
		t.Errorf("an export created disabled is exported:\n%s", b)
	}
}
