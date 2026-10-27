package handlers

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"
)

var maintPoolRe = regexp.MustCompile(`^[a-zA-Z0-9_\-]+$`)

// poolDependencyCheck returns a human-readable list of active dependencies that
// block destruction or forced export of a pool. It checks:
//   - Datasets with active mountpoints in /proc/mounts
//   - NFS exports whose path lives under the pool
//   - SMB shares whose path lives under the pool
//
// An empty slice means the pool has no active dependencies.
func poolDependencyCheck(db *sql.DB, poolName string) ([]string, error) {
	// Enumerate all dataset mountpoints for this pool.
	out, err := executeCommandWithTimeout(TimeoutFast, "zfs",
		[]string{"list", "-H", "-r", "-o", "name,mountpoint", poolName})
	if err != nil {
		return nil, fmt.Errorf("zfs list: %w", err)
	}

	// Build a set of active mountpoints (those actually present in /proc/mounts).
	mountsOut, _ := executeCommandWithTimeout(TimeoutFast, "cat", []string{"/proc/mounts"})
	activeMounts := map[string]bool{}
	for line := range strings.SplitSeq(mountsOut, "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 {
			activeMounts[fields[1]] = true
		}
	}

	var activePaths []string
	for line := range strings.SplitSeq(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 || fields[1] == "-" || fields[1] == "none" {
			continue
		}
		if activeMounts[fields[1]] {
			activePaths = append(activePaths, fields[1])
		}
	}

	if db == nil {
		return activePaths, nil
	}

	// Check NFS exports under this pool.
	rows, err := db.Query(`SELECT path FROM nfs_exports WHERE enabled = 1`)
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var path string
			if rows.Scan(&path) == nil && strings.HasPrefix(path, "/") {
				for _, mp := range activePaths {
					if path == mp || strings.HasPrefix(path, mp+"/") {
						activePaths = append(activePaths, "NFS export: "+path)
						break
					}
				}
			}
		}
	}

	// Check SMB shares under this pool.
	rows2, err := db.Query(`SELECT path FROM smb_shares WHERE enabled = 1`)
	if err == nil {
		defer rows2.Close()
		for rows2.Next() {
			var path string
			if rows2.Scan(&path) == nil {
				for _, mp := range activePaths {
					if path == mp || strings.HasPrefix(path, mp+"/") {
						activePaths = append(activePaths, "SMB share: "+path)
						break
					}
				}
			}
		}
	}

	return activePaths, nil
}

// HandlePoolDestroy handles POST /api/zfs/pools/destroy
// Now a method on ZFSHandler to access the database for dependency checks.
func (h *ZFSHandler) HandlePoolDestroy(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name  string `json:"name"`
		Force bool   `json:"force"` // when true, bypasses soft dependency check (see comment below)
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondErrorSimple(w, "Invalid request body", http.StatusBadRequest)
		return
	}
	if !maintPoolRe.MatchString(req.Name) {
		respondErrorSimple(w, "invalid pool name", http.StatusBadRequest)
		return
	}

	// Dependency check: refuse to destroy a pool that has active mounts or shares.
	// With force=false (default): block if any dependency is found.
	// With force=true: bypass the soft dependency check. The caller is responsible
	// for ensuring services are stopped. This is intentional: if a share entry is
	// corrupt and cannot be removed, force allows recovery without being permanently
	// blocked by a dependency that cannot be cleared. The confirmRoute wrapper
	// already requires the operator to type the pool name before this executes.
	// Contrast with ExportPool (reversible): force there still checks, because a
	// forced export with open handles causes client-side data corruption. A forced
	// destroy is final either way; clients lose access regardless.
	if !req.Force {
		deps, err := poolDependencyCheck(h.db, req.Name)
		if err == nil && len(deps) > 0 {
			guidedPoolDestroyBlocked(w, req.Name, deps)
			return
		}
	}

	if _, err := executeCommandWithTimeout(TimeoutSlow, "zpool", []string{"destroy", req.Name}); err != nil {
		respondErrorSimple(w, "Failed to destroy pool: "+err.Error(), http.StatusInternalServerError)
		return
	}
	respondOK(w, CommandResponse{Success: true, Output: "Pool " + req.Name + " destroyed"})
}

// PoolFeature represents a single ZFS feature flag.
type PoolFeature struct {
	Name        string `json:"name"`
	State       string `json:"state"` // active | enabled | disabled
	Description string `json:"description,omitempty"`
}

// PoolCheckpointStatus contains checkpoint information for a pool.
type PoolCheckpointStatus struct {
	Pool          string `json:"pool"`
	HasCheckpoint bool   `json:"has_checkpoint"`
	Size          string `json:"size,omitempty"`
}

// GetCheckpointStatus handles GET /api/zfs/checkpoint
func GetCheckpointStatus(w http.ResponseWriter, r *http.Request) {
	pool := r.URL.Query().Get("pool")
	if pool != "" && !maintPoolRe.MatchString(pool) {
		respondErrorSimple(w, "invalid pool name", http.StatusBadRequest)
		return
	}

	// zpool list -H -o name,checkpoint
	args := []string{"list", "-H", "-o", "name,checkpoint"}
	if pool != "" {
		args = append(args, pool)
	}
	out, err := executeCommandWithTimeout(TimeoutFast, "zpool", args)
	if err != nil {
		respondErrorSimple(w, "Failed to get checkpoint status: "+err.Error(), http.StatusInternalServerError)
		return
	}

	var statuses []PoolCheckpointStatus
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		cp := PoolCheckpointStatus{Pool: fields[0]}
		if fields[1] != "-" && fields[1] != "none" {
			cp.HasCheckpoint = true
			cp.Size = fields[1]
		}
		statuses = append(statuses, cp)
	}
	if statuses == nil {
		statuses = []PoolCheckpointStatus{}
	}
	respondOK(w, map[string]any{"success": true, "checkpoints": statuses})
}

// CreateCheckpoint handles POST /api/zfs/checkpoint
func CreateCheckpoint(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Pool string `json:"pool"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondErrorSimple(w, "Invalid request body", http.StatusBadRequest)
		return
	}
	if !maintPoolRe.MatchString(req.Pool) {
		respondErrorSimple(w, "invalid pool name", http.StatusBadRequest)
		return
	}
	if _, err := executeCommandWithTimeout(TimeoutMedium, "zpool", []string{"checkpoint", req.Pool}); err != nil {
		respondErrorSimple(w, "Failed to create checkpoint: "+err.Error(), http.StatusInternalServerError)
		return
	}
	respondOK(w, CommandResponse{Success: true, Output: "Checkpoint created for pool " + req.Pool})
}

// DiscardCheckpoint handles POST /api/zfs/checkpoint/discard
func DiscardCheckpoint(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Pool string `json:"pool"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondErrorSimple(w, "Invalid request body", http.StatusBadRequest)
		return
	}
	if !maintPoolRe.MatchString(req.Pool) {
		respondErrorSimple(w, "invalid pool name", http.StatusBadRequest)
		return
	}
	if _, err := executeCommandWithTimeout(TimeoutSlow, "zpool", []string{"checkpoint", "--discard", req.Pool}); err != nil {
		respondErrorSimple(w, "Failed to discard checkpoint: "+err.Error(), http.StatusInternalServerError)
		return
	}
	respondOK(w, CommandResponse{Success: true, Output: "Checkpoint discarded for pool " + req.Pool})
}

// UpgradePool handles POST /api/zfs/pool/upgrade
func UpgradePool(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Pool string `json:"pool"`
		All  bool   `json:"all"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondErrorSimple(w, "Invalid request body", http.StatusBadRequest)
		return
	}
	var args []string
	if req.All {
		args = []string{"upgrade", "-a"}
	} else {
		if !maintPoolRe.MatchString(req.Pool) {
			respondErrorSimple(w, "invalid pool name", http.StatusBadRequest)
			return
		}
		args = []string{"upgrade", req.Pool}
	}
	out, err := executeCommandWithTimeout(TimeoutMedium, "zpool", args)
	if err != nil {
		respondErrorSimple(w, "Failed to upgrade pool: "+err.Error(), http.StatusInternalServerError)
		return
	}
	respondOK(w, CommandResponse{Success: true, Output: strings.TrimSpace(out)})
}

// GetPoolFeatures handles GET /api/zfs/pool/features
func GetPoolFeatures(w http.ResponseWriter, r *http.Request) {
	pool := r.URL.Query().Get("pool")
	if pool == "" {
		respondErrorSimple(w, "pool parameter required", http.StatusBadRequest)
		return
	}
	if !maintPoolRe.MatchString(pool) {
		respondErrorSimple(w, "invalid pool name", http.StatusBadRequest)
		return
	}
	// zpool get -H all <pool> - filter feature@ lines
	out, err := executeCommandWithTimeout(TimeoutFast, "zpool", []string{"get", "-H", "all", pool})
	if err != nil {
		respondErrorSimple(w, "Failed to get pool features: "+err.Error(), http.StatusInternalServerError)
		return
	}
	var features []PoolFeature
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		prop := fields[1]
		if !strings.HasPrefix(prop, "feature@") {
			continue
		}
		features = append(features, PoolFeature{
			Name:  strings.TrimPrefix(prop, "feature@"),
			State: fields[2],
		})
	}
	if features == nil {
		features = []PoolFeature{}
	}
	respondOK(w, map[string]any{"success": true, "pool": pool, "features": features})
}

// SetMultihost handles POST /api/zfs/pool/multihost
func SetMultihost(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Pool    string `json:"pool"`
		Enabled bool   `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondErrorSimple(w, "Invalid request body", http.StatusBadRequest)
		return
	}
	if !maintPoolRe.MatchString(req.Pool) {
		respondErrorSimple(w, "invalid pool name", http.StatusBadRequest)
		return
	}
	val := "off"
	if req.Enabled {
		val = "on"
	}
	if _, err := executeCommandWithTimeout(TimeoutFast, "zpool", []string{"set", "multihost=" + val, req.Pool}); err != nil {
		respondErrorSimple(w, "Failed to set multihost: "+err.Error(), http.StatusInternalServerError)
		return
	}
	respondOK(w, CommandResponse{Success: true, Output: "multihost=" + val + " set on pool " + req.Pool})
}

// GetDDTStats handles GET /api/zfs/ddt/stats
func GetDDTStats(w http.ResponseWriter, r *http.Request) {
	pool := r.URL.Query().Get("pool")
	args := []string{"status", "-D"}
	if pool != "" {
		if !maintPoolRe.MatchString(pool) {
			respondErrorSimple(w, "invalid pool name", http.StatusBadRequest)
			return
		}
		args = append(args, pool)
	}
	out, err := executeCommandWithTimeout(TimeoutFast, "zpool", args)
	if err != nil {
		respondErrorSimple(w, "Failed to get DDT stats: "+err.Error(), http.StatusInternalServerError)
		return
	}
	// Return the raw dedup stats section parsed per-pool
	pools := parseDDTStats(out)
	respondOK(w, map[string]any{"success": true, "pools": pools})
}

type DDTPoolStats struct {
	Pool     string            `json:"pool"`
	Stats    map[string]string `json:"stats"`
	RawLines []string          `json:"raw_lines"`
}

func parseDDTStats(out string) []DDTPoolStats {
	var result []DDTPoolStats
	var current *DDTPoolStats
	inDedup := false

	for _, rawLine := range strings.Split(out, "\n") {
		line := strings.TrimSpace(rawLine)
		if strings.HasPrefix(line, "pool: ") {
			if current != nil {
				result = append(result, *current)
			}
			name := strings.TrimPrefix(line, "pool: ")
			current = &DDTPoolStats{
				Pool:  name,
				Stats: make(map[string]string),
			}
			inDedup = false
		} else if current != nil && strings.Contains(line, "DDT entries") {
			inDedup = true
			current.RawLines = append(current.RawLines, line)
		} else if inDedup && current != nil && line != "" {
			current.RawLines = append(current.RawLines, line)
		}
	}
	if current != nil {
		result = append(result, *current)
	}
	return result
}
