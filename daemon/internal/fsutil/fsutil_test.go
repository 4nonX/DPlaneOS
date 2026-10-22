package fsutil

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWriteFileDurable(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "k")
	if err := WriteFileDurable(p, []byte("one"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := WriteFileDurable(p, []byte("two"), 0o600); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(p)
	if err != nil || string(b) != "two" {
		t.Fatalf("content %q, %v", b, err)
	}
	st, _ := os.Stat(p)
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", st.Mode().Perm())
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatalf("temporary files left: %v", entries)
	}
	if err := WriteFileDurable(filepath.Join(dir, "missing", "k"), nil, 0o600); err == nil {
		t.Fatal("a missing directory must fail")
	}
}
