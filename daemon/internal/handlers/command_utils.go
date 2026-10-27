package handlers

import (
	"time"

	"dplaned/internal/cmdutil"
)

// Default timeouts for different command categories
const (
	TimeoutFast   = 5 * time.Second   // zfs list, docker ps
	TimeoutMedium = 30 * time.Second  // zfs snapshot, docker stop
	TimeoutSlow   = 120 * time.Second // zfs scrub, docker pull
	TimeoutLong   = 0                 // zfs send (no timeout, runs async)
)

// executeCommand runs a command with TimeoutMedium (30s).
func executeCommand(name string, args []string) (string, error) {
	out, err := cmdutil.Run(TimeoutMedium, name, args...)
	return string(out), err
}

// executeCommandWithTimeout runs a command with a deadline.
// If the command exceeds the timeout, it's killed and an error is returned.
// A timeout of 0 means no timeout (for long-running operations like zfs send).
func executeCommandWithTimeout(timeout time.Duration, name string, args []string) (string, error) {
	out, err := cmdutil.Run(timeout, name, args...)
	return string(out), err
}

// executeBackgroundCommand runs a command at idle I/O priority (ionice -c 3)
// Used for scrubbing, indexing, thumbnail generation - anything that shouldn't
// starve interactive workloads.
func executeBackgroundCommand(path string, args []string) (string, error) {
	// Wrap in ionice -c 3 (idle class: only gets I/O when nothing else needs it)
	ioniceArgs := []string{"-c", "3", path}
	ioniceArgs = append(ioniceArgs, args...)
	return executeCommand("ionice", ioniceArgs)
}
