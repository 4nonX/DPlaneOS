package gitops

import (
	"database/sql"
	"log"
	"os"
	"sync"
	"time"
)

// ═══════════════════════════════════════════════════════════════════════════════
//  DRIFT DETECTOR  (Task 3.4)
//
//  Runs as a background goroutine. Every interval it:
//    1. Reads state.yaml from the git repo path
//    2. Reads live state from ZFS + DB
//    3. Computes the diff plan
//    4. If any non-NOP items are found → broadcasts a "gitops.drift" WS event
//    5. Records the result in the DB for the UI status endpoint
//
//  The detector does NOT apply anything — it only observes and alerts.
//  Application is always explicit via POST /api/gitops/apply.
// ═══════════════════════════════════════════════════════════════════════════════

// DriftBroadcaster is the interface the detector uses to emit WS events.
// Matches MonitorHub.Broadcast exactly — no import cycle needed.
type DriftBroadcaster interface {
	Broadcast(eventType string, data interface{}, level string)
}

// DriftDetector monitors for divergence between desired and live state.
type DriftDetector struct {
	db           *sql.DB
	stateYAMLPath string   // absolute path to state.yaml in the cloned repo
	interval     time.Duration
	hub          DriftBroadcaster
	stopCh       chan struct{}
	mu           sync.Mutex
	lastResult   *DriftResult
}

// DriftResult is what the UI queries via GET /api/gitops/drift-status.
type DriftResult struct {
	CheckedAt    time.Time  `json:"checked_at"`
	Drifted      bool       `json:"drifted"`
	Plan         *Plan      `json:"plan,omitempty"`
	Error        string     `json:"error,omitempty"`
	StateYAMLPath string    `json:"state_yaml_path"`
}

// NewDriftDetector creates a detector. Call Start() to begin monitoring.
//
//   stateYAMLPath  — full path to state.yaml, e.g. /var/lib/dplaneos/gitops/state.yaml
//   interval       — how often to check; 5 minutes is a reasonable default
func NewDriftDetector(db *sql.DB, stateYAMLPath string, interval time.Duration, hub DriftBroadcaster) *DriftDetector {
	return &DriftDetector{
		db:            db,
		stateYAMLPath: stateYAMLPath,
		interval:      interval,
		hub:           hub,
		stopCh:        make(chan struct{}),
	}
}

// Start launches the background drift-check loop.
func (d *DriftDetector) Start() {
	go d.loop()
	log.Printf("GITOPS DRIFT: detector started — checking every %s", d.interval)
}

// Stop signals the loop to exit cleanly.
func (d *DriftDetector) Stop() {
	close(d.stopCh)
}

// LastResult returns the most recent drift check result (nil if none yet).
func (d *DriftDetector) LastResult() *DriftResult {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.lastResult
}

// CheckNow runs a single drift check synchronously and returns the result.
// Used by the HTTP handler for on-demand checks (GET /api/gitops/status).
func (d *DriftDetector) CheckNow() *DriftResult {
	result := d.runCheck()
	d.mu.Lock()
	d.lastResult = result
	d.mu.Unlock()
	return result
}

// loop is the background goroutine.
func (d *DriftDetector) loop() {
	// Run an immediate check on startup so the UI has data fast
	d.CheckNow()

	ticker := time.NewTicker(d.interval)
	defer ticker.Stop()

	for {
		select {
		case <-d.stopCh:
			log.Printf("GITOPS DRIFT: detector stopped")
			return
		case <-ticker.C:
			result := d.runCheck()
			d.mu.Lock()
			d.lastResult = result
			d.mu.Unlock()
		}
	}
}

// runCheck performs one full drift check cycle.
func (d *DriftDetector) runCheck() *DriftResult {
	result := &DriftResult{
		CheckedAt:     time.Now(),
		StateYAMLPath: d.stateYAMLPath,
	}

	// 1. Read and parse state.yaml
	stateContent, err := readFile(d.stateYAMLPath)
	if err != nil {
		result.Error = "cannot read state.yaml: " + err.Error()
		log.Printf("GITOPS DRIFT: %s", result.Error)
		d.broadcast(result)
		return result
	}

	desired, err := ParseStateYAML(string(stateContent))
	if err != nil {
		result.Error = "invalid state.yaml: " + err.Error()
		log.Printf("GITOPS DRIFT: %s", result.Error)
		d.broadcast(result)
		return result
	}

	// 2. Read live state
	live, err := ReadLiveState(d.db)
	if err != nil {
		result.Error = "cannot read live state: " + err.Error()
		log.Printf("GITOPS DRIFT: %s", result.Error)
		d.broadcast(result)
		return result
	}

	// 3. Compute diff
	plan := ComputeDiff(desired, live)
	result.Plan = plan

	// Drifted = anything other than all-NOP
	result.Drifted = plan.CreateCount+plan.ModifyCount+plan.DeleteCount+plan.BlockedCount > 0

	// 4. Broadcast if drifted
	if result.Drifted {
		log.Printf("GITOPS DRIFT: detected — create=%d modify=%d delete=%d blocked=%d",
			plan.CreateCount, plan.ModifyCount, plan.DeleteCount, plan.BlockedCount)
		d.broadcast(result)
	}

	return result
}

// broadcast emits a WS event. Level reflects the worst item in the plan.
func (d *DriftDetector) broadcast(result *DriftResult) {
	if d.hub == nil {
		return
	}

	level := "info"
	if result.Error != "" {
		level = "warning"
	} else if result.Plan != nil && result.Plan.HasBlocked {
		level = "critical"
	} else if result.Plan != nil && result.Drifted {
		level = "warning"
	}

	d.hub.Broadcast("gitops.drift", map[string]interface{}{
		"drifted":       result.Drifted,
		"error":         result.Error,
		"checked_at":    result.CheckedAt.Format(time.RFC3339),
		"create_count":  safeInt(result.Plan, func(p *Plan) int { return p.CreateCount }),
		"modify_count":  safeInt(result.Plan, func(p *Plan) int { return p.ModifyCount }),
		"delete_count":  safeInt(result.Plan, func(p *Plan) int { return p.DeleteCount }),
		"blocked_count": safeInt(result.Plan, func(p *Plan) int { return p.BlockedCount }),
		"safe_to_apply": result.Plan != nil && result.Plan.SafeToApply,
	}, level)
}

func safeInt(plan *Plan, fn func(*Plan) int) int {
	if plan == nil {
		return 0
	}
	return fn(plan)
}

// readFile reads a file from disk. Isolated here so tests can stub it easily.
func readFile(path string) ([]byte, error) {
	return os.ReadFile(path)
}
