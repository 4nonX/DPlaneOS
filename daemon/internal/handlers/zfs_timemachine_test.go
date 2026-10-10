package handlers

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// tmEnv: a dataset mounted at a temp dir with snapshot s1 holding photos/a.jpg.
func tmEnv(t *testing.T) (h *ZFSTimeMachineHandler, mount string) {
	mount = t.TempDir()
	fakeCommands(t, map[string]func([]string) ([]byte, error){"zfs": out(mount + "\n")})
	snap := filepath.Join(mount, ".zfs", "snapshot", "s1", "photos")
	if err := os.MkdirAll(snap, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(snap, "a.jpg"), []byte("old"), 0644); err != nil {
		t.Fatal(err)
	}
	return NewZFSTimeMachineHandler(), mount
}

func TestTimeMachineBrowseAndRestore(t *testing.T) {
	h, mount := tmEnv(t)
	r := call(t, h.BrowseSnapshot, req{path: "/api/timemachine/browse?snapshot=tank/data@s1&path=/photos"})
	if !r.ok() || !strings.Contains(r.raw, "a.jpg") {
		t.Fatalf("browse: %s", r)
	}
	if r := call(t, h.RestoreFile, req{method: "POST", body: map[string]any{"snapshot": "tank/data@s1", "source_path": "/photos/a.jpg"}}); !r.ok() {
		t.Fatalf("restore: %s", r)
	}
	b, _ := os.ReadFile(filepath.Join(mount, "photos", "a.jpg"))
	if string(b) != "old" {
		t.Errorf("restored content %q", b)
	}
	if r := call(t, h.RestoreFile, req{method: "POST", body: map[string]any{"snapshot": "tank/data@s1", "source_path": "/photos/a.jpg"}}); r.code != 409 {
		t.Errorf("existing destination without overwrite: %s", r)
	}
	if r := call(t, h.RestoreFile, req{method: "POST", body: map[string]any{"snapshot": "tank/data@s1", "source_path": "/photos/a.jpg", "overwrite": true}}); !r.ok() {
		t.Errorf("overwrite: %s", r)
	}
	if r := call(t, h.RestoreFile, req{method: "POST", body: map[string]any{"snapshot": "tank/data@s1", "source_path": "/../../etc/passwd"}}); r.code == 200 {
		t.Errorf("traversal: %s", r)
	}
}

// A share user's symlink must not make the daemon (root) write or read
// outside the dataset.
func TestTimeMachineRefusesSymlinks(t *testing.T) {
	h, mount := tmEnv(t)
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(mount, "photos")); err != nil {
		t.Skipf("symlinks not available: %v", err)
	}
	r := call(t, h.RestoreFile, req{method: "POST", body: map[string]any{"snapshot": "tank/data@s1", "source_path": "/photos/a.jpg"}})
	if r.code == 200 {
		t.Errorf("restore through a symlinked directory: %s", r)
	}
	if _, err := os.Stat(filepath.Join(outside, "a.jpg")); err == nil {
		t.Fatal("file written outside the dataset")
	}

	secret := filepath.Join(outside, "shadow")
	_ = os.WriteFile(secret, []byte("secret"), 0600)
	link := filepath.Join(mount, ".zfs", "snapshot", "s1", "photos", "evil")
	if err := os.Symlink(secret, link); err != nil {
		t.Skip(err)
	}
	if r := call(t, h.RestoreFile, req{method: "POST", body: map[string]any{"snapshot": "tank/data@s1", "source_path": "/photos/evil", "dest_path": "/copy"}}); r.code == 200 {
		t.Errorf("restore of a symlink in the snapshot: %s", r)
	}
	if err := os.Symlink(outside, filepath.Join(mount, ".zfs", "snapshot", "s1", "out")); err == nil {
		if r := call(t, h.BrowseSnapshot, req{path: "/api/timemachine/browse?snapshot=tank/data@s1&path=/out"}); r.ok() {
			t.Errorf("browse through a symlink: %s", r)
		}
	}
}
