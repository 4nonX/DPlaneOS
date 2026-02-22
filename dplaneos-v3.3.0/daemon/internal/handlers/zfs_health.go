package handlers

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
)

// ZFSHealthHandler provides deep ZFS health monitoring and predictive analysis
type ZFSHealthHandler struct{}

func NewZFSHealthHandler() *ZFSHealthHandler {
	return &ZFSHealthHandler{}
}

// DiskHealth represents the health data for a single disk in a pool
type DiskHealth struct {
	Device       string `json:"device"`
	State        string `json:"state"`         // ONLINE, DEGRADED, FAULTED, OFFLINE
	ReadErrors   int64  `json:"read_errors"`
	WriteErrors  int64  `json:"write_errors"`
	ChecksumErrs int64  `json:"checksum_errors"`
	SlowIOs      int64  `json:"slow_ios"`
	Risk         string `json:"risk"`          // low, medium, high, critical
}

// PoolHealthDetail represents detailed health for a pool
type PoolHealthDetail struct {
	Name        string       `json:"name"`
	State       string       `json:"state"`
	ScanStatus  string       `json:"scan_status"`
	Errors      string       `json:"errors"`
	Disks       []DiskHealth `json:"disks"`
	OverallRisk string       `json:"overall_risk"`
}

// GetPoolHealth returns deep health analysis for all pools
// GET /api/zfs/health
func (h *ZFSHealthHandler) GetPoolHealth(w http.ResponseWriter, r *http.Request) {
	output, err := executeCommand("/usr/sbin/zpool", []string{"status", "-p"})
	if err != nil {
		respondOK(w, map[string]interface{}{
			"success": true,
			"pools":   []interface{}{},
			"error":   "Cannot read pool status",
		})
		return
	}

	pools := parsePoolHealth(output)

	respondOK(w, map[string]interface{}{
		"success": true,
		"pools":   pools,
		"count":   len(pools),
	})
}

// GetIOStats returns real-time I/O statistics per disk
// GET /api/zfs/iostat
func (h *ZFSHealthHandler) GetIOStats(w http.ResponseWriter, r *http.Request) {
	// One-shot iostat with parseable output
	output, err := executeCommand("/usr/sbin/zpool", []string{
		"iostat", "-p", "-H", "-l",
	})
	if err != nil {
		// Fallback to simpler format
		output, err = executeCommand("/usr/sbin/zpool", []string{"iostat", "-v"})
		if err != nil {
			respondOK(w, map[string]interface{}{
				"success": true,
				"stats":   []interface{}{},
			})
			return
		}
	}

	stats := parseIOStats(output)

	respondOK(w, map[string]interface{}{
		"success": true,
		"stats":   stats,
	})
}

// GetPoolEvents returns recent ZFS events (checksum errors, scrub results, etc.)
// GET /api/zfs/events?count=50
func (h *ZFSHealthHandler) GetPoolEvents(w http.ResponseWriter, r *http.Request) {
	countStr := r.URL.Query().Get("count")
	count := 50
	if countStr != "" {
		if n, err := strconv.Atoi(countStr); err == nil && n > 0 && n <= 500 {
			count = n
		}
	}

	output, err := executeCommand("/usr/sbin/zpool", []string{
		"events", "-v", "-c",
	})
	if err != nil {
		// zpool events not available on all systems
		respondOK(w, map[string]interface{}{
			"success": true,
			"events":  []interface{}{},
			"note":    "Pool events not available on this system",
		})
		return
	}

	events := parsePoolEvents(output, count)

	respondOK(w, map[string]interface{}{
		"success": true,
		"events":  events,
		"count":   len(events),
	})
}

// GetSMARTHealth returns S.M.A.R.T. data for all disks
// GET /api/zfs/smart
func (h *ZFSHealthHandler) GetSMARTHealth(w http.ResponseWriter, r *http.Request) {
	// Get list of disks from zpool
	zpoolOutput, err := executeCommand("/usr/sbin/zpool", []string{"status"})
	if err != nil {
		respondOK(w, map[string]interface{}{
			"success": true,
			"disks":   []interface{}{},
		})
		return
	}

	disks := extractDiskDevices(zpoolOutput)
	var smartData []map[string]interface{}

	for _, disk := range disks {
		devicePath := "/dev/" + disk
		output, err := executeCommand("/usr/sbin/smartctl", []string{
			"-a", "-j", devicePath,
		})
		if err != nil {
			smartData = append(smartData, map[string]interface{}{
				"device": disk,
				"error":  "SMART data unavailable",
			})
			continue
		}

		var smartJSON map[string]interface{}
		if err := json.Unmarshal([]byte(output), &smartJSON); err != nil {
			smartData = append(smartData, map[string]interface{}{
				"device":  disk,
				"raw":     output,
			})
			continue
		}

		// Extract key health indicators
		result := map[string]interface{}{
			"device": disk,
		}

		if health, ok := smartJSON["smart_status"]; ok {
			result["smart_status"] = health
		}
		if temp, ok := smartJSON["temperature"]; ok {
			result["temperature"] = temp
			// NVMe/SSD temperature warnings
			if tempMap, ok := temp.(map[string]interface{}); ok {
				if current, ok := tempMap["current"].(float64); ok {
					if current >= 70 {
						result["temp_warning"] = "critical"
					} else if current >= 55 {
						result["temp_warning"] = "high"
					} else {
						result["temp_warning"] = "normal"
					}
				}
			}
		}
		if powerOn, ok := smartJSON["power_on_time"]; ok {
			result["power_on_time"] = powerOn
		}
		if attrs, ok := smartJSON["ata_smart_attributes"]; ok {
			result["attributes"] = attrs
		}
		// NVMe-specific health info
		if nvmeHealth, ok := smartJSON["nvme_smart_health_information_log"]; ok {
			result["nvme_health"] = nvmeHealth
		}

		smartData = append(smartData, result)
	}

	respondOK(w, map[string]interface{}{
		"success": true,
		"disks":   smartData,
		"count":   len(smartData),
	})
}

// parsePoolHealth parses `zpool status -p` output into structured health data
func parsePoolHealth(output string) []PoolHealthDetail {
	var pools []PoolHealthDetail
	var current *PoolHealthDetail

	lines := strings.Split(output, "\n")
	inConfig := false

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)

		if strings.HasPrefix(trimmed, "pool:") {
			if current != nil {
				current.OverallRisk = calculatePoolRisk(current)
				pools = append(pools, *current)
			}
			current = &PoolHealthDetail{
				Name: strings.TrimSpace(strings.TrimPrefix(trimmed, "pool:")),
			}
			inConfig = false
		} else if current != nil {
			if strings.HasPrefix(trimmed, "state:") {
				current.State = strings.TrimSpace(strings.TrimPrefix(trimmed, "state:"))
			} else if strings.HasPrefix(trimmed, "scan:") {
				current.ScanStatus = strings.TrimSpace(strings.TrimPrefix(trimmed, "scan:"))
			} else if strings.HasPrefix(trimmed, "errors:") {
				current.Errors = strings.TrimSpace(strings.TrimPrefix(trimmed, "errors:"))
			} else if strings.HasPrefix(trimmed, "NAME") && strings.Contains(trimmed, "STATE") {
				inConfig = true
			} else if inConfig && trimmed != "" && !strings.HasPrefix(trimmed, "pool:") {
				disk := parseDiskLine(trimmed)
				if disk != nil {
					current.Disks = append(current.Disks, *disk)
				}
			}
		}
	}
	if current != nil {
		current.OverallRisk = calculatePoolRisk(current)
		pools = append(pools, *current)
	}

	return pools
}

// parseDiskLine parses a single disk line from zpool status
func parseDiskLine(line string) *DiskHealth {
	fields := strings.Fields(line)
	if len(fields) < 5 {
		return nil
	}

	// Skip non-disk entries (pool name, mirror, raidz, etc.)
	name := fields[0]
	state := fields[1]

	readErr, _ := strconv.ParseInt(fields[2], 10, 64)
	writeErr, _ := strconv.ParseInt(fields[3], 10, 64)
	ckErr, _ := strconv.ParseInt(fields[4], 10, 64)

	var slowIO int64
	if len(fields) > 5 {
		slowIO, _ = strconv.ParseInt(fields[5], 10, 64)
	}

	risk := "low"
	totalErrors := readErr + writeErr + ckErr
	if state == "FAULTED" || state == "UNAVAIL" {
		risk = "critical"
	} else if state == "DEGRADED" || totalErrors > 100 {
		risk = "high"
	} else if totalErrors > 10 || ckErr > 0 {
		// Any checksum error is concerning — ZFS is detecting silent corruption
		risk = "medium"
	} else if totalErrors > 0 {
		risk = "low"
	}

	return &DiskHealth{
		Device:       name,
		State:        state,
		ReadErrors:   readErr,
		WriteErrors:  writeErr,
		ChecksumErrs: ckErr,
		SlowIOs:      slowIO,
		Risk:         risk,
	}
}

// calculatePoolRisk determines overall pool risk level
func calculatePoolRisk(pool *PoolHealthDetail) string {
	if pool.State == "FAULTED" || pool.State == "UNAVAIL" {
		return "critical"
	}
	if pool.State == "DEGRADED" {
		return "high"
	}

	maxRisk := "low"
	for _, disk := range pool.Disks {
		if disk.Risk == "critical" {
			return "critical"
		}
		if disk.Risk == "high" && maxRisk != "critical" {
			maxRisk = "high"
		}
		if disk.Risk == "medium" && maxRisk == "low" {
			maxRisk = "medium"
		}
	}

	// Check for checksum errors specifically — early warning sign
	var totalCkErrors int64
	for _, disk := range pool.Disks {
		totalCkErrors += disk.ChecksumErrs
	}
	if totalCkErrors > 0 && maxRisk == "low" {
		maxRisk = "medium"
	}

	return maxRisk
}

// parseIOStats parses zpool iostat output
func parseIOStats(output string) []map[string]interface{} {
	var stats []map[string]interface{}
	lines := strings.Split(strings.TrimSpace(output), "\n")

	for _, line := range lines {
		if line == "" || strings.HasPrefix(line, "-") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) >= 6 {
			stats = append(stats, map[string]interface{}{
				"device":    fields[0],
				"alloc":     fields[1],
				"free":      fields[2],
				"read_ops":  fields[3],
				"write_ops": fields[4],
				"read_bw":   fields[5],
			})
		}
	}
	return stats
}

// parsePoolEvents parses zpool events output
func parsePoolEvents(output string, maxCount int) []map[string]string {
	var events []map[string]string
	lines := strings.Split(strings.TrimSpace(output), "\n")

	for _, line := range lines {
		if line == "" {
			continue
		}
		// Events are typically in format: "timestamp class payload"
		events = append(events, map[string]string{
			"raw": strings.TrimSpace(line),
		})
		if len(events) >= maxCount {
			break
		}
	}
	return events
}

// extractDiskDevices extracts disk device names from zpool status output
func extractDiskDevices(output string) []string {
	var disks []string
	lines := strings.Split(output, "\n")
	inConfig := false

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "NAME") && strings.Contains(trimmed, "STATE") {
			inConfig = true
			continue
		}
		if inConfig && trimmed == "" {
			inConfig = false
			continue
		}
		if inConfig {
			fields := strings.Fields(trimmed)
			if len(fields) >= 2 {
				name := fields[0]
				// Only include actual disk devices (sd*, nvme*, etc.)
				if strings.HasPrefix(name, "sd") || strings.HasPrefix(name, "nvme") ||
					strings.HasPrefix(name, "hd") || strings.HasPrefix(name, "vd") ||
					strings.HasPrefix(name, "xvd") || strings.HasPrefix(name, "da") {
					disks = append(disks, name)
				}
			}
		}
	}
	return disks
}
