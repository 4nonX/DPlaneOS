package handlers

import (
	"context"
	"database/sql"
	"path/filepath"
	"time"

	"dplaned/internal/cmdutil"
	"dplaned/internal/gitops"
	"dplaned/internal/smbconf"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
)

// ShareCRUDHandler handles SMB share CRUD operations
type ShareCRUDHandler struct {
	db          *sql.DB
	smbConfPath string
}

func NewShareCRUDHandler(db *sql.DB, smbConfPath string) *ShareCRUDHandler {
	return &ShareCRUDHandler{db: db, smbConfPath: smbConfPath}
}

func (h *ShareCRUDHandler) HandleShares(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		h.listShares(w, r)
	case http.MethodPost:
		h.shareAction(w, r)
	case http.MethodPut:
		h.handlePutShare(w, r)
	case http.MethodDelete:
		h.deleteShareByName(w, r)
	default:
		respondErrorSimple(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func (h *ShareCRUDHandler) listShares(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	// Check for ?id= query param
	idParam := r.URL.Query().Get("id")
	if idParam != "" {
		h.getShare(w, idParam)
		return
	}

	rows, err := h.db.QueryContext(ctx, `SELECT `+smbconf.ShareColumns+`, id, enabled, created_at FROM smb_shares ORDER BY name`)
	if err != nil {
		respondErrorSimple(w, "Failed to list shares", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	var shares []map[string]any
	for rows.Next() {
		var id, enabled int
		var createdAt string
		sh, err := smbconf.Scan(extraScanner{rows, []any{&id, &enabled, &createdAt}})
		if err != nil {
			log.Printf("WARN: smb_shares list scan: %v", err)
			continue
		}
		shares = append(shares, shareJSON(id, sh, enabled == 1, createdAt))
	}

	if err := rows.Err(); err != nil {
		log.Printf("WARN: smb_shares list rows: %v", err)
	}
	if shares == nil {
		shares = []map[string]any{}
	}

	respondJSON(w, http.StatusOK, map[string]any{
		"success": true,
		"shares":  shares,
	})
}

func (h *ShareCRUDHandler) getShare(w http.ResponseWriter, id string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var shareID, enabled int
	var createdAt string
	sh, err := smbconf.Scan(extraScanner{
		h.db.QueryRowContext(ctx, `SELECT `+smbconf.ShareColumns+`, id, enabled, created_at FROM smb_shares WHERE id = $1`, id),
		[]any{&shareID, &enabled, &createdAt},
	})
	if err != nil {
		respondErrorSimple(w, "Share not found", http.StatusNotFound)
		return
	}

	respondJSON(w, http.StatusOK, map[string]any{
		"success": true,
		"share":   shareJSON(shareID, sh, enabled == 1, createdAt),
	})
}

// extraScanner appends destinations for columns selected after smbconf.ShareColumns.
type extraScanner struct {
	row   interface{ Scan(...any) error }
	extra []any
}

func (e extraScanner) Scan(dest ...any) error { return e.row.Scan(append(dest, e.extra...)...) }

func shareJSON(id int, s smbconf.Share, enabled bool, createdAt string) map[string]any {
	return map[string]any{
		"id":                 id,
		"name":               s.Name,
		"path":               s.Path,
		"comment":            s.Comment,
		"browsable":          s.Browsable,
		"read_only":          s.ReadOnly,
		"guest_ok":           s.GuestOK,
		"valid_users":        s.ValidUsers,
		"write_list":         s.WriteList,
		"create_mask":        s.CreateMask,
		"directory_mask":     s.DirectoryMask,
		"time_machine":       s.TimeMachine,
		"time_machine_quota": s.TimeMachineQuota,
		"shadow_copy":        s.ShadowCopy,
		"recycle_bin":        s.RecycleBin,
		"hosts_allow":        s.HostsAllow,
		"hosts_deny":         s.HostsDeny,
		"enabled":            enabled,
		"created_at":         createdAt,
	}
}

type shareActionRequest struct {
	Action        string `json:"action"` // create, update, delete
	ID            int    `json:"id"`
	Name          string `json:"name"`
	Path          string `json:"path"`
	Comment       string `json:"comment"`
	Browsable     *bool  `json:"browsable"`
	ReadOnly      *bool  `json:"read_only"`
	GuestOk       *bool  `json:"guest_ok"`
	ValidUsers    string `json:"valid_users"`
	WriteList     string `json:"write_list"`
	CreateMask    string `json:"create_mask"`
	DirectoryMask string `json:"directory_mask"`
	Enabled       *bool  `json:"enabled"`

	// Per-share options (pointers: nil = leave unchanged on update)
	TimeMachine      *bool   `json:"time_machine"`
	TimeMachineQuota *string `json:"time_machine_quota"`
	ShadowCopy       *bool   `json:"shadow_copy"`
	RecycleBin       *bool   `json:"recycle_bin"`
	HostsAllow       *string `json:"hosts_allow"`
	HostsDeny        *string `json:"hosts_deny"`
}

// validateShareOptions checks the per-share option values in a request.
func validateShareOptions(req shareActionRequest) error {
	if req.TimeMachineQuota != nil {
		if err := smbconf.ValidateTimeMachineQuota(strings.TrimSpace(*req.TimeMachineQuota)); err != nil {
			return err
		}
	}
	for _, list := range []*string{req.HostsAllow, req.HostsDeny} {
		if list != nil {
			if err := smbconf.ValidateHostList(*list); err != nil {
				return err
			}
		}
	}
	return nil
}

func boolInt(p *bool) int {
	if p != nil && *p {
		return 1
	}
	return 0
}

func strVal(p *string) string {
	if p == nil {
		return ""
	}
	return strings.TrimSpace(*p)
}

func (h *ShareCRUDHandler) shareAction(w http.ResponseWriter, r *http.Request) {
	var req shareActionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondErrorSimple(w, "Invalid request", http.StatusBadRequest)
		return
	}

	switch req.Action {
	case "create":
		h.createShare(w, req)
	case "update":
		h.updateShare(w, req)
	case "delete":
		h.deleteShare(w, req)
	default:
		respondErrorSimple(w, "Unknown action: "+req.Action, http.StatusBadRequest)
	}
}

func (h *ShareCRUDHandler) createShare(w http.ResponseWriter, req shareActionRequest) {
	if err := checkBinary("smbcontrol"); err != nil {
		log.Printf("WARN: Samba (smbcontrol) not found, continuing without reload")
	}
	if req.Name == "" || req.Path == "" {
		respondErrorSimple(w, "Share name and path are required", http.StatusBadRequest)
		return
	}

	// Validate name (alphanumeric, dashes, underscores)
	if !isAlphanumericDash(req.Name) {
		respondErrorSimple(w, "Invalid share name (use alphanumeric, dash, underscore)", http.StatusBadRequest)
		return
	}

	// Validate path (must be absolute)
	if !strings.HasPrefix(req.Path, "/") {
		respondErrorSimple(w, "Path must be absolute", http.StatusBadRequest)
		return
	}

	if err := validateShareOptions(req); err != nil {
		respondErrorSimple(w, err.Error(), http.StatusBadRequest)
		return
	}

	// Sanitize inputs before DB insertion (Finding #30)
	req.Name = sanitizeSMBConfValue(req.Name)
	req.Path = filepath.Clean(req.Path)
	req.Comment = sanitizeSMBConfValue(req.Comment)
	req.ValidUsers = sanitizeSMBConfValue(req.ValidUsers)
	req.WriteList = sanitizeSMBConfValue(req.WriteList)

	browsable := 1
	if req.Browsable != nil && !*req.Browsable {
		browsable = 0
	}
	readOnly := 0
	if req.ReadOnly != nil && *req.ReadOnly {
		readOnly = 1
	}
	guestOk := 0
	if req.GuestOk != nil && *req.GuestOk {
		guestOk = 1
	}

	createMask := "0664"
	if req.CreateMask != "" {
		createMask = req.CreateMask
	}
	dirMask := "0775"
	if req.DirectoryMask != "" {
		dirMask = req.DirectoryMask
	}

	var id int64
	err := h.db.QueryRow(
		`INSERT INTO smb_shares (name, path, comment, browsable, read_only, guest_ok, valid_users, write_list, create_mask, directory_mask,
		                         time_machine, time_machine_quota, shadow_copy, recycle_bin, hosts_allow, hosts_deny)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16) RETURNING id`,
		req.Name, req.Path, req.Comment, browsable, readOnly, guestOk, req.ValidUsers, req.WriteList, createMask, dirMask,
		boolInt(req.TimeMachine), strVal(req.TimeMachineQuota), boolInt(req.ShadowCopy), boolInt(req.RecycleBin),
		strVal(req.HostsAllow), strVal(req.HostsDeny),
	).Scan(&id)

	if err != nil {
		respondErrorSimple(w, "Failed to create share (name may already exist)", http.StatusConflict)
		return
	}

	// Regenerate smb.conf
	h.respondAfterRegen(w, map[string]any{
		"success": true,
		"id":      id,
		"message": fmt.Sprintf("Share %s created", req.Name),
	})

	// GITOPS HOOK: write state back to git
	gitops.CommitAllAsync(h.db)
}

func (h *ShareCRUDHandler) updateShare(w http.ResponseWriter, req shareActionRequest) {
	if req.ID == 0 {
		respondErrorSimple(w, "Share ID required", http.StatusBadRequest)
		return
	}
	if err := validateShareOptions(req); err != nil {
		respondErrorSimple(w, err.Error(), http.StatusBadRequest)
		return
	}

	tx, err := h.db.Begin()
	if err != nil {
		respondErrorSimple(w, "Transaction failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	defer tx.Rollback()

	if req.Name != "" {
		if _, err := tx.Exec(`UPDATE smb_shares SET name = $1, updated_at = NOW() WHERE id = $2`, req.Name, req.ID); err != nil {
			respondErrorSimple(w, "Failed to update name: "+err.Error(), http.StatusInternalServerError)
			return
		}
	}
	if req.Path != "" {
		if _, err := tx.Exec(`UPDATE smb_shares SET path = $1, updated_at = NOW() WHERE id = $2`, req.Path, req.ID); err != nil {
			respondErrorSimple(w, "Failed to update path: "+err.Error(), http.StatusInternalServerError)
			return
		}
	}
	if req.Comment != "" {
		if _, err := tx.Exec(`UPDATE smb_shares SET comment = $1, updated_at = NOW() WHERE id = $2`, req.Comment, req.ID); err != nil {
			respondErrorSimple(w, "Failed to update comment: "+err.Error(), http.StatusInternalServerError)
			return
		}
	}
	if req.Browsable != nil {
		v := 0
		if *req.Browsable {
			v = 1
		}
		if _, err := tx.Exec(`UPDATE smb_shares SET browsable = $1, updated_at = NOW() WHERE id = $2`, v, req.ID); err != nil {
			respondErrorSimple(w, "Failed to update browsable: "+err.Error(), http.StatusInternalServerError)
			return
		}
	}
	if req.ReadOnly != nil {
		v := 0
		if *req.ReadOnly {
			v = 1
		}
		if _, err := tx.Exec(`UPDATE smb_shares SET read_only = $1, updated_at = NOW() WHERE id = $2`, v, req.ID); err != nil {
			respondErrorSimple(w, "Failed to update read_only: "+err.Error(), http.StatusInternalServerError)
			return
		}
	}
	if req.GuestOk != nil {
		v := 0
		if *req.GuestOk {
			v = 1
		}
		if _, err := tx.Exec(`UPDATE smb_shares SET guest_ok = $1, updated_at = NOW() WHERE id = $2`, v, req.ID); err != nil {
			respondErrorSimple(w, "Failed to update guest_ok: "+err.Error(), http.StatusInternalServerError)
			return
		}
	}
	if req.ValidUsers != "" {
		sanitized := sanitizeSMBConfValue(req.ValidUsers)
		if _, err := tx.Exec(`UPDATE smb_shares SET valid_users = $1, updated_at = NOW() WHERE id = $2`, sanitized, req.ID); err != nil {
			respondErrorSimple(w, "Failed to update valid_users: "+err.Error(), http.StatusInternalServerError)
			return
		}
	}
	if req.WriteList != "" {
		sanitized := sanitizeSMBConfValue(req.WriteList)
		if _, err := tx.Exec(`UPDATE smb_shares SET write_list = $1, updated_at = NOW() WHERE id = $2`, sanitized, req.ID); err != nil {
			respondErrorSimple(w, "Failed to update write_list: "+err.Error(), http.StatusInternalServerError)
			return
		}
	}
	if req.Enabled != nil {
		v := 0
		if *req.Enabled {
			v = 1
		}
		if _, err := tx.Exec(`UPDATE smb_shares SET enabled = $1, updated_at = NOW() WHERE id = $2`, v, req.ID); err != nil {
			respondErrorSimple(w, "Failed to update enabled: "+err.Error(), http.StatusInternalServerError)
			return
		}
	}

	for _, f := range []struct {
		column string
		set    bool
		value  any
	}{
		{"time_machine", req.TimeMachine != nil, boolInt(req.TimeMachine)},
		{"time_machine_quota", req.TimeMachineQuota != nil, strVal(req.TimeMachineQuota)},
		{"shadow_copy", req.ShadowCopy != nil, boolInt(req.ShadowCopy)},
		{"recycle_bin", req.RecycleBin != nil, boolInt(req.RecycleBin)},
		{"hosts_allow", req.HostsAllow != nil, strVal(req.HostsAllow)},
		{"hosts_deny", req.HostsDeny != nil, strVal(req.HostsDeny)},
	} {
		if !f.set {
			continue
		}
		// Column names come from the fixed list above, never from the request.
		if _, err := tx.Exec(`UPDATE smb_shares SET `+f.column+` = $1, updated_at = NOW() WHERE id = $2`, f.value, req.ID); err != nil {
			respondErrorSimple(w, "Failed to update "+f.column+": "+err.Error(), http.StatusInternalServerError)
			return
		}
	}

	if err := tx.Commit(); err != nil {
		respondErrorSimple(w, "Commit failed: "+err.Error(), http.StatusInternalServerError)
		return
	}

	h.respondAfterRegen(w, map[string]any{
		"success": true,
		"message": "Share updated",
	})

	// GITOPS HOOK: write state back to git
	gitops.CommitAllAsync(h.db)
}

func (h *ShareCRUDHandler) handlePutShare(w http.ResponseWriter, r *http.Request) {
	var req shareActionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondErrorSimple(w, "Invalid request", http.StatusBadRequest)
		return
	}
	// PUT implies update
	h.updateShare(w, req)
}

func (h *ShareCRUDHandler) deleteShare(w http.ResponseWriter, req shareActionRequest) {
	if req.ID == 0 {
		respondErrorSimple(w, "Share ID required", http.StatusBadRequest)
		return
	}

	if _, err := h.db.Exec(`DELETE FROM smb_shares WHERE id = $1`, req.ID); err != nil {
		respondErrorSimple(w, "Failed to delete share: "+err.Error(), http.StatusInternalServerError)
		return
	}
	h.respondAfterRegen(w, map[string]any{
		"success": true,
		"message": "Share deleted",
	})

	// GITOPS HOOK: write state back to git
	gitops.CommitAllAsync(h.db)
}

// deleteShareByName handles DELETE /api/shares with a JSON body { "name": "sharename" }.
// This is how the frontend deletes shares (it knows the name, not the DB id).
func (h *ShareCRUDHandler) deleteShareByName(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondErrorSimple(w, "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.Name == "" {
		respondErrorSimple(w, "name is required", http.StatusBadRequest)
		return
	}

	result, err := h.db.Exec(`DELETE FROM smb_shares WHERE name = $1`, req.Name)
	if err != nil {
		respondErrorSimple(w, "Database error: "+err.Error(), http.StatusInternalServerError)
		return
	}
	rows, _ := result.RowsAffected()
	if rows == 0 {
		respondErrorSimple(w, "Share not found: "+req.Name, http.StatusNotFound)
		return
	}

	h.respondAfterRegen(w, map[string]any{
		"success": true,
		"message": "Share deleted",
	})

	// GITOPS HOOK: write state back to git
	gitops.CommitAllAsync(h.db)
}

// GetSharesByPath aggregates SMB and NFS shares for a specific filesystem path
// GET /api/shares/by-path?path=/tank/data
func (h *ShareCRUDHandler) GetSharesByPath(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Query().Get("path")
	if path == "" {
		respondErrorSimple(w, "path is required", http.StatusBadRequest)
		return
	}

	// 1. Get SMB shares
	smbRows, err := h.db.Query(`SELECT name, comment, enabled FROM smb_shares WHERE path = $1`, path)
	if err != nil {
		respondErrorSimple(w, "SMB query failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	defer smbRows.Close()
	var smbShares []map[string]any
	for smbRows.Next() {
		var name, comment string
		var enabled int
		if err := smbRows.Scan(&name, &comment, &enabled); err != nil {
			log.Printf("WARN: smb_shares by-path scan: %v", err)
			continue
		}
		smbShares = append(smbShares, map[string]any{
			"name":    name,
			"comment": comment,
			"enabled": enabled == 1,
		})
	}
	if smbShares == nil {
		smbShares = []map[string]any{}
	}

	// 2. Get NFS exports
	nfsRows, err := h.db.Query(`SELECT id, clients, options, enabled FROM nfs_exports WHERE path = $1`, path)
	if err != nil {
		log.Printf("NFS query failed (table might be missing): %v", err)
		nfsRows = nil
	}

	var nfsExports []map[string]any
	if nfsRows != nil {
		defer nfsRows.Close()
		for nfsRows.Next() {
			var id, enabled int
			var clients, options string
			if err := nfsRows.Scan(&id, &clients, &options, &enabled); err != nil {
				log.Printf("WARN: nfs_exports by-path scan: %v", err)
				continue
			}
			nfsExports = append(nfsExports, map[string]any{
				"id":      id,
				"clients": clients,
				"options": options,
				"enabled": enabled == 1,
			})
		}
	}
	if nfsExports == nil {
		nfsExports = []map[string]any{}
	}

	respondOK(w, map[string]any{
		"success": true,
		"path":    path,
		"smb":     smbShares,
		"nfs":     nfsExports,
	})
}

// RegenerateSMBConf rewrites the share configuration from the database.
// Called once at daemon startup. Outside NixOS the target is the host's own
// smb.conf, so it is only rewritten when DPlaneOS manages shares there.
func (h *ShareCRUDHandler) RegenerateSMBConf() {
	if !smbconf.IsNixOS() {
		var n int
		if err := h.db.QueryRow(`SELECT COUNT(*) FROM smb_shares`).Scan(&n); err != nil || n == 0 {
			return
		}
	}
	h.regenerateSMBConf()
}

// regenerateSMBConf rebuilds the Samba share configuration from the database
// (smbconf.Render), reloads smbd and refreshes the Time Machine advertisement.
//
// err means Samba was not updated; warning means the shares are live but the
// Time Machine Bonjour advertisement could not be refreshed.
func (h *ShareCRUDHandler) regenerateSMBConf() (warning string, err error) {
	shares, err := smbconf.LoadEnabled(h.db)
	if err != nil {
		log.Printf("SMB REGEN ERROR: %v", err)
		return "", fmt.Errorf("load shares: %w", err)
	}
	opts := smbconf.LoadOptions(h.db)

	if err := smbconf.WriteAtomic(h.smbConfPath, []byte(smbconf.Render(opts, shares))); err != nil {
		log.Printf("SMB WRITE ERROR: %v", err)
		return "", fmt.Errorf("write %s: %w", h.smbConfPath, err)
	}

	// Reload samba
	if out, err := cmdutil.RunFast("smbcontrol", "all", "reload-config"); err != nil {
		log.Printf("WARN: smbcontrol reload: %v", err)
		return "", fmt.Errorf("smbcontrol reload-config: %v %s", err, strings.TrimSpace(string(out)))
	}

	if err := smbconf.SyncAvahi(smbconf.TimeMachineShares(shares)); err != nil {
		log.Printf("WARN: %v", err)
		warning = "Time Machine discovery not updated: " + err.Error()
	}

	// On NixOS: also update dplane-generated.nix with global SMB settings
	// so they survive the next nixos-rebuild switch.
	persistSambaGlobals(h.db)

	log.Printf("SMB config regenerated and reloaded (%d shares, apple=%v, nixos=%v)",
		len(shares), smbconf.AppleEnabled(opts, shares), opts.NixOSManaged)
	return warning, nil
}

// respondAfterRegen applies the share change to Samba and answers with
// payload, or with the failure: the database change is saved either way.
func (h *ShareCRUDHandler) respondAfterRegen(w http.ResponseWriter, payload map[string]any) {
	warning, err := h.regenerateSMBConf()
	if err != nil {
		respondJSON(w, http.StatusOK, map[string]any{"success": false, "error": "Saved, but Samba was not updated: " + err.Error()})
		return
	}
	if warning != "" {
		payload["warning"] = warning
	}
	respondJSON(w, http.StatusOK, payload)
}

// GetSMBSettings returns current global SMB protocol settings
// GET /api/smb/settings
func (h *ShareCRUDHandler) GetSMBSettings(w http.ResponseWriter, r *http.Request) {
	var timeMachine, shadowCopy, recycleBin int
	h.db.QueryRow(`SELECT COALESCE(value,'0') FROM settings WHERE key='smb_time_machine'`).Scan(&timeMachine)
	h.db.QueryRow(`SELECT COALESCE(value,'0') FROM settings WHERE key='smb_shadow_copy'`).Scan(&shadowCopy)
	h.db.QueryRow(`SELECT COALESCE(value,'0') FROM settings WHERE key='smb_recycle_bin'`).Scan(&recycleBin)
	_, avahiErr := os.Stat(smbconf.AvahiPath())
	respondOK(w, map[string]any{
		"success":        true,
		"time_machine":   timeMachine == 1,
		"shadow_copy":    shadowCopy == 1,
		"recycle_bin":    recycleBin == 1,
		"avahi_file_ok":  avahiErr == nil,
	})
}

// UpdateSMBSettings updates global SMB protocol settings and regenerates smb.conf
// POST /api/smb/settings
func (h *ShareCRUDHandler) UpdateSMBSettings(w http.ResponseWriter, r *http.Request) {
	var req struct {
		TimeMachine *bool `json:"time_machine"`
		ShadowCopy  *bool `json:"shadow_copy"`
		RecycleBin  *bool `json:"recycle_bin"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondErrorSimple(w, "Invalid request", http.StatusBadRequest)
		return
	}

	setSetting := func(key string, val *bool) error {
		if val == nil {
			return nil
		}
		v := "0"
		if *val {
			v = "1"
		}
		if _, err := h.db.Exec(`INSERT INTO settings (key, value) VALUES ($1, $2) ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_at = NOW()`, key, v); err != nil {
			return fmt.Errorf("save %s: %w", key, err)
		}
		return nil
	}

	// time_machine is the global "macOS support" toggle: it loads the Apple
	// SMB extensions on every share. Which shares are Time Machine targets is
	// a per-share option.
	if err := setSetting("smb_time_machine", req.TimeMachine); err != nil {
		respondErrorSimple(w, "Failed to save settings: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Shadow copies and the recycle bin are per-share options. The global
	// fields remain for API compatibility and apply the value to all shares.
	for _, bulk := range []struct {
		key, column string
		val         *bool
	}{
		{"smb_shadow_copy", "shadow_copy", req.ShadowCopy},
		{"smb_recycle_bin", "recycle_bin", req.RecycleBin},
	} {
		if bulk.val == nil {
			continue
		}
		if err := setSetting(bulk.key, bulk.val); err != nil {
			respondErrorSimple(w, "Failed to save settings: "+err.Error(), http.StatusInternalServerError)
			return
		}
		// Column names come from the fixed list above.
		if _, err := h.db.Exec(`UPDATE smb_shares SET `+bulk.column+` = $1, updated_at = NOW()`, boolInt(bulk.val)); err != nil {
			respondErrorSimple(w, "Failed to update shares: "+err.Error(), http.StatusInternalServerError)
			return
		}
	}

	h.respondAfterRegen(w, map[string]any{"success": true})
}

// sanitizeSMBConfValue removes newlines and other characters that could break smb.conf formatting
func sanitizeSMBConfValue(val string) string { return smbconf.Sanitize(val) }

// SMBSessionInfo holds per-session data from smbstatus.
type SMBSessionInfo struct {
	ID          string   `json:"id"`
	User        string   `json:"user"`
	IP          string   `json:"ip"`
	Shares      []string `json:"shares"`
	OpenFiles   int      `json:"open_files"`
	ConnectedAt string   `json:"connected_at,omitempty"`
}

// ListSMBSessions returns active SMB client sessions via smbstatus.
// GET /api/shares/smb/sessions
func (h *ShareCRUDHandler) ListSMBSessions(w http.ResponseWriter, r *http.Request) {
	// -p: process list (PID, user, machine/IP)
	procOut, err := cmdutil.RunFast("smbstatus", "-p", "-n")
	if err != nil {
		// smbstatus unavailable or Samba not running - return empty list
		respondOK(w, map[string]any{"success": true, "sessions": []SMBSessionInfo{}})
		return
	}

	// Parse process output: skip header lines, each data line has
	// PID  Username  Group  Machine  ...
	sessions := map[string]*SMBSessionInfo{}
	inData := false
	for _, line := range strings.Split(string(procOut), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "---") {
			inData = true
			continue
		}
		if !inData || line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 4 {
			continue
		}
		pid := fields[0]
		user := fields[1]
		// Machine field may be "192.168.1.10 (ipv4:...)" - take first token
		ip := strings.TrimSuffix(fields[3], ",")
		if idx := strings.Index(ip, "("); idx > 0 {
			ip = strings.TrimSpace(ip[:idx])
		}
		sessions[pid] = &SMBSessionInfo{
			ID:     pid,
			User:   user,
			IP:     ip,
			Shares: []string{},
		}
	}

	// -S: share list (service, PID, machine, connected_at, ...)
	shareOut, err := cmdutil.RunFast("smbstatus", "-S", "-n")
	if err == nil {
		inData = false
		for _, line := range strings.Split(string(shareOut), "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "---") {
				inData = true
				continue
			}
			if !inData || line == "" {
				continue
			}
			fields := strings.Fields(line)
			if len(fields) < 2 {
				continue
			}
			shareName := fields[0]
			pid := fields[1]
			if s, ok := sessions[pid]; ok {
				// Avoid duplicate share entries
				found := false
				for _, existing := range s.Shares {
					if existing == shareName {
						found = true
						break
					}
				}
				if !found {
					s.Shares = append(s.Shares, shareName)
				}
			}
		}
	}

	result := make([]SMBSessionInfo, 0, len(sessions))
	for _, s := range sessions {
		result = append(result, *s)
	}
	respondOK(w, map[string]any{"success": true, "sessions": result})
}

// DisconnectSMBSession terminates an SMB session by PID.
// POST /api/shares/smb/sessions/disconnect
func (h *ShareCRUDHandler) DisconnectSMBSession(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.ID == "" {
		respondErrorSimple(w, "id is required", http.StatusBadRequest)
		return
	}
	// Validate: PID must be numeric only
	for _, c := range req.ID {
		if c < '0' || c > '9' {
			respondErrorSimple(w, "invalid session id", http.StatusBadRequest)
			return
		}
	}
	if _, err := cmdutil.RunFast("smbcontrol", req.ID, "shutdown"); err != nil {
		respondErrorSimple(w, "failed to disconnect session: "+err.Error(), http.StatusInternalServerError)
		return
	}
	respondOK(w, map[string]any{"success": true})
}
