package handlers

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"dplaned/internal/gitops"
)

// ═══════════════════════════════════════════════════════════════════════════════
//  GITOPS HTTP HANDLER  (Phase 3)
//
//  Routes:
//    GET  /api/gitops/status          — last drift result + plan summary
//    GET  /api/gitops/plan            — full diff plan (live computation)
//    POST /api/gitops/apply           — apply the current plan
//    POST /api/gitops/approve         — mark a BLOCKED item as approved
//    POST /api/gitops/check           — trigger an immediate drift check
//    GET  /api/gitops/state           — return current state.yaml content
//    PUT  /api/gitops/state           — write and validate a new state.yaml
//
//  The handler holds no plan cache — every GET /plan computes fresh from ZFS.
//  State (approvals) IS held in memory and in the DB between calls.
// ═══════════════════════════════════════════════════════════════════════════════

// GitOpsHandler is the HTTP handler for all GitOps endpoints.
type GitOpsHandler struct {
	db            *sql.DB
	smbConfPath   string
	stateYAMLPath string
	detector      *gitops.DriftDetector
	// approvals tracks BLOCKED items that the operator has approved.
	// Key: "<kind>/<name>". Cleared after a successful apply.
	approvalsMu sync.Mutex
	approvals   map[string]bool
}

// NewGitOpsHandler constructs the handler and starts the drift detector.
//
//   stateYAMLPath  — absolute path to state.yaml in the git repo
//   smbConfPath    — absolute path to smb.conf for share reloads
//   hub            — WebSocket hub for drift event broadcasting
func NewGitOpsHandler(
	db *sql.DB,
	stateYAMLPath string,
	smbConfPath string,
	hub gitops.DriftBroadcaster,
) *GitOpsHandler {
	detector := gitops.NewDriftDetector(db, stateYAMLPath, 5*time.Minute, hub)
	detector.Start()

	return &GitOpsHandler{
		db:            db,
		smbConfPath:   smbConfPath,
		stateYAMLPath: stateYAMLPath,
		detector:      detector,
		approvals:     make(map[string]bool),
	}
}

// Stop cleans up background goroutines. Call from main defer.
func (h *GitOpsHandler) Stop() {
	h.detector.Stop()
}

// ── GET /api/gitops/status ────────────────────────────────────────────────────

// Status returns the last drift detection result without re-computing.
// For a fresh computation, use /api/gitops/check first.
func (h *GitOpsHandler) Status(w http.ResponseWriter, r *http.Request) {
	result := h.detector.LastResult()
	if result == nil {
		respondOK(w, map[string]interface{}{
			"success": true,
			"status":  "pending",
			"message": "First drift check has not completed yet — try again in a moment",
		})
		return
	}
	respondOK(w, map[string]interface{}{
		"success":       true,
		"drifted":       result.Drifted,
		"checked_at":    result.CheckedAt.Format(time.RFC3339),
		"error":         result.Error,
		"state_yaml":    result.StateYAMLPath,
		"plan_summary":  planSummary(result.Plan),
	})
}

// ── GET /api/gitops/plan ──────────────────────────────────────────────────────

// Plan computes and returns the full diff plan against live state.
// This makes live ZFS calls — may take 1-3 seconds on large pools.
func (h *GitOpsHandler) Plan(w http.ResponseWriter, r *http.Request) {
	desired, err := h.loadDesiredState()
	if err != nil {
		respondError(w, http.StatusUnprocessableEntity, "state.yaml error", err)
		return
	}

	live, err := gitops.ReadLiveState(h.db)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "cannot read live state", err)
		return
	}

	plan := gitops.ComputeDiff(desired, live)
	h.stampApprovals(plan)

	respondOK(w, map[string]interface{}{
		"success":       true,
		"plan":          plan,
		"plan_summary":  planSummary(plan),
		"computed_at":   time.Now().Format(time.RFC3339),
	})
}

// ── POST /api/gitops/apply ────────────────────────────────────────────────────

// Apply computes the current plan and applies it.
// BLOCKED items that have not been approved via /api/gitops/approve halt the apply.
func (h *GitOpsHandler) Apply(w http.ResponseWriter, r *http.Request) {
	desired, err := h.loadDesiredState()
	if err != nil {
		respondError(w, http.StatusUnprocessableEntity, "state.yaml error", err)
		return
	}

	live, err := gitops.ReadLiveState(h.db)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "cannot read live state", err)
		return
	}

	plan := gitops.ComputeDiff(desired, live)
	h.stampApprovals(plan)

	if plan.HasBlocked {
		// Check if all blocked items have been approved
		allApproved := true
		var unapproved []string
		for _, item := range plan.Items {
			if item.Action == gitops.ActionBlocked && !item.Approved {
				allApproved = false
				unapproved = append(unapproved, fmt.Sprintf("%s/%s", item.Kind, item.Name))
			}
		}
		if !allApproved {
			respondOK(w, map[string]interface{}{
				"success":    false,
				"error":      "plan contains BLOCKED items that require explicit approval",
				"unapproved": unapproved,
				"blocked_count": plan.BlockedCount,
				"hint":       "POST /api/gitops/approve with {kind, name} for each blocked item, then re-apply",
			})
			return
		}
	}

	ctx := gitops.ApplyContext{
		DB:          h.db,
		SmbConfPath: h.smbConfPath,
	}

	result, applyErr := gitops.ApplyPlan(ctx, plan)

	if applyErr != nil {
		log.Printf("GITOPS APPLY ERROR: %v", applyErr)
		respondOK(w, map[string]interface{}{
			"success": false,
			"error":   applyErr.Error(),
			"applied": result.Applied,
			"failed":  result.Failed,
		})
		return
	}

	// Clear approvals after successful apply
	h.approvalsMu.Lock()
	h.approvals = make(map[string]bool)
	h.approvalsMu.Unlock()

	log.Printf("GITOPS APPLY: success — %d items in %s", len(result.Applied), result.Duration)
	respondOK(w, map[string]interface{}{
		"success":  true,
		"applied":  result.Applied,
		"count":    len(result.Applied),
		"duration": result.Duration.String(),
	})
}

// ── POST /api/gitops/approve ──────────────────────────────────────────────────

// Approve marks a BLOCKED item as operator-approved, allowing it to be applied.
// Body: { "kind": "dataset", "name": "tank/old-data", "reason": "verified empty" }
func (h *GitOpsHandler) Approve(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Kind   string `json:"kind"`
		Name   string `json:"name"`
		Reason string `json:"reason"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "invalid request body", err)
		return
	}

	if req.Kind == "" || req.Name == "" {
		respondErrorSimple(w, "kind and name are required", http.StatusBadRequest)
		return
	}

	// Validate the item is actually BLOCKED in the current plan before approving.
	// This prevents pre-approving items that haven't been evaluated yet.
	desired, err := h.loadDesiredState()
	if err != nil {
		respondError(w, http.StatusUnprocessableEntity, "state.yaml error", err)
		return
	}
	live, err := gitops.ReadLiveState(h.db)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "cannot read live state", err)
		return
	}
	plan := gitops.ComputeDiff(desired, live)

	found := false
	var blockReason string
	for _, item := range plan.Items {
		if string(item.Kind) == req.Kind && item.Name == req.Name && item.Action == gitops.ActionBlocked {
			found = true
			blockReason = item.BlockReason
			break
		}
	}

	if !found {
		respondOK(w, map[string]interface{}{
			"success": false,
			"error":   fmt.Sprintf("%s/%s is not currently BLOCKED in the plan — re-evaluate before approving", req.Kind, req.Name),
		})
		return
	}

	key := req.Kind + "/" + req.Name
	h.approvalsMu.Lock()
	h.approvals[key] = true
	h.approvalsMu.Unlock()

	// Persist approval to DB so it survives a daemon restart
	h.db.Exec(`
		INSERT INTO gitops_approvals (kind, name, reason, approved_at)
		VALUES (?, ?, ?, datetime('now'))
		ON CONFLICT(kind, name) DO UPDATE SET reason=excluded.reason, approved_at=excluded.approved_at`,
		req.Kind, req.Name, req.Reason,
	)

	log.Printf("GITOPS APPROVE: %s/%s approved — reason: %q", req.Kind, req.Name, req.Reason)
	respondOK(w, map[string]interface{}{
		"success":      true,
		"approved":     key,
		"block_reason": blockReason,
		"message":      fmt.Sprintf("%s/%s approved. Apply the plan to execute.", req.Kind, req.Name),
	})
}

// ── POST /api/gitops/check ────────────────────────────────────────────────────

// Check triggers an immediate drift check and returns the result.
func (h *GitOpsHandler) Check(w http.ResponseWriter, r *http.Request) {
	result := h.detector.CheckNow()
	respondOK(w, map[string]interface{}{
		"success":      true,
		"drifted":      result.Drifted,
		"checked_at":   result.CheckedAt.Format(time.RFC3339),
		"error":        result.Error,
		"plan_summary": planSummary(result.Plan),
	})
}

// ── GET /api/gitops/state ─────────────────────────────────────────────────────

// GetState returns the current state.yaml content as a string.
func (h *GitOpsHandler) GetState(w http.ResponseWriter, r *http.Request) {
	content, err := os.ReadFile(h.stateYAMLPath)
	if err != nil {
		if os.IsNotExist(err) {
			respondOK(w, map[string]interface{}{
				"success": true,
				"exists":  false,
				"content": defaultStateYAML(),
			})
			return
		}
		respondError(w, http.StatusInternalServerError, "cannot read state.yaml", err)
		return
	}
	respondOK(w, map[string]interface{}{
		"success": true,
		"exists":  true,
		"content": string(content),
		"path":    h.stateYAMLPath,
	})
}

// ── PUT /api/gitops/state ─────────────────────────────────────────────────────

// PutState validates and writes a new state.yaml.
// Validation runs before any write — an invalid YAML is rejected entirely.
// Body: { "content": "version: \"1\"\npools: ..." }
func (h *GitOpsHandler) PutState(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Content  string `json:"content"`
		DryRun   bool   `json:"dry_run"` // validate only, do not write
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "invalid request body", err)
		return
	}
	if strings.TrimSpace(req.Content) == "" {
		respondErrorSimple(w, "content is required", http.StatusBadRequest)
		return
	}

	// Validate before writing — fail closed
	if _, err := gitops.ParseStateYAML(req.Content); err != nil {
		respondOK(w, map[string]interface{}{
			"success":  false,
			"valid":    false,
			"error":    err.Error(),
			"message":  "state.yaml validation failed — file NOT written",
		})
		return
	}

	// dry_run: validate only, return success without writing
	if req.DryRun {
		respondOK(w, map[string]interface{}{
			"success": true,
			"valid":   true,
			"message": "state.yaml is valid (dry run — not written)",
		})
		return
	}

	// Ensure directory exists
	if err := os.MkdirAll(filepath.Dir(h.stateYAMLPath), 0755); err != nil {
		respondError(w, http.StatusInternalServerError, "cannot create state directory", err)
		return
	}

	// Write atomically
	tmp := h.stateYAMLPath + ".tmp"
	if err := os.WriteFile(tmp, []byte(req.Content), 0644); err != nil {
		respondError(w, http.StatusInternalServerError, "write failed", err)
		return
	}
	if err := os.Rename(tmp, h.stateYAMLPath); err != nil {
		os.Remove(tmp)
		respondError(w, http.StatusInternalServerError, "rename failed", err)
		return
	}

	log.Printf("GITOPS: state.yaml updated (%d bytes)", len(req.Content))

	// Trigger an immediate check so the UI reflects the new state
	go h.detector.CheckNow()

	respondOK(w, map[string]interface{}{
		"success": true,
		"valid":   true,
		"path":    h.stateYAMLPath,
		"bytes":   len(req.Content),
		"message": "state.yaml saved and validated — drift check triggered",
	})
}

// ── Helpers ───────────────────────────────────────────────────────────────────

// loadDesiredState reads and parses state.yaml from disk.
func (h *GitOpsHandler) loadDesiredState() (*gitops.DesiredState, error) {
	content, err := os.ReadFile(h.stateYAMLPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("state.yaml not found at %s — create it via PUT /api/gitops/state", h.stateYAMLPath)
		}
		return nil, fmt.Errorf("reading state.yaml: %w", err)
	}
	return gitops.ParseStateYAML(string(content))
}

// stampApprovals marks plan items as Approved if the operator has approved them.
func (h *GitOpsHandler) stampApprovals(plan *gitops.Plan) {
	h.approvalsMu.Lock()
	defer h.approvalsMu.Unlock()
	for i, item := range plan.Items {
		key := string(item.Kind) + "/" + item.Name
		if h.approvals[key] {
			plan.Items[i].Approved = true
		}
	}
}

// planSummary returns a compact map suitable for the status endpoint.
func planSummary(plan *gitops.Plan) map[string]interface{} {
	if plan == nil {
		return nil
	}
	return map[string]interface{}{
		"create_count":  plan.CreateCount,
		"modify_count":  plan.ModifyCount,
		"delete_count":  plan.DeleteCount,
		"blocked_count": plan.BlockedCount,
		"nop_count":     plan.NopCount,
		"has_blocked":   plan.HasBlocked,
		"safe_to_apply": plan.SafeToApply,
	}
}

// defaultStateYAML returns an annotated starter template when no state.yaml exists yet.
func defaultStateYAML() string {
	return `# D-PlaneOS state.yaml — declarative NAS configuration
# version must be "1"
version: "1"

# pools: declare ZFS pools.
# IMPORTANT: disks MUST use /dev/disk/by-id/ paths.
# Using /dev/sdX paths is REJECTED — they change across reboots.
pools:
  - name: tank
    vdev_type: mirror          # mirror, raidz, raidz2, raidz3, or "" (stripe)
    disks:
      - /dev/disk/by-id/ata-WDC_WD140EDFZ_REPLACE_WITH_REAL_ID
      - /dev/disk/by-id/ata-WDC_WD140EDFZ_REPLACE_WITH_REAL_ID
    ashift: 12                 # 12 = 4096-byte sectors (recommended for modern drives)
    options:
      compression: lz4
      atime: "off"

# datasets: declare ZFS datasets.
# Destroying a non-empty dataset is always BLOCKED until manually approved.
datasets:
  - name: tank/data
    quota: 2T
    compression: lz4
    atime: "off"
    mountpoint: /mnt/data

# shares: declare SMB shares.
# Removing a share with active connections is BLOCKED.
shares:
  - name: data
    path: /mnt/data
    read_only: false
    valid_users: "@users"
    comment: "Main data share"
`
}
