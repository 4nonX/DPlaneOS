package handlers

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// shareRoot makes a temp dir the only file root and returns its real path
// with the given folders created in it.
func shareRoot(t *testing.T, dirs ...string) string {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	prev := fileRoots
	fileRoots = []string{root}
	t.Cleanup(func() { fileRoots = prev })
	for _, d := range dirs {
		_ = os.MkdirAll(filepath.Join(root, d), 0755)
	}
	return root
}

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

func TestTrashRoundTrip(t *testing.T) {
	root, _ := filesEnv(t)
	h := NewTrashHandler()
	doc := filepath.Join(root, "share", "doc.txt")
	r := call(t, h.MoveToTrash, req{method: "POST", body: map[string]any{"path": doc}})
	if !r.ok() {
		t.Fatalf("trash: %s", r)
	}
	if _, err := os.Stat(doc); err == nil {
		t.Fatal("file still in place")
	}
	var name string
	if r := call(t, h.ListTrash, req{}); !r.ok() || !strings.Contains(r.raw, "doc.txt") {
		t.Fatalf("list: %s", r)
	} else {
		name = r.body["items"].([]any)[0].(map[string]any)["name"].(string)
	}
	if r := call(t, h.RestoreFromTrash, req{method: "POST", body: map[string]any{"name": name}}); !r.ok() {
		t.Fatalf("restore: %s", r)
	}
	if b, _ := os.ReadFile(doc); string(b) != "hello" {
		t.Fatal("not restored")
	}
}

func TestTrashRefusesEscapes(t *testing.T) {
	root, outside := filesEnv(t)
	h := NewTrashHandler()
	if r := call(t, h.MoveToTrash, req{method: "POST", body: map[string]any{"path": filepath.Join(root, "..", filepath.Base(outside), "shadow")}}); r.ok() {
		t.Errorf("trashed a file outside the roots: %s", r)
	}
	for _, name := range []string{"../share/doc.txt", "..", "x.meta"} {
		if r := call(t, h.RestoreFromTrash, req{method: "POST", body: map[string]any{"name": name}}); r.ok() {
			t.Errorf("restore %q accepted: %s", name, r)
		}
	}
	// A planted item whose .meta points outside the roots is not restored.
	trash := filepath.Join(root, trashDirName)
	if err := checkTrashDir(trash, true); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(trash, "evil"), []byte("x"), 0600)
	_ = os.WriteFile(filepath.Join(trash, "evil.meta"), []byte(filepath.Join(outside, "cron")), 0600)
	if r := call(t, h.RestoreFromTrash, req{method: "POST", body: map[string]any{"name": "evil"}}); r.ok() {
		t.Errorf("restored outside the roots: %s", r)
	}
	if _, err := os.Stat(filepath.Join(outside, "cron")); err == nil {
		t.Fatal("file placed outside the roots")
	}
}

// Public share links: only files within the roots, re-checked at download.
func TestFileSharesStayInRoots(t *testing.T) {
	root, outside := filesEnv(t)
	prevConfig := ConfigDir
	SetConfigDir(t.TempDir())
	t.Cleanup(func() { ConfigDir = prevConfig })
	if r := call(t, CreateFileShare, req{method: "POST", body: map[string]any{"path": filepath.Join(outside, "shadow")}}); r.ok() || r.code == 200 {
		t.Errorf("public link to a file outside the roots: %s", r)
	}
	doc := filepath.Join(root, "share", "doc.txt")
	r := call(t, CreateFileShare, req{method: "POST", body: map[string]any{"path": doc}})
	if !r.ok() {
		t.Fatalf("share: %s", r)
	}
	share, _ := r.body["share"].(map[string]any)
	token, _ := share["token"].(string)
	if token == "" {
		t.Fatalf("no token: %s", r)
	}
	dl := func() resp {
		return call(t, DownloadFileShare, req{path: "/api/s/" + token + "/download", vars: map[string]string{"token": token}})
	}
	if r := dl(); r.code != 200 || r.raw != "hello" {
		t.Fatalf("download: %s", r)
	}
	// Replaced by a symlink to a secret: refused.
	_ = os.Remove(doc)
	if err := os.Symlink(filepath.Join(outside, "shadow"), doc); err != nil {
		t.Skipf("symlinks not available: %v", err)
	}
	if r := dl(); r.code == 200 {
		t.Errorf("download followed a swapped-in symlink: %s", r)
	}
}

func TestCloudSyncLocalPathInRoots(t *testing.T) {
	root, outside := filesEnv(t)
	fakeCommands(t, nil)
	h := NewCloudSyncHandler()
	link := filepath.Join(root, "share", "etc")
	symlinks := os.Symlink(outside, link) == nil
	for _, p := range []string{outside, filepath.Join(root, "..", "x")} {
		if r := call(t, h.runSync, req{method: "POST", body: map[string]any{"remote": "b2", "local_path": p, "direction": "download"}}); r.code != 400 {
			t.Errorf("download to %s: %s", p, r)
		}
	}
	if symlinks {
		if r := call(t, h.runSync, req{method: "POST", body: map[string]any{"remote": "b2", "local_path": link, "direction": "download"}}); r.code != 400 {
			t.Errorf("download through a symlink: %s", r)
		}
	}
}

// A pool mounted at /<pool> is a file root; system directories never are.
func TestZFSMountsAreRoots(t *testing.T) {
	dir := t.TempDir()
	mounts := filepath.Join(dir, "mounts")
	_ = os.WriteFile(mounts, []byte("rpool/root / zfs rw 0 0\nrpool/nix /nix zfs rw 0 0\nrpool/var /var/lib zfs rw 0 0\ntank /tank2 zfs rw 0 0\ntank/my\\040data /tank2/my\\040data zfs rw 0 0\n/dev/sda1 /backup ext4 rw 0 0\n"), 0644)
	prevFile, prevRoots := mountsFile, fileRoots
	mountsFile, fileRoots = mounts, nil
	zfsRootsAt = time.Time{}
	t.Cleanup(func() { mountsFile, fileRoots, zfsRootsAt = prevFile, prevRoots, time.Time{} })
	got := strings.Join(FileRoots(), "|")
	want := filepath.Clean("/tank2") + "|" + filepath.Clean("/tank2/my data")
	if got != want {
		t.Errorf("roots = %q, want %q", got, want)
	}
}
