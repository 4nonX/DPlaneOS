package gitops

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

type memSynced struct{ sha string }

func (m *memSynced) get() string          { return m.sha }
func (m *memSynced) set(sha string) error { m.sha = sha; return nil }

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@t", "-c", "init.defaultBranch=main"}, args...)...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// setup returns a bare "remote" holding one operator commit, an operator
// working copy of it, and the node's state clone.
func setup(t *testing.T) (remote, operator, clone string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	root := t.TempDir()
	remote = filepath.Join(root, "remote.git")
	operator = filepath.Join(root, "operator")
	clone = filepath.Join(root, "clone")
	git(t, root, "init", "--bare", "-b", "main", remote)
	git(t, root, "clone", remote, operator)
	operatorCommit(t, operator, "version: \"1\"\n# operator\n")
	git(t, root, "clone", "--branch", "main", remote, clone)
	return remote, operator, clone
}

func operatorCommit(t *testing.T, operator, content string) string {
	t.Helper()
	if err := os.WriteFile(filepath.Join(operator, stateFileName), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	git(t, operator, "add", "-A")
	git(t, operator, "-c", "user.name=op", "-c", "user.email=op@x", "commit", "-m", "operator change")
	git(t, operator, "push", "origin", "HEAD:main")
	return git(t, operator, "rev-parse", "HEAD")
}

func remoteHead(t *testing.T, remote string) string {
	return git(t, remote, "rev-parse", "main")
}

func TestCommitSnapshotFirstUseCommitsAndPushes(t *testing.T) {
	remote, _, clone := setup(t)
	store := &memSynced{}
	if err := commitSnapshot(store, clone, nil, "version: \"1\"\n# live\n", "main", "", ""); err != nil {
		t.Fatal(err)
	}
	if store.sha != remoteHead(t, remote) {
		t.Errorf("synced %q, remote %q", store.sha, remoteHead(t, remote))
	}
	if got := git(t, remote, "show", "main:"+stateFileName); !strings.Contains(got, "# live") {
		t.Errorf("snapshot not pushed: %s", got)
	}
}

// The bug this protects against: a GUI change snapshots live state and commits
// it over an operator's commit that was never applied, silently reverting it.
func TestCommitSnapshotRefusesUnappliedRemoteCommits(t *testing.T) {
	remote, operator, clone := setup(t)
	store := &memSynced{}
	if err := commitSnapshot(store, clone, nil, "version: \"1\"\n# live A\n", "main", "", ""); err != nil {
		t.Fatal(err)
	}
	git(t, operator, "pull", "origin", "main")
	opSHA := operatorCommit(t, operator, "version: \"1\"\n# operator B\n")

	err := commitSnapshot(store, clone, nil, "version: \"1\"\n# live C\n", "main", "", "")
	if !errors.Is(err, ErrUnappliedRemote) {
		t.Fatalf("want ErrUnappliedRemote, got %v", err)
	}
	if remoteHead(t, remote) != opSHA {
		t.Error("remote was changed although the commit was refused")
	}

	// After the operator's commit is applied (the drift check fast-forwarded the
	// clone, then apply recorded it), the next write-back goes through on top.
	if _, err := syncClone(clone, nil, "main"); err != nil {
		t.Fatal(err)
	}
	if err := store.set(git(t, clone, "rev-parse", "HEAD")); err != nil {
		t.Fatal(err)
	}
	if err := commitSnapshot(store, clone, nil, "version: \"1\"\n# live D\n", "main", "", ""); err != nil {
		t.Fatal(err)
	}
	if !isAncestor(clone, opSHA, remoteHead(t, remote)) {
		t.Error("operator commit is not in the pushed history")
	}
}

func TestSyncCloneFastForwardsAndResetsDivergedSnapshots(t *testing.T) {
	_, operator, clone := setup(t)
	// Unpushed local snapshot commit, then the operator pushes: histories diverge.
	if err := os.WriteFile(filepath.Join(clone, stateFileName), []byte("# local snapshot\n"), 0644); err != nil {
		t.Fatal(err)
	}
	git(t, clone, "add", "-A")
	git(t, clone, "commit", "-m", "snapshot")
	git(t, operator, "pull", "origin", "main")
	opSHA := operatorCommit(t, operator, "# operator\n")

	remote, err := syncClone(clone, nil, "main")
	if err != nil {
		t.Fatal(err)
	}
	if remote != opSHA || git(t, clone, "rev-parse", "HEAD") != opSHA {
		t.Errorf("clone not reset to the remote: remote=%s head=%s", remote, git(t, clone, "rev-parse", "HEAD"))
	}
}

func TestFetchRemoteFreshAndUnreachable(t *testing.T) {
	_, _, clone := setup(t)
	if sha, err := fetchRemote(clone, nil, "no-such-branch"); err != nil || sha != "" {
		t.Errorf("missing branch: want fresh (\"\", nil), got (%q, %v)", sha, err)
	}
	git(t, clone, "remote", "set-url", "origin", filepath.Join(t.TempDir(), "missing.git"))
	if _, err := fetchRemote(clone, nil, "main"); err == nil {
		t.Error("an unreachable remote must be an error, not a fresh repository")
	}
}

func TestFullyApplied(t *testing.T) {
	if !FullyApplied(&ApplyResult{Applied: []string{"create dataset tank/a"}}) {
		t.Error("plain apply should count as fully applied")
	}
	if FullyApplied(&ApplyResult{Applied: []string{"[DEFERRED no-quorum] create pool tank"}}) {
		t.Error("deferred pool operation must not count")
	}
	if FullyApplied(&ApplyResult{Failed: "tank/b"}) {
		t.Error("failed apply must not count")
	}
}
