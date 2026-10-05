// Package monitoring provides observability for DPlaneOS.
// health_check.go: Phase 3.3 - Subsystem health aggregation
package monitoring

import (
	"context"
	"fmt"
	"log"
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
	HealthOK        HealthStatus = "ok"
	HealthDegraded  HealthStatus = "degraded"
	HealthUnavailable HealthStatus = "unavailable"
	HealthUnknown   HealthStatus = "unknown"
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
	Overall    HealthStatus                    `json:"overall"`
	Subsystems map[string]SubsystemHealth     `json:"subsystems"`
	CheckedAt  time.Time                      `json:"checked_at"`
	Timestamp  time.Time                      `json:"timestamp"`
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

	// Hardware-aware checks. Without a profile, assume no optional hardware.
	hw := c.hwProfile
	if hw == nil {
		hw = &hardware.Profile{BMCType: "none"}
	}
	if hw.BMCType != "none" {
		c.RegisterCheck("bmc", c.checkBMC)
	} else {
		c.DisableCheck("bmc")
	}

	if hw.Capabilities["ses_enclosure"] {
		c.RegisterCheck("enclosure", c.checkEnclosure)
	} else {
		c.RegisterCheck("storage_monitoring", c.checkStorageMonitoring) // Fallback to S.M.A.R.T
	}

	if hw.BondingSupport {
		c.RegisterCheck("bonding", c.checkBonding)
	}

	if hw.VLANSupport {
		c.RegisterCheck("vlan", c.checkVLAN)
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

func (c *Checker) checkBMC(ctx context.Context) SubsystemHealth {
	// TODO: Implement BMC health check (IPMI ping, sensor read)
	return SubsystemHealth{
		Name:      "bmc",
		Status:    HealthUnknown,
		CheckedAt: time.Now(),
	}
}

func (c *Checker) checkEnclosure(ctx context.Context) SubsystemHealth {
	// TODO: Implement SES enclosure health check
	return SubsystemHealth{
		Name:      "enclosure",
		Status:    HealthUnknown,
		CheckedAt: time.Now(),
	}
}

func (c *Checker) checkStorageMonitoring(ctx context.Context) SubsystemHealth {
	// Fallback S.M.A.R.T monitoring when SES unavailable
	return SubsystemHealth{
		Name:      "storage_monitoring",
		Status:    HealthUnknown,
		CheckedAt: time.Now(),
	}
}

func (c *Checker) checkBonding(ctx context.Context) SubsystemHealth {
	// TODO: Check bond status
	return SubsystemHealth{
		Name:      "bonding",
		Status:    HealthUnknown,
		CheckedAt: time.Now(),
	}
}

func (c *Checker) checkVLAN(ctx context.Context) SubsystemHealth {
	// TODO: Check VLAN configuration
	return SubsystemHealth{
		Name:      "vlan",
		Status:    HealthUnknown,
		CheckedAt: time.Now(),
	}
}

func (c *Checker) checkZFS(ctx context.Context) SubsystemHealth {
	// TODO: Check ZFS pool status via `zpool status -x`
	return SubsystemHealth{
		Name:      "zfs",
		Status:    HealthUnknown,
		CheckedAt: time.Now(),
	}
}

func (c *Checker) checkDocker(ctx context.Context) SubsystemHealth {
	// TODO: Check Docker daemon responsiveness
	return SubsystemHealth{
		Name:      "docker",
		Status:    HealthUnknown,
		CheckedAt: time.Now(),
	}
}

func (c *Checker) checkPostgres(ctx context.Context) SubsystemHealth {
	// TODO: Check PostgreSQL connection pool + replication lag if HA
	return SubsystemHealth{
		Name:      "postgres",
		Status:    HealthUnknown,
		CheckedAt: time.Now(),
	}
}

func (c *Checker) checkNetwork(ctx context.Context) SubsystemHealth {
	// TODO: Check network interfaces + routing
	return SubsystemHealth{
		Name:      "network",
		Status:    HealthUnknown,
		CheckedAt: time.Now(),
	}
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
