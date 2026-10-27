// Package resource monitors and enforces resource limits (disk, memory, file descriptors).
// Prevents catastrophic failures from resource exhaustion.
package resource

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ResourceStatus tracks current resource usage.
type ResourceStatus struct {
	Timestamp             time.Time
	DiskUsagePercent      int // 0-100
	DiskAvailableGB       float64
	MemoryUsageBytes      uint64
	MemoryLimitBytes      uint64
	MemoryPercent         int // 0-100
	OpenFileCount         int
	FileDescriptorMax     int
	FileDescriptorPercent int
	Status                string // "OK", "DEGRADED", "CRITICAL"
	Warnings              []string
}

// Watcher monitors system resources and triggers callbacks on threshold violations.
type Watcher struct {
	mu               sync.RWMutex
	lastStatus       *ResourceStatus
	checkInterval    time.Duration
	stopChan         chan bool
	diskCheckPaths   []string // paths to monitor (e.g., ["/persist", "/"])
	memoryLimitBytes uint64
	onCritical       func(ResourceStatus) // callback when CRITICAL threshold hit
	onDegraded       func(ResourceStatus) // callback when DEGRADED threshold hit
}

// NewWatcher creates a resource watcher.
func NewWatcher(checkInterval time.Duration, memoryLimitBytes uint64, diskPaths []string) *Watcher {
	if len(diskPaths) == 0 {
		diskPaths = []string{"/persist", "/"}
	}
	w := &Watcher{
		checkInterval:    checkInterval,
		memoryLimitBytes: memoryLimitBytes,
		diskCheckPaths:   diskPaths,
		stopChan:         make(chan bool),
	}
	// Perform initial check so GetStatus() doesn't return UNKNOWN
	status := w.check()
	w.lastStatus = &status
	return w
}

// SetCriticalCallback sets the callback invoked when resource hits CRITICAL threshold.
func (w *Watcher) SetCriticalCallback(fn func(ResourceStatus)) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.onCritical = fn
}

// SetDegradedCallback sets the callback invoked when resource hits DEGRADED threshold.
func (w *Watcher) SetDegradedCallback(fn func(ResourceStatus)) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.onDegraded = fn
}

// Start begins monitoring resources in a background goroutine.
func (w *Watcher) Start() {
	go w.loop()
}

// Stop stops the monitoring loop.
func (w *Watcher) Stop() {
	w.stopChan <- true
}

// GetStatus returns the last known resource status.
func (w *Watcher) GetStatus() ResourceStatus {
	w.mu.RLock()
	defer w.mu.RUnlock()
	if w.lastStatus == nil {
		return ResourceStatus{Status: "UNKNOWN"}
	}
	return *w.lastStatus
}

func (w *Watcher) loop() {
	ticker := time.NewTicker(w.checkInterval)
	defer ticker.Stop()

	var lastStatus ResourceStatus
	for {
		select {
		case <-w.stopChan:
			return
		case <-ticker.C:
			status := w.check()
			w.mu.Lock()
			w.lastStatus = &status
			w.mu.Unlock()

			// Trigger callbacks on state change
			if status.Status == "CRITICAL" && lastStatus.Status != "CRITICAL" {
				if w.onCritical != nil {
					go w.onCritical(status)
				}
			}
			if status.Status == "DEGRADED" && lastStatus.Status != "DEGRADED" && lastStatus.Status != "CRITICAL" {
				if w.onDegraded != nil {
					go w.onDegraded(status)
				}
			}
			lastStatus = status
		}
	}
}

func (w *Watcher) check() ResourceStatus {
	status := ResourceStatus{
		Timestamp: time.Now(),
		Warnings:  []string{},
		Status:    "OK",
	}

	// Check disk usage (monitor primary path)
	if len(w.diskCheckPaths) > 0 {
		diskUsage := w.checkDisk(w.diskCheckPaths[0])
		status.DiskUsagePercent = diskUsage.Percent
		status.DiskAvailableGB = diskUsage.AvailableGB

		if diskUsage.Percent >= DiskCritical {
			status.Status = "CRITICAL"
			status.Warnings = append(status.Warnings, fmt.Sprintf("Disk CRITICAL: %d%% used", diskUsage.Percent))
		} else if diskUsage.Percent >= 95 {
			if status.Status != "CRITICAL" {
				status.Status = "DEGRADED"
			}
			status.Warnings = append(status.Warnings, fmt.Sprintf("Disk DEGRADED: %d%% used", diskUsage.Percent))
		} else if diskUsage.Percent >= 90 {
			status.Warnings = append(status.Warnings, fmt.Sprintf("Disk WARNING: %d%% used", diskUsage.Percent))
		}
	}

	// Check memory usage
	memStatus := w.checkMemory()
	status.MemoryUsageBytes = memStatus.UsageBytes
	status.MemoryLimitBytes = memStatus.LimitBytes
	status.MemoryPercent = memStatus.Percent

	if memStatus.Percent >= MemoryCritical {
		status.Status = "CRITICAL"
		status.Warnings = append(status.Warnings, fmt.Sprintf("Memory CRITICAL: %d%% used", memStatus.Percent))
	} else if memStatus.Percent >= 85 {
		if status.Status != "CRITICAL" {
			status.Status = "DEGRADED"
		}
		status.Warnings = append(status.Warnings, fmt.Sprintf("Memory DEGRADED: %d%% used", memStatus.Percent))
	}

	// Check file descriptors
	fdStatus := w.checkFileDescriptors()
	status.OpenFileCount = fdStatus.Open
	status.FileDescriptorMax = fdStatus.Max
	status.FileDescriptorPercent = fdStatus.Percent

	if fdStatus.Percent >= FileDescriptorsCritical {
		status.Status = "CRITICAL"
		status.Warnings = append(status.Warnings, fmt.Sprintf("File descriptors CRITICAL: %d/%d (%.1f%%)", fdStatus.Open, fdStatus.Max, float64(fdStatus.Percent)))
	} else if fdStatus.Percent >= 80 {
		if status.Status != "CRITICAL" {
			status.Status = "DEGRADED"
		}
		status.Warnings = append(status.Warnings, fmt.Sprintf("File descriptors DEGRADED: %d/%d (%.1f%%)", fdStatus.Open, fdStatus.Max, float64(fdStatus.Percent)))
	}

	return status
}

type diskUsage struct {
	Percent     int
	AvailableGB float64
}

func (w *Watcher) checkDisk(path string) diskUsage {
	// Parse /proc/mounts and df output for cross-platform compatibility
	cmd := exec.Command("df", "-B1", path)
	output, err := cmd.Output()
	if err != nil {
		return diskUsage{Percent: 0, AvailableGB: 0}
	}

	lines := strings.Split(string(output), "\n")
	if len(lines) < 2 {
		return diskUsage{Percent: 0, AvailableGB: 0}
	}

	// Parse second line (skip header)
	fields := strings.Fields(lines[1])
	if len(fields) < 5 {
		return diskUsage{Percent: 0, AvailableGB: 0}
	}

	totalBytes, _ := strconv.ParseUint(fields[1], 10, 64)
	usedBytes, _ := strconv.ParseUint(fields[2], 10, 64)
	availBytes, _ := strconv.ParseUint(fields[3], 10, 64)

	var percent int
	if totalBytes > 0 {
		percent = int((usedBytes * 100) / totalBytes)
	}

	availGB := float64(availBytes) / (1024 * 1024 * 1024)
	return diskUsage{Percent: percent, AvailableGB: availGB}
}

type memoryStatus struct {
	UsageBytes uint64
	LimitBytes uint64
	Percent    int
}

func (w *Watcher) checkMemory() memoryStatus {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)

	limitBytes := w.memoryLimitBytes
	if limitBytes == 0 {
		limitBytes = 1024 * 1024 * 1024 // Default to 1 GB
	}

	usageBytes := m.Alloc
	var percent int
	if limitBytes > 0 {
		percent = int((usageBytes * 100) / limitBytes)
	}

	return memoryStatus{
		UsageBytes: usageBytes,
		LimitBytes: limitBytes,
		Percent:    percent,
	}
}

type fdStatus struct {
	Open    int
	Max     int
	Percent int
}

func (w *Watcher) checkFileDescriptors() fdStatus {
	// Read /proc/self/limits to get FD limit
	limitsPath := "/proc/self/limits"
	data, err := os.ReadFile(limitsPath)
	if err != nil {
		// Fallback: no /proc on this system
		return fdStatus{Open: 0, Max: 0, Percent: 0}
	}

	maxFD := 1024 // Conservative default
	for _, line := range strings.Split(string(data), "\n") {
		// Parse "Max open files" line
		if strings.Contains(line, "Max open files") {
			fields := strings.Fields(line)
			if len(fields) >= 4 {
				if val, err := strconv.Atoi(fields[3]); err == nil {
					maxFD = val
				}
			}
			break
		}
	}

	// Count open FDs in /proc/self/fd
	fdDir := "/proc/self/fd"
	entries, err := os.ReadDir(fdDir)
	if err != nil {
		return fdStatus{Open: 0, Max: maxFD, Percent: 0}
	}

	openCount := len(entries)
	percent := (openCount * 100) / maxFD
	if percent > 100 {
		percent = 100
	}

	return fdStatus{
		Open:    openCount,
		Max:     maxFD,
		Percent: percent,
	}
}

// Critical levels (percent used).
const (
	DiskCritical            = 98
	MemoryCritical          = 95
	FileDescriptorsCritical = 90
)

// CriticalResources returns each resource at its critical level with its use
// in percent ("disk", "memory", "file_descriptors").
func (s ResourceStatus) CriticalResources() map[string]int {
	out := map[string]int{}
	if s.DiskUsagePercent >= DiskCritical {
		out["disk"] = s.DiskUsagePercent
	}
	if s.MemoryPercent >= MemoryCritical {
		out["memory"] = s.MemoryPercent
	}
	if s.FileDescriptorPercent >= FileDescriptorsCritical {
		out["file_descriptors"] = s.FileDescriptorPercent
	}
	return out
}
