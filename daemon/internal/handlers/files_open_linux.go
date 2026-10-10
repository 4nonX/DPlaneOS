//go:build linux

package handlers

import (
	"os"
	"syscall"
)

// openNoFollow opens path without following it if it is a symlink.
func openNoFollow(path string, flag int, perm os.FileMode) (*os.File, error) {
	return os.OpenFile(path, flag|syscall.O_NOFOLLOW, perm)
}
