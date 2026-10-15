package gitops

import (
	"database/sql"
	"strings"
	"sync"
)

// Changes made in the web interface reach the Git repository through the
// Git backend (internal/gitsync, Design 0001 phase 5): every change is
// recorded as a revision of the configuration store, and the node that owns
// the change's scope commits it. Handlers call CommitAll / CommitAllAsync
// after a change; that records it (change hooks) and asks the backend to
// synchronise soon. Nothing is written to Git inline.

var (
	gitNotifyMu sync.RWMutex
	gitNotify   func()
)

// SetGitNotify registers the Git backend's trigger.
func SetGitNotify(fn func()) {
	gitNotifyMu.Lock()
	gitNotify = fn
	gitNotifyMu.Unlock()
}

// CommitAllAsync reports a configuration change made through the web interface.
func CommitAllAsync(db *sql.DB) {
	go func() { _ = CommitAll(db) }()
}

// CommitAll reports a configuration change: the change hooks record it in
// the configuration store, then the Git backend is asked to synchronise.
func CommitAll(_ *sql.DB) error {
	runChangeHooks()
	gitNotifyMu.RLock()
	fn := gitNotify
	gitNotifyMu.RUnlock()
	if fn != nil {
		fn()
	}
	return nil
}

// FullyApplied reports whether an apply applied every item: pool operations
// deferred by the HA guard leave the live system behind the desired state.
func FullyApplied(r *ApplyResult) bool {
	if r == nil || r.Failed != "" {
		return false
	}
	for _, a := range r.Applied {
		if strings.HasPrefix(a, "[DEFERRED") {
			return false
		}
	}
	return true
}

// DriftBroadcaster emits events over the WebSocket hub.
type DriftBroadcaster interface {
	Broadcast(eventType string, data any, level string)
}
