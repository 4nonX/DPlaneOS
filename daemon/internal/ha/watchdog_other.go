//go:build !linux

package ha

// Watchdog is a no-op on non-Linux platforms (development / test builds).
func openWatchdog(_ string) error { return nil }
func petWatchdog()                {}
func shutdownWatchdog()           {}
func closeWatchdog()              {}
