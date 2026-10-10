package handlers

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// filesEnv: one file root (a temp dir) and a directory outside it.
func filesEnv(t *testing.T) (root, outside string) {
	root, outside = t.TempDir(), t.TempDir()
	prev := fileRoots
	fileRoots = []string{root}
	t.Cleanup(func() { fileRoots = prev })
	_ = os.WriteFile(filepath.Join(outside, "shadow"), []byte("secret"), 0600)
	_ = os.MkdirAll(filepath.Join(root, "share"), 0755)
	_ = os.WriteFile(filepath.Join(root, "share", "doc.txt"), []byte("hello"), 0644)
	return root, outside
}

func TestFilesStayInRoots(t *testing.T) {
	root, outside := filesEnv(t)
	h := NewFilesExtendedHandler()
	if r := call(t, h.ListFiles, req{path: "/api/files/list?path=" + outside}); r.ok() {
		t.Errorf("listing outside the roots: %s", r)
	}
	if r := call(t, h.ListFiles, req{path: "/api/files/list?path=" + filepath.Join(root, "share")}); !r.ok() || !strings.Contains(r.raw, "doc.txt") {
		t.Errorf("listing a share: %s", r)
	}
	if r := call(t, h.ReadFile, req{path: "/api/files/read?path=" + filepath.Join(outside, "shadow")}); r.ok() {
		t.Errorf("reading outside: %s", r)
	}
	if r := call(t, h.WriteFile, req{method: "POST", body: map[string]any{"path": filepath.Join(root, "share", "x.sh"), "content": "x", "mode": "4755"}}); r.ok() {
		t.Errorf("setuid mode accepted: %s", r)
	}
	if r := call(t, h.WriteFile, req{method: "POST", body: map[string]any{"path": filepath.Join(root, "share", "new.txt"), "content": "x", "mode": "0640"}}); !r.ok() {
		t.Errorf("write: %s", r)
	}
}

// A share user's symlink must not let the daemon (root) leave the roots.
func TestFilesRefuseSymlinkEscapes(t *testing.T) {
	root, outside := filesEnv(t)
	h := NewFilesExtendedHandler()
	link := filepath.Join(root, "share", "etc")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlinks not available: %v", err)
	}
	if r := call(t, h.ReadFile, req{path: "/api/files/read?path=" + filepath.Join(link, "shadow")}); r.ok() {
		t.Errorf("read through symlink: %s", r)
	}
	if r := call(t, h.DownloadFile, req{path: "/api/files/download?path=" + filepath.Join(link, "shadow")}); r.code == 200 {
		t.Errorf("download through symlink: %d", r.code)
	}
	if r := call(t, h.WriteFile, req{method: "POST", body: map[string]any{"path": filepath.Join(link, "passwd"), "content": "x"}}); r.ok() {
		t.Errorf("write through symlinked dir: %s", r)
	}
	if _, err := os.Stat(filepath.Join(outside, "passwd")); err == nil {
		t.Fatal("file written outside the roots")
	}
	fileLink := filepath.Join(root, "share", "evil.txt")
	_ = os.Symlink(filepath.Join(outside, "shadow"), fileLink)
	if r := call(t, h.WriteFile, req{method: "POST", body: map[string]any{"path": fileLink, "content": "pwned"}}); r.ok() {
		t.Errorf("write through a file symlink: %s", r)
	}
	if b, _ := os.ReadFile(filepath.Join(outside, "shadow")); string(b) != "secret" {
		t.Fatal("target of a symlink overwritten")
	}
	if r := call(t, h.CopyFile, req{method: "POST", body: map[string]any{"source": filepath.Join(root, "share", "doc.txt"), "destination": link}}); r.ok() {
		t.Errorf("copy into a symlinked dir: %s", r)
	}
	// Deleting a symlink removes the link, never its target.
	fakeCommands(t, nil)
	if r := call(t, DeletePath, req{method: "POST", body: map[string]any{"path": link}}); !r.ok() {
		t.Errorf("delete link: %s", r)
	}
	if _, err := os.Stat(filepath.Join(outside, "shadow")); err != nil {
		t.Error("symlink target deleted")
	}
}

func TestParseFileMode(t *testing.T) {
	for _, ok := range []string{"644", "0755", "1777"} {
		if _, err := parseFileMode(ok); err != nil {
			t.Errorf("%s: %v", ok, err)
		}
	}
	for _, bad := range []string{"4755", "2755", "6777", "9", "-R"} {
		if _, err := parseFileMode(bad); err == nil {
			t.Errorf("%s accepted", bad)
		}
	}
}
