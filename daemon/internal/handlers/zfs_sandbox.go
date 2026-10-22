package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"dplaned/internal/libzfs"
)

// ZFSSandboxHandler manages ephemeral ZFS clones for Docker sandboxing
type ZFSSandboxHandler struct{}

func NewZFSSandboxHandler() *ZFSSandboxHandler {
	return &ZFSSandboxHandler{}
}

// SandboxInfo represents an active sandbox
type SandboxInfo struct {
	Name       string `json:"name"`
	Origin     string `json:"origin"` // parent snapshot
	Mountpoint string `json:"mountpoint"`
	Used       string `json:"used"`
	Creation   string `json:"creation"`
}

// CreateSandbox creates a ZFS clone for ephemeral container testing
// 1. Snapshots the source dataset
// 2. Creates a clone from that snapshot
// 3. Returns the clone mountpoint for Docker volume use
// POST /api/sandbox/create
func (h *ZFSSandboxHandler) CreateSandbox(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Dataset string `json:"dataset"` // tank/docker or tank/data
		Name    string `json:"name"`    // sandbox name (optional, auto-generated)
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondErrorSimple(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	if !isValidDataset(req.Dataset) {
		respondErrorSimple(w, "Invalid dataset name", http.StatusBadRequest)
		return
	}

	// Generate sandbox name
	sandboxName := req.Name
	if sandboxName == "" {
		sandboxName = fmt.Sprintf("sandbox-%s", time.Now().Format("20060102-150405"))
	}
	if strings.ContainsAny(sandboxName, "@/;|&$`\\\"' ") {
		respondErrorSimple(w, "Invalid sandbox name", http.StatusBadRequest)
		return
	}
	if len(sandboxName) > 64 {
		respondErrorSimple(w, "Sandbox name too long (max 64 chars)", http.StatusBadRequest)
		return
	}

	// Step 1: Create a snapshot as clone base
	snapName := fmt.Sprintf("%s@sandbox-base-%s", req.Dataset, sandboxName)
	err := libzfs.SnapshotCreate(snapName)
	if err != nil {
		respondOK(w, map[string]any{
			"success": false,
			"error":   fmt.Sprintf("Failed to create base snapshot: %v", err),
		})
		return
	}

	// Step 2: Create clone
	// Clone goes into a sandboxes container under the pool
	// e.g., tank/docker → tank/sandboxes/sandbox-20250215
	poolName := strings.Split(req.Dataset, "/")[0]
	cloneDataset := fmt.Sprintf("%s/sandboxes/%s", poolName, sandboxName)

	// Ensure sandboxes parent dataset exists (idempotent: error ignored if already exists)
	libzfs.DatasetCreate(poolName + "/sandboxes")

	start := time.Now()
	err = libzfs.SnapshotClone(snapName, cloneDataset)
	duration := time.Since(start)

	if err != nil {
		// Cleanup the snapshot on failure
		libzfs.SnapshotDestroy(snapName)
		respondOK(w, map[string]any{
			"success": false,
			"error":   fmt.Sprintf("Failed to create clone: %v", err),
		})
		return
	}

	// Get the mountpoint
	mountpoint, _ := libzfs.DatasetGet(cloneDataset, "mountpoint")

	respondOK(w, map[string]any{
		"success":     true,
		"sandbox":     cloneDataset,
		"mountpoint":  mountpoint,
		"origin":      snapName,
		"duration_ms": duration.Milliseconds(),
		"hint":        fmt.Sprintf("Use as Docker volume: docker run -v %s:/data ...", mountpoint),
	})
}

// ListSandboxes lists all active sandboxes (ZFS clones under pool/sandboxes/)
// GET /api/sandbox/list
func (h *ZFSSandboxHandler) ListSandboxes(w http.ResponseWriter, r *http.Request) {
	// -d 2: the sandboxes are <pool>/sandboxes/<name>; with -d 1 (and no
	// dataset) the list reached only <pool>/sandboxes and was always empty.
	output, err := executeCommand("zfs", []string{
		"list", "-t", "filesystem", "-H",
		"-o", "name,origin,used,mountpoint,creation",
		"-r", "-d", "2",
	})
	if err != nil {
		respondOK(w, map[string]any{
			"success":   true,
			"sandboxes": []any{},
		})
		return
	}

	var sandboxes []SandboxInfo
	lines := strings.Split(strings.TrimSpace(output), "\n")
	for _, line := range lines {
		if line == "" {
			continue
		}
		// Only include datasets under */sandboxes/*
		if !strings.Contains(line, "/sandboxes/") {
			continue
		}
		parts := strings.SplitN(line, "\t", 5)
		if len(parts) < 4 {
			continue
		}
		origin := strings.TrimSpace(parts[1])
		if origin == "-" {
			continue // Not a clone
		}
		sb := SandboxInfo{
			Name:       strings.TrimSpace(parts[0]),
			Origin:     origin,
			Used:       strings.TrimSpace(parts[2]),
			Mountpoint: strings.TrimSpace(parts[3]),
		}
		if len(parts) >= 5 {
			sb.Creation = strings.TrimSpace(parts[4])
		}
		sandboxes = append(sandboxes, sb)
	}

	respondOK(w, map[string]any{
		"success":   true,
		"sandboxes": sandboxes,
		"count":     len(sandboxes),
	})
}

// DestroySandbox removes a sandbox clone and its base snapshot
// Cleans up completely - no residue
// DELETE /api/sandbox/destroy
func (h *ZFSSandboxHandler) DestroySandbox(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Sandbox string `json:"sandbox"` // tank/sandboxes/sandbox-20250215
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondErrorSimple(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	if !isValidDataset(req.Sandbox) {
		respondErrorSimple(w, "Invalid sandbox name", http.StatusBadRequest)
		return
	}
	if !strings.Contains(req.Sandbox, "/sandboxes/") {
		respondErrorSimple(w, "Not a sandbox dataset", http.StatusBadRequest)
		return
	}

	// Get the origin snapshot before destroying the clone
	origin, _ := libzfs.DatasetGet(req.Sandbox, "origin")

	// Step 1: Destroy the clone
	err := libzfs.DatasetDestroy(req.Sandbox, true)
	if err != nil {
		respondOK(w, map[string]any{
			"success": false,
			"error":   fmt.Sprintf("Failed to destroy sandbox: %v", err),
		})
		return
	}

	// Step 2: Destroy the origin snapshot (no longer needed)
	cleanedUp := false
	if origin != "" && origin != "-" {
		err := libzfs.SnapshotDestroy(origin)
		cleanedUp = err == nil
	}

	respondOK(w, map[string]any{
		"success":        true,
		"destroyed":      req.Sandbox,
		"origin_cleaned": cleanedUp,
	})
}

// CleanOrphanVolumes removes what sandboxes leave behind: base snapshots
// (<dataset>@sandbox-base-*) whose clone is gone, e.g. destroyed by hand.
// (It ran a system-wide `docker volume prune` before, which has nothing to
// do with sandboxes - they are ZFS clones - and was not routed.)
// POST /api/sandbox/cleanup
func (h *ZFSSandboxHandler) CleanOrphanVolumes(w http.ResponseWriter, r *http.Request) {
	output, err := executeCommand("zfs", []string{"list", "-H", "-t", "snapshot", "-o", "name,clones"})
	if err != nil {
		respondOK(w, map[string]any{"success": false, "error": fmt.Sprintf("Cleanup failed: %v", err)})
		return
	}
	var removed, failed []string
	for _, name := range orphanSandboxBases(output) {
		if err := libzfs.SnapshotDestroy(name); err != nil {
			failed = append(failed, name+": "+err.Error())
		} else {
			removed = append(removed, name)
		}
	}
	msg := fmt.Sprintf("%d leftover sandbox snapshot(s) removed", len(removed))
	respondOK(w, map[string]any{
		"success": len(failed) == 0,
		"removed": removed,
		"output":  strings.TrimSpace(msg + "\n" + strings.Join(failed, "\n")),
	})
}

// orphanSandboxBases picks the sandbox base snapshots without a clone from
// `zfs list -H -t snapshot -o name,clones` output.
func orphanSandboxBases(out string) []string {
	var names []string
	for line := range strings.SplitSeq(strings.TrimSpace(out), "\n") {
		f := strings.Split(line, "\t")
		if len(f) != 2 {
			continue
		}
		_, snap, ok := strings.Cut(f[0], "@")
		if ok && strings.HasPrefix(snap, "sandbox-base-") && (f[1] == "" || f[1] == "-") {
			names = append(names, f[0])
		}
	}
	return names
}
