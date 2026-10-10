//go:build linux

package handlers

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// The daemon runs as root and writes into a directory the user owns: every
// step goes through a directory handle opened with O_NOFOLLOW, so a user who
// replaces ~/.ssh (or authorized_keys) with a symlink to /root/.ssh cannot
// make the daemon write there.

// openUserSSHDir opens (creating if needed) the user's ~/.ssh without
// following symlinks.
func openUserSSHDir(home string, uid, gid int, create bool) (int, error) {
	homeFd, err := unix.Open(home, unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return -1, fmt.Errorf("open home %s: %w", home, err)
	}
	defer unix.Close(homeFd)
	if create {
		if err := unix.Mkdirat(homeFd, ".ssh", 0700); err == nil {
			if err := unix.Fchownat(homeFd, ".ssh", uid, gid, unix.AT_SYMLINK_NOFOLLOW); err != nil {
				return -1, fmt.Errorf("chown .ssh: %w", err)
			}
		} else if !errors.Is(err, unix.EEXIST) {
			return -1, fmt.Errorf("create .ssh: %w", err)
		}
	}
	fd, err := unix.Openat(homeFd, ".ssh", unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		if errors.Is(err, unix.ELOOP) || errors.Is(err, unix.ENOTDIR) {
			return -1, fmt.Errorf("refusing: %s is a symlink or not a directory", filepath.Join(home, ".ssh"))
		}
		return -1, err
	}
	return fd, nil
}

// writeUserSSHFile replaces ~/.ssh/<name> atomically, owned by the user.
func writeUserSSHFile(home string, uid, gid int, name string, content []byte) error {
	dirFd, err := openUserSSHDir(home, uid, gid, true)
	if err != nil {
		return err
	}
	defer unix.Close(dirFd)

	tmp := fmt.Sprintf(".%s.dplaneos-%d", name, os.Getpid())
	_ = unix.Unlinkat(dirFd, tmp, 0)
	fd, err := unix.Openat(dirFd, tmp, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0600)
	if err != nil {
		return fmt.Errorf("create %s: %w", tmp, err)
	}
	f := os.NewFile(uintptr(fd), tmp)
	_, werr := f.Write(content)
	if werr == nil {
		werr = f.Chown(uid, gid)
	}
	if werr == nil {
		werr = f.Sync()
	}
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		_ = unix.Unlinkat(dirFd, tmp, 0)
		return fmt.Errorf("write %s: %w", name, werr)
	}
	// rename replaces a symlink at the destination itself, never its target.
	if err := unix.Renameat(dirFd, tmp, dirFd, name); err != nil {
		_ = unix.Unlinkat(dirFd, tmp, 0)
		return fmt.Errorf("replace %s: %w", name, err)
	}
	return nil
}

// readUserSSHFile reads ~/.ssh/<name> without following symlinks; a missing
// file reads as empty.
func readUserSSHFile(home, name string) ([]byte, error) {
	dirFd, err := openUserSSHDir(home, -1, -1, false)
	if err != nil {
		if errors.Is(err, unix.ENOENT) {
			return nil, nil
		}
		return nil, err
	}
	defer unix.Close(dirFd)
	fd, err := unix.Openat(dirFd, name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		if errors.Is(err, unix.ENOENT) {
			return nil, nil
		}
		return nil, fmt.Errorf("open %s: %w", name, err)
	}
	f := os.NewFile(uintptr(fd), name)
	defer f.Close()
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil || st.Mode&unix.S_IFMT != unix.S_IFREG {
		return nil, fmt.Errorf("refusing: %s is not a regular file", name)
	}
	buf := make([]byte, 0, st.Size)
	tmp := make([]byte, 32*1024)
	for {
		n, err := f.Read(tmp)
		buf = append(buf, tmp[:n]...)
		if err != nil {
			break
		}
	}
	return buf, nil
}
