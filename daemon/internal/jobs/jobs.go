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

// Status values
const (
	StatusRunning  = "running"
	StatusDone     = "done"
	StatusFailed   = "failed"
)

// Job holds state for one background operation.
type Job struct {
	ID        string                 `json:"id"`
	Type      string                 `json:"type"`
	Status    string                 `json:"status"`
	Result    map[string]interface{} `json:"result,omitempty"`
	Error     string                 `json:"error,omitempty"`
	StartedAt time.Time              `json:"started_at"`
	FinishedAt *time.Time            `json:"finished_at,omitempty"`

	mu sync.Mutex
}

// Done marks the job as completed with a result payload.
func (j *Job) Done(result map[string]interface{}) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.Status = StatusDone
	j.Result = result
	now := time.Now()
	j.FinishedAt = &now
}

// Fail marks the job as failed with an error message.
func (j *Job) Fail(errMsg string) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.Status = StatusFailed
	j.Error = errMsg
	now := time.Now()
	j.FinishedAt = &now
}

// Snapshot returns a copy of the job safe to marshal without holding the lock.
func (j *Job) Snapshot() Job {
	j.mu.Lock()
	defer j.mu.Unlock()
	return Job{
		ID:         j.ID,
		Type:       j.Type,
		Status:     j.Status,
		Result:     j.Result,
		Error:      j.Error,
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
