package handlers

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"regexp"
	"strings"
	"time"

	"dplaned/internal/libzfs"
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

	output, err := executeCommand("zfs", args)
	if err != nil {
		respondOK(w, map[string]any{
			"success":   true,
			"snapshots": []Snapshot{},
		})
		return
	}

	snapshots := parseSnapshotList(output)

	respondOK(w, map[string]any{
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
	err := libzfs.SnapshotCreate(fullName)
	duration := time.Since(start)

	if err != nil {
		respondOK(w, map[string]any{
			"success": false,
			"error":   fmt.Sprintf("Failed to create snapshot: %v", err),
		})
		return
	}

	respondOK(w, map[string]any{
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

	// Guard: detect dependent clones before attempting destruction.
	// ZFS will refuse the delete at the kernel level but the error is opaque;
	// proactively checking gives a clear, actionable message.
	if cloneOut, err := executeCommandWithTimeout(TimeoutFast, "zfs",
		[]string{"list", "-H", "-t", "filesystem,volume", "-o", "name,origin"}); err == nil {
		for line := range strings.SplitSeq(cloneOut, "\n") {
			fields := strings.Fields(line)
			if len(fields) == 2 && fields[1] == req.Snapshot {
				respondOK(w, map[string]any{
					"success": false,
					"error":   fmt.Sprintf("snapshot has a dependent clone %q; destroy or promote the clone first", fields[0]),
					"clone":   fields[0],
				})
				return
			}
		}
	}

	err := libzfs.SnapshotDestroy(req.Snapshot)
	if err != nil {
		respondOK(w, map[string]any{
			"success": false,
			"error":   fmt.Sprintf("Failed to destroy snapshot: %v", err),
		})
		return
	}

	respondOK(w, map[string]any{
		"success":  true,
		"message":  fmt.Sprintf("Snapshot %s destroyed", req.Snapshot),
	})
}

// RollbackSnapshot rolls back a dataset to a snapshot
// POST /api/zfs/snapshots/rollback { "snapshot": "tank/data@before-update", "mode": "safe" }
// A refusal by ZFS (e.g. newer snapshots exist in safe mode) returns 409 with ZFS's message.
func (h *ZFSSnapshotHandler) RollbackSnapshot(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Snapshot string `json:"snapshot"` // tank/data@snap-name
		Mode     string `json:"mode"`     // safe | destroy_newer | destroy_clones (see rollbackArgs)
		Force    bool   `json:"force"`    // legacy -r flag, only used when mode is empty
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondErrorSimple(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	if !isValidSnapshotName(req.Snapshot) {
		respondErrorSimple(w, "Invalid snapshot name", http.StatusBadRequest)
		return
	}

	args, err := rollbackArgs(req.Mode, req.Force, req.Snapshot)
	if err != nil {
		respondErrorSimple(w, err.Error(), http.StatusBadRequest)
		return
	}

	start := time.Now()
	out, err := executeCommand("zfs", args)
	duration := time.Since(start)

	if err != nil {
		msg := strings.TrimSpace(out)
		if msg == "" {
			msg = err.Error()
		}
		respondErrorSimple(w, "Rollback refused: "+msg, http.StatusConflict)
		return
	}

	// Ensure the dataset is mounted after rollback. ZFS unmounts the dataset
	// during rollback and NixOS's ZFS systemd integration remounts it; issuing
	// an explicit mount is safe (no-op if already mounted) and makes the
	// post-rollback state deterministic regardless of OS automount behaviour.
	dataset := req.Snapshot[:strings.IndexByte(req.Snapshot, '@')]
	if _, mountErr := executeCommand("zfs", []string{"mount", dataset}); mountErr != nil {
		log.Printf("RollbackSnapshot: zfs mount %s after rollback: %v (may already be mounted)", dataset, mountErr)
	}

	respondOK(w, map[string]any{
		"success":  true,
		"message":  fmt.Sprintf("Rolled back to %s", req.Snapshot),
		"duration": duration.Milliseconds(),
	})
}

// rollbackArgs maps a rollback safety level to `zfs rollback` arguments.
//
//	safe           zfs rollback <snap>     refuses if newer snapshots, bookmarks or clones exist
//	destroy_newer  zfs rollback -r <snap>  destroys newer snapshots, refuses if any of them has clones
//	destroy_clones zfs rollback -R <snap>  destroys newer snapshots and their clones (no safety check)
//
// An empty mode keeps the pre-v14.8 behaviour of the force flag (true → -r).
func rollbackArgs(mode string, force bool, snapshot string) ([]string, error) {
	args := []string{"rollback"}
	switch mode {
	case "safe":
	case "destroy_newer":
		args = append(args, "-r")
	case "destroy_clones":
		args = append(args, "-R")
	case "":
		if force {
			args = append(args, "-r")
		}
	default:
		return nil, fmt.Errorf("invalid rollback mode %q (use safe, destroy_newer or destroy_clones)", mode)
	}
	return append(args, snapshot), nil
}

// CloneSnapshot clones a ZFS snapshot into a new dataset.
// POST /api/zfs/snapshots/clone { "snapshot": "tank/data@snap", "clone": "tank/data-clone" }
func (h *ZFSSnapshotHandler) CloneSnapshot(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Snapshot string `json:"snapshot"` // source: dataset@snapname
		Clone    string `json:"clone"`    // target dataset path
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondErrorSimple(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	if !isValidSnapshotName(req.Snapshot) {
		respondErrorSimple(w, "Invalid snapshot name", http.StatusBadRequest)
		return
	}
	if !isValidDataset(req.Clone) {
		respondErrorSimple(w, "Invalid clone dataset name", http.StatusBadRequest)
		return
	}

	start := time.Now()
	err := libzfs.SnapshotClone(req.Snapshot, req.Clone)
	duration := time.Since(start)

	if err != nil {
		respondOK(w, map[string]any{
			"success": false,
			"error":   fmt.Sprintf("Failed to clone snapshot: %v", err),
		})
		return
	}

	respondOK(w, map[string]any{
		"success":  true,
		"clone":    req.Clone,
		"origin":   req.Snapshot,
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
