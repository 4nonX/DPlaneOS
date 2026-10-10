package handlers

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"

	"dplaned/internal/audit"
	"dplaned/internal/cmdutil"
	"dplaned/internal/middleware"
	"dplaned/internal/security"
	"dplaned/internal/zfs"
)

// allowedBasePaths: the file roots as path prefixes (pool-root checks below).
// Path checks themselves: files_paths.go.
var allowedBasePaths = func() []string {
	out := make([]string, len(fileRoots))
	for i, r := range fileRoots {
		out[i] = r + "/"
	}
	return out
}()

// CreateDirectory creates a directory
func CreateDirectory(w http.ResponseWriter, r *http.Request) {
	// Use middleware context (Finding 32)
	u := r.Context().Value(middleware.UserContextKey)
	if u == nil {
		respondErrorSimple(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	user := u.(*middleware.User).Username

	var req struct {
		Path string `json:"path"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondErrorSimple(w, "Invalid request", http.StatusBadRequest)
		return
	}

	safePath, err := resolveEntry(req.Path)
	if err != nil {
		respondJSON(w, http.StatusForbidden, map[string]any{"success": false, "error": "Path not allowed"})
		return
	}
	req.Path = safePath
	output, err := cmdutil.RunFast("mkdir", "-p", req.Path)

	audit.LogActivity(user, "directory_create", map[string]any{
		"path":    req.Path,
		"success": err == nil,
	})

	if err != nil {
		respondError(w, http.StatusInternalServerError, "Operation failed", err)
		return
	}

	json.NewEncoder(w).Encode(map[string]any{
		"success": true,
		"path":    req.Path,
		"output":  string(output),
	})
}

// DeletePath deletes a file or directory
func DeletePath(w http.ResponseWriter, r *http.Request) {
	// Use middleware context (Finding 32)
	u := r.Context().Value(middleware.UserContextKey)
	if u == nil {
		respondErrorSimple(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	user := u.(*middleware.User).Username

	var req struct {
		Path string `json:"path"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondErrorSimple(w, "Invalid request", http.StatusBadRequest)
		return
	}

	const poolRootMsg = "Cannot delete a ZFS pool root or system base path via the file browser. Use the Storage tab to destroy pools."
	// A pool root or dataset mountpoint (checked on the real path too).
	if real, rerr := filepath.EvalSymlinks(filepath.Clean(req.Path)); rerr == nil && (isFileRoot(real) || isPoolRoot(real)) {
		respondJSON(w, http.StatusForbidden, map[string]any{"success": false, "error": poolRootMsg})
		return
	}
	safePath, err := resolveEntry(req.Path)
	if err != nil {
		respondJSON(w, http.StatusForbidden, map[string]any{"success": false, "error": "Path not allowed"})
		return
	}
	req.Path = safePath

	// GUARD: Prevent recursive deletion of pool roots or critical base paths (Finding 33)
	if isPoolRoot(req.Path) {
		respondJSON(w, http.StatusForbidden, map[string]any{"success": false, "error": poolRootMsg})
		return
	}

	output, err := cmdutil.RunFast("rm", "-rf", req.Path)

	audit.LogActivity(user, "path_delete", map[string]any{
		"path":    req.Path,
		"success": err == nil,
	})

	if err != nil {
		respondError(w, http.StatusInternalServerError, "Operation failed", err)
		return
	}

	json.NewEncoder(w).Encode(map[string]any{
		"success": true,
		"path":    req.Path,
		"output":  string(output),
	})
}

// ChangeOwnership changes file/directory ownership
func ChangeOwnership(w http.ResponseWriter, r *http.Request) {
	// Use middleware context (Finding 32)
	u := r.Context().Value(middleware.UserContextKey)
	if u == nil {
		respondErrorSimple(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	user := u.(*middleware.User).Username

	var req struct {
		Path  string `json:"path"`
		Owner string `json:"owner"`
		Group string `json:"group"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondErrorSimple(w, "Invalid request", http.StatusBadRequest)
		return
	}

	safePath, err := resolveExisting(req.Path)
	if err != nil {
		respondJSON(w, http.StatusForbidden, map[string]any{"success": false, "error": "Path not allowed"})
		return
	}
	req.Path = safePath

	ownerGroup := req.Owner
	if req.Group != "" {
		ownerGroup = req.Owner + ":" + req.Group
	}

	output, err := cmdutil.RunFast("chown", ownerGroup, req.Path)

	audit.LogActivity(user, "ownership_change", map[string]any{
		"path":    req.Path,
		"owner":   req.Owner,
		"group":   req.Group,
		"success": err == nil,
	})

	if err != nil {
		respondError(w, http.StatusInternalServerError, "Operation failed", err)
		return
	}

	_ = output
	json.NewEncoder(w).Encode(map[string]any{
		"success": true,
	})
}

// ChangePermissions changes file/directory permissions
func ChangePermissions(w http.ResponseWriter, r *http.Request) {
	user := r.Header.Get("X-User")
	sessionID := r.Header.Get("X-Session-ID")

	if valid, _ := security.ValidateSession(sessionID, user); !valid {
		respondErrorSimple(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	var req struct {
		Path        string `json:"path"`
		Mode        string `json:"mode"`        // preferred (matches frontend)
		Permissions string `json:"permissions"` // legacy alias
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondErrorSimple(w, "Invalid request", http.StatusBadRequest)
		return
	}
	// Accept either field name
	if req.Mode == "" {
		req.Mode = req.Permissions
	}
	if req.Mode == "" {
		respondErrorSimple(w, "mode is required", http.StatusBadRequest)
		return
	}

	safePath, err := resolveExisting(req.Path)
	if err != nil {
		respondJSON(w, http.StatusForbidden, map[string]any{"success": false, "error": "Path not allowed"})
		return
	}
	req.Path = safePath
	if _, err := parseFileMode(req.Mode); err != nil {
		respondErrorSimple(w, err.Error(), http.StatusBadRequest)
		return
	}

	output, err := cmdutil.RunFast("chmod", req.Mode, req.Path)

	audit.LogActivity(user, "permissions_change", map[string]any{
		"path":    req.Path,
		"mode":    req.Mode,
		"success": err == nil,
	})

	if err != nil {
		respondError(w, http.StatusInternalServerError, "Operation failed", err)
		return
	}

	_ = output
	json.NewEncoder(w).Encode(map[string]any{
		"success": true,
	})
}

// isPoolRoot checks if a path is a ZFS pool root or system base path
func isPoolRoot(path string) bool {
	cleaned := filepath.Clean(path)
	// 1. Check against base paths
	for _, base := range allowedBasePaths {
		if cleaned == filepath.Clean(base) {
			return true
		}
	}

	// 2. Dynamic check against all mounted ZFS pools (#33)
	pools, err := zfs.DiscoverPools()
	if err == nil {
		for _, p := range pools {
			if cleaned == filepath.Clean(p.MountPoint) {
				return true
			}
		}
	}

	// 3. Check for top-level directories in /mnt/ or /tank/ etc. (Legacy/Fallback)
	// Only block if depth is exactly 1 under /mnt/, /tank/, /data/ (e.g., /mnt/pool)
	parts := strings.Split(strings.Trim(cleaned, "/"), "/")
	if len(parts) == 2 {
		parent := "/" + parts[0] + "/"
		for _, base := range []string{"/mnt/", "/tank/", "/data/", "/media/"} {
			if parent == base {
				return true
			}
		}
	}
	return false
}

