package handlers

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	"dplaned/internal/audit"
	"dplaned/internal/cmdutil"
	"dplaned/internal/jobs"

	"github.com/google/uuid"
	"github.com/gorilla/mux"
)

// Cloud sync tasks: a folder of this system and a folder of a remote, copied
// one way, by hand or on a schedule.
//
// Two things a task is strict about, because a mistake in either destroys
// data without a question:
//
//   - the remote side is always a path inside the remote (bucket/prefix),
//     never the remote itself. "The whole account" as a destination would
//     turn every top-level folder into a bucket, and as the destination of
//     a mirror it would delete what the other buckets hold;
//   - a task copies. Deleting at the destination what is no longer at the
//     source ("mirror") has to be chosen.
type CloudSyncTask struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Remote     string `json:"remote"`
	RemotePath string `json:"remote_path"` // bucket/prefix or folder inside the remote
	LocalPath  string `json:"local_path"`
	Direction  string `json:"direction"` // "upload" (this system → remote) | "download"
	Mode       string `json:"mode"`      // "copy" | "mirror" (also deletes at the destination)
	Interval   string `json:"interval"`  // "manual" | "hourly" | "daily" | "weekly"
	Hour       int    `json:"hour"`      // daily, weekly: the hour of the day (0-23, local time)
	DayOfWeek  int    `json:"day_of_week"`
	Enabled    bool   `json:"enabled"`

	CreatedAt  time.Time  `json:"created_at"`
	LastRun    *time.Time `json:"last_run,omitempty"`
	LastStatus string     `json:"last_status,omitempty"` // "running" | "done" | "failed"
	LastError  string     `json:"last_error,omitempty"`
	LastJobID  string     `json:"last_job_id,omitempty"`
}

const cloudSyncTasksFile = "cloud-sync-tasks.json"

var (
	cloudTasksMu      sync.Mutex
	cloudTasksRunning = map[string]bool{}
	cloudTaskNameRe   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9 ._-]{0,62}$`)
)

// runRcloneTransfer runs a transfer, however long it takes. Replaced in tests.
var runRcloneTransfer = func(args ...string) ([]byte, error) {
	a := append([]string{"--config", rcloneConfig}, args...)
	return cmdutil.RunNoTimeout("rclone", a...)
}

func loadCloudTasks() ([]CloudSyncTask, error) {
	data, err := os.ReadFile(configPath(cloudSyncTasksFile))
	if err != nil {
		if os.IsNotExist(err) {
			return []CloudSyncTask{}, nil
		}
		return nil, err
	}
	var out []CloudSyncTask
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("%s: %w", cloudSyncTasksFile, err)
	}
	return out, nil
}

func saveCloudTasks(tasks []CloudSyncTask) error {
	if tasks == nil {
		tasks = []CloudSyncTask{}
	}
	data, err := json.MarshalIndent(tasks, "", "  ")
	if err != nil {
		return err
	}
	path := configPath(cloudSyncTasksFile)
	if err := os.WriteFile(path+".tmp", data, 0o600); err != nil {
		return err
	}
	return os.Rename(path+".tmp", path)
}

// modifyCloudTasks changes the saved list under the lock.
func modifyCloudTasks(fn func([]CloudSyncTask) ([]CloudSyncTask, error)) error {
	cloudTasksMu.Lock()
	defer cloudTasksMu.Unlock()
	tasks, err := loadCloudTasks()
	if err != nil {
		return err
	}
	tasks, err = fn(tasks)
	if err != nil {
		return err
	}
	return saveCloudTasks(tasks)
}

// cleanRemotePath checks the path inside the remote and returns it without
// leading or trailing slashes.
func cleanRemotePath(p string) (string, error) {
	p = strings.Trim(strings.TrimSpace(p), "/")
	if p == "" {
		return "", errors.New("remote_path is required: the bucket (and folder) inside the remote, e.g. my-bucket/nas. A task never works on a whole remote")
	}
	if len(p) > 1024 || strings.HasPrefix(p, "-") {
		return "", errors.New("invalid remote_path")
	}
	for _, part := range strings.Split(p, "/") {
		if part == "" || part == "." || part == ".." {
			return "", errors.New("invalid remote_path")
		}
	}
	for _, r := range p {
		if r < 0x20 || r == 0x7f || r == ':' || r == '\\' {
			return "", errors.New("invalid remote_path (no colons, backslashes or control characters)")
		}
	}
	return p, nil
}

// validateCloudTask checks a task and fills in its defaults; the local path
// is resolved (within the pools, no way out through a symbolic link).
func validateCloudTask(t *CloudSyncTask) error {
	t.Name = strings.TrimSpace(t.Name)
	if !cloudTaskNameRe.MatchString(t.Name) {
		return errors.New("name: letters, digits, spaces, . _ - (up to 63)")
	}
	if !validateRemoteName(t.Remote) {
		return errors.New("invalid remote name")
	}
	rp, err := cleanRemotePath(t.RemotePath)
	if err != nil {
		return err
	}
	t.RemotePath = rp
	switch t.Direction {
	case "", "upload":
		t.Direction = "upload"
	case "download":
	default:
		return errors.New(`direction must be "upload" or "download"`)
	}
	switch t.Mode {
	case "", "copy":
		t.Mode = "copy"
	case "mirror":
	default:
		return errors.New(`mode must be "copy" or "mirror"`)
	}
	switch t.Interval {
	case "", "manual":
		t.Interval = "manual"
	case "hourly", "daily", "weekly":
	default:
		return errors.New(`interval must be "manual", "hourly", "daily" or "weekly"`)
	}
	if t.Hour < 0 || t.Hour > 23 || t.DayOfWeek < 0 || t.DayOfWeek > 6 {
		return errors.New("hour must be 0-23 and day_of_week 0 (Sunday) to 6")
	}
	if t.LocalPath == "" {
		return errors.New("local_path is required")
	}
	var local string
	if t.Direction == "download" {
		local, err = resolveDestination(t.LocalPath)
	} else {
		local, err = resolveExisting(t.LocalPath)
	}
	if err != nil {
		return fmt.Errorf("local_path not allowed (a folder in a pool): %w", err)
	}
	t.LocalPath = local
	return nil
}

// cloudTaskArgs returns the rclone arguments of a task.
func cloudTaskArgs(t CloudSyncTask, dryRun bool) []string {
	cmd := "copy"
	if t.Mode == "mirror" {
		cmd = "sync"
	}
	remote := t.Remote + ":" + t.RemotePath
	src, dst := t.LocalPath, remote
	if t.Direction == "download" {
		src, dst = remote, t.LocalPath
	}
	args := []string{cmd, src, dst, "--stats-one-line", "--stats", "30s"}
	if dryRun {
		args = append(args, "--dry-run")
	}
	return args
}

// cloudTaskDue reports whether a scheduled task should run now: its latest
// scheduled time has passed and it has not run (or been created) since. A
// run that was missed while the system was off is made up for at the next
// start, once.
func cloudTaskDue(t CloudSyncTask, now time.Time) bool {
	if !t.Enabled || t.Interval == "manual" || t.Interval == "" {
		return false
	}
	var slot time.Time
	switch t.Interval {
	case "hourly":
		slot = now.Truncate(time.Hour)
	case "daily":
		slot = time.Date(now.Year(), now.Month(), now.Day(), t.Hour, 0, 0, 0, now.Location())
		if slot.After(now) {
			slot = slot.AddDate(0, 0, -1)
		}
	case "weekly":
		slot = time.Date(now.Year(), now.Month(), now.Day(), t.Hour, 0, 0, 0, now.Location())
		back := (int(now.Weekday()) - t.DayOfWeek + 7) % 7
		slot = slot.AddDate(0, 0, -back)
		if slot.After(now) {
			slot = slot.AddDate(0, 0, -7)
		}
	default:
		return false
	}
	since := t.CreatedAt
	if t.LastRun != nil && t.LastRun.After(since) {
		since = *t.LastRun
	}
	return since.Before(slot)
}

// startCloudTask runs a task in the background. A task that is still
// running is not started a second time.
func startCloudTask(t CloudSyncTask, dryRun bool, user string) (string, error) {
	cloudTasksMu.Lock()
	if cloudTasksRunning[t.ID] {
		cloudTasksMu.Unlock()
		return "", errors.New("this task is still running")
	}
	cloudTasksRunning[t.ID] = true
	cloudTasksMu.Unlock()

	args := cloudTaskArgs(t, dryRun)
	label := t.Mode
	if dryRun {
		label += " (dry run)"
	}
	cj := &CloudSyncJob{
		ID: uuid.New().String(), Provider: t.Remote, Action: label,
		Source: args[1], Destination: args[2], Status: jobs.StatusRunning, StartedAt: time.Now(),
	}
	addCloudJob(cj)

	jobID := jobs.Start("cloud_sync", func(j *jobs.Job) {
		started := time.Now()
		out, err := runRcloneTransfer(args...)
		now := time.Now()
		msg := ""
		if err != nil {
			msg = tailLines(string(out), 3)
			if msg == "" {
				msg = err.Error()
			}
		}
		cloudTasksMu.Lock()
		delete(cloudTasksRunning, t.ID)
		cloudTasksMu.Unlock()
		if !dryRun {
			_ = modifyCloudTasks(func(all []CloudSyncTask) ([]CloudSyncTask, error) {
				for i := range all {
					if all[i].ID == t.ID {
						all[i].LastRun = &started
						all[i].LastJobID = j.ID
						all[i].LastError = msg
						all[i].LastStatus = "done"
						if err != nil {
							all[i].LastStatus = "failed"
						}
					}
				}
				return all, nil
			})
		}
		audit.LogAction("cloud_sync", user, fmt.Sprintf("Task %q: %s %s -> %s", t.Name, label, args[1], args[2]), err == nil, now.Sub(started))
		if err != nil {
			j.Fail(msg)
			updateCloudJob(cj.ID, func(c *CloudSyncJob) { c.Status, c.FinishedAt, c.Error = jobs.StatusFailed, &now, msg })
			if !dryRun {
				DispatchAlert("critical", EventBackupFailed, t.Name,
					fmt.Sprintf("Cloud sync task %q (%s to %s) failed: %s", t.Name, args[1], args[2], msg))
			}
			return
		}
		j.Done(map[string]any{"output": tailLines(string(out), 20)})
		updateCloudJob(cj.ID, func(c *CloudSyncJob) { c.Status, c.FinishedAt = jobs.StatusDone, &now })
	})
	updateCloudJob(cj.ID, func(c *CloudSyncJob) { c.JobID = jobID })
	return jobID, nil
}

// tailLines returns the last n lines of s that say something.
func tailLines(s string, n int) string {
	var keep []string
	lines := strings.Split(strings.TrimSpace(s), "\n")
	for i := len(lines) - 1; i >= 0 && len(keep) < n; i-- {
		if l := strings.TrimSpace(lines[i]); l != "" {
			keep = append([]string{l}, keep...)
		}
	}
	out := strings.Join(keep, "\n")
	if len(out) > 2000 {
		out = out[len(out)-2000:]
	}
	return out
}

// StartCloudSyncScheduler runs the scheduled tasks when they are due.
func StartCloudSyncScheduler() {
	go func() {
		for {
			time.Sleep(time.Minute)
			cloudTasksMu.Lock()
			tasks, err := loadCloudTasks()
			cloudTasksMu.Unlock()
			if err != nil {
				log.Printf("CLOUD SYNC: %v", err)
				continue
			}
			now := time.Now()
			for _, t := range tasks {
				if !cloudTaskDue(t, now) {
					continue
				}
				if _, err := startCloudTask(t, false, "schedule"); err == nil {
					log.Printf("CLOUD SYNC: task %q started (scheduled)", t.Name)
				}
			}
		}
	}()
}

// ── API ──────────────────────────────────────────────────────────────────────

// ListCloudSyncTasks: GET /api/cloud-sync/tasks
func ListCloudSyncTasks(w http.ResponseWriter, r *http.Request) {
	cloudTasksMu.Lock()
	tasks, err := loadCloudTasks()
	for i := range tasks {
		if cloudTasksRunning[tasks[i].ID] {
			tasks[i].LastStatus = "running"
		}
	}
	cloudTasksMu.Unlock()
	if err != nil {
		respondErrorSimple(w, err.Error(), http.StatusInternalServerError)
		return
	}
	respondOK(w, map[string]any{"success": true, "tasks": tasks})
}

func (h *CloudSyncHandler) remoteExists(name string) bool {
	for _, r := range h.getRcloneRemotes() {
		if r == name {
			return true
		}
	}
	return false
}

func decodeCloudTask(w http.ResponseWriter, r *http.Request, h *CloudSyncHandler) (CloudSyncTask, bool) {
	var t CloudSyncTask
	if err := json.NewDecoder(r.Body).Decode(&t); err != nil {
		respondErrorSimple(w, "invalid JSON", http.StatusBadRequest)
		return t, false
	}
	if err := validateCloudTask(&t); err != nil {
		respondErrorSimple(w, err.Error(), http.StatusBadRequest)
		return t, false
	}
	if !h.remoteExists(t.Remote) {
		respondErrorSimple(w, "remote "+t.Remote+" is not configured", http.StatusBadRequest)
		return t, false
	}
	return t, true
}

// CreateCloudSyncTask: POST /api/cloud-sync/tasks
func (h *CloudSyncHandler) CreateCloudSyncTask(w http.ResponseWriter, r *http.Request) {
	t, ok := decodeCloudTask(w, r, h)
	if !ok {
		return
	}
	t.ID = uuid.New().String()
	t.CreatedAt = time.Now()
	t.LastRun, t.LastStatus, t.LastError, t.LastJobID = nil, "", "", ""
	err := modifyCloudTasks(func(all []CloudSyncTask) ([]CloudSyncTask, error) {
		for _, e := range all {
			if strings.EqualFold(e.Name, t.Name) {
				return nil, fmt.Errorf("a task named %q exists", t.Name)
			}
		}
		return append(all, t), nil
	})
	if err != nil {
		respondErrorSimple(w, err.Error(), http.StatusConflict)
		return
	}
	audit.LogActivity(r.Header.Get("X-User"), "cloud_sync_task_create", map[string]any{"name": t.Name, "remote": t.Remote, "direction": t.Direction, "mode": t.Mode, "interval": t.Interval})
	respondOK(w, map[string]any{"success": true, "task": t})
}

// UpdateCloudSyncTask: PUT /api/cloud-sync/tasks/{id}
func (h *CloudSyncHandler) UpdateCloudSyncTask(w http.ResponseWriter, r *http.Request) {
	id := mux.Vars(r)["id"]
	t, ok := decodeCloudTask(w, r, h)
	if !ok {
		return
	}
	code := http.StatusConflict
	err := modifyCloudTasks(func(all []CloudSyncTask) ([]CloudSyncTask, error) {
		found := -1
		for i, e := range all {
			if e.ID == id {
				found = i
			} else if strings.EqualFold(e.Name, t.Name) {
				return nil, fmt.Errorf("a task named %q exists", t.Name)
			}
		}
		if found < 0 {
			code = http.StatusNotFound
			return nil, errors.New("task not found")
		}
		old := all[found]
		t.ID, t.LastRun, t.LastStatus, t.LastError, t.LastJobID = old.ID, old.LastRun, old.LastStatus, old.LastError, old.LastJobID
		// A changed schedule counts from now: the task does not run at once
		// to make up for a time that was never its own.
		t.CreatedAt = old.CreatedAt
		if old.Interval != t.Interval || old.Hour != t.Hour || old.DayOfWeek != t.DayOfWeek || (!old.Enabled && t.Enabled) {
			t.CreatedAt = time.Now()
		}
		all[found] = t
		return all, nil
	})
	if err != nil {
		respondErrorSimple(w, err.Error(), code)
		return
	}
	audit.LogActivity(r.Header.Get("X-User"), "cloud_sync_task_update", map[string]any{"name": t.Name, "direction": t.Direction, "mode": t.Mode, "interval": t.Interval, "enabled": t.Enabled})
	respondOK(w, map[string]any{"success": true})
}

// DeleteCloudSyncTask: DELETE /api/cloud-sync/tasks/{id}
func DeleteCloudSyncTask(w http.ResponseWriter, r *http.Request) {
	id := mux.Vars(r)["id"]
	name := ""
	err := modifyCloudTasks(func(all []CloudSyncTask) ([]CloudSyncTask, error) {
		out := make([]CloudSyncTask, 0, len(all))
		for _, e := range all {
			if e.ID == id {
				name = e.Name
				continue
			}
			out = append(out, e)
		}
		if name == "" {
			return nil, errors.New("task not found")
		}
		return out, nil
	})
	if err != nil {
		respondErrorSimple(w, err.Error(), http.StatusNotFound)
		return
	}
	audit.LogActivity(r.Header.Get("X-User"), "cloud_sync_task_delete", map[string]any{"name": name})
	respondOK(w, map[string]any{"success": true})
}

// RunCloudSyncTask: POST /api/cloud-sync/tasks/{id}/run  {"dry_run": bool}
func RunCloudSyncTask(w http.ResponseWriter, r *http.Request) {
	id := mux.Vars(r)["id"]
	var req struct {
		DryRun bool `json:"dry_run"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	cloudTasksMu.Lock()
	tasks, err := loadCloudTasks()
	cloudTasksMu.Unlock()
	if err != nil {
		respondErrorSimple(w, err.Error(), http.StatusInternalServerError)
		return
	}
	for _, t := range tasks {
		if t.ID != id {
			continue
		}
		// The folder may have changed since the task was saved.
		if err := validateCloudTask(&t); err != nil {
			respondErrorSimple(w, err.Error(), http.StatusBadRequest)
			return
		}
		jobID, err := startCloudTask(t, req.DryRun, r.Header.Get("X-User"))
		if err != nil {
			respondErrorSimple(w, err.Error(), http.StatusConflict)
			return
		}
		respondOK(w, map[string]any{"success": true, "job_id": jobID})
		return
	}
	respondErrorSimple(w, "task not found", http.StatusNotFound)
}

// tasksUsingRemote names the tasks that use a remote.
func tasksUsingRemote(remote string) []string {
	cloudTasksMu.Lock()
	defer cloudTasksMu.Unlock()
	tasks, _ := loadCloudTasks()
	var names []string
	for _, t := range tasks {
		if t.Remote == remote {
			names = append(names, t.Name)
		}
	}
	return names
}
