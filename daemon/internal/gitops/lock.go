package gitops

import (
	"sync"
)

// reconcileMu ensures only one NixOS rebuild or ZFS apply runs at a time.
// Callers use TryLock/Unlock; it is separate from stateMu below.
var reconcileMu sync.Mutex

// TryLock attempts to acquire the reconciliation lock without blocking.
// Returns true if acquired, false if another reconciliation is in progress.
func TryLock() bool {
	return reconcileMu.TryLock()
}

// Unlock releases the reconciliation lock.
func Unlock() {
	reconcileMu.Unlock()
}

// stateMu serializes all operations that read from or write to the git state
// repository. CommitAll and ApplyPlan both hold a write lock for their full
// duration so they cannot race against each other or against concurrent
// background commit goroutines.
var stateMu sync.Mutex
