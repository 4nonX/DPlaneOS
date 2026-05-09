package handlers

import (
	"encoding/json"
	"fmt"
	"log"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"dplaned/internal/audit"

	"github.com/google/uuid"
	"github.com/gorilla/mux"
	"golang.org/x/crypto/bcrypt"
)

// ═══════════════════════════════════════════════════════════════
//  Shareable file-download links
//
//  Authenticated users create time-limited, optionally password-
//  protected, optionally download-count-capped download tokens.
//  Recipients download at GET /api/s/{token}/download (public).
// ═══════════════════════════════════════════════════════════════

const fileSharesFile = "file-shares.json"

var fileSharesMu sync.RWMutex

// FileShare is the on-disk representation. PasswordHash is never
// returned to the API caller; only HasPassword (the boolean) is.
type FileShare struct {
	ID            string     `json:"id"`
	Token         string     `json:"token"`
	Path          string     `json:"path"`
	Filename      string     `json:"filename"`
	CreatedBy     string     `json:"created_by"`
	CreatedAt     time.Time  `json:"created_at"`
	ExpiresAt     *time.Time `json:"expires_at,omitempty"`
	PasswordHash  string     `json:"password_hash,omitempty"`
	HasPassword   bool       `json:"has_password"`
	MaxDownloads  int        `json:"max_downloads"`
	DownloadCount int        `json:"download_count"`
	Revoked       bool       `json:"revoked"`
}

// fileSharePublic is what the API sends to authenticated callers
// and to the public info endpoint. PasswordHash is stripped.
type fileSharePublic struct {
	ID            string     `json:"id"`
	Token         string     `json:"token"`
	Path          string     `json:"path"`
	Filename      string     `json:"filename"`
	CreatedBy     string     `json:"created_by"`
	CreatedAt     time.Time  `json:"created_at"`
	ExpiresAt     *time.Time `json:"expires_at,omitempty"`
	HasPassword   bool       `json:"has_password"`
	MaxDownloads  int        `json:"max_downloads"`
	DownloadCount int        `json:"download_count"`
	Revoked       bool       `json:"revoked"`
}

func toPublic(s FileShare) fileSharePublic {
	return fileSharePublic{
		ID: s.ID, Token: s.Token, Path: s.Path, Filename: s.Filename,
		CreatedBy: s.CreatedBy, CreatedAt: s.CreatedAt, ExpiresAt: s.ExpiresAt,
		HasPassword: s.HasPassword, MaxDownloads: s.MaxDownloads,
		DownloadCount: s.DownloadCount, Revoked: s.Revoked,
	}
}

func loadFileShares() ([]FileShare, error) {
	fileSharesMu.RLock()
	defer fileSharesMu.RUnlock()

	data, err := os.ReadFile(configPath(fileSharesFile))
	if err != nil {
		if os.IsNotExist(err) {
			return []FileShare{}, nil
		}
		return nil, err
	}
	var out []FileShare
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func saveFileShares(shares []FileShare) error {
	fileSharesMu.Lock()
	defer fileSharesMu.Unlock()

	os.MkdirAll(ConfigDir, 0755)
	data, err := json.MarshalIndent(shares, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(configPath(fileSharesFile), data, 0600)
}

// findShare looks up a share by token without holding any lock
// (caller must hold at least a read lock or work on a local copy).
func findShareByToken(shares []FileShare, token string) *FileShare {
	for i := range shares {
		if shares[i].Token == token {
			return &shares[i]
		}
	}
	return nil
}

// shareValid returns an error string if the share cannot be served.
func shareValid(s FileShare) string {
	if s.Revoked {
		return "This link has been revoked"
	}
	if s.ExpiresAt != nil && time.Now().After(*s.ExpiresAt) {
		return "This link has expired"
	}
	if s.MaxDownloads > 0 && s.DownloadCount >= s.MaxDownloads {
		return "This link has reached its download limit"
	}
	return ""
}

// ─── Authenticated handlers ────────────────────────────────────

// ListFileShares GET /api/file-shares
func ListFileShares(w http.ResponseWriter, r *http.Request) {
	shares, err := loadFileShares()
	if err != nil {
		respondErrorSimple(w, "Failed to load file shares", http.StatusInternalServerError)
		return
	}
	out := make([]fileSharePublic, len(shares))
	for i, s := range shares {
		out[i] = toPublic(s)
	}
	respondOK(w, map[string]interface{}{"success": true, "shares": out})
}

// CreateFileShare POST /api/file-shares
//
//	Body: { path, expires_in_hours (0=never), password, max_downloads (0=unlimited) }
func CreateFileShare(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Path           string `json:"path"`
		ExpiresInHours int    `json:"expires_in_hours"`
		Password       string `json:"password"`
		MaxDownloads   int    `json:"max_downloads"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondErrorSimple(w, "Invalid JSON", http.StatusBadRequest)
		return
	}
	if req.Path == "" {
		respondErrorSimple(w, "path is required", http.StatusBadRequest)
		return
	}

	// Validate path: must be absolute, no traversal, must exist and be a file
	clean := filepath.Clean(req.Path)
	if !filepath.IsAbs(clean) || strings.Contains(clean, "..") {
		respondErrorSimple(w, "invalid path", http.StatusBadRequest)
		return
	}
	info, err := os.Stat(clean)
	if err != nil {
		respondErrorSimple(w, "path does not exist: "+clean, http.StatusBadRequest)
		return
	}
	if info.IsDir() {
		respondErrorSimple(w, "sharing directories is not supported; select a specific file", http.StatusBadRequest)
		return
	}

	share := FileShare{
		ID:           uuid.New().String(),
		Token:        uuid.New().String(),
		Path:         clean,
		Filename:     filepath.Base(clean),
		CreatedBy:    r.Header.Get("X-User"),
		CreatedAt:    time.Now(),
		MaxDownloads: req.MaxDownloads,
	}

	if req.ExpiresInHours > 0 {
		t := time.Now().Add(time.Duration(req.ExpiresInHours) * time.Hour)
		share.ExpiresAt = &t
	}

	if req.Password != "" {
		hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
		if err != nil {
			respondErrorSimple(w, "Failed to hash password", http.StatusInternalServerError)
			return
		}
		share.PasswordHash = string(hash)
		share.HasPassword = true
	}

	shares, err := loadFileShares()
	if err != nil {
		respondErrorSimple(w, "Failed to load shares", http.StatusInternalServerError)
		return
	}
	shares = append(shares, share)
	if err := saveFileShares(shares); err != nil {
		respondErrorSimple(w, "Failed to save share", http.StatusInternalServerError)
		return
	}

	audit.LogActivity(share.CreatedBy, "file_share_create", map[string]interface{}{
		"id": share.ID, "path": share.Path, "has_password": share.HasPassword,
	})
	respondOK(w, map[string]interface{}{"success": true, "share": toPublic(share)})
}

// DeleteFileShare DELETE /api/file-shares/{id}
func DeleteFileShare(w http.ResponseWriter, r *http.Request) {
	id := mux.Vars(r)["id"]

	shares, err := loadFileShares()
	if err != nil {
		respondErrorSimple(w, "Failed to load shares", http.StatusInternalServerError)
		return
	}

	found := false
	for i := range shares {
		if shares[i].ID == id {
			shares[i].Revoked = true
			found = true
			break
		}
	}
	if !found {
		respondErrorSimple(w, "Share not found", http.StatusNotFound)
		return
	}
	if err := saveFileShares(shares); err != nil {
		respondErrorSimple(w, "Failed to save shares", http.StatusInternalServerError)
		return
	}

	audit.LogActivity(r.Header.Get("X-User"), "file_share_revoke", map[string]interface{}{"id": id})
	respondOK(w, map[string]interface{}{"success": true})
}

// ─── Public handlers (no session required) ─────────────────────

// GetFileShareInfo GET /api/s/{token}
// Returns metadata about the share so the UI can decide whether to
// show a password form or proceed directly to download.
func GetFileShareInfo(w http.ResponseWriter, r *http.Request) {
	token := mux.Vars(r)["token"]

	shares, err := loadFileShares()
	if err != nil {
		respondErrorSimple(w, "Internal error", http.StatusInternalServerError)
		return
	}
	s := findShareByToken(shares, token)
	if s == nil {
		respondErrorSimple(w, "Link not found", http.StatusNotFound)
		return
	}
	if msg := shareValid(*s); msg != "" {
		respondErrorSimple(w, msg, http.StatusGone)
		return
	}

	info, err := os.Stat(s.Path)
	size := int64(-1)
	if err == nil {
		size = info.Size()
	}

	respondOK(w, map[string]interface{}{
		"success":      true,
		"filename":     s.Filename,
		"size":         size,
		"has_password": s.HasPassword,
		"expires_at":   s.ExpiresAt,
		"max_downloads": s.MaxDownloads,
		"download_count": s.DownloadCount,
	})
}

// DownloadFileShare GET /api/s/{token}/download?pw={password}
// Streams the file to the caller. For password-protected shares the
// ?pw query parameter is required. Increments the download counter.
func DownloadFileShare(w http.ResponseWriter, r *http.Request) {
	token := mux.Vars(r)["token"]

	// Load, find, validate - all under read lock first
	shares, err := loadFileShares()
	if err != nil {
		http.Error(w, "Internal error", http.StatusInternalServerError)
		return
	}
	idx := -1
	for i := range shares {
		if shares[i].Token == token {
			idx = i
			break
		}
	}
	if idx == -1 {
		http.Error(w, "Link not found", http.StatusNotFound)
		return
	}
	s := shares[idx]

	if msg := shareValid(s); msg != "" {
		http.Error(w, msg, http.StatusGone)
		return
	}

	// Password check
	if s.HasPassword {
		pw := r.URL.Query().Get("pw")
		if pw == "" {
			http.Error(w, "Password required", http.StatusUnauthorized)
			return
		}
		if err := bcrypt.CompareHashAndPassword([]byte(s.PasswordHash), []byte(pw)); err != nil {
			http.Error(w, "Incorrect password", http.StatusUnauthorized)
			return
		}
	}

	// Open file before incrementing counter so a missing file is caught early
	f, err := os.Open(s.Path)
	if err != nil {
		log.Printf("file_share download: open %s: %v", s.Path, err)
		http.Error(w, "File not found", http.StatusNotFound)
		return
	}
	defer f.Close()

	fInfo, err := f.Stat()
	if err != nil {
		http.Error(w, "Internal error", http.StatusInternalServerError)
		return
	}

	// Increment download counter
	shares[idx].DownloadCount++
	if err := saveFileShares(shares); err != nil {
		log.Printf("file_share: failed to increment download count: %v", err)
	}

	// Content-Type based on extension, fallback to binary
	ct := mime.TypeByExtension(filepath.Ext(s.Filename))
	if ct == "" {
		ct = "application/octet-stream"
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename=%q`, s.Filename))

	http.ServeContent(w, r, s.Filename, fInfo.ModTime(), f)
}
