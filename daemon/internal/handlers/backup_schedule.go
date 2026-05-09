package handlers

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"dplaned/internal/audit"
	"dplaned/internal/cmdutil"
	"dplaned/internal/jobs"
	"dplaned/internal/systemd"

	"github.com/google/uuid"
	"github.com/gorilla/mux"
)

// RsyncSchedule defines a recurring rsync backup job.
type RsyncSchedule struct {
	ID          string     `json:"id"`
	Name        string     `json:"name"`
	Source      string     `json:"source"`
	Destination string     `json:"destination"`
	Options     string     `json:"options"`
	Interval    string     `json:"interval"` // "hourly" | "daily" | "weekly" | "monthly"
	Hour        int        `json:"hour"`
	DayOfWeek   int        `json:"day_of_week"`   // 0=Sun..6=Sat, used for weekly
	DayOfMonth  int        `json:"day_of_month"`  // 1-31, used for monthly
	Enabled     bool       `json:"enabled"`
	LastRun     *time.Time `json:"last_run,omitempty"`
	LastStatus  string     `json:"last_status,omitempty"`
	LastJobID   string     `json:"last_job_id,omitempty"`
}

var rsyncSchedMu sync.RWMutex

const rsyncScheduleFile = "backup-schedules.json"

func loadRsyncSchedules() ([]RsyncSchedule, error) {
	rsyncSchedMu.RLock()
	defer rsyncSchedMu.RUnlock()

	data, err := os.ReadFile(configPath(rsyncScheduleFile))
	if err != nil {
		if os.IsNotExist(err) {
			return []RsyncSchedule{}, nil
		}
		return nil, err
	}
	var out []RsyncSchedule
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func saveRsyncSchedules(schedules []RsyncSchedule) error {
	rsyncSchedMu.Lock()
	defer rsyncSchedMu.Unlock()

	os.MkdirAll(ConfigDir, 0755)
	data, err := json.MarshalIndent(schedules, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(configPath(rsyncScheduleFile), data, 0600)
}

func onCalendarForRsync(s RsyncSchedule) string {
	days := []string{"Sun", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat"}
	switch s.Interval {
	case "hourly":
		return "*-*-* *:00:00"
	case "daily":
		h := s.Hour
		if h < 0 || h > 23 {
			h = 2
		}
		return fmt.Sprintf("*-*-* %02d:00:00", h)
	case "weekly":
		d := s.DayOfWeek
		if d < 0 || d > 6 {
			d = 0
		}
		h := s.Hour
		if h < 0 || h > 23 {
			h = 3
		}
		return fmt.Sprintf("%s *-*-* %02d:00:00", days[d], h)
	case "monthly":
		d := s.DayOfMonth
		if d < 1 || d > 28 {
			d = 1
		}
		h := s.Hour
		if h < 0 || h > 23 {
			h = 4
		}
		return fmt.Sprintf("*-*-%02d %02d:00:00", d, h)
	}
	return "*-*-* 02:00:00"
}

func installRsyncTimers(schedules []RsyncSchedule) {
	systemd.UninstallAllWithPrefix("dplaneos-rsync-")
	for _, s := range schedules {
		if !s.Enabled {
			continue
		}
		payload, _ := json.Marshal(map[string]string{"id": s.ID})
		safePayload := strings.ReplaceAll(string(payload), "'", "'\\''")
		cmd := fmt.Sprintf(
			"curl -sf -X POST http://127.0.0.1:9000/api/backup/rsync/cron-hook -H 'Content-Type: application/json' -H 'X-Internal-Token: dplaneos-internal-reconciliation-secret-v1' -d '%s'",
			safePayload,
		)
		safeName := strings.NewReplacer("/", "-", " ", "_").Replace(s.ID)
		err := systemd.InstallTimer(systemd.TimerConfig{
			Name:        fmt.Sprintf("rsync-%s", safeName),
			Description: fmt.Sprintf("Rsync backup: %s", s.Name),
			Command:     fmt.Sprintf("bash -c \"%s\"", strings.ReplaceAll(cmd, "\"", "\\\"")),
			OnCalendar:  onCalendarForRsync(s),
			Persistent:  true,
		})
		if err != nil {
			log.Printf("ERROR: failed to install rsync timer %s: %v", s.ID, err)
		}
	}
}

// GET /api/backup/rsync/schedules
func ListRsyncSchedules(w http.ResponseWriter, r *http.Request) {
	schedules, err := loadRsyncSchedules()
	if err != nil {
		respondErrorSimple(w, "Failed to load schedules", http.StatusInternalServerError)
		return
	}
	respondOK(w, map[string]interface{}{"success": true, "schedules": schedules})
}

// POST /api/backup/rsync/schedules
func CreateRsyncSchedule(w http.ResponseWriter, r *http.Request) {
	var s RsyncSchedule
	if err := json.NewDecoder(r.Body).Decode(&s); err != nil {
		respondErrorSimple(w, "Invalid JSON", http.StatusBadRequest)
		return
	}
	if s.Source == "" || s.Destination == "" {
		respondErrorSimple(w, "source and destination are required", http.StatusBadRequest)
		return
	}
	if s.Name == "" {
		s.Name = s.Source
	}
	if s.Options == "" {
		s.Options = "-avz --progress"
	}
	if s.Interval == "" {
		s.Interval = "daily"
	}
	s.ID = uuid.New().String()

	schedules, err := loadRsyncSchedules()
	if err != nil {
		respondErrorSimple(w, "Failed to load schedules", http.StatusInternalServerError)
		return
	}
	schedules = append(schedules, s)
	if err := saveRsyncSchedules(schedules); err != nil {
		respondErrorSimple(w, "Failed to save schedules", http.StatusInternalServerError)
		return
	}
	installRsyncTimers(schedules)

	audit.LogActivity(r.Header.Get("X-User"), "rsync_schedule_create", map[string]interface{}{"id": s.ID, "name": s.Name})
	respondOK(w, map[string]interface{}{"success": true, "schedule": s})
}

// PUT /api/backup/rsync/schedules/{id}
func UpdateRsyncSchedule(w http.ResponseWriter, r *http.Request) {
	id := mux.Vars(r)["id"]
	var updated RsyncSchedule
	if err := json.NewDecoder(r.Body).Decode(&updated); err != nil {
		respondErrorSimple(w, "Invalid JSON", http.StatusBadRequest)
		return
	}
	updated.ID = id

	schedules, err := loadRsyncSchedules()
	if err != nil {
		respondErrorSimple(w, "Failed to load schedules", http.StatusInternalServerError)
		return
	}
	found := false
	for i, s := range schedules {
		if s.ID == id {
			// Preserve run history
			updated.LastRun = s.LastRun
			updated.LastStatus = s.LastStatus
			updated.LastJobID = s.LastJobID
			schedules[i] = updated
			found = true
			break
		}
	}
	if !found {
		respondErrorSimple(w, "Schedule not found", http.StatusNotFound)
		return
	}
	if err := saveRsyncSchedules(schedules); err != nil {
		respondErrorSimple(w, "Failed to save schedules", http.StatusInternalServerError)
		return
	}
	installRsyncTimers(schedules)

	audit.LogActivity(r.Header.Get("X-User"), "rsync_schedule_update", map[string]interface{}{"id": id})
	respondOK(w, map[string]interface{}{"success": true, "schedule": updated})
}

// DELETE /api/backup/rsync/schedules/{id}
func DeleteRsyncSchedule(w http.ResponseWriter, r *http.Request) {
	id := mux.Vars(r)["id"]

	schedules, err := loadRsyncSchedules()
	if err != nil {
		respondErrorSimple(w, "Failed to load schedules", http.StatusInternalServerError)
		return
	}
	filtered := schedules[:0]
	found := false
	for _, s := range schedules {
		if s.ID == id {
			found = true
			continue
		}
		filtered = append(filtered, s)
	}
	if !found {
		respondErrorSimple(w, "Schedule not found", http.StatusNotFound)
		return
	}
	if err := saveRsyncSchedules(filtered); err != nil {
		respondErrorSimple(w, "Failed to save schedules", http.StatusInternalServerError)
		return
	}
	installRsyncTimers(filtered)

	audit.LogActivity(r.Header.Get("X-User"), "rsync_schedule_delete", map[string]interface{}{"id": id})
	respondOK(w, map[string]interface{}{"success": true})
}

// POST /api/backup/rsync/schedules/{id}/run
func RunRsyncScheduleNow(w http.ResponseWriter, r *http.Request) {
	id := mux.Vars(r)["id"]

	schedules, err := loadRsyncSchedules()
	if err != nil {
		respondErrorSimple(w, "Failed to load schedules", http.StatusInternalServerError)
		return
	}
	var target *RsyncSchedule
	for i := range schedules {
		if schedules[i].ID == id {
			target = &schedules[i]
			break
		}
	}
	if target == nil {
		respondErrorSimple(w, "Schedule not found", http.StatusNotFound)
		return
	}

	src, dst, opts := target.Source, target.Destination, target.Options
	scheduleID := target.ID

	jobID := jobs.Start("rsync_scheduled", func(j *jobs.Job) {
		args := append(strings.Fields(opts), src, dst)
		output, err := cmdutil.RunSlow("rsync", args...)
		now := time.Now()

		rsyncSchedMu.Lock()
		data, _ := os.ReadFile(configPath(rsyncScheduleFile))
		var all []RsyncSchedule
		json.Unmarshal(data, &all)
		for i := range all {
			if all[i].ID == scheduleID {
				all[i].LastRun = &now
				if err != nil {
					all[i].LastStatus = "failed"
					j.Fail(err.Error())
				} else {
					all[i].LastStatus = "done"
					j.Done(map[string]interface{}{"output": string(output)})
				}
				all[i].LastJobID = j.ID
				break
			}
		}
		out, _ := json.MarshalIndent(all, "", "  ")
		os.WriteFile(configPath(rsyncScheduleFile), out, 0600)
		rsyncSchedMu.Unlock()
	})

	// Record job ID immediately
	rsyncSchedMu.Lock()
	for i := range schedules {
		if schedules[i].ID == id {
			schedules[i].LastJobID = jobID
			schedules[i].LastStatus = "running"
			break
		}
	}
	saveRsyncSchedules(schedules)
	rsyncSchedMu.Unlock()

	audit.LogActivity(r.Header.Get("X-User"), "rsync_schedule_run_now", map[string]interface{}{"id": id})
	respondOK(w, map[string]interface{}{"success": true, "job_id": jobID})
}

// POST /api/backup/rsync/cron-hook
// Called by systemd timers on localhost only (enforced by sessionMiddleware).
func RsyncCronHook(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondErrorSimple(w, "Invalid JSON", http.StatusBadRequest)
		return
	}

	schedules, err := loadRsyncSchedules()
	if err != nil {
		respondErrorSimple(w, "Failed to load schedules", http.StatusInternalServerError)
		return
	}
	var target *RsyncSchedule
	for i := range schedules {
		if schedules[i].ID == req.ID {
			target = &schedules[i]
			break
		}
	}
	if target == nil {
		respondErrorSimple(w, "Schedule not found", http.StatusNotFound)
		return
	}
	if !target.Enabled {
		respondOK(w, map[string]interface{}{"success": true, "skipped": "disabled"})
		return
	}

	src, dst, opts := target.Source, target.Destination, target.Options
	scheduleID := req.ID

	jobID := jobs.Start("rsync_scheduled", func(j *jobs.Job) {
		args := append(strings.Fields(opts), src, dst)
		output, runErr := cmdutil.RunSlow("rsync", args...)
		now := time.Now()

		rsyncSchedMu.Lock()
		data, _ := os.ReadFile(configPath(rsyncScheduleFile))
		var all []RsyncSchedule
		json.Unmarshal(data, &all)
		for i := range all {
			if all[i].ID == scheduleID {
				all[i].LastRun = &now
				all[i].LastJobID = j.ID
				if runErr != nil {
					all[i].LastStatus = "failed"
					j.Fail(runErr.Error())
				} else {
					all[i].LastStatus = "done"
					j.Done(map[string]interface{}{"output": string(output)})
				}
				break
			}
		}
		out, _ := json.MarshalIndent(all, "", "  ")
		os.WriteFile(configPath(rsyncScheduleFile), out, 0600)
		rsyncSchedMu.Unlock()
	})

	log.Printf("RSYNC SCHEDULE: started job %s for schedule %s", jobID, scheduleID)
	respondOK(w, map[string]interface{}{"success": true, "job_id": jobID})
}
