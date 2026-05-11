package handlers

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"dplaned/internal/jobs"
	"dplaned/internal/security"
)

// ═══════════════════════════════════════════════════════════════
//  RAID-Z Parity Expansion (OpenZFS 2022+)
//  POST /api/zfs/pool/raidz-expand
//  GET  /api/zfs/pool/raidz-expand/status?pool=X
// ═══════════════════════════════════════════════════════════════

// ExpandRAIDZ attaches a new disk to an existing RAID-Z vdev to expand its parity width.
// Uses `zpool attach <pool> <anchor-disk> <new-disk>` which, on RAID-Z vdevs (OpenZFS 2022+),
// triggers an online expansion rather than mirror creation.
//
// POST /api/zfs/pool/raidz-expand
// Body: { "pool": "tank", "anchor_disk": "/dev/disk/by-id/...", "new_disk": "/dev/disk/by-id/..." }
func ExpandRAIDZ(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Pool       string `json:"pool"`
		AnchorDisk string `json:"anchor_disk"`
		NewDisk    string `json:"new_disk"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondErrorSimple(w, "Invalid request body", http.StatusBadRequest)
		return
	}
	if !isValidDataset(req.Pool) {
		respondErrorSimple(w, "Invalid pool name", http.StatusBadRequest)
		return
	}
	if err := validateDevicePath(req.AnchorDisk); err != nil {
		respondErrorSimple(w, fmt.Sprintf("Invalid anchor disk: %v", err), http.StatusBadRequest)
		return
	}
	if err := validateDevicePath(req.NewDisk); err != nil {
		respondErrorSimple(w, fmt.Sprintf("Invalid new disk: %v", err), http.StatusBadRequest)
		return
	}

	// Verify anchor disk is actually in a RAID-Z vdev, not a mirror, to prevent accidental
	// mirror creation on pools where zpool attach has mirror semantics.
	statusOut, err := executeCommandWithTimeout(TimeoutFast, "zpool", []string{"status", "-P", req.Pool})
	if err != nil {
		respondErrorSimple(w, "Failed to query pool status", http.StatusInternalServerError)
		return
	}
	if !isInRAIDZVdev(statusOut, req.AnchorDisk) {
		respondErrorSimple(w, "Anchor disk is not a member of a RAID-Z vdev in this pool. Use pool attach for mirrors.", http.StatusBadRequest)
		return
	}

	pool := req.Pool
	anchorDisk := req.AnchorDisk
	newDisk := req.NewDisk

	jobID := jobs.Start("raidz-expand", func(j *jobs.Job) {
		if diskEventHub != nil {
			diskEventHub.Broadcast("raidz_expand_started", map[string]interface{}{
				"pool":        pool,
				"anchor_disk": anchorDisk,
				"new_disk":    newDisk,
			}, "info")
		}

		output, err := executeCommandWithTimeout(TimeoutMedium, "zpool", []string{"attach", pool, anchorDisk, newDisk})
		if err != nil {
			if diskEventHub != nil {
				diskEventHub.Broadcast("raidz_expand_completed", map[string]interface{}{
					"pool":    pool,
					"success": false,
					"error":   err.Error(),
				}, "warning")
			}
			j.Fail(fmt.Sprintf("zpool attach (raidz expand) failed: %v - %s", err, strings.TrimSpace(output)))
			return
		}

		// zpool attach returns immediately; expansion runs in background.
		// Start a polling goroutine to emit progress events.
		go pollRAIDZExpansion(pool)

		j.Done(map[string]interface{}{
			"message": "RAID-Z expansion initiated. Data redistribution running in the background.",
		})
	})

	respondOK(w, map[string]interface{}{"success": true, "job_id": jobID})
}

// GetRAIDZExpandStatus returns the current expansion progress for a pool.
// Reads `zpool status -P` and parses the "expanding" scan line.
//
// GET /api/zfs/pool/raidz-expand/status?pool=X
func GetRAIDZExpandStatus(w http.ResponseWriter, r *http.Request) {
	pool := r.URL.Query().Get("pool")
	if pool == "" || !isValidDataset(pool) {
		respondErrorSimple(w, "Invalid or missing pool name", http.StatusBadRequest)
		return
	}

	output, err := executeCommandWithTimeout(TimeoutFast, "zpool", []string{"status", "-P", pool})
	if err != nil {
		respondErrorSimple(w, "Failed to query pool status", http.StatusInternalServerError)
		return
	}

	expanding, pct, eta := parseExpansionProgress(output)
	respondOK(w, map[string]interface{}{
		"success":    true,
		"pool":       pool,
		"expanding":  expanding,
		"percent":    pct,
		"eta":        eta,
	})
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// isInRAIDZVdev returns true if diskPath appears as a leaf under a raidz vdev
// in zpool status -P output.
//
// The parser tracks indentation levels: when it encounters a raidz* line it
// records that indent depth, then watches for subsequent lines at that depth+1
// (the children). A line at depth <= raidz depth closes the vdev group.
func isInRAIDZVdev(statusOutput, diskPath string) bool {
	lines := strings.Split(statusOutput, "\n")
	inConfig := false
	raidzDepth := -1

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "config:" {
			inConfig = true
			continue
		}
		if !inConfig || trimmed == "" {
			continue
		}
		// config section ends at the next top-level keyword (no leading whitespace)
		if !strings.HasPrefix(line, "\t") && !strings.HasPrefix(line, " ") &&
			trimmed != "" && !strings.HasPrefix(trimmed, "NAME") {
			inConfig = false
			continue
		}

		depth := countLeadingTabs(line)
		firstField := strings.Fields(trimmed)
		if len(firstField) == 0 {
			continue
		}
		name := firstField[0]

		if strings.HasPrefix(name, "raidz") {
			raidzDepth = depth
			continue
		}

		if raidzDepth >= 0 {
			if depth <= raidzDepth {
				// Left the raidz group
				raidzDepth = -1
			} else if depth == raidzDepth+1 {
				// Direct child of raidz - check if it matches the target disk
				if name == diskPath || strings.HasSuffix(diskPath, name) || strings.HasSuffix(name, diskPath) {
					return true
				}
			}
		}
	}
	return false
}

// countLeadingTabs counts leading tab characters. Mixed tab/space uses tabs as primary unit.
func countLeadingTabs(line string) int {
	count := 0
	for _, ch := range line {
		if ch == '\t' {
			count++
		} else {
			break
		}
	}
	return count
}

var (
	reExpandPct = regexp.MustCompile(`(\d+\.\d+)%\s+done`)
	reExpandETA = regexp.MustCompile(`(\d+h\d+m|\d+:\d+:\d+|\d+m\d+s)\s+to go`)
)

// parseExpansionProgress scans zpool status output for expansion progress info.
// Returns (expanding bool, percent float64, eta string).
func parseExpansionProgress(statusOutput string) (bool, float64, string) {
	lower := strings.ToLower(statusOutput)
	if !strings.Contains(lower, "expanding") && !strings.Contains(lower, "expand") {
		return false, 0, ""
	}

	var pct float64
	var eta string

	for _, line := range strings.Split(statusOutput, "\n") {
		if m := reExpandPct.FindStringSubmatch(line); len(m) == 2 {
			pct, _ = strconv.ParseFloat(m[1], 64)
		}
		if m := reExpandETA.FindStringSubmatch(line); len(m) == 2 {
			eta = m[1]
		}
	}

	return true, pct, eta
}

// pollRAIDZExpansion runs as a goroutine, broadcasting raidz_expand_progress
// events every 30 seconds until expansion completes or a 7-day safety timeout.
func pollRAIDZExpansion(pool string) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	deadline := time.Now().Add(7 * 24 * time.Hour)

	for {
		<-ticker.C
		if time.Now().After(deadline) {
			log.Printf("pollRAIDZExpansion: safety timeout reached for pool %s", pool)
			return
		}

		output, err := executeCommandWithTimeout(TimeoutFast, "zpool", []string{"status", "-P", pool})
		if err != nil {
			log.Printf("pollRAIDZExpansion: zpool status error for pool %s: %v", pool, err)
			return
		}

		expanding, pct, eta := parseExpansionProgress(output)
		if !expanding {
			if diskEventHub != nil {
				diskEventHub.Broadcast("raidz_expand_completed", map[string]interface{}{
					"pool":    pool,
					"success": true,
				}, "success")
			}
			return
		}

		if diskEventHub != nil {
			diskEventHub.Broadcast("raidz_expand_progress", map[string]interface{}{
				"pool":    pool,
				"percent": pct,
				"eta":     eta,
			}, "info")
		}
	}
}

// validateDevicePath calls the security package validator.
func validateDevicePath(path string) error {
	return security.ValidateDevicePath(path)
}
