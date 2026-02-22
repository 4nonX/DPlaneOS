package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"
)

// ZFSSnapshotHandler handles ZFS snapshot CRUD operations
type ZFSSnapshotHandler struct{}

func NewZFSSnapshotHandler() *ZFSSnapshotHandler {
	return &ZFSSnapshotHandler{}
}

// Snapshot represents a ZFS snapshot
type Snapshot struct {
	Name       string `json:"name"`       // tank/data@snap-2025-02-15
	Dataset    string `json:"dataset"`    // tank/data
	SnapName   string `json:"snap_name"`  // snap-2025-02-15
	Used       string `json:"used"`       // 1.5G
	Refer      string `json:"refer"`      // 10.2G
	Creation   string `json:"creation"`   // 2025-02-15 03:00
}

// Strict validation: dataset names can only contain alphanumeric, slash, hyphen, underscore, period
var validDatasetRe = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9/_\-\.]*$`)
var validSnapRe = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9/_\-\.]*@[a-zA-Z0-9][a-zA-Z0-9_\-\.]*$`)

func isValidDataset(name string) bool {
	return len(name) >= 1 && len(name) <= 200 && validDatasetRe.MatchString(name)
}

func isValidSnapshotName(name string) bool {
	return len(name) >= 3 && len(name) <= 250 && validSnapRe.MatchString(name)
}

// ListSnapshots returns all snapshots, optionally filtered by dataset
// GET /api/zfs/snapshots?dataset=tank/data
func (h *ZFSSnapshotHandler) ListSnapshots(w http.ResponseWriter, r *http.Request) {
	dataset := r.URL.Query().Get("dataset")

	args := []string{"list", "-t", "snapshot", "-H", "-o", "name,used,refer,creation", "-s", "creation"}
	if dataset != "" {
		if !isValidDataset(dataset) {
			respondErrorSimple(w, "Invalid dataset name", http.StatusBadRequest)
			return
		}
		args = append(args, "-r", dataset)
	}

	output, err := executeCommand("/usr/sbin/zfs", args)
	if err != nil {
		respondOK(w, map[string]interface{}{
			"success":   true,
			"snapshots": []Snapshot{},
		})
		return
	}

	snapshots := parseSnapshotList(output)

	respondOK(w, map[string]interface{}{
		"success":   true,
		"snapshots": snapshots,
		"count":     len(snapshots),
	})
}

// CreateSnapshot creates a new ZFS snapshot
// POST /api/zfs/snapshots { "dataset": "tank/data", "name": "before-update" }
func (h *ZFSSnapshotHandler) CreateSnapshot(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Dataset string `json:"dataset"` // tank/data
		Name    string `json:"name"`    // optional, auto-generated if empty
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondErrorSimple(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	if !isValidDataset(req.Dataset) {
		respondErrorSimple(w, "Invalid dataset name", http.StatusBadRequest)
		return
	}

	// Auto-generate snapshot name if not provided
	snapName := req.Name
	if snapName == "" {
		snapName = fmt.Sprintf("manual-%s", time.Now().Format("2006-01-02-150405"))
	}

	// Validate snap name part (no @ allowed in the name itself)
	if strings.Contains(snapName, "@") || strings.Contains(snapName, "/") {
		respondErrorSimple(w, "Snapshot name cannot contain @ or /", http.StatusBadRequest)
		return
	}

	fullName := fmt.Sprintf("%s@%s", req.Dataset, snapName)
	if !isValidSnapshotName(fullName) {
		respondErrorSimple(w, "Invalid snapshot name", http.StatusBadRequest)
		return
	}

	start := time.Now()
	_, err := executeCommand("/usr/sbin/zfs", []string{"snapshot", fullName})
	duration := time.Since(start)

	if err != nil {
		respondOK(w, map[string]interface{}{
			"success": false,
			"error":   fmt.Sprintf("Failed to create snapshot: %v", err),
		})
		return
	}

	respondOK(w, map[string]interface{}{
		"success":  true,
		"snapshot": fullName,
		"duration": duration.Milliseconds(),
	})
}

// DestroySnapshot deletes a ZFS snapshot
// DELETE /api/zfs/snapshots { "snapshot": "tank/data@before-update" }
func (h *ZFSSnapshotHandler) DestroySnapshot(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Snapshot string `json:"snapshot"` // tank/data@snap-name
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondErrorSimple(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	if !isValidSnapshotName(req.Snapshot) {
		respondErrorSimple(w, "Invalid snapshot name", http.StatusBadRequest)
		return
	}

	_, err := executeCommand("/usr/sbin/zfs", []string{"destroy", req.Snapshot})
	if err != nil {
		respondOK(w, map[string]interface{}{
			"success": false,
			"error":   fmt.Sprintf("Failed to destroy snapshot: %v", err),
		})
		return
	}

	respondOK(w, map[string]interface{}{
		"success":  true,
		"message":  fmt.Sprintf("Snapshot %s destroyed", req.Snapshot),
	})
}

// RollbackSnapshot rolls back a dataset to a snapshot
// POST /api/zfs/snapshots/rollback { "snapshot": "tank/data@before-update", "force": true }
func (h *ZFSSnapshotHandler) RollbackSnapshot(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Snapshot string `json:"snapshot"` // tank/data@snap-name
		Force    bool   `json:"force"`    // -r flag (destroy newer snapshots)
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondErrorSimple(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	if !isValidSnapshotName(req.Snapshot) {
		respondErrorSimple(w, "Invalid snapshot name", http.StatusBadRequest)
		return
	}

	args := []string{"rollback"}
	if req.Force {
		args = append(args, "-r")
	}
	args = append(args, req.Snapshot)

	start := time.Now()
	_, err := executeCommand("/usr/sbin/zfs", args)
	duration := time.Since(start)

	if err != nil {
		respondOK(w, map[string]interface{}{
			"success": false,
			"error":   fmt.Sprintf("Failed to rollback: %v", err),
		})
		return
	}

	respondOK(w, map[string]interface{}{
		"success":  true,
		"message":  fmt.Sprintf("Rolled back to %s", req.Snapshot),
		"duration": duration.Milliseconds(),
	})
}

// parseSnapshotList parses `zfs list -t snapshot -H -o name,used,refer,creation` output
func parseSnapshotList(output string) []Snapshot {
	var snapshots []Snapshot
	lines := strings.Split(strings.TrimSpace(output), "\n")
	for _, line := range lines {
		if line == "" {
			continue
		}
		// Tab-separated: name, used, refer, creation (creation may contain spaces)
		parts := strings.SplitN(line, "\t", 4)
		if len(parts) < 3 {
			continue
		}

		name := strings.TrimSpace(parts[0])
		atIdx := strings.Index(name, "@")
		if atIdx == -1 {
			continue
		}

		snap := Snapshot{
			Name:     name,
			Dataset:  name[:atIdx],
			SnapName: name[atIdx+1:],
			Used:     strings.TrimSpace(parts[1]),
			Refer:    strings.TrimSpace(parts[2]),
		}
		if len(parts) >= 4 {
			snap.Creation = strings.TrimSpace(parts[3])
		}
		snapshots = append(snapshots, snap)
	}
	return snapshots
}
