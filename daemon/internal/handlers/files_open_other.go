//go:build !linux

package handlers

import (
	"fmt"
	"os"
)

// openNoFollow (development builds): refuses an existing symlink.
func openNoFollow(path string, flag int, perm os.FileMode) (*os.File, error) {
	if fi, err := os.Lstat(path); err == nil && fi.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("%s is a symbolic link", path)
	}
	return os.OpenFile(path, flag, perm)
}

// privateToDaemon (development builds): ownership is not checked.
func privateToDaemon(os.FileInfo) bool { return true }

// deviceOf (development builds): device IDs are unknown; the trash goes to
// the file root.
func deviceOf(os.FileInfo) (uint64, bool) { return 0, false }
