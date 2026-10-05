// Package bootstrap provides initialization for all hardening phases.
// phases.go: Wiring and bootstrapping all Phase 1-5 components at startup
package bootstrap

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"runtime"
	"time"

	"dplaned/internal/audit"
	"dplaned/internal/features"
	"dplaned/internal/gitops"
	"dplaned/internal/hardware"
	"dplaned/internal/monitoring"
	"dplaned/internal/resilience"
	"dplaned/internal/resource"
	"dplaned/internal/rollback"
)

// AllPhases wraps all five hardening phase components for dependency injection.
type AllPhases struct {
	// Phase 1: Resource Exhaustion Monitoring
	ResourceWatcher *resource.Watcher

	// Phase 2: Hardware Detection + Feature Gating + Resilience
	HardwareProfile any // *hardware.SystemProfile (avoid direct dependency)
	FeatureManager  *features.Manager
	CircuitPool     *resilience.Pool

	// Phase 3.1: State Machine for Crash Recovery
	StateMachine *gitops.StateMachine

	// Phase 3.3: Health Check Aggregation
	HealthChecker *monitoring.Checker

	// Phase 4: Immutable Audit Logging
	EventLogger *audit.EventLogger

	// Phase 5: Automatic Rollback & Recovery
	RollbackManager *rollback.Manager

	db *sql.DB
}

// InitializeAllPhases boots all components in correct dependency order.
// Call this from main() after database is ready.
//
// Initialization Order:
// 1. Database + migrations
// 2. Phase 1: Resource watcher (independent)
// 3. Phase 2: Hardware detection + feature flags + circuit breaker pool (uses Phase 1)
// 4. Phase 3.1: State machine (uses database)
// 5. Phase 3.3: Health checker (uses Phase 1 watcher, Phase 2 circuits, Phase 2 hardware)
// 6. Phase 4: Event logger (uses database)
// 7. Phase 5: Rollback manager (uses Phase 3.1, 2, 4)
// 8. Wire callbacks for integration
// 9. Start background services (Phase 1 watcher, Phase 3.3 health checks)
func InitializeAllPhases(ctx context.Context, db *sql.DB, repoPath string) (*AllPhases, error) {
	phases := &AllPhases{db: db}

	log.Println("[BOOTSTRAP] Initializing Phase 1: Resource Monitoring")
	watcher, err := initPhase1()
	if err != nil {
		return nil, fmt.Errorf("phase 1 init failed: %w", err)
	}
	phases.ResourceWatcher = watcher

	log.Println("[BOOTSTRAP] Initializing Phase 2: Hardware Detection & Feature Gating")
	hwProfile, featureMgr, circuitPool, err := initPhase2(ctx, db)
	if err != nil {
		return nil, fmt.Errorf("phase 2 init failed: %w", err)
	}
	phases.HardwareProfile = hwProfile
	phases.FeatureManager = featureMgr
	phases.CircuitPool = circuitPool

	log.Println("[BOOTSTRAP] Initializing Phase 3.1: State Machine")
	stateMachine, err := initPhase3_1(db)
	if err != nil {
		return nil, fmt.Errorf("phase 3.1 init failed: %w", err)
	}
	phases.StateMachine = stateMachine

	log.Println("[BOOTSTRAP] Initializing Phase 3.3: Health Checks")
	healthChecker, err := initPhase3_3(watcher, circuitPool)
	if err != nil {
		return nil, fmt.Errorf("phase 3.3 init failed: %w", err)
	}
	phases.HealthChecker = healthChecker

	log.Println("[BOOTSTRAP] Initializing Phase 4: Audit Logging")
	eventLogger, err := initPhase4(db)
	if err != nil {
		return nil, fmt.Errorf("phase 4 init failed: %w", err)
	}
	phases.EventLogger = eventLogger

	log.Println("[BOOTSTRAP] Initializing Phase 5: Rollback & Recovery")
	rollbackMgr, err := initPhase5(db, stateMachine, featureMgr, eventLogger, repoPath)
	if err != nil {
		return nil, fmt.Errorf("phase 5 init failed: %w", err)
	}
	phases.RollbackManager = rollbackMgr

	log.Println("[BOOTSTRAP] Wiring phase callbacks for integration")
	if err := wirePhaseCallbacks(ctx, phases); err != nil {
		return nil, fmt.Errorf("callback wiring failed: %w", err)
	}

	log.Println("[BOOTSTRAP] Performing recovery of incomplete operations")
	if !rollbackMgr.RecoverIncompleteOperations(ctx) {
		log.Println("[BOOTSTRAP] WARNING: Operation recovery incomplete, manual intervention may be needed")
	}

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

// initPhase2 detects hardware and initializes feature flags + circuit breakers.
func initPhase2(ctx context.Context, db *sql.DB) (
	any, *features.Manager, *resilience.Pool, error) {

	// Detect hardware
	profile, err := hardware.DetectHardware()
	if err != nil {
		return nil, nil, nil, fmt.Errorf("hardware detection failed: %w", err)
	}

	// Initialize feature manager
	featureMgr := features.NewManager(db)
	if err := featureMgr.LoadFromDB(ctx); err != nil {
		return nil, nil, nil, fmt.Errorf("load feature flags failed: %w", err)
	}

	// Register built-in features
	registerBuiltInFeatures(featureMgr)

	// Initialize circuit breaker pool
	circuitPool := resilience.NewPool()

	return profile, featureMgr, circuitPool, nil
}

// initPhase3_1 initializes the state machine for crash recovery.
func initPhase3_1(db *sql.DB) (*gitops.StateMachine, error) {
	sm := gitops.NewStateMachine(db)
	return sm, nil
}

// initPhase3_3 initializes health check aggregation.
func initPhase3_3(watcher *resource.Watcher, circuitPool *resilience.Pool) (*monitoring.Checker, error) {

	// Note: In production, would type-assert hwProfile and pass to NewChecker
	// For now, create checker without profile dependency
	checker := monitoring.NewChecker(watcher, circuitPool, nil, nil)
	checker.InitializeDefaultChecks()
	return checker, nil
}

// initPhase4 initializes structured event logging.
func initPhase4(db *sql.DB) (*audit.EventLogger, error) {
	eventLogger := audit.NewEventLogger(db)
	return eventLogger, nil
}

// initPhase5 initializes rollback manager.
func initPhase5(db *sql.DB, sm *gitops.StateMachine,
	fm *features.Manager, el *audit.EventLogger, repoPath string) (*rollback.Manager, error) {

	rm := rollback.NewManager(db, sm, fm, el, repoPath)
	return rm, nil
}

// wirePhaseCallbacks connects all phases for cross-phase communication.
// This shows the integration pattern; actual implementations may vary based on API.
func wirePhaseCallbacks(ctx context.Context, phases *AllPhases) error {
	// Phase 1 → Phase 5: Resource exhaustion triggers rollback decision
	phases.ResourceWatcher.SetCriticalCallback(func(status resource.ResourceStatus) {
		allow := phases.RollbackManager.OnResourceExhausted(ctx, "disk", status.DiskUsagePercent)
		log.Printf("[BOOTSTRAP] Resource critical, rollback decision: allow=%v", allow)
		// HTTP middleware checks this and returns 507 if allow=false
	})

	phases.ResourceWatcher.SetDegradedCallback(func(status resource.ResourceStatus) {
		log.Printf("[BOOTSTRAP] Resource degraded: %s", status.Warnings)
	})

	// Phase 4: Log hardware detection results at startup
	hwDetails := map[string]any{
		"detected": phases.HardwareProfile != nil,
	}
	phases.EventLogger.LogHardwareDetected(ctx, hwDetails)

	// Phase 2 → Phase 4: Feature state changes logged
	// Note: Register callbacks as features are enabled/disabled via HTTP
	// Typical pattern in handler: eventLogger.LogFeatureChange(ctx, featureID, newState, reason)

	// Phase 2 → Phase 4 & Phase 5: Circuit breaker state changes
	// Note: Circuit breakers would call this on state transitions
	// Typical pattern in circuit breaker: eventLogger.LogCircuitStateChange(ctx, service, newState)

	return nil
}

// registerBuiltInFeatures registers all available features with their defaults.
func registerBuiltInFeatures(fm *features.Manager) {
	features.RegisterBuiltIns(fm)
}

// Close gracefully shuts down all phase components.
func (ap *AllPhases) Close() error {
	log.Println("[BOOTSTRAP] Shutting down all phases")

	// Phase 1: Resource watcher cleanup
	ap.ResourceWatcher.Stop()

	// Phase 3.3: Health checker cleanup
	// (implement Stop method if needed)

	// Phase 4: Audit logger cleanup
	// (no explicit cleanup needed)

	// Phase 5: Rollback manager cleanup
	ap.RollbackManager.Close()

	return nil
}
