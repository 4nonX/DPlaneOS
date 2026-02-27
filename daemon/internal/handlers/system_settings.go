package handlers

import (
	"bufio"
	"encoding/json"
	"net/http"
	"os"
	"strconv"
	"strings"

	"dplaned/internal/monitoring"
)

type SystemSettings struct {
	ARCLimitGB          int  `json:"arc_limit_gb"`
	Swappiness          int  `json:"swappiness"`
	RealtimeEnabled     bool `json:"realtime_enabled"`
	PeriodicEnabled     bool `json:"periodic_enabled"`
	InotifyWarnThreshold int `json:"inotify_warn_threshold"`
	MemoryWarnThreshold  int `json:"memory_warn_threshold"`
	IOWaitWarnThreshold  int `json:"iowait_warn_threshold"`
	WebSocketAlerts     bool `json:"websocket_alerts"`
}

type SystemMetrics struct {
	Inotify InotifyMetrics `json:"inotify"`
	Memory  MemoryMetrics  `json:"memory"`
	ARC     ARCMetrics     `json:"arc"`
	IOWait  int            `json:"iowait"`
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
	metrics := SystemMetrics{}

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

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(metrics)
}

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
