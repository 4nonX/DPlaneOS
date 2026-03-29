package gitops

import (
	"sync"
)

// ReconcileMu is a global mutex that ensures only one reconciliation
// (NixOS rebuild or ZFS apply) is running at a time.
var reconcileMu sync.Mutex

// TryLock attempts to acquire the global reconciliation lock without blocking.
// Returns true if acquired, false if another reconciliation is in progress.
func TryLock() bool {
	return reconcileMu.TryLock()
}

// Unlock releases the global reconciliation lock.
func Unlock() {
	reconcileMu.Unlock()
}
