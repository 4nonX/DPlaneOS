package secrets

import (
	"path/filepath"
	"testing"
)

// An HA pair moving to one shared key: node B switches to node A's key and
// keeps its own old key as fallback. Values B sealed earlier stay readable, and
// new values are sealed only under the shared key.
func TestFallbackKey(t *testing.T) {
	dir := t.TempDir()
	keyA, keyB := filepath.Join(dir, "a.key"), filepath.Join(dir, "b.key")

	if err := Init(keyB); err != nil { // node B's own key
		t.Fatal(err)
	}
	sealedByB, err := Seal("git-token")
	if err != nil {
		t.Fatal(err)
	}
	if err := Init(keyA); err != nil { // the shared key (created here)
		t.Fatal(err)
	}
	if _, err := OpenActive(sealedByB); err == nil {
		t.Fatal("a value sealed under B must not open with A")
	}
	if HasFallback() {
		t.Fatal("no fallback loaded yet")
	}
	if _, err := Open(sealedByB); err == nil {
		t.Fatal("without fallback, Open must fail")
	}

	if err := InitFallback(keyB); err != nil {
		t.Fatal(err)
	}
	if got, err := Open(sealedByB); err != nil || got != "git-token" {
		t.Fatalf("Open via fallback: %q, %v", got, err)
	}
	if got, err := OpenFallback(sealedByB); err != nil || got != "git-token" {
		t.Fatalf("OpenFallback: %q, %v", got, err)
	}

	resealed, err := Seal("git-token")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := OpenActive(resealed); err != nil {
		t.Fatalf("new values must be sealed under the active key: %v", err)
	}
	if _, err := OpenFallback(resealed); err == nil {
		t.Fatal("new values must not be sealed under the fallback key")
	}

	if err := InitFallback(filepath.Join(dir, "missing.key")); err == nil {
		t.Fatal("a missing fallback key must be an error")
	}
}
