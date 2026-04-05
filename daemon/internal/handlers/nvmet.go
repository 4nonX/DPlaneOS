package handlers

import (
	"encoding/json"
	"net/http"
	"strings"

	"dplaned/internal/nvmet"
)

// GetNVMeTargetStatus reports whether configfs nvmet is available (loads modules best-effort).
// GET /api/nvmet/status
func GetNVMeTargetStatus(w http.ResponseWriter, r *http.Request) {
	_, _ = executeCommandWithTimeout(TimeoutSlow, "modprobe", []string{"nvmet"})
	_, _ = executeCommandWithTimeout(TimeoutSlow, "modprobe", []string{"nvmet-tcp"})
	root := "/sys/kernel/config/nvmet"
	_, err := executeCommandWithTimeout(TimeoutFast, "test", []string{"-d", root})
	respondOK(w, map[string]interface{}{
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
	respondOK(w, map[string]interface{}{
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

func saveAndApplyNVMe(list []nvmet.Export) error {
	if err := nvmet.SaveExports(nvmet.TargetsFile, list); err != nil {
		return err
	}
	return nvmet.Apply(list)
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
	list, err := nvmet.LoadExports(nvmet.TargetsFile)
	if err != nil {
		respondErrorSimple(w, err.Error(), http.StatusInternalServerError)
		return
	}
	for _, e := range list {
		if e.SubsystemNQN == req.SubsystemNQN {
			respondErrorSimple(w, "subsystem_nqn already exists", http.StatusConflict)
			return
		}
	}
	list = append(list, req)
	if err := saveAndApplyNVMe(list); err != nil {
		respondErrorSimple(w, err.Error(), http.StatusInternalServerError)
		return
	}
	respondOK(w, map[string]interface{}{
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
	list, err := nvmet.LoadExports(nvmet.TargetsFile)
	if err != nil {
		respondErrorSimple(w, err.Error(), http.StatusInternalServerError)
		return
	}
	found := false
	for i := range list {
		if list[i].SubsystemNQN == req.SubsystemNQN {
			list[i] = req
			found = true
			break
		}
	}
	if !found {
		respondErrorSimple(w, "subsystem not found", http.StatusNotFound)
		return
	}
	if err := saveAndApplyNVMe(list); err != nil {
		respondErrorSimple(w, err.Error(), http.StatusInternalServerError)
		return
	}
	respondOK(w, map[string]interface{}{
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
	list, err := nvmet.LoadExports(nvmet.TargetsFile)
	if err != nil {
		respondErrorSimple(w, err.Error(), http.StatusInternalServerError)
		return
	}
	var out []nvmet.Export
	for _, e := range list {
		if e.SubsystemNQN != nqn {
			out = append(out, e)
		}
	}
	if len(out) == len(list) {
		respondErrorSimple(w, "subsystem not found", http.StatusNotFound)
		return
	}
	if err := saveAndApplyNVMe(out); err != nil {
		respondErrorSimple(w, err.Error(), http.StatusInternalServerError)
		return
	}
	respondOK(w, map[string]interface{}{
		"success": true,
		"message": "NVMe-oF target removed",
	})
}
