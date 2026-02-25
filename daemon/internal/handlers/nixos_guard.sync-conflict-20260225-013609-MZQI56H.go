package handlers

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"dplaned/internal/cmdutil"
	"strings"
	"time"
)

// NixOSGuardHandler provides NixOS configuration validation and management
// These endpoints only function on NixOS systems
type NixOSGuardHandler struct {
	db *sql.DB
}

func NewNixOSGuardHandler(db *sql.DB) *NixOSGuardHandler {
	return &NixOSGuardHandler{db: db}
}

// isNixOS checks if we're running on NixOS
func isNixOS() bool {
	_, err := os.Stat("/etc/NIXOS")
	return err == nil
}

// DetectNixOS reports whether the system is NixOS
// GET /api/nixos/detect
func (h *NixOSGuardHandler) DetectNixOS(w http.ResponseWriter, r *http.Request) {
	nixos := isNixOS()

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

// ValidateConfig dry-runs a NixOS configuration change
// POST /api/nixos/validate { "config_snippet": "..." }
func (h *NixOSGuardHandler) ValidateConfig(w http.ResponseWriter, r *http.Request) {
	if !isNixOS() {
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
		output, err = executeCommand("/run/current-system/sw/bin/nixos-rebuild", []string{
			"dry-activate", "--flake", flakePath + "#dplaneos",
		})
	} else {
		// Traditional configuration.nix
		output, err = executeCommand("/run/current-system/sw/bin/nixos-rebuild", []string{
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
	if !isNixOS() {
		respondErrorSimple(w, "Not a NixOS system", http.StatusBadRequest)
		return
	}

	output, err := executeCommand("/run/current-system/sw/bin/nix-env", []string{
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
	if !isNixOS() {
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
		output, err = executeCommand("/run/current-system/sw/bin/nixos-rebuild", []string{
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
		output, err = executeCommand("/run/current-system/sw/bin/nix-env", []string{
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
