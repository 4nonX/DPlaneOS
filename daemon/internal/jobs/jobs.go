// Package jobs provides a minimal in-process job store for long-running operations.
//
// Usage:
//
//	id := jobs.Start("zfs_send", func(j *jobs.Job) {
//	    output, err := cmdutil.RunSlow("zfs", "send", "-R", snapshot)
//	    if err != nil {
//	        j.Fail(err.Error())
//	        return
//	    }
//	    j.Done(map[string]interface{}{"output": string(output)})
//	})
//	// Return id to the caller immediately; they poll GET /api/jobs/{id}
package jobs

import (
	"sync"
	"time"

	"github.com/google/uuid"
)

var broadcastCallback func(event string, data interface{}, level string)

// SetBroadcastCallback sets a global repository for job status broadcasts.
func SetBroadcastCallback(cb func(event string, data interface{}, level string)) {
	broadcastCallback = cb
}

// Status values
const (
	StatusRunning = "running"
	StatusDone    = "done"
	StatusFailed  = "failed"
)

// Job holds state for one background operation.
type Job struct {
	ID         string                 `json:"id"`
	Type       string                 `json:"type"`
	Status     string                 `json:"status"`
	Result     map[string]interface{} `json:"result,omitempty"`
	Error      string                 `json:"error,omitempty"`
	Logs       []string               `json:"logs,omitempty"` // streaming progress lines
	StartedAt  time.Time              `json:"started_at"`
	FinishedAt *time.Time             `json:"finished_at,omitempty"`

	mu sync.Mutex
}

// Log appends a progress line visible to callers polling GET /api/jobs/{id}.
// Safe to call from the job goroutine at any time.
func (j *Job) Log(line string) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.Logs = append(j.Logs, line)
}

// Progress broadcasts a structured progress update via WebSocket.
func (j *Job) Progress(data interface{}) {
	if broadcastCallback != nil {
		go broadcastCallback("job.progress", map[string]interface{}{
			"job_id": j.ID,
			"data":   data,
		}, "info")
	}
}

// Done marks the job as completed with a result payload.
func (j *Job) Done(result map[string]interface{}) {
	j.mu.Lock()
	j.Status = StatusDone
	j.Result = result
	now := time.Now()
	j.FinishedAt = &now
	j.mu.Unlock()

	if broadcastCallback != nil {
		go broadcastCallback("job.completed", map[string]interface{}{
			"job_id":   j.ID,
			"job_type": j.Type,
			"success":  true,
			"message":  "Job completed successfully",
			"result":   result,
		}, "info")
	}
}

// Fail marks the job as failed with an error message.
func (j *Job) Fail(errMsg string) {
	j.mu.Lock()
	j.Status = StatusFailed
	j.Error = errMsg
	now := time.Now()
	j.FinishedAt = &now
	j.mu.Unlock()

	if broadcastCallback != nil {
		go broadcastCallback("job.failed", map[string]interface{}{
			"job_id":   j.ID,
			"job_type": j.Type,
			"success":  false,
			"message":  errMsg,
		}, "error")
	}
}

// JobSnapshot is a mutex-free copy of Job fields, safe to marshal directly.
type JobSnapshot struct {
	ID         string                 `json:"id"`
	Type       string                 `json:"type"`
	Status     string                 `json:"status"`
	Result     map[string]interface{} `json:"result,omitempty"`
	Error      string                 `json:"error,omitempty"`
	Logs       []string               `json:"logs,omitempty"`
	StartedAt  time.Time              `json:"started_at"`
	FinishedAt *time.Time             `json:"finished_at,omitempty"`
}

// Snapshot returns a mutex-free copy of the job safe to marshal without holding the lock.
func (j *Job) Snapshot() JobSnapshot {
	j.mu.Lock()
	defer j.mu.Unlock()
	logsCopy := make([]string, len(j.Logs))
	copy(logsCopy, j.Logs)
	return JobSnapshot{
		ID:         j.ID,
		Type:       j.Type,
		Status:     j.Status,
		Result:     j.Result,
		Error:      j.Error,
		Logs:       logsCopy,
		StartedAt:  j.StartedAt,
		FinishedAt: j.FinishedAt,
	}
}

// store is the global in-process job registry.
var store = &jobStore{
	jobs: make(map[string]*Job),
}

type jobStore struct {
	mu   sync.RWMutex
	jobs map[string]*Job
}

// Start creates a job, launches fn in a goroutine, and returns the job ID.
func Start(jobType string, fn func(j *Job)) string {
	j := &Job{
		ID:        uuid.New().String(),
		Type:      jobType,
		Status:    StatusRunning,
		StartedAt: time.Now(),
	}

	store.mu.Lock()
	store.jobs[j.ID] = j
	store.mu.Unlock()

	go fn(j)

	return j.ID
}

// Get returns a job by ID, or nil if not found.
func Get(id string) *Job {
	store.mu.RLock()
	defer store.mu.RUnlock()
	return store.jobs[id]
}

// StartReaper runs a background goroutine that removes finished jobs older
// than maxAge. Call once from main(). Prevents unbounded memory growth.
func StartReaper(maxAge time.Duration) {
	go func() {
		ticker := time.NewTicker(maxAge / 2)
		defer ticker.Stop()
		for range ticker.C {
			cutoff := time.Now().Add(-maxAge)
			store.mu.Lock()
			for id, j := range store.jobs {
				j.mu.Lock()
				finished := j.FinishedAt != nil && j.FinishedAt.Before(cutoff)
				j.mu.Unlock()
				if finished {
					delete(store.jobs, id)
				}
			}
			store.mu.Unlock()
		}
	}()
}
