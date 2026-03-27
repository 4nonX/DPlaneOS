package handlers

import (
	"fmt"
	"strings"
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

// executeCommandAsync runs a command in the background and returns immediately.
// The caller gets a channel that receives the result when done.
// Used for long-running operations like zfs send/recv.
type AsyncResult struct {
	Output string
	Error  error
}

func executeCommandAsync(path string, args []string) <-chan AsyncResult {
	ch := make(chan AsyncResult, 1)
	go func() {
		output, err := executeCommand(path, args)
		ch <- AsyncResult{Output: output, Error: err}
	}()
	return ch
}

// Pool capacity helper functions

// getPoolUsagePercent returns the usage percentage of a ZFS pool
func getPoolUsagePercent(poolName string) (float64, error) {
	output, err := executeCommandWithTimeout(TimeoutFast, "zpool", []string{
		"list", "-Hp", "-o", "capacity", poolName,
	})
	if err != nil {
		return 0, err
	}

	var pct float64
	_, err = fmt.Sscanf(strings.TrimSpace(output), "%f", &pct)
	return pct, err
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

// executeBackgroundCommandWithTimeout combines ionice + timeout
func executeBackgroundCommandWithTimeout(timeout time.Duration, path string, args []string) (string, error) {
	ioniceArgs := []string{"-c", "3", path}
	ioniceArgs = append(ioniceArgs, args...)
	return executeCommandWithTimeout(timeout, "ionice", ioniceArgs)
}

