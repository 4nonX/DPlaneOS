package zfs

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// isValidPoolName checks pool name against strict whitelist.
// ZFS pool names: start with letter, alphanumeric + hyphens/underscores/dots only.
var poolNameRegex = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9_\-\.]{0,254}$`)

func isValidPoolName(name string) bool {
	return poolNameRegex.MatchString(name)
}

// The pool monitor watches the health of every imported pool, also those
// imported after the daemon started (one heartbeat per pool found at start
// watched only those).
//
// Health comes from `zpool list -o health`. Before, the word UNAVAIL anywhere
// in `zpool status` counted as "pool SUSPENDED/UNAVAIL, I/O halted" and
// stopped Docker: a mirror with one missing disk (pool DEGRADED, fully
// working) lists that disk as UNAVAIL. The monitor also wrote a file to every
// pool's root every 30 seconds as an I/O test, which kept the disks from
// spinning down, changed every snapshot (and so every replication) and could
// hang on a stalled pool; the pool state covers a suspended pool.

var (
	alertMu       sync.RWMutex
	alertDispatch func(level, event, pool, msg string)
)

// SetAlertDispatch wires the alert function (handlers.DispatchAlert).
func SetAlertDispatch(fn func(level, event, pool, msg string)) {
	alertMu.Lock()
	alertDispatch = fn
	alertMu.Unlock()
}

func dispatch(level, event, pool, msg string) {
	alertMu.RLock()
	fn := alertDispatch
	alertMu.RUnlock()
	if fn != nil {
		fn(level, event, pool, msg)
	}
}

// HealthLevel is the alert level of a pool health state ("" = healthy).
func HealthLevel(health string) string {
	switch strings.ToUpper(strings.TrimSpace(health)) {
	case "ONLINE":
		return ""
	case "DEGRADED":
		return "warning"
	case "FAULTED", "UNAVAIL", "SUSPENDED", "REMOVED":
		return "critical"
	default:
		return "warning"
	}
}

// PoolMonitor checks pool health periodically.
type PoolMonitor struct {
	// StopDockerOnFailure stops Docker when a pool is FAULTED, UNAVAIL or
	// SUSPENDED: containers would write into the empty mountpoint folders on
	// the root filesystem.
	StopDockerOnFailure bool

	// listHealth returns pool name → health; replaced in tests.
	listHealth func(ctx context.Context) (map[string]string, error)
	stopDocker func() error

	mu            sync.Mutex
	last          map[string]string // pool → last health seen
	dockerStopped bool
	zpoolStuck    bool
}

// NewPoolMonitor returns a monitor using the zpool command.
func NewPoolMonitor() *PoolMonitor {
	return &PoolMonitor{
		listHealth: zpoolHealth,
		stopDocker: func() error {
			out, err := exec.Command("systemctl", "stop", "docker.service", "docker.socket").CombinedOutput()
			if err != nil {
				return fmt.Errorf("%v: %s", err, strings.TrimSpace(string(out)))
			}
			return nil
		},
		last: map[string]string{},
	}
}

func zpoolHealth(ctx context.Context) (map[string]string, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "zpool", "list", "-H", "-o", "name,health").Output()
	if ctx.Err() == context.DeadlineExceeded {
		return nil, fmt.Errorf("zpool list did not answer within 30 s")
	}
	if err != nil {
		return nil, err
	}
	res := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if f := strings.Fields(line); len(f) == 2 && isValidPoolName(f[0]) {
			res[f[0]] = f[1]
		}
	}
	return res, nil
}

// Start checks now and then every interval until ctx ends.
func (m *PoolMonitor) Start(ctx context.Context, interval time.Duration) {
	removeHeartbeatFiles()
	go func() {
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			m.Check(ctx)
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
		}
	}()
}

// Check runs one health check and alerts on changes.
func (m *PoolMonitor) Check(ctx context.Context) {
	health, err := m.listHealth(ctx)
	m.mu.Lock()
	defer m.mu.Unlock()
	if err != nil {
		if !m.zpoolStuck {
			m.zpoolStuck = true
			log.Printf("CRITICAL: pool health unknown: %v", err)
			dispatch("critical", "pool.critical", "zpool",
				"The pool health cannot be read ("+err.Error()+"). A pool may be hung.")
		}
		return
	}
	m.zpoolStuck = false
	pools := make([]string, 0, len(health))
	for p := range health {
		pools = append(pools, p)
	}
	sort.Strings(pools)
	anyCritical := false
	for _, p := range pools {
		h := health[p]
		prev, seen := m.last[p]
		m.last[p] = h
		level := HealthLevel(h)
		if level == "critical" {
			anyCritical = true
		}
		if seen && prev == h {
			continue
		}
		switch {
		case level == "critical":
			dispatch("critical", "pool.critical", p,
				fmt.Sprintf("Pool '%s' is %s: its data is not accessible. Check its disks now (zpool status %s).", p, h, p))
		case level == "warning":
			dispatch("warning", "pool.degraded", p,
				fmt.Sprintf("Pool '%s' is %s: a disk has failed or is missing, the pool still works without full redundancy. Replace the disk soon (zpool status %s).", p, h, p))
		case seen && HealthLevel(prev) != "":
			dispatch("info", "pool.recovered", p, fmt.Sprintf("Pool '%s' is %s again.", p, h))
		}
	}
	for p := range m.last { // exported or destroyed
		if _, ok := health[p]; !ok {
			delete(m.last, p)
		}
	}
	if anyCritical && m.StopDockerOnFailure && !m.dockerStopped {
		m.dockerStopped = true
		log.Printf("CRITICAL: a pool failed - stopping Docker so containers do not write to the root filesystem")
		if err := m.stopDocker(); err != nil {
			log.Printf("ERROR: stopping Docker: %v", err)
		} else {
			dispatch("critical", "docker.stopped", "docker",
				"Docker was stopped because a pool failed; start it again when the pool is healthy.")
		}
	}
	if !anyCritical {
		m.dockerStopped = false
	}
}

// removeHeartbeatFiles deletes the test files earlier versions wrote.
func removeHeartbeatFiles() {
	pools, err := DiscoverPools()
	if err != nil {
		return
	}
	for _, p := range pools {
		os.Remove(filepath.Join(p.MountPoint, ".dplaneos_heartbeat"))
	}
}

type PoolInfo struct {
	Name       string
	MountPoint string
}

func DiscoverPools() ([]PoolInfo, error) {
	cmd := exec.Command("zpool", "list", "-H", "-o", "name")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("failed to list pools: %w", err)
	}
	if stderrStr := strings.TrimSpace(stderr.String()); stderrStr != "" {
		log.Printf("ZFS pool discovery stderr: %s", stderrStr)
	}

	lines := strings.Split(strings.TrimSpace(stdout.String()), "\n")
	pools := make([]PoolInfo, 0, len(lines))

	for _, poolName := range lines {
		poolName = strings.TrimSpace(poolName)
		if poolName == "" {
			continue
		}

		// Validate pool name before using in exec.Command - prevents injection
		if !isValidPoolName(poolName) {
			log.Printf("ZFS discovery: skipping invalid pool name: %q", poolName)
			continue
		}

		cmd := exec.Command("zfs", "get", "-H", "-o", "value", "mountpoint", poolName)
		var mountStdout bytes.Buffer
		cmd.Stdout = &mountStdout
		if err := cmd.Run(); err != nil {
			continue
		}

		mountPoint := strings.TrimSpace(mountStdout.String())
		if mountPoint == "-" || mountPoint == "none" || mountPoint == "legacy" {
			continue
		}

		pools = append(pools, PoolInfo{
			Name:       poolName,
			MountPoint: mountPoint,
		})
	}

	return pools, nil
}
