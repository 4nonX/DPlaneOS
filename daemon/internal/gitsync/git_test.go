package gitsync

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func needGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
}

func bare(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "remote.git")
	if out, err := exec.Command("git", "init", "--quiet", "--bare", dir).CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	return dir
}

func clone(t *testing.T, remotes ...string) *Clone {
	t.Helper()
	c := &Clone{Dir: filepath.Join(t.TempDir(), "clone"), Branch: "main"}
	for i, r := range remotes {
		c.Remotes = append(c.Remotes, Remote{Name: "r" + string(rune('0'+i)), URL: r})
	}
	if err := c.Ensure(); err != nil {
		t.Fatal(err)
	}
	return c
}

var me = Identity{Name: "DPlaneOS nas1", Email: "nas1@dplaneos.local"}

func TestCommitPushFetchShow(t *testing.T) {
	needGit(t)
	remote := bare(t)
	c := clone(t, remote)

	head, _, err := c.FetchBranch("main")
	if err != nil || head != "" {
		t.Fatalf("empty repository: %q %v", head, err)
	}
	first, err := c.Commit("", map[string]string{"cluster/state.yaml": "version: \"1\"\n"}, "first", me)
	if err != nil {
		t.Fatal(err)
	}
	if res := c.Push(first, "main", false); !res[0].OK {
		t.Fatalf("push: %+v", res)
	}
	// Nothing changed: no new commit.
	same, err := c.Commit(first, map[string]string{"cluster/state.yaml": "version: \"1\"\n"}, "again", me)
	if err != nil || same != first {
		t.Fatalf("unchanged commit: %q %v", same, err)
	}
	second, err := c.Commit(first, map[string]string{"nodes/nas1/state.yaml": "version: \"1\"\n", "cluster/state.yaml": ""}, "second", me)
	if err != nil || second == first {
		t.Fatal(err)
	}
	c.Push(second, "main", false)

	other := clone(t, remote)
	got, _, err := other.FetchBranch("main")
	if err != nil || got != second {
		t.Fatalf("fetch: %q %v, want %q", got, err, second)
	}
	if s, _ := other.Show(got, "nodes/nas1/state.yaml"); s != "version: \"1\"\n" {
		t.Errorf("show: %q", s)
	}
	if s, err := other.Show(got, "cluster/state.yaml"); s != "" || err != nil {
		t.Errorf("removed file: %q %v", s, err)
	}
	if cs, _ := other.Commits(first, second); len(cs) != 1 || cs[0] != second {
		t.Errorf("commits: %v", cs)
	}
	if other.Subject(second) != "second" {
		t.Errorf("subject %q", other.Subject(second))
	}

	// A push from an outdated parent is rejected, never forced.
	stale, _ := c.Commit(first, map[string]string{"x.yaml": "a: 1\n"}, "stale", me)
	if res := c.Push(stale, "main", false); res[0].OK || !res[0].Rejected {
		t.Fatalf("stale push: %+v", res)
	}
}

func TestFetchPicksNewestAndRefusesDivergence(t *testing.T) {
	needGit(t)
	a, b := bare(t), bare(t)
	c := clone(t, a, b)
	one, _ := c.Commit("", map[string]string{"s.yaml": "v: 1\n"}, "one", me)
	two, _ := c.Commit(one, map[string]string{"s.yaml": "v: 2\n"}, "two", me)
	c.Push(two, "main", false)
	// r1 is behind: push only "one" there.
	(&Clone{Dir: c.Dir, Remotes: c.Remotes[1:]}).Push(one, "main", false)
	head, heads, err := c.FetchBranch("main")
	if err != nil || head != two || len(heads) != 2 {
		t.Fatalf("newest: %q %v %+v", head, err, heads)
	}
	// r1 gets a different history.
	other, _ := c.Commit(one, map[string]string{"s.yaml": "v: other\n"}, "other", me)
	(&Clone{Dir: c.Dir, Remotes: c.Remotes[1:]}).Push(other, "main", true)
	if _, _, err := c.FetchBranch("main"); err == nil || !strings.Contains(err.Error(), "diverged") {
		t.Fatalf("divergence: %v", err)
	}
	// An unreachable remote does not stop the others.
	c.Remotes = append(c.Remotes[:1], Remote{Name: "r1", URL: filepath.Join(t.TempDir(), "missing.git")})
	if err := c.Ensure(); err != nil {
		t.Fatal(err)
	}
	if head, heads, err := c.FetchBranch("main"); err != nil || head != two || heads[1].Err == nil {
		t.Fatalf("one remote down: %q %v %+v", head, err, heads)
	}
}

func TestSignedCommitsVerify(t *testing.T) {
	needGit(t)
	if _, err := exec.LookPath("ssh-keygen"); err != nil {
		t.Skip("ssh-keygen not installed")
	}
	dir := t.TempDir()
	key := filepath.Join(dir, "signing")
	if out, err := exec.Command("ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-C", "nas1", "-f", key).CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	pub, _ := os.ReadFile(key + ".pub")
	c := clone(t, bare(t))
	signer := me
	signer.SigningKey = key
	signed, err := c.Commit("", map[string]string{"s.yaml": "v: 1\n"}, "signed", signer)
	if err != nil {
		t.Fatal(err)
	}
	unsigned, _ := c.Commit(signed, map[string]string{"s.yaml": "v: 2\n"}, "unsigned", me)

	allowed := Signers{SSH: string(pub)}
	if bad, err := c.VerifyCommits([]string{signed}, allowed); err != nil {
		t.Fatalf("signed commit refused (%s): %v", bad, err)
	}
	bad, err := c.VerifyCommits([]string{signed, unsigned}, allowed)
	if !errors.Is(err, ErrUnsigned) || bad != unsigned {
		t.Fatalf("unsigned commit: %s %v", bad, err)
	}
	// Another key is not allowed.
	other := filepath.Join(dir, "other")
	exec.Command("ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-f", other).Run()
	otherPub, _ := os.ReadFile(other + ".pub")
	if _, err := c.VerifyCommits([]string{signed}, Signers{SSH: string(otherPub)}); !errors.Is(err, ErrUnsigned) {
		t.Fatalf("foreign key accepted: %v", err)
	}
	if _, err := c.VerifyCommits([]string{signed}, Signers{}); err == nil {
		t.Fatal("no key configured must fail")
	}
}

func TestRedactsCredentialsInErrors(t *testing.T) {
	e := &GitError{Args: []string{"ls-remote", "https://user:tok3n@git.lan/x.git"}, Err: errors.New("exit status 128")}
	if strings.Contains(e.Error(), "tok3n") {
		t.Fatal(e.Error())
	}
}

func TestVerifyWebhook(t *testing.T) {
	body := []byte(`{"ref":"refs/heads/main"}`)
	mac := hmac.New(sha256.New, []byte("s3cret"))
	mac.Write(body)
	sig := hex.EncodeToString(mac.Sum(nil))

	for _, c := range []struct {
		header, value, provider string
		ok                      bool
	}{
		{"X-Hub-Signature-256", "sha256=" + sig, "github", true},
		{"X-Hub-Signature-256", "sha256=00" + sig[2:], "github", false},
		{"X-Gitea-Signature", sig, "gitea", true},
		{"X-Forgejo-Signature", sig, "gitea", true},
		{"X-Gitlab-Token", "s3cret", "gitlab", true},
		{"X-Gitlab-Token", "wrong", "gitlab", false},
	} {
		h := http.Header{}
		h.Set(c.header, c.value)
		p, ok := VerifyWebhook(h, body, "s3cret")
		if p != c.provider || ok != c.ok {
			t.Errorf("%s=%s: %s %v", c.header, c.value, p, ok)
		}
	}
	if _, ok := VerifyWebhook(http.Header{}, body, "s3cret"); ok {
		t.Error("unsigned request accepted")
	}
	h := http.Header{}
	h.Set("X-Gitlab-Token", "")
	if _, ok := VerifyWebhook(h, body, ""); ok {
		t.Error("no secret configured must refuse")
	}
}
