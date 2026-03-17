package handlers

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"

	"dplaned/internal/audit"
	"dplaned/internal/cmdutil"
	"dplaned/internal/gitops"
)

// ═══════════════════════════════════════════════════════════════
//  NFS Export Management
//  Writes /etc/exports and calls exportfs -ra on every change.
//  Requires nfs-kernel-server: apt install nfs-kernel-server
// ═══════════════════════════════════════════════════════════════

const nfsExportsPath = "/etc/exports"
const nfsDplaneosMark = "# D-PlaneOS NFS exports - managed automatically, do not edit by hand"

// NFSExport represents a single exported path
type NFSExport struct {
	ID         int    `json:"id"`
	Path       string `json:"path"`
	Clients    string `json:"clients"`    // e.g. "192.168.1.0/24" or "*"
	Options    string `json:"options"`    // e.g. "rw,sync,no_subtree_check"
	Enabled    bool   `json:"enabled"`
	CreatedAt  string `json:"created_at"`
}

// NFSHandler handles NFS export CRUD
type NFSHandler struct {
	db *sql.DB
}

func NewNFSHandler(db *sql.DB) *NFSHandler {
	h := &NFSHandler{db: db}
	h.initTable()
	return h
}

func (h *NFSHandler) initTable() {
	h.db.Exec(`CREATE TABLE IF NOT EXISTS nfs_exports (
		id        INTEGER PRIMARY KEY AUTOINCREMENT,
		path      TEXT NOT NULL,
		clients   TEXT NOT NULL DEFAULT '*',
		options   TEXT NOT NULL DEFAULT 'rw,sync,no_subtree_check,no_root_squash',
		enabled   INTEGER NOT NULL DEFAULT 1,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
	)`)
}

// nfsInstalled returns true if exportfs is on PATH
func nfsInstalled() bool {
	_, err := cmdutil.RunFast("which", "exportfs")
	return err == nil
}

// validateNFSPath checks the export path is absolute and safe
var unsafePath = regexp.MustCompile(`\.\.`)

func validateNFSPath(path string) error {
	if !strings.HasPrefix(path, "/") {
		return fmt.Errorf("path must be absolute (start with /)")
	}
	if unsafePath.MatchString(path) {
		return fmt.Errorf("path must not contain ..")
	}
	if _, err := os.Stat(path); err != nil {
		return fmt.Errorf("path does not exist: %s", path)
	}
	return nil
}

// validateNFSClients checks the clients field
// Allows: *, 192.168.1.0/24, 10.0.0.1, hostname.local, *.example.com
var validClient = regexp.MustCompile(`^[\w.*/:@-]+$`)

func validateNFSClients(clients string) error {
	for _, c := range strings.Fields(clients) {
		if !validClient.MatchString(c) {
			return fmt.Errorf("invalid client spec: %q", c)
		}
	}
	return nil
}

// validateNFSOptions checks the options string for injection
var validOption = regexp.MustCompile(`^[a-zA-Z0-9_=,@.-]+$`)

func validateNFSOptions(opts string) error {
	for _, o := range strings.Split(opts, ",") {
		o = strings.TrimSpace(o)
		if o == "" {
			continue
		}
		if !validOption.MatchString(o) {
			return fmt.Errorf("invalid option: %q", o)
		}
	}
	return nil
}

// writeExportsFile regenerates /etc/exports from the database
func (h *NFSHandler) writeExportsFile() error {
	rows, err := h.db.Query(`SELECT path, clients, options FROM nfs_exports WHERE enabled = 1 ORDER BY id`)
	if err != nil {
		return fmt.Errorf("query nfs_exports: %w", err)
	}
	defer rows.Close()

	var sb strings.Builder
	sb.WriteString(nfsDplaneosMark + "\n")
	sb.WriteString("# Changes made here will be overwritten. Use the D-PlaneOS web UI.\n\n")

	for rows.Next() {
		var path, clients, options string
		if err := rows.Scan(&path, &clients, &options); err != nil {
			continue
		}
		// Format: /path client1(opts) client2(opts) ...
		for _, client := range strings.Fields(clients) {
			sb.WriteString(fmt.Sprintf("%s\t%s(%s)\n", path, client, options))
		}
	}

	if err := os.WriteFile(nfsExportsPath, []byte(sb.String()), 0644); err != nil {
		return fmt.Errorf("write %s: %w", nfsExportsPath, err)
	}
	return nil
}

// reloadExports calls exportfs -ra to apply the current exports file
func reloadExports(user string) error {
	_, err := cmdutil.RunFast("exportfs", "-ra")
	if err != nil {
		log.Printf("exportfs -ra failed: %v", err)
		audit.LogActivity(user, "nfs_reload", map[string]interface{}{"success": false, "error": err.Error()})
		return err
	}
	audit.LogActivity(user, "nfs_reload", map[string]interface{}{"success": true})
	return nil
}

// ─── HTTP Handlers ────────────────────────────────────────────

// GetNFSStatus GET /api/nfs/status
func (h *NFSHandler) GetNFSStatus(w http.ResponseWriter, r *http.Request) {
	if !nfsInstalled() {
		respondOK(w, map[string]interface{}{
			"success":   true,
			"installed": false,
			"message":   "NFS server not installed. Run: sudo apt install nfs-kernel-server",
		})
		return
	}

	out, err := cmdutil.RunFast("systemctl", "is-active", "nfs-kernel-server")
	active := err == nil && strings.TrimSpace(string(out)) == "active"

	var count int
	h.db.QueryRow(`SELECT COUNT(*) FROM nfs_exports WHERE enabled = 1`).Scan(&count)

	respondOK(w, map[string]interface{}{
		"success":      true,
		"installed":    true,
		"active":       active,
		"export_count": count,
	})
}

// ListNFSExports GET /api/nfs/exports
func (h *NFSHandler) ListNFSExports(w http.ResponseWriter, r *http.Request) {
	if !nfsInstalled() {
		respondOK(w, map[string]interface{}{
			"success": true,
			"exports": []interface{}{},
			"note":    "NFS server not installed. Run: sudo apt install nfs-kernel-server",
		})
		return
	}

	rows, err := h.db.Query(`SELECT id, path, clients, options, enabled, created_at FROM nfs_exports ORDER BY id`)
	if err != nil {
		respondErrorSimple(w, "database error", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	var exports []NFSExport
	for rows.Next() {
		var e NFSExport
		var enabled int
		rows.Scan(&e.ID, &e.Path, &e.Clients, &e.Options, &enabled, &e.CreatedAt)
		e.Enabled = enabled == 1
		exports = append(exports, e)
	}
	if exports == nil {
		exports = []NFSExport{}
	}

	respondOK(w, map[string]interface{}{
		"success": true,
		"exports": exports,
	})
}

// CreateNFSExport POST /api/nfs/exports
func (h *NFSHandler) CreateNFSExport(w http.ResponseWriter, r *http.Request) {
	if !nfsInstalled() {
		respondErrorSimple(w, "NFS server not installed. Run: sudo apt install nfs-kernel-server", http.StatusServiceUnavailable)
		return
	}

	var req struct {
		Path    string `json:"path"`
		Clients string `json:"clients"`
		Options string `json:"options"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondErrorSimple(w, "invalid JSON", http.StatusBadRequest)
		return
	}

	// Defaults
	if req.Clients == "" {
		req.Clients = "*"
	}
	if req.Options == "" {
		req.Options = "rw,sync,no_subtree_check,no_root_squash"
	}

	// Validate
	if err := validateNFSPath(req.Path); err != nil {
		respondErrorSimple(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := validateNFSClients(req.Clients); err != nil {
		respondErrorSimple(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := validateNFSOptions(req.Options); err != nil {
		respondErrorSimple(w, err.Error(), http.StatusBadRequest)
		return
	}

	res, err := h.db.Exec(
		`INSERT INTO nfs_exports (path, clients, options) VALUES (?, ?, ?)`,
		req.Path, req.Clients, req.Options,
	)
	if err != nil {
		respondErrorSimple(w, "database error: "+err.Error(), http.StatusInternalServerError)
		return
	}
	id, _ := res.LastInsertId()

	if err := h.writeExportsFile(); err != nil {
		log.Printf("writeExportsFile: %v", err)
	}

	user := r.Header.Get("X-User")
	reloadExports(user)
	audit.LogActivity(user, "nfs_export_create", map[string]interface{}{"path": req.Path, "clients": req.Clients})

	respondOK(w, map[string]interface{}{
		"success": true,
		"id":      id,
		"message": "Export created and applied",
	})
	gitops.CommitAll(h.db)
}

// UpdateNFSExport POST /api/nfs/exports/{id}/update
func (h *NFSHandler) UpdateNFSExport(w http.ResponseWriter, r *http.Request) {
	if !nfsInstalled() {
		respondErrorSimple(w, "NFS server not installed", http.StatusServiceUnavailable)
		return
	}

	// Extract id from path
	parts := strings.Split(r.URL.Path, "/")
	idStr := ""
	for i, p := range parts {
		if p == "exports" && i+1 < len(parts) {
			idStr = parts[i+1]
			break
		}
	}
	id, err := strconv.Atoi(idStr)
	if err != nil || id <= 0 {
		respondErrorSimple(w, "invalid export id", http.StatusBadRequest)
		return
	}

	var req struct {
		Path    string `json:"path"`
		Clients string `json:"clients"`
		Options string `json:"options"`
		Enabled *bool  `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondErrorSimple(w, "invalid JSON", http.StatusBadRequest)
		return
	}

	if req.Path != "" {
		if err := validateNFSPath(req.Path); err != nil {
			respondErrorSimple(w, err.Error(), http.StatusBadRequest)
			return
		}
	}
	if req.Clients != "" {
		if err := validateNFSClients(req.Clients); err != nil {
			respondErrorSimple(w, err.Error(), http.StatusBadRequest)
			return
		}
	}
	if req.Options != "" {
		if err := validateNFSOptions(req.Options); err != nil {
			respondErrorSimple(w, err.Error(), http.StatusBadRequest)
			return
		}
	}

	// Build update dynamically
	sets := []string{"updated_at = CURRENT_TIMESTAMP"}
	args := []interface{}{}
	if req.Path != "" {
		sets = append(sets, "path = ?")
		args = append(args, req.Path)
	}
	if req.Clients != "" {
		sets = append(sets, "clients = ?")
		args = append(args, req.Clients)
	}
	if req.Options != "" {
		sets = append(sets, "options = ?")
		args = append(args, req.Options)
	}
	if req.Enabled != nil {
		v := 0
		if *req.Enabled {
			v = 1
		}
		sets = append(sets, "enabled = ?")
		args = append(args, v)
	}
	args = append(args, id)

	query := "UPDATE nfs_exports SET " + strings.Join(sets, ", ") + " WHERE id = ?"
	if _, err := h.db.Exec(query, args...); err != nil {
		respondErrorSimple(w, "database error: "+err.Error(), http.StatusInternalServerError)
		return
	}

	if err := h.writeExportsFile(); err != nil {
		log.Printf("writeExportsFile: %v", err)
	}
	user := r.Header.Get("X-User")
	reloadExports(user)
	audit.LogActivity(user, "nfs_export_update", map[string]interface{}{"id": id})

	respondOK(w, map[string]interface{}{"success": true, "message": "Export updated and applied"})
	gitops.CommitAll(h.db)
}

// DeleteNFSExport DELETE /api/nfs/exports/{id}
func (h *NFSHandler) DeleteNFSExport(w http.ResponseWriter, r *http.Request) {
	if !nfsInstalled() {
		respondErrorSimple(w, "NFS server not installed", http.StatusServiceUnavailable)
		return
	}

	parts := strings.Split(r.URL.Path, "/")
	idStr := parts[len(parts)-1]
	id, err := strconv.Atoi(idStr)
	if err != nil || id <= 0 {
		respondErrorSimple(w, "invalid export id", http.StatusBadRequest)
		return
	}

	// Fetch path for audit before deleting
	var path string
	h.db.QueryRow(`SELECT path FROM nfs_exports WHERE id = ?`, id).Scan(&path)

	if _, err := h.db.Exec(`DELETE FROM nfs_exports WHERE id = ?`, id); err != nil {
		respondErrorSimple(w, "database error: "+err.Error(), http.StatusInternalServerError)
		return
	}

	if err := h.writeExportsFile(); err != nil {
		log.Printf("writeExportsFile: %v", err)
	}
	user := r.Header.Get("X-User")
	reloadExports(user)
	audit.LogActivity(user, "nfs_export_delete", map[string]interface{}{"id": id, "path": path})

	respondOK(w, map[string]interface{}{"success": true, "message": "Export deleted and applied"})
	gitops.CommitAll(h.db)
}

// ReloadNFSExportsHandler POST /api/nfs/reload
func (h *NFSHandler) ReloadNFSExportsHandler(w http.ResponseWriter, r *http.Request) {
	if !nfsInstalled() {
		respondErrorSimple(w, "NFS server not installed. Run: sudo apt install nfs-kernel-server", http.StatusServiceUnavailable)
		return
	}
	user := r.Header.Get("X-User")
	if err := h.writeExportsFile(); err != nil {
		respondErrorSimple(w, "failed to write exports file: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if err := reloadExports(user); err != nil {
		respondErrorSimple(w, "exportfs -ra failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	respondOK(w, map[string]interface{}{"success": true, "message": "NFS exports reloaded"})
}

