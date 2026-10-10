package handlers

import (
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
)

func testPublicKey(t *testing.T, comment string) string {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	sp, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	line := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(sp)))
	if comment != "" {
		line += " " + comment
	}
	return line
}

func TestParseSSHPublicKey(t *testing.T) {
	good := testPublicKey(t, "laptop")
	if kt, _, c, err := parseSSHPublicKey(good); err != nil || kt != "ssh-ed25519" || c != "laptop" {
		t.Fatalf("valid key: %s %s %v", kt, c, err)
	}
	other := testPublicKey(t, "")
	for name, bad := range map[string]string{
		"second line":   good + "\n" + other,
		"options":       `command="/bin/sh" ` + good,
		"bad blob":      "ssh-ed25519 AAAAnotakey",
		"type mismatch": "ssh-rsa " + strings.Fields(good)[1],
		"unknown type":  "ssh-foo AAAA",
	} {
		if _, _, _, err := parseSSHPublicKey(bad); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}

func TestSanitizeKeyLabel(t *testing.T) {
	if got := sanitizeKeyLabel("work\nssh-ed25519 AAAA attacker"); strings.Contains(got, "\n") {
		t.Errorf("newline kept: %q", got)
	}
	if got := sanitizeKeyLabel(strings.Repeat("x", 300)); len(got) != 128 {
		t.Errorf("length %d", len(got))
	}
}

// sshEnv: a fake system user with a temporary home and key store.
func sshEnv(t *testing.T) (home string) {
	t.Helper()
	home = t.TempDir()
	prevLookup, prevDir := sshLookupUser, ConfigDir
	uid, gid := "1000", "1000"
	if runtime.GOOS == "linux" {
		uid, gid = fmt.Sprint(os.Getuid()), fmt.Sprint(os.Getgid())
	}
	sshLookupUser = func(name string) (*user.User, error) {
		if name != "alice" {
			return nil, user.UnknownUserError(name)
		}
		return &user.User{Username: name, Uid: uid, Gid: gid, HomeDir: home}, nil
	}
	ConfigDir = t.TempDir()
	t.Cleanup(func() { sshLookupUser, ConfigDir = prevLookup, prevDir })
	fakeCommands(t, nil)
	return home
}

func TestAddSSHKeyWritesAuthorizedKeys(t *testing.T) {
	home := sshEnv(t)
	key := testPublicKey(t, "laptop")
	r := call(t, AddSSHKey, req{method: "POST", body: map[string]any{"username": "alice", "label": "work\nssh-ed25519 AAAAinjected x", "public_key": key}})
	if !r.ok() {
		t.Fatalf("add: %s", r)
	}
	b, err := os.ReadFile(filepath.Join(home, ".ssh", "authorized_keys"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	keyLines := 0
	for _, l := range lines {
		if !strings.HasPrefix(l, "#") {
			keyLines++
			if l != key {
				t.Errorf("unexpected key line %q", l)
			}
		}
	}
	if keyLines != 1 {
		t.Errorf("the label injected an authorized_keys entry:\n%s", b)
	}

	if r := call(t, AddSSHKey, req{method: "POST", body: map[string]any{"username": "alice", "public_key": key}}); r.code != 409 {
		t.Errorf("duplicate key: %s", r)
	}
	if r := call(t, AddSSHKey, req{method: "POST", body: map[string]any{"username": "alice", "public_key": key + "\n" + testPublicKey(t, "")}}); r.code != 400 {
		t.Errorf("two keys in one: %s", r)
	}
	if r := call(t, AddSSHKey, req{method: "POST", body: map[string]any{"username": "bob", "public_key": key}}); r.code != 400 {
		t.Errorf("unknown user: %s", r)
	}
}

func TestSSHKeysImportExisting(t *testing.T) {
	home := sshEnv(t)
	old := testPublicKey(t, "old-laptop")
	if err := os.MkdirAll(filepath.Join(home, ".ssh"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".ssh", "authorized_keys"), []byte(old+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if r := call(t, AddSSHKey, req{method: "POST", body: map[string]any{"username": "alice", "public_key": testPublicKey(t, "new")}}); !r.ok() {
		t.Fatalf("add: %s", r)
	}
	b, _ := os.ReadFile(filepath.Join(home, ".ssh", "authorized_keys"))
	if !strings.Contains(string(b), old) {
		t.Errorf("pre-existing key lost:\n%s", b)
	}
}

// A user who points ~/.ssh at another account's directory must not get
// the daemon (root) to write there.
func TestSSHKeysRefuseSymlinkedDir(t *testing.T) {
	home := sshEnv(t)
	victim := t.TempDir()
	if err := os.Symlink(victim, filepath.Join(home, ".ssh")); err != nil {
		t.Skipf("symlinks not available: %v", err)
	}
	r := call(t, AddSSHKey, req{method: "POST", body: map[string]any{"username": "alice", "public_key": testPublicKey(t, "")}})
	if _, err := os.Stat(filepath.Join(victim, "authorized_keys")); err == nil {
		t.Fatalf("authorized_keys written through a symlink (%s)", r)
	}
	if !strings.Contains(r.raw, "write failed") {
		t.Errorf("the failed write must be reported: %s", r)
	}
}
