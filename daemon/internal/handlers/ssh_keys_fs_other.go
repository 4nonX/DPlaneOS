//go:build !linux

package handlers

import (
	"fmt"
	"os"
	"path/filepath"
)

// Non-Linux builds (development only): the same rules with Lstat checks.
// The daemon itself only runs on Linux, where ssh_keys_fs_linux.go applies.

func userSSHDir(home string, create bool) (string, error) {
	dir := filepath.Join(home, ".ssh")
	fi, err := os.Lstat(dir)
	if os.IsNotExist(err) && create {
		return dir, os.Mkdir(dir, 0700)
	}
	if err != nil {
		return dir, err
	}
	if fi.Mode()&os.ModeSymlink != 0 || !fi.IsDir() {
		return dir, fmt.Errorf("refusing: %s is a symlink or not a directory", dir)
	}
	return dir, nil
}

func writeUserSSHFile(home string, uid, gid int, name string, content []byte) error {
	dir, err := userSSHDir(home, true)
	if err != nil {
		return err
	}
	tmp := filepath.Join(dir, "."+name+".dplaneos")
	if err := os.WriteFile(tmp, content, 0600); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(dir, name))
}

func readUserSSHFile(home, name string) ([]byte, error) {
	dir, err := userSSHDir(home, false)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	p := filepath.Join(dir, name)
	fi, err := os.Lstat(p)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("refusing: %s is not a regular file", name)
	}
	return os.ReadFile(p)
}
