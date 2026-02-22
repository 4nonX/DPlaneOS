package handlers

import (
	"database/sql"

	"dplaned/internal/cmdutil"
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

func (h *ShareCRUDHandler) initTable() {
	h.db.Exec(`CREATE TABLE IF NOT EXISTS smb_shares (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		name TEXT NOT NULL UNIQUE,
		path TEXT NOT NULL,
		comment TEXT DEFAULT '',
		browsable INTEGER DEFAULT 1,
		read_only INTEGER DEFAULT 0,
		guest_ok INTEGER DEFAULT 0,
		valid_users TEXT DEFAULT '',
		write_list TEXT DEFAULT '',
		create_mask TEXT DEFAULT '0664',
		directory_mask TEXT DEFAULT '0775',
		enabled INTEGER DEFAULT 1,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
	)`)
}

// HandleShares — GET: list shares, POST: create/update/delete
func (h *ShareCRUDHandler) HandleShares(w http.ResponseWriter, r *http.Request) {
	h.initTable()

	switch r.Method {
	case http.MethodGet:
		h.listShares(w, r)
	case http.MethodPost:
		h.shareAction(w, r)
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func (h *ShareCRUDHandler) listShares(w http.ResponseWriter, r *http.Request) {
	// Check for ?id= query param
	idParam := r.URL.Query().Get("id")
	if idParam != "" {
		h.getShare(w, idParam)
		return
	}

	rows, err := h.db.Query(`SELECT id, name, path, comment, browsable, read_only, guest_ok, valid_users, write_list, create_mask, directory_mask, enabled, created_at FROM smb_shares ORDER BY name`)
	if err != nil {
		respondErrorSimple(w, "Failed to list shares", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	var shares []map[string]interface{}
	for rows.Next() {
		var id, browsable, readOnly, guestOk, enabled int
		var name, path, comment, validUsers, writeList, createMask, dirMask, createdAt string
		rows.Scan(&id, &name, &path, &comment, &browsable, &readOnly, &guestOk, &validUsers, &writeList, &createMask, &dirMask, &enabled, &createdAt)
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
	var shareID, browsable, readOnly, guestOk, enabled int
	var name, path, comment, validUsers, writeList, createMask, dirMask, createdAt string

	err := h.db.QueryRow(
		`SELECT id, name, path, comment, browsable, read_only, guest_ok, valid_users, write_list, create_mask, directory_mask, enabled, created_at FROM smb_shares WHERE id = ?`, id,
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
	Action       string `json:"action"` // create, update, delete
	ID           int    `json:"id"`
	Name         string `json:"name"`
	Path         string `json:"path"`
	Comment      string `json:"comment"`
	Browsable    *bool  `json:"browsable"`
	ReadOnly     *bool  `json:"read_only"`
	GuestOk      *bool  `json:"guest_ok"`
	ValidUsers   string `json:"valid_users"`
	WriteList    string `json:"write_list"`
	CreateMask   string `json:"create_mask"`
	DirectoryMask string `json:"directory_mask"`
	Enabled      *bool  `json:"enabled"`
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

	result, err := h.db.Exec(
		`INSERT INTO smb_shares (name, path, comment, browsable, read_only, guest_ok, valid_users, write_list, create_mask, directory_mask)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		req.Name, req.Path, req.Comment, browsable, readOnly, guestOk, req.ValidUsers, req.WriteList, createMask, dirMask,
	)
	if err != nil {
		respondErrorSimple(w, "Failed to create share (name may already exist)", http.StatusConflict)
		return
	}

	id, _ := result.LastInsertId()

	// Regenerate smb.conf
	h.regenerateSMBConf()

	respondJSON(w, http.StatusOK, map[string]interface{}{
		"success": true,
		"id":      id,
		"message": fmt.Sprintf("Share %s created", req.Name),
	})
}

func (h *ShareCRUDHandler) updateShare(w http.ResponseWriter, req shareActionRequest) {
	if req.ID == 0 {
		respondErrorSimple(w, "Share ID required", http.StatusBadRequest)
		return
	}

	if req.Name != "" {
		h.db.Exec(`UPDATE smb_shares SET name = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`, req.Name, req.ID)
	}
	if req.Path != "" {
		h.db.Exec(`UPDATE smb_shares SET path = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`, req.Path, req.ID)
	}
	if req.Comment != "" {
		h.db.Exec(`UPDATE smb_shares SET comment = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`, req.Comment, req.ID)
	}
	if req.Browsable != nil {
		v := 0
		if *req.Browsable {
			v = 1
		}
		h.db.Exec(`UPDATE smb_shares SET browsable = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`, v, req.ID)
	}
	if req.ReadOnly != nil {
		v := 0
		if *req.ReadOnly {
			v = 1
		}
		h.db.Exec(`UPDATE smb_shares SET read_only = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`, v, req.ID)
	}
	if req.GuestOk != nil {
		v := 0
		if *req.GuestOk {
			v = 1
		}
		h.db.Exec(`UPDATE smb_shares SET guest_ok = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`, v, req.ID)
	}
	if req.ValidUsers != "" {
		h.db.Exec(`UPDATE smb_shares SET valid_users = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`, req.ValidUsers, req.ID)
	}
	if req.Enabled != nil {
		v := 0
		if *req.Enabled {
			v = 1
		}
		h.db.Exec(`UPDATE smb_shares SET enabled = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`, v, req.ID)
	}

	h.regenerateSMBConf()

	respondJSON(w, http.StatusOK, map[string]interface{}{
		"success": true,
		"message": "Share updated",
	})
}

func (h *ShareCRUDHandler) deleteShare(w http.ResponseWriter, req shareActionRequest) {
	if req.ID == 0 {
		respondErrorSimple(w, "Share ID required", http.StatusBadRequest)
		return
	}

	h.db.Exec(`DELETE FROM smb_shares WHERE id = ?`, req.ID)
	h.regenerateSMBConf()

	respondJSON(w, http.StatusOK, map[string]interface{}{
		"success": true,
		"message": "Share deleted",
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
	conf.WriteString("   server string = D-PlaneOS NAS\n")
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
				conf.WriteString("   " + trimmed + "\n")
			}
		}
	}
	conf.WriteString("\n")

	for rows.Next() {
		var name, path, comment, validUsers, writeList, createMask, dirMask string
		var browsable, readOnly, guestOk int
		rows.Scan(&name, &path, &comment, &browsable, &readOnly, &guestOk, &validUsers, &writeList, &createMask, &dirMask)

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
			conf.WriteString("   shadow:format = %Y-%m-%d-%H%M%S\n")
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
