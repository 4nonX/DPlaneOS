//go:build linux

package ha

import (
	"log"
	"os"
	"sync"
	"sync/atomic"
)

// watchdogState manages the open /dev/watchdog file descriptor.
// The kernel resets the node if the file is not written within the
// configured timeout. Writing 'V' before closing disables the watchdog
// gracefully (magic close).
type watchdogState struct {
	mu      sync.Mutex
	f       *os.File
	petting atomic.Bool // true while quorum is healthy and we should keep petting
}

var globalWatchdog watchdogState

// openWatchdog opens the watchdog device. Must be called once at startup.
func openWatchdog(device string) error {
	globalWatchdog.mu.Lock()
	defer globalWatchdog.mu.Unlock()
	if globalWatchdog.f != nil {
		return nil
	}
	f, err := os.OpenFile(device, os.O_WRONLY, 0)
	if err != nil {
		return err
	}
	globalWatchdog.f = f
	return nil
}

// petWatchdog writes to the watchdog device to prevent a hardware reset.
// Safe to call from multiple goroutines.
func petWatchdog() {
	globalWatchdog.mu.Lock()
	defer globalWatchdog.mu.Unlock()
	if globalWatchdog.f == nil {
		return
	}
	if _, err := globalWatchdog.f.Write([]byte("1")); err != nil {
		log.Printf("HA WATCHDOG: pet failed: %v (watchdog will fire in remaining timeout)", err)
	}
}

// shutdownWatchdog writes the magic 'V' character and closes the device,
// which gracefully disables the watchdog on supported drivers. Call on
// clean daemon shutdown only.
func shutdownWatchdog() {
	globalWatchdog.mu.Lock()
	defer globalWatchdog.mu.Unlock()
	if globalWatchdog.f == nil {
		return
	}
	_, _ = globalWatchdog.f.Write([]byte("V"))
	_ = globalWatchdog.f.Close()
	globalWatchdog.f = nil
	log.Printf("HA WATCHDOG: graceful shutdown (magic close)")
}
