package handlers

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"dplaned/internal/cmdutil"
	"dplaned/internal/gitops"
	"dplaned/internal/jobs"
	"log"
)

// NixOSGuardHandler provides NixOS configuration validation and management
// These endpoints only function on NixOS systems
type NixOSGuardHandler struct {
	db *sql.DB
}

func NewNixOSGuardHandler(db *sql.DB) *NixOSGuardHandler {
	return &NixOSGuardHandler{db: db}
}

// BackupConfig handles POST /api/nixos/backup-config
// It commits all changes in /etc/nixos and pushes to the configured NixOS Git repository.
func (h *NixOSGuardHandler) BackupConfig(w http.ResponseWriter, r *http.Request) {
	// 1. Get NixOS Repo ID from GitOps config
	var repoID sql.NullInt64
	err := h.db.QueryRow(`SELECT nixos_repo_id FROM gitops_config WHERE id=1`).Scan(&repoID)
	if err != nil || !repoID.Valid {
		respondJSON(w, 400, map[string]interface{}{
			"success": false,
			"error":   "NixOS backup repository not configured. Set it up in Infrastructure Sync.",
		})
		return
	}

	// 2. Ensure /etc/nixos is a git repo
	const nixDir = "/etc/nixos"
	// Get repo details (URL/Branch) to initialize if needed
	var repoURL, branch string
	h.db.QueryRow(`SELECT repo_url, branch FROM git_sync_repos WHERE id=$1`, repoID.Int64).Scan(&repoURL, &branch)

	if err := gitops.EnsureRepoRootDir(nixDir, repoURL, branch, nil); err != nil {
		respondJSON(w, 500, map[string]interface{}{"success": false, "error": "Failed to initialize Git in /etc/nixos: " + err.Error()})
		return
	}

	// 3. Authenticate and Push
	env := gitops.BuildPushEnvForRepoID(h.db, repoID.Int64)
	defer gitops.CleanupAskpass()

	// We use the same identity as the NixOS repo if set, otherwise default
	var name, email string
	h.db.QueryRow(`SELECT commit_name, commit_email FROM git_sync_repos WHERE id=$1`, repoID.Int64).Scan(&name, &email)

	if err := gitops.CommitAndPush(nixDir, env, "feat: NixOS configuration backup via D-PlaneOS", name, email, branch); err != nil {
		respondJSON(w, 500, map[string]interface{}{"success": false, "error": err.Error()})
		return
	}

	respondJSON(w, 200, map[string]interface{}{"success": true, "message": "NixOS configuration successfully backed up"})
}

// IsNixOS checks if we're running on NixOS
func IsNixOS() bool {
	_, err := os.Stat("/etc/NIXOS")
	return err == nil
}

// DetectNixOS reports whether the system is NixOS
// GET /api/nixos/detect
func (h *NixOSGuardHandler) DetectNixOS(w http.ResponseWriter, r *http.Request) {
	nixos := IsNixOS()

	result := map[string]interface{}{
		"success": true,
		"is_nixos": nixos,
	}

	if nixos {
		// Get current NixOS version
		if data, err := os.ReadFile("/etc/os-release"); err == nil {
			for _, line := range strings.Split(string(data), "\n") {
				if strings.HasPrefix(line, "VERSION_ID=") {
					result["nixos_version"] = strings.Trim(strings.TrimPrefix(line, "VERSION_ID="), "\"")
				}
			}
		}

		// Get current generation
		if out, err := cmdutil.RunMedium("nixos-rebuild", "list-generations", "--no-build-nix"); err == nil {
			lines := strings.Split(strings.TrimSpace(string(out)), "\n")
			if len(lines) > 0 {
				result["current_generation"] = strings.TrimSpace(lines[len(lines)-1])
				result["total_generations"] = len(lines)
			}
		}
	}

	respondOK(w, result)
}

// Status reports whether there are pending changes that need reconciliation.
// GET /api/nixos/status
func (h *NixOSGuardHandler) GetStatus(w http.ResponseWriter, r *http.Request) {
	if !IsNixOS() {
		respondOK(w, map[string]interface{}{"success": true, "is_nixos": false})
		return
	}

	dirty, err := NixWriter.IsDirty()
	if err != nil {
		log.Printf("NixOS Status: IsDirty failed: %v", err)
	}

	respondOK(w, map[string]interface{}{
		"success":  true,
		"is_nixos": true,
		"is_dirty": dirty,
	})
}

// DiffIntent reports the detailed changes between current intent and applied state.
// GET /api/nixos/diff-intent
func (h *NixOSGuardHandler) DiffIntent(w http.ResponseWriter, r *http.Request) {
	if !IsNixOS() {
		respondOK(w, map[string]interface{}{"success": true, "changes": []interface{}{}})
		return
	}

	changes, err := NixWriter.DiffIntent()
	if err != nil {
		respondError(w, http.StatusInternalServerError, "Failed to calculate diff", err)
		return
	}

	respondOK(w, map[string]interface{}{
		"success": true,
		"changes": changes,
		"count":   len(changes),
	})
}

// Reconcile triggers a nixos-rebuild switch to apply pending changes.
// POST /api/nixos/reconcile
func (h *NixOSGuardHandler) Reconcile(w http.ResponseWriter, r *http.Request) {
	if !IsNixOS() {
		respondErrorSimple(w, "Not a NixOS system", http.StatusBadRequest)
		return
	}

	jobID := jobs.Start("nixos-reconcile", func(j *jobs.Job) {
		j.Log("Starting NixOS system reconciliation...")

		// 1. Double check if dirty
		dirty, err := NixWriter.IsDirty()
		if err != nil {
			j.Log(fmt.Sprintf("Warning: Checksum verification error: %v", err))
		}
		if !dirty {
			j.Log("System is already in sync. NixWriter report: NOT_DIRTY.")
			// We still run the rebuild to be safe, or should we stop?
			// User might have manually edited files. Let's proceed.
		}

		// 2. Run rebuild switch
		j.Log("Executing nixos-rebuild switch...")
		output, err := cmdutil.RunSlow("nixos-rebuild", "switch")
		if err != nil {
			j.Log("NixOS reconciliation failed!")
			j.Fail(fmt.Sprintf("Rebuild failed: %v\nOutput: %s", err, output))
			return
		}

		// 3. Mark as applied on success
		j.Log("Rebuild successful. Committing checksum to applied-state registry...")
		if err := NixWriter.MarkApplied(); err != nil {
			j.Log(fmt.Sprintf("Warning: Failed to mark state as applied: %v", err))
		}

		j.Log("System successfully reconciled with declarative intent.")
		j.Done(map[string]interface{}{
			"output": string(output),
		})
	})

	respondOK(w, map[string]interface{}{
		"success": true,
		"job_id":  jobID,
		"message": "Reconciliation started in background",
	})
}

// ValidateConfig dry-runs a NixOS configuration change
// POST /api/nixos/validate { "config_snippet": "..." }
func (h *NixOSGuardHandler) ValidateConfig(w http.ResponseWriter, r *http.Request) {
	if !IsNixOS() {
		respondErrorSimple(w, "Not a NixOS system", http.StatusBadRequest)
		return
	}

	var req struct {
		FlakePath string `json:"flake_path"` // path to flake directory (optional)
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondErrorSimple(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	// Default flake path
	flakePath := req.FlakePath
	if flakePath == "" {
		flakePath = "/etc/nixos"
	}
	if strings.ContainsAny(flakePath, ";|&$`\\\"'") {
		respondErrorSimple(w, "Invalid path", http.StatusBadRequest)
		return
	}

	start := time.Now()

	// Try dry-activate first (validates without applying)
	var output string
	var err error

	// Check if it's a flake
	flakeNix := flakePath + "/flake.nix"
	if _, statErr := os.Stat(flakeNix); statErr == nil {
		// Flake-based system
		output, err = executeCommand("nixos-rebuild", []string{
			"dry-activate", "--flake", flakePath + "#dplaneos",
		})
	} else {
		// Traditional configuration.nix
		output, err = executeCommand("nixos-rebuild", []string{
			"dry-activate",
		})
	}
	duration := time.Since(start)

	if err != nil {
		respondOK(w, map[string]interface{}{
			"success":     false,
			"valid":       false,
			"error":       fmt.Sprintf("Configuration invalid: %v", err),
			"output":      output,
			"duration_ms": duration.Milliseconds(),
		})
		return
	}

	respondOK(w, map[string]interface{}{
		"success":     true,
		"valid":       true,
		"message":     "Configuration is valid and can be applied",
		"output":      output,
		"duration_ms": duration.Milliseconds(),
	})
}

// ListGenerations lists NixOS system generations (rollback points)
// GET /api/nixos/generations
func (h *NixOSGuardHandler) ListGenerations(w http.ResponseWriter, r *http.Request) {
	if !IsNixOS() {
		respondErrorSimple(w, "Not a NixOS system", http.StatusBadRequest)
		return
	}

	output, err := executeCommand("nix-env", []string{
		"--list-generations", "--profile", "/nix/var/nix/profiles/system",
	})
	if err != nil {
		respondOK(w, map[string]interface{}{
			"success":     true,
			"generations": []interface{}{},
			"error":       "Could not list generations",
		})
		return
	}

	type Generation struct {
		ID      string `json:"id"`
		Date    string `json:"date"`
		Current bool   `json:"current"`
	}

	var generations []Generation
	for _, line := range strings.Split(strings.TrimSpace(output), "\n") {
		if line == "" {
			continue
		}
		current := strings.Contains(line, "(current)")
		// Clean up the line
		clean := strings.TrimSpace(strings.Replace(line, "(current)", "", 1))
		parts := strings.Fields(clean)
		if len(parts) >= 3 {
			generations = append(generations, Generation{
				ID:      parts[0],
				Date:    strings.Join(parts[1:], " "),
				Current: current,
			})
		}
	}

	respondOK(w, map[string]interface{}{
		"success":     true,
		"generations": generations,
		"count":       len(generations),
	})
}

// RollbackGeneration switches to a previous NixOS generation
// POST /api/nixos/rollback { "generation": "42" }
func (h *NixOSGuardHandler) RollbackGeneration(w http.ResponseWriter, r *http.Request) {
	if !IsNixOS() {
		respondErrorSimple(w, "Not a NixOS system", http.StatusBadRequest)
		return
	}

	var req struct {
		Generation string `json:"generation"` // empty = previous, or specific number
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondErrorSimple(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	start := time.Now()
	var output string
	var err error

	if req.Generation == "" {
		// Simple rollback to previous
		output, err = executeCommand("nixos-rebuild", []string{
			"switch", "--rollback",
		})
	} else {
		// Validate generation ID (must be numeric)
		for _, c := range req.Generation {
			if c < '0' || c > '9' {
				respondErrorSimple(w, "Invalid generation ID (must be numeric)", http.StatusBadRequest)
				return
			}
		}
		output, err = executeCommand("nix-env", []string{
			"--switch-generation", req.Generation,
			"--profile", "/nix/var/nix/profiles/system",
		})
		if err == nil {
			// Activate the generation
			output, err = executeCommand(
				fmt.Sprintf("/nix/var/nix/profiles/system-%s-link/activate", req.Generation),
				[]string{},
			)
		}
	}
	duration := time.Since(start)

	if err != nil {
		respondOK(w, map[string]interface{}{
			"success":     false,
			"error":       fmt.Sprintf("Rollback failed: %v", err),
			"output":      output,
			"duration_ms": duration.Milliseconds(),
		})
		return
	}

	respondOK(w, map[string]interface{}{
		"success":     true,
		"message":     "System rolled back successfully",
		"generation":  req.Generation,
		"output":      output,
		"duration_ms": duration.Milliseconds(),
	})
}

// ListPreUpgradeSnapshots returns all pre-upgrade ZFS snapshots recorded in DB.
// GET /api/nixos/pre-upgrade-snapshots
func (h *NixOSGuardHandler) ListPreUpgradeSnapshots(w http.ResponseWriter, r *http.Request) {
	type snap struct {
		ID          int64  `json:"id"`
		Snapshot    string `json:"snapshot"`
		Pool        string `json:"pool"`
		CreatedAt   string `json:"created_at"`
		NixOSApply  string `json:"nixos_apply"`
		Success     int    `json:"success"`
		Error       string `json:"error,omitempty"`
	}

	rows, err := h.db.Query(`
		SELECT id, snapshot, pool, created_at, nixos_apply, success, error
		FROM pre_upgrade_snapshots
		ORDER BY id DESC
		LIMIT 100
	`)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "Failed to query snapshots", err)
		return
	}
	defer rows.Close()

	var snaps []snap
	for rows.Next() {
		var s snap
		if err := rows.Scan(&s.ID, &s.Snapshot, &s.Pool, &s.CreatedAt, &s.NixOSApply, &s.Success, &s.Error); err != nil {
			continue
		}
		snaps = append(snaps, s)
	}

	respondOK(w, map[string]interface{}{
		"success":   true,
		"snapshots": snaps,
		"count":     len(snaps),
	})
}
