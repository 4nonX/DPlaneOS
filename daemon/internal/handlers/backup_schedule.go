package handlers

import (
	"encoding/json"
	"errors"
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

var errRsyncScheduleNotFound = errors.New("rsync schedule not found")

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

// atomicModifyRsyncSchedules holds the write lock across the full load-modify-save
// cycle, eliminating TOCTOU races between concurrent CRUD requests.
func atomicModifyRsyncSchedules(fn func([]RsyncSchedule) ([]RsyncSchedule, error)) error {
	rsyncSchedMu.Lock()
	defer rsyncSchedMu.Unlock()

	data, err := os.ReadFile(configPath(rsyncScheduleFile))
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	var schedules []RsyncSchedule
	if len(data) > 0 {
		if err := json.Unmarshal(data, &schedules); err != nil {
			return err
		}
	}
	modified, err := fn(schedules)
	if err != nil {
		return err
	}
	os.MkdirAll(ConfigDir, 0755)
	out, err := json.MarshalIndent(modified, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(configPath(rsyncScheduleFile), out, 0600)
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

	var final []RsyncSchedule
	if err := atomicModifyRsyncSchedules(func(schedules []RsyncSchedule) ([]RsyncSchedule, error) {
		result := append(schedules, s)
		final = result
		return result, nil
	}); err != nil {
		respondErrorSimple(w, "Failed to save schedules", http.StatusInternalServerError)
		return
	}
	installRsyncTimers(final)

	audit.LogActivity(r.Header.Get("X-User"), "rsync_schedule_create", map[string]interface{}{"id": s.ID, "name": s.Name})
	respondOK(w, map[string]interface{}{"success": true, "schedule": s})
}

// PUT /api/backup/rsync/schedules/{id}
func UpdateRsyncSchedule(w http.ResponseWriter, r *http.Request) {
	id := mux.Vars(r)["id"]
	var req RsyncSchedule
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondErrorSimple(w, "Invalid JSON", http.StatusBadRequest)
		return
	}
	req.ID = id

	var final []RsyncSchedule
	err := atomicModifyRsyncSchedules(func(schedules []RsyncSchedule) ([]RsyncSchedule, error) {
		for i, s := range schedules {
			if s.ID != id {
				continue
			}
			req.LastRun = s.LastRun
			req.LastStatus = s.LastStatus
			req.LastJobID = s.LastJobID
			schedules[i] = req
			final = schedules
			return schedules, nil
		}
		return nil, errRsyncScheduleNotFound
	})

	if errors.Is(err, errRsyncScheduleNotFound) {
		respondErrorSimple(w, "Schedule not found", http.StatusNotFound)
		return
	}
	if err != nil {
		respondErrorSimple(w, "Failed to save schedules", http.StatusInternalServerError)
		return
	}
	installRsyncTimers(final)

	audit.LogActivity(r.Header.Get("X-User"), "rsync_schedule_update", map[string]interface{}{"id": id})
	respondOK(w, map[string]interface{}{"success": true, "schedule": req})
}

// DELETE /api/backup/rsync/schedules/{id}
func DeleteRsyncSchedule(w http.ResponseWriter, r *http.Request) {
	id := mux.Vars(r)["id"]

	var final []RsyncSchedule
	err := atomicModifyRsyncSchedules(func(schedules []RsyncSchedule) ([]RsyncSchedule, error) {
		out := schedules[:0]
		found := false
		for _, s := range schedules {
			if s.ID == id {
				found = true
				continue
			}
			out = append(out, s)
		}
		if !found {
			return nil, errRsyncScheduleNotFound
		}
		final = out
		return out, nil
	})

	if errors.Is(err, errRsyncScheduleNotFound) {
		respondErrorSimple(w, "Schedule not found", http.StatusNotFound)
		return
	}
	if err != nil {
		respondErrorSimple(w, "Failed to save schedules", http.StatusInternalServerError)
		return
	}
	installRsyncTimers(final)

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

	if err := atomicModifyRsyncSchedules(func(all []RsyncSchedule) ([]RsyncSchedule, error) {
		for i := range all {
			if all[i].ID == id {
				all[i].LastJobID = jobID
				all[i].LastStatus = "running"
				break
			}
		}
		return all, nil
	}); err != nil {
		log.Printf("WARN: RunRsyncScheduleNow: failed to persist running status for %s: %v", id, err)
	}

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
