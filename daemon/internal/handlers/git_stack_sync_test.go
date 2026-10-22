package handlers

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func gitT(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// The clone follows the remote (fetch + reset, as Komodo does): a `git pull
// --rebase` stopped for good at the first local change. Unpushed local work
// is never thrown away: the update stops and says why.
func TestUpdateClone(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	root := t.TempDir()
	remote := filepath.Join(root, "remote.git")
	gitT(t, root, "init", "-q", "--bare", "-b", "main", remote)
	work := filepath.Join(root, "work")
	gitT(t, root, "clone", "-q", remote, work)
	gitT(t, work, "checkout", "-q", "-b", "main")
	os.WriteFile(filepath.Join(work, "docker-compose.yml"), []byte("services: {a: {image: x}}\n"), 0o644)
	gitT(t, work, "add", ".")
	gitT(t, work, "commit", "-q", "-m", "one")
	gitT(t, work, "push", "-q", "origin", "main")

	clone := filepath.Join(root, "clone")
	gitT(t, root, "clone", "-q", "-b", "main", remote, clone)

	// A new commit upstream arrives.
	os.WriteFile(filepath.Join(work, "docker-compose.yml"), []byte("services: {a: {image: y}}\n"), 0o644)
	gitT(t, work, "commit", "-q", "-am", "two")
	gitT(t, work, "push", "-q", "origin", "main")
	if err := updateClone(clone, remote, "main", nil); err != nil {
		t.Fatalf("update: %v", err)
	}
	if gitT(t, clone, "rev-parse", "HEAD") != gitT(t, work, "rev-parse", "HEAD") {
		t.Error("clone not at the remote head")
	}

	// Local edits that are not pushed: kept, the update stops.
	os.WriteFile(filepath.Join(clone, "docker-compose.yml"), []byte("services: {a: {image: local}}\n"), 0o644)
	if err := updateClone(clone, remote, "main", nil); err == nil || !strings.Contains(err.Error(), "local changes") {
		t.Errorf("dirty clone: %v", err)
	}
	if b, _ := os.ReadFile(filepath.Join(clone, "docker-compose.yml")); !strings.Contains(string(b), "local") {
		t.Error("local edit thrown away")
	}

	// A local commit that is not pushed: kept, the update stops.
	gitT(t, clone, "commit", "-q", "-am", "local")
	if err := updateClone(clone, remote, "main", nil); err == nil || !strings.Contains(err.Error(), "not pushed") {
		t.Errorf("unpushed commit: %v", err)
	}
}
