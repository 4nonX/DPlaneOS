// Package resource watches what the daemon itself needs to keep working:
// free space on the system disk, free system memory and file descriptors.
// The result is the "resources" health check; a change to DEGRADED or
// CRITICAL (and back) is reported through the callback, which the daemon
// turns into an alert.
package resource

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Thresholds in percent used.
const (
	diskDegraded, diskCritical = 90, 97
	memDegraded, memCritical   = 90, 97
	fdDegraded, fdCritical     = 80, 90
)

// ResourceStatus is one measurement.
type ResourceStatus struct {
	Timestamp             time.Time
	DiskPath              string // the fullest of the watched file systems
	DiskUsagePercent      int
	DiskAvailableGB       float64
	MemoryPercent         int // system memory in use, not counting what the ZFS ARC gives back
	OpenFileCount         int
	FileDescriptorMax     int
	FileDescriptorPercent int
	Status                string // "OK", "DEGRADED", "CRITICAL"
	Warnings              []string
}

// Watcher measures periodically and reports status changes.
type Watcher struct {
	mu            sync.RWMutex
	lastStatus    ResourceStatus
	checkInterval time.Duration
	stopChan      chan struct{}
	stopOnce      sync.Once
	diskPaths     []string
	onChange      func(prev, cur ResourceStatus)

	// Replaced in tests.
	diskUsage func(path string) (percent int, availGB float64, ok bool)
	memUsage  func() (percent int, ok bool)
	fdUsage   func() (open, max int, ok bool)
}

// NewWatcher creates a watcher for the file systems holding diskPaths
// (paths that do not exist are skipped).
func NewWatcher(checkInterval time.Duration, diskPaths []string) *Watcher {
	w := &Watcher{
		checkInterval: checkInterval,
		diskPaths:     diskPaths,
		stopChan:      make(chan struct{}),
		diskUsage:     dfUsage,
		memUsage:      systemMemoryUsage,
		fdUsage:       fileDescriptorUsage,
	}
	w.lastStatus = w.check()
	return w
}

// OnChange sets the function called when the status changes (OK, DEGRADED,
// CRITICAL). It runs in its own goroutine.
func (w *Watcher) OnChange(fn func(prev, cur ResourceStatus)) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.onChange = fn
}

// Start measures every interval until Stop.
func (w *Watcher) Start() { go w.loop() }

// Stop ends the loop.
func (w *Watcher) Stop() { w.stopOnce.Do(func() { close(w.stopChan) }) }

// GetStatus returns the last measurement.
func (w *Watcher) GetStatus() ResourceStatus {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.lastStatus
}

func (w *Watcher) loop() {
	ticker := time.NewTicker(w.checkInterval)
	defer ticker.Stop()
	for {
		select {
		case <-w.stopChan:
			return
		case <-ticker.C:
			w.Tick()
		}
	}
}

// Tick measures once and reports a status change.
func (w *Watcher) Tick() {
	cur := w.check()
	w.mu.Lock()
	prev := w.lastStatus
	w.lastStatus = cur
	fn := w.onChange
	w.mu.Unlock()
	if fn != nil && prev.Status != cur.Status {
		go fn(prev, cur)
	}
}

func (w *Watcher) check() ResourceStatus {
	st := ResourceStatus{Timestamp: time.Now(), Warnings: []string{}, Status: "OK"}
	raise := func(level, msg string) {
		if level == "CRITICAL" || st.Status == "OK" {
			st.Status = level
		}
		st.Warnings = append(st.Warnings, msg)
	}
	grade := func(percent, degraded, critical int, what string) {
		switch {
		case percent >= critical:
			raise("CRITICAL", what)
		case percent >= degraded:
			raise("DEGRADED", what)
		}
	}

	// The fullest of the watched file systems counts.
	st.DiskUsagePercent = -1
	for _, p := range w.diskPaths {
		pct, avail, ok := w.diskUsage(p)
		if ok && pct > st.DiskUsagePercent {
			st.DiskPath, st.DiskUsagePercent, st.DiskAvailableGB = p, pct, avail
		}
	}
	if st.DiskUsagePercent < 0 {
		st.DiskUsagePercent = 0
	} else {
		grade(st.DiskUsagePercent, diskDegraded, diskCritical,
			fmt.Sprintf("system disk (%s) %d%% full, %.1f GB free", st.DiskPath, st.DiskUsagePercent, st.DiskAvailableGB))
	}

	if pct, ok := w.memUsage(); ok {
		st.MemoryPercent = pct
		grade(pct, memDegraded, memCritical, fmt.Sprintf("memory %d%% in use", pct))
	}

	if open, max, ok := w.fdUsage(); ok && max > 0 {
		st.OpenFileCount, st.FileDescriptorMax = open, max
		st.FileDescriptorPercent = min(open*100/max, 100)
		grade(st.FileDescriptorPercent, fdDegraded, fdCritical,
			fmt.Sprintf("the daemon has %d of %d file descriptors open", open, max))
	}
	return st
}

// dfUsage reads the usage of the file system holding path.
func dfUsage(path string) (int, float64, bool) {
	if _, err := os.Stat(path); err != nil {
		return 0, 0, false
	}
	out, err := exec.Command("df", "-P", "-B1", path).Output()
	if err != nil {
		return 0, 0, false
	}
	return parseDf(string(out))
}

// parseDf reads "df -P -B1" output: Filesystem, blocks, used, available, ...
// Percent is used/(used+available), as df prints it (root-reserved blocks
// are not available to the daemon's data either way).
func parseDf(out string) (int, float64, bool) {
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) < 2 {
		return 0, 0, false
	}
	f := strings.Fields(lines[len(lines)-1])
	if len(f) < 4 {
		return 0, 0, false
	}
	used, err1 := strconv.ParseUint(f[2], 10, 64)
	avail, err2 := strconv.ParseUint(f[3], 10, 64)
	if err1 != nil || err2 != nil || used+avail == 0 {
		return 0, 0, false
	}
	pct := int((used*100 + (used+avail)/2) / (used + avail))
	return pct, float64(avail) / (1 << 30), true
}

// systemMemoryUsage: memory in use as the kernel sees it, minus what the ZFS
// ARC releases under pressure (the ARC is not page cache, so MemAvailable
// does not count it; on a NAS it holds most of the RAM by design).
func systemMemoryUsage() (int, bool) {
	data, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0, false
	}
	arc, _ := os.ReadFile("/proc/spl/kstat/zfs/arcstats")
	return parseMemory(string(data), string(arc))
}

func parseMemory(meminfo, arcstats string) (int, bool) {
	kb := func(key string) (uint64, bool) {
		for _, line := range strings.Split(meminfo, "\n") {
			if rest, ok := strings.CutPrefix(line, key+":"); ok {
				f := strings.Fields(rest)
				if len(f) == 0 {
					return 0, false
				}
				v, err := strconv.ParseUint(f[0], 10, 64)
				return v, err == nil
			}
		}
		return 0, false
	}
	total, ok1 := kb("MemTotal")
	avail, ok2 := kb("MemAvailable")
	if !ok1 || !ok2 || total == 0 {
		return 0, false
	}
	availBytes := avail * 1024
	// arcstats lines: name type data
	var size, cmin uint64
	for _, line := range strings.Split(arcstats, "\n") {
		f := strings.Fields(line)
		if len(f) != 3 {
			continue
		}
		switch f[0] {
		case "size":
			size, _ = strconv.ParseUint(f[2], 10, 64)
		case "c_min":
			cmin, _ = strconv.ParseUint(f[2], 10, 64)
		}
	}
	if size > cmin {
		availBytes += size - cmin
	}
	totalBytes := total * 1024
	if availBytes > totalBytes {
		availBytes = totalBytes
	}
	return int((totalBytes - availBytes) * 100 / totalBytes), true
}

// fileDescriptorUsage: the daemon's open descriptors against its limit.
func fileDescriptorUsage() (int, int, bool) {
	data, err := os.ReadFile("/proc/self/limits")
	if err != nil {
		return 0, 0, false
	}
	max := 0
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "Max open files") {
			if f := strings.Fields(line); len(f) >= 4 {
				max, _ = strconv.Atoi(f[3])
			}
			break
		}
	}
	entries, err := os.ReadDir("/proc/self/fd")
	if err != nil || max <= 0 {
		return 0, 0, false
	}
	return len(entries), max, true
}
