// Package fsutil writes files that must survive a power loss.
package fsutil

import (
	"fmt"
	"os"
	"path/filepath"
)

// WriteFileDurable writes data to path so that after a crash path holds
// either its previous content or data, never an empty or partial file: a
// temporary file in the same directory is written and synced, renamed over
// path, and the directory is synced. A plain os.WriteFile of a new file can
// leave it empty after a power loss (the directory entry reaches the disk
// before the data).
func WriteFileDurable(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	fail := func(err error) error {
		tmp.Close()
		os.Remove(name)
		return fmt.Errorf("writing %s: %w", path, err)
	}
	if err := tmp.Chmod(perm); err != nil {
		return fail(err)
	}
	if _, err := tmp.Write(data); err != nil {
		return fail(err)
	}
	if err := tmp.Sync(); err != nil {
		return fail(err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(name)
		return fmt.Errorf("writing %s: %w", path, err)
	}
	if err := os.Rename(name, path); err != nil {
		os.Remove(name)
		return fmt.Errorf("writing %s: %w", path, err)
	}
	return SyncDir(dir)
}

// RenameDurable renames oldpath to newpath and syncs the directory.
func RenameDurable(oldpath, newpath string) error {
	if err := os.Rename(oldpath, newpath); err != nil {
		return err
	}
	return SyncDir(filepath.Dir(newpath))
}

// SyncDir makes the entries of dir (creations, renames) durable.
func SyncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	if err := d.Sync(); err != nil {
		return fmt.Errorf("syncing %s: %w", dir, err)
	}
	return nil
}
