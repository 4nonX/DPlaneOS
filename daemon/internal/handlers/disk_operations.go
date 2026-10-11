package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"

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

	// The disk must not be part of an imported pool. If that cannot be
	// determined, the disk is not wiped.
	membership, merr := libzfs.PoolIsMember(req.Device)
	if merr != nil {
		storageops.Fail(registryDB, opID, "pool membership check: "+merr.Error())
		respondOK(w, map[string]any{
			"success": false,
			"error":   "Cannot check whether the disk belongs to a pool; not wiped: " + merr.Error(),
		})
		return
	}
	if membership.InPool {
		storageops.Fail(registryDB, opID, "disk is a member of pool "+membership.PoolName)
		respondOK(w, map[string]any{
			"success": false,
			"error":   "Safety check failed: Disk is a member of pool " + membership.PoolName + ". Remove or detach it first.",
		})
		return
	}

	// 1. wipefs -a
	wipeOut, wipeErr := executeCommandWithTimeout(TimeoutMedium, "wipefs", []string{"-a", req.Device})
	if wipeErr != nil {
		storageops.Fail(registryDB, opID, fmt.Sprintf("wipefs: %v", wipeErr))
		respondOK(w, map[string]any{
			"success": false,
			"error":   fmt.Sprintf("wipefs failed: %v", wipeErr),
			"output":  wipeOut,
		})
		return
	}

	// 2. zpool labelclear -f (failure is non-fatal: no ZFS label present is fine after wipefs)
	labelOut, _ := executeCommandWithTimeout(TimeoutMedium, "zpool", []string{"labelclear", "-f", req.Device})

	storageops.Commit(registryDB, opID)
	if real, err := filepath.EvalSymlinks(req.Device); err == nil {
		ForgetDiskPool(registryDB, filepath.Base(real))
	}
	respondOK(w, map[string]any{
		"success": true,
		"device":  req.Device,
		"message": "Disk signatures and ZFS labels cleared successfully.",
		"output":  fmt.Sprintf("%s\n%s", wipeOut, labelOut),
	})
}
