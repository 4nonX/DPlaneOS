// Package bootstrap starts the daemon's monitoring parts.
// phases.go: resource watcher, hardware profile, circuit breakers, health
// checks and the structured event log, started after the database is ready.
package bootstrap

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"runtime"
	"time"

	"dplaned/internal/audit"
	"dplaned/internal/hardware"
	"dplaned/internal/monitoring"
	"dplaned/internal/resilience"
	"dplaned/internal/resource"
)

// AllPhases holds the monitoring parts.
type AllPhases struct {
	ResourceWatcher *resource.Watcher
	HardwareProfile any // *hardware.SystemProfile (avoid direct dependency)
	CircuitPool     *resilience.Pool
	HealthChecker   *monitoring.Checker
	EventLogger     *audit.EventLogger

	db *sql.DB
}

// InitializeAllPhases starts the parts in dependency order: the resource
// watcher, the hardware profile and circuit breakers, the health checker (uses
// both) and the event log. Call it from main() after the database is ready.
func InitializeAllPhases(ctx context.Context, db *sql.DB) (*AllPhases, error) {
	phases := &AllPhases{db: db}

	log.Println("[BOOTSTRAP] Starting the resource watcher")
	watcher, err := initPhase1()
	if err != nil {
		return nil, fmt.Errorf("resource watcher: %w", err)
	}
	phases.ResourceWatcher = watcher

	log.Println("[BOOTSTRAP] Detecting hardware")
	hwProfile, circuitPool, err := initPhase2()
	if err != nil {
		return nil, fmt.Errorf("hardware detection: %w", err)
	}
	phases.HardwareProfile = hwProfile
	phases.CircuitPool = circuitPool

	log.Println("[BOOTSTRAP] Starting the health checks")
	phases.HealthChecker = initPhase3(db, watcher, circuitPool)

	phases.EventLogger = audit.NewEventLogger(db)

	wirePhaseCallbacks(ctx, phases)

	log.Println("[BOOTSTRAP] Starting background services")
	phases.ResourceWatcher.Start()
	phases.HealthChecker.Start(ctx)

	return phases, nil
}

// initPhase1 creates resource monitoring.
func initPhase1() (*resource.Watcher, error) {
	// Configure check interval and memory limit based on system capacity
	checkInterval := 5 * time.Second
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	memoryLimitBytes := m.Alloc + 1024*1024*1024 // Alloc + 1GB headroom

	diskPaths := []string{"/persist", "/"}

	watcher := resource.NewWatcher(checkInterval, memoryLimitBytes, diskPaths)

	// Thresholds are baked into check() method:
	// Disk: 95% (DEGRADED), 98% (CRITICAL)
	// Memory: checked dynamically
	// File descriptors: checked via /proc/sys/fs/file-nr

	return watcher, nil
}

// initPhase2 detects the hardware and creates the circuit breaker pool.
func initPhase2() (any, *resilience.Pool, error) {
	profile, err := hardware.DetectHardware()
	if err != nil {
		return nil, nil, err
	}
	return profile, resilience.NewPool(), nil
}

// initPhase3 creates the health checker.
func initPhase3(db *sql.DB, watcher *resource.Watcher, circuitPool *resilience.Pool) *monitoring.Checker {
	checker := monitoring.NewChecker(watcher, circuitPool, nil)
	checker.SetDB(db)
	checker.InitializeDefaultChecks()
	return checker
}

// wirePhaseCallbacks logs resource levels and writes critical ones and the
// hardware detection to the event log.
func wirePhaseCallbacks(ctx context.Context, phases *AllPhases) {
	phases.ResourceWatcher.SetCriticalCallback(func(status resource.ResourceStatus) {
		log.Printf("[BOOTSTRAP] Resource critical: disk %d%%, memory %d%%, file descriptors %d%%",
			status.DiskUsagePercent, status.MemoryPercent, status.FileDescriptorPercent)
		for name, percent := range status.CriticalResources() {
			phases.EventLogger.LogResourceCritical(ctx, name, percent)
		}
	})

	phases.ResourceWatcher.SetDegradedCallback(func(status resource.ResourceStatus) {
		log.Printf("[BOOTSTRAP] Resource degraded: %s", status.Warnings)
	})

	phases.EventLogger.LogHardwareDetected(ctx, map[string]any{
		"detected": phases.HardwareProfile != nil,
	})
}

// Close stops the resource watcher.
func (ap *AllPhases) Close() error {
	log.Println("[BOOTSTRAP] Shutting down")
	ap.ResourceWatcher.Stop()
	return nil
}
