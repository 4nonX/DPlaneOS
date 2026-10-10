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

// privateToDaemon: owned by the daemon's user (root in production) and not
// writable by group or others.
func privateToDaemon(fi os.FileInfo) bool {
	st, ok := fi.Sys().(*syscall.Stat_t)
	return ok && int(st.Uid) == os.Geteuid() && fi.Mode().Perm()&0o022 == 0
}

// deviceOf returns the device a file lives on.
func deviceOf(fi os.FileInfo) (uint64, bool) {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, false
	}
	return uint64(st.Dev), true //nolint:unconvert // Dev is uint64 on amd64, not on every arch
}
