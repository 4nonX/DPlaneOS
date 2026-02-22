package zfs

import (
	"bytes"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
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

type PoolHeartbeat struct {
	poolName      string
	mountPoint    string
	checkInterval time.Duration
	heartbeatFile string
	mu            sync.RWMutex
	lastSuccess   time.Time
	lastError     error
	stopChan      chan struct{}
	// Optional callback for critical errors (e.g., Telegram alerts)
	onCriticalError func(poolName string, err error, details map[string]string)

	// When true: run `systemctl stop docker` if pool goes SUSPENDED/UNAVAIL/read-only.
	// This prevents containers from writing to the bare mountpoint directory on the
	// root filesystem while ZFS is offline — the same race that the boot gate prevents.
	StopDockerOnFailure bool

	// Track whether we already stopped docker in this failure window (avoid repeating)
	dockerStopped bool
}

func NewPoolHeartbeat(poolName string, mountPoint string, checkInterval time.Duration) *PoolHeartbeat {
	if checkInterval <= 0 {
		checkInterval = 30 * time.Second
	}

	return &PoolHeartbeat{
		poolName:      poolName,
		mountPoint:    mountPoint,
		checkInterval: checkInterval,
		heartbeatFile: filepath.Join(mountPoint, ".dplaneos_heartbeat"),
		stopChan:      make(chan struct{}),
	}
}

func (ph *PoolHeartbeat) Start() {
	go func() {
		ticker := time.NewTicker(ph.checkInterval)
		defer ticker.Stop()

		ph.performCheck()

		for {
			select {
			case <-ticker.C:
				ph.performCheck()
			case <-ph.stopChan:
				return
			}
		}
	}()
}

func (ph *PoolHeartbeat) Stop() {
	close(ph.stopChan)
}

func (ph *PoolHeartbeat) performCheck() {
	ph.mu.Lock()
	defer ph.mu.Unlock()

	// Check pool status
	cmd := exec.Command("zpool", "status", ph.poolName)
	output, err := cmd.CombinedOutput()
	if err != nil {
		ph.lastError = fmt.Errorf("pool status failed: %w", err)
		log.Printf("ERROR: ZFS pool %s status check failed", ph.poolName)
		return
	}

	status := string(output)
	if strings.Contains(status, "SUSPENDED") || strings.Contains(status, "UNAVAIL") {
		newErr := fmt.Errorf("pool is SUSPENDED or UNAVAIL")
		
		// Only alert if this is a NEW error (state change)
		if ph.lastError == nil || ph.lastError.Error() != newErr.Error() {
			log.Printf("CRITICAL: ZFS pool %s is SUSPENDED/UNAVAIL!", ph.poolName)
			ph.triggerAlert(newErr, map[string]string{
				"Pool":   ph.poolName,
				"Status": "SUSPENDED/UNAVAIL",
				"Action": "Check pool status immediately",
			})
			ph.maybeStopDocker("SUSPENDED/UNAVAIL")
		}
		
		ph.lastError = newErr
		return
	}

	// Active I/O test
	testData := []byte(fmt.Sprintf("heartbeat:%d\n", time.Now().Unix()))
	err = os.WriteFile(ph.heartbeatFile, testData, 0644)
	if err != nil {
		newErr := fmt.Errorf("write failed: %w", err)
		
		// Only alert on NEW write errors
		if ph.lastError == nil || !strings.Contains(ph.lastError.Error(), "write failed") {
			log.Printf("CRITICAL: ZFS pool %s cannot write", ph.poolName)
			ph.triggerAlert(newErr, map[string]string{
				"Pool":   ph.poolName,
				"Error":  "Write failed",
				"Action": "Pool may be read-only or suspended",
			})
			ph.maybeStopDocker("read-only/write-failed")
		}
		
		ph.lastError = newErr
		return
	}

	readData, err := os.ReadFile(ph.heartbeatFile)
	if err != nil {
		ph.lastError = fmt.Errorf("read failed: %w", err)
		log.Printf("ERROR: ZFS pool %s cannot read", ph.poolName)
		return
	}

	if string(readData) != string(testData) {
		ph.lastError = fmt.Errorf("data mismatch")
		log.Printf("ERROR: ZFS pool %s data corruption detected", ph.poolName)
		return
	}

	ph.lastSuccess = time.Now()
	ph.lastError = nil
	ph.dockerStopped = false // pool recovered — reset guard
}

func (ph *PoolHeartbeat) GetStatus() (time.Time, error) {
	ph.mu.RLock()
	defer ph.mu.RUnlock()
	return ph.lastSuccess, ph.lastError
}

func (ph *PoolHeartbeat) IsHealthy() bool {
	ph.mu.RLock()
	defer ph.mu.RUnlock()

	if ph.lastError != nil {
		return false
	}

	if ph.lastSuccess.IsZero() {
		return false
	}

	maxAge := ph.checkInterval * 2
	if time.Since(ph.lastSuccess) > maxAge {
		return false
	}

	return true
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

		// Validate pool name before using in exec.Command — prevents injection
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

// SetErrorCallback sets an optional callback for critical errors
func (ph *PoolHeartbeat) SetErrorCallback(callback func(poolName string, err error, details map[string]string)) {
	ph.mu.Lock()
	defer ph.mu.Unlock()
	ph.onCriticalError = callback
}

// maybeStopDocker stops the Docker service if StopDockerOnFailure is enabled.
// Called when a ZFS pool goes SUSPENDED, UNAVAIL, or read-only during runtime.
// This prevents containers from writing to empty mountpoint directories on the
// root filesystem while ZFS is offline.
//
// Guard: only fires once per failure window (resets on pool recovery).
func (ph *PoolHeartbeat) maybeStopDocker(reason string) {
	if !ph.StopDockerOnFailure || ph.dockerStopped {
		return
	}
	ph.dockerStopped = true
	log.Printf("CRITICAL: ZFS pool %s failure (%s) — stopping Docker to prevent root-FS data loss", ph.poolName, reason)
	cmd := exec.Command("systemctl", "stop", "docker")
	if out, err := cmd.CombinedOutput(); err != nil {
		log.Printf("ERROR: Failed to stop Docker after pool failure: %v — %s", err, strings.TrimSpace(string(out)))
	} else {
		log.Printf("WARNING: Docker stopped due to pool failure. Restart when pool is healthy: systemctl start docker")
	}
}

// triggerAlert calls the error callback if set
func (ph *PoolHeartbeat) triggerAlert(err error, details map[string]string) {
	if ph.onCriticalError != nil {
		ph.onCriticalError(ph.poolName, err, details)
	}
}
