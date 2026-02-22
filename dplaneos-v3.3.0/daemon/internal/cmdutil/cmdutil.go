package cmdutil

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// Default timeouts for different operation classes
const (
	TimeoutFast   = 10 * time.Second  // ls, stat, status checks
	TimeoutMedium = 60 * time.Second  // snapshot, mount, config reload
	TimeoutSlow   = 5 * time.Minute   // scrub, resilver, large recursive ops
	TimeoutZFS    = 2 * time.Minute   // zpool/zfs commands (can hang on bad disks)
)

// Run executes a command with the given timeout, returns (output, error).
// If the command exceeds the timeout, it is killed and an error is returned.
// This prevents the Go daemon from hanging when hardware is unresponsive.
func Run(timeout time.Duration, name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, name, args...)
	output, err := cmd.CombinedOutput()

	if ctx.Err() == context.DeadlineExceeded {
		return output, fmt.Errorf("command timed out after %v: %s %v", timeout, name, args)
	}

	return output, err
}

// RunFast executes a command with TimeoutFast (10s).
// Use for: status checks, list operations, stat, hdparm -C, getfacl
func RunFast(name string, args ...string) ([]byte, error) {
	return Run(TimeoutFast, name, args...)
}

// RunMedium executes a command with TimeoutMedium (60s).
// Use for: snapshot creation, mount/unmount, config reload, setfacl, ufw
func RunMedium(name string, args ...string) ([]byte, error) {
	return Run(TimeoutMedium, name, args...)
}

// RunZFS executes a ZFS/zpool command with TimeoutZFS (2min).
// Use for: zfs list, zpool status, zfs get — commands that may hang on bad disks.
// For long-running operations (scrub, send/receive), use RunSlow.
func RunZFS(name string, args ...string) ([]byte, error) {
	return Run(TimeoutZFS, name, args...)
}

// RunSlow executes a command with TimeoutSlow (5min).
// Use for: recursive chown/chmod/setfacl, zfs send, rsync, large operations
func RunSlow(name string, args ...string) ([]byte, error) {
	return Run(TimeoutSlow, name, args...)
}

// RunNoTimeout executes a command without a timeout (same as exec.Command).
// Use ONLY for commands that must complete regardless of time (e.g., mv for trash).
func RunNoTimeout(name string, args ...string) ([]byte, error) {
	cmd := exec.Command(name, args...)
	return cmd.CombinedOutput()
}

// RunWithStdin executes a command with timeout and pipes stdinData to its stdin.
// Use for: zfs load-key, zfs create (encryption), zfs change-key — commands that
// require passphrase input via stdin.
func RunWithStdin(timeout time.Duration, stdinData string, name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdin = strings.NewReader(stdinData)
	output, err := cmd.CombinedOutput()

	if ctx.Err() == context.DeadlineExceeded {
		return output, fmt.Errorf("command timed out after %v: %s %v", timeout, name, args)
	}

	return output, err
}

