package handlers

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"dplaned/internal/audit"
	"dplaned/internal/cmdutil"
	"dplaned/internal/jobs"
	"dplaned/internal/security"
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
	DayOfWeek   int        `json:"day_of_week"`  // 0=Sun..6=Sat, used for weekly
	DayOfMonth  int        `json:"day_of_month"` // 1-31, used for monthly
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

// RestoreRsyncTimers re-installs rsync backup timers at daemon start.
func RestoreRsyncTimers() {
	schedules, err := loadRsyncSchedules()
	if err != nil {
		log.Printf("WARN: rsync schedules: %v", err)
		return
	}
	if err := installRsyncTimers(schedules); err != nil {
		log.Printf("ERROR: rsync timers: %v", err)
	}
}

func installRsyncTimers(schedules []RsyncSchedule) error {
	var errs []error
	if err := systemd.UninstallAllWithPrefix("dplaneos-rsync-"); err != nil {
		errs = append(errs, fmt.Errorf("clear old timers: %w", err))
	}
	for _, s := range schedules {
		if !s.Enabled {
			continue
		}
		payload, _ := json.Marshal(map[string]string{"id": s.ID})
		safePayload := strings.ReplaceAll(string(payload), "'", "'\\''")
		cmd := fmt.Sprintf(
			"curl -sf --unix-socket /run/dplaneos/dplaned.sock -X POST http://localhost/api/backup/rsync/cron-hook -H 'Content-Type: application/json' -H 'X-Internal-Token: %s' -d '%s'",
			cronToken,
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
			errs = append(errs, fmt.Errorf("%s: %w", s.Name, err))
		}
	}
	return errors.Join(errs...)
}

// GET /api/backup/rsync/schedules
func ListRsyncSchedules(w http.ResponseWriter, r *http.Request) {
	schedules, err := loadRsyncSchedules()
	if err != nil {
		respondErrorSimple(w, "Failed to load schedules", http.StatusInternalServerError)
		return
	}
	respondOK(w, map[string]any{"success": true, "schedules": schedules})
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
	if _, err := rsyncArgs(s.Options, s.Source, s.Destination); err != nil {
		respondErrorSimple(w, err.Error(), http.StatusBadRequest)
		return
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
	if err := installRsyncTimers(final); err != nil {
		respondOK(w, map[string]any{"success": false, "error": "Schedule saved, but timers could not be installed: " + err.Error()})
		return
	}

	audit.LogActivity(r.Header.Get("X-User"), "rsync_schedule_create", map[string]any{"id": s.ID, "name": s.Name})
	respondOK(w, map[string]any{"success": true, "schedule": s})
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
	if _, err := rsyncArgs(req.Options, req.Source, req.Destination); err != nil {
		respondErrorSimple(w, err.Error(), http.StatusBadRequest)
		return
	}
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
	if err := installRsyncTimers(final); err != nil {
		respondOK(w, map[string]any{"success": false, "error": "Schedule saved, but timers could not be installed: " + err.Error()})
		return
	}

	audit.LogActivity(r.Header.Get("X-User"), "rsync_schedule_update", map[string]any{"id": id})
	respondOK(w, map[string]any{"success": true, "schedule": req})
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
	if err := installRsyncTimers(final); err != nil {
		respondOK(w, map[string]any{"success": false, "error": "Schedule saved, but timers could not be installed: " + err.Error()})
		return
	}

	audit.LogActivity(r.Header.Get("X-User"), "rsync_schedule_delete", map[string]any{"id": id})
	respondOK(w, map[string]any{"success": true})
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
		var output []byte
		args, err := rsyncArgs(opts, src, dst)
		if err == nil {
			output, err = cmdutil.RunSlow("rsync", args...)
		}
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
					j.Done(map[string]any{"output": string(output)})
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

	audit.LogActivity(r.Header.Get("X-User"), "rsync_schedule_run_now", map[string]any{"id": id})
	respondOK(w, map[string]any{"success": true, "job_id": jobID})
}

// rsyncArgs builds the rsync arguments for a backup: the options (from the
// allowed set), the source and the destination. Local paths are resolved
// (symlinks included) against the file roots right before every run, so a
// folder replaced by a symlink since the task was saved cannot redirect a
// backup into the system. With a remote side, ssh runs without prompts.
func rsyncArgs(opts, src, dst string) ([]string, error) {
	if strings.TrimSpace(opts) == "" {
		opts = "-avz --progress"
	}
	args := strings.Fields(opts)
	for _, a := range args {
		if a == "-e" || !strings.HasPrefix(a, "-") {
			return nil, fmt.Errorf("option %q not allowed", a)
		}
	}
	local := func(p string, dest bool) (string, error) {
		if _, remote := security.IsRsyncRemote(p); remote {
			return p, nil
		}
		slash := len(p) > 1 && strings.HasSuffix(p, "/")
		var real string
		var err error
		if dest {
			real, err = resolveDestination(p)
		} else {
			real, err = resolveExisting(p)
		}
		if err != nil {
			return "", fmt.Errorf("%s: %v (pool and media mounts only)", p, err)
		}
		real = filepath.ToSlash(real)
		if slash {
			real += "/"
		}
		return real, nil
	}
	s, err := local(src, false)
	if err != nil {
		return nil, err
	}
	d, err := local(dst, true)
	if err != nil {
		return nil, err
	}
	_, srcRemote := security.IsRsyncRemote(s)
	_, dstRemote := security.IsRsyncRemote(d)
	if srcRemote && dstRemote {
		return nil, fmt.Errorf("source and destination cannot both be remote")
	}
	if srcRemote || dstRemote {
		args = append(args, "-e", security.RsyncSSH)
	}
	args = append(args, s, d)
	if runtime.GOOS == "linux" {
		if err := security.ValidateCommand("rsync", args); err != nil {
			return nil, err
		}
	}
	return args, nil
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
		respondOK(w, map[string]any{"success": true, "skipped": "disabled"})
		return
	}

	src, dst, opts := target.Source, target.Destination, target.Options
	scheduleID := req.ID

	jobID := jobs.Start("rsync_scheduled", func(j *jobs.Job) {
		var output []byte
		args, runErr := rsyncArgs(opts, src, dst)
		if runErr == nil {
			output, runErr = cmdutil.RunSlow("rsync", args...)
		}
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
					j.Done(map[string]any{"output": string(output)})
				}
				break
			}
		}
		out, _ := json.MarshalIndent(all, "", "  ")
		os.WriteFile(configPath(rsyncScheduleFile), out, 0600)
		rsyncSchedMu.Unlock()
	})

	log.Printf("RSYNC SCHEDULE: started job %s for schedule %s", jobID, scheduleID)
	respondOK(w, map[string]any{"success": true, "job_id": jobID})
}
