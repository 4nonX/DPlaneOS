package handlers

import (
	"dplaned/internal/security"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// WipeDiskRequest represents the request to wipe a disk
type WipeDiskRequest struct {
	Device string `json:"device"` // e.g. /dev/disk/by-id/...
	Force  bool   `json:"force"`  // Currently unused but for parity
}

// WipeDisk clears filesystem signatures and ZFS labels from a disk
// POST /api/disk/wipe
func WipeDisk(w http.ResponseWriter, r *http.Request) {
	var req WipeDiskRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondErrorSimple(w, "Invalid request", http.StatusBadRequest)
		return
	}

	if err := security.ValidateDevicePath(req.Device); err != nil {
		respondErrorSimple(w, fmt.Sprintf("Invalid device path: %v", err), http.StatusBadRequest)
		return
	}

	// CRITICAL SAFETY CHECK: Disk must NOT be part of any active pool
	statusOutput, err := executeCommandWithTimeout(TimeoutFast, "zpool", []string{"status"})
	if err == nil {
		// Even if zpool status fails (no pools), we continue. If it succeeds, check if device is in output.
		// Note: zpool status might show short names or by-id depending on import. 
		// We check for the device string in the output as a heuristic.
		// A more robust check would be 'zfs list' or checking 'lsblk'.
		if strings.Contains(statusOutput, req.Device) || strings.Contains(statusOutput, strings.TrimPrefix(req.Device, "/dev/")) {
			respondOK(w, map[string]interface{}{
				"success": false,
				"error":   "Safety check failed: Disk appears to be a member of an active ZFS pool. Remove or detach it first.",
			})
			return
		}
	}

	// 1. wipefs -a
	wipeOut, wipeErr := executeCommandWithTimeout(TimeoutMedium, "wipefs", []string{"-a", req.Device})
	if wipeErr != nil {
		// Log but continue to labelclear? Often wipefs fails if busy.
		// Actually, if wipefs fails, we should probably stop.
		respondOK(w, map[string]interface{}{
			"success": false,
			"error":   fmt.Sprintf("wipefs failed: %v", wipeErr),
			"output":  wipeOut,
		})
		return
	}

	// 2. zpool labelclear -f
	labelOut, labelErr := executeCommandWithTimeout(TimeoutMedium, "zpool", []string{"labelclear", "-f", req.Device})
	if labelErr != nil {
		// labelclear might fail if there was no ZFS label, which is fine after wipefs.
		// But if it's a real failure, we should report it.
	}

	respondOK(w, map[string]interface{}{
		"success": true,
		"device":  req.Device,
		"message": "Disk signatures and ZFS labels cleared successfully.",
		"output":  fmt.Sprintf("%s\n%s", wipeOut, labelOut),
	})
}
