package handlers

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"dplaned/internal/monitoring"
)

// ── Settings persistence ──────────────────────────────────────────────────────

type SystemSettings struct {
	ARCLimitGB           int  `json:"arc_limit_gb"`
	Swappiness           int  `json:"swappiness"`
	RealtimeEnabled      bool `json:"realtime_enabled"`
	PeriodicEnabled      bool `json:"periodic_enabled"`
	InotifyWarnThreshold int  `json:"inotify_warn_threshold"`
	MemoryWarnThreshold  int  `json:"memory_warn_threshold"`
	IOWaitWarnThreshold  int  `json:"iowait_warn_threshold"`
	WebSocketAlerts      bool `json:"websocket_alerts"`
}

var defaultSystemSettings = SystemSettings{
	ARCLimitGB:           8,
	Swappiness:           10,
	RealtimeEnabled:      true,
	PeriodicEnabled:      true,
	InotifyWarnThreshold: 80,
	MemoryWarnThreshold:  85,
	IOWaitWarnThreshold:  20,
	WebSocketAlerts:      true,
}

const systemSettingsFile = "system-settings.json"

func systemSettingsPath() string {
	return ConfigDir + "/" + systemSettingsFile
}

func loadSystemSettings() (SystemSettings, error) {
	f, err := os.Open(systemSettingsPath())
	if err != nil {
		if os.IsNotExist(err) {
			return defaultSystemSettings, nil
		}
		return defaultSystemSettings, err
	}
	defer f.Close()

	var s SystemSettings
	if err := json.NewDecoder(f).Decode(&s); err != nil {
		return defaultSystemSettings, err
	}
	return s, nil
}

func saveSystemSettings(s SystemSettings) error {
	os.MkdirAll(ConfigDir, 0755)
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(systemSettingsPath(), data, 0644)
}

// applyARCLimit writes the ARC max to the running kernel and to the modprobe
// persistence file so the setting survives a reboot.
func applyARCLimit(gb int) error {
	bytes := int64(gb) * 1024 * 1024 * 1024
	val := strconv.FormatInt(bytes, 10)

	// Live kernel parameter
	if err := os.WriteFile("/sys/module/zfs/parameters/zfs_arc_max", []byte(val), 0644); err != nil {
		return fmt.Errorf("write zfs_arc_max sysfs: %w", err)
	}

	// Persist across reboots via modprobe
	line := fmt.Sprintf("options zfs zfs_arc_max=%s\n", val)
	if err := os.WriteFile("/etc/modprobe.d/zfs.conf", []byte(line), 0644); err != nil {
		return fmt.Errorf("write /etc/modprobe.d/zfs.conf: %w", err)
	}
	return nil
}

// applySwappiness writes the swappiness value to the running kernel and to
// the sysctl drop-in file for persistence.
func applySwappiness(v int) error {
	val := strconv.Itoa(v)

	// Live kernel parameter
	if err := os.WriteFile("/proc/sys/vm/swappiness", []byte(val), 0644); err != nil {
		return fmt.Errorf("write swappiness: %w", err)
	}

	// Persist via sysctl drop-in
	line := fmt.Sprintf("vm.swappiness=%s\n", val)
	if err := os.WriteFile("/etc/sysctl.d/99-dplaneos.conf", []byte(line), 0644); err != nil {
		return fmt.Errorf("write /etc/sysctl.d/99-dplaneos.conf: %w", err)
	}
	return nil
}

// HandleSystemSettings handles GET/POST for /api/system/tuning.
// GET  — returns current settings (file or defaults).
// POST — validates, persists, and applies kernel-level tuning.
func HandleSystemSettings(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	if r.Method == http.MethodGet {
		settings, err := loadSystemSettings()
		if err != nil {
			// Non-fatal: return defaults with a warning header
			w.Header().Set("X-Settings-Warning", "could not read settings file: "+err.Error())
		}
		json.NewEncoder(w).Encode(settings)
		return
	}

	if r.Method == http.MethodPost {
		var settings SystemSettings
		if err := json.NewDecoder(r.Body).Decode(&settings); err != nil {
			http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
			return
		}

		// Basic validation
		if settings.ARCLimitGB < 1 {
			http.Error(w, "arc_limit_gb must be >= 1", http.StatusBadRequest)
			return
		}
		if settings.Swappiness < 0 || settings.Swappiness > 100 {
			http.Error(w, "swappiness must be 0–100", http.StatusBadRequest)
			return
		}

		// Apply kernel-level settings (best-effort; log but don't fail the request)
		var applyErrors []string

		if err := applyARCLimit(settings.ARCLimitGB); err != nil {
			applyErrors = append(applyErrors, "arc_limit: "+err.Error())
		}
		if err := applySwappiness(settings.Swappiness); err != nil {
			applyErrors = append(applyErrors, "swappiness: "+err.Error())
		}

		// Persist to JSON (all fields including monitoring thresholds)
		if err := saveSystemSettings(settings); err != nil {
			http.Error(w, "failed to save settings: "+err.Error(), http.StatusInternalServerError)
			return
		}

		resp := map[string]interface{}{
			"success": true,
			"status":  "saved",
		}
		if len(applyErrors) > 0 {
			resp["apply_warnings"] = applyErrors
		}
		json.NewEncoder(w).Encode(resp)
		return
	}

	http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
}

// ── Metrics ───────────────────────────────────────────────────────────────────

// SystemMetrics is the full metrics payload returned by /api/system/metrics.
type SystemMetrics struct {
	Success    bool           `json:"success"`
	Inotify    InotifyMetrics `json:"inotify"`
	Memory     MemoryMetrics  `json:"memory"`
	ARC        ARCMetrics     `json:"arc"`
	IOWait     int            `json:"iowait"`
	CPUModel   string         `json:"cpu_model"`
	CPUPercent float64        `json:"cpu_percent"`
	Uptime     string         `json:"uptime"`
	OS         string         `json:"os"`
	Kernel     string         `json:"kernel"`
	LoadAvg    []float64      `json:"load_avg"`
}

type InotifyMetrics struct {
	Used    int     `json:"used"`
	Limit   int     `json:"limit"`
	Percent float64 `json:"percent"`
}

type MemoryMetrics struct {
	Used    uint64  `json:"used"`
	Total   uint64  `json:"total"`
	Percent float64 `json:"percent"`
}

type ARCMetrics struct {
	Used    uint64  `json:"used"`
	Limit   uint64  `json:"limit"`
	Percent float64 `json:"percent"`
}

func HandleSystemMetrics(w http.ResponseWriter, r *http.Request) {
	metrics := SystemMetrics{Success: true}

	// ── Inotify ──────────────────────────────────────────────
	if stats, err := monitoring.GetInotifyStats(); err == nil {
		metrics.Inotify = InotifyMetrics{
			Used:    stats.Used,
			Limit:   stats.Limit,
			Percent: stats.Percent,
		}
	}

	// ── Memory ───────────────────────────────────────────────
	if mem, err := readMeminfo(); err == nil {
		total := mem["MemTotal"]
		available := mem["MemAvailable"]
		used := total - available
		pct := 0.0
		if total > 0 {
			pct = float64(used) / float64(total) * 100.0
		}
		metrics.Memory = MemoryMetrics{Used: used * 1024, Total: total * 1024, Percent: pct}
	}

	// ── ZFS ARC ──────────────────────────────────────────────
	if arc, err := readARCStats(); err == nil {
		pct := 0.0
		if arc.Limit > 0 {
			pct = float64(arc.Used) / float64(arc.Limit) * 100.0
		}
		metrics.ARC = ARCMetrics{Used: arc.Used, Limit: arc.Limit, Percent: pct}
	}

	// ── I/O Wait ─────────────────────────────────────────────
	if iowait, err := readIOWait(); err == nil {
		metrics.IOWait = iowait
	}

	// ── CPU Model ────────────────────────────────────────────
	if model, err := readCPUModel(); err == nil {
		metrics.CPUModel = model
	}

	// ── CPU Percent (two-sample, 200 ms apart) ────────────────
	if pct, err := readCPUPercent(); err == nil {
		metrics.CPUPercent = pct
	}

	// ── Uptime ───────────────────────────────────────────────
	if uptime, err := readUptime(); err == nil {
		metrics.Uptime = uptime
	}

	// ── OS pretty name ───────────────────────────────────────
	if osName, err := readOSRelease(); err == nil {
		metrics.OS = osName
	}

	// ── Kernel version ───────────────────────────────────────
	if kernel, err := readKernelVersion(); err == nil {
		metrics.Kernel = kernel
	}

	// ── Load average ─────────────────────────────────────────
	if loadAvg, err := readLoadAvg(); err == nil {
		metrics.LoadAvg = loadAvg
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(metrics)
}

// ── helpers ───────────────────────────────────────────────────────────────────

// readMeminfo parses /proc/meminfo into a kB map.
func readMeminfo() (map[string]uint64, error) {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return nil, err
	}
	defer f.Close()
	result := make(map[string]uint64)
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		parts := strings.SplitN(scanner.Text(), ":", 2)
		if len(parts) != 2 {
			continue
		}
		key := strings.TrimSpace(parts[0])
		valStr := strings.Fields(strings.TrimSpace(parts[1]))[0]
		val, err := strconv.ParseUint(valStr, 10, 64)
		if err == nil {
			result[key] = val
		}
	}
	return result, scanner.Err()
}

type arcRaw struct{ Used, Limit uint64 }

// readARCStats reads ZFS ARC size and target from /proc/spl/kstat/zfs/arcstats.
func readARCStats() (arcRaw, error) {
	f, err := os.Open("/proc/spl/kstat/zfs/arcstats")
	if err != nil {
		return arcRaw{}, err
	}
	defer f.Close()
	var arc arcRaw
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 3 {
			continue
		}
		val, err := strconv.ParseUint(fields[2], 10, 64)
		if err != nil {
			continue
		}
		switch fields[0] {
		case "size":
			arc.Used = val
		case "c_max":
			arc.Limit = val
		}
	}
	return arc, scanner.Err()
}

// readIOWait reads the iowait percentage from /proc/stat (first CPU line).
func readIOWait() (int, error) {
	f, err := os.Open("/proc/stat")
	if err != nil {
		return 0, err
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "cpu ") {
			continue
		}
		fields := strings.Fields(line)
		// cpu user nice system idle iowait irq softirq ...
		if len(fields) < 6 {
			break
		}
		var total, iowait uint64
		for i, f := range fields[1:] {
			v, _ := strconv.ParseUint(f, 10, 64)
			total += v
			if i == 4 { // iowait is index 4 in the value list
				iowait = v
			}
		}
		if total > 0 {
			return int(iowait * 100 / total), nil
		}
	}
	return 0, nil
}

// readCPUModel returns the model name from /proc/cpuinfo.
func readCPUModel() (string, error) {
	f, err := os.Open("/proc/cpuinfo")
	if err != nil {
		return "", err
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "model name") {
			parts := strings.SplitN(line, ":", 2)
			if len(parts) == 2 {
				return strings.TrimSpace(parts[1]), nil
			}
		}
	}
	return "", fmt.Errorf("model name not found in /proc/cpuinfo")
}

// cpuStatFields reads the aggregate CPU line from /proc/stat and returns
// (idle, total) jiffies.
func cpuStatFields() (idle, total uint64, err error) {
	f, err := os.Open("/proc/stat")
	if err != nil {
		return 0, 0, err
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "cpu ") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 5 {
			return 0, 0, fmt.Errorf("unexpected cpu line: %s", line)
		}
		// fields[0] = "cpu", then: user nice system idle iowait irq softirq ...
		for i, s := range fields[1:] {
			v, parseErr := strconv.ParseUint(s, 10, 64)
			if parseErr != nil {
				continue
			}
			total += v
			if i == 3 { // idle is index 3 in the value slice
				idle = v
			}
		}
		return idle, total, nil
	}
	return 0, 0, fmt.Errorf("cpu line not found in /proc/stat")
}

// readCPUPercent takes two samples 200 ms apart and computes utilisation.
func readCPUPercent() (float64, error) {
	idle1, total1, err := cpuStatFields()
	if err != nil {
		return 0, err
	}
	time.Sleep(200 * time.Millisecond)
	idle2, total2, err := cpuStatFields()
	if err != nil {
		return 0, err
	}

	deltaTotal := total2 - total1
	deltaIdle := idle2 - idle1
	if deltaTotal == 0 {
		return 0, nil
	}
	return float64(deltaTotal-deltaIdle) / float64(deltaTotal) * 100.0, nil
}

// readUptime reads /proc/uptime and formats the first field as "Xd Yh Zm".
func readUptime() (string, error) {
	data, err := os.ReadFile("/proc/uptime")
	if err != nil {
		return "", err
	}
	fields := strings.Fields(string(data))
	if len(fields) == 0 {
		return "", fmt.Errorf("empty /proc/uptime")
	}
	secs, err := strconv.ParseFloat(fields[0], 64)
	if err != nil {
		return "", err
	}
	total := int(secs)
	days := total / 86400
	hours := (total % 86400) / 3600
	mins := (total % 3600) / 60
	return fmt.Sprintf("%dd %dh %dm", days, hours, mins), nil
}

// readOSRelease returns the PRETTY_NAME value from /etc/os-release.
func readOSRelease() (string, error) {
	f, err := os.Open("/etc/os-release")
	if err != nil {
		return "", err
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "PRETTY_NAME=") {
			val := strings.TrimPrefix(line, "PRETTY_NAME=")
			val = strings.Trim(val, `"`)
			return val, nil
		}
	}
	return "", fmt.Errorf("PRETTY_NAME not found in /etc/os-release")
}

// readKernelVersion runs `uname -r` and returns the trimmed output.
func readKernelVersion() (string, error) {
	var out bytes.Buffer
	cmd := exec.Command("uname", "-r")
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return "", err
	}
	return strings.TrimSpace(out.String()), nil
}

// readLoadAvg parses /proc/loadavg and returns the first three fields.
func readLoadAvg() ([]float64, error) {
	data, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return nil, err
	}
	fields := strings.Fields(string(data))
	if len(fields) < 3 {
		return nil, fmt.Errorf("unexpected /proc/loadavg format")
	}
	result := make([]float64, 3)
	for i := 0; i < 3; i++ {
		v, err := strconv.ParseFloat(fields[i], 64)
		if err != nil {
			return nil, err
		}
		result[i] = v
	}
	return result, nil
}
