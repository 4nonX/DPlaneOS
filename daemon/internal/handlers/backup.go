package handlers

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"dplaned/internal/audit"
	"dplaned/internal/cmdutil"
	"dplaned/internal/jobs"
	"dplaned/internal/security"

	"github.com/google/uuid"
	"github.com/gorilla/mux"
)

// BackupTask records one rsync job, persisted to ConfigDir/backup-tasks.json.
type BackupTask struct {
	ID         string     `json:"id"`
	Source     string     `json:"source"`
	Dest       string     `json:"dest"`
	Options    string     `json:"options"`
	Status     string     `json:"status"` // running, done, failed
	StartedAt  time.Time  `json:"started_at"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
	ExitCode   int        `json:"exit_code"`
	JobID      string     `json:"job_id"`
}

var (
	backupTasksMu sync.Mutex
)

func backupTasksFile() string {
	return filepath.Join(ConfigDir, "backup-tasks.json")
}

// loadTasks reads all tasks from the JSON file. Returns empty slice on any error.
func loadTasks() []*BackupTask {
	backupTasksMu.Lock()
	defer backupTasksMu.Unlock()

	data, err := os.ReadFile(backupTasksFile())
	if err != nil {
		return []*BackupTask{}
	}
	var tasks []*BackupTask
	if err := json.Unmarshal(data, &tasks); err != nil {
		return []*BackupTask{}
	}
	return tasks
}

// saveTasks writes the task list to disk. Must be called with backupTasksMu held.
func saveTasks(tasks []*BackupTask) error {
	data, err := json.MarshalIndent(tasks, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(ConfigDir, 0755); err != nil {
		return err
	}
	return os.WriteFile(backupTasksFile(), data, 0600)
}

// appendTask adds a new task and saves the file. Thread-safe.
func appendTask(t *BackupTask) {
	backupTasksMu.Lock()
	defer backupTasksMu.Unlock()

	data, _ := os.ReadFile(backupTasksFile())
	var tasks []*BackupTask
	json.Unmarshal(data, &tasks) // ignore error — start fresh if corrupt
	tasks = append(tasks, t)
	saveTasks(tasks) //nolint:errcheck
}

// updateTask applies fn to the task with the given ID and saves. Thread-safe.
func updateTask(id string, fn func(t *BackupTask)) {
	backupTasksMu.Lock()
	defer backupTasksMu.Unlock()

	data, _ := os.ReadFile(backupTasksFile())
	var tasks []*BackupTask
	json.Unmarshal(data, &tasks)
	for _, t := range tasks {
		if t.ID == id {
			fn(t)
			break
		}
	}
	saveTasks(tasks) //nolint:errcheck
}

// ExecuteRsync handles GET (list tasks) and POST (enqueue rsync backup job).
// POST returns {"job_id": "..."} immediately; poll GET /api/jobs/{id} for status.
func ExecuteRsync(w http.ResponseWriter, r *http.Request) {
	user := r.Header.Get("X-User")
	sessionID := r.Header.Get("X-Session-ID")

	if valid, _ := security.ValidateSession(sessionID, user); !valid {
		respondErrorSimple(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	// GET: return newest 50 tasks from the JSON file
	if r.Method == http.MethodGet {
		all := loadTasks()
		// Sort newest first
		sort.Slice(all, func(i, j int) bool {
			return all[i].StartedAt.After(all[j].StartedAt)
		})
		limit := 50
		if len(all) < limit {
			limit = len(all)
		}
		respondOK(w, map[string]interface{}{
			"success": true,
			"tasks":   all[:limit],
		})
		return
	}

	if r.Method != http.MethodPost {
		respondErrorSimple(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		Source      string `json:"source"`
		Destination string `json:"destination"`
		Options     string `json:"options"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondErrorSimple(w, "Invalid request", http.StatusBadRequest)
		return
	}

	taskID := uuid.New().String()
	task := &BackupTask{
		ID:        taskID,
		Source:    req.Source,
		Dest:      req.Destination,
		Options:   req.Options,
		Status:    jobs.StatusRunning,
		StartedAt: time.Now(),
	}

	src, dst := req.Source, req.Destination
	jobID := jobs.Start("rsync_backup", func(j *jobs.Job) {
		output, err := cmdutil.RunSlow("rsync", "-avz", "--progress", src, dst)
		audit.LogActivity(user, "rsync_backup", map[string]interface{}{
			"source":      src,
			"destination": dst,
			"success":     err == nil,
		})

		now := time.Now()
		if err != nil {
			j.Fail(err.Error())
			updateTask(taskID, func(t *BackupTask) {
				t.Status = jobs.StatusFailed
				t.FinishedAt = &now
				t.ExitCode = 1
			})
			return
		}
		j.Done(map[string]interface{}{"output": string(output)})
		updateTask(taskID, func(t *BackupTask) {
			t.Status = jobs.StatusDone
			t.FinishedAt = &now
			t.ExitCode = 0
		})
	})

	task.JobID = jobID
	appendTask(task)

	respondOK(w, map[string]interface{}{"job_id": jobID, "task_id": taskID})
}

// DeleteBackupTask removes a task by ID from the JSON file.
// Route: DELETE /api/backup/rsync/{id}
func DeleteBackupTask(w http.ResponseWriter, r *http.Request) {
	user := r.Header.Get("X-User")
	sessionID := r.Header.Get("X-Session-ID")

	if valid, _ := security.ValidateSession(sessionID, user); !valid {
		respondErrorSimple(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	id := mux.Vars(r)["id"]
	if id == "" {
		respondErrorSimple(w, "Missing task ID", http.StatusBadRequest)
		return
	}

	backupTasksMu.Lock()
	defer backupTasksMu.Unlock()

	data, _ := os.ReadFile(backupTasksFile())
	var tasks []*BackupTask
	json.Unmarshal(data, &tasks)

	found := false
	filtered := make([]*BackupTask, 0, len(tasks))
	for _, t := range tasks {
		if t.ID == id {
			found = true
			continue
		}
		filtered = append(filtered, t)
	}

	if !found {
		respondErrorSimple(w, "Task not found", http.StatusNotFound)
		return
	}

	if err := saveTasks(filtered); err != nil {
		http.Error(w, "Failed to save tasks", http.StatusInternalServerError)
		return
	}

	respondOK(w, map[string]interface{}{"success": true, "deleted": id})
}
