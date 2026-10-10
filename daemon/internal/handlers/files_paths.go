package handlers

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"dplaned/internal/security"
)

// File manager paths. The daemon runs as root and the files live on shares
// whose users can create symlinks, so every path is checked after resolving
// symlinks, against the real file roots:
//
//   - resolveExisting: the path must exist; it is resolved completely (for
//     reading, listing, downloading, chown/chmod: the target).
//   - resolveEntry: the last component is kept as is (for creating,
//     deleting, renaming, moving: a symlink itself is acted on, never its
//     target); everything above it is resolved.
//
// The roots are pool and removable-media mounts only: not /home, /tmp or
// the daemon's own data directory (the secrets key, ssh-keys.json, ...).
// Besides the fixed mount directories, every mounted ZFS filesystem outside
// the system directories is a root: a pool created or imported without a
// mountpoint is mounted at /<pool>.
var fileRoots = []string{"/mnt", "/tank", "/data", "/media"}

// mountsFile lists the mounted filesystems (replaced in tests).
var mountsFile = "/proc/self/mounts"

var (
	zfsRootsMu   sync.Mutex
	zfsRootsAt   time.Time
	zfsRootsList []string
)

// zfsMountRoots returns the mountpoints of ZFS filesystems outside the
// system directories (cached briefly: path checks run per request).
func zfsMountRoots() []string {
	zfsRootsMu.Lock()
	defer zfsRootsMu.Unlock()
	if time.Since(zfsRootsAt) < 3*time.Second {
		return zfsRootsList
	}
	var out []string
	if data, err := os.ReadFile(mountsFile); err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			f := strings.Fields(line)
			if len(f) < 3 || f[2] != "zfs" {
				continue
			}
			mp := filepath.Clean(strings.ReplaceAll(f[1], `\040`, " "))
			parts := strings.Split(strings.Trim(filepath.ToSlash(mp), "/"), "/")
			if mp == "/" || mp == "." || parts[0] == "" || security.SystemDirs[parts[0]] {
				continue
			}
			out = append(out, mp)
		}
	}
	zfsRootsList, zfsRootsAt = out, time.Now()
	return out
}

func realRoots() []string {
	var out []string
	for _, r := range fileRoots {
		if rr, err := filepath.EvalSymlinks(r); err == nil {
			out = append(out, rr)
		}
	}
	return append(out, zfsMountRoots()...)
}

// FileRoots lists the directories the file manager and shares may use.
func FileRoots() []string { return realRoots() }

func underRoot(real string) bool {
	for _, root := range realRoots() {
		rel, err := filepath.Rel(root, real)
		if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

func cleanAbs(p string) (string, error) {
	if p == "" || strings.ContainsAny(p, "\x00\n\r") {
		return "", fmt.Errorf("invalid path")
	}
	c := filepath.Clean(p)
	if !filepath.IsAbs(c) {
		return "", fmt.Errorf("path must be absolute")
	}
	return c, nil
}

// resolveExisting returns the real path of an existing file under a root.
func resolveExisting(p string) (string, error) {
	c, err := cleanAbs(p)
	if err != nil {
		return "", err
	}
	real, err := filepath.EvalSymlinks(c)
	if err != nil {
		return "", fmt.Errorf("not found")
	}
	if !underRoot(real) {
		return "", fmt.Errorf("path not allowed")
	}
	return real, nil
}

// resolveEntry returns <real parent>/<name> for a path whose parent
// directories (existing or not yet) lie under a root.
func resolveEntry(p string) (string, error) {
	c, err := cleanAbs(p)
	if err != nil {
		return "", err
	}
	name := filepath.Base(c)
	if name == "/" || name == "." || name == ".." || name == string(filepath.Separator) {
		return "", fmt.Errorf("invalid path")
	}
	// Resolve the nearest existing ancestor, keep the rest.
	dir := filepath.Dir(c)
	var rest []string
	for {
		if _, err := os.Lstat(dir); err == nil {
			break
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("path not allowed")
		}
		rest = append([]string{filepath.Base(dir)}, rest...)
		dir = parent
	}
	realDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return "", fmt.Errorf("path not allowed")
	}
	out := filepath.Join(append(append([]string{realDir}, rest...), name)...)
	if !underRoot(out) || isFileRoot(out) {
		return "", fmt.Errorf("path not allowed")
	}
	return out, nil
}

func isFileRoot(real string) bool {
	for _, r := range realRoots() {
		if real == r {
			return true
		}
	}
	return false
}

// parseFileMode parses an octal mode ("644", "0755", "1777") and refuses
// setuid/setgid: files are written by root, and a setuid root executable on
// a share is a root shell for anyone who can run it. (An octal value cast to
// os.FileMode keeps the raw 04000/02000 bits, which the system call honours.)
func parseFileMode(s string) (os.FileMode, error) {
	v, err := strconv.ParseUint(s, 8, 32)
	if err != nil || v > 0o1777 {
		return 0, fmt.Errorf("invalid mode %q", s)
	}
	if v&0o6000 != 0 {
		return 0, fmt.Errorf("setuid/setgid modes are not allowed")
	}
	return os.FileMode(v), nil
}

// resolveDestination: an existing directory (copy/move into it) resolved
// completely, otherwise the new entry.
func resolveDestination(p string) (string, error) {
	if real, err := resolveExisting(p); err == nil {
		if fi, err := os.Stat(real); err == nil && fi.IsDir() {
			return real, nil
		}
	}
	entry, err := resolveEntry(p)
	if err != nil {
		return "", err
	}
	// An existing symlink as destination would be followed by cp/rename
	// into a directory: refused (resolveExisting above already refused
	// links whose target is outside the roots).
	if fi, err := os.Lstat(entry); err == nil && fi.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("destination is a symbolic link")
	}
	return entry, nil
}

// writeFileNoFollow replaces path with data; path itself is never followed
// if it is (or becomes) a symlink.
func writeFileNoFollow(path string, data []byte, perm os.FileMode) error {
	f, err := openNoFollow(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, perm)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// sharePathOK: an SMB share or NFS export must be an existing directory
// within the file roots, given by its real path (no symlinks): a share of
// / or /etc, or an export with no_root_squash, hands out the system.
func sharePathOK(p string) error {
	real, err := resolveExisting(p)
	if err != nil {
		return fmt.Errorf("share path must be a folder on a pool or media mount (%v)", err)
	}
	if fi, err := os.Stat(real); err != nil || !fi.IsDir() {
		return fmt.Errorf("share path must be a folder")
	}
	if real != filepath.Clean(p) {
		return fmt.Errorf("share path contains a symbolic link; use the real path %s", real)
	}
	return nil
}
