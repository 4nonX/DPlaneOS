package gitops

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"os"
	"path/filepath"
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
//  The detector does NOT apply anything - it only observes and alerts.
//  Application is always explicit via POST /api/gitops/apply.
// ═══════════════════════════════════════════════════════════════════════════════

// DriftBroadcaster is the interface the detector uses to emit WS events.
// Matches MonitorHub.Broadcast exactly - no import cycle needed.
type DriftBroadcaster interface {
	Broadcast(eventType string, data any, level string)
}

// DriftDetector monitors for divergence between desired and live state.
type DriftDetector struct {
	db           *sql.DB
	stateYAMLPath string   // absolute path to state.yaml in the cloned repo
	interval     time.Duration
	hub          DriftBroadcaster
	stopCh       chan struct{}
	wg           sync.WaitGroup
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
//   stateYAMLPath  - full path to state.yaml, e.g. /var/lib/dplaneos/gitops/state.yaml
//   interval       - how often to check; 5 minutes is a reasonable default
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
	d.wg.Add(1)
	go d.loop()
	log.Printf("GITOPS DRIFT: detector started - checking every %s", d.interval)
}

// Stop signals the loop to exit and waits for it to return.
func (d *DriftDetector) Stop() {
	close(d.stopCh)
	d.wg.Wait()
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
	defer d.wg.Done()
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
			// Only run if enabled
			var enabled int
			qctx, qcancel := context.WithTimeout(context.Background(), 5*time.Second)
			err := d.db.QueryRowContext(qctx, "SELECT enabled FROM gitops_config WHERE id = 1").Scan(&enabled)
			qcancel()
			if err != nil {
				log.Printf("GITOPS DRIFT: failed to read config: %v", err)
				continue
			}
			if enabled == 1 {
				result := d.runCheck()
				d.mu.Lock()
				d.lastResult = result
				d.mu.Unlock()
			}
		}
	}
}

// runCheck performs one full drift check cycle.
func (d *DriftDetector) runCheck() *DriftResult {
	result := &DriftResult{
		CheckedAt:     time.Now(),
		StateYAMLPath: d.stateYAMLPath,
	}

	// 0. Check enabled and pull if repo is configured
	var enabled int
	var repoID sql.NullInt64
	rctx, rcancel := context.WithTimeout(context.Background(), 5*time.Second)
	err := d.db.QueryRowContext(rctx, "SELECT enabled, repo_id FROM gitops_config WHERE id = 1").Scan(&enabled, &repoID)
	rcancel()
	if err != nil {
		result.Error = "failed to read gitops config: " + err.Error()
		return result
	}

	if enabled == 0 {
		result.Error = "GitOps is disabled"
		return result
	}


	if repoID.Valid {
		// Bring the clone up to date so drift is measured against what is in
		// the repository, not against the last state this node happened to see.
		if err := d.pullRepo(repoID.Int64); err != nil {
			result.Error = "cannot update from the Git repository: " + err.Error()
			log.Printf("GITOPS DRIFT: %s", result.Error)
			d.broadcast(result)
			return result
		}
	}

	// The clone is kept current on every node (fetching is read-only), so a
	// standby promoted after a failover starts from the latest state.yaml.
	// Drift itself is only meaningful on the writer: a standby's live state
	// lacks the pools the active node owns and would report everything as drifted.
	if ok, reason := IsWriter(); !ok {
		result.Error = "drift checks run on the GitOps writer; this node: " + reason
		return result
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
		log.Printf("GITOPS DRIFT: detected - create=%d modify=%d delete=%d blocked=%d",
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

	d.hub.Broadcast("gitops.drift", map[string]any{
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

// pullRepo fetches the configured repository into the state clone (the
// directory holding d.stateYAMLPath) and fast-forwards it. Holds stateMu so it
// cannot interleave with CommitAll or an apply.
func (d *DriftDetector) pullRepo(repoID int64) error {
	var repoURL, branch sql.NullString
	if err := d.db.QueryRow(`SELECT repo_url, branch FROM git_sync_repos WHERE id = $1`, repoID).Scan(&repoURL, &branch); err != nil {
		return fmt.Errorf("loading repository %d: %w", repoID, err)
	}
	dir := filepath.Dir(d.stateYAMLPath)
	env := BuildPushEnvForRepoID(d.db, repoID)
	defer CleanupAskpass()

	stateMu.Lock()
	defer stateMu.Unlock()
	if err := EnsureRepoRootDir(dir, repoURL.String, branchOrMain(branch.String), env); err != nil {
		return err
	}
	_, err := syncClone(dir, env, branchOrMain(branch.String))
	return err
}
