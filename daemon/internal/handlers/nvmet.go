package handlers

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"

	"dplaned/internal/nvmet"
)

var nvmeMu sync.Mutex

var (
	errNVMeConflict = errors.New("nvme subsystem_nqn already exists")
	errNVMeNotFound = errors.New("nvme subsystem not found")
)

// errNVMeApply wraps a failure of the kernel configuration.
var errNVMeApply = errors.New("nvme apply")

// atomicModifyNVMeTargets holds nvmeMu across load, modify, apply and save.
// The list is saved only after the kernel took it: a failed apply used to
// leave the export in the saved list (refused as a duplicate afterwards,
// failing again at every boot). On failure the previous list is applied again.
func atomicModifyNVMeTargets(fn func([]nvmet.Export) ([]nvmet.Export, error)) ([]nvmet.Export, error) {
	nvmeMu.Lock()
	defer nvmeMu.Unlock()

	list, err := nvmet.LoadExports(nvmet.TargetsFile)
	if err != nil {
		return nil, err
	}
	previous := append([]nvmet.Export(nil), list...)
	modified, err := fn(list)
	if err != nil {
		return nil, err
	}
	if err := nvmet.Apply(modified); err != nil {
		if rerr := nvmet.Apply(previous); rerr != nil {
			err = fmt.Errorf("%v; restoring the previous exports failed too: %v", err, rerr)
		}
		return nil, fmt.Errorf("%w: %v", errNVMeApply, err)
	}
	if err := nvmet.SaveExports(nvmet.TargetsFile, modified); err != nil {
		return nil, err
	}
	return modified, nil
}

// GetNVMeTargetStatus reports whether configfs nvmet is available (loads modules best-effort).
// GET /api/nvmet/status
func GetNVMeTargetStatus(w http.ResponseWriter, r *http.Request) {
	_, _ = executeCommandWithTimeout(TimeoutSlow, "modprobe", []string{"nvmet"})
	_, _ = executeCommandWithTimeout(TimeoutSlow, "modprobe", []string{"nvmet-tcp"})
	root := "/sys/kernel/config/nvmet"
	_, err := executeCommandWithTimeout(TimeoutFast, "test", []string{"-d", root})
	respondOK(w, map[string]any{
		"success":    true,
		"ready":      err == nil,
		"nvmet_root": root,
	})
}

// ListNVMeTargets returns persisted NVMe-oF exports.
// GET /api/nvmet/targets
func ListNVMeTargets(w http.ResponseWriter, r *http.Request) {
	list, err := nvmet.LoadExports(nvmet.TargetsFile)
	if err != nil {
		respondErrorSimple(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if list == nil {
		list = []nvmet.Export{}
	}
	respondOK(w, map[string]any{
		"success": true,
		"targets": list,
		"count":   len(list),
	})
}

// ListNVMeZvols lists ZFS volumes suitable as backing stores.
// GET /api/nvmet/zvols
func ListNVMeZvols(w http.ResponseWriter, r *http.Request) {
	GetISCSIZvolList(w, r)
}

// CreateNVMeTarget appends one export and applies nvmet.
// POST /api/nvmet/targets
func CreateNVMeTarget(w http.ResponseWriter, r *http.Request) {
	var req nvmet.Export
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondErrorSimple(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	if err := nvmet.ValidateExport(&req); err != nil {
		respondError(w, http.StatusBadRequest, "Validation failed", err)
		return
	}
	if by := blockDeviceExportedBy(nvmet.ZvolDevicePath(req.Zvol), "", ""); by != "" {
		respondErrorSimple(w, errBlockDeviceInUse(nvmet.ZvolDevicePath(req.Zvol), by).Error(), http.StatusConflict)
		return
	}
	_, err := atomicModifyNVMeTargets(func(all []nvmet.Export) ([]nvmet.Export, error) {
		for _, e := range all {
			if e.SubsystemNQN == req.SubsystemNQN {
				return nil, errNVMeConflict
			}
		}
		return append(all, req), nil
	})
	if errors.Is(err, errNVMeConflict) {
		respondErrorSimple(w, "subsystem_nqn already exists", http.StatusConflict)
		return
	}
	if err != nil {
		respondErrorSimple(w, err.Error(), http.StatusInternalServerError)
		return
	}
	respondOK(w, map[string]any{
		"success":       true,
		"message":       "NVMe-oF target created",
		"subsystem_nqn": req.SubsystemNQN,
	})
}

// UpdateNVMeTarget replaces an export by subsystem_nqn.
// PUT /api/nvmet/targets
func UpdateNVMeTarget(w http.ResponseWriter, r *http.Request) {
	var req nvmet.Export
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondErrorSimple(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	if err := nvmet.ValidateExport(&req); err != nil {
		respondError(w, http.StatusBadRequest, "Validation failed", err)
		return
	}
	if by := blockDeviceExportedBy(nvmet.ZvolDevicePath(req.Zvol), req.SubsystemNQN, ""); by != "" {
		respondErrorSimple(w, errBlockDeviceInUse(nvmet.ZvolDevicePath(req.Zvol), by).Error(), http.StatusConflict)
		return
	}
	_, err := atomicModifyNVMeTargets(func(all []nvmet.Export) ([]nvmet.Export, error) {
		for i := range all {
			if all[i].SubsystemNQN == req.SubsystemNQN {
				all[i] = req
				return all, nil
			}
		}
		return nil, errNVMeNotFound
	})
	if errors.Is(err, errNVMeNotFound) {
		respondErrorSimple(w, "subsystem not found", http.StatusNotFound)
		return
	}
	if err != nil {
		respondErrorSimple(w, err.Error(), http.StatusInternalServerError)
		return
	}
	respondOK(w, map[string]any{
		"success": true,
		"message": "NVMe-oF target updated",
	})
}

// DeleteNVMeTarget removes an export by query ?subsystem_nqn=
// DELETE /api/nvmet/targets?subsystem_nqn=nqn....
func DeleteNVMeTarget(w http.ResponseWriter, r *http.Request) {
	nqn := strings.TrimSpace(r.URL.Query().Get("subsystem_nqn"))
	if nqn == "" {
		respondErrorSimple(w, "subsystem_nqn query parameter required", http.StatusBadRequest)
		return
	}
	_, err := atomicModifyNVMeTargets(func(all []nvmet.Export) ([]nvmet.Export, error) {
		out := make([]nvmet.Export, 0, len(all))
		for _, e := range all {
			if e.SubsystemNQN != nqn {
				out = append(out, e)
			}
		}
		if len(out) == len(all) {
			return nil, errNVMeNotFound
		}
		return out, nil
	})
	if errors.Is(err, errNVMeNotFound) {
		respondErrorSimple(w, "subsystem not found", http.StatusNotFound)
		return
	}
	if err != nil {
		respondErrorSimple(w, err.Error(), http.StatusInternalServerError)
		return
	}
	respondOK(w, map[string]any{
		"success": true,
		"message": "NVMe-oF target removed",
	})
}
