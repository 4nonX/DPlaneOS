package handlers

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ZFSTimeMachineHandler provides temporal file access through ZFS snapshots
type ZFSTimeMachineHandler struct{}

func NewZFSTimeMachineHandler() *ZFSTimeMachineHandler {
	return &ZFSTimeMachineHandler{}
}

// SnapshotEntry represents a file/dir within a snapshot
type SnapshotEntry struct {
	Name    string `json:"name"`
	Path    string `json:"path"`
	IsDir   bool   `json:"is_dir"`
	Size    int64  `json:"size"`
	ModTime string `json:"mod_time"`
	Mode    string `json:"mode"`
}

// ListSnapshotVersions lists all snapshots for a dataset, showing when each was taken
// GET /api/timemachine/versions?dataset=tank/data
func (h *ZFSTimeMachineHandler) ListSnapshotVersions(w http.ResponseWriter, r *http.Request) {
	dataset := r.URL.Query().Get("dataset")
	if dataset == "" || !isValidDataset(dataset) {
		respondErrorSimple(w, "Invalid or missing dataset parameter", http.StatusBadRequest)
		return
	}

	output, err := executeCommand("/usr/sbin/zfs", []string{
		"list", "-t", "snapshot", "-H",
		"-o", "name,creation,used,refer",
		"-s", "creation",
		"-r", dataset,
	})
	if err != nil {
		respondOK(w, map[string]interface{}{
			"success":  true,
			"versions": []interface{}{},
		})
		return
	}

	snapshots := parseSnapshotList(output)

	respondOK(w, map[string]interface{}{
		"success":  true,
		"dataset":  dataset,
		"versions": snapshots,
		"count":    len(snapshots),
	})
}

// BrowseSnapshot lists files inside a ZFS snapshot at a given path
// ZFS auto-exposes snapshots at <mountpoint>/.zfs/snapshot/<snapname>/
// GET /api/timemachine/browse?snapshot=tank/data@daily-2025-02-15&path=/photos
func (h *ZFSTimeMachineHandler) BrowseSnapshot(w http.ResponseWriter, r *http.Request) {
	snapshotParam := r.URL.Query().Get("snapshot")
	browsePath := r.URL.Query().Get("path")

	if !isValidSnapshotName(snapshotParam) {
		respondErrorSimple(w, "Invalid snapshot name", http.StatusBadRequest)
		return
	}

	// Split snapshot into dataset and snap name
	atIdx := strings.Index(snapshotParam, "@")
	if atIdx == -1 {
		respondErrorSimple(w, "Invalid snapshot format (expected dataset@name)", http.StatusBadRequest)
		return
	}
	dataset := snapshotParam[:atIdx]
	snapName := snapshotParam[atIdx+1:]

	// Get mountpoint of the dataset
	mountpoint, err := getDatasetMountpoint(dataset)
	if err != nil || mountpoint == "" {
		respondErrorSimple(w, "Cannot determine dataset mountpoint", http.StatusBadRequest)
		return
	}

	// Build the snapshot browse path:
	// <mountpoint>/.zfs/snapshot/<snapname>/<browsePath>
	snapshotDir := filepath.Join(mountpoint, ".zfs", "snapshot", snapName)

	// Sanitize browsePath — prevent path traversal
	if browsePath == "" {
		browsePath = "/"
	}
	cleanPath := filepath.Clean(browsePath)
	if strings.Contains(cleanPath, "..") {
		respondErrorSimple(w, "Path traversal not allowed", http.StatusBadRequest)
		return
	}

	fullPath := filepath.Join(snapshotDir, cleanPath)

	// Verify the resolved path is still within the snapshot directory
	if !strings.HasPrefix(fullPath, snapshotDir) {
		respondErrorSimple(w, "Path outside snapshot boundary", http.StatusBadRequest)
		return
	}

	// Check if path exists
	info, err := os.Stat(fullPath)
	if err != nil {
		respondErrorSimple(w, "Path not found in snapshot", http.StatusNotFound)
		return
	}

	// If it's a file, return file info
	if !info.IsDir() {
		respondOK(w, map[string]interface{}{
			"success": true,
			"type":    "file",
			"entry": SnapshotEntry{
				Name:    info.Name(),
				Path:    cleanPath,
				IsDir:   false,
				Size:    info.Size(),
				ModTime: info.ModTime().Format(time.RFC3339),
				Mode:    info.Mode().String(),
			},
		})
		return
	}

	// List directory contents
	dirEntries, err := os.ReadDir(fullPath)
	if err != nil {
		respondErrorSimple(w, "Cannot read snapshot directory", http.StatusInternalServerError)
		return
	}

	// Get custom ignore patterns from query param
	ignoreParam := r.URL.Query().Get("ignore")
	var customIgnore []string
	if ignoreParam != "" {
		customIgnore = strings.Split(ignoreParam, ",")
	}

	var entries []SnapshotEntry
	for _, de := range dirEntries {
		// Filter out system junk files
		if ShouldIgnoreFile(de.Name(), customIgnore) {
			continue
		}
		fi, err := de.Info()
		if err != nil {
			continue
		}
		entryPath := filepath.Join(cleanPath, de.Name())
		entries = append(entries, SnapshotEntry{
			Name:    de.Name(),
			Path:    entryPath,
			IsDir:   de.IsDir(),
			Size:    fi.Size(),
			ModTime: fi.ModTime().Format(time.RFC3339),
			Mode:    fi.Mode().String(),
		})
	}

	respondOK(w, map[string]interface{}{
		"success":  true,
		"type":     "directory",
		"snapshot": snapshotParam,
		"path":     cleanPath,
		"entries":  entries,
		"count":    len(entries),
	})
}

// RestoreFile copies a single file from a snapshot back to the live dataset
// POST /api/timemachine/restore
func (h *ZFSTimeMachineHandler) RestoreFile(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Snapshot    string `json:"snapshot"`     // tank/data@daily-2025-02-15
		SourcePath  string `json:"source_path"`  // /photos/vacation.jpg (relative to snapshot)
		DestPath    string `json:"dest_path"`    // /photos/vacation.jpg (relative to live dataset, optional)
		Overwrite   bool   `json:"overwrite"`    // overwrite existing file
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondErrorSimple(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	if !isValidSnapshotName(req.Snapshot) {
		respondErrorSimple(w, "Invalid snapshot name", http.StatusBadRequest)
		return
	}

	atIdx := strings.Index(req.Snapshot, "@")
	if atIdx == -1 {
		respondErrorSimple(w, "Invalid snapshot format", http.StatusBadRequest)
		return
	}
	dataset := req.Snapshot[:atIdx]
	snapName := req.Snapshot[atIdx+1:]

	mountpoint, err := getDatasetMountpoint(dataset)
	if err != nil || mountpoint == "" {
		respondErrorSimple(w, "Cannot determine dataset mountpoint", http.StatusBadRequest)
		return
	}

	// Sanitize paths
	sourcePath := filepath.Clean(req.SourcePath)
	if strings.Contains(sourcePath, "..") {
		respondErrorSimple(w, "Path traversal not allowed", http.StatusBadRequest)
		return
	}

	destPath := sourcePath // Default: same relative path
	if req.DestPath != "" {
		destPath = filepath.Clean(req.DestPath)
		if strings.Contains(destPath, "..") {
			respondErrorSimple(w, "Path traversal not allowed in dest", http.StatusBadRequest)
			return
		}
	}

	// Build full paths
	snapshotFile := filepath.Join(mountpoint, ".zfs", "snapshot", snapName, sourcePath)
	liveFile := filepath.Join(mountpoint, destPath)

	// Verify paths are within boundaries
	snapshotBase := filepath.Join(mountpoint, ".zfs", "snapshot", snapName)
	if !strings.HasPrefix(snapshotFile, snapshotBase) {
		respondErrorSimple(w, "Source path outside snapshot boundary", http.StatusBadRequest)
		return
	}
	if !strings.HasPrefix(liveFile, mountpoint) || strings.Contains(liveFile, ".zfs") {
		respondErrorSimple(w, "Destination path outside dataset boundary", http.StatusBadRequest)
		return
	}

	// Check source exists
	srcInfo, err := os.Stat(snapshotFile)
	if err != nil {
		respondErrorSimple(w, "Source file not found in snapshot", http.StatusNotFound)
		return
	}
	if srcInfo.IsDir() {
		respondErrorSimple(w, "Cannot restore directories — use ZFS rollback for that", http.StatusBadRequest)
		return
	}

	// Check if destination exists
	if _, err := os.Stat(liveFile); err == nil && !req.Overwrite {
		respondErrorSimple(w, "Destination file already exists. Set overwrite=true to replace.", http.StatusConflict)
		return
	}

	// Ensure destination directory exists
	destDir := filepath.Dir(liveFile)
	if err := os.MkdirAll(destDir, 0755); err != nil {
		respondErrorSimple(w, "Cannot create destination directory", http.StatusInternalServerError)
		return
	}

	// Copy file
	start := time.Now()
	if err := copyFile(snapshotFile, liveFile); err != nil {
		respondOK(w, map[string]interface{}{
			"success": false,
			"error":   fmt.Sprintf("Restore failed: %v", err),
		})
		return
	}
	duration := time.Since(start)

	// Preserve original permissions
	os.Chmod(liveFile, srcInfo.Mode())

	respondOK(w, map[string]interface{}{
		"success":     true,
		"source":      snapshotFile,
		"destination": liveFile,
		"size":        srcInfo.Size(),
		"duration_ms": duration.Milliseconds(),
	})
}

// getDatasetMountpoint returns the mountpoint of a ZFS dataset
func getDatasetMountpoint(dataset string) (string, error) {
	if !isValidDataset(dataset) {
		return "", fmt.Errorf("invalid dataset name")
	}
	output, err := executeCommand("/usr/sbin/zfs", []string{
		"get", "-H", "-o", "value", "mountpoint", dataset,
	})
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(output), nil
}

// copyFile copies a single file from src to dst
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()

	_, err = io.Copy(out, in)
	if err != nil {
		return err
	}
	return out.Sync()
}
