package monitoring

import (
	"context"
	"encoding/json"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"dplaned/internal/zfs"
)

// alertState tracks the last time an alert was fired per event type,
// and whether it is currently in a "firing" state.
// This prevents notification flooding when a value oscillates around a threshold.
type alertState struct {
	lastFired time.Time
	lastLevel string
	firingAt  time.Time // when we first entered this level (for hysteresis)
	isFiring  bool
}

// BackgroundMonitor runs periodic checks and sends alerts with debouncing.
type BackgroundMonitor struct {
	interval      time.Duration
	alertCallback func(eventType string, data interface{}, level string)
	stopChan      chan bool

	// Debounce state: one entry per (eventType+level) key
	mu          sync.Mutex
	alertStates map[string]*alertState

	// Mount health: poolName → currently healthy (writable)
	mountMu     sync.Mutex
	mountHealth map[string]bool

	// Disk temperature thresholds (degrees Celsius).
	// Defaults: 45°C warning, 55°C critical.
	TempWarnC     int
	TempCriticalC int

	// Internal tick counter for modulo-N scheduling.
	tickCount uint64
}

// Debounce configuration
const (
	// Minimum time between repeated alerts of the same type+level
	alertCooldown = 5 * time.Minute

	// A threshold must be exceeded for this long before we alert (prevents flapping)
	hysteresisWindow = 30 * time.Second

	// After a condition clears, suppress re-alert for this duration
	clearanceCooldown = 2 * time.Minute
)

// NewBackgroundMonitor creates a new background monitor
func NewBackgroundMonitor(interval time.Duration, alertCallback func(string, interface{}, string)) *BackgroundMonitor {
	return &BackgroundMonitor{
		interval:      interval,
		alertCallback: alertCallback,
		stopChan:      make(chan bool),
		alertStates:   make(map[string]*alertState),
		mountHealth:   make(map[string]bool),
		TempWarnC:     45,
		TempCriticalC: 55,
	}
}

// Start begins the monitoring loop
func (m *BackgroundMonitor) Start() {
	go m.run()
}

// Stop halts the monitoring loop
func (m *BackgroundMonitor) Stop() {
	m.stopChan <- true
}

func (m *BackgroundMonitor) run() {
	ticker := time.NewTicker(m.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			m.tickCount++
			m.check()
		case <-m.stopChan:
			log.Println("Background monitor stopped")
			return
		}
	}
}

// maybeAlert fires the alertCallback only if debounce conditions are met.
// key should be unique per event type, e.g. "inotify_warning".
// level: "info", "warning", "critical", "clear"
func (m *BackgroundMonitor) maybeAlert(key string, level string, data interface{}) {
	m.mu.Lock()
	defer m.mu.Unlock()

	now := time.Now()
	state, exists := m.alertStates[key]
	if !exists {
		state = &alertState{}
		m.alertStates[key] = state
	}

	// "info" level updates are always passed through for UI dashboard refresh -
	// they are not alerting events and don't need debouncing.
	if level == "info" {
		m.alertCallback(key, data, level)
		return
	}

	// "clear": condition resolved - notify once, then enter clearance cooldown
	if level == "clear" {
		if state.isFiring {
			state.isFiring = false
			state.lastFired = now
			state.lastLevel = "clear"
			m.alertCallback(key, data, level)
			log.Printf("MONITOR [%s]: condition cleared", key)
		}
		return
	}

	// For warning/critical: apply hysteresis - must be in this state for
	// hysteresisWindow before we fire, to prevent flapping alerts.
	if !state.isFiring || state.lastLevel != level {
		// Condition just entered or level changed - start hysteresis timer
		if !state.isFiring || state.lastLevel != level {
			state.firingAt = now
			state.isFiring = true
			state.lastLevel = level
		}
	}

	if now.Sub(state.firingAt) < hysteresisWindow {
		// Still within hysteresis window - don't fire yet
		return
	}

	// Apply cooldown: don't re-fire same level within cooldown period
	if !state.lastFired.IsZero() && now.Sub(state.lastFired) < alertCooldown {
		return
	}

	// All checks passed - fire the alert
	state.lastFired = now
	m.alertCallback(key, data, level)
	log.Printf("MONITOR [%s]: alert fired (level=%s)", key, level)
}

// check runs all periodic monitors.
// Mount status is checked on every tick (30 s by default).
// Disk temperatures are checked every 10 ticks (≈ 5 min at 30 s intervals).
func (m *BackgroundMonitor) check() {
	// ── Inotify stats ────────────────────────────────────────────────────────
	stats, err := GetInotifyStats()
	if err != nil {
		log.Printf("Error getting inotify stats: %v", err)
	} else {
		if stats.Critical {
			m.maybeAlert("inotify_status", "critical", stats)
		} else if stats.Warning {
			m.maybeAlert("inotify_status", "warning", stats)
		} else {
			m.maybeAlert("inotify_status", "clear", stats)
			m.maybeAlert("inotify_status", "info", stats)
		}
	}

	// ── Mount status (every tick) ─────────────────────────────────────────────
	m.CheckMountStatus()

	// ── Disk temperatures (every 10 ticks ≈ 5 min at 30 s default interval) ──
	if m.tickCount%10 == 0 {
		m.checkDiskTemperatures()
	}

	// ── ZFS Resilver/Scrub Progress (every tick) ─────────────────────────────
	m.checkZFSProgress()
}

// ── Mount status ──────────────────────────────────────────────────────────────

// healthFileName is written inside each mounted pool root to verify
// the filesystem is writable.
const healthFileName = ".dplaneos-health"

// CheckMountStatus verifies that each known ONLINE ZFS pool's mountpoint is
// actually mounted and writable.
//
// Algorithm:
//  1. Run `zpool list -H -o name,health` to enumerate pools.
//  2. For each ONLINE pool, resolve its mountpoint with `zfs get mountpoint`.
//  3. Stat the mountpoint and attempt to write a small sentinel file.
//  4. On failure: broadcast "mountError" and update the internal health map.
//  5. On recovery: broadcast "mountError" clear.
func (m *BackgroundMonitor) CheckMountStatus() {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	listOut, err := exec.CommandContext(ctx, "zpool", "list", "-H", "-o", "name,health").CombinedOutput()
	if err != nil {
		log.Printf("MONITOR: zpool list failed: %v", err)
		return
	}

	for _, line := range strings.Split(strings.TrimSpace(string(listOut)), "\n") {
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		poolName := fields[0]
		poolState := fields[1]

		// Only check ONLINE pools - degraded states are handled by pool_heartbeat.
		if poolState != "ONLINE" {
			continue
		}

		mountPoint := zpoolMountpoint(poolName)
		if mountPoint == "" || mountPoint == "-" || mountPoint == "none" || mountPoint == "legacy" {
			continue
		}

		healthy := m.testMountWritable(poolName, mountPoint)
		alertKey := "mount_health_" + poolName

		m.mountMu.Lock()
		prevHealthy, seen := m.mountHealth[poolName]
		m.mountHealth[poolName] = healthy
		m.mountMu.Unlock()

		if !healthy {
			m.maybeAlert(alertKey, "critical", map[string]interface{}{
				"pool":       poolName,
				"mountpoint": mountPoint,
				"error":      "mountpoint not writable",
			})
		} else if seen && !prevHealthy && healthy {
			// Pool recovered - clear the firing alert.
			m.maybeAlert(alertKey, "clear", map[string]interface{}{
				"pool":       poolName,
				"mountpoint": mountPoint,
			})
		}
	}
}

// testMountWritable returns true if mountPoint is accessible and a sentinel
// file can be written inside it.
func (m *BackgroundMonitor) testMountWritable(poolName, mountPoint string) bool {
	if _, err := os.Stat(mountPoint); err != nil {
		log.Printf("MONITOR: pool %s mountpoint %s inaccessible: %v", poolName, mountPoint, err)
		return false
	}
	testFile := filepath.Join(mountPoint, healthFileName)
	content := []byte("dplaneos-health:" + strconv.FormatInt(time.Now().Unix(), 10) + "\n")
	if err := os.WriteFile(testFile, content, 0600); err != nil {
		log.Printf("MONITOR: pool %s mountpoint %s write failed: %v", poolName, mountPoint, err)
		return false
	}
	return true
}

// zpoolMountpoint returns the mountpoint property of a ZFS pool.
func zpoolMountpoint(poolName string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "zfs", "get", "-H", "-o", "value", "mountpoint", poolName).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// ── Disk temperature monitoring ───────────────────────────────────────────────

// diskTempReading is a per-disk temperature observation.
type diskTempReading struct {
	DevName string
	TempC   int
}

// checkDiskTemperatures reads temperatures from /sys/class/hwmon/ and from
// smartctl for any block device not covered by hwmon, then broadcasts
// warning/critical events for disks that exceed the configured thresholds.
func (m *BackgroundMonitor) checkDiskTemperatures() {
	readings := m.readHwmonTemps()

	// Supplement with smartctl for block devices not covered by hwmon.
	covered := make(map[string]bool)
	for _, r := range readings {
		covered[r.DevName] = true
	}
	for _, dev := range listBlockDevices() {
		if covered[dev] {
			continue
		}
		if temp := readSmartTemp(dev); temp > 0 {
			readings = append(readings, diskTempReading{DevName: dev, TempC: temp})
		}
	}

	for _, r := range readings {
		alertKey := "disk_temp_" + r.DevName
		data := map[string]interface{}{
			"device": r.DevName,
			"temp_c": r.TempC,
		}

		if r.TempC >= m.TempCriticalC {
			log.Printf("MONITOR: disk %s temperature critical: %d°C", r.DevName, r.TempC)
			m.maybeAlert(alertKey, "critical", data)
			// Immediate broadcast (bypasses per-key debounce so the UI always sees it).
			m.alertCallback("diskTempWarning", data, "critical")
		} else if r.TempC >= m.TempWarnC {
			log.Printf("MONITOR: disk %s temperature warning: %d°C", r.DevName, r.TempC)
			m.maybeAlert(alertKey, "warning", data)
			m.alertCallback("diskTempWarning", data, "warning")
		} else {
			m.maybeAlert(alertKey, "clear", data)
		}
	}
}

// readHwmonTemps reads temperature inputs from /sys/class/hwmon/ and returns
// a slice of readings for known disk-related sensors (drivetemp, nvme, ahci).
func (m *BackgroundMonitor) readHwmonTemps() []diskTempReading {
	var readings []diskTempReading

	entries, err := os.ReadDir("/sys/class/hwmon")
	if err != nil {
		return readings
	}

	// Disk-related hwmon driver names.
	diskDrivers := map[string]bool{
		"drivetemp": true, // kernel 5.6+ unified drive temperature
		"nvme":      true,
		"ahci":      true,
	}

	for _, entry := range entries {
		hwmonPath := filepath.Join("/sys/class/hwmon", entry.Name())

		nameBytes, err := os.ReadFile(filepath.Join(hwmonPath, "name"))
		if err != nil {
			continue
		}
		if !diskDrivers[strings.TrimSpace(string(nameBytes))] {
			continue
		}

		// temp1_input is in milli-Celsius.
		tempData, err := os.ReadFile(filepath.Join(hwmonPath, "temp1_input"))
		if err != nil {
			continue
		}
		milliC, err := strconv.ParseInt(strings.TrimSpace(string(tempData)), 10, 64)
		if err != nil || milliC <= 0 {
			continue
		}

		devName := resolveHwmonToBlockDev(hwmonPath)
		if devName == "" {
			devName = strings.TrimSpace(string(nameBytes)) + "_" + entry.Name()
		}

		readings = append(readings, diskTempReading{
			DevName: devName,
			TempC:   int(milliC / 1000),
		})
	}
	return readings
}

// resolveHwmonToBlockDev follows sysfs links from the hwmon device path back
// up the tree to find a /sys/block/<name> ancestor.
func resolveHwmonToBlockDev(hwmonPath string) string {
	dir := hwmonPath
	for i := 0; i < 8; i++ {
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		if filepath.Base(filepath.Dir(parent)) == "block" {
			return filepath.Base(parent)
		}
		dir = parent
	}
	return ""
}

// listBlockDevices returns the names of non-virtual block devices from /sys/block/.
func listBlockDevices() []string {
	entries, err := os.ReadDir("/sys/block")
	if err != nil {
		return nil
	}
	var devs []string
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, "loop") ||
			strings.HasPrefix(name, "dm-") ||
			strings.HasPrefix(name, "ram") ||
			strings.HasPrefix(name, "zram") {
			continue
		}
		devs = append(devs, name)
	}
	return devs
}

// smartJSON is the minimal subset of smartctl -A -j output we need.
type smartJSON struct {
	Temperature struct {
		Current int `json:"current"`
	} `json:"temperature"`
	ATASmartAttributes struct {
		Table []struct {
			Name  string `json:"name"`
			Value int    `json:"value"`
			Raw   struct {
				Value int `json:"value"`
			} `json:"raw"`
		} `json:"table"`
	} `json:"ata_smart_attributes"`
}

// readSmartTemp runs `smartctl -A -j /dev/<name>` with a 3-second timeout and
// returns the drive temperature in Celsius.  Returns 0 if unavailable.
func readSmartTemp(devName string) int {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	out, err := exec.CommandContext(ctx, "smartctl", "-A", "-j", "/dev/"+devName).Output()
	if err != nil && len(out) == 0 {
		return 0
	}

	var s smartJSON
	if err := json.Unmarshal(out, &s); err != nil {
		return 0
	}

	if s.Temperature.Current > 0 {
		return s.Temperature.Current
	}
	for _, attr := range s.ATASmartAttributes.Table {
		if attr.Name == "Temperature_Celsius" || attr.Name == "Airflow_Temperature_Cel" {
			if attr.Raw.Value > 0 {
				return attr.Raw.Value
			}
			return attr.Value
		}
	}
	return 0
}

// checkZFSProgress enumerates all pools and broadcasts incremental progress
// for active resilvers or scrubs.
func (m *BackgroundMonitor) checkZFSProgress() {
	pools, err := zfs.DiscoverPools()
	if err != nil {
		return
	}

	for _, p := range pools {
		rawScan, err := zfs.GetPoolScanLine(p.Name)
		if err != nil {
			continue
		}

		parsed := zfs.ParseScanLine(rawScan)
		if !parsed.InProgress {
			continue
		}

		// Determine event type: zfs.resilver.progress or zfs.scrub.progress
		eventType := "zfs.scrub.progress"
		if strings.Contains(strings.ToLower(rawScan), "resilver") {
			eventType = "zfs.resilver.progress"
		}

		// Broadcast incremental progress to all connected UI clients.
		// Higher-level 'maybeAlert' is not used here because we WANT frequent
		// updates (every 30s) while the operation is active.
		m.alertCallback(eventType, map[string]interface{}{
			"pool":         p.Name,
			"percent_done": parsed.PercentDone,
			"eta":          parsed.ETA,
			"bytes_done":   parsed.BytesDone,
		}, "info")
	}
}
