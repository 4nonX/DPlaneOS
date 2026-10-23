package stacklock

import (
	"testing"
	"time"
)

func TestTryLockAndLock(t *testing.T) {
	unlock, ok := TryLock("media")
	if !ok {
		t.Fatal("free lock not taken")
	}
	if _, ok := TryLock("media"); ok {
		t.Fatal("held lock taken twice")
	}
	if u, ok := TryLock("other"); !ok {
		t.Fatal("another stack's lock not taken")
	} else {
		u()
	}

	got := make(chan struct{})
	go func() {
		u := Lock("media")
		close(got)
		u()
	}()
	select {
	case <-got:
		t.Fatal("Lock returned while the lock was held")
	case <-time.After(50 * time.Millisecond):
	}
	unlock()
	select {
	case <-got:
	case <-time.After(5 * time.Second):
		t.Fatal("Lock did not return after the unlock")
	}
}
