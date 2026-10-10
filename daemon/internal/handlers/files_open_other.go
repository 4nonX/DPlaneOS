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
