package handlers

import (
	"encoding/json"
	"net/http"
	"regexp"
	"strings"
)

var maintPoolRe = regexp.MustCompile(`^[a-zA-Z0-9_\-]+$`)

// HandlePoolDestroy handles POST /api/zfs/pools/destroy
func HandlePoolDestroy(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondErrorSimple(w, "Invalid request body", http.StatusBadRequest)
		return
	}
	if !maintPoolRe.MatchString(req.Name) {
		respondErrorSimple(w, "invalid pool name", http.StatusBadRequest)
		return
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
	State       string `json:"state"`    // active | enabled | disabled
	Description string `json:"description,omitempty"`
}

// PoolCheckpointStatus contains checkpoint information for a pool.
type PoolCheckpointStatus struct {
	Pool       string `json:"pool"`
	HasCheckpoint bool   `json:"has_checkpoint"`
	Size       string `json:"size,omitempty"`
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
	respondOK(w, map[string]interface{}{"success": true, "checkpoints": statuses})
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
	respondOK(w, map[string]interface{}{"success": true, "pool": pool, "features": features})
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
	respondOK(w, map[string]interface{}{"success": true, "pools": pools})
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
