package handlers

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/gorilla/mux"

	"dplaned/internal/audit"
	"dplaned/internal/gitops"
	"dplaned/internal/jobs"
	"dplaned/internal/ldap"
	"dplaned/internal/nixwriter"
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
		COALESCE(bind_password,''), provider_type, realm, domain_joined FROM ldap_config WHERE id=1`)

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
		ProviderType    string `json:"provider_type"`
		Realm           string `json:"realm"`
		DomainJoined    bool   `json:"domain_joined"`
	}

	var bindPwd string
	err := row.Scan(&cfg.Enabled, &cfg.Server, &cfg.Port, &cfg.UseTLS, &cfg.BindDN, &cfg.BaseDN,
		&cfg.UserFilter, &cfg.UserIDAttr, &cfg.UserNameAttr, &cfg.UserEmailAttr,
		&cfg.GroupBaseDN, &cfg.GroupFilter, &cfg.GroupMemberAttr,
		&cfg.JIT, &cfg.DefaultRole, &cfg.SyncInterval, &cfg.Timeout, &bindPwd,
		&cfg.ProviderType, &cfg.Realm, &cfg.DomainJoined)
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
		ProviderType    string `json:"provider_type"` // "openldap", "ad", "active-directory", "apple-od"
		Realm           string `json:"realm"`
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
		enabled=$1, server=$2, port=$3, use_tls=$4, bind_dn=$5, bind_password=$6, base_dn=$7,
		user_filter=$8, user_id_attr=$9, user_name_attr=$10, user_email_attr=$11,
		group_base_dn=$12, group_filter=$13, group_member_attr=$14,
		jit_provisioning=$15, default_role=$16, sync_interval=$17, timeout=$18,
		provider_type=$19, realm=$20,
		updated_at=NOW() WHERE id=1`,
		req.Enabled, req.Server, req.Port, req.UseTLS, req.BindDN, bindPwd, req.BaseDN,
		req.UserFilter, req.UserIDAttr, req.UserNameAttr, req.UserEmailAttr,
		req.GroupBaseDN, req.GroupFilter, req.GroupMemberAttr,
		req.JIT, req.DefaultRole, req.SyncInterval, req.Timeout,
		req.ProviderType, req.Realm)
	if err != nil {
		writeJSON(w, 500, ldapResp{Error: "Failed to save: " + err.Error()})
		return
	}

	audit.Log(audit.AuditLog{
		Level: audit.LevelInfo, Command: "LDAP_CONFIG_SAVE",
		User: r.Header.Get("X-User"), Success: true,
		Metadata: map[string]interface{}{"enabled": req.Enabled, "server": req.Server},
	})
	var warning string
	if req.UseTLS == 0 {
		warning = "TLS is disabled - LDAP credentials will be transmitted in plaintext. Enable TLS for production use."
	}

	// Trigger GitOps commit
	gitops.CommitAllAsync(h.db)

	// v7.3.0: If AD is selected, update nixwriter state for Samba AD membership
	if req.ProviderType == "ad" || req.ProviderType == "active-directory" {
		securityMode := "ads"
		if req.Enabled == 0 {
			securityMode = "user"
		}

		writer := nixwriter.DefaultWriter()
		state := writer.State()

		err = writer.SetSambaGlobals(nixwriter.SambaGlobalOpts{
			Workgroup:        state.SambaWorkgroup,
			ServerString:     state.SambaServerString,
			TimeMachine:      state.SambaTimeMachine,
			AllowGuest:       state.SambaAllowGuest,
			ExtraGlobal:      state.SambaExtraGlobal,
			SecurityMode:     securityMode,
			Realm:            req.Realm,
			DomainController: req.Server,
		})
		if err != nil {
			log.Printf("ERROR: Failed to update nixwriter Samba state: %v", err)
		}
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

	// Actually fetch all users from the directory.
	syncRes, users, err := client.SyncAll()
	if err != nil {
		h.logSync("manual", false, 0, 0, 0, 0, err.Error(), 0)
		writeJSON(w, 200, ldapResp{Success: false, Error: "Sync search failed: " + err.Error()})
		return
	}

	// Upsert each user into the local users table (source='ldap').
	// LDAP users get an empty password_hash - they authenticate via LDAP bind,
	// never via local password. role defaults to 'user' unless a group mapping matches.
	for _, u := range users {
		roleIDs := client.MapGroupsToRoles(u.Groups)
		role := cfg.DefaultRole
		if role == "" {
			role = "user"
		}
		// Use the first mapped role if available. Roles are stored as names in the users table.
		if len(roleIDs) > 0 && len(cfg.GroupMappings) > 0 {
			for _, gm := range cfg.GroupMappings {
				if gm.RoleID == roleIDs[0] && gm.RoleName != "" {
					role = gm.RoleName
					break
				}
			}
		}

		var existing int
		row := h.db.QueryRow(`SELECT COUNT(*) FROM users WHERE username=$1 AND source='ldap'`, u.Username)
		if scanErr := row.Scan(&existing); scanErr != nil {
			syncRes.Errors = append(syncRes.Errors, "db scan for "+u.Username+": "+scanErr.Error())
			syncRes.UsersSkipped++
			continue
		}

		if existing == 0 {
			_, dbErr := h.db.Exec(
				`INSERT INTO users (username, email, display_name, role, source, password_hash, updated_at)
				 VALUES ($1, $2, $3, $4, 'ldap', '', NOW())`,
				u.Username, u.Email, u.FullName, role,
			)
			if dbErr != nil {
				syncRes.Errors = append(syncRes.Errors, "insert "+u.Username+": "+dbErr.Error())
				syncRes.UsersSkipped++
			} else {
				syncRes.UsersCreated++
			}
		} else {
			_, dbErr := h.db.Exec(
				`UPDATE users SET email=$1, display_name=$2, role=$3, updated_at=NOW()
				 WHERE username=$4 AND source='ldap'`,
				u.Email, u.FullName, role, u.Username,
			)
			if dbErr != nil {
				syncRes.Errors = append(syncRes.Errors, "update "+u.Username+": "+dbErr.Error())
				syncRes.UsersSkipped++
			} else {
				syncRes.UsersUpdated++
			}
		}
	}

	ms := int(time.Since(start).Milliseconds())
	success := len(syncRes.Errors) == 0
	msg := fmt.Sprintf("Sync completed: found=%d created=%d updated=%d skipped=%d",
		syncRes.UsersFound, syncRes.UsersCreated, syncRes.UsersUpdated, syncRes.UsersSkipped)

	h.logSync("manual", success, syncRes.UsersFound, syncRes.UsersCreated, syncRes.UsersUpdated, syncRes.UsersSkipped, msg, ms)

	if _, dbErr := h.db.Exec(`UPDATE ldap_config SET last_sync_at=NOW(), last_sync_ok=$1, last_sync_msg=$2 WHERE id=1`,
		success, msg); dbErr != nil {
		log.Printf("LDAP: failed to update sync status: %v", dbErr)
	}

	audit.Log(audit.AuditLog{Level: audit.LevelInfo, Command: "LDAP_SYNC_MANUAL", User: r.Header.Get("X-User"), Success: success})
	writeJSON(w, 200, ldapResp{Success: success, Data: map[string]interface{}{
		"message":       msg,
		"duration_ms":   ms,
		"users_found":   syncRes.UsersFound,
		"users_created": syncRes.UsersCreated,
		"users_updated": syncRes.UsersUpdated,
		"users_skipped": syncRes.UsersSkipped,
		"errors":        syncRes.Errors,
	}})
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
		"message":  "Search executed - check server logs for results",
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

	var id int64
	err := h.db.QueryRow("INSERT INTO ldap_group_mappings (ldap_group, role_name) VALUES ($1, $2) RETURNING id",
		req.LDAPGroup, req.RoleName).Scan(&id)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			writeJSON(w, 409, ldapResp{Error: "Mapping already exists"})
			return
		}
		writeJSON(w, 500, ldapResp{Error: "Insert failed: " + err.Error()})
		return
	}
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
	res, err := h.db.Exec("DELETE FROM ldap_group_mappings WHERE id=$1", id)
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

	rows, err := h.db.Query("SELECT id, sync_type, success, users_synced, users_created, users_updated, users_disabled, error_msg, duration_ms, created_at FROM ldap_sync_log ORDER BY id DESC LIMIT $1", limit)
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
	if _, err := h.db.Exec("UPDATE ldap_config SET last_test_at=NOW(), last_test_ok=$1, last_test_msg=$2 WHERE id=1", v, msg); err != nil {
		log.Printf("LDAP: failed to update test status: %v", err)
	}
}

func (h *LDAPHandler) logSync(syncType string, success bool, synced, created, updated, disabled int, errMsg string, ms int) {
	s := 0; if success { s = 1 }
	if _, err := h.db.Exec(`INSERT INTO ldap_sync_log (sync_type, success, users_synced, users_created, users_updated, users_disabled, error_msg, duration_ms) VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`,
		syncType, s, synced, created, updated, disabled, errMsg, ms); err != nil {
		log.Printf("LDAP: failed to insert sync log: %v", err)
	}
	if _, err := h.db.Exec(`UPDATE ldap_config SET last_sync_at=NOW(), last_sync_ok=$1, last_sync_count=$2, last_sync_msg=$3 WHERE id=1`, s, synced, errMsg); err != nil {
		log.Printf("LDAP: failed to update sync config: %v", err)
	}
}

func init() {
	log.Println("LDAP handler module loaded")
}

// ── AD domain CRUD ────────────────────────────────────────────────────────────

// domainNameRe allows uppercase alphanumeric with dots/hyphens (short AD names).
var domainNameRe = regexp.MustCompile(`^[A-Z0-9][A-Z0-9\.\-]{0,62}$`)

// realmRe allows a Kerberos realm: uppercase FQDN.
var realmRe = regexp.MustCompile(`^[A-Z0-9][A-Z0-9\.\-]{1,253}\.[A-Z]{2,}$`)

// idmapBackendSet is the set of accepted IDMAP backends.
var idmapBackendSet = map[string]bool{
	"rid": true, "ad": true, "tdb": true, "autorid": true,
}

// ============================================================
// GET /api/ldap/domains
// ============================================================
// ListDomains returns all configured AD forests with their IDMAP and join status.
func (h *LDAPHandler) ListDomains(w http.ResponseWriter, r *http.Request) {
	rows, err := h.db.Query(`SELECT id, name, realm, server, idmap_backend, idmap_low, idmap_high,
		domain_joined, domain_joined_at, kinit_principal, last_kinit_at, kinit_ok, enabled
		FROM ad_domains ORDER BY name`)
	if err != nil {
		writeJSON(w, 500, ldapResp{Error: "DB error: " + err.Error()})
		return
	}
	defer rows.Close()

	type domainRow struct {
		ID             int64  `json:"id"`
		Name           string `json:"name"`
		Realm          string `json:"realm"`
		Server         string `json:"server"`
		IDMAPBackend   string `json:"idmap_backend"`
		IDMAPLow       int64  `json:"idmap_low"`
		IDMAPHigh      int64  `json:"idmap_high"`
		DomainJoined   bool   `json:"domain_joined"`
		DomainJoinedAt string `json:"domain_joined_at,omitempty"`
		KinitPrincipal string `json:"kinit_principal"`
		LastKinitAt    string `json:"last_kinit_at,omitempty"`
		KinitOK        bool   `json:"kinit_ok"`
		Enabled        bool   `json:"enabled"`
	}
	var out []domainRow
	for rows.Next() {
		var d domainRow
		var joinedAt, kinitAt sql.NullString
		if err := rows.Scan(&d.ID, &d.Name, &d.Realm, &d.Server, &d.IDMAPBackend,
			&d.IDMAPLow, &d.IDMAPHigh, &d.DomainJoined, &joinedAt,
			&d.KinitPrincipal, &kinitAt, &d.KinitOK, &d.Enabled); err != nil {
			writeJSON(w, 500, ldapResp{Error: "row scan: " + err.Error()})
			return
		}
		if joinedAt.Valid {
			d.DomainJoinedAt = joinedAt.String
		}
		if kinitAt.Valid {
			d.LastKinitAt = kinitAt.String
		}
		out = append(out, d)
	}
	writeJSON(w, 200, ldapResp{Success: true, Data: out})
}

// ============================================================
// POST /api/ldap/domains
// ============================================================
// CreateDomain registers a new AD forest / IDMAP range configuration.
// Joining the domain is a separate POST /api/ldap/domains/:id/join operation.
func (h *LDAPHandler) CreateDomain(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name         string `json:"name"`
		Realm        string `json:"realm"`
		Server       string `json:"server"`
		BindDN       string `json:"bind_dn"`
		BindPassword string `json:"bind_password"`
		IDMAPBackend string `json:"idmap_backend"`
		IDMAPLow     int64  `json:"idmap_low"`
		IDMAPHigh    int64  `json:"idmap_high"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, 400, ldapResp{Error: "Invalid request body"})
		return
	}

	req.Name = strings.ToUpper(strings.TrimSpace(req.Name))
	req.Realm = strings.ToUpper(strings.TrimSpace(req.Realm))

	if !domainNameRe.MatchString(req.Name) {
		writeJSON(w, 400, ldapResp{Error: "Invalid domain name (use uppercase alphanumeric, dots, hyphens)"})
		return
	}
	if !realmRe.MatchString(req.Realm) {
		writeJSON(w, 400, ldapResp{Error: "Invalid Kerberos realm (must be uppercase FQDN)"})
		return
	}
	if req.IDMAPBackend == "" {
		req.IDMAPBackend = "rid"
	}
	if !idmapBackendSet[req.IDMAPBackend] {
		writeJSON(w, 400, ldapResp{Error: "idmap_backend must be one of: rid, ad, tdb, autorid"})
		return
	}
	if req.IDMAPLow <= 0 {
		req.IDMAPLow = 10000
	}
	if req.IDMAPHigh <= req.IDMAPLow {
		writeJSON(w, 400, ldapResp{Error: "idmap_high must be greater than idmap_low"})
		return
	}

	_, err := h.db.Exec(`INSERT INTO ad_domains
		(name, realm, server, bind_dn, bind_password, idmap_backend, idmap_low, idmap_high)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`,
		req.Name, req.Realm, req.Server, req.BindDN, req.BindPassword,
		req.IDMAPBackend, req.IDMAPLow, req.IDMAPHigh)
	if err != nil {
		writeJSON(w, 500, ldapResp{Error: "Failed to create domain: " + err.Error()})
		return
	}

	audit.Log(audit.AuditLog{
		Level: audit.LevelInfo, Command: "AD_DOMAIN_CREATE",
		User: r.Header.Get("X-User"), Success: true,
		Metadata: map[string]interface{}{"name": req.Name, "realm": req.Realm},
	})
	syncIDMAPToNixwriter(h.db)
	writeJSON(w, 200, ldapResp{Success: true})
}

// ============================================================
// DELETE /api/ldap/domains/:name
// ============================================================
// DeleteDomain removes an AD domain registration. The domain must not be joined.
func (h *LDAPHandler) DeleteDomain(w http.ResponseWriter, r *http.Request) {
	name := strings.ToUpper(strings.TrimSpace(mux.Vars(r)["name"]))
	if !domainNameRe.MatchString(name) {
		writeJSON(w, 400, ldapResp{Error: "Invalid domain name"})
		return
	}

	var joined bool
	if err := h.db.QueryRow(`SELECT domain_joined FROM ad_domains WHERE name=$1`, name).Scan(&joined); err != nil {
		writeJSON(w, 404, ldapResp{Error: "Domain not found"})
		return
	}
	if joined {
		writeJSON(w, 409, ldapResp{Error: "Domain is joined - leave the domain before deleting"})
		return
	}

	if _, err := h.db.Exec(`DELETE FROM ad_domains WHERE name=$1`, name); err != nil {
		writeJSON(w, 500, ldapResp{Error: "Delete failed: " + err.Error()})
		return
	}

	audit.Log(audit.AuditLog{
		Level: audit.LevelInfo, Command: "AD_DOMAIN_DELETE",
		User: r.Header.Get("X-User"), Success: true,
		Metadata: map[string]interface{}{"name": name},
	})
	syncIDMAPToNixwriter(h.db)
	writeJSON(w, 200, ldapResp{Success: true})
}

// ============================================================
// POST /api/ldap/domains/:name/join
// ============================================================
// JoinDomain joins an Active Directory domain using the supplied AD admin credentials.
// The join sequence:
//  1. Obtain a Kerberos TGT for the admin user (kinit, password piped via stdin).
//  2. Run `net ads join -k` to join using the TGT.
//  3. Update ad_domains.domain_joined and kinit_principal.
//  4. Sync IDMAP config to nixwriter so the next nixos-rebuild activates winbind.
//
// Returns a job_id; poll GET /api/jobs/:id for result.
func (h *LDAPHandler) JoinDomain(w http.ResponseWriter, r *http.Request) {
	name := strings.ToUpper(strings.TrimSpace(mux.Vars(r)["name"]))
	if !domainNameRe.MatchString(name) {
		respondErrorSimple(w, "Invalid domain name", http.StatusBadRequest)
		return
	}

	var req struct {
		Username string `json:"username"` // AD admin, no domain prefix
		Password string `json:"password"`
		OU       string `json:"ou"` // optional: target OU for computer account
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Username == "" || req.Password == "" {
		respondErrorSimple(w, "username and password required", http.StatusBadRequest)
		return
	}

	var realm, server string
	if err := h.db.QueryRow(`SELECT realm, server FROM ad_domains WHERE name=$1`, name).Scan(&realm, &server); err != nil {
		respondErrorSimple(w, "Domain not found", http.StatusNotFound)
		return
	}

	user := r.Header.Get("X-User")
	password := req.Password
	username := req.Username
	ou := req.OU
	db := h.db

	jobID := jobs.Start("ad_join", func(j *jobs.Job) {
		j.Log("Starting AD join for domain " + name + " (" + realm + ")")

		principal := username + "@" + realm

		// Step 1: kinit to get a TGT for the join operation.
		// Password is piped via stdin, never in command args.
		kinitCmd := exec.Command("kinit", principal)
		kinitCmd.Stdin = strings.NewReader(password + "\n")
		if out, err := kinitCmd.CombinedOutput(); err != nil {
			j.Fail("kinit failed: " + err.Error() + ": " + strings.TrimSpace(string(out)))
			return
		}
		j.Log("Kerberos TGT obtained for " + principal)

		// Step 2: net ads join -k (Kerberos auth, no password in args).
		joinArgs := []string{"ads", "join", "-k"}
		if ou != "" {
			joinArgs = append(joinArgs, "createcomputer="+ou)
		}
		netCmd := exec.Command("net", joinArgs...)
		if out, err := netCmd.CombinedOutput(); err != nil {
			j.Fail("net ads join failed: " + err.Error() + ": " + strings.TrimSpace(string(out)))
			return
		}
		j.Log("Domain join successful")

		// Step 3: update DB.
		_, dbErr := db.Exec(`UPDATE ad_domains SET
			domain_joined=true, domain_joined_at=NOW(),
			kinit_principal=$1, updated_at=NOW()
			WHERE name=$2`, principal, name)
		if dbErr != nil {
			log.Printf("AD JOIN: failed to update DB: %v", dbErr)
		}

		// Step 4: sync IDMAP to nixwriter so next rebuild activates winbind.
		syncIDMAPToNixwriter(db)

		audit.Log(audit.AuditLog{
			Level: audit.LevelInfo, Command: "AD_DOMAIN_JOIN",
			User: user, Success: true,
			Metadata: map[string]interface{}{"domain": name, "realm": realm},
		})

		j.Done(map[string]interface{}{
			"domain": name, "realm": realm, "joined": true,
		})
	})

	respondJSON(w, http.StatusAccepted, map[string]interface{}{"job_id": jobID})
}

// ============================================================
// POST /api/ldap/domains/:name/leave
// ============================================================
// LeaveDomain removes the machine account from Active Directory.
func (h *LDAPHandler) LeaveDomain(w http.ResponseWriter, r *http.Request) {
	name := strings.ToUpper(strings.TrimSpace(mux.Vars(r)["name"]))
	if !domainNameRe.MatchString(name) {
		respondErrorSimple(w, "Invalid domain name", http.StatusBadRequest)
		return
	}

	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Username == "" || req.Password == "" {
		respondErrorSimple(w, "username and password required", http.StatusBadRequest)
		return
	}

	var realm string
	if err := h.db.QueryRow(`SELECT realm FROM ad_domains WHERE name=$1`, name).Scan(&realm); err != nil {
		respondErrorSimple(w, "Domain not found", http.StatusNotFound)
		return
	}

	user := r.Header.Get("X-User")
	password := req.Password
	username := req.Username
	db := h.db

	jobID := jobs.Start("ad_leave", func(j *jobs.Job) {
		j.Log("Leaving domain " + name + " (" + realm + ")")

		principal := username + "@" + realm

		// Obtain a fresh TGT for the leave operation.
		kinitCmd := exec.Command("kinit", principal)
		kinitCmd.Stdin = strings.NewReader(password + "\n")
		if out, err := kinitCmd.CombinedOutput(); err != nil {
			j.Fail("kinit failed: " + err.Error() + ": " + strings.TrimSpace(string(out)))
			return
		}

		leaveCmd := exec.Command("net", "ads", "leave", "-k")
		if out, err := leaveCmd.CombinedOutput(); err != nil {
			j.Fail("net ads leave failed: " + err.Error() + ": " + strings.TrimSpace(string(out)))
			return
		}
		j.Log("Domain leave successful")

		db.Exec(`UPDATE ad_domains SET domain_joined=false, domain_joined_at=NULL, updated_at=NOW() WHERE name=$1`, name)
		syncIDMAPToNixwriter(db)

		audit.Log(audit.AuditLog{
			Level: audit.LevelInfo, Command: "AD_DOMAIN_LEAVE",
			User: user, Success: true,
			Metadata: map[string]interface{}{"domain": name},
		})

		j.Done(map[string]interface{}{"domain": name, "left": true})
	})

	respondJSON(w, http.StatusAccepted, map[string]interface{}{"job_id": jobID})
}

// syncIDMAPToNixwriter reads all enabled ad_domains and updates the nixwriter
// IDMAP state. Called after any domain create/delete/join/leave operation.
func syncIDMAPToNixwriter(db *sql.DB) {
	rows, err := db.Query(`SELECT name, idmap_backend, idmap_low, idmap_high, domain_joined
		FROM ad_domains WHERE enabled=true ORDER BY name`)
	if err != nil {
		log.Printf("AD: syncIDMAP: query failed: %v", err)
		return
	}
	defer rows.Close()

	var domains []nixwriter.IDMAPDomain
	anyJoined := false
	for rows.Next() {
		var d nixwriter.IDMAPDomain
		var joined bool
		if err := rows.Scan(&d.Name, &d.Backend, &d.Low, &d.High, &joined); err != nil {
			continue
		}
		if joined {
			anyJoined = true
		}
		domains = append(domains, d)
	}

	// Always include a catch-all "*" entry with tdb if not already present.
	hasStar := false
	for _, d := range domains {
		if d.Name == "*" {
			hasStar = true
			break
		}
	}
	if !hasStar && len(domains) > 0 {
		domains = append([]nixwriter.IDMAPDomain{{
			Name: "*", Backend: "tdb", Low: 3000, High: 9999,
		}}, domains...)
	}

	w := nixwriter.DefaultWriter()
	if err := w.SetIDMAP(anyJoined, domains); err != nil {
		log.Printf("AD: syncIDMAP: nixwriter update failed: %v", err)
	}
}

