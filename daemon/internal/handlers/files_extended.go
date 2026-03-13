package handlers

import (
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"dplaned/internal/audit"
	"dplaned/internal/cmdutil"
)

type FilesExtendedHandler struct{}

func NewFilesExtendedHandler() *FilesExtendedHandler {
	return &FilesExtendedHandler{}
}

// FileInfo is the canonical file entry returned by ListFiles.
// mtime is an RFC3339 string so the frontend can parse it directly.
// mode is a 4-digit octal string (e.g. "0755").
// permissions is the symbolic string (e.g. "drwxr-xr-x").
type FileInfo struct {
	Name        string `json:"name"`
	Path        string `json:"path"`
	Size        int64  `json:"size"`
	IsDir       bool   `json:"is_dir"`
	Mode        string `json:"mode"`        // octal, e.g. "0644"
	Permissions string `json:"permissions"` // symbolic, e.g. "-rw-r--r--"
	Mtime       string `json:"mtime"`       // RFC3339
	Owner       string `json:"owner"`       // UID as string (resolve in UI if needed)
	Group       string `json:"group"`       // GID as string
}

type FileListResponse struct {
	Success bool       `json:"success"`
	Files   []FileInfo `json:"files"`
	Path    string     `json:"path"`
	Error   string     `json:"error,omitempty"`
}

// ─────────────────────────────────────────────────────────────────────────────
// GET /api/files/list?path=
// ─────────────────────────────────────────────────────────────────────────────

func (h *FilesExtendedHandler) ListFiles(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Query().Get("path")
	if path == "" {
		path = "/mnt"
	}
	path = filepath.Clean(path)

	entries, err := os.ReadDir(path)
	if err != nil {
		respondJSON(w, http.StatusOK, FileListResponse{
			Success: false,
			Error:   fmt.Sprintf("Cannot read directory: %v", err),
		})
		return
	}

	files := make([]FileInfo, 0, len(entries))
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil {
			continue
		}
		fullPath := filepath.Join(path, entry.Name())
		fi := buildFileInfo(fullPath, info)
		files = append(files, fi)
	}

	// Directories first, then files; each group sorted alphabetically
	sort.Slice(files, func(i, j int) bool {
		if files[i].IsDir != files[j].IsDir {
			return files[i].IsDir
		}
		return strings.ToLower(files[i].Name) < strings.ToLower(files[j].Name)
	})

	respondJSON(w, http.StatusOK, FileListResponse{
		Success: true,
		Files:   files,
		Path:    path,
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// GET /api/files/properties?path=
// ─────────────────────────────────────────────────────────────────────────────

func (h *FilesExtendedHandler) GetFileProperties(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Query().Get("path")
	if path == "" {
		respondErrorSimple(w, "path parameter required", http.StatusBadRequest)
		return
	}
	path = filepath.Clean(path)
	info, err := os.Stat(path)
	if err != nil {
		respondJSON(w, http.StatusOK, map[string]interface{}{"success": false, "error": err.Error()})
		return
	}
	respondJSON(w, http.StatusOK, map[string]interface{}{
		"success": true,
		"file":    buildFileInfo(path, info),
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// GET /api/files/read?path=
// Returns text content. Capped at 2 MB.
// ─────────────────────────────────────────────────────────────────────────────

const maxReadBytes = 2 * 1024 * 1024

func (h *FilesExtendedHandler) ReadFile(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Query().Get("path")
	if path == "" {
		respondErrorSimple(w, "path parameter required", http.StatusBadRequest)
		return
	}
	safePath, ok := validateFilePath(filepath.Clean(path))
	if !ok {
		respondJSON(w, http.StatusForbidden, map[string]interface{}{"success": false, "error": "Path not allowed"})
		return
	}
	info, err := os.Stat(safePath)
	if err != nil {
		respondJSON(w, http.StatusOK, map[string]interface{}{"success": false, "error": err.Error()})
		return
	}
	if info.IsDir() {
		respondJSON(w, http.StatusOK, map[string]interface{}{"success": false, "error": "Path is a directory"})
		return
	}
	if info.Size() > maxReadBytes {
		respondJSON(w, http.StatusOK, map[string]interface{}{
			"success":   false,
			"too_large": true,
			"size":      info.Size(),
			"error":     fmt.Sprintf("File is %s — too large to edit in browser (max 2 MB). Download instead.", fileSizeStr(info.Size())),
		})
		return
	}
	data, err := os.ReadFile(safePath)
	if err != nil {
		respondJSON(w, http.StatusOK, map[string]interface{}{"success": false, "error": err.Error()})
		return
	}
	respondJSON(w, http.StatusOK, map[string]interface{}{
		"success": true,
		"path":    safePath,
		"content": string(data),
		"size":    info.Size(),
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// POST /api/files/write
// Body: { "path": "/mnt/tank/foo.conf", "content": "...", "mode": "0644" }
// ─────────────────────────────────────────────────────────────────────────────

func (h *FilesExtendedHandler) WriteFile(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Path    string `json:"path"`
		Content string `json:"content"`
		Mode    string `json:"mode"` // optional octal string
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondErrorSimple(w, "Invalid request body", http.StatusBadRequest)
		return
	}
	safePath, ok := validateFilePath(filepath.Clean(req.Path))
	if !ok {
		respondJSON(w, http.StatusForbidden, map[string]interface{}{"success": false, "error": "Path not allowed"})
		return
	}
	perm := os.FileMode(0644)
	if req.Mode != "" {
		if v, err := strconv.ParseUint(req.Mode, 8, 32); err == nil {
			perm = os.FileMode(v)
		}
	}
	if err := os.WriteFile(safePath, []byte(req.Content), perm); err != nil {
		respondJSON(w, http.StatusOK, map[string]interface{}{"success": false, "error": err.Error()})
		return
	}
	user := r.Header.Get("X-User")
	audit.LogActivity(user, "file_write", map[string]interface{}{"path": safePath, "size": len(req.Content)})
	respondJSON(w, http.StatusOK, map[string]interface{}{"success": true, "path": safePath, "size": len(req.Content)})
}

// ─────────────────────────────────────────────────────────────────────────────
// GET /api/files/download?path=
// Streams file as an attachment. Auth is checked via session header.
// ─────────────────────────────────────────────────────────────────────────────

func (h *FilesExtendedHandler) DownloadFile(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Query().Get("path")
	if path == "" {
		respondErrorSimple(w, "path required", http.StatusBadRequest)
		return
	}
	safePath, ok := validateFilePath(filepath.Clean(path))
	if !ok {
		respondErrorSimple(w, "Path not allowed", http.StatusForbidden)
		return
	}
	info, err := os.Stat(safePath)
	if err != nil || info.IsDir() {
		respondErrorSimple(w, "File not found", http.StatusNotFound)
		return
	}
	f, err := os.Open(safePath)
	if err != nil {
		http.Error(w, "Cannot open file", http.StatusInternalServerError)
		return
	}
	defer f.Close()

	ext := strings.ToLower(filepath.Ext(safePath))
	ct := mime.TypeByExtension(ext)
	if ct == "" {
		ct = "application/octet-stream"
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename=%q`, filepath.Base(safePath)))
	w.Header().Set("Content-Length", strconv.FormatInt(info.Size(), 10))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	io.Copy(w, f) //nolint:errcheck

	user := r.Header.Get("X-User")
	audit.LogActivity(user, "file_download", map[string]interface{}{"path": safePath})
}

// ─────────────────────────────────────────────────────────────────────────────
// POST /api/files/rename
// Body: { "old_path": "...", "new_name": "newname.txt" }
// new_name is the filename only — rename stays within the same directory.
// ─────────────────────────────────────────────────────────────────────────────

func (h *FilesExtendedHandler) RenameFile(w http.ResponseWriter, r *http.Request) {
	var req struct {
		OldPath string `json:"old_path"`
		NewName string `json:"new_name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondErrorSimple(w, "Invalid request body", http.StatusBadRequest)
		return
	}
	oldPath, ok := validateFilePath(filepath.Clean(req.OldPath))
	if !ok {
		respondJSON(w, http.StatusForbidden, map[string]interface{}{"success": false, "error": "Path not allowed"})
		return
	}
	newName := filepath.Base(req.NewName)
	if newName == "" || newName == "." || newName == ".." || strings.ContainsAny(newName, "/\\") {
		respondJSON(w, http.StatusBadRequest, map[string]interface{}{"success": false, "error": "Invalid filename"})
		return
	}
	newPath := filepath.Join(filepath.Dir(oldPath), newName)
	if err := os.Rename(oldPath, newPath); err != nil {
		respondJSON(w, http.StatusOK, map[string]interface{}{"success": false, "error": err.Error()})
		return
	}
	user := r.Header.Get("X-User")
	audit.LogActivity(user, "file_rename", map[string]interface{}{"old": oldPath, "new": newPath})
	respondJSON(w, http.StatusOK, map[string]interface{}{"success": true, "new_path": newPath})
}

// ─────────────────────────────────────────────────────────────────────────────
// POST /api/files/copy  — handles files and directories via cp -a
// Body: { "source": "...", "destination": "..." }
// ─────────────────────────────────────────────────────────────────────────────

func (h *FilesExtendedHandler) CopyFile(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Source      string `json:"source"`
		Destination string `json:"destination"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondErrorSimple(w, "Invalid request body", http.StatusBadRequest)
		return
	}
	src, ok1 := validateFilePath(filepath.Clean(req.Source))
	dst, ok2 := validateFilePath(filepath.Clean(req.Destination))
	if !ok1 || !ok2 {
		respondJSON(w, http.StatusForbidden, map[string]interface{}{"success": false, "error": "Path not allowed"})
		return
	}
	out, err := cmdutil.RunMedium("/bin/cp", "-a", src, dst)
	if err != nil {
		respondJSON(w, http.StatusOK, map[string]interface{}{"success": false, "error": fmt.Sprintf("%v: %s", err, out)})
		return
	}
	user := r.Header.Get("X-User")
	audit.LogActivity(user, "file_copy", map[string]interface{}{"src": src, "dst": dst})
	respondJSON(w, http.StatusOK, map[string]interface{}{"success": true, "destination": dst})
}

// ─────────────────────────────────────────────────────────────────────────────
// POST /api/files/move
// Body: { "source": "...", "destination": "..." }
// ─────────────────────────────────────────────────────────────────────────────

func (h *FilesExtendedHandler) MoveFile(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Source      string `json:"source"`
		Destination string `json:"destination"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondErrorSimple(w, "Invalid request body", http.StatusBadRequest)
		return
	}
	src, ok1 := validateFilePath(filepath.Clean(req.Source))
	dst, ok2 := validateFilePath(filepath.Clean(req.Destination))
	if !ok1 || !ok2 {
		respondJSON(w, http.StatusForbidden, map[string]interface{}{"success": false, "error": "Path not allowed"})
		return
	}
	// os.Rename is atomic on same filesystem; falls back to cp+rm across filesystems
	if err := os.Rename(src, dst); err != nil {
		out, cpErr := cmdutil.RunMedium("/bin/cp", "-a", src, dst)
		if cpErr != nil {
			respondJSON(w, http.StatusOK, map[string]interface{}{"success": false, "error": fmt.Sprintf("%v: %s", cpErr, out)})
			return
		}
		if rmErr := os.RemoveAll(src); rmErr != nil {
			respondJSON(w, http.StatusOK, map[string]interface{}{"success": false, "error": "Copied but could not remove source: " + rmErr.Error()})
			return
		}
	}
	user := r.Header.Get("X-User")
	audit.LogActivity(user, "file_move", map[string]interface{}{"src": src, "dst": dst})
	respondJSON(w, http.StatusOK, map[string]interface{}{"success": true, "destination": dst})
}

// ─────────────────────────────────────────────────────────────────────────────
// POST /api/files/upload  (chunked multipart)
// Form fields: file (blob), path (dest dir), filename, chunk (0-based), totalChunks
// ─────────────────────────────────────────────────────────────────────────────

func (h *FilesExtendedHandler) UploadChunk(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		respondErrorSimple(w, "Failed to parse form", http.StatusBadRequest)
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		respondErrorSimple(w, "Failed to get file from form", http.StatusBadRequest)
		return
	}
	defer file.Close()

	destDir := filepath.Clean(r.FormValue("path"))
	filename := r.FormValue("filename")
	if filename == "" {
		filename = header.Filename
	}
	filename = filepath.Base(filename) // no path separators

	destDir, ok := validateFilePath(destDir)
	if !ok {
		respondJSON(w, http.StatusForbidden, map[string]interface{}{"success": false, "error": "Path not allowed"})
		return
	}
	targetPath := filepath.Join(destDir, filename)

	chunkStr := r.FormValue("chunk")
	totalStr := r.FormValue("totalChunks")

	if chunkStr != "" && totalStr != "" {
		idx, _ := strconv.Atoi(chunkStr)
		flag := os.O_CREATE | os.O_WRONLY | os.O_APPEND
		if idx == 0 {
			flag = os.O_CREATE | os.O_WRONLY | os.O_TRUNC
		}
		f, err := os.OpenFile(targetPath, flag, 0644)
		if err != nil {
			respondJSON(w, http.StatusOK, map[string]interface{}{"success": false, "error": err.Error()})
			return
		}
		defer f.Close()
		if _, err := io.Copy(f, file); err != nil {
			respondJSON(w, http.StatusOK, map[string]interface{}{"success": false, "error": err.Error()})
			return
		}
		respondJSON(w, http.StatusOK, map[string]interface{}{"success": true, "chunk": idx})
		return
	}

	// Non-chunked
	f, err := os.Create(targetPath)
	if err != nil {
		respondJSON(w, http.StatusOK, map[string]interface{}{"success": false, "error": err.Error()})
		return
	}
	defer f.Close()
	n, err := io.Copy(f, file)
	if err != nil {
		respondJSON(w, http.StatusOK, map[string]interface{}{"success": false, "error": err.Error()})
		return
	}
	respondJSON(w, http.StatusOK, map[string]interface{}{"success": true, "filename": filename, "size": n})
}

// ─────────────────────────────────────────────────────────────────────────────
// Helpers
// ─────────────────────────────────────────────────────────────────────────────

func buildFileInfo(fullPath string, info os.FileInfo) FileInfo {
	fi := FileInfo{
		Name:        filepath.Base(fullPath),
		Path:        fullPath,
		Size:        info.Size(),
		IsDir:       info.IsDir(),
		Mode:        fmt.Sprintf("%04o", info.Mode().Perm()),
		Permissions: info.Mode().String(),
		Mtime:       info.ModTime().UTC().Format(time.RFC3339),
	}
	if stat, ok := info.Sys().(*syscall.Stat_t); ok {
		fi.Owner = fmt.Sprintf("%d", stat.Uid)
		fi.Group = fmt.Sprintf("%d", stat.Gid)
	}
	return fi
}

func fileSizeStr(b int64) string {
	units := []string{"B", "KB", "MB", "GB", "TB"}
	v := float64(b)
	i := 0
	for v >= 1024 && i < len(units)-1 {
		v /= 1024
		i++
	}
	return fmt.Sprintf("%.1f %s", v, units[i])
}
