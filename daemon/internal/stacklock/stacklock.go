// Package stacklock allows one compose run per stack at a time across the
// daemon: the stack handlers, the storage groups that start and stop a
// group's stacks, and GitOps. Two runs on one project at once fail ("the
// container name is already in use") or leave it half-applied.
package stacklock

import "sync"

var locks sync.Map // name -> *sync.Mutex

func mutex(name string) *sync.Mutex {
	m, _ := locks.LoadOrStore(name, &sync.Mutex{})
	return m.(*sync.Mutex)
}

// TryLock takes the stack's lock if it is free.
func TryLock(name string) (unlock func(), ok bool) {
	mu := mutex(name)
	if !mu.TryLock() {
		return nil, false
	}
	return mu.Unlock, true
}

// Lock waits for the stack's lock.
func Lock(name string) (unlock func()) {
	mu := mutex(name)
	mu.Lock()
	return mu.Unlock
}
