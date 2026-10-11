package handlers

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/mux"

	"dplaned/internal/audit"
	"dplaned/internal/gitops"
	"dplaned/internal/ldap"
	"dplaned/internal/nixwriter"
	"dplaned/internal/secrets"
	"dplaned/internal/security"
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
	Success bool   `json:"success"`
	Data    any    `json:"data,omitempty"`
	Error   string `json:"error,omitempty"`
	Code    string `json:"code,omitempty"`
	Guide   string `json:"guide,omitempty"`
	Warning string `json:"warning,omitempty"`
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
	if req.Timeout <= 0 {
		req.Timeout = 10
	}
	if req.SyncInterval <= 0 {
		req.SyncInterval = 3600
	}
	if req.UserIDAttr == "" {
		req.UserIDAttr = "sAMAccountName"
	}
	if req.UserNameAttr == "" {
		req.UserNameAttr = "displayName"
	}
	if req.UserEmailAttr == "" {
		req.UserEmailAttr = "mail"
	}
	if req.GroupFilter == "" {
		req.GroupFilter = "(&(objectClass=group)(member={user_dn}))"
	}
	if req.GroupMemberAttr == "" {
		req.GroupMemberAttr = "member"
	}
	if req.DefaultRole == "" {
		req.DefaultRole = "user"
	}

	// If password is empty keep the existing sealed value; otherwise seal the new one.
	bindPwd := ""
	if req.BindPassword != "" {
		sealed, sealErr := secrets.Seal(req.BindPassword)
		if sealErr != nil {
			log.Printf("LDAP: failed to seal bind_password: %v", sealErr)
			writeJSON(w, 500, ldapResp{Error: "Failed to encrypt password"})
			return
		}
		bindPwd = sealed
	} else {
		if err := h.db.QueryRow("SELECT COALESCE(bind_password,'') FROM ldap_config WHERE id=1").Scan(&bindPwd); err != nil {
			log.Printf("LDAP: failed to read existing password: %v", err)
		}
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
		Metadata: map[string]any{"enabled": req.Enabled, "server": req.Server},
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
	writeJSON(w, 200, ldapResp{Success: true, Data: map[string]any{"message": msg, "duration_ms": ms}})
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
	if tAt.Valid {
		s.LastTestAt = tAt.String
	}
	if tMsg.Valid {
		s.LastTestMsg = tMsg.String
	}
	if sAt.Valid {
		s.LastSyncAt = sAt.String
	}
	if sMsg.Valid {
		s.LastSyncMsg = sMsg.String
	}
	if sCnt.Valid {
		s.LastSyncCount = int(sCnt.Int64)
	}
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
	res, err := h.syncDirectory(cfg, "manual")
	audit.Log(audit.AuditLog{Level: audit.LevelInfo, Command: "LDAP_SYNC_MANUAL", User: r.Header.Get("X-User"), Success: err == nil && len(res.Errors) == 0})
	if err != nil {
		writeJSON(w, 200, ldapResp{Success: false, Error: err.Error()})
		return
	}
	writeJSON(w, 200, ldapResp{Success: len(res.Errors) == 0, Data: map[string]any{
		"message":       res.message,
		"duration_ms":   res.ms,
		"users_found":   res.UsersFound,
		"users_created": res.UsersCreated,
		"users_updated": res.UsersUpdated,
		"users_skipped": res.UsersSkipped,
		"errors":        res.Errors,
	}})
}

type directorySync struct {
	*ldap.SyncResult
	message string
	ms      int
}

// ldapSyncMu: one sync at a time (manual and scheduled).
var ldapSyncMu sync.Mutex

// syncDirectory fetches all users of the directory and brings the local
// accounts in line with them. syncType ("manual", "scheduled") goes into
// the sync log.
func (h *LDAPHandler) syncDirectory(cfg *ldap.Config, syncType string) (*directorySync, error) {
	ldapSyncMu.Lock()
	defer ldapSyncMu.Unlock()
	fail := func(what string, err error) (*directorySync, error) {
		h.logSync(syncType, false, 0, 0, 0, 0, err.Error(), 0)
		if _, dbErr := h.db.Exec(`UPDATE ldap_config SET last_sync_at=NOW(), last_sync_ok=0, last_sync_msg=$1 WHERE id=1`, what+": "+err.Error()); dbErr != nil {
			log.Printf("LDAP: failed to update sync status: %v", dbErr)
		}
		return nil, fmt.Errorf("%s: %w", what, err)
	}

	client, err := ldap.NewClient(cfg)
	if err != nil {
		return fail("Client error", err)
	}
	start := time.Now()
	if err := client.Connect(); err != nil {
		return fail("Connect failed", err)
	}
	defer client.Close()
	if err := client.Bind(); err != nil {
		return fail("Bind failed", err)
	}
	syncRes, users, err := client.SyncAll()
	if err != nil {
		return fail("Sync search failed", err)
	}
	mappings, err := h.loadGroupMappings()
	if err != nil {
		return fail("Loading group mappings failed", err)
	}
	for _, u := range users {
		created, err := h.upsertDirectoryUser(u, mappings, cfg.DefaultRole)
		switch {
		case err != nil:
			syncRes.Errors = append(syncRes.Errors, u.Username+": "+err.Error())
			syncRes.UsersSkipped++
		case created:
			syncRes.UsersCreated++
		default:
			syncRes.UsersUpdated++
		}
	}

	res := &directorySync{SyncResult: syncRes, ms: int(time.Since(start).Milliseconds())}
	success := len(syncRes.Errors) == 0
	res.message = fmt.Sprintf("Sync completed: found=%d created=%d updated=%d skipped=%d",
		syncRes.UsersFound, syncRes.UsersCreated, syncRes.UsersUpdated, syncRes.UsersSkipped)
	h.logSync(syncType, success, syncRes.UsersFound, syncRes.UsersCreated, syncRes.UsersUpdated, syncRes.UsersSkipped, res.message, res.ms)
	okInt := 0
	if success {
		okInt = 1
	}
	if _, dbErr := h.db.Exec(`UPDATE ldap_config SET last_sync_at=NOW(), last_sync_ok=$1, last_sync_count=$2, last_sync_msg=$3 WHERE id=1`,
		okInt, syncRes.UsersFound, res.message); dbErr != nil {
		log.Printf("LDAP: failed to update sync status: %v", dbErr)
	}
	return res, nil
}

// upsertDirectoryUser creates or updates the local account of a directory
// user (source='ldap'). Such accounts have no local password: they sign in
// with a bind against the directory. Permissions come from RBAC roles: the
// roles of the user's mapped directory groups, else the default role.
func (h *LDAPHandler) upsertDirectoryUser(u *ldap.User, mappings map[string][]string, defaultRole string) (created bool, err error) {
	roleNames := mappedRoles(u.Groups, mappings)
	if len(roleNames) == 0 && defaultRole != "" {
		roleNames = []string{defaultRole}
	}
	role := "user" // users.role: the rank used for account management
	for _, n := range roleNames {
		if n == "admin" {
			role = "admin"
		}
	}

	var source string
	switch err := h.db.QueryRow(`SELECT COALESCE(source,'local') FROM users WHERE username=$1`, u.Username).Scan(&source); {
	case err == sql.ErrNoRows:
		if _, err := h.db.Exec(
			`INSERT INTO users (username, email, display_name, role, source, password_hash, updated_at)
			 VALUES ($1, $2, $3, $4, 'ldap', '', NOW())`,
			u.Username, u.Email, u.FullName, role,
		); err != nil {
			return false, fmt.Errorf("insert: %w", err)
		}
		created = true
	case err != nil:
		return false, err
	case source != "ldap":
		// A local account of the same name is never taken over by the directory.
		return false, fmt.Errorf("a local account of that name exists")
	default:
		if _, err := h.db.Exec(
			`UPDATE users SET email=$1, display_name=$2, role=$3, updated_at=NOW()
			 WHERE username=$4 AND source='ldap'`,
			u.Email, u.FullName, role, u.Username,
		); err != nil {
			return false, fmt.Errorf("update: %w", err)
		}
	}

	// Without a successful group lookup the roles are left as they are:
	// a directory hiccup must not strip everyone's access.
	if u.GroupsKnown || created {
		if err := h.applyLDAPRoles(u.Username, roleNames); err != nil {
			return created, fmt.Errorf("roles: %w", err)
		}
	}
	return created, nil
}

// ProvisionDirectoryUserAtLogin is "just-in-time provisioning": somebody
// who is in the directory but has no account here yet signs in for the
// first time. With the option on and the password accepted by the
// directory, the account is created (roles from the group mappings, else
// the default role) and the sign-in goes on as for any directory user.
// It reports whether an account was created.
func ProvisionDirectoryUserAtLogin(db *sql.DB, username, password string) bool {
	h := NewLDAPHandler(db)
	cfg, err := h.loadLDAPConfig()
	if err != nil || !cfg.Enabled || !cfg.JITProvisioning || cfg.Server == "" {
		return false
	}
	if !ldapCBAllow() {
		return false
	}
	client, err := ldap.NewClient(cfg)
	if err != nil {
		return false
	}
	u, err := client.Authenticate(username, password)
	if err != nil {
		if isLDAPConnError(err) {
			ldapCBFailure()
		} else {
			ldapCBSuccess()
		}
		return false
	}
	ldapCBSuccess()
	// The name the directory knows the user by must be the one typed: the
	// account is looked up by it afterwards.
	if !strings.EqualFold(u.Username, username) {
		return false
	}
	u.Username = username
	u.GroupsKnown = true
	mappings, err := h.loadGroupMappings()
	if err != nil {
		log.Printf("LDAP JIT: group mappings: %v", err)
		return false
	}
	created, err := h.upsertDirectoryUser(u, mappings, cfg.DefaultRole)
	if err != nil {
		log.Printf("LDAP JIT: %s: %v", username, err)
		return false
	}
	if created {
		log.Printf("LDAP JIT: account created for %s at first sign-in", username)
		audit.Log(audit.AuditLog{Level: audit.LevelInfo, Command: "LDAP_JIT_PROVISION", User: username, Success: true})
	}
	return created
}

// StartDirectorySync runs the scheduled sync: every sync_interval seconds
// (0: never) while the directory is enabled. Accounts of new directory
// users appear, names, addresses and roles follow the directory.
func StartDirectorySync(ctx context.Context, db *sql.DB) {
	h := NewLDAPHandler(db)
	go func() {
		t := time.NewTicker(time.Minute)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
			cfg, err := h.loadLDAPConfig()
			if err != nil || !cfg.Enabled || cfg.Server == "" || cfg.SyncInterval <= 0 {
				continue
			}
			var due bool
			if err := db.QueryRow(`SELECT last_sync_at IS NULL OR last_sync_at < NOW() - make_interval(secs => $1) FROM ldap_config WHERE id=1`,
				cfg.SyncInterval).Scan(&due); err != nil || !due {
				continue
			}
			if res, err := h.syncDirectory(cfg, "scheduled"); err != nil {
				log.Printf("LDAP: scheduled sync: %v", err)
			} else {
				log.Printf("LDAP: scheduled sync: %s", res.message)
			}
		}
	}()
}

// loadGroupMappings returns directory group (lower case) -> RBAC role names.
func (h *LDAPHandler) loadGroupMappings() (map[string][]string, error) {
	rows, err := h.db.Query(`SELECT ldap_group, role_name FROM ldap_group_mappings WHERE role_name <> ''`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]string{}
	for rows.Next() {
		var g, r string
		if err := rows.Scan(&g, &r); err != nil {
			return nil, err
		}
		out[strings.ToLower(g)] = append(out[strings.ToLower(g)], r)
	}
	return out, rows.Err()
}

// mappedRoles returns the RBAC roles of a user's directory groups.
func mappedRoles(groups []string, mappings map[string][]string) []string {
	seen := map[string]bool{}
	var out []string
	for _, g := range groups {
		for _, r := range mappings[strings.ToLower(g)] {
			if !seen[r] {
				seen[r] = true
				out = append(out, r)
			}
		}
	}
	return out
}

// applyLDAPRoles makes the user's LDAP-granted RBAC roles exactly roleNames:
// roles of groups the user left are revoked; roles granted by an admin
// (granted_by other than 'ldap') are kept.
func (h *LDAPHandler) applyLDAPRoles(username string, roleNames []string) error {
	tx, err := h.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck
	var userID int64
	if err := tx.QueryRow(`SELECT id FROM users WHERE username = $1 AND source = 'ldap'`, username).Scan(&userID); err != nil {
		return err
	}
	var ids []int64
	for _, n := range roleNames {
		var id int64
		if err := tx.QueryRow(`SELECT id FROM roles WHERE name = $1`, n).Scan(&id); err != nil {
			return fmt.Errorf("unknown role %q", n)
		}
		ids = append(ids, id)
	}
	if _, err := tx.Exec(`DELETE FROM user_roles WHERE user_id = $1 AND granted_by = 'ldap' AND NOT (role_id = ANY($2))`,
		userID, pgInt64Array(ids)); err != nil {
		return err
	}
	for _, id := range ids {
		if _, err := tx.Exec(`INSERT INTO user_roles (user_id, role_id, granted_by) VALUES ($1, $2, 'ldap') ON CONFLICT (user_id, role_id) DO NOTHING`,
			userID, id); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	security.InvalidateUserPermissions(int(userID))
	return nil
}

// pgInt64Array renders ids as a PostgreSQL array literal ("{1,2}").
func pgInt64Array(ids []int64) string {
	parts := make([]string, len(ids))
	for i, id := range ids {
		parts[i] = strconv.FormatInt(id, 10)
	}
	return "{" + strings.Join(parts, ",") + "}"
}

// ============================================================
// POST /api/ldap/search-user
// ============================================================
func (h *LDAPHandler) SearchUser(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Username string `json:"username"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || strings.TrimSpace(req.Username) == "" {
		writeJSON(w, 400, ldapResp{Error: "Username required"})
		return
	}
	req.Username = strings.TrimSpace(req.Username)

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
	user, err := client.LookupUser(req.Username)
	if err != nil {
		writeJSON(w, 200, ldapResp{Success: false, Error: err.Error()})
		return
	}

	// The roles a login of this user would get from the group mappings.
	roles := []string{}
	seen := map[string]bool{}
	mappings, err := h.loadGroupMappings()
	if err != nil {
		writeJSON(w, 500, ldapResp{Error: "Group mappings: " + err.Error()})
		return
	}
	for _, g := range user.Groups {
		for _, role := range mappings[strings.ToLower(g)] {
			if !seen[role] {
				seen[role] = true
				roles = append(roles, role)
			}
		}
	}
	if len(roles) == 0 && cfg.DefaultRole != "" {
		roles = append(roles, cfg.DefaultRole+" (default)")
	}
	groups := user.Groups
	if groups == nil {
		groups = []string{}
	}
	writeJSON(w, 200, ldapResp{Success: true, Data: map[string]any{
		"dn":        user.DN,
		"username":  user.Username,
		"full_name": user.FullName,
		"email":     user.Email,
		"groups":    groups,
		"roles":     roles,
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
		if err := rows.Scan(&m.ID, &m.LDAPGroup, &m.RoleName, &rid, &m.CreatedAt); err != nil {
			continue
		}
		if rid.Valid {
			v := int(rid.Int64)
			m.RoleID = &v
		}
		list = append(list, m)
	}
	if err := rows.Err(); err != nil {
		log.Printf("WARN: ldap_group_mappings rows: %v", err)
	}
	if list == nil {
		list = []mapping{}
	}
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
	// The mapped role is an RBAC role (what grants permissions).
	var roleID int64
	if err := h.db.QueryRow(`SELECT id FROM roles WHERE name = $1`, req.RoleName).Scan(&roleID); err != nil {
		writeJSON(w, 400, ldapResp{Error: "Unknown role: " + req.RoleName})
		return
	}

	var id int64
	err := h.db.QueryRow("INSERT INTO ldap_group_mappings (ldap_group, role_name, role_id) VALUES ($1, $2, $3) RETURNING id",
		req.LDAPGroup, req.RoleName, roleID).Scan(&id)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			writeJSON(w, 409, ldapResp{Error: "Mapping already exists"})
			return
		}
		writeJSON(w, 500, ldapResp{Error: "Insert failed: " + err.Error()})
		return
	}
	audit.Log(audit.AuditLog{Level: audit.LevelInfo, Command: "LDAP_MAPPING_ADD", User: r.Header.Get("X-User"), Success: true,
		Metadata: map[string]any{"ldap_group": req.LDAPGroup, "role": req.RoleName}})
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
	if l, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && l > 0 && l <= 100 {
		limit = l
	}

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
		if err := rows.Scan(&e.ID, &e.SyncType, &e.Success, &e.UsersSynced, &e.UsersCreated, &e.UsersUpdated, &e.UsersDisabled, &e.ErrorMsg, &e.DurationMs, &e.CreatedAt); err != nil {
			log.Printf("LDAP SYNC LOG SCAN ERROR: %v", err)
			continue
		}
		list = append(list, e)
	}
	if err := rows.Err(); err != nil {
		log.Printf("LDAP SYNC LOG ROWS ERROR: %v", err)
	}
	if list == nil {
		list = []entry{}
	}
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

	plainPwd, openErr := secrets.Open(bindPwd)
	if openErr != nil {
		return nil, fmt.Errorf("decrypting bind_password: %w", openErr)
	}

	return &ldap.Config{
		Enabled:            enabled == 1,
		Server:             server,
		Port:               port,
		UseTLS:             useTLS == 1,
		BindDN:             bindDN,
		BindPassword:       plainPwd,
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
		SyncInterval:       syncInterval,
	}, nil
}

func (h *LDAPHandler) updateTest(ok bool, msg string) {
	v := 0
	if ok {
		v = 1
	}
	if _, err := h.db.Exec("UPDATE ldap_config SET last_test_at=NOW(), last_test_ok=$1, last_test_msg=$2 WHERE id=1", v, msg); err != nil {
		log.Printf("LDAP: failed to update test status: %v", err)
	}
}

func (h *LDAPHandler) logSync(syncType string, success bool, synced, created, updated, disabled int, errMsg string, ms int) {
	s := 0
	if success {
		s = 1
	}
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
	if err := rows.Err(); err != nil {
		writeJSON(w, 500, ldapResp{Error: "row iteration error: " + err.Error()})
		return
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

	// Each domain needs its own UID/GID range: overlapping ranges give users
	// of two domains the same IDs (and each other's files).
	var clash string
	if err := h.db.QueryRow(`SELECT name FROM ad_domains WHERE name <> $1 AND idmap_low <= $3 AND idmap_high >= $2 LIMIT 1`,
		req.Name, req.IDMAPLow, req.IDMAPHigh).Scan(&clash); err == nil {
		writeJSON(w, 409, ldapResp{Error: fmt.Sprintf("ID range %d-%d overlaps the range of domain %s", req.IDMAPLow, req.IDMAPHigh, clash)})
		return
	}
	var exists int
	if err := h.db.QueryRow(`SELECT 1 FROM ad_domains WHERE name=$1`, req.Name).Scan(&exists); err == nil {
		writeJSON(w, 409, ldapResp{Error: "Domain " + req.Name + " is already registered"})
		return
	}

	sealedDomainPwd, sealErr := secrets.Seal(req.BindPassword)
	if sealErr != nil {
		log.Printf("LDAP: failed to seal ad_domains bind_password: %v", sealErr)
		writeJSON(w, 500, ldapResp{Error: "Failed to encrypt password"})
		return
	}

	_, err := h.db.Exec(`INSERT INTO ad_domains
		(name, realm, server, bind_dn, bind_password, idmap_backend, idmap_low, idmap_high)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`,
		req.Name, req.Realm, req.Server, req.BindDN, sealedDomainPwd,
		req.IDMAPBackend, req.IDMAPLow, req.IDMAPHigh)
	if err != nil {
		writeJSON(w, 500, ldapResp{Error: "Failed to create domain: " + err.Error()})
		return
	}

	audit.Log(audit.AuditLog{
		Level: audit.LevelInfo, Command: "AD_DOMAIN_CREATE",
		User: r.Header.Get("X-User"), Success: true,
		Metadata: map[string]any{"name": req.Name, "realm": req.Realm},
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
		Metadata: map[string]any{"name": name},
	})
	syncIDMAPToNixwriter(h.db)
	writeJSON(w, 200, ldapResp{Success: true})
}

// ============================================================
// GET /api/ldap/domains/:name/status
// ============================================================
// WinbindStatus returns live winbind health for a joined domain:
// trust check (wbinfo -t), DC reachability (wbinfo --online-status),
// and sample user/group lists to verify NSS resolution is working.
func (h *LDAPHandler) WinbindStatus(w http.ResponseWriter, r *http.Request) {
	name := strings.ToUpper(strings.TrimSpace(mux.Vars(r)["name"]))
	if !domainNameRe.MatchString(name) {
		respondErrorSimple(w, "Invalid domain name", http.StatusBadRequest)
		return
	}
	var joined bool
	if err := h.db.QueryRow(`SELECT domain_joined FROM ad_domains WHERE name=$1`, name).Scan(&joined); err != nil {
		respondErrorSimple(w, "Domain not found", http.StatusNotFound)
		return
	}
	if !joined {
		respondJSON(w, http.StatusOK, map[string]any{
			"joined":  false,
			"message": "Domain is registered but not joined. Use POST /api/ldap/domains/" + name + "/join first.",
		})
		return
	}

	result := map[string]any{"joined": true, "domain": name}

	// wbinfo -p: ping winbind daemon
	if pingOut, err := executeCommandWithTimeout(TimeoutFast, "wbinfo", []string{"-p"}); err == nil {
		result["winbind_ping"] = strings.TrimSpace(pingOut)
	} else {
		result["winbind_ping_error"] = "winbind daemon not responding: " + err.Error()
	}

	// wbinfo -t: machine account trust check
	if trustOut, err := executeCommandWithTimeout(30*time.Second, "wbinfo", []string{"-t"}); err == nil {
		result["trust_check"] = strings.TrimSpace(trustOut)
		result["trust_ok"] = true
	} else {
		result["trust_check_error"] = "Trust check failed (wbinfo -t): " + err.Error()
		result["trust_ok"] = false
	}

	// wbinfo --online-status: DC reachability
	if onlineOut, err := executeCommandWithTimeout(TimeoutFast, "wbinfo", []string{"--online-status"}); err == nil {
		result["online_status"] = strings.TrimSpace(onlineOut)
	} else {
		result["online_status_error"] = err.Error()
	}

	// Sample users (first 10) - verifies NSS resolution is working
	if usersOut, err := executeCommandWithTimeout(TimeoutFast, "wbinfo", []string{"-u"}); err == nil {
		lines := strings.Split(strings.TrimSpace(usersOut), "\n")
		if len(lines) > 10 {
			lines = lines[:10]
		}
		result["sample_users"] = lines
		result["user_count_note"] = "showing first 10 of " + fmt.Sprintf("%d", len(strings.Split(strings.TrimSpace(usersOut), "\n"))) + " users"
	} else {
		result["sample_users_error"] = "wbinfo -u failed: " + err.Error() + " (check winbind is running)"
	}

	// Sample groups (first 10)
	if groupsOut, err := executeCommandWithTimeout(TimeoutFast, "wbinfo", []string{"-g"}); err == nil {
		lines := strings.Split(strings.TrimSpace(groupsOut), "\n")
		if len(lines) > 10 {
			lines = lines[:10]
		}
		result["sample_groups"] = lines
	} else {
		result["sample_groups_error"] = "wbinfo -g failed: " + err.Error()
	}

	respondJSON(w, http.StatusOK, result)
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
	if err := rows.Err(); err != nil {
		log.Printf("AD: syncIDMAP: rows error: %v", err)
		return
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
