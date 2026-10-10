package handlers

import (
	"strings"
	"testing"
)

func TestValidatePassphrase(t *testing.T) {
	if validatePassphrase("long enough") != nil {
		t.Error("valid passphrase rejected")
	}
	for _, bad := range []string{"short", strings.Repeat("x", 513), "first line\nsecond line"} {
		if validatePassphrase(bad) == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestCreateEncryptedDataset(t *testing.T) {
	h := NewZFSEncryptionHandler()
	cmds := fakeCommands(t, nil)
	for name, body := range map[string]map[string]any{
		"short key":   {"name": "tank/secret", "key": "short"},
		"newline key": {"name": "tank/secret", "key": "passphrase\nother"},
		"bad algo":    {"name": "tank/secret", "key": "passphrase", "encryption": "rot13"},
		"bad name":    {"name": "tank/../x", "key": "passphrase"},
	} {
		if r := call(t, h.CreateEncryptedDataset, req{method: "POST", body: body}); r.code != 400 {
			t.Errorf("%s: %s", name, r)
		}
	}
	if r := call(t, h.CreateEncryptedDataset, req{method: "POST", body: map[string]any{"name": "tank/secret", "key": "passphrase"}}); !r.ok() {
		t.Fatalf("create: %s", r)
	}
	c := cmds.find("zfs", "create")
	if c == nil || !strings.Contains(strings.Join(c.Args, " "), "encryption=aes-256-gcm") {
		t.Fatalf("zfs create: %v", cmds.keys())
	}
	if c.Stdin != "passphrase\npassphrase\n" {
		t.Errorf("stdin %q", c.Stdin)
	}
	for _, a := range c.Args {
		if strings.Contains(a, "passphrase") && a != "keyformat=passphrase" {
			t.Errorf("passphrase on the command line: %v", c.Args)
		}
	}
}

func TestChangeKey(t *testing.T) {
	h := NewZFSEncryptionHandler()
	body := map[string]any{"dataset": "tank/secret", "old_key": "old passphrase", "new_key": "new passphrase"}

	// Wrong old passphrase: nothing is changed.
	cmds := fakeCommands(t, map[string]func([]string) ([]byte, error){
		"zfs": func(a []string) ([]byte, error) {
			if a[0] == "load-key" {
				return fail("Key load error: Incorrect key provided")(a)
			}
			return nil, nil
		},
	})
	if r := call(t, h.ChangeKey, req{method: "POST", body: body}); r.ok() || !strings.Contains(r.raw, "current passphrase is wrong") {
		t.Errorf("wrong old key: %s", r)
	}
	if cmds.ran("zfs", "change-key") {
		t.Fatal("key changed without the right old passphrase")
	}

	// Unlocked dataset (key loaded): verify, then change with only the new key on stdin.
	cmds = fakeCommands(t, map[string]func([]string) ([]byte, error){
		"zfs": func(a []string) ([]byte, error) {
			if a[0] == "get" {
				return []byte("available\n"), nil
			}
			return nil, nil
		},
	})
	if r := call(t, h.ChangeKey, req{method: "POST", body: body}); !r.ok() {
		t.Fatalf("change: %s", r)
	}
	if c := cmds.find("zfs", "load-key", "-n"); c == nil || c.Stdin != "old passphrase\n" {
		t.Errorf("old key not verified: %v", cmds.keys())
	}
	if cmds.ran("zfs", "load-key", "tank/secret") {
		t.Error("a loaded key was loaded again")
	}
	c := cmds.find("zfs", "change-key")
	if c == nil || c.Stdin != "new passphrase\nnew passphrase\n" {
		t.Fatalf("change-key: %v", cmds.keys())
	}

	// Locked dataset: the key is loaded first.
	cmds = fakeCommands(t, map[string]func([]string) ([]byte, error){
		"zfs": func(a []string) ([]byte, error) {
			if a[0] == "get" {
				return []byte("unavailable\n"), nil
			}
			return nil, nil
		},
	})
	if r := call(t, h.ChangeKey, req{method: "POST", body: body}); !r.ok() {
		t.Fatalf("change on a locked dataset: %s", r)
	}
	if !cmds.ran("zfs", "load-key", "tank/secret") {
		t.Errorf("locked key not loaded: %v", cmds.keys())
	}

	if r := call(t, h.ChangeKey, req{method: "POST", body: map[string]any{"dataset": "tank/secret", "old_key": "old passphrase", "new_key": "short"}}); r.code != 400 {
		t.Errorf("short new key: %s", r)
	}
}

func TestUnlockLockDataset(t *testing.T) {
	h := NewZFSEncryptionHandler()
	cmds := fakeCommands(t, nil)
	if r := call(t, h.UnlockDataset, req{method: "POST", body: map[string]any{"dataset": "tank/secret", "key": "a\nb"}}); r.code != 400 {
		t.Errorf("newline key: %s", r)
	}
	if r := call(t, h.UnlockDataset, req{method: "POST", body: map[string]any{"dataset": "tank/secret", "key": "passphrase"}}); !r.ok() {
		t.Fatalf("unlock: %s", r)
	}
	if c := cmds.find("zfs", "load-key", "tank/secret"); c == nil || c.Stdin != "passphrase\n" {
		t.Errorf("load-key: %v", cmds.keys())
	}
	if r := call(t, h.LockDataset, req{method: "POST", body: map[string]any{"dataset": "tank/secret"}}); !r.ok() || !cmds.ran("zfs", "unload-key", "tank/secret") {
		t.Errorf("lock: %s %v", r, cmds.keys())
	}
	fakeCommands(t, map[string]func([]string) ([]byte, error){"zfs": fail("Incorrect key provided")})
	if r := call(t, h.UnlockDataset, req{method: "POST", body: map[string]any{"dataset": "tank/secret", "key": "passphrase"}}); r.ok() {
		t.Errorf("wrong key reported as success: %s", r)
	}
}
