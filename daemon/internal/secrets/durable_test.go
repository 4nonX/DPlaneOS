package secrets

import (
	"os"
	"path/filepath"
	"testing"
)

func resetFallback() {
	mu.Lock()
	fallbackGCM = nil
	mu.Unlock()
}

// A key file left empty by a power loss (versions before 16.0 did not sync
// it) is replaced instead of stopping the daemon for good.
func TestEmptyKeyFileIsReplaced(t *testing.T) {
	resetFallback()
	key := filepath.Join(t.TempDir(), "secrets.key")
	if err := os.WriteFile(key, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Init(key); err != nil {
		t.Fatalf("Init with an empty key file: %v", err)
	}
	if b, _ := os.ReadFile(key); len(b) != 32 {
		t.Fatalf("new key has %d bytes", len(b))
	}
	if s, err := Seal("x"); err != nil || s == "" {
		t.Fatalf("Seal: %q %v", s, err)
	}

	// A key of another wrong length is not touched.
	if err := os.WriteFile(key, []byte("short"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Init(key); err == nil {
		t.Fatal("a 5-byte key must be refused")
	}
	if b, _ := os.ReadFile(key); string(b) != "short" {
		t.Fatal("a key of the wrong length was overwritten")
	}
}

// A crash between the re-encryption commit and the rename of the new key:
// values sealed with either key still open after the restart.
func TestInterruptedRotationKeepsBothKeysUsable(t *testing.T) {
	resetFallback()
	key := filepath.Join(t.TempDir(), "secrets.key")
	if err := Init(key); err != nil {
		t.Fatal(err)
	}
	old, err := Seal("sealed-with-old")
	if err != nil {
		t.Fatal(err)
	}
	_, sealNew, _, err := PrepareRotation(key)
	if err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(key + ".new"); err != nil || len(b) != 32 {
		t.Fatalf("the new key must be on disk before anything is sealed with it: %d bytes, %v", len(b), err)
	}
	reencrypted, err := sealNew("sealed-with-new")
	if err != nil {
		t.Fatal(err)
	}
	// crash: commit never ran; the daemon starts again
	resetFallback()
	if err := Init(key); err != nil {
		t.Fatal(err)
	}
	for sealed, want := range map[string]string{old: "sealed-with-old", reencrypted: "sealed-with-new"} {
		if got, err := Open(sealed); err != nil || got != want {
			t.Fatalf("Open after the interrupted rotation: %q, %v (want %q)", got, err, want)
		}
	}
}

// A completed rotation leaves only the new key.
func TestRotationCommit(t *testing.T) {
	resetFallback()
	key := filepath.Join(t.TempDir(), "secrets.key")
	if err := Init(key); err != nil {
		t.Fatal(err)
	}
	_, sealNew, commit, err := PrepareRotation(key)
	if err != nil {
		t.Fatal(err)
	}
	sealed, _ := sealNew("v")
	if err := commit(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(key + ".new"); !os.IsNotExist(err) {
		t.Fatalf("%s.new left behind: %v", key, err)
	}
	resetFallback()
	if err := Init(key); err != nil {
		t.Fatal(err)
	}
	if got, err := OpenActive(sealed); err != nil || got != "v" {
		t.Fatalf("OpenActive after commit: %q, %v", got, err)
	}
}
