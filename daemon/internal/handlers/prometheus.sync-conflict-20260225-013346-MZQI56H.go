package handlers

import (
	"bufio"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

// HandlePrometheusMetrics exposes system metrics in Prometheus text format.
// GET /metrics
//
// No authentication required so Prometheus can scrape without tokens.
// Restrict access via network policy / firewall if needed.
func HandlePrometheusMetrics(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")

	now := time.Now().UnixMilli()
	b := &strings.Builder{}

	writeMetric(b, "dplaneos_scrape_timestamp_ms", nil, float64(now), "Timestamp of this scrape in milliseconds")

	// ── Memory ─────────────────────────────────────────────────────
	memStats := readMemInfo()
	if total, ok := memStats["MemTotal"]; ok {
		writeMetric(b, "dplaneos_memory_total_bytes", nil, kbToBytes(total), "Total physical memory")
	}
	if avail, ok := memStats["MemAvailable"]; ok {
		writeMetric(b, "dplaneos_memory_available_bytes", nil, kbToBytes(avail), "Available memory")
	}
	if total, ok := memStats["MemTotal"]; ok {
		if avail, ok2 := memStats["MemAvailable"]; ok2 {
			used := total - avail
			writeMetric(b, "dplaneos_memory_used_bytes", nil, kbToBytes(used), "Used memory (total - available)")
		}
	}

	// ── ZFS ARC ────────────────────────────────────────────────────
	if arcSize := readSysfsUint64("/proc/spl/kstat/zfs/arcstats", "size"); arcSize > 0 {
		writeMetric(b, "dplaneos_zfs_arc_size_bytes", nil, float64(arcSize), "ZFS ARC current size")
	}
	if arcMax := readSysfsUint64("/proc/spl/kstat/zfs/arcstats", "c_max"); arcMax > 0 {
		writeMetric(b, "dplaneos_zfs_arc_max_bytes", nil, float64(arcMax), "ZFS ARC maximum size")
	}

	// ── CPU (from /proc/stat) ──────────────────────────────────────
	if iowait, total, err := readCPUStat(); err == nil {
		writeMetric(b, "dplaneos_cpu_iowait_ratio", nil, iowait/total, "CPU IO wait fraction (0-1)")
	}

	// ── Load average ───────────────────────────────────────────────
	if load1, load5, load15, err := readLoadAvg(); err == nil {
		writeMetric(b, "dplaneos_load_1", nil, load1, "1-minute load average")
		writeMetric(b, "dplaneos_load_5", nil, load5, "5-minute load average")
		writeMetric(b, "dplaneos_load_15", nil, load15, "15-minute load average")
	}

	// ── Inotify ────────────────────────────────────────────────────
	if used := readProcSysInt("/proc/sys/fs/inotify/max_user_watches"); used > 0 {
		writeMetric(b, "dplaneos_inotify_max_watches", nil, float64(used), "inotify max_user_watches kernel limit")
	}
	if count := countInotifyInstances(); count >= 0 {
		writeMetric(b, "dplaneos_inotify_active_instances", nil, float64(count), "Approximate active inotify instances")
	}

	// ── ZFS pool health (via zpool list) ───────────────────────────
	pools := readZpoolList()
	for _, pool := range pools {
		labels := map[string]string{"pool": pool.name}
		writeMetric(b, "dplaneos_zfs_pool_size_bytes", labels, pool.size, "ZFS pool total size")
		writeMetric(b, "dplaneos_zfs_pool_alloc_bytes", labels, pool.alloc, "ZFS pool allocated bytes")
		writeMetric(b, "dplaneos_zfs_pool_free_bytes", labels, pool.free, "ZFS pool free bytes")
		healthVal := 0.0
		if pool.health == "ONLINE" {
			healthVal = 1.0
		}
		writeMetric(b, "dplaneos_zfs_pool_healthy", labels, healthVal, "ZFS pool health (1=ONLINE, 0=degraded/faulted)")
	}

	fmt.Fprint(w, b.String())
}

// ─── Prometheus text format helpers ─────────────────────────────

func writeMetric(b *strings.Builder, name string, labels map[string]string, value float64, help string) {
	fmt.Fprintf(b, "# HELP %s %s\n", name, help)
	fmt.Fprintf(b, "# TYPE %s gauge\n", name)
	if len(labels) == 0 {
		fmt.Fprintf(b, "%s %g\n", name, value)
	} else {
		parts := make([]string, 0, len(labels))
		for k, v := range labels {
			parts = append(parts, fmt.Sprintf(`%s="%s"`, k, v))
		}
		fmt.Fprintf(b, "%s{%s} %g\n", name, strings.Join(parts, ","), value)
	}
}

func kbToBytes(kb float64) float64 { return kb * 1024 }

// ─── /proc readers ──────────────────────────────────────────────

// readMemInfo parses /proc/meminfo into key→kB map
func readMemInfo() map[string]float64 {
	result := make(map[string]float64)
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return result
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		parts := strings.Fields(line)
		if len(parts) >= 2 {
			key := strings.TrimSuffix(parts[0], ":")
			val, err := strconv.ParseFloat(parts[1], 64)
			if err == nil {
				result[key] = val
			}
		}
	}
	return result
}

// readSysfsUint64 reads a named field from a kstat-style /proc file
func readSysfsUint64(path, field string) uint64 {
	f, err := os.Open(path)
	if err != nil {
		return 0
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		parts := strings.Fields(line)
		// Format: "name type value"
		if len(parts) >= 3 && parts[0] == field {
			v, err := strconv.ParseUint(parts[2], 10, 64)
			if err == nil {
				return v
			}
		}
	}
	return 0
}

// readCPUStat returns iowait ticks and total ticks from /proc/stat
func readCPUStat() (iowait, total float64, err error) {
	f, err := os.Open("/proc/stat")
	if err != nil {
		return 0, 0, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "cpu ") {
			continue
		}
		parts := strings.Fields(line)
		// cpu user nice system idle iowait irq softirq steal guest guest_nice
		if len(parts) < 6 {
			break
		}
		for i := 1; i < len(parts); i++ {
			v, e := strconv.ParseFloat(parts[i], 64)
			if e != nil {
				continue
			}
			total += v
			if i == 5 { // iowait is index 5
				iowait = v
			}
		}
		return iowait, total, nil
	}
	return 0, 0, fmt.Errorf("cpu line not found")
}

// readLoadAvg reads /proc/loadavg
func readLoadAvg() (load1, load5, load15 float64, err error) {
	data, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return
	}
	parts := strings.Fields(string(data))
	if len(parts) < 3 {
		err = fmt.Errorf("unexpected format")
		return
	}
	load1, err = strconv.ParseFloat(parts[0], 64)
	if err != nil {
		return
	}
	load5, err = strconv.ParseFloat(parts[1], 64)
	if err != nil {
		return
	}
	load15, err = strconv.ParseFloat(parts[2], 64)
	return
}

// readProcSysInt reads a single integer from a /proc/sys file
func readProcSysInt(path string) int {
	data, err := os.ReadFile(path)
	if err != nil {
		return -1
	}
	v, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return -1
	}
	return v
}

// countInotifyInstances counts fd entries pointing to inotify (approximation)
func countInotifyInstances() int {
	entries, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		return -1
	}
	count := 0
	for _, e := range entries {
		link, err := os.Readlink("/proc/self/fd/" + e.Name())
		if err == nil && strings.Contains(link, "inotify") {
			count++
		}
	}
	return count
}

// zpoolStat holds parsed zpool list output for one pool
type zpoolStat struct {
	name   string
	size   float64
	alloc  float64
	free   float64
	health string
}

// readZpoolList runs zpool list and parses the output
func readZpoolList() []zpoolStat {
	out, err := executeCommandWithTimeout(TimeoutFast,
		"/run/current-system/sw/bin/zpool",
		[]string{"list", "-Hp", "-o", "name,size,alloc,free,health"})
	if err != nil {
		return nil
	}
	var pools []zpoolStat
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		parts := strings.Fields(line)
		if len(parts) != 5 {
			continue
		}
		p := zpoolStat{
			name:   parts[0],
			health: parts[4],
		}
		p.size, _ = strconv.ParseFloat(parts[1], 64)
		p.alloc, _ = strconv.ParseFloat(parts[2], 64)
		p.free, _ = strconv.ParseFloat(parts[3], 64)
		pools = append(pools, p)
	}
	return pools
}
