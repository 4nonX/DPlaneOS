package handlers

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"dplaned/internal/configbackup"
)

func TestAutoConfigBackup(t *testing.T) {
	db := testDB(t)
	pool := t.TempDir()
	oldMounts, oldDumper, oldDir := poolMounts, configbackup.Dumper, ConfigDir
	poolMounts = func() map[string]string { return map[string]string{"tank": pool} }
	configbackup.Dumper = func(_ context.Context, _ string, w io.Writer) error { _, err := io.WriteString(w, "DUMP"); return err }
	ConfigDir = t.TempDir()
	t.Cleanup(func() { poolMounts, configbackup.Dumper, ConfigDir = oldMounts, oldDumper, oldDir })

	dir := filepath.Join(pool, autoBackupDirName)
	// Older backups beyond the kept number are pruned, the newest kept.
	os.MkdirAll(dir, 0o700)
	for i := 0; i < autoBackupKeep+3; i++ {
		name := "dplaneos-config-old-" + time.Date(2026, 1, 1+i, 0, 0, 0, 0, time.UTC).Format("20060102-150405") + ".tar.gz"
		os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o600)
	}
	os.WriteFile(filepath.Join(dir, "unrelated.txt"), []byte("x"), 0o600)
	if err := writeAutoBackup(db, ""); err != nil {
		t.Fatal(err)
	}
	list := storedBackups()
	if len(list) != autoBackupKeep {
		t.Fatalf("%d stored backups, want %d", len(list), autoBackupKeep)
	}
	newest := ""
	for _, b := range list {
		if b.Name[len(b.Name)-22:] > newest {
			newest = b.Name[len(b.Name)-22:]
		}
	}
	if newest[:4] != time.Now().UTC().Format("2006") {
		t.Errorf("today's backup was pruned: newest %s", newest)
	}
	if _, err := os.Stat(filepath.Join(dir, "dplaneos-config-old-20260101-000000.tar.gz")); err == nil {
		t.Error("the oldest backup was kept")
	}
	if _, err := os.Stat(filepath.Join(dir, "unrelated.txt")); err != nil {
		t.Error("an unrelated file was removed")
	}
	info, _ := os.Stat(dir)
	if info.Mode().Perm() != 0o700 {
		t.Errorf("backup directory mode %v", info.Mode().Perm())
	}
	if _, ok := storedBackupPath("tank", "../../etc/shadow"); ok {
		t.Error("path traversal accepted")
	}
	if _, ok := storedBackupPath("other", list[0].Name); ok {
		t.Error("unknown pool accepted")
	}
}
