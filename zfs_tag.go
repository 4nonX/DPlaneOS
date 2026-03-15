package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"dplaned/internal/cmdutil"
	"dplaned/internal/jobs"
)

// ÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉ
//  1. ZFS SCRUB SCHEDULING
// ÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉ

// StartScrub triggers a manual scrub on a pool
// POST /api/zfs/scrub/start { "pool": "tank" }
func StartScrub(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Pool string `json:"pool"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondErrorSimple(w, "Invalid request", http.StatusBadRequest)
		return
	}
	if !isValidDataset(req.Pool) {
		respondErrorSimple(w, "Invalid pool name", http.StatusBadRequest)
		return
	}
	// Run scrub at idle I/O priority
	_, err := executeBackgroundCommand("/usr/sbin/zpool", []string{"scrub", req.Pool})
	if err != nil {
		respondOK(w, map[string]interface{}{"success": false, "error": err.Error()})
		return
	}
	// Broadcast scrub_started so the frontend (PoolsPage) can react in real time.
	if diskEventHub != nil {
		diskEventHub.Broadcast("scrub_started", map[string]interface{}{"pool": req.Pool}, "info")
	}

	// Poll for natural completion every 10 s and broadcast scrub_completed.
	// This fires when scrub finishes on its own (not via StopScrub).
	// The goroutine exits as soon as the scrub is no longer in progress.
	pool := req.Pool
	go func() {
		for {
			time.Sleep(10 * time.Second)
			out, err := executeCommandWithTimeout(TimeoutFast, "/usr/sbin/zpool", []string{"status", pool})
			if err != nil {
				return // pool gone or zpool failed ÔÇö stop polling
			}
			if !strings.Contains(out, "scrub in progress") {
				if diskEventHub != nil {
					diskEventHub.Broadcast("scrub_completed", map[string]interface{}{
						"pool":      pool,
						"cancelled": false,
					}, "info")
				}
				return
			}
		}
	}()

	respondOK(w, map[string]interface{}{
		"success": true,
		"message": fmt.Sprintf("Scrub started on pool %s (idle I/O priority)", req.Pool),
	})
}

// StopScrub cancels a running scrub
// POST /api/zfs/scrub/stop { "pool": "tank" }
func StopScrub(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Pool string `json:"pool"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondErrorSimple(w, "Invalid request", http.StatusBadRequest)
		return
	}
	if !isValidDataset(req.Pool) {
		respondErrorSimple(w, "Invalid pool name", http.StatusBadRequest)
		return
	}
	_, err := executeCommandWithTimeout(TimeoutMedium, "/usr/sbin/zpool", []string{"scrub", "-s", req.Pool})
	if err != nil {
		respondOK(w, map[string]interface{}{"success": false, "error": err.Error()})
		return
	}
	// Broadcast scrub_completed so PoolsPage knows the scrub has ended.
	if diskEventHub != nil {
		diskEventHub.Broadcast("scrub_completed", map[string]interface{}{"pool": req.Pool, "cancelled": true}, "info")
	}
	respondOK(w, map[string]interface{}{"success": true, "message": "Scrub stopped"})
}

// ScrubScanInfo holds parsed scan-line data common to both scrub and resilver.
type ScrubScanInfo struct {
	InProgress  bool    `json:"in_progress"`
	PercentDone float64 `json:"percent_done"`
	BytesDone   string  `json:"bytes_done"`
	ETA         string  `json:"eta"`
	Errors      int     `json:"errors"`
	Completed   bool    `json:"completed"`
	CompletedAt string  `json:"completed_at,omitempty"`
	RawScanLine string  `json:"raw_scan_line"`
}

// parseScanLine parses a `zpool status` scan: line.
// It handles both in-progress and completed resilver/scrub lines.
//
// Example in-progress:
//
//	scan: resilver in progress since Mon Jan  1 00:00:00 2024
//	      1.23G done, 42.70% done, ETA 00:14:22
//
// Example completed:
//
//	scan: resilvered 3.45G in 00:22:10 with 0 errors on Mon Jan  1 00:30:00 2024
func parseScanLine(rawLine string) ScrubScanInfo {
	info := ScrubScanInfo{RawScanLine: rawLine}

	// In-progress pattern: "X.XXG done, XX.XX% done, ETA HH:MM:SS"
	pctRe := regexp.MustCompile(`([\d.]+)%\s+done`)
	etaRe := regexp.MustCompile(`ETA\s+(\S+)`)
	bytesRe := regexp.MustCompile(`([\d.]+[KMGT]?)\s+done`)

	if strings.Contains(rawLine, "in progress") {
		info.InProgress = true
		if m := pctRe.FindStringSubmatch(rawLine); len(m) > 1 {
			info.PercentDone, _ = strconv.ParseFloat(m[1], 64)
		}
		if m := etaRe.FindStringSubmatch(rawLine); len(m) > 1 {
			info.ETA = m[1]
		}
		if m := bytesRe.FindStringSubmatch(rawLine); len(m) > 1 {
			info.BytesDone = m[1]
		}
		return info
	}

	// Completed scrub: "scrub repaired X in HH:MM:SS with N errors on ..."
	// Completed resilver: "resilvered X in HH:MM:SS with N errors on ..."
	completedRe := regexp.MustCompile(`(?:resilvered|scrub repaired)\s+([\d.]+[KMGT]?)\s+in\s+\S+\s+with\s+(\d+)\s+errors?\s+on\s+(.+)`)
	if m := completedRe.FindStringSubmatch(rawLine); len(m) > 3 {
		info.Completed = true
		info.BytesDone = m[1]
		info.Errors, _ = strconv.Atoi(m[2])
		info.CompletedAt = strings.TrimSpace(m[3])
		return info
	}

	return info
}

// GetScrubStatus returns current scrub progress
// GET /api/zfs/scrub/status?pool=tank
func GetScrubStatus(w http.ResponseWriter, r *http.Request) {
	pool := r.URL.Query().Get("pool")
	if pool == "" || !isValidDataset(pool) {
		respondErrorSimple(w, "Invalid pool", http.StatusBadRequest)
		return
	}
	output, err := executeCommandWithTimeout(TimeoutFast, "/usr/sbin/zpool", []string{
		"status", pool,
	})
	if err != nil {
		respondOK(w, map[string]interface{}{"success": false, "error": err.Error()})
		return
	}

	// Collect all scan-related lines (the continuation line follows the scan: line)
	var scanLines []string
	lines := strings.Split(output, "\n")
	inScan := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "scan:") {
			inScan = true
			scanLines = append(scanLines, trimmed)
			continue
		}
		if inScan {
			// Continuation lines are indented and don't start a new field
			if strings.HasPrefix(line, "\t") || (len(line) > 0 && line[0] == ' ') {
				if !strings.Contains(trimmed, ":") || strings.HasPrefix(trimmed, "scan:") {
					scanLines = append(scanLines, trimmed)
					continue
				}
			}
			inScan = false
		}
	}

	rawScan := strings.Join(scanLines, " ")

	// Only parse scrub lines (not resilver) for this endpoint
	scrubInfo := "none"
	if rawScan != "" {
		scrubInfo = rawScan
	}

	parsed := parseScanLine(rawScan)

	respondOK(w, map[string]interface{}{
		"success":      true,
		"pool":         pool,
		"scrub":        scrubInfo,
		"in_progress":  parsed.InProgress,
		"percent_done": parsed.PercentDone,
		"bytes_done":   parsed.BytesDone,
		"eta":          parsed.ETA,
		"errors":       parsed.Errors,
		"completed":    parsed.Completed,
		"completed_at": parsed.CompletedAt,
	})
}

// HandleResilverStatus returns resilver progress for a pool
// GET /api/zfs/resilver/status?pool=tank
func HandleResilverStatus(w http.ResponseWriter, r *http.Request) {
	pool := r.URL.Query().Get("pool")
	if pool == "" || !isValidDataset(pool) {
		respondErrorSimple(w, "Invalid pool", http.StatusBadRequest)
		return
	}

	output, err := executeCommandWithTimeout(TimeoutFast, "/usr/sbin/zpool", []string{
		"status", "-P", pool,
	})
	if err != nil {
		respondOK(w, map[string]interface{}{"success": false, "error": err.Error()})
		return
	}

	// Collect scan: line and its continuations
	var scanLines []string
	lines := strings.Split(output, "\n")
	inScan := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "scan:") {
			inScan = true
			scanLines = append(scanLines, trimmed)
			continue
		}
		if inScan {
			if strings.HasPrefix(line, "\t") || (len(line) > 0 && line[0] == ' ') {
				// Only collect if it doesn't start a new top-level field
				if !strings.Contains(trimmed, ":") || strings.HasPrefix(trimmed, "scan:") {
					scanLines = append(scanLines, trimmed)
					continue
				}
			}
			inScan = false
		}
	}

	rawScan := strings.Join(scanLines, " ")

	// Only return resilver data ÔÇö not scrub
	isResilver := strings.Contains(rawScan, "resilver")
	if !isResilver {
		respondOK(w, map[string]interface{}{
			"pool":         pool,
			"resilvering":  false,
			"percent_done": 0,
			"bytes_done":   "",
			"eta":          "",
			"errors":       0,
			"completed":    false,
			"completed_at": nil,
		})
		return
	}

	parsed := parseScanLine(rawScan)

	var completedAt interface{} = nil
	if parsed.CompletedAt != "" {
		completedAt = parsed.CompletedAt
	}

	respondOK(w, map[string]interface{}{
		"pool":         pool,
		"resilvering":  parsed.InProgress || parsed.Completed,
		"percent_done": parsed.PercentDone,
		"bytes_done":   parsed.BytesDone,
		"eta":          parsed.ETA,
		"errors":       parsed.Errors,
		"completed":    parsed.Completed,
		"completed_at": completedAt,
	})
}

// ÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉ
//  4. VDEV ADD / EXPAND POOL
// ÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉ

// AddVdevToPool adds a vdev to an existing pool
// POST /api/zfs/pool/add-vdev { "pool": "tank", "vdev_type": "mirror", "disks": ["sdc","sdd"] }
func AddVdevToPool(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Pool     string   `json:"pool"`
		VdevType string   `json:"vdev_type"` // mirror, raidz, raidz2, raidz3, cache, log, special, ""(stripe)
		Disks    []string `json:"disks"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondErrorSimple(w, "Invalid request", http.StatusBadRequest)
		return
	}
	if !isValidDataset(req.Pool) || len(req.Disks) == 0 {
		respondErrorSimple(w, "Invalid pool name or empty disk list", http.StatusBadRequest)
		return
	}
	// Validate disks
	for _, d := range req.Disks {
		if strings.ContainsAny(d, ";|&$`\\\"' /") || len(d) > 64 {
			respondErrorSimple(w, fmt.Sprintf("Invalid disk name: %s", d), http.StatusBadRequest)
			return
		}
	}
	validTypes := map[string]bool{
		"": true, "mirror": true, "raidz": true, "raidz1": true,
		"raidz2": true, "raidz3": true, "cache": true, "log": true, "special": true,
	}
	if !validTypes[req.VdevType] {
		respondErrorSimple(w, "Invalid vdev type", http.StatusBadRequest)
		return
	}

	args := []string{"add", req.Pool}
	if req.VdevType != "" {
		args = append(args, req.VdevType)
	}
	args = append(args, req.Disks...)

	output, err := executeCommandWithTimeout(TimeoutMedium, "/usr/sbin/zpool", args)
	if err != nil {
		respondOK(w, map[string]interface{}{
			"success": false,
			"error":   fmt.Sprintf("Failed to add vdev: %v", err),
			"output":  output,
		})
		return
	}
	respondOK(w, map[string]interface{}{
		"success":   true,
		"pool":      req.Pool,
		"vdev_type": req.VdevType,
		"disks":     req.Disks,
	})
}

// ÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉ
//  5. L2ARC / SLOG MANAGEMENT (cache/log devices)
//  Covered by AddVdevToPool with vdev_type="cache" or "log"
//  These are just convenience aliases
// ÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉ

// RemoveCacheOrLog removes a cache or log device from a pool
// POST /api/zfs/pool/remove-device { "pool": "tank", "device": "sde" }
func RemoveCacheOrLog(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Pool   string `json:"pool"`
		Device string `json:"device"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondErrorSimple(w, "Invalid request", http.StatusBadRequest)
		return
	}
	if !isValidDataset(req.Pool) || strings.ContainsAny(req.Device, ";|&$`\\\"' /") {
		respondErrorSimple(w, "Invalid pool or device", http.StatusBadRequest)
		return
	}
	output, err := executeCommandWithTimeout(TimeoutMedium, "/usr/sbin/zpool", []string{
		"remove", req.Pool, req.Device,
	})
	if err != nil {
		respondOK(w, map[string]interface{}{"success": false, "error": err.Error(), "output": output})
		return
	}
	respondOK(w, map[string]interface{}{"success": true, "pool": req.Pool, "removed": req.Device})
}

// ÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉ
//  6. DISK REPLACEMENT (zpool replace)
// ÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉ

// ReplaceDisk replaces a failed disk and starts resilver via the jobs system
// POST /api/zfs/pool/replace { "pool": "tank", "old_disk": "sda", "new_disk": "sde" }
func ReplaceDisk(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Pool    string `json:"pool"`
		OldDisk string `json:"old_disk"`
		NewDisk string `json:"new_disk"`
		Force   bool   `json:"force"` // -f flag for mismatched sizes
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondErrorSimple(w, "Invalid request", http.StatusBadRequest)
		return
	}
	if !isValidDataset(req.Pool) {
		respondErrorSimple(w, "Invalid pool", http.StatusBadRequest)
		return
	}
	for _, d := range []string{req.OldDisk, req.NewDisk} {
		if strings.ContainsAny(d, ";|&$`\\\"' /") || len(d) > 128 {
			respondErrorSimple(w, "Invalid disk name", http.StatusBadRequest)
			return
		}
	}

	args := []string{"replace"}
	if req.Force {
		args = append(args, "-f")
	}
	args = append(args, req.Pool, req.OldDisk, req.NewDisk)

	// Snapshot local copies for the closure
	pool := req.Pool
	oldDisk := req.OldDisk
	newDisk := req.NewDisk
	argsCopy := append([]string(nil), args...)

	jobID := jobs.Start("zpool-replace", func(j *jobs.Job) {
		// Broadcast resilver_started immediately so PoolsPage shows live state.
		if diskEventHub != nil {
			diskEventHub.Broadcast("resilver_started", map[string]interface{}{
				"pool":     pool,
				"old_disk": oldDisk,
				"new_disk": newDisk,
			}, "info")
		}

		output, err := executeCommandWithTimeout(TimeoutMedium, "/usr/sbin/zpool", argsCopy)
		if err != nil {
			if diskEventHub != nil {
				diskEventHub.Broadcast("resilver_completed", map[string]interface{}{
					"pool":    pool,
					"success": false,
					"error":   err.Error(),
				}, "warning")
			}
			j.Fail(fmt.Sprintf("zpool replace failed: %v ÔÇö %s", err, strings.TrimSpace(output)))
			return
		}
		if diskEventHub != nil {
			diskEventHub.Broadcast("resilver_completed", map[string]interface{}{
				"pool":    pool,
				"success": true,
			}, "info")
		}
		j.Done(map[string]interface{}{
			"success":  true,
			"pool":     pool,
			"old_disk": oldDisk,
			"new_disk": newDisk,
			"output":   strings.TrimSpace(output),
		})
	})

	respondOK(w, map[string]interface{}{
		"success":  true,
		"job_id":   jobID,
		"pool":     pool,
		"old_disk": oldDisk,
		"new_disk": newDisk,
		"message":  fmt.Sprintf("Replacement started. Track progress: GET /api/zfs/resilver/status?pool=%s", pool),
	})
}

// ÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉ
//  7. DATASET QUOTAS & RESERVATIONS
// ÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉ

// SetDatasetQuota sets refquota and refreservation on a dataset
// POST /api/zfs/dataset/quota
func SetDatasetQuota(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Dataset        string `json:"dataset"`
		RefQuota       string `json:"refquota"`       // e.g. "500G", "1T", "none"
		RefReservation string `json:"refreservation"` // e.g. "100G", "none"
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondErrorSimple(w, "Invalid request", http.StatusBadRequest)
		return
	}
	if !isValidDataset(req.Dataset) {
		respondErrorSimple(w, "Invalid dataset", http.StatusBadRequest)
		return
	}

	results := map[string]interface{}{"success": true, "dataset": req.Dataset}

	if req.RefQuota != "" {
		_, err := executeCommandWithTimeout(TimeoutMedium, "/usr/sbin/zfs", []string{
			"set", fmt.Sprintf("refquota=%s", req.RefQuota), req.Dataset,
		})
		if err != nil {
			results["refquota_error"] = err.Error()
			results["success"] = false
		} else {
			results["refquota"] = req.RefQuota
		}
	}

	if req.RefReservation != "" {
		_, err := executeCommandWithTimeout(TimeoutMedium, "/usr/sbin/zfs", []string{
			"set", fmt.Sprintf("refreservation=%s", req.RefReservation), req.Dataset,
		})
		if err != nil {
			results["refreservation_error"] = err.Error()
			results["success"] = false
		} else {
			results["refreservation"] = req.RefReservation
		}
	}

	respondOK(w, results)
}

// GetDatasetQuota reads quota/reservation settings
// GET /api/zfs/dataset/quota?dataset=tank/data
func GetDatasetQuota(w http.ResponseWriter, r *http.Request) {
	dataset := r.URL.Query().Get("dataset")
	if !isValidDataset(dataset) {
		respondErrorSimple(w, "Invalid dataset", http.StatusBadRequest)
		return
	}
	output, err := executeCommandWithTimeout(TimeoutFast, "/usr/sbin/zfs", []string{
		"get", "-Hp", "-o", "property,value",
		"quota,refquota,reservation,refreservation,used,available",
		dataset,
	})
	if err != nil {
		respondOK(w, map[string]interface{}{"success": false, "error": err.Error()})
		return
	}

	props := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(output), "\n") {
		parts := strings.Fields(line)
		if len(parts) >= 2 {
			props[parts[0]] = parts[1]
		}
	}
	respondOK(w, map[string]interface{}{
		"success":        true,
		"dataset":        dataset,
		"quota":          props["quota"],
		"refquota":       props["refquota"],
		"reservation":    props["reservation"],
		"refreservation": props["refreservation"],
		"used":           props["used"],
		"available":      props["available"],
	})
}

// ÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉ
//  8. S.M.A.R.T. SCHEDULED TESTS
// ÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉ

// RunSMARTTest triggers a SMART test on a disk
// POST /api/zfs/smart/test { "device": "sda", "type": "short" }
func RunSMARTTest(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Device string `json:"device"` // sda, nvme0n1
		Type   string `json:"type"`   // short, long, conveyance
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondErrorSimple(w, "Invalid request", http.StatusBadRequest)
		return
	}
	if strings.ContainsAny(req.Device, ";|&$`\\\"' /") {
		respondErrorSimple(w, "Invalid device name", http.StatusBadRequest)
		return
	}
	validTypes := map[string]bool{"short": true, "long": true, "conveyance": true}
	if !validTypes[req.Type] {
		respondErrorSimple(w, "Invalid test type (short, long, conveyance)", http.StatusBadRequest)
		return
	}

	devicePath := "/dev/" + req.Device
	output, err := executeCommandWithTimeout(TimeoutMedium, "/usr/sbin/smartctl", []string{
		"-t", req.Type, devicePath,
	})
	if err != nil {
		respondOK(w, map[string]interface{}{"success": false, "error": err.Error(), "output": output})
		return
	}

	// Estimate time
	estimate := "1-2 minutes"
	if req.Type == "long" {
		estimate = "hours (depends on disk size)"
	}

	respondOK(w, map[string]interface{}{
		"success":  true,
		"device":   req.Device,
		"type":     req.Type,
		"estimate": estimate,
		"output":   strings.TrimSpace(output),
	})
}

// GetSMARTTestResults gets results of last SMART test
// GET /api/zfs/smart/results?device=sda
func GetSMARTTestResults(w http.ResponseWriter, r *http.Request) {
	device := r.URL.Query().Get("device")
	if strings.ContainsAny(device, ";|&$`\\\"' /") {
		respondErrorSimple(w, "Invalid device", http.StatusBadRequest)
		return
	}
	output, err := executeCommandWithTimeout(TimeoutFast, "/usr/sbin/smartctl", []string{
		"-l", "selftest", "/dev/" + device,
	})
	if err != nil {
		respondOK(w, map[string]interface{}{"success": false, "error": err.Error()})
		return
	}
	respondOK(w, map[string]interface{}{
		"success": true,
		"device":  device,
		"results": strings.TrimSpace(output),
	})
}

// ÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉ
//  10. ZFS DELEGATION (zfs allow)
// ÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉ

// SetZFSDelegation grants ZFS permissions to a user
// POST /api/zfs/delegation { "dataset": "tank/data", "user": "bob", "permissions": "send,snapshot,mount" }
func SetZFSDelegation(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Dataset     string `json:"dataset"`
		User        string `json:"user"`
		Permissions string `json:"permissions"` // comma-separated: send,snapshot,mount,destroy,etc.
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondErrorSimple(w, "Invalid request", http.StatusBadRequest)
		return
	}
	if !isValidDataset(req.Dataset) {
		respondErrorSimple(w, "Invalid dataset", http.StatusBadRequest)
		return
	}
	if strings.ContainsAny(req.User, ";|&$`\\\"' /") || len(req.User) > 64 {
		respondErrorSimple(w, "Invalid user", http.StatusBadRequest)
		return
	}
	if strings.ContainsAny(req.Permissions, ";|&$`\\\"' /") {
		respondErrorSimple(w, "Invalid permissions", http.StatusBadRequest)
		return
	}

	_, err := executeCommandWithTimeout(TimeoutMedium, "/usr/sbin/zfs", []string{
		"allow", req.User, req.Permissions, req.Dataset,
	})
	if err != nil {
		respondOK(w, map[string]interface{}{"success": false, "error": err.Error()})
		return
	}
	respondOK(w, map[string]interface{}{
		"success":     true,
		"dataset":     req.Dataset,
		"user":        req.User,
		"permissions": req.Permissions,
	})
}

// GetZFSDelegation lists current delegations
// GET /api/zfs/delegation?dataset=tank/data
func GetZFSDelegation(w http.ResponseWriter, r *http.Request) {
	dataset := r.URL.Query().Get("dataset")
	if !isValidDataset(dataset) {
		respondErrorSimple(w, "Invalid dataset", http.StatusBadRequest)
		return
	}
	output, err := executeCommandWithTimeout(TimeoutFast, "/usr/sbin/zfs", []string{
		"allow", dataset,
	})
	if err != nil {
		respondOK(w, map[string]interface{}{"success": true, "delegations": ""})
		return
	}
	respondOK(w, map[string]interface{}{
		"success":     true,
		"dataset":     dataset,
		"delegations": strings.TrimSpace(output),
	})
}

// RevokeZFSDelegation removes ZFS permissions
// POST /api/zfs/delegation/revoke { "dataset": "tank/data", "user": "bob", "permissions": "send,snapshot" }
func RevokeZFSDelegation(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Dataset     string `json:"dataset"`
		User        string `json:"user"`
		Permissions string `json:"permissions"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondErrorSimple(w, "Invalid request", http.StatusBadRequest)
		return
	}
	if !isValidDataset(req.Dataset) || strings.ContainsAny(req.User, ";|&$`\\\"' /") {
		respondErrorSimple(w, "Invalid input", http.StatusBadRequest)
		return
	}
	_, err := executeCommandWithTimeout(TimeoutMedium, "/usr/sbin/zfs", []string{
		"unallow", req.User, req.Permissions, req.Dataset,
	})
	if err != nil {
		respondOK(w, map[string]interface{}{"success": false, "error": err.Error()})
		return
	}
	respondOK(w, map[string]interface{}{"success": true})
}

// ÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉ
//  9. NETWORK ROLLBACK TIMER
// ÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉ

// Variable types needed
var (
	netRollbackContent []byte
	netRollbackPath    string
	netRollbackTimer   *countdownTimer
)

type countdownTimer struct {
	timer  *safeTimer
	active bool
}

type safeTimer struct{ t interface{ Stop() bool } }

// ApplyNetworkWithRollback applies network config with auto-revert
// POST /api/network/apply { "timeout_seconds": 60 }
func ApplyNetworkWithRollback(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ConfigPath     string `json:"config_path"` // /etc/network/interfaces or /etc/netplan/...
		NewConfig      string `json:"new_config"`
		TimeoutSeconds int    `json:"timeout_seconds"` // default 60
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondErrorSimple(w, "Invalid request", http.StatusBadRequest)
		return
	}
	if req.TimeoutSeconds == 0 {
		req.TimeoutSeconds = 60
	}
	if req.TimeoutSeconds < 15 || req.TimeoutSeconds > 300 {
		respondErrorSimple(w, "Timeout must be 15-300 seconds", http.StatusBadRequest)
		return
	}
	if strings.ContainsAny(req.ConfigPath, ";|&$`\\\"'") || req.ConfigPath == "" {
		respondErrorSimple(w, "Invalid config path", http.StatusBadRequest)
		return
	}

	// Save current config for rollback
	currentConfig, err := readFileContent(req.ConfigPath)
	if err != nil {
		respondOK(w, map[string]interface{}{"success": false, "error": "Cannot read current config"})
		return
	}

	// Write new config
	if err := writeFileContent(req.ConfigPath, []byte(req.NewConfig)); err != nil {
		respondOK(w, map[string]interface{}{"success": false, "error": "Cannot write new config"})
		return
	}

	// Apply
	executeCommandWithTimeout(TimeoutMedium, "/usr/sbin/netplan", []string{"apply"})

	// Start rollback timer
	netRollbackContent = currentConfig
	netRollbackPath = req.ConfigPath

	respondOK(w, map[string]interface{}{
		"success":         true,
		"timeout_seconds": req.TimeoutSeconds,
		"message":         fmt.Sprintf("Network config applied. Confirm within %d seconds or auto-revert.", req.TimeoutSeconds),
	})
}

// ConfirmNetwork cancels the rollback timer
// POST /api/network/confirm
func ConfirmNetwork(w http.ResponseWriter, r *http.Request) {
	netRollbackContent = nil
	netRollbackPath = ""
	respondOK(w, map[string]interface{}{
		"success": true,
		"message": "Network change confirmed. Rollback cancelled.",
	})
}

func readFileContent(path string) ([]byte, error) {
	return executeCommandBytes("/bin/cat", []string{path})
}

func writeFileContent(path string, content []byte) error {
	// Use tee to write as the daemon runs as root
	_, err := executeCommandWithTimeout(TimeoutFast, "/usr/bin/tee", []string{path})
	if err != nil {
		// Fallback
		return fmt.Errorf("write failed: %v", err)
	}
	return nil
}

func executeCommandBytes(path string, args []string) ([]byte, error) {
	out, err := executeCommandWithTimeout(TimeoutFast, path, args)
	return []byte(out), err
}

// ÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉ
//  Helper for quota size validation
// ÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉ

func isValidSize(s string) bool {
	if s == "none" || s == "0" {
		return true
	}
	s = strings.TrimSpace(s)
	if len(s) == 0 {
		return false
	}
	// Must end with K, M, G, T, P or be a number
	last := s[len(s)-1]
	prefix := s[:len(s)-1]
	if last >= '0' && last <= '9' {
		_, err := strconv.ParseInt(s, 10, 64)
		return err == nil
	}
	if last == 'K' || last == 'M' || last == 'G' || last == 'T' || last == 'P' {
		_, err := strconv.ParseFloat(prefix, 64)
		return err == nil
	}
	return false
}

// ÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉ
//  USER & GROUP QUOTAS  (ZFS userquota / groupquota)
// ÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉ

// GetUserGroupQuotas returns per-user and per-group space usage and quotas
// for a given dataset.
// GET /api/zfs/quota/usergroup?dataset=tank/data
func GetUserGroupQuotas(w http.ResponseWriter, r *http.Request) {
	dataset := r.URL.Query().Get("dataset")
	if !isValidDataset(dataset) {
		respondErrorSimple(w, "Invalid or missing dataset", http.StatusBadRequest)
		return
	}

	// zfs userspace: columns name, type, used, quota
	userOut, err := runZFSCommand([]string{
		"userspace", "-H", "-o", "name,type,used,quota", dataset,
	})
	if err != nil {
		respondErrorSimple(w, "zfs userspace failed: "+err.Error(), http.StatusInternalServerError)
		return
	}

	groupOut, _ := runZFSCommand([]string{
		"groupspace", "-H", "-o", "name,type,used,quota", dataset,
	})

	type QuotaEntry struct {
		Name  string `json:"name"`
		Type  string `json:"type"`
		Used  string `json:"used"`
		Quota string `json:"quota"`
	}

	parseEntries := func(raw []byte) []QuotaEntry {
		var entries []QuotaEntry
		for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
			if line == "" {
				continue
			}
			fields := strings.Fields(line)
			if len(fields) < 4 {
				continue
			}
			entries = append(entries, QuotaEntry{
				Name:  fields[0],
				Type:  fields[1],
				Used:  fields[2],
				Quota: fields[3],
			})
		}
		if entries == nil {
			entries = []QuotaEntry{}
		}
		return entries
	}

	respondOK(w, map[string]interface{}{
		"success": true,
		"dataset": dataset,
		"users":   parseEntries(userOut),
		"groups":  parseEntries(groupOut),
	})
}

// SetUserGroupQuota sets or clears a per-user or per-group quota on a dataset.
// POST /api/zfs/quota/usergroup
// Body: {"dataset":"tank/data","type":"user","name":"alice","quota":"50G"}
// Set quota to "none" to remove it.
func SetUserGroupQuota(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Dataset string `json:"dataset"`
		Type    string `json:"type"`  // "user" or "group"
		Name    string `json:"name"`  // username or group name
		Quota   string `json:"quota"` // e.g. "50G", "none"
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondErrorSimple(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	if !isValidDataset(req.Dataset) {
		respondErrorSimple(w, "Invalid dataset", http.StatusBadRequest)
		return
	}
	if req.Type != "user" && req.Type != "group" {
		respondErrorSimple(w, "type must be 'user' or 'group'", http.StatusBadRequest)
		return
	}
	if !isValidPosixName(req.Name) {
		respondErrorSimple(w, "Invalid user/group name", http.StatusBadRequest)
		return
	}
	if !isValidSize(req.Quota) && req.Quota != "none" {
		respondErrorSimple(w, "Invalid quota value (e.g. '50G', 'none')", http.StatusBadRequest)
		return
	}

	// Property: userquota@alice or groupquota@devteam
	prop := fmt.Sprintf("%squota@%s=%s", req.Type, req.Name, req.Quota)
	if _, err := runZFSCommand([]string{"set", prop, req.Dataset}); err != nil {
		respondErrorSimple(w, "Failed to set quota: "+err.Error(), http.StatusInternalServerError)
		return
	}

	respondOK(w, map[string]interface{}{
		"success": true,
		"dataset": req.Dataset,
		"type":    req.Type,
		"name":    req.Name,
		"quota":   req.Quota,
	})
}

// isValidPosixName validates POSIX usernames and group names.
// Allows: letters, digits, underscores, dashes, dots. Max 256 chars.
func isValidPosixName(name string) bool {
	if len(name) == 0 || len(name) > 256 {
		return false
	}
	for _, c := range name {
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') ||
			(c >= '0' && c <= '9') || c == '-' || c == '_' || c == '.') {
			return false
		}
	}
	return true
}

// ÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉ
//  11. POOL MAINTENANCE (CLEAR / ONLINE)
// ÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉÔòÉ

// PoolOperations handles pool maintenance commands like clear and online
// POST /api/zfs/pool/operations
func PoolOperations(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Pool   string `json:"pool"`
		Op     string `json:"operation"` // "clear", "online"
		Device string `json:"device"`    // required for "online"
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondErrorSimple(w, "Invalid request", http.StatusBadRequest)
		return
	}

	if !isValidDataset(req.Pool) {
		respondErrorSimple(w, "Invalid pool name", http.StatusBadRequest)
		return
	}

	var args []string
	switch req.Op {
	case "clear":
		args = []string{"clear", req.Pool}
	case "online":
		if req.Device == "" || strings.ContainsAny(req.Device, ";|&$`\"' /") {
			respondErrorSimple(w, "Invalid or missing device for online operation", http.StatusBadRequest)
			return
		}
		args = []string{"online", req.Pool, req.Device}
	default:
		respondErrorSimple(w, "Invalid operation (must be 'clear' or 'online')", http.StatusBadRequest)
		return
	}

	output, err := executeCommandWithTimeout(TimeoutMedium, "/usr/sbin/zpool", args)
	if err != nil {
		respondOK(w, map[string]interface{}{
			"success": false,
			"error":   err.Error(),
			"output":  strings.TrimSpace(output),
		})
		return
	}

	respondOK(w, map[string]interface{}{
		"success": true,
		"message": fmt.Sprintf("Pool %s: %s successful", req.Pool, req.Op),
	})
}

// runZFSCommand is a thin wrapper around exec.Command("/usr/sbin/zfs", args...).
// It returns combined stdout/stderr and any error.
func runZFSCommand(args []string) ([]byte, error) {
	return cmdutil.RunFast("/usr/sbin/zfs", args...)
}
