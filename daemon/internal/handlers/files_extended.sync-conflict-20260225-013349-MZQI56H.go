package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"syscall"
)

type FilesExtendedHandler struct{}

func NewFilesExtendedHandler() *FilesExtendedHandler {
	return &FilesExtendedHandler{}
}

type FileInfo struct {
	Name     string `json:"name"`
	Path     string `json:"path"`
	Size     int64  `json:"size"`
	IsDir    bool   `json:"is_dir"`
	Mode     string `json:"mode"`
	ModTime  int64  `json:"mod_time"`
	Owner    string `json:"owner"`
	Group    string `json:"group"`
	MimeType string `json:"mime_type,omitempty"`
}

type FileListResponse struct {
	Success bool       `json:"success"`
	Files   []FileInfo `json:"files"`
	Path    string     `json:"path"`
	Error   string     `json:"error,omitempty"`
}

type FilePropertiesResponse struct {
	Success    bool   `json:"success"`
	Name       string `json:"name"`
	Path       string `json:"path"`
	Size       int64  `json:"size"`
	IsDir      bool   `json:"is_dir"`
	Mode       string `json:"mode"`
	ModTime    int64  `json:"mod_time"`
	Owner      string `json:"owner"`
	Group      string `json:"group"`
	MimeType   string `json:"mime_type,omitempty"`
	Error      string `json:"error,omitempty"`
}

// ListFiles lists files in a directory
func (h *FilesExtendedHandler) ListFiles(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Query().Get("path")
	if path == "" {
		path = "/"
	}

	// Sanitize path
	path = filepath.Clean(path)

	entries, err := os.ReadDir(path)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(FileListResponse{
			Success: false,
			Error:   fmt.Sprintf("Failed to read directory: %v", err),
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
		
		fileInfo := FileInfo{
			Name:    entry.Name(),
			Path:    fullPath,
			Size:    info.Size(),
			IsDir:   entry.IsDir(),
			Mode:    info.Mode().String(),
			ModTime: info.ModTime().Unix(),
		}

		// Get owner/group on Unix systems
		if stat, ok := info.Sys().(*syscall.Stat_t); ok {
			fileInfo.Owner = fmt.Sprintf("%d", stat.Uid)
			fileInfo.Group = fmt.Sprintf("%d", stat.Gid)
		}

		files = append(files, fileInfo)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(FileListResponse{
		Success: true,
		Files:   files,
		Path:    path,
	})
}

// GetFileProperties gets detailed properties of a file
func (h *FilesExtendedHandler) GetFileProperties(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Query().Get("path")
	if path == "" {
		http.Error(w, "Path parameter required", http.StatusBadRequest)
		return
	}

	path = filepath.Clean(path)

	info, err := os.Stat(path)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(FilePropertiesResponse{
			Success: false,
			Error:   fmt.Sprintf("Failed to stat file: %v", err),
		})
		return
	}

	response := FilePropertiesResponse{
		Success: true,
		Name:    filepath.Base(path),
		Path:    path,
		Size:    info.Size(),
		IsDir:   info.IsDir(),
		Mode:    info.Mode().String(),
		ModTime: info.ModTime().Unix(),
	}

	// Get owner/group
	if stat, ok := info.Sys().(*syscall.Stat_t); ok {
		response.Owner = fmt.Sprintf("%d", stat.Uid)
		response.Group = fmt.Sprintf("%d", stat.Gid)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}

// RenameFile renames a file or directory
func (h *FilesExtendedHandler) RenameFile(w http.ResponseWriter, r *http.Request) {
	var req struct {
		OldPath string `json:"old_path"`
		NewPath string `json:"new_path"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	req.OldPath = filepath.Clean(req.OldPath)
	req.NewPath = filepath.Clean(req.NewPath)

	if err := os.Rename(req.OldPath, req.NewPath); err != nil {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": false,
			"error":   err.Error(),
		})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]bool{"success": true})
}

// CopyFile copies a file or directory
func (h *FilesExtendedHandler) CopyFile(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Source      string `json:"source"`
		Destination string `json:"destination"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	req.Source = filepath.Clean(req.Source)
	req.Destination = filepath.Clean(req.Destination)

	// Simple file copy (doesn't handle directories recursively)
	sourceFile, err := os.Open(req.Source)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": false,
			"error":   err.Error(),
		})
		return
	}
	defer sourceFile.Close()

	destFile, err := os.Create(req.Destination)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": false,
			"error":   err.Error(),
		})
		return
	}
	defer destFile.Close()

	_, err = destFile.ReadFrom(sourceFile)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": false,
			"error":   err.Error(),
		})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]bool{"success": true})
}

// UploadChunk handles chunked file uploads
func (h *FilesExtendedHandler) UploadChunk(w http.ResponseWriter, r *http.Request) {
	// Parse multipart form
	if err := r.ParseMultipartForm(32 << 20); err != nil { // 32 MB max
		http.Error(w, "Failed to parse form", http.StatusBadRequest)
		return
	}

	file, header, err := r.FormFile("file")
	if err != nil {
		http.Error(w, "Failed to get file from form", http.StatusBadRequest)
		return
	}
	defer file.Close()

	targetPath := r.FormValue("path")
	if targetPath == "" {
		http.Error(w, "Path parameter required", http.StatusBadRequest)
		return
	}

	targetPath = filepath.Clean(targetPath)

	// Check if this is a chunked upload
	chunkIndex := r.FormValue("chunkIndex")
	totalChunks := r.FormValue("totalChunks")

	if chunkIndex != "" && totalChunks != "" {
		// Chunked upload: append to file
		idx := 0
		fmt.Sscanf(chunkIndex, "%d", &idx)

		var openFlag int
		if idx == 0 {
			openFlag = os.O_CREATE | os.O_WRONLY | os.O_TRUNC
		} else {
			openFlag = os.O_CREATE | os.O_WRONLY | os.O_APPEND
		}

		targetFile, err := os.OpenFile(targetPath, openFlag, 0644)
		if err != nil {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]interface{}{
				"success": false,
				"error":   fmt.Sprintf("Failed to open file: %v", err),
			})
			return
		}
		defer targetFile.Close()

		_, err = targetFile.ReadFrom(file)
		if err != nil {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]interface{}{
				"success": false,
				"error":   fmt.Sprintf("Failed to write chunk: %v", err),
			})
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": true,
			"chunk":   idx,
			"message": "Chunk uploaded",
		})
		return
	}

	// Simple (non-chunked) upload
	// If path doesn't include filename, append it
	if fi, err := os.Stat(targetPath); err == nil && fi.IsDir() {
		targetPath = filepath.Join(targetPath, header.Filename)
	}

	// Create target file
	targetFile, err := os.Create(targetPath)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": false,
			"error":   fmt.Sprintf("Failed to create file: %v", err),
		})
		return
	}
	defer targetFile.Close()

	// Copy uploaded data to target
	_, err = targetFile.ReadFrom(file)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": false,
			"error":   fmt.Sprintf("Failed to write file: %v", err),
		})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success":  true,
		"filename": header.Filename,
		"size":     header.Size,
	})
}
