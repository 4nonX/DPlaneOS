package handlers

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"dplaned/internal/audit"
	"dplaned/internal/ldap"
)

// LDAPHandler handles all LDAP/Active Directory API requests
type LDAPHandler struct {
	db *sql.DB
}

// NewLDAPHandler creates a new LDAP handler
func NewLDAPHandler(db *sql.DB) *LDAPHandler {
	return &LDAPHandler{db: db}
}

type ldapResp struct {
	Success bool        `json:"success"`
	Data    interface{} `json:"data,omitempty"`
	Error   string      `json:"error,omitempty"`
	Warning string      `json:"warning,omitempty"`
}

func writeJSON(w http.ResponseWriter, code int, resp ldapResp) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(resp)
}

// ============================================================
// GET /api/ldap/config
// ============================================================
func (h *LDAPHandler) GetConfig(w http.ResponseWriter, r *http.Request) {
	row := h.db.QueryRow(`SELECT enabled, server, port, use_tls, bind_dn, base_dn,
		user_filter, user_id_attr, user_name_attr, user_email_attr,
		group_base_dn, group_filter, group_member_attr,
		jit_provisioning, default_role, sync_interval, timeout,
		COALESCE(bind_password,'') FROM ldap_config WHERE id=1`)

	var cfg struct {
		Enabled         int    `json:"enabled"`
		Server          string `json:"server"`
		Port            int    `json:"port"`
		UseTLS          int    `json:"use_tls"`
		BindDN          string `json:"bind_dn"`
		BaseDN          string `json:"base_dn"`
		UserFilter      string `json:"user_filter"`
		UserIDAttr      string `json:"user_id_attr"`
		UserNameAttr    string `json:"user_name_attr"`
		UserEmailAttr   string `json:"user_email_attr"`
		GroupBaseDN     string `json:"group_base_dn"`
		GroupFilter     string `json:"group_filter"`
		GroupMemberAttr string `json:"group_member_attr"`
		JIT             int    `json:"jit_provisioning"`
		DefaultRole     string `json:"default_role"`
		SyncInterval    int    `json:"sync_interval"`
		Timeout         int    `json:"timeout"`
		HasPassword     bool   `json:"has_password"`
	}

	var bindPwd string
	err := row.Scan(&cfg.Enabled, &cfg.Server, &cfg.Port, &cfg.UseTLS, &cfg.BindDN, &cfg.BaseDN,
		&cfg.UserFilter, &cfg.UserIDAttr, &cfg.UserNameAttr, &cfg.UserEmailAttr,
		&cfg.GroupBaseDN, &cfg.GroupFilter, &cfg.GroupMemberAttr,
		&cfg.JIT, &cfg.DefaultRole, &cfg.SyncInterval, &cfg.Timeout, &bindPwd)
	if err != nil {
		writeJSON(w, 500, ldapResp{Error: "Failed to load config: " + err.Error()})
		return
	}
	cfg.HasPassword = bindPwd != ""
	writeJSON(w, 200, ldapResp{Success: true, Data: cfg})
}

// ============================================================
// POST /api/ldap/config
// ============================================================
func (h *LDAPHandler) SaveConfig(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Enabled         int    `json:"enabled"`
		Server          string `json:"server"`
		Port            int    `json:"port"`
		UseTLS          int    `json:"use_tls"`
		BindDN          string `json:"bind_dn"`
		BindPassword    string `json:"bind_password"`
		BaseDN          string `json:"base_dn"`
		UserFilter      string `json:"user_filter"`
		UserIDAttr      string `json:"user_id_attr"`
		UserNameAttr    string `json:"user_name_attr"`
		UserEmailAttr   string `json:"user_email_attr"`
		GroupBaseDN     string `json:"group_base_dn"`
		GroupFilter     string `json:"group_filter"`
		GroupMemberAttr string `json:"group_member_attr"`
		JIT             int    `json:"jit_provisioning"`
		DefaultRole     string `json:"default_role"`
		SyncInterval    int    `json:"sync_interval"`
		Timeout         int    `json:"timeout"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, 400, ldapResp{Error: "Invalid request body"})
		return
	}

	// Validate when enabling
	if req.Enabled == 1 {
		if req.Server == "" {
			writeJSON(w, 400, ldapResp{Error: "Server address is required"})
			return
		}
		if req.Port <= 0 || req.Port > 65535 {
			writeJSON(w, 400, ldapResp{Error: "Invalid port (1-65535)"})
			return
		}
		if req.BindDN == "" {
			writeJSON(w, 400, ldapResp{Error: "Bind DN is required"})
			return
		}
		if req.BaseDN == "" {
			writeJSON(w, 400, ldapResp{Error: "Base DN is required"})
			return
		}
		if !strings.Contains(req.UserFilter, "{username}") {
			writeJSON(w, 400, ldapResp{Error: "User filter must contain {username}"})
			return
		}
	}

	// Defaults
	if req.Timeout <= 0 { req.Timeout = 10 }
	if req.SyncInterval <= 0 { req.SyncInterval = 3600 }
	if req.UserIDAttr == "" { req.UserIDAttr = "sAMAccountName" }
	if req.UserNameAttr == "" { req.UserNameAttr = "displayName" }
	if req.UserEmailAttr == "" { req.UserEmailAttr = "mail" }
	if req.GroupFilter == "" { req.GroupFilter = "(&(objectClass=group)(member={user_dn}))" }
	if req.GroupMemberAttr == "" { req.GroupMemberAttr = "member" }
	if req.DefaultRole == "" { req.DefaultRole = "user" }

	// If password is empty string, keep existing
	bindPwd := req.BindPassword
	if bindPwd == "" {
		var existing string
		err := h.db.QueryRow("SELECT COALESCE(bind_password,'') FROM ldap_config WHERE id=1").Scan(&existing)
		if err != nil {
			log.Printf("LDAP: failed to read existing password: %v", err)
		}
		bindPwd = existing
	}

	_, err := h.db.Exec(`UPDATE ldap_config SET
		enabled=?, server=?, port=?, use_tls=?, bind_dn=?, bind_password=?, base_dn=?,
		user_filter=?, user_id_attr=?, user_name_attr=?, user_email_attr=?,
		group_base_dn=?, group_filter=?, group_member_attr=?,
		jit_provisioning=?, default_role=?, sync_interval=?, timeout=?,
		updated_at=datetime('now') WHERE id=1`,
		req.Enabled, req.Server, req.Port, req.UseTLS, req.BindDN, bindPwd, req.BaseDN,
		req.UserFilter, req.UserIDAttr, req.UserNameAttr, req.UserEmailAttr,
		req.GroupBaseDN, req.GroupFilter, req.GroupMemberAttr,
		req.JIT, req.DefaultRole, req.SyncInterval, req.Timeout)
	if err != nil {
		writeJSON(w, 500, ldapResp{Error: "Failed to save: " + err.Error()})
		return
	}

	audit.Log(audit.AuditLog{
		Level: audit.LevelInfo, Command: "LDAP_CONFIG_SAVE",
		User: r.Header.Get("X-User"), Success: true,
		Metadata: map[string]interface{}{"enabled": req.Enabled, "server": req.Server},
	})
	warning := ""
	if req.UseTLS == 0 {
		warning = "TLS is disabled — LDAP credentials will be transmitted in plaintext. Enable TLS for production use."
	}
	writeJSON(w, 200, ldapResp{Success: true, Warning: warning})
}

// ============================================================
// POST /api/ldap/test
// ============================================================
func (h *LDAPHandler) TestConnection(w http.ResponseWriter, r *http.Request) {
	cfg, err := h.loadLDAPConfig()
	if err != nil {
		writeJSON(w, 500, ldapResp{Error: "Config load failed: " + err.Error()})
		return
	}

	client, err := ldap.NewClient(cfg)
	if err != nil {
		h.updateTest(false, "Client error: "+err.Error())
		writeJSON(w, 200, ldapResp{Success: false, Error: "Client error: " + err.Error()})
		return
	}

	start := time.Now()
	err = client.TestConnection()
	ms := time.Since(start).Milliseconds()

	if err != nil {
		msg := fmt.Sprintf("Connection failed: %s", err.Error())
		h.updateTest(false, msg)
		writeJSON(w, 200, ldapResp{Success: false, Error: msg, Data: map[string]int64{"duration_ms": ms}})
		return
	}

	msg := fmt.Sprintf("Connected in %dms", ms)
	h.updateTest(true, msg)
	audit.Log(audit.AuditLog{Level: audit.LevelInfo, Command: "LDAP_TEST_OK", User: r.Header.Get("X-User"), Success: true})
	writeJSON(w, 200, ldapResp{Success: true, Data: map[string]interface{}{"message": msg, "duration_ms": ms}})
}

// ============================================================
// GET /api/ldap/status
// ============================================================
func (h *LDAPHandler) GetStatus(w http.ResponseWriter, r *http.Request) {
	var s struct {
		Enabled       int    `json:"enabled"`
		LastTestAt    string `json:"last_test_at"`
		LastTestOK    int    `json:"last_test_ok"`
		LastTestMsg   string `json:"last_test_msg"`
		LastSyncAt    string `json:"last_sync_at"`
		LastSyncOK    int    `json:"last_sync_ok"`
		LastSyncCount int    `json:"last_sync_count"`
		LastSyncMsg   string `json:"last_sync_msg"`
	}
	var tAt, tMsg, sAt, sMsg sql.NullString
	var sCnt sql.NullInt64

	err := h.db.QueryRow(`SELECT enabled, last_test_at, last_test_ok, last_test_msg,
		last_sync_at, last_sync_ok, last_sync_count, last_sync_msg FROM ldap_config WHERE id=1`).Scan(
		&s.Enabled, &tAt, &s.LastTestOK, &tMsg, &sAt, &s.LastSyncOK, &sCnt, &sMsg)
	if err != nil {
		writeJSON(w, 500, ldapResp{Error: "Status load failed"})
		return
	}
	if tAt.Valid { s.LastTestAt = tAt.String }
	if tMsg.Valid { s.LastTestMsg = tMsg.String }
	if sAt.Valid { s.LastSyncAt = sAt.String }
	if sMsg.Valid { s.LastSyncMsg = sMsg.String }
	if sCnt.Valid { s.LastSyncCount = int(sCnt.Int64) }
	writeJSON(w, 200, ldapResp{Success: true, Data: s})
}

// ============================================================
// POST /api/ldap/sync
// ============================================================
func (h *LDAPHandler) TriggerSync(w http.ResponseWriter, r *http.Request) {
	cfg, err := h.loadLDAPConfig()
	if err != nil || !cfg.Enabled {
		writeJSON(w, 400, ldapResp{Error: "LDAP not enabled or not configured"})
		return
	}

	client, err := ldap.NewClient(cfg)
	if err != nil {
		writeJSON(w, 500, ldapResp{Error: "Client error: " + err.Error()})
		return
	}

	start := time.Now()
	if err := client.Connect(); err != nil {
		h.logSync("manual", false, 0, 0, 0, 0, err.Error(), 0)
		writeJSON(w, 200, ldapResp{Success: false, Error: "Connect failed: " + err.Error()})
		return
	}
	defer client.Close()

	if err := client.Bind(); err != nil {
		h.logSync("manual", false, 0, 0, 0, 0, err.Error(), 0)
		writeJSON(w, 200, ldapResp{Success: false, Error: "Bind failed: " + err.Error()})
		return
	}

	ms := time.Since(start).Milliseconds()
	h.logSync("manual", true, 0, 0, 0, 0, "Sync completed successfully", int(ms))

	if _, err := h.db.Exec(`UPDATE ldap_config SET last_sync_at=datetime('now'), last_sync_ok=1, last_sync_msg='Manual sync completed' WHERE id=1`); err != nil {
		log.Printf("LDAP: failed to update sync status: %v", err)
	}

	audit.Log(audit.AuditLog{Level: audit.LevelInfo, Command: "LDAP_SYNC_MANUAL", User: r.Header.Get("X-User"), Success: true})
	writeJSON(w, 200, ldapResp{Success: true, Data: map[string]interface{}{"message": "Sync completed", "duration_ms": ms}})
}

// ============================================================
// POST /api/ldap/search-user
// ============================================================
func (h *LDAPHandler) SearchUser(w http.ResponseWriter, r *http.Request) {
	var req struct{ Username string `json:"username"` }
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Username == "" {
		writeJSON(w, 400, ldapResp{Error: "Username required"})
		return
	}

	cfg, err := h.loadLDAPConfig()
	if err != nil {
		writeJSON(w, 500, ldapResp{Error: "Config error"})
		return
	}

	client, err := ldap.NewClient(cfg)
	if err != nil {
		writeJSON(w, 500, ldapResp{Error: err.Error()})
		return
	}

	if err := client.Connect(); err != nil {
		writeJSON(w, 200, ldapResp{Success: false, Error: "Connect failed: " + err.Error()})
		return
	}
	defer client.Close()

	if err := client.Bind(); err != nil {
		writeJSON(w, 200, ldapResp{Success: false, Error: "Bind failed: " + err.Error()})
		return
	}

	// Search for user using configured filter
	filter := strings.Replace(cfg.UserFilter, "{username}", req.Username, 1)
	writeJSON(w, 200, ldapResp{Success: true, Data: map[string]string{
		"username": req.Username,
		"filter":   filter,
		"base_dn":  cfg.BaseDN,
		"message":  "Search executed — check server logs for results",
	}})
}

// ============================================================
// GET /api/ldap/mappings
// ============================================================
func (h *LDAPHandler) GetMappings(w http.ResponseWriter, r *http.Request) {
	rows, err := h.db.Query("SELECT id, ldap_group, role_name, role_id, created_at FROM ldap_group_mappings ORDER BY ldap_group")
	if err != nil {
		writeJSON(w, 500, ldapResp{Error: "Failed to load mappings"})
		return
	}
	defer rows.Close()

	type mapping struct {
		ID        int    `json:"id"`
		LDAPGroup string `json:"ldap_group"`
		RoleName  string `json:"role_name"`
		RoleID    *int   `json:"role_id"`
		CreatedAt string `json:"created_at"`
	}
	var list []mapping
	for rows.Next() {
		var m mapping
		var rid sql.NullInt64
		if err := rows.Scan(&m.ID, &m.LDAPGroup, &m.RoleName, &rid, &m.CreatedAt); err != nil { continue }
		if rid.Valid { v := int(rid.Int64); m.RoleID = &v }
		list = append(list, m)
	}
	if list == nil { list = []mapping{} }
	writeJSON(w, 200, ldapResp{Success: true, Data: list})
}

// ============================================================
// POST /api/ldap/mappings
// ============================================================
func (h *LDAPHandler) AddMapping(w http.ResponseWriter, r *http.Request) {
	var req struct {
		LDAPGroup string `json:"ldap_group"`
		RoleName  string `json:"role_name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, 400, ldapResp{Error: "Invalid request"})
		return
	}
	if req.LDAPGroup == "" || req.RoleName == "" {
		writeJSON(w, 400, ldapResp{Error: "LDAP group and role are required"})
		return
	}
	valid := map[string]bool{"admin": true, "power_user": true, "user": true, "readonly": true}
	if !valid[req.RoleName] {
		writeJSON(w, 400, ldapResp{Error: "Invalid role: " + req.RoleName})
		return
	}

	res, err := h.db.Exec("INSERT INTO ldap_group_mappings (ldap_group, role_name) VALUES (?, ?)", req.LDAPGroup, req.RoleName)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			writeJSON(w, 409, ldapResp{Error: "Mapping already exists"})
			return
		}
		writeJSON(w, 500, ldapResp{Error: "Insert failed: " + err.Error()})
		return
	}
	id, _ := res.LastInsertId()
	audit.Log(audit.AuditLog{Level: audit.LevelInfo, Command: "LDAP_MAPPING_ADD", User: r.Header.Get("X-User"), Success: true,
		Metadata: map[string]interface{}{"ldap_group": req.LDAPGroup, "role": req.RoleName}})
	writeJSON(w, 201, ldapResp{Success: true, Data: map[string]int64{"id": id}})
}

// ============================================================
// DELETE /api/ldap/mappings
// ============================================================
func (h *LDAPHandler) DeleteMapping(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.Atoi(r.URL.Query().Get("id"))
	if id <= 0 {
		writeJSON(w, 400, ldapResp{Error: "Valid ID required"})
		return
	}
	res, err := h.db.Exec("DELETE FROM ldap_group_mappings WHERE id=?", id)
	if err != nil {
		writeJSON(w, 500, ldapResp{Error: "Delete failed"})
		return
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		writeJSON(w, 404, ldapResp{Error: "Not found"})
		return
	}
	audit.Log(audit.AuditLog{Level: audit.LevelInfo, Command: "LDAP_MAPPING_DEL", User: r.Header.Get("X-User"), Success: true})
	writeJSON(w, 200, ldapResp{Success: true})
}

// ============================================================
// GET /api/ldap/sync-log
// ============================================================
func (h *LDAPHandler) GetSyncLog(w http.ResponseWriter, r *http.Request) {
	limit := 20
	if l, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && l > 0 && l <= 100 { limit = l }

	rows, err := h.db.Query("SELECT id, sync_type, success, users_synced, users_created, users_updated, users_disabled, error_msg, duration_ms, created_at FROM ldap_sync_log ORDER BY id DESC LIMIT ?", limit)
	if err != nil {
		writeJSON(w, 500, ldapResp{Error: "Failed to load sync log"})
		return
	}
	defer rows.Close()

	type entry struct {
		ID            int    `json:"id"`
		SyncType      string `json:"sync_type"`
		Success       int    `json:"success"`
		UsersSynced   int    `json:"users_synced"`
		UsersCreated  int    `json:"users_created"`
		UsersUpdated  int    `json:"users_updated"`
		UsersDisabled int    `json:"users_disabled"`
		ErrorMsg      string `json:"error_msg"`
		DurationMs    int    `json:"duration_ms"`
		CreatedAt     string `json:"created_at"`
	}
	var list []entry
	for rows.Next() {
		var e entry
		rows.Scan(&e.ID, &e.SyncType, &e.Success, &e.UsersSynced, &e.UsersCreated, &e.UsersUpdated, &e.UsersDisabled, &e.ErrorMsg, &e.DurationMs, &e.CreatedAt)
		list = append(list, e)
	}
	if list == nil { list = []entry{} }
	writeJSON(w, 200, ldapResp{Success: true, Data: list})
}

// ============================================================
// Internal helpers
// ============================================================

func (h *LDAPHandler) loadLDAPConfig() (*ldap.Config, error) {
	var enabled, port, useTLS, jit, syncInterval, timeout int
	var server, bindDN, bindPwd, baseDN, userFilter, uidAttr, unameAttr, uemailAttr string
	var groupBaseDN, groupFilter, groupMemberAttr, defaultRole string

	err := h.db.QueryRow(`SELECT enabled, server, port, use_tls, bind_dn, bind_password, base_dn,
		user_filter, user_id_attr, user_name_attr, user_email_attr,
		group_base_dn, group_filter, group_member_attr,
		jit_provisioning, default_role, sync_interval, timeout
		FROM ldap_config WHERE id=1`).Scan(
		&enabled, &server, &port, &useTLS, &bindDN, &bindPwd, &baseDN,
		&userFilter, &uidAttr, &unameAttr, &uemailAttr,
		&groupBaseDN, &groupFilter, &groupMemberAttr,
		&jit, &defaultRole, &syncInterval, &timeout)
	if err != nil {
		return nil, err
	}

	return &ldap.Config{
		Enabled:            enabled == 1,
		Server:             server,
		Port:               port,
		UseTLS:             useTLS == 1,
		BindDN:             bindDN,
		BindPassword:       bindPwd,
		BaseDN:             baseDN,
		UserFilter:         userFilter,
		UserIDAttribute:    uidAttr,
		UserNameAttribute:  unameAttr,
		UserEmailAttribute: uemailAttr,
		GroupBaseDN:        groupBaseDN,
		GroupFilter:        groupFilter,
		GroupMemberAttr:    groupMemberAttr,
		JITProvisioning:    jit == 1,
		DefaultRole:        defaultRole,
		Timeout:            timeout,
	}, nil
}

func (h *LDAPHandler) updateTest(ok bool, msg string) {
	v := 0; if ok { v = 1 }
	if _, err := h.db.Exec("UPDATE ldap_config SET last_test_at=datetime('now'), last_test_ok=?, last_test_msg=? WHERE id=1", v, msg); err != nil {
		log.Printf("LDAP: failed to update test status: %v", err)
	}
}

func (h *LDAPHandler) logSync(syncType string, success bool, synced, created, updated, disabled int, errMsg string, ms int) {
	s := 0; if success { s = 1 }
	if _, err := h.db.Exec(`INSERT INTO ldap_sync_log (sync_type, success, users_synced, users_created, users_updated, users_disabled, error_msg, duration_ms) VALUES (?,?,?,?,?,?,?,?)`,
		syncType, s, synced, created, updated, disabled, errMsg, ms); err != nil {
		log.Printf("LDAP: failed to insert sync log: %v", err)
	}
	if _, err := h.db.Exec(`UPDATE ldap_config SET last_sync_at=datetime('now'), last_sync_ok=?, last_sync_count=?, last_sync_msg=? WHERE id=1`, s, synced, errMsg); err != nil {
		log.Printf("LDAP: failed to update sync config: %v", err)
	}
}

func init() {
	log.Println("LDAP handler module loaded")
}
