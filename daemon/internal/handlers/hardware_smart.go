package handlers

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"

	"dplaned/internal/audit"
	"dplaned/internal/cmdutil"
	"dplaned/internal/gitops"
	"dplaned/internal/security"
)

// SMARTTestRequest represents a request to trigger a SMART test
type SMARTTestRequest struct {
	Device string `json:"device"`
	Type   string `json:"type"` // short, long, conveyance
}

// RunSMARTNow triggers an immediate SMART test on a device
// POST /api/hardware/smart/run-now
func RunSMARTNow(w http.ResponseWriter, r *http.Request) {
	user := r.Header.Get("X-User")
	var req SMARTTestRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondErrorSimple(w, "Invalid request", http.StatusBadRequest)
		return
	}

	if err := security.ValidateDevicePath(req.Device); err != nil {
		respondErrorSimple(w, fmt.Sprintf("Invalid device path: %v", err), http.StatusBadRequest)
		return
	}

	testType := "short"
	if req.Type == "long" || req.Type == "conveyance" {
		testType = req.Type
	}

	start := time.Now()
	// smartctl -t <type> <device>
	output, err := cmdutil.RunFast("smartctl_test", "-t", testType, req.Device)
	duration := time.Since(start)
	
	if err != nil {
		audit.LogAction("smart_test_manual", user, fmt.Sprintf("Failed: %s %s: %s", testType, req.Device, string(output)), false, duration)
		respondOK(w, map[string]interface{}{
			"success": false,
			"error":   fmt.Sprintf("SMART test failed to start: %v", err),
			"output":  string(output),
		})
		return
	}

	audit.LogAction("smart_test_manual", user, fmt.Sprintf("Started %s SMART test on %s", testType, req.Device), true, duration)
	respondOK(w, map[string]interface{}{
		"success": true,
		"message": fmt.Sprintf("SMART %s test started on %s.", testType, req.Device),
		"output":  string(output),
	})
}

// RunSMARTCronHook is called by systemd timers to execute scheduled SMART tests
// POST /api/hardware/smart/cron-hook
func RunSMARTCronHook(w http.ResponseWriter, r *http.Request) {
	var req SMARTTestRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondErrorSimple(w, "Invalid request", http.StatusBadRequest)
		return
	}

	// Internal hook - skip audit log for 'start', but log completion/failure
	testType := "short"
	if req.Type == "long" || req.Type == "conveyance" {
		testType = req.Type
	}

	if err := security.ValidateDevicePath(req.Device); err != nil {
		respondErrorSimple(w, fmt.Sprintf("Invalid device path: %v", err), http.StatusBadRequest)
		return
	}
	log.Printf("SMART CRON: Starting %s test on %s", testType, req.Device)
	output, err := cmdutil.RunFast("smartctl_test", "-t", testType, req.Device)

	if err != nil {
		audit.LogAction("smart_test_cron", "system", fmt.Sprintf("CRON Failed: %s %s: %s", testType, req.Device, string(output)), false, 0)
		respondErrorSimple(w, "SMART test failed", http.StatusInternalServerError)
		return
	}

	audit.LogAction("smart_test_cron", "system", fmt.Sprintf("CRON Started %s SMART test on %s", testType, req.Device), true, 0)
	respondOK(w, map[string]interface{}{"success": true})
}

// SMARTSchedule represents a persisted SMART task
type SMARTSchedule struct {
	ID       int    `json:"id"`
	Device   string `json:"device"`
	Type     string `json:"type"`
	Schedule string `json:"schedule"`
	Enabled  bool   `json:"enabled"`
}

// ListSMARTSchedules returns all active SMART test schedules from the DB
// GET /api/hardware/smart/schedules
func ListSMARTSchedules(w http.ResponseWriter, r *http.Request) {
	db := ReconcilerDB
	if db == nil {
		respondErrorSimple(w, "Database unavailable", http.StatusInternalServerError)
		return
	}

	rows, err := db.Query("SELECT id, device, test_type, schedule, enabled FROM smart_schedules")
	if err != nil {
		respondErrorSimple(w, fmt.Sprintf("Query failed: %v", err), http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	var schedules []SMARTSchedule
	for rows.Next() {
		var s SMARTSchedule
		if err := rows.Scan(&s.ID, &s.Device, &s.Type, &s.Schedule, &s.Enabled); err != nil {
			continue
		}
		schedules = append(schedules, s)
	}

	respondOK(w, map[string]interface{}{
		"success":   true,
		"schedules": schedules,
	})
}

// (RegenerateSMARTTimers moved to internal/hardware/smart.go)

// AddSMARTSchedule adds a new SMART task to the GitOps state.yaml
// POST /api/hardware/smart/schedules
func AddSMARTSchedule(w http.ResponseWriter, r *http.Request) {
	var req SMARTTestRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondErrorSimple(w, "Invalid request", http.StatusBadRequest)
		return
	}
	if err := security.ValidateDevicePath(req.Device); err != nil {
		respondErrorSimple(w, fmt.Sprintf("Invalid device path: %v", err), http.StatusBadRequest)
		return
	}
	cron := r.URL.Query().Get("schedule")
	if cron == "" {
		respondErrorSimple(w, "Missing schedule parameter", http.StatusBadRequest)
		return
	}

	// 1. Load state.yaml
	content, err := os.ReadFile(GitOpsStatePath)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "Failed to read state.yaml", err)
		return
	}
	state, err := gitops.ParseStateYAML(string(content))
	if err != nil {
		respondError(w, http.StatusUnprocessableEntity, "State YAML error", err)
		return
	}

	// 2. Add or update task
	found := false
	for i, t := range state.SMART {
		if t.Device == req.Device && t.Type == req.Type {
			state.SMART[i].Schedule = cron
			state.SMART[i].Enabled = true
			found = true
			break
		}
	}
	if !found {
		state.SMART = append(state.SMART, gitops.DesiredSMARTTask{
			Device:   req.Device,
			Type:     req.Type,
			Schedule: cron,
			Enabled:  true,
		})
	}

	// 3. Write back
	newContent := gitops.PrintStateYAML(state)
	if err := os.WriteFile(GitOpsStatePath, []byte(newContent), 0644); err != nil {
		respondError(w, http.StatusInternalServerError, "Failed to write state.yaml", err)
		return
	}

	audit.LogAction("smart_schedule_add", r.Header.Get("X-User"), fmt.Sprintf("Added %s test for %s at %s", req.Type, req.Device, cron), true, 0)
	respondOK(w, map[string]interface{}{"success": true, "message": "Schedule added to GitOps state."})
}

// DeleteSMARTSchedule removes a SMART task from the GitOps state.yaml
// DELETE /api/hardware/smart/schedules
func DeleteSMARTSchedule(w http.ResponseWriter, r *http.Request) {
	device := r.URL.Query().Get("device")
	testType := r.URL.Query().Get("type")
	if device == "" || testType == "" {
		respondErrorSimple(w, "device and type parameters required", http.StatusBadRequest)
		return
	}
	if err := security.ValidateDevicePath(device); err != nil {
		respondErrorSimple(w, fmt.Sprintf("Invalid device path: %v", err), http.StatusBadRequest)
		return
	}

	// 1. Load state.yaml
	content, err := os.ReadFile(GitOpsStatePath)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "Failed to read state.yaml", err)
		return
	}
	state, err := gitops.ParseStateYAML(string(content))
	if err != nil {
		respondError(w, http.StatusUnprocessableEntity, "State YAML error", err)
		return
	}

	// 2. Remove task
	newTasks := []gitops.DesiredSMARTTask{}
	found := false
	for _, t := range state.SMART {
		if t.Device == device && t.Type == testType {
			found = true
			continue
		}
		newTasks = append(newTasks, t)
	}
	if !found {
		respondErrorSimple(w, "Schedule not found", http.StatusNotFound)
		return
	}
	state.SMART = newTasks

	// 3. Write back
	newContent := gitops.PrintStateYAML(state)
	if err := os.WriteFile(GitOpsStatePath, []byte(newContent), 0644); err != nil {
		respondError(w, http.StatusInternalServerError, "Failed to write state.yaml", err)
		return
	}

	audit.LogAction("smart_schedule_delete", r.Header.Get("X-User"), fmt.Sprintf("Removed %s test for %s", testType, device), true, 0)
	respondOK(w, map[string]interface{}{"success": true, "message": "Schedule removed from GitOps state."})
}
