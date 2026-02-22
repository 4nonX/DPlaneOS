package handlers

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// CapacityGuardianHandler monitors and protects pool capacity
type CapacityGuardianHandler struct {
	// Thresholds are configurable — different setups have different needs
	// A 1TB home NAS might want warnings at 85%, a 100TB array at 92%
	WarningPct   float64 // default: 80
	CriticalPct  float64 // default: 90
	EmergencyPct float64 // default: 95
	ReservePct   float64 // default: 2 (percentage of pool to reserve)
}

func NewCapacityGuardianHandler() *CapacityGuardianHandler {
	return &CapacityGuardianHandler{
		WarningPct:   80,
		CriticalPct:  90,
		EmergencyPct: 95,
		ReservePct:   2,
	}
}

// PoolCapacityStatus represents the capacity state of a pool
type PoolCapacityStatus struct {
	Pool       string  `json:"pool"`
	UsedPct    float64 `json:"used_percent"`
	UsedBytes  string  `json:"used"`
	FreeBytes  string  `json:"free"`
	TotalBytes string  `json:"total"`
	State      string  `json:"state"`       // ok, warning, critical, emergency
	Reserved   string  `json:"reserved"`    // amount of slop space reserved
	HasReserve bool    `json:"has_reserve"` // whether a reserve dataset exists
}

// capacityState returns the state string for a given usage percentage
func (h *CapacityGuardianHandler) capacityState(pct float64) string {
	if pct >= h.EmergencyPct {
		return "emergency"
	} else if pct >= h.CriticalPct {
		return "critical"
	} else if pct >= h.WarningPct {
		return "warning"
	}
	return "ok"
}

// GetCapacityStatus returns capacity status for all pools with risk assessment
// GET /api/zfs/capacity
func (h *CapacityGuardianHandler) GetCapacityStatus(w http.ResponseWriter, r *http.Request) {
	output, err := executeCommandWithTimeout(TimeoutFast, "/usr/sbin/zpool", []string{
		"list", "-Hp", "-o", "name,size,alloc,free,capacity",
	})
	if err != nil {
		respondOK(w, map[string]interface{}{
			"success": true,
			"pools":   []interface{}{},
			"error":   "Cannot read pool capacity",
		})
		return
	}

	var pools []PoolCapacityStatus
	lines := strings.Split(strings.TrimSpace(output), "\n")
	for _, line := range lines {
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 5 {
			continue
		}

		capacity, _ := strconv.ParseFloat(fields[4], 64)

		state := h.capacityState(capacity)

		// Check if reserve dataset exists
		poolName := fields[0]
		hasReserve := checkReserveExists(poolName)
		reserveSize := "none"
		if hasReserve {
			if rOut, err := executeCommandWithTimeout(TimeoutFast, "/usr/sbin/zfs", []string{
				"get", "-Hp", "-o", "value", "reservation", poolName + "/dplane-reserved",
			}); err == nil {
				reserveSize = strings.TrimSpace(rOut)
			}
		}

		pools = append(pools, PoolCapacityStatus{
			Pool:       poolName,
			UsedPct:    capacity,
			UsedBytes:  fields[2],
			FreeBytes:  fields[3],
			TotalBytes: fields[1],
			State:      state,
			Reserved:   reserveSize,
			HasReserve: hasReserve,
		})
	}

	respondOK(w, map[string]interface{}{
		"success": true,
		"pools":   pools,
		"count":   len(pools),
	})
}

// SetupReserve creates a 2% emergency reserve on a pool
// POST /api/zfs/capacity/reserve { "pool": "tank" }
func (h *CapacityGuardianHandler) SetupReserve(w http.ResponseWriter, r *http.Request) {
	pool := r.URL.Query().Get("pool")
	if pool == "" || !isValidDataset(pool) {
		respondErrorSimple(w, "Invalid pool name", http.StatusBadRequest)
		return
	}

	// Get pool size
	output, err := executeCommandWithTimeout(TimeoutFast, "/usr/sbin/zpool", []string{
		"list", "-Hp", "-o", "size", pool,
	})
	if err != nil {
		respondOK(w, map[string]interface{}{
			"success": false,
			"error":   "Cannot read pool size",
		})
		return
	}

	totalBytes, _ := strconv.ParseInt(strings.TrimSpace(output), 10, 64)
	if totalBytes == 0 {
		respondOK(w, map[string]interface{}{
			"success": false,
			"error":   "Cannot determine pool size",
		})
		return
	}

	// Reserve configured percentage of total pool size (default 2%)
	reserveBytes := int64(float64(totalBytes) * h.ReservePct / 100.0)
	reserveStr := fmt.Sprintf("%d", reserveBytes)

	reserveDataset := pool + "/dplane-reserved"

	// Create the reserve dataset if it doesn't exist
	if !checkReserveExists(pool) {
		_, err := executeCommandWithTimeout(TimeoutMedium, "/usr/sbin/zfs", []string{
			"create", reserveDataset,
		})
		if err != nil {
			respondOK(w, map[string]interface{}{
				"success": false,
				"error":   fmt.Sprintf("Cannot create reserve dataset: %v", err),
			})
			return
		}
	}

	// Set the reservation
	_, err = executeCommandWithTimeout(TimeoutMedium, "/usr/sbin/zfs", []string{
		"set", fmt.Sprintf("reservation=%s", reserveStr), reserveDataset,
	})
	if err != nil {
		respondOK(w, map[string]interface{}{
			"success": false,
			"error":   fmt.Sprintf("Cannot set reservation: %v", err),
		})
		return
	}

	respondOK(w, map[string]interface{}{
		"success":        true,
		"pool":           pool,
		"reserve_bytes":  reserveBytes,
		"reserve_human":  humanizeBytes(reserveBytes),
		"reserve_pct":    2.0,
	})
}

// ReleaseReserve drops the emergency reserve (when pool is at 95%+, frees space for cleanup)
// POST /api/zfs/capacity/release { "pool": "tank" }
func (h *CapacityGuardianHandler) ReleaseReserve(w http.ResponseWriter, r *http.Request) {
	pool := r.URL.Query().Get("pool")
	if pool == "" || !isValidDataset(pool) {
		respondErrorSimple(w, "Invalid pool name", http.StatusBadRequest)
		return
	}

	reserveDataset := pool + "/dplane-reserved"

	// Remove reservation first
	executeCommandWithTimeout(TimeoutMedium, "/usr/sbin/zfs", []string{
		"set", "reservation=none", reserveDataset,
	})

	// Destroy the reserve dataset to free its space
	_, err := executeCommandWithTimeout(TimeoutMedium, "/usr/sbin/zfs", []string{
		"destroy", reserveDataset,
	})
	if err != nil {
		respondOK(w, map[string]interface{}{
			"success": false,
			"error":   fmt.Sprintf("Cannot release reserve: %v", err),
		})
		return
	}

	respondOK(w, map[string]interface{}{
		"success": true,
		"message": fmt.Sprintf("Emergency reserve released on %s — you now have extra space for cleanup", pool),
	})
}

// checkReserveExists checks if the reserve dataset exists
func checkReserveExists(pool string) bool {
	_, err := executeCommandWithTimeout(TimeoutFast, "/usr/sbin/zfs", []string{
		"list", "-H", pool + "/dplane-reserved",
	})
	return err == nil
}

// humanizeBytes converts bytes to human-readable format
func humanizeBytes(bytes int64) string {
	const (
		KB = 1024
		MB = KB * 1024
		GB = MB * 1024
		TB = GB * 1024
	)
	switch {
	case bytes >= TB:
		return fmt.Sprintf("%.1f TB", float64(bytes)/float64(TB))
	case bytes >= GB:
		return fmt.Sprintf("%.1f GB", float64(bytes)/float64(GB))
	case bytes >= MB:
		return fmt.Sprintf("%.1f MB", float64(bytes)/float64(MB))
	default:
		return fmt.Sprintf("%d bytes", bytes)
	}
}

// RunCapacityCheck is called periodically by the daemon to check all pools
// Returns pools that need attention
func RunCapacityCheck(warningPct, criticalPct, emergencyPct float64) []PoolCapacityStatus {
	output, _ := executeCommandWithTimeout(TimeoutFast, "/usr/sbin/zpool", []string{
		"list", "-Hp", "-o", "name,capacity",
	})
	if output == "" {
		return nil
	}

	var alerts []PoolCapacityStatus
	for _, line := range strings.Split(strings.TrimSpace(output), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		pct, _ := strconv.ParseFloat(fields[1], 64)
		if pct >= warningPct {
			state := "warning"
			if pct >= emergencyPct {
				state = "emergency"
			} else if pct >= criticalPct {
				state = "critical"
			}
			alerts = append(alerts, PoolCapacityStatus{
				Pool:    fields[0],
				UsedPct: pct,
				State:   state,
			})
		}
	}
	return alerts
}

// StartCapacityMonitor runs a background goroutine that checks capacity every 5 minutes
// Uses default thresholds — the handler instance thresholds are for API responses
func StartCapacityMonitor() {
	go func() {
		ticker := time.NewTicker(5 * time.Minute)
		defer ticker.Stop()
		for range ticker.C {
			alerts := RunCapacityCheck(80, 90, 95)
			for _, a := range alerts {
				if a.State == "emergency" {
					// Auto-release reserve if pool is at emergency level
					if checkReserveExists(a.Pool) {
						executeCommandWithTimeout(TimeoutMedium, "/usr/sbin/zfs", []string{
							"set", "reservation=none", a.Pool + "/dplane-reserved",
						})
						executeCommandWithTimeout(TimeoutMedium, "/usr/sbin/zfs", []string{
							"destroy", a.Pool + "/dplane-reserved",
						})
					}
				}
			}
		}
	}()
}
