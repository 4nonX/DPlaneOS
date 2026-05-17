package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"

	"dplaned/internal/libzfs"
	"dplaned/internal/security"
	"dplaned/internal/storageops"
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

	opID, err := storageops.Begin(registryDB, storageops.OpWipeDisk, req.Device)
	if err != nil {
		respondError(w, http.StatusConflict, err.Error(), nil)
		return
	}

	// CRITICAL SAFETY CHECK: disk must NOT be part of any active ZFS pool.
	// libzfs.PoolIsMember walks the vdev nvlist tree directly (no subprocess
	// string grep) so it correctly handles by-id paths and bare /dev/sdX paths.
	if membership, merr := libzfs.PoolIsMember(req.Device); merr == nil && membership.InPool {
		storageops.Fail(registryDB, opID, "disk is a member of pool "+membership.PoolName)
		respondOK(w, map[string]interface{}{
			"success": false,
			"error":   "Safety check failed: Disk is a member of pool " + membership.PoolName + ". Remove or detach it first.",
		})
		return
	}

	// 1. wipefs -a
	wipeOut, wipeErr := executeCommandWithTimeout(TimeoutMedium, "wipefs", []string{"-a", req.Device})
	if wipeErr != nil {
		storageops.Fail(registryDB, opID, fmt.Sprintf("wipefs: %v", wipeErr))
		respondOK(w, map[string]interface{}{
			"success": false,
			"error":   fmt.Sprintf("wipefs failed: %v", wipeErr),
			"output":  wipeOut,
		})
		return
	}

	// 2. zpool labelclear -f (failure is non-fatal: no ZFS label present is fine after wipefs)
	labelOut, _ := executeCommandWithTimeout(TimeoutMedium, "zpool", []string{"labelclear", "-f", req.Device})

	storageops.Commit(registryDB, opID)
	respondOK(w, map[string]interface{}{
		"success": true,
		"device":  req.Device,
		"message": "Disk signatures and ZFS labels cleared successfully.",
		"output":  fmt.Sprintf("%s\n%s", wipeOut, labelOut),
	})
}
