package handlers

import (
	"context"
	"database/sql"
	"path/filepath"
	"time"

	"dplaned/internal/cmdutil"
	"dplaned/internal/gitops"
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

	rows, err := h.db.QueryContext(ctx, `SELECT id, name, path, comment, browsable, read_only, guest_ok, valid_users, write_list, create_mask, directory_mask, enabled, created_at FROM smb_shares ORDER BY name`)
	if err != nil {
		respondErrorSimple(w, "Failed to list shares", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	var shares []map[string]interface{}
	for rows.Next() {
		var id, browsable, readOnly, guestOk, enabled int
		var name, path, comment, validUsers, writeList, createMask, dirMask, createdAt string
		if err := rows.Scan(&id, &name, &path, &comment, &browsable, &readOnly, &guestOk, &validUsers, &writeList, &createMask, &dirMask, &enabled, &createdAt); err != nil {
			log.Printf("WARN: smb_shares list scan: %v", err)
			continue
		}
		shares = append(shares, map[string]interface{}{
			"id":             id,
			"name":           name,
			"path":           path,
			"comment":        comment,
			"browsable":      browsable == 1,
			"read_only":      readOnly == 1,
			"guest_ok":       guestOk == 1,
			"valid_users":    validUsers,
			"write_list":     writeList,
			"create_mask":    createMask,
			"directory_mask": dirMask,
			"enabled":        enabled == 1,
			"created_at":     createdAt,
		})
	}

	if shares == nil {
		shares = []map[string]interface{}{}
	}

	respondJSON(w, http.StatusOK, map[string]interface{}{
		"success": true,
		"shares":  shares,
	})
}

func (h *ShareCRUDHandler) getShare(w http.ResponseWriter, id string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var shareID, browsable, readOnly, guestOk, enabled int
	var name, path, comment, validUsers, writeList, createMask, dirMask, createdAt string

	err := h.db.QueryRowContext(ctx,
		`SELECT id, name, path, comment, browsable, read_only, guest_ok, valid_users, write_list, create_mask, directory_mask, enabled, created_at FROM smb_shares WHERE id = $1`, id,
	).Scan(&shareID, &name, &path, &comment, &browsable, &readOnly, &guestOk, &validUsers, &writeList, &createMask, &dirMask, &enabled, &createdAt)

	if err != nil {
		respondErrorSimple(w, "Share not found", http.StatusNotFound)
		return
	}

	respondJSON(w, http.StatusOK, map[string]interface{}{
		"success": true,
		"share": map[string]interface{}{
			"id":             shareID,
			"name":           name,
			"path":           path,
			"comment":        comment,
			"browsable":      browsable == 1,
			"read_only":      readOnly == 1,
			"guest_ok":       guestOk == 1,
			"valid_users":    validUsers,
			"write_list":     writeList,
			"create_mask":    createMask,
			"directory_mask": dirMask,
			"enabled":        enabled == 1,
			"created_at":     createdAt,
		},
	})
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
		`INSERT INTO smb_shares (name, path, comment, browsable, read_only, guest_ok, valid_users, write_list, create_mask, directory_mask)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10) RETURNING id`,
		req.Name, req.Path, req.Comment, browsable, readOnly, guestOk, req.ValidUsers, req.WriteList, createMask, dirMask,
	).Scan(&id)

	if err != nil {
		respondErrorSimple(w, "Failed to create share (name may already exist)", http.StatusConflict)
		return
	}

	// Regenerate smb.conf
	h.regenerateSMBConf()

	respondJSON(w, http.StatusOK, map[string]interface{}{
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

	if err := tx.Commit(); err != nil {
		respondErrorSimple(w, "Commit failed: "+err.Error(), http.StatusInternalServerError)
		return
	}

	h.regenerateSMBConf()

	respondJSON(w, http.StatusOK, map[string]interface{}{
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
	h.regenerateSMBConf()

	respondJSON(w, http.StatusOK, map[string]interface{}{
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

	h.regenerateSMBConf()
	respondJSON(w, http.StatusOK, map[string]interface{}{
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
	var smbShares []map[string]interface{}
	for smbRows.Next() {
		var name, comment string
		var enabled int
		if err := smbRows.Scan(&name, &comment, &enabled); err != nil {
			log.Printf("WARN: smb_shares by-path scan: %v", err)
			continue
		}
		smbShares = append(smbShares, map[string]interface{}{
			"name":    name,
			"comment": comment,
			"enabled": enabled == 1,
		})
	}
	if smbShares == nil {
		smbShares = []map[string]interface{}{}
	}

	// 2. Get NFS exports
	nfsRows, err := h.db.Query(`SELECT id, clients, options, enabled FROM nfs_exports WHERE path = $1`, path)
	if err != nil {
		log.Printf("NFS query failed (table might be missing): %v", err)
		nfsRows = nil
	}

	var nfsExports []map[string]interface{}
	if nfsRows != nil {
		defer nfsRows.Close()
		for nfsRows.Next() {
			var id, enabled int
			var clients, options string
			if err := nfsRows.Scan(&id, &clients, &options, &enabled); err != nil {
				log.Printf("WARN: nfs_exports by-path scan: %v", err)
				continue
			}
			nfsExports = append(nfsExports, map[string]interface{}{
				"id":      id,
				"clients": clients,
				"options": options,
				"enabled": enabled == 1,
			})
		}
	}
	if nfsExports == nil {
		nfsExports = []map[string]interface{}{}
	}

	respondOK(w, map[string]interface{}{
		"success": true,
		"path":    path,
		"smb":     smbShares,
		"nfs":     nfsExports,
	})
}

// regenerateSMBConf rebuilds /etc/samba/smb.conf from the database
func (h *ShareCRUDHandler) regenerateSMBConf() {
	rows, err := h.db.Query(`SELECT name, path, comment, browsable, read_only, guest_ok, valid_users, write_list, create_mask, directory_mask FROM smb_shares WHERE enabled = 1`)
	if err != nil {
		log.Printf("SMB REGEN ERROR: %v", err)
		return
	}
	defer rows.Close()

	// Load global VFS settings from DB
	var globalTimeMachine, globalShadowCopy, globalRecycleBin int
	var globalExtra string
	h.db.QueryRow(`SELECT COALESCE(value,'0') FROM settings WHERE key='smb_time_machine'`).Scan(&globalTimeMachine)
	h.db.QueryRow(`SELECT COALESCE(value,'0') FROM settings WHERE key='smb_shadow_copy'`).Scan(&globalShadowCopy)
	h.db.QueryRow(`SELECT COALESCE(value,'0') FROM settings WHERE key='smb_recycle_bin'`).Scan(&globalRecycleBin)
	h.db.QueryRow(`SELECT COALESCE(value,'') FROM settings WHERE key='smb_extra_global'`).Scan(&globalExtra)

	var conf strings.Builder
	conf.WriteString("[global]\n")
	conf.WriteString("   workgroup = WORKGROUP\n")
	conf.WriteString("   server string = DPlaneOS NAS\n")
	conf.WriteString("   security = user\n")
	conf.WriteString("   map to guest = Bad User\n")
	conf.WriteString("   log file = /var/log/samba/log.%m\n")
	conf.WriteString("   max log size = 1000\n")

	// Global VFS modules for macOS Time Machine support
	if globalTimeMachine == 1 {
		conf.WriteString("   # macOS Time Machine support\n")
		conf.WriteString("   fruit:metadata = stream\n")
		conf.WriteString("   fruit:model = MacSamba\n")
		conf.WriteString("   fruit:posix_rename = yes\n")
		conf.WriteString("   fruit:veto_appledouble = no\n")
		conf.WriteString("   fruit:nfs_aces = no\n")
		conf.WriteString("   fruit:wipe_intentionally_left_blank_rfork = yes\n")
		conf.WriteString("   fruit:delete_empty_adfiles = yes\n")
	}

	// Global extra parameters
	if globalExtra != "" {
		conf.WriteString("   # Custom global parameters\n")
		for _, line := range strings.Split(globalExtra, "\n") {
			trimmed := strings.TrimSpace(line)
			if trimmed != "" {
				// Block dangerous directives like 'include' or section headers '[...]'
				if strings.Contains(strings.ToLower(trimmed), "include") || strings.Contains(trimmed, "[") {
					log.Printf("SMB REGEN: blocked dangerous global parameter: %s", trimmed)
					continue
				}
				conf.WriteString("   " + trimmed + "\n")
			}
		}
	}
	conf.WriteString("\n")

	for rows.Next() {
		var name, path, comment, validUsers, writeList, createMask, dirMask string
		var browsable, readOnly, guestOk int
		rows.Scan(&name, &path, &comment, &browsable, &readOnly, &guestOk, &validUsers, &writeList, &createMask, &dirMask)

		// Sanitize values to prevent injection
		name = sanitizeSMBConfValue(name)
		path = sanitizeSMBConfValue(path)
		comment = sanitizeSMBConfValue(comment)
		validUsers = sanitizeSMBConfValue(validUsers)
		writeList = sanitizeSMBConfValue(writeList)

		conf.WriteString(fmt.Sprintf("[%s]\n", name))
		conf.WriteString(fmt.Sprintf("   path = %s\n", path))
		if comment != "" {
			conf.WriteString(fmt.Sprintf("   comment = %s\n", comment))
		}
		if browsable == 1 {
			conf.WriteString("   browsable = yes\n")
		} else {
			conf.WriteString("   browsable = no\n")
		}
		if readOnly == 1 {
			conf.WriteString("   read only = yes\n")
		} else {
			conf.WriteString("   read only = no\n")
		}
		if guestOk == 1 {
			conf.WriteString("   guest ok = yes\n")
		}
		if validUsers != "" {
			conf.WriteString(fmt.Sprintf("   valid users = %s\n", validUsers))
		}
		if writeList != "" {
			conf.WriteString(fmt.Sprintf("   write list = %s\n", writeList))
		}
		conf.WriteString(fmt.Sprintf("   create mask = %s\n", createMask))
		conf.WriteString(fmt.Sprintf("   directory mask = %s\n", dirMask))

		// Per-share VFS objects
		var vfsObjects []string
		if globalTimeMachine == 1 {
			vfsObjects = append(vfsObjects, "catia", "fruit", "streams_xattr")
		}
		if globalShadowCopy == 1 {
			vfsObjects = append(vfsObjects, "shadow_copy2")
			conf.WriteString("   shadow:snapdir = .zfs/snapshot\n")
			conf.WriteString("   shadow:sort = desc\n")
			// Strip frequency prefix (e.g. "auto-daily-", "auto-weekly-") then parse date
			conf.WriteString("   shadow:snapprefix = ^auto-[a-z]+-\n")
			conf.WriteString("   shadow:format = %Y%m%d-%H%M\n")
		}
		if globalRecycleBin == 1 {
			vfsObjects = append(vfsObjects, "recycle")
			conf.WriteString("   recycle:repository = .recycle/%U\n")
			conf.WriteString("   recycle:keeptree = yes\n")
			conf.WriteString("   recycle:versions = yes\n")
			conf.WriteString("   recycle:touch = yes\n")
			conf.WriteString("   recycle:directory_mode = 0770\n")
		}
		if len(vfsObjects) > 0 {
			conf.WriteString(fmt.Sprintf("   vfs objects = %s\n", strings.Join(vfsObjects, " ")))
		}
		if globalTimeMachine == 1 {
			conf.WriteString("   fruit:time machine = yes\n")
		}

		conf.WriteString("\n")
	}

	err = os.WriteFile(h.smbConfPath, []byte(conf.String()), 0644)
	if err != nil {
		log.Printf("SMB WRITE ERROR: %v", err)
		return
	}

	// Reload samba
	if _, err := cmdutil.RunFast("smbcontrol", "all", "reload-config"); err != nil {
		log.Printf("WARN: smbcontrol reload: %v", err)
	}

	// On NixOS: also update dplane-generated.nix with global SMB settings
	// so they survive the next nixos-rebuild switch.
	persistSambaGlobals(h.db)

	log.Printf("SMB config regenerated and reloaded (VFS: tm=%d sc=%d rb=%d)", globalTimeMachine, globalShadowCopy, globalRecycleBin)
}

// GetSMBSettings returns current global SMB protocol settings
// GET /api/smb/settings
func (h *ShareCRUDHandler) GetSMBSettings(w http.ResponseWriter, r *http.Request) {
	var timeMachine, shadowCopy, recycleBin int
	h.db.QueryRow(`SELECT COALESCE(value,'0') FROM settings WHERE key='smb_time_machine'`).Scan(&timeMachine)
	h.db.QueryRow(`SELECT COALESCE(value,'0') FROM settings WHERE key='smb_shadow_copy'`).Scan(&shadowCopy)
	h.db.QueryRow(`SELECT COALESCE(value,'0') FROM settings WHERE key='smb_recycle_bin'`).Scan(&recycleBin)
	_, avahiErr := os.Stat(avahiTimeMachinePath)
	respondOK(w, map[string]interface{}{
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

	setSetting := func(key string, val *bool) {
		if val == nil {
			return
		}
		v := "0"
		if *val {
			v = "1"
		}
		h.db.Exec(`INSERT INTO settings (key, value) VALUES ($1, $2) ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_at = NOW()`, key, v)
	}

	setSetting("smb_time_machine", req.TimeMachine)
	setSetting("smb_shadow_copy", req.ShadowCopy)
	setSetting("smb_recycle_bin", req.RecycleBin)

	if req.TimeMachine != nil {
		if *req.TimeMachine {
			writeAvahiTimeMachineService()
		} else {
			os.Remove(avahiTimeMachinePath)
		}
	}

	h.regenerateSMBConf()
	respondOK(w, map[string]interface{}{"success": true})
}

const avahiTimeMachinePath = "/etc/avahi/services/dplaneos-timemachine.service"
const avahiTimeMachineXML = `<?xml version="1.0" standalone='no'?>
<!DOCTYPE service-group SYSTEM "avahi-service.dtd">
<service-group>
  <name replace-wildcards="yes">%h</name>
  <service>
    <type>_adisk._tcp</type>
    <port>9</port>
    <txt-record>sys=waMa=0,adVF=0x100</txt-record>
    <txt-record>dk0=adVN=DPlaneOS,adVF=0x82</txt-record>
  </service>
  <service>
    <type>_device-info._tcp</type>
    <port>0</port>
    <txt-record>model=MacSamba</txt-record>
  </service>
</service-group>
`

func writeAvahiTimeMachineService() {
	if err := os.MkdirAll("/etc/avahi/services", 0755); err != nil {
		log.Printf("WARN: avahi services dir: %v", err)
		return
	}
	if err := os.WriteFile(avahiTimeMachinePath, []byte(avahiTimeMachineXML), 0644); err != nil {
		log.Printf("WARN: avahi service write: %v", err)
	}
}

// sanitizeSMBConfValue removes newlines and other characters that could break smb.conf formatting
func sanitizeSMBConfValue(val string) string {
	// Remove carriage returns and newlines
	val = strings.ReplaceAll(val, "\n", " ")
	val = strings.ReplaceAll(val, "\r", " ")
	// Strip characters that could break section headers or start new directives
	val = strings.ReplaceAll(val, "[", "(")
	val = strings.ReplaceAll(val, "]", ")")
	val = strings.ReplaceAll(val, "=", ":")
	return strings.TrimSpace(val)
}
