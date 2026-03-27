package cmdutil

import (
	"context"
	"fmt"
	"log"
	"math/rand"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"dplaned/internal/security"
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
// A timeout of 0 means no timeout (use with caution).
func Run(timeout time.Duration, name string, args ...string) ([]byte, error) {
	return runInternal(context.Background(), timeout, name, args...)
}

// runInternal handles the core command execution with security validation.
func runInternal(ctx context.Context, timeout time.Duration, name string, args ...string) ([]byte, error) {
	// 1. Mandatory Security Whitelist Routing (Finding 23)
	if cmd, exists := security.CommandWhitelist[name]; exists {
		if err := security.ValidateCommand(name, args); err != nil {
			return nil, fmt.Errorf("security refusal: %w", err)
		}
		// Rewrite the name to the absolute binary path from the whitelist
		name = cmd.Path
	} else {
		// Safety check for governed binaries
		governed := map[string]bool{"zfs": true, "zpool": true, "systemctl": true, "ipmitool": true, "docker": true}
		if governed[name] {
			log.Printf("SECURITY WARNING: Binary '%s' invoked directly via cmdutil without a whitelist KEY. This bypasses structural validation.", name)
		}
	}

	// 2. Fault Injection (CI only)
	if inject := os.Getenv("DPLANE_FAULT_INJECT"); inject != "" {
		if err := checkForFault(name, args...); err != nil {
			return []byte("SIMULATED FAULT: " + err.Error()), err
		}
	}

	var cancel context.CancelFunc
	if timeout > 0 {
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}

	cmd := exec.CommandContext(ctx, name, args...)
	output, err := cmd.CombinedOutput()

	if ctx.Err() == context.DeadlineExceeded {
		return output, fmt.Errorf("command timed out after %v: %s %v", timeout, name, args)
	}

	return output, err
}

// checkForFault parses DPLANE_FAULT_INJECT environment variable.
// Format: cmd:subcmd=prob;cmd=prob
// Example: zfs:set=1.0;docker=0.5
func checkForFault(name string, args ...string) error {
	rules := strings.Split(os.Getenv("DPLANE_FAULT_INJECT"), ";")
	for _, rule := range rules {
		parts := strings.SplitN(rule, "=", 2)
		if len(parts) != 2 {
			continue
		}
		target := parts[0]
		probStr := parts[1]

		prob, err := strconv.ParseFloat(probStr, 64)
		if err != nil {
			continue
		}

		match := false
		if strings.Contains(target, ":") {
			// Cmd + Subcmd (e.g. zfs:set)
			subparts := strings.SplitN(target, ":", 2)
			if name == subparts[0] && len(args) > 0 && args[0] == subparts[1] {
				match = true
			}
		} else {
			// Binary name only (e.g. zfs)
			if name == target {
				match = true
			}
		}

		if match && rand.Float64() < prob {
			return fmt.Errorf("fault injected for %s (%s)", name, target)
		}
	}
	return nil
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
// Use for: zfs list, zpool status, zfs get - commands that may hang on bad disks.
// For long-running operations (scrub, send/receive), use RunSlow.
func RunZFS(name string, args ...string) ([]byte, error) {
	return Run(TimeoutZFS, name, args...)
}

// RunSlow executes a command with TimeoutSlow (5min).
// Use for: recursive chown/chmod/setfacl, zfs send, rsync, large operations
func RunSlow(name string, args ...string) ([]byte, error) {
	return Run(TimeoutSlow, name, args...)
}

// RunInDir executes a command with the given timeout in a specific working directory.
func RunInDir(timeout time.Duration, dir, name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()

	if ctx.Err() == context.DeadlineExceeded {
		return output, fmt.Errorf("command timed out after %v in %s: %s %v", timeout, dir, name, args)
	}

	return output, err
}

// RunFastInDir executes a command with TimeoutFast (10s) in a specific directory.
func RunFastInDir(dir, name string, args ...string) ([]byte, error) {
	return RunInDir(TimeoutFast, dir, name, args...)
}

// RunMediumInDir executes a command with TimeoutMedium (60s) in a specific directory.
func RunMediumInDir(dir, name string, args ...string) ([]byte, error) {
	return RunInDir(TimeoutMedium, dir, name, args...)
}

// RunInDirWithEnv executes a command with the given timeout, directory, and environment variables.
func RunInDirWithEnv(timeout time.Duration, dir string, env []string, name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	if len(env) > 0 {
		cmd.Env = env
	}
	output, err := cmd.CombinedOutput()

	if ctx.Err() == context.DeadlineExceeded {
		return output, fmt.Errorf("command timed out after %v in %s: %s %v", timeout, dir, name, args)
	}

	return output, err
}

// RunNoTimeout executes a command without a timeout (same as exec.Command).
// Use ONLY for commands that must complete regardless of time (e.g., mv for trash).
func RunNoTimeout(name string, args ...string) ([]byte, error) {
	return Run(0, name, args...)
}

// RunWithStdin executes a command with timeout and pipes stdinData to its stdin.
// Use for: zfs load-key, zfs create (encryption), zfs change-key - commands that
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


