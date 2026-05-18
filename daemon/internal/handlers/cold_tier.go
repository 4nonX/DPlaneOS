package handlers

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"dplaned/internal/audit"
	"dplaned/internal/cmdutil"
	"dplaned/internal/jobs"

	"github.com/gorilla/mux"
)

// ═══════════════════════════════════════════════════════════════
//  Cold Tier - rclone FUSE mounts
//
//  Provides persistent FUSE mounts of rclone remotes under /mnt/cold/.
//  Each mount is a VFS-cached read/write filesystem backed by the remote.
//  State is stored in cold_tier_mounts table; mounts are re-established
//  on daemon restart if marked mounted.
//
//  Routes:
//    GET    /api/storage/cold-tier           - list mounts
//    POST   /api/storage/cold-tier           - create + mount
//    DELETE /api/storage/cold-tier/{id}      - unmount + delete
//    POST   /api/storage/cold-tier/{id}/mount   - mount existing
//    POST   /api/storage/cold-tier/{id}/unmount - unmount existing
//    POST   /api/storage/cold-tier/{id}/usage   - start usage job
// ═══════════════════════════════════════════════════════════════

const coldTierBase = "/mnt/cold"

// ColdTierMount is one registered cold-tier mount.
type ColdTierMount struct {
	ID          int64      `json:"id"`
	Name        string     `json:"name"`
	Remote      string     `json:"remote"`
	RemotePath  string     `json:"remote_path"`
	MountPoint  string     `json:"mount_point"`
	VFSCache    string     `json:"vfs_cache_mode"`
	Mounted     bool       `json:"mounted"`
	CreatedAt   time.Time  `json:"created_at"`
	LastMountAt *time.Time `json:"last_mount_at"`
}

// ColdTierHandler manages cold-tier FUSE mounts backed by rclone.
type ColdTierHandler struct {
	db *sql.DB
}

func NewColdTierHandler(db *sql.DB) *ColdTierHandler {
	return &ColdTierHandler{db: db}
}

// isMounted checks whether a path is currently a FUSE mount point.
func isMounted(path string) bool {
	cmd := exec.Command("mountpoint", "-q", path)
	return cmd.Run() == nil
}

// listColdTierMounts queries all registered mounts and checks live status.
func (h *ColdTierHandler) listColdTierMounts() ([]ColdTierMount, error) {
	rows, err := h.db.Query(`
		SELECT id, name, remote, remote_path, mount_point, vfs_cache_mode,
		       mounted, created_at, last_mount_at
		FROM cold_tier_mounts
		ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var mounts []ColdTierMount
	for rows.Next() {
		var m ColdTierMount
		var dbMounted int
		if err := rows.Scan(&m.ID, &m.Name, &m.Remote, &m.RemotePath,
			&m.MountPoint, &m.VFSCache, &dbMounted, &m.CreatedAt, &m.LastMountAt); err != nil {
			return nil, err
		}
		// Live check overrides the DB flag - the daemon may have restarted
		m.Mounted = isMounted(m.MountPoint)
		if m.Mounted != (dbMounted == 1) {
			// Sync DB to reality
			if _, err := h.db.Exec("UPDATE cold_tier_mounts SET mounted=$1 WHERE id=$2",
				boolToInt(m.Mounted), m.ID); err != nil {
				log.Printf("cold_tier: sync mounted state for %s: %v", m.Name, err)
			}
		}
		mounts = append(mounts, m)
	}
	return mounts, rows.Err()
}

// validateRemoteExists verifies an rclone remote is configured.
func validateRemoteExists(remote string) error {
	out, err := cmdutil.RunFast("rclone", "--config", rcloneConfig, "listremotes")
	if err != nil {
		return fmt.Errorf("rclone not available: %w", err)
	}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		name := strings.TrimSuffix(strings.TrimSpace(line), ":")
		if name == remote {
			return nil
		}
	}
	return fmt.Errorf("remote %q not found in rclone config", remote)
}

// mountRclone starts an rclone FUSE mount as a background daemon.
func mountRclone(m ColdTierMount) error {
	if err := os.MkdirAll(m.MountPoint, 0755); err != nil {
		return fmt.Errorf("create mountpoint: %w", err)
	}

	remotePath := m.Remote + ":"
	if m.RemotePath != "" {
		remotePath += m.RemotePath
	}

	cache := m.VFSCache
	if cache == "" {
		cache = "writes"
	}

	args := []string{
		"--config", rcloneConfig,
		"mount", remotePath, m.MountPoint,
		"--daemon",
		"--vfs-cache-mode", cache,
		"--allow-other",
		"--log-level", "NOTICE",
	}
	out, err := cmdutil.RunSlow("rclone", args...)
	if err != nil {
		return fmt.Errorf("rclone mount: %w: %s", err, string(out))
	}
	return nil
}

// unmountRclone detaches the FUSE mount.
func unmountRclone(mountPoint string) error {
	// Try fusermount first (Linux), fall back to umount
	if out, err := cmdutil.RunFast("fusermount", "-u", mountPoint); err != nil {
		if out2, err2 := cmdutil.RunFast("umount", mountPoint); err2 != nil {
			return fmt.Errorf("fusermount: %s; umount: %s", string(out), string(out2))
		}
	}
	return nil
}

// ─── HTTP handlers ────────────────────────────────────────────────────────────

// HandleList GET /api/storage/cold-tier
func (h *ColdTierHandler) HandleList(w http.ResponseWriter, r *http.Request) {
	mounts, err := h.listColdTierMounts()
	if err != nil {
		respondErrorSimple(w, "Failed to list cold-tier mounts: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if mounts == nil {
		mounts = []ColdTierMount{}
	}
	respondOK(w, map[string]interface{}{"success": true, "mounts": mounts})
}

// HandleCreate POST /api/storage/cold-tier
// Body: { name, remote, remote_path?, vfs_cache_mode? }
func (h *ColdTierHandler) HandleCreate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name       string `json:"name"`
		Remote     string `json:"remote"`
		RemotePath string `json:"remote_path"`
		VFSCache   string `json:"vfs_cache_mode"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondErrorSimple(w, "Invalid JSON", http.StatusBadRequest)
		return
	}

	req.Name = strings.TrimSpace(req.Name)
	req.Remote = strings.TrimSpace(req.Remote)
	if req.Name == "" || req.Remote == "" {
		respondErrorSimple(w, "name and remote are required", http.StatusBadRequest)
		return
	}
	if strings.ContainsAny(req.Name, "/\\:*?\"<>|") {
		respondErrorSimple(w, "name contains invalid characters", http.StatusBadRequest)
		return
	}

	cache := req.VFSCache
	if cache == "" {
		cache = "writes"
	}
	validCaches := map[string]bool{"off": true, "minimal": true, "writes": true, "full": true}
	if !validCaches[cache] {
		respondErrorSimple(w, fmt.Sprintf("invalid vfs_cache_mode %q; valid: off, minimal, writes, full", cache), http.StatusBadRequest)
		return
	}

	if err := validateRemoteExists(req.Remote); err != nil {
		respondErrorSimple(w, err.Error(), http.StatusBadRequest)
		return
	}

	mountPoint := filepath.Join(coldTierBase, req.Name)

	var id int64
	err := h.db.QueryRow(`
		INSERT INTO cold_tier_mounts (name, remote, remote_path, mount_point, vfs_cache_mode)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING id`,
		req.Name, req.Remote, req.RemotePath, mountPoint, cache,
	).Scan(&id)
	if err != nil {
		if strings.Contains(err.Error(), "unique") || strings.Contains(err.Error(), "UNIQUE") {
			respondErrorSimple(w, fmt.Sprintf("a cold-tier mount named %q already exists", req.Name), http.StatusConflict)
			return
		}
		respondErrorSimple(w, "Failed to save mount record: "+err.Error(), http.StatusInternalServerError)
		return
	}

	m := ColdTierMount{
		ID: id, Name: req.Name, Remote: req.Remote, RemotePath: req.RemotePath,
		MountPoint: mountPoint, VFSCache: cache,
	}

	// Mount in a background job so the HTTP response returns quickly
	jobID := jobs.Start("cold-tier-mount", func(j *jobs.Job) {
		j.Log(fmt.Sprintf("Mounting %s:%s at %s...", req.Remote, req.RemotePath, mountPoint))
		if err := mountRclone(m); err != nil {
			j.Fail(fmt.Sprintf("Mount failed: %v", err))
			return
		}
		if _, err := h.db.Exec(
			"UPDATE cold_tier_mounts SET mounted=1, last_mount_at=NOW() WHERE id=$1", id,
		); err != nil {
			log.Printf("cold_tier: update mounted status: %v", err)
		}
		j.Done(map[string]interface{}{"mounted": true, "mount_point": mountPoint})
	})

	audit.LogActivity(r.Header.Get("X-User"), "cold_tier_create", map[string]interface{}{
		"name": req.Name, "remote": req.Remote, "mount_point": mountPoint,
	})
	respondOK(w, map[string]interface{}{
		"success":     true,
		"mount":       m,
		"job_id":      jobID,
		"message":     "Mount started in background",
	})
}

// HandleDelete DELETE /api/storage/cold-tier/{id}
func (h *ColdTierHandler) HandleDelete(w http.ResponseWriter, r *http.Request) {
	id := mux.Vars(r)["id"]

	var m ColdTierMount
	err := h.db.QueryRow(`
		SELECT id, name, mount_point, mounted FROM cold_tier_mounts WHERE id=$1`, id,
	).Scan(&m.ID, &m.Name, &m.MountPoint, &m.Mounted)
	if err == sql.ErrNoRows {
		respondErrorSimple(w, "Mount not found", http.StatusNotFound)
		return
	}
	if err != nil {
		respondErrorSimple(w, "DB error: "+err.Error(), http.StatusInternalServerError)
		return
	}

	if isMounted(m.MountPoint) {
		if err := unmountRclone(m.MountPoint); err != nil {
			respondErrorSimple(w, "Unmount failed: "+err.Error(), http.StatusInternalServerError)
			return
		}
	}

	// Remove the mountpoint directory if empty
	_ = os.Remove(m.MountPoint)

	if _, err := h.db.Exec("DELETE FROM cold_tier_mounts WHERE id=$1", m.ID); err != nil {
		respondErrorSimple(w, "Failed to delete record: "+err.Error(), http.StatusInternalServerError)
		return
	}

	audit.LogActivity(r.Header.Get("X-User"), "cold_tier_delete", map[string]interface{}{
		"name": m.Name, "mount_point": m.MountPoint,
	})
	respondOK(w, map[string]interface{}{"success": true})
}

// HandleMount POST /api/storage/cold-tier/{id}/mount
func (h *ColdTierHandler) HandleMount(w http.ResponseWriter, r *http.Request) {
	id := mux.Vars(r)["id"]

	var m ColdTierMount
	err := h.db.QueryRow(`
		SELECT id, name, remote, remote_path, mount_point, vfs_cache_mode
		FROM cold_tier_mounts WHERE id=$1`, id,
	).Scan(&m.ID, &m.Name, &m.Remote, &m.RemotePath, &m.MountPoint, &m.VFSCache)
	if err == sql.ErrNoRows {
		respondErrorSimple(w, "Mount not found", http.StatusNotFound)
		return
	}
	if err != nil {
		respondErrorSimple(w, "DB error: "+err.Error(), http.StatusInternalServerError)
		return
	}

	if isMounted(m.MountPoint) {
		respondOK(w, map[string]interface{}{"success": true, "message": "Already mounted"})
		return
	}

	jobID := jobs.Start("cold-tier-mount", func(j *jobs.Job) {
		j.Log(fmt.Sprintf("Mounting %s:%s at %s...", m.Remote, m.RemotePath, m.MountPoint))
		if err := mountRclone(m); err != nil {
			j.Fail(fmt.Sprintf("Mount failed: %v", err))
			return
		}
		if _, err := h.db.Exec("UPDATE cold_tier_mounts SET mounted=1, last_mount_at=NOW() WHERE id=$1", m.ID); err != nil {
			j.Log(fmt.Sprintf("Warning: failed to update mount state in DB: %v", err))
		}
		j.Done(map[string]interface{}{"mounted": true})
	})

	respondOK(w, map[string]interface{}{"success": true, "job_id": jobID})
}

// HandleUnmount POST /api/storage/cold-tier/{id}/unmount
func (h *ColdTierHandler) HandleUnmount(w http.ResponseWriter, r *http.Request) {
	id := mux.Vars(r)["id"]

	var m ColdTierMount
	err := h.db.QueryRow(
		"SELECT id, name, mount_point FROM cold_tier_mounts WHERE id=$1", id,
	).Scan(&m.ID, &m.Name, &m.MountPoint)
	if err == sql.ErrNoRows {
		respondErrorSimple(w, "Mount not found", http.StatusNotFound)
		return
	}
	if err != nil {
		respondErrorSimple(w, "DB error: "+err.Error(), http.StatusInternalServerError)
		return
	}

	if !isMounted(m.MountPoint) {
		if _, err := h.db.Exec("UPDATE cold_tier_mounts SET mounted=0 WHERE id=$1", m.ID); err != nil {
			log.Printf("cold_tier: clear stale mounted flag for %s: %v", m.Name, err)
		}
		respondOK(w, map[string]interface{}{"success": true, "message": "Not currently mounted"})
		return
	}

	if err := unmountRclone(m.MountPoint); err != nil {
		respondErrorSimple(w, "Unmount failed: "+err.Error(), http.StatusInternalServerError)
		return
	}

	if _, err := h.db.Exec("UPDATE cold_tier_mounts SET mounted=0 WHERE id=$1", m.ID); err != nil {
		log.Printf("cold_tier: clear mounted flag for %s: %v", m.Name, err)
	}
	audit.LogActivity(r.Header.Get("X-User"), "cold_tier_unmount", map[string]interface{}{
		"name": m.Name,
	})
	respondOK(w, map[string]interface{}{"success": true})
}

// HandleUsage POST /api/storage/cold-tier/{id}/usage
// Starts a background job that checks disk usage on the mount point.
func (h *ColdTierHandler) HandleUsage(w http.ResponseWriter, r *http.Request) {
	id := mux.Vars(r)["id"]

	var mountPoint, name string
	err := h.db.QueryRow(
		"SELECT mount_point, name FROM cold_tier_mounts WHERE id=$1", id,
	).Scan(&mountPoint, &name)
	if err == sql.ErrNoRows {
		respondErrorSimple(w, "Mount not found", http.StatusNotFound)
		return
	}
	if err != nil {
		respondErrorSimple(w, "DB error: "+err.Error(), http.StatusInternalServerError)
		return
	}

	if !isMounted(mountPoint) {
		respondErrorSimple(w, "Not currently mounted", http.StatusConflict)
		return
	}

	jobID := jobs.Start("cold-tier-usage", func(j *jobs.Job) {
		j.Log(fmt.Sprintf("Checking usage for %s at %s...", name, mountPoint))
		out, err := cmdutil.RunSlow("df", "-h", "--output=used,avail,pcent", mountPoint)
		if err != nil {
			j.Fail(fmt.Sprintf("df failed: %v", err))
			return
		}
		lines := strings.Split(strings.TrimSpace(string(out)), "\n")
		if len(lines) < 2 {
			j.Fail("unexpected df output")
			return
		}
		fields := strings.Fields(lines[1])
		result := map[string]interface{}{"raw": string(out)}
		if len(fields) >= 3 {
			result["used"] = fields[0]
			result["available"] = fields[1]
			result["percent"] = fields[2]
		}
		j.Done(result)
	})

	respondOK(w, map[string]interface{}{"success": true, "job_id": jobID})
}

// ReMountAll is called at daemon startup to restore mounts that were active
// before the daemon stopped. Runs each mount in a goroutine; failures are
// logged but do not block startup.
func (h *ColdTierHandler) ReMountAll() {
	rows, err := h.db.Query(`
		SELECT id, name, remote, remote_path, mount_point, vfs_cache_mode
		FROM cold_tier_mounts WHERE mounted=1`)
	if err != nil {
		log.Printf("cold_tier: list mounts for re-mount: %v", err)
		return
	}
	defer rows.Close()

	for rows.Next() {
		var m ColdTierMount
		if err := rows.Scan(&m.ID, &m.Name, &m.Remote, &m.RemotePath, &m.MountPoint, &m.VFSCache); err != nil {
			log.Printf("cold_tier: scan re-mount row: %v", err)
			continue
		}
		if isMounted(m.MountPoint) {
			continue
		}
		go func(mount ColdTierMount) {
			log.Printf("cold_tier: re-mounting %s at %s", mount.Name, mount.MountPoint)
			if err := mountRclone(mount); err != nil {
				log.Printf("cold_tier: re-mount %s failed: %v", mount.Name, err)
				if _, dbErr := h.db.Exec("UPDATE cold_tier_mounts SET mounted=0 WHERE id=$1", mount.ID); dbErr != nil {
					log.Printf("cold_tier: clear mounted flag for %s: %v", mount.Name, dbErr)
				}
			}
		}(m)
	}
}
