package handlers

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"regexp"
	"strings"
	"time"

	"dplaned/internal/audit"
	"dplaned/internal/cmdutil"
	"dplaned/internal/gitops"
	"dplaned/internal/hardware"
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

	req.Device = smartDevice(req.Device)
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
		respondOK(w, map[string]any{
			"success": false,
			"error":   fmt.Sprintf("SMART test failed to start: %v", err),
			"output":  string(output),
		})
		return
	}

	audit.LogAction("smart_test_manual", user, fmt.Sprintf("Started %s SMART test on %s", testType, req.Device), true, duration)
	respondOK(w, map[string]any{
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
	respondOK(w, map[string]any{"success": true})
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
		var enabled int
		if err := rows.Scan(&s.ID, &s.Device, &s.Type, &s.Schedule, &enabled); err != nil {
			log.Printf("WARN: smart schedules list: %v", err)
			continue
		}
		s.Enabled = enabled == 1
		schedules = append(schedules, s)
	}
	if err := rows.Err(); err != nil {
		log.Printf("WARN: smart schedules list rows: %v", err)
	}

	respondOK(w, map[string]any{
		"success":   true,
		"schedules": schedules,
	})
}

// (RegenerateSMARTTimers moved to internal/hardware/smart.go)

// smartDevice: "sda" and "/dev/sda" both name the disk.
func smartDevice(d string) string {
	d = strings.TrimSpace(d)
	if d != "" && !strings.HasPrefix(d, "/") {
		return "/dev/" + d
	}
	return d
}

// A systemd calendar expression ("daily", "Sun *-*-* 03:00:00"); the timers
// are systemd timers, so cron syntax does not work.
var (
	smartCalendarRe = regexp.MustCompile(`^[A-Za-z0-9*:,/~. -]{1,80}$`)
	smartCronRe     = regexp.MustCompile(`^[0-9*/,-]+( [0-9*/,-]+){4}$`)
)

func validSMARTCalendar(v string) error {
	if !smartCalendarRe.MatchString(v) {
		return fmt.Errorf("invalid schedule")
	}
	if smartCronRe.MatchString(v) {
		return fmt.Errorf("schedule is a cron expression; use a systemd calendar expression such as \"daily\", \"weekly\" or \"Sun *-*-* 03:00:00\"")
	}
	return nil
}

// regenerateSMARTTimers installs the systemd timers (replaced in tests).
var regenerateSMARTTimers = hardware.RegenerateSMARTTimers

// applySMARTSchedules: timers follow the table at once; the GitOps state is
// written back like after every other change.
func applySMARTSchedules(db *sql.DB) error {
	err := regenerateSMARTTimers(db)
	gitops.CommitAllAsync(db)
	return err
}

// AddSMARTSchedule adds or replaces a scheduled SMART self-test.
// POST /api/hardware/smart/schedules {device, type, schedule}
// (schedule may also be given as ?schedule=)
func AddSMARTSchedule(w http.ResponseWriter, r *http.Request) {
	db := ReconcilerDB
	if db == nil {
		respondErrorSimple(w, "Database unavailable", http.StatusInternalServerError)
		return
	}
	var req struct {
		Device   string `json:"device"`
		Type     string `json:"type"`
		Schedule string `json:"schedule"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondErrorSimple(w, "Invalid request", http.StatusBadRequest)
		return
	}
	req.Device = smartDevice(req.Device)
	if err := security.ValidateDevicePath(req.Device); err != nil {
		respondErrorSimple(w, fmt.Sprintf("Invalid device path: %v", err), http.StatusBadRequest)
		return
	}
	if req.Type != "short" && req.Type != "long" && req.Type != "conveyance" {
		respondErrorSimple(w, "type must be short, long or conveyance", http.StatusBadRequest)
		return
	}
	if req.Schedule == "" {
		req.Schedule = r.URL.Query().Get("schedule")
	}
	req.Schedule = strings.TrimSpace(req.Schedule)
	if err := validSMARTCalendar(req.Schedule); err != nil {
		respondErrorSimple(w, err.Error(), http.StatusBadRequest)
		return
	}
	if _, err := db.Exec(`INSERT INTO smart_schedules (device, test_type, schedule, enabled)
		VALUES ($1, $2, $3, 1)
		ON CONFLICT(device, test_type) DO UPDATE SET schedule=EXCLUDED.schedule, enabled=1`,
		req.Device, req.Type, req.Schedule); err != nil {
		respondError(w, http.StatusInternalServerError, "Failed to save the schedule", err)
		return
	}
	user := r.Header.Get("X-User")
	if err := applySMARTSchedules(db); err != nil {
		audit.LogAction("smart_schedule_add", user, fmt.Sprintf("%s test for %s at %s saved, timer failed: %v", req.Type, req.Device, req.Schedule, err), false, 0)
		respondOK(w, map[string]any{"success": false, "error": "Saved, but the timer could not be installed: " + err.Error()})
		return
	}
	audit.LogAction("smart_schedule_add", user, fmt.Sprintf("Added %s test for %s at %s", req.Type, req.Device, req.Schedule), true, 0)
	respondOK(w, map[string]any{"success": true, "message": "Schedule saved."})
}

// DeleteSMARTSchedule removes a scheduled SMART self-test.
// DELETE /api/hardware/smart/schedules?device=&type=
func DeleteSMARTSchedule(w http.ResponseWriter, r *http.Request) {
	db := ReconcilerDB
	if db == nil {
		respondErrorSimple(w, "Database unavailable", http.StatusInternalServerError)
		return
	}
	device := smartDevice(r.URL.Query().Get("device"))
	testType := r.URL.Query().Get("type")
	if testType == "" {
		respondErrorSimple(w, "device and type parameters required", http.StatusBadRequest)
		return
	}
	if err := security.ValidateDevicePath(device); err != nil {
		respondErrorSimple(w, fmt.Sprintf("Invalid device path: %v", err), http.StatusBadRequest)
		return
	}
	res, err := db.Exec(`DELETE FROM smart_schedules WHERE device=$1 AND test_type=$2`, device, testType)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "Failed to remove the schedule", err)
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		respondErrorSimple(w, "Schedule not found", http.StatusNotFound)
		return
	}
	user := r.Header.Get("X-User")
	if err := applySMARTSchedules(db); err != nil {
		respondOK(w, map[string]any{"success": false, "error": "Removed, but the timers could not be updated: " + err.Error()})
		return
	}
	audit.LogAction("smart_schedule_delete", user, fmt.Sprintf("Removed %s test for %s", testType, device), true, 0)
	respondOK(w, map[string]any{"success": true, "message": "Schedule removed."})
}
