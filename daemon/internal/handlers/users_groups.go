package handlers

import (
	"database/sql"
	"dplaned/internal/gitops"
	"dplaned/internal/middleware"
	"dplaned/internal/security"
	"dplaned/internal/scram"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os/user"
	"regexp"
	"strconv"
	"strings"

	"github.com/gorilla/mux"
	"golang.org/x/crypto/bcrypt"
)

// UserGroupHandler handles user and group CRUD
type UserGroupHandler struct {
	db *sql.DB
}

func NewUserGroupHandler(db *sql.DB) *UserGroupHandler {
	return &UserGroupHandler{db: db}
}

// ─── USERS ─────────────────────────────────────────────────

// HandleUsers - GET: list users, POST: create/update/delete users
func (h *UserGroupHandler) HandleUsers(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		h.listUsers(w, r)
	case http.MethodPost:
		// Use middleware manually for POST mutations (#17)
		middleware.RequirePermission("users", "write")(http.HandlerFunc(h.userAction)).ServeHTTP(w, r)
	default:
		respondErrorSimple(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func (h *UserGroupHandler) listUsers(w http.ResponseWriter, r *http.Request) {
	// Support ?id= for single-user lookup
	if idStr := r.URL.Query().Get("id"); idStr != "" {
		var id, active int
		var username, email, role, createdAt string
		err := h.db.QueryRow(
			`SELECT id, username, COALESCE(email,''), COALESCE(role,'user'), active, COALESCE(TO_CHAR(created_at, 'YYYY-MM-DD"T"HH24:MI:SS"Z"'), '') FROM users WHERE id = $1`, idStr,
		).Scan(&id, &username, &email, &role, &active, &createdAt)
		if err != nil {
			respondErrorSimple(w, "User not found", http.StatusNotFound)
			return
		}
		respondJSON(w, http.StatusOK, map[string]any{
			"success": true,
			"user": map[string]any{
				"id":         id,
				"username":   username,
				"email":      email,
				"role":       role,
				"active":     active == 1,
				"created_at": createdAt,
			},
		})
		return
	}

	rows, err := h.db.Query(`SELECT id, username, COALESCE(email,''), COALESCE(role,'user'), active, COALESCE(TO_CHAR(created_at, 'YYYY-MM-DD"T"HH24:MI:SS"Z"'), '') FROM users ORDER BY id`)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "Failed to list users", err)
		return
	}
	defer rows.Close()

	var users []map[string]any
	for rows.Next() {
		var id, active int
		var username, email, role, createdAt string
		if err := rows.Scan(&id, &username, &email, &role, &active, &createdAt); err != nil {
			log.Printf("USER LIST SCAN ERROR: %v", err)
			continue
		}
		users = append(users, map[string]any{
			"id":         id,
			"username":   username,
			"email":      email,
			"role":       role,
			"active":     active == 1,
			"created_at": createdAt,
		})
	}
	if err := rows.Err(); err != nil {
		respondError(w, http.StatusInternalServerError, "Error iterating users", err)
		return
	}

	if users == nil {
		users = []map[string]any{}
	}

	respondJSON(w, http.StatusOK, map[string]any{
		"success": true,
		"users":   users,
	})
}

type userActionRequest struct {
	Action          string `json:"action"` // create, update, delete
	ID              int    `json:"id"`
	Username        string `json:"username"`
	Email           string `json:"email"`
	Password        string `json:"password"`
	Role            string `json:"role"`
	Active          *bool  `json:"active"`
	ConfirmPassword string `json:"confirm_password"` // Required for sensitive ops (#17)
}

// userRoles are the values of users.role (permissions themselves come from
// the RBAC roles in user_roles).
// Each is also the built-in RBAC role the account gets (readonly = viewer).
var userRoles = map[string]int{"admin": 100, "operator": 50, "user": 10, "viewer": 5, "readonly": 5}

var groupNameRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_.-]{0,31}$`)

// mayManage reports whether requester may change target: admins manage
// everyone; others only accounts of a lower rank (and never admins).
func mayManage(requesterRole, targetRole string) bool {
	if requesterRole == "admin" {
		return true
	}
	return targetRole != "admin" && userRoles[requesterRole] > userRoles[targetRole]
}

func (h *UserGroupHandler) userAction(w http.ResponseWriter, r *http.Request) {
	var req userActionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondErrorSimple(w, "Invalid request", http.StatusBadRequest)
		return
	}

	// ─── SECURITY & HIERRARCHY CHECKS (#17) ───────────────────────────

	// 1. Get Requester Info from middleware context
	u := r.Context().Value(middleware.UserContextKey)
	if u == nil {
		respondErrorSimple(w, "Unauthorized (No user context)", http.StatusUnauthorized)
		return
	}
	requester := u.(*middleware.User)

	// Fetch requester's full details (role and password hash)
	var reqRole, reqPassHash string
	err := h.db.QueryRow(`SELECT role, password_hash FROM users WHERE username = $1`, requester.Username).Scan(&reqRole, &reqPassHash)
	if err != nil {
		respondErrorSimple(w, "Error verifying requester", http.StatusInternalServerError)
		return
	}

	if req.Role != "" {
		if _, ok := userRoles[req.Role]; !ok {
			respondErrorSimple(w, "Unknown role "+req.Role+" (use admin, operator, user or viewer)", http.StatusBadRequest)
			return
		}
	}

	// 3. Sensitive Action Authorization
	// Sensitive if updating role, password, active status, OR deleting
	isSensitive := req.Action == "delete" || req.Role != "" || req.Password != "" || req.Active != nil

	if isSensitive {
		// A. Validate Current Password (ConfirmPassword)
		if req.ConfirmPassword == "" {
			respondErrorSimple(w, "Current password required to authorize this change", http.StatusBadRequest)
			return
		}
		if err := bcrypt.CompareHashAndPassword([]byte(reqPassHash), []byte(req.ConfirmPassword)); err != nil {
			respondErrorSimple(w, "Invalid current password", http.StatusForbidden)
			return
		}

		// B. Hierarchy Enforcement. Creating counts too: a non-admin may not
		// create an account of equal or higher rank (e.g. a new admin).
		if req.Action == "create" {
			newRole := req.Role
			if newRole == "" {
				newRole = "user"
			}
			if !mayManage(reqRole, newRole) {
				respondErrorSimple(w, fmt.Sprintf("Higher privileges required to create %s accounts", newRole), http.StatusForbidden)
				return
			}
		} else {
			var targetRole string
			var targetID int
			if err := h.db.QueryRow(`SELECT id, role FROM users WHERE id = $1`, req.ID).Scan(&targetID, &targetRole); err != nil {
				respondErrorSimple(w, "Failed to fetch target user", http.StatusInternalServerError)
				return
			}

			// Editing someone else?
			if targetID != int(requester.ID) {
				if targetRole == "admin" && reqRole != "admin" {
					respondErrorSimple(w, "Only admins can manage other admin accounts", http.StatusForbidden)
					return
				}
				if !mayManage(reqRole, targetRole) {
					respondErrorSimple(w, fmt.Sprintf("Higher privileges required to manage %s accounts", targetRole), http.StatusForbidden)
					return
				}
			}
			// Nobody raises their own rank.
			if targetID == int(requester.ID) && req.Role != "" && reqRole != "admin" && userRoles[req.Role] > userRoles[reqRole] {
				respondErrorSimple(w, "You cannot raise your own role", http.StatusForbidden)
				return
			}
		}
	}

	switch req.Action {
	case "create":
		h.createUser(w, req)
	case "update":
		h.updateUser(w, req)
	case "delete":
		h.deleteUser(w, req)
	default:
		respondErrorSimple(w, "Unknown action: "+req.Action, http.StatusBadRequest)
	}
}

func (h *UserGroupHandler) createUser(w http.ResponseWriter, req userActionRequest) {
	if req.Username == "" || req.Password == "" {
		respondErrorSimple(w, "Username and password required", http.StatusBadRequest)
		return
	}

	// Trim whitespace to catch accidental copy-paste spaces/newlines
	req.Password = strings.TrimSpace(req.Password)
	if ok, msg := validatePasswordStrength(req.Password); !ok {
		respondErrorSimple(w, msg, http.StatusBadRequest)
		return
	}

	if !isAlphanumericDash(req.Username) || len(req.Username) > 64 {
		respondErrorSimple(w, "Invalid username format", http.StatusBadRequest)
		return
	}

	// Check for duplicate
	var count int
	h.db.QueryRow(`SELECT COUNT(*) FROM users WHERE username = $1`, req.Username).Scan(&count)
	if count > 0 {
		respondErrorSimple(w, "Username already exists", http.StatusConflict)
		return
	}

	// Hash password with bcrypt and derive SCRAM-SHA-512 keys.
	hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
	if err != nil {
		respondErrorSimple(w, "Internal error", http.StatusInternalServerError)
		return
	}
	scramKeys, err := scram.Derive(req.Password)
	if err != nil {
		respondErrorSimple(w, "Internal error", http.StatusInternalServerError)
		return
	}

	role := req.Role
	if role == "" {
		role = "user"
	}

	var id int64
	err = h.db.QueryRow(
		`INSERT INTO users (username, password_hash, scram_salt, scram_iterations, scram_stored_key, scram_server_key, email, role, active)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, 1) RETURNING id`,
		req.Username, string(hash),
		scram.EncodeBase64(scramKeys.Salt), scramKeys.Iterations,
		scram.EncodeBase64(scramKeys.StoredKey), scram.EncodeBase64(scramKeys.ServerKey),
		req.Email, role,
	).Scan(&id)
	if err != nil {
		respondErrorSimple(w, "Failed to create user", http.StatusInternalServerError)
		log.Printf("USER CREATE ERROR: %v", err)
		return
	}
	// The permissions come from the RBAC role of the same name.
	if err := security.SetPrimaryRole(int(id), "", role); err != nil {
		h.db.Exec(`DELETE FROM users WHERE id = $1`, id) //nolint:errcheck
		respondErrorSimple(w, "Failed to assign the role: "+err.Error(), http.StatusInternalServerError)
		return
	}

	respondJSON(w, http.StatusOK, map[string]any{
		"success": true,
		"id":      id,
		"message": fmt.Sprintf("User %s created", req.Username),
	})

	// GITOPS HOOK: write state back to git
	gitops.CommitAllAsync(h.db)
}

func (h *UserGroupHandler) updateUser(w http.ResponseWriter, req userActionRequest) {
	if req.ID == 0 {
		respondErrorSimple(w, "User ID required", http.StatusBadRequest)
		return
	}

	// Build dynamic update
	if req.Email != "" {
		_, err := h.db.Exec(`UPDATE users SET email = $1 WHERE id = $2`, req.Email, req.ID)
		if err != nil {
			respondErrorSimple(w, "Failed to update email", http.StatusInternalServerError)
			log.Printf("USER UPDATE EMAIL ERROR: %v", err)
			return
		}
	}
	if req.Role != "" {
		var oldRole string
		_ = h.db.QueryRow(`SELECT COALESCE(role, '') FROM users WHERE id = $1`, req.ID).Scan(&oldRole)
		_, err := h.db.Exec(`UPDATE users SET role = $1 WHERE id = $2`, req.Role, req.ID)
		if err != nil {
			respondErrorSimple(w, "Failed to update role", http.StatusInternalServerError)
			log.Printf("USER UPDATE ROLE ERROR: %v", err)
			return
		}
		if err := security.SetPrimaryRole(req.ID, oldRole, req.Role); err != nil {
			respondErrorSimple(w, "Failed to assign the role: "+err.Error(), http.StatusInternalServerError)
			return
		}
	}
	if req.Active != nil {
		if !*req.Active {
			// Ensure we don't deactivate the last admin
			var currentRole string
			var adminCount int
			if err := h.db.QueryRow(`SELECT role FROM users WHERE id = $1`, req.ID).Scan(&currentRole); err != nil {
				respondErrorSimple(w, "Failed to fetch user role", http.StatusInternalServerError)
				return
			}
			if err := h.db.QueryRow(`SELECT COUNT(*) FROM users WHERE role = 'admin' AND active = 1`).Scan(&adminCount); err != nil {
				respondErrorSimple(w, "Failed to check admin count", http.StatusInternalServerError)
				return
			}
			if currentRole == "admin" && adminCount <= 1 {
				respondErrorSimple(w, "Cannot deactivate the last remaining active admin account", http.StatusForbidden)
				return
			}
		}
		activeVal := 0
		if *req.Active {
			activeVal = 1
		}
		_, err := h.db.Exec(`UPDATE users SET active = $1 WHERE id = $2`, activeVal, req.ID)
		if err != nil {
			respondErrorSimple(w, "Failed to update active status", http.StatusInternalServerError)
			log.Printf("USER UPDATE ACTIVE ERROR: %v", err)
			return
		}
	}
	if req.Password != "" {
		req.Password = strings.TrimSpace(req.Password)
		if ok, msg := validatePasswordStrength(req.Password); !ok {
			respondErrorSimple(w, msg, http.StatusBadRequest)
			return
		}
		hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
		if err != nil {
			respondErrorSimple(w, "Failed to hash password", http.StatusInternalServerError)
			return
		}
		scramKeys, err := scram.Derive(req.Password)
		if err != nil {
			respondErrorSimple(w, "Internal error", http.StatusInternalServerError)
			return
		}
		_, err = h.db.Exec(
			`UPDATE users SET password_hash = $1, scram_salt = $2, scram_iterations = $3,
			 scram_stored_key = $4, scram_server_key = $5 WHERE id = $6`,
			string(hash),
			scram.EncodeBase64(scramKeys.Salt), scramKeys.Iterations,
			scram.EncodeBase64(scramKeys.StoredKey), scram.EncodeBase64(scramKeys.ServerKey),
			req.ID,
		)
		if err != nil {
			respondErrorSimple(w, "Failed to update password", http.StatusInternalServerError)
			log.Printf("USER UPDATE PASSWORD ERROR: %v", err)
			return
		}
	}

	respondJSON(w, http.StatusOK, map[string]any{
		"success": true,
		"message": "User updated",
	})

	// GITOPS HOOK: write state back to git
	gitops.CommitAllAsync(h.db)
}

func (h *UserGroupHandler) deleteUser(w http.ResponseWriter, req userActionRequest) {
	if req.ID == 0 {
		respondErrorSimple(w, "User ID required", http.StatusBadRequest)
		return
	}

	var targetUsername, targetRole string
	if err := h.db.QueryRow(`SELECT username, role FROM users WHERE id = $1`, req.ID).Scan(&targetUsername, &targetRole); err != nil {
		respondErrorSimple(w, "User not found", http.StatusNotFound)
		return
	}

	// 1. HARD BLOCK for critical low-level system accounts (#17)
	if targetUsername == "root" || targetUsername == "admin" || targetUsername == "dplaneos" {
		respondErrorSimple(w, "Cannot delete protected system service account: "+targetUsername, http.StatusForbidden)
		return
	}

	// 2. UID-based block (UID < 1000) - NixOS/Debian system users
	u, err := user.Lookup(targetUsername)
	if err == nil {
		uid, _ := strconv.Atoi(u.Uid)
		if uid < 1000 {
			respondErrorSimple(w, fmt.Sprintf("Cannot delete system user: %s (UID %d)", targetUsername, uid), http.StatusForbidden)
			return
		}
	}

	// 3. Fallback static list for common system users (Finding #17)
	protectedStatic := map[string]bool{
		"bin": true, "daemon": true, "sys": true, "sync": true, "games": true,
		"man": true, "lp": true, "mail": true, "news": true, "uucp": true,
		"proxy": true, "www-data": true, "backup": true, "list": true, "irc": true,
		"gnats": true, "nobody": true, "_apt": true, "systemd-timesync": true,
		"systemd-network": true, "systemd-resolve": true, "messagebus": true, "sshd": true,
		"syslog": true, "uuidd": true, "tcpdump": true, "pollinate": true, "landscape": true,
	}
	if protectedStatic[targetUsername] {
		respondErrorSimple(w, "Cannot delete protected system user: "+targetUsername, http.StatusForbidden)
		return
	}

	// 2. CHECK for last admin (ensure at least one admin remains)
	var adminCount int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM users WHERE role = 'admin' AND active = 1`).Scan(&adminCount); err != nil {
		respondErrorSimple(w, "Failed to check admin count", http.StatusInternalServerError)
		log.Printf("USER DELETE ADMIN COUNT ERROR: %v", err)
		return
	}
	if targetRole == "admin" && adminCount <= 1 {
		respondErrorSimple(w, "Cannot delete the last remaining active admin account", http.StatusForbidden)
		return
	}

	_, err = h.db.Exec(`DELETE FROM sessions WHERE user_id = $1`, req.ID)
	if err != nil {
		respondErrorSimple(w, "Failed to delete user sessions", http.StatusInternalServerError)
		log.Printf("USER DELETE SESSIONS ERROR: %v", err)
		return
	}
	_, err = h.db.Exec(`DELETE FROM users WHERE id = $1`, req.ID)
	if err != nil {
		respondErrorSimple(w, "Failed to delete user", http.StatusInternalServerError)
		log.Printf("USER DELETE ERROR: %v", err)
		return
	}

	respondJSON(w, http.StatusOK, map[string]any{
		"success": true,
		"message": "User deleted",
	})
}

// ResetUserPassword - POST /api/users/{id}/reset-password
// Sets a temporary password and forces the user to change it on next login.
// Revokes all current sessions for the target user.
func (h *UserGroupHandler) ResetUserPassword(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	idStr := vars["id"]
	id, err := strconv.Atoi(idStr)
	if err != nil {
		respondErrorSimple(w, "Invalid user ID", http.StatusBadRequest)
		return
	}

	type req struct {
		TempPassword string `json:"temp_password"`
	}
	var body req
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		respondErrorSimple(w, "Invalid request", http.StatusBadRequest)
		return
	}

	body.TempPassword = strings.TrimSpace(body.TempPassword)
	if ok, msg := validatePasswordStrength(body.TempPassword); !ok {
		respondErrorSimple(w, msg, http.StatusBadRequest)
		return
	}

	var username, source, targetRole string
	err = h.db.QueryRow(`SELECT username, COALESCE(source,'local'), COALESCE(role,'user') FROM users WHERE id = $1`, id).Scan(&username, &source, &targetRole)
	if err != nil {
		respondErrorSimple(w, "User not found", http.StatusNotFound)
		return
	}
	// Resetting someone's password is taking over their account: the same
	// hierarchy as for other changes applies.
	u := r.Context().Value(middleware.UserContextKey)
	if u == nil {
		respondErrorSimple(w, "Unauthorized (No user context)", http.StatusUnauthorized)
		return
	}
	var reqRole string
	if err := h.db.QueryRow(`SELECT COALESCE(role,'user') FROM users WHERE username = $1`, u.(*middleware.User).Username).Scan(&reqRole); err != nil {
		respondErrorSimple(w, "Error verifying requester", http.StatusInternalServerError)
		return
	}
	if !mayManage(reqRole, targetRole) {
		respondErrorSimple(w, fmt.Sprintf("Higher privileges required to reset the password of %s accounts", targetRole), http.StatusForbidden)
		return
	}

	if source == "ldap" {
		respondErrorSimple(w, "Cannot reset password for LDAP users - manage via directory server", http.StatusBadRequest)
		return
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(body.TempPassword), bcrypt.DefaultCost)
	if err != nil {
		respondErrorSimple(w, "Internal error", http.StatusInternalServerError)
		return
	}
	scramKeys, err := scram.Derive(body.TempPassword)
	if err != nil {
		respondErrorSimple(w, "Internal error", http.StatusInternalServerError)
		return
	}

	_, err = h.db.Exec(
		`UPDATE users SET password_hash = $1, scram_salt = $2, scram_iterations = $3,
		 scram_stored_key = $4, scram_server_key = $5, must_change_password = 1 WHERE id = $6`,
		string(hash),
		scram.EncodeBase64(scramKeys.Salt), scramKeys.Iterations,
		scram.EncodeBase64(scramKeys.StoredKey), scram.EncodeBase64(scramKeys.ServerKey),
		id,
	)
	if err != nil {
		respondErrorSimple(w, "Failed to reset password", http.StatusInternalServerError)
		return
	}

	// Revoke all sessions so the user must log in with the new temp password
	if _, err := h.db.Exec(`DELETE FROM sessions WHERE username = $1`, username); err != nil {
		log.Printf("RESET PASSWORD: failed to revoke sessions for %s: %v", username, err)
	}

	respondJSON(w, http.StatusOK, map[string]any{
		"success": true,
		"message": fmt.Sprintf("Password reset for %s - user must set a new password on next login", username),
	})
}

// ─── GROUPS ────────────────────────────────────────────────

// HandleGroups - GET: list groups, POST: create/update/delete groups
func (h *UserGroupHandler) HandleGroups(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		h.listGroups(w, r)
	case http.MethodPost:
		// Use middleware manually for POST mutations (#17)
		middleware.RequirePermission("users", "write")(http.HandlerFunc(h.groupAction)).ServeHTTP(w, r)
	default:
		respondErrorSimple(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func (h *UserGroupHandler) listGroups(w http.ResponseWriter, r *http.Request) {
	// Support ?id= for single-group lookup
	if idStr := r.URL.Query().Get("id"); idStr != "" {
		var id, gid int
		var name, desc, createdAt string
		err := h.db.QueryRow(
			`SELECT id, name, COALESCE(description,''), COALESCE(gid,0), COALESCE(TO_CHAR(created_at, 'YYYY-MM-DD"T"HH24:MI:SS"Z"'), '') FROM groups WHERE id = $1`, idStr,
		).Scan(&id, &name, &desc, &gid, &createdAt)
		if err != nil {
			respondErrorSimple(w, "Group not found", http.StatusNotFound)
			return
		}
		// Get member count
		var memberCount int
		if err := h.db.QueryRow(`SELECT COUNT(*) FROM group_members WHERE group_name = $1`, name).Scan(&memberCount); err != nil {
			log.Printf("SINGLE GROUP MEMBER COUNT ERROR: %v", err)
			memberCount = 0
		}

		respondJSON(w, http.StatusOK, map[string]any{
			"success": true,
			"group": map[string]any{
				"id":           id,
				"name":         name,
				"description":  desc,
				"gid":          gid,
				"member_count": memberCount,
				"members":      h.groupMemberNames(name),
				"created_at":   createdAt,
			},
		})
		return
	}

	rows, err := h.db.Query(`SELECT id, name, COALESCE(description,''), COALESCE(gid,0), COALESCE(TO_CHAR(created_at, 'YYYY-MM-DD"T"HH24:MI:SS"Z"'), '') FROM groups ORDER BY name`)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "Failed to list groups", err)
		return
	}
	defer rows.Close()

	var groups []map[string]any
	for rows.Next() {
		var id, gid int
		var name, desc, createdAt string
		if err := rows.Scan(&id, &name, &desc, &gid, &createdAt); err != nil {
			log.Printf("GROUP ROW SCAN ERROR: %v", err)
			continue
		}

		// Get member count
		var memberCount int
		if err := h.db.QueryRow(`SELECT COUNT(*) FROM group_members WHERE group_name = $1`, name).Scan(&memberCount); err != nil {
			log.Printf("GROUP MEMBER COUNT ERROR: %v", err)
			memberCount = 0
		}

		groups = append(groups, map[string]any{
			"id":           id,
			"name":         name,
			"description":  desc,
			"gid":          gid,
			"member_count": memberCount,
			"members":      h.groupMemberNames(name),
			"created_at":   createdAt,
		})
	}

	if err := rows.Err(); err != nil {
		respondError(w, http.StatusInternalServerError, "Error iterating groups", err)
		return
	}
	if groups == nil {
		groups = []map[string]any{}
	}

	respondJSON(w, http.StatusOK, map[string]any{
		"success": true,
		"groups":  groups,
	})
}

type groupActionRequest struct {
	Action          string          `json:"action"` // create, update, delete
	ID              int             `json:"id"`
	Name            string          `json:"name"`
	Description     string          `json:"description"`
	GID             int             `json:"gid"`
	Members         json.RawMessage `json:"members"`          // usernames (web UI) or user IDs
	ConfirmPassword string          `json:"confirm_password"` // Required for all mutations (#17)
}

func (h *UserGroupHandler) groupAction(w http.ResponseWriter, r *http.Request) {
	var req groupActionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondErrorSimple(w, "Invalid request", http.StatusBadRequest)
		return
	}

	// ─── SECURITY & HIERRARCHY CHECKS (#17) ───────────────────────────

	// 1. Get Requester Info
	u := r.Context().Value(middleware.UserContextKey)
	if u == nil {
		respondErrorSimple(w, "Unauthorized (No user context)", http.StatusUnauthorized)
		return
	}
	requester := u.(*middleware.User)

	// Fetch requester's password hash and role
	var reqRole, reqPassHash string
	err := h.db.QueryRow(`SELECT role, password_hash FROM users WHERE username = $1`, requester.Username).Scan(&reqRole, &reqPassHash)
	if err != nil {
		respondErrorSimple(w, "Error verifying requester", http.StatusInternalServerError)
		return
	}

	// 2. Authorization
	// Only admins or users with sufficient privs (already checked by permRoute)
	// but we MANDATE confirm_password for any mutation.
	if req.ConfirmPassword == "" {
		respondErrorSimple(w, "Current password required to authorize group management", http.StatusBadRequest)
		return
	}
	if err := bcrypt.CompareHashAndPassword([]byte(reqPassHash), []byte(req.ConfirmPassword)); err != nil {
		respondErrorSimple(w, "Invalid current password", http.StatusForbidden)
		return
	}

	switch req.Action {
	case "create":
		if !groupNameRe.MatchString(req.Name) {
			respondErrorSimple(w, "Invalid group name (letters, digits, _ . -; up to 32, not starting with a digit or -)", http.StatusBadRequest)
			return
		}
		members, err := h.resolveMembers(req.Members)
		if err != nil {
			respondErrorSimple(w, err.Error(), http.StatusBadRequest)
			return
		}
		tx, err := h.db.Begin()
		if err != nil {
			respondErrorSimple(w, "Internal error", http.StatusInternalServerError)
			return
		}
		defer tx.Rollback() //nolint:errcheck
		var id int64
		if err := tx.QueryRow(
			`INSERT INTO groups (name, description, gid) VALUES ($1, $2, $3) RETURNING id`,
			req.Name, req.Description, req.GID,
		).Scan(&id); err != nil {
			respondErrorSimple(w, "Failed to create group (name may already exist)", http.StatusConflict)
			return
		}
		if members != nil {
			if err := setGroupMembers(tx, req.Name, members); err != nil {
				respondErrorSimple(w, "Failed to add group members", http.StatusInternalServerError)
				return
			}
		}
		if err := tx.Commit(); err != nil {
			respondErrorSimple(w, "Failed to create group", http.StatusInternalServerError)
			return
		}
		respondJSON(w, http.StatusOK, map[string]any{
			"success": true, "id": id, "message": "Group created",
		})
		gitops.CommitAllAsync(h.db)

	case "update":
		id, currentName, ok := h.findGroup(w, req)
		if !ok {
			return
		}
		members, err := h.resolveMembers(req.Members)
		if err != nil {
			respondErrorSimple(w, err.Error(), http.StatusBadRequest)
			return
		}
		tx, err := h.db.Begin()
		if err != nil {
			respondErrorSimple(w, "Internal error", http.StatusInternalServerError)
			return
		}
		defer tx.Rollback() //nolint:errcheck
		// A name differing from the current one (with an id) is a rename;
		// memberships are keyed by name and follow it (ON UPDATE CASCADE).
		if req.ID != 0 && req.Name != "" && req.Name != currentName {
			if !groupNameRe.MatchString(req.Name) {
				respondErrorSimple(w, "Invalid group name", http.StatusBadRequest)
				return
			}
			if _, err := tx.Exec(`UPDATE groups SET name = $1 WHERE id = $2`, req.Name, id); err != nil {
				respondErrorSimple(w, "Failed to update group name (may already exist)", http.StatusConflict)
				return
			}
			currentName = req.Name
		}
		if req.Description != "" {
			if _, err := tx.Exec(`UPDATE groups SET description = $1 WHERE id = $2`, req.Description, id); err != nil {
				respondErrorSimple(w, "Failed to update group description", http.StatusInternalServerError)
				return
			}
		}
		if req.GID != 0 {
			if _, err := tx.Exec(`UPDATE groups SET gid = $1 WHERE id = $2`, req.GID, id); err != nil {
				respondErrorSimple(w, "Failed to update group GID", http.StatusInternalServerError)
				return
			}
		}
		if members != nil {
			if err := setGroupMembers(tx, currentName, members); err != nil {
				log.Printf("GROUP UPDATE MEMBERS: %v", err)
				respondErrorSimple(w, "Failed to update group members", http.StatusInternalServerError)
				return
			}
		}
		if err := tx.Commit(); err != nil {
			respondErrorSimple(w, "Failed to update group", http.StatusInternalServerError)
			return
		}
		respondJSON(w, http.StatusOK, map[string]any{
			"success": true, "message": "Group updated",
		})
		gitops.CommitAllAsync(h.db)

	case "delete":
		id, groupName, ok := h.findGroup(w, req)
		if !ok {
			return
		}
		tx, err := h.db.Begin()
		if err != nil {
			respondErrorSimple(w, "Internal error", http.StatusInternalServerError)
			return
		}
		defer tx.Rollback() //nolint:errcheck
		if _, err := tx.Exec(`DELETE FROM group_members WHERE group_name = $1`, groupName); err != nil {
			respondErrorSimple(w, "Failed to delete group members", http.StatusInternalServerError)
			return
		}
		if _, err := tx.Exec(`DELETE FROM groups WHERE id = $1`, id); err != nil {
			respondErrorSimple(w, "Failed to delete group", http.StatusInternalServerError)
			return
		}
		if err := tx.Commit(); err != nil {
			respondErrorSimple(w, "Failed to delete group", http.StatusInternalServerError)
			return
		}
		respondJSON(w, http.StatusOK, map[string]any{
			"success": true, "message": "Group deleted",
		})
		gitops.CommitAllAsync(h.db)

	default:
		respondErrorSimple(w, "Unknown action", http.StatusBadRequest)
	}
}

// findGroup identifies the group of an update/delete by id, or by name
// (the web UI sends the name).
func (h *UserGroupHandler) findGroup(w http.ResponseWriter, req groupActionRequest) (int, string, bool) {
	var id int
	var name string
	var err error
	switch {
	case req.ID != 0:
		err = h.db.QueryRow(`SELECT id, name FROM groups WHERE id = $1`, req.ID).Scan(&id, &name)
	case req.Name != "":
		err = h.db.QueryRow(`SELECT id, name FROM groups WHERE name = $1`, req.Name).Scan(&id, &name)
	default:
		respondErrorSimple(w, "Group id or name required", http.StatusBadRequest)
		return 0, "", false
	}
	if err != nil {
		respondErrorSimple(w, "Group not found", http.StatusNotFound)
		return 0, "", false
	}
	return id, name, true
}

// resolveMembers turns the members field (usernames or user IDs) into
// usernames; nil when the field is absent (members unchanged). Unknown
// users are an error, not silently skipped.
func (h *UserGroupHandler) resolveMembers(raw json.RawMessage) ([]string, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var names []string
	if err := json.Unmarshal(raw, &names); err != nil {
		var ids []int
		if err := json.Unmarshal(raw, &ids); err != nil {
			return nil, fmt.Errorf("members must be a list of usernames or user IDs")
		}
		names = nil // a failed decode can leave partial elements behind
		for _, uid := range ids {
			var n string
			if err := h.db.QueryRow(`SELECT username FROM users WHERE id = $1`, uid).Scan(&n); err != nil {
				return nil, fmt.Errorf("unknown user id %d", uid)
			}
			names = append(names, n)
		}
		return append([]string{}, names...), nil // non-nil: an empty list clears the members
	}
	out := []string{}
	for _, n := range names {
		var exists bool
		if err := h.db.QueryRow(`SELECT EXISTS(SELECT 1 FROM users WHERE username = $1)`, n).Scan(&exists); err != nil || !exists {
			return nil, fmt.Errorf("unknown user %q", n)
		}
		out = append(out, n)
	}
	return out, nil
}

func setGroupMembers(tx *sql.Tx, group string, members []string) error {
	if _, err := tx.Exec(`DELETE FROM group_members WHERE group_name = $1`, group); err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, m := range members {
		if seen[m] {
			continue
		}
		seen[m] = true
		if _, err := tx.Exec(`INSERT INTO group_members (group_name, username) VALUES ($1, $2)`, group, m); err != nil {
			return err
		}
	}
	return nil
}

// groupMemberNames lists a group's members.
func (h *UserGroupHandler) groupMemberNames(group string) []string {
	out := []string{}
	rows, err := h.db.Query(`SELECT username FROM group_members WHERE group_name = $1 ORDER BY username`, group)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var n string
		if rows.Scan(&n) == nil {
			out = append(out, n)
		}
	}
	return out
}
