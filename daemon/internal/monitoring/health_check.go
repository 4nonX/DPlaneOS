// Package monitoring provides observability for DPlaneOS.
// health_check.go: Phase 3.3 - Subsystem health aggregation
package monitoring

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"os"
	"sync"
	"time"

	"dplaned/internal/features"
	"dplaned/internal/hardware"
	"dplaned/internal/resilience"
	"dplaned/internal/resource"
)

// HealthStatus represents the overall health of the system.
type HealthStatus string

const (
	HealthOK          HealthStatus = "ok"
	HealthDegraded    HealthStatus = "degraded"
	HealthUnavailable HealthStatus = "unavailable"
	HealthUnknown     HealthStatus = "unknown"
)

// SubsystemHealth represents health of a single subsystem.
type SubsystemHealth struct {
	Name      string                 `json:"name"`
	Status    HealthStatus           `json:"status"`
	Reason    string                 `json:"reason,omitempty"`
	Details   map[string]interface{} `json:"details,omitempty"`
	CheckedAt time.Time              `json:"checked_at"`
}

// SystemHealth aggregates all subsystem health.
type SystemHealth struct {
	Overall    HealthStatus               `json:"overall"`
	Subsystems map[string]SubsystemHealth `json:"subsystems"`
	CheckedAt  time.Time                  `json:"checked_at"`
	Timestamp  time.Time                  `json:"timestamp"`
}

// Checker performs health checks across all subsystems.
type Checker struct {
	mu              sync.RWMutex
	lastHealth      *SystemHealth
	checkInterval   time.Duration
	resourceWatcher *resource.Watcher
	circuitPool     *resilience.Pool
	hwProfile       *hardware.Profile
	featureManager  *features.Manager
	enabledChecks   map[string]bool
	db              *sql.DB
	checks          map[string]func(context.Context) SubsystemHealth
	log             func(string, ...interface{})
}

// NewChecker creates a health checker.
func NewChecker(
	resourceWatcher *resource.Watcher,
	circuitPool *resilience.Pool,
	hwProfile *hardware.Profile,
	featureManager *features.Manager,
) *Checker {
	return &Checker{
		lastHealth:      &SystemHealth{Subsystems: make(map[string]SubsystemHealth)},
		checkInterval:   5 * time.Second,
		resourceWatcher: resourceWatcher,
		circuitPool:     circuitPool,
		hwProfile:       hwProfile,
		featureManager:  featureManager,
		enabledChecks:   make(map[string]bool),
		checks:          make(map[string]func(context.Context) SubsystemHealth),
		log: func(msg string, args ...interface{}) {
			log.Printf("[HEALTH-CHECK] "+msg, args...)
		},
	}
}

// SetDB gives the PostgreSQL check its connection.
func (c *Checker) SetDB(db *sql.DB) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.db = db
}

// RegisterCheck registers a health check for a subsystem.
func (c *Checker) RegisterCheck(name string, fn func(context.Context) SubsystemHealth) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.checks[name] = fn
	c.enabledChecks[name] = true
	c.log("Registered health check: %s", name)
}

// EnableCheck enables a check by name.
func (c *Checker) EnableCheck(name string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.enabledChecks[name] = true
	c.log("Enabled health check: %s", name)
}

// DisableCheck disables a check by name.
func (c *Checker) DisableCheck(name string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.enabledChecks[name] = false
	c.log("Disabled health check: %s", name)
}

// Check performs all enabled health checks and returns aggregated status.
func (c *Checker) Check(ctx context.Context) SystemHealth {
	// Copy the enabled checks and release the lock before running them:
	// storing the result below takes the write lock.
	c.mu.RLock()
	enabled := make(map[string]func(context.Context) SubsystemHealth, len(c.checks))
	for name, check := range c.checks {
		if c.enabledChecks[name] {
			enabled[name] = check
		}
	}
	c.mu.RUnlock()

	health := SystemHealth{
		Subsystems: make(map[string]SubsystemHealth),
		CheckedAt:  time.Now(),
		Timestamp:  time.Now(),
	}

	// Run all enabled checks in parallel
	var wg sync.WaitGroup
	resultsChan := make(chan SubsystemHealth, len(enabled))

	for name, check := range enabled {
		wg.Add(1)
		go func(name string, check func(context.Context) SubsystemHealth) {
			defer wg.Done()
			resultsChan <- check(ctx)
		}(name, check)
	}

	wg.Wait()
	close(resultsChan)

	// Collect results
	for sh := range resultsChan {
		health.Subsystems[sh.Name] = sh
	}

	// Determine overall health
	health.Overall = c.aggregateStatus(health.Subsystems)

	c.mu.Lock()
	c.lastHealth = &health
	c.mu.Unlock()

	return health
}

// GetLastHealth returns the most recent health check result.
func (c *Checker) GetLastHealth() SystemHealth {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.lastHealth == nil {
		return SystemHealth{Overall: HealthUnknown, Subsystems: make(map[string]SubsystemHealth)}
	}
	return *c.lastHealth
}

// Start begins periodic health checking.
func (c *Checker) Start(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(c.checkInterval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				c.Check(ctx)
			}
		}
	}()
}

// InitializeDefaultChecks sets up built-in health checks.
func (c *Checker) InitializeDefaultChecks() {
	// Phase 1: Resource monitoring
	c.RegisterCheck("resources", c.checkResources)

	// Phase 2: Circuit breakers
	c.RegisterCheck("external_services", c.checkCircuitBreakers)

	// Network bonds, where the kernel has any.
	if _, err := os.Stat("/proc/net/bonding"); err == nil {
		c.RegisterCheck("bonding", c.checkBonding)
	}

	// Always-enabled checks
	c.RegisterCheck("zfs", c.checkZFS)
	c.RegisterCheck("docker", c.checkDocker)
	c.RegisterCheck("postgres", c.checkPostgres)
	c.RegisterCheck("network", c.checkNetwork)

	c.log("Initialized %d default health checks", len(c.checks))
}

// Private check implementations

func (c *Checker) checkResources(ctx context.Context) SubsystemHealth {
	if c.resourceWatcher == nil {
		return SubsystemHealth{
			Name:      "resources",
			Status:    HealthUnknown,
			Reason:    "Resource watcher not initialized",
			CheckedAt: time.Now(),
		}
	}

	status := c.resourceWatcher.GetStatus()
	sh := SubsystemHealth{
		Name:      "resources",
		CheckedAt: time.Now(),
		Details: map[string]interface{}{
			"disk_usage_percent":      status.DiskUsagePercent,
			"disk_available_gb":       status.DiskAvailableGB,
			"memory_percent":          status.MemoryPercent,
			"file_descriptor_percent": status.FileDescriptorPercent,
		},
	}

	switch status.Status {
	case "OK":
		sh.Status = HealthOK
	case "DEGRADED":
		sh.Status = HealthDegraded
		sh.Reason = fmt.Sprintf("Resource degraded: %v", status.Warnings)
	case "CRITICAL":
		sh.Status = HealthUnavailable
		sh.Reason = fmt.Sprintf("Resource critical: %v", status.Warnings)
	default:
		sh.Status = HealthUnknown
	}

	return sh
}

func (c *Checker) checkCircuitBreakers(ctx context.Context) SubsystemHealth {
	if c.circuitPool == nil {
		return SubsystemHealth{
			Name:      "external_services",
			Status:    HealthUnknown,
			CheckedAt: time.Now(),
		}
	}

	sh := SubsystemHealth{
		Name:      "external_services",
		CheckedAt: time.Now(),
		Details:   make(map[string]interface{}),
	}

	allStats := c.circuitPool.GetAll()
	openCount := 0

	for service, stats := range allStats {
		state, _ := stats["state"].(string)
		if state == "open" {
			openCount++
		}
		sh.Details[service] = stats
	}

	if openCount == 0 {
		sh.Status = HealthOK
		sh.Reason = fmt.Sprintf("All %d external services operational", len(allStats))
	} else if openCount < len(allStats)/2 {
		sh.Status = HealthDegraded
		sh.Reason = fmt.Sprintf("%d/%d external services degraded", openCount, len(allStats))
	} else {
		sh.Status = HealthUnavailable
		sh.Reason = fmt.Sprintf("Majority of external services unavailable (%d/%d)", openCount, len(allStats))
	}

	return sh
}

// aggregateStatus determines overall health from subsystems.
func (c *Checker) aggregateStatus(subsystems map[string]SubsystemHealth) HealthStatus {
	unavailableCount := 0
	degradedCount := 0
	totalChecked := 0

	for _, sh := range subsystems {
		totalChecked++
		switch sh.Status {
		case HealthUnavailable:
			unavailableCount++
		case HealthDegraded:
			degradedCount++
		}
	}

	if totalChecked == 0 {
		return HealthUnknown
	}

	// Any critical subsystem down = system unavailable
	if unavailableCount > 0 {
		return HealthUnavailable
	}

	// Any degradation = system degraded
	if degradedCount > 0 {
		return HealthDegraded
	}

	return HealthOK
}
