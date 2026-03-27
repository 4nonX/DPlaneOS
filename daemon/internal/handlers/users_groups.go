package handlers

import (
	"database/sql"
	"dplaned/internal/gitops"
	"dplaned/internal/middleware"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"

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
		respondJSON(w, http.StatusOK, map[string]interface{}{
			"success": true,
			"user": map[string]interface{}{
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

	var users []map[string]interface{}
	for rows.Next() {
		var id, active int
		var username, email, role, createdAt string
		rows.Scan(&id, &username, &email, &role, &active, &createdAt)
		users = append(users, map[string]interface{}{
			"id":         id,
			"username":   username,
			"email":      email,
			"role":       role,
			"active":     active == 1,
			"created_at": createdAt,
		})
	}

	if users == nil {
		users = []map[string]interface{}{}
	}

	respondJSON(w, http.StatusOK, map[string]interface{}{
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

	// 2. Role Hierarchy Table
	roleRank := map[string]int{"admin": 100, "user": 10, "": 0}
	
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

		// B. Hierarchy Enforcement
		if req.Action != "create" {
			var targetRole string
			var targetID int
			h.db.QueryRow(`SELECT id, role FROM users WHERE id = $1`, req.ID).Scan(&targetID, &targetRole)
			
			// Editing someone else?
			if targetID != int(requester.ID) {
				// 1. HARD RULE: Only admins can manage other admin accounts
				if targetRole == "admin" && reqRole != "admin" {
					respondErrorSimple(w, "Only admins can manage other admin accounts", http.StatusForbidden)
					return
				}
				// 2. HIERARCHY: Non-admins cannot manage accounts of equal or higher rank
				if reqRole != "admin" && roleRank[reqRole] <= roleRank[targetRole] {
					respondErrorSimple(w, fmt.Sprintf("Higher privileges required to manage %s accounts", targetRole), http.StatusForbidden)
					return
				}
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

	// Hash password
	hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
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
		`INSERT INTO users (username, password_hash, email, role, active) VALUES ($1, $2, $3, $4, 1) RETURNING id`,
		req.Username, string(hash), req.Email, role,
	).Scan(&id)
	if err != nil {
		respondErrorSimple(w, "Failed to create user", http.StatusInternalServerError)
		log.Printf("USER CREATE ERROR: %v", err)
		return
	}

	respondJSON(w, http.StatusOK, map[string]interface{}{
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
		_, err := h.db.Exec(`UPDATE users SET role = $1 WHERE id = $2`, req.Role, req.ID)
		if err != nil {
			respondErrorSimple(w, "Failed to update role", http.StatusInternalServerError)
			log.Printf("USER UPDATE ROLE ERROR: %v", err)
			return
		}
	}
	if req.Active != nil {
		if !*req.Active {
			// Ensure we don't deactivate the last admin
			var currentRole string
			var adminCount int
			h.db.QueryRow(`SELECT role FROM users WHERE id = $1`, req.ID).Scan(&currentRole)
			h.db.QueryRow(`SELECT COUNT(*) FROM users WHERE role = 'admin' AND active = 1`).Scan(&adminCount)
			if currentRole == "admin" && adminCount <= 1 {
				respondErrorSimple(w, "Cannot deactivate the last remaining active admin account", http.StatusForbidden)
				return
			}
		}
		activeVal := 0
		if *req.Active { activeVal = 1 }
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
		_, err = h.db.Exec(`UPDATE users SET password_hash = $1 WHERE id = $2`, string(hash), req.ID)
		if err != nil {
			respondErrorSimple(w, "Failed to update password", http.StatusInternalServerError)
			log.Printf("USER UPDATE PASSWORD ERROR: %v", err)
			return
		}
	}

	respondJSON(w, http.StatusOK, map[string]interface{}{
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
	if targetUsername == "root" || targetUsername == "dplaneos" {
		respondErrorSimple(w, "Cannot delete protected system service account: "+targetUsername, http.StatusForbidden)
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

	_, err := h.db.Exec(`DELETE FROM sessions WHERE user_id = $1`, req.ID)
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

	respondJSON(w, http.StatusOK, map[string]interface{}{
		"success": true,
		"message": "User deleted",
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

		respondJSON(w, http.StatusOK, map[string]interface{}{
			"success": true,
			"group": map[string]interface{}{
				"id":           id,
				"name":         name,
				"description":  desc,
				"gid":          gid,
				"member_count": memberCount,
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

	var groups []map[string]interface{}
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

		groups = append(groups, map[string]interface{}{
			"id":           id,
			"name":         name,
			"description":  desc,
			"gid":          gid,
			"member_count": memberCount,
			"created_at":   createdAt,
		})
	}

	if groups == nil {
		groups = []map[string]interface{}{}
	}

	respondJSON(w, http.StatusOK, map[string]interface{}{
		"success": true,
		"groups":  groups,
	})
}

type groupActionRequest struct {
	Action          string `json:"action"` // create, update, delete
	ID              int    `json:"id"`
	Name            string `json:"name"`
	Description     string `json:"description"`
	GID             int    `json:"gid"`
	Members         []int  `json:"members"`
	ConfirmPassword string `json:"confirm_password"` // Required for all mutations (#17)
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
		if req.Name == "" {
			respondErrorSimple(w, "Group name required", http.StatusBadRequest)
			return
		}
		var id int64
		err := h.db.QueryRow(
			`INSERT INTO groups (name, description, gid) VALUES ($1, $2, $3) RETURNING id`,
			req.Name, req.Description, req.GID,
		).Scan(&id)
		if err != nil {
			respondErrorSimple(w, "Failed to create group (name may already exist)", http.StatusConflict)
			return
		}
		respondJSON(w, http.StatusOK, map[string]interface{}{
			"success": true, "id": id, "message": "Group created",
		})

		// GITOPS HOOK: write state back to git
		gitops.CommitAllAsync(h.db)

	case "update":
		if req.ID == 0 {
			respondErrorSimple(w, "Group ID required", http.StatusBadRequest)
			return
		}
		if req.Name != "" {
			_, err := h.db.Exec(`UPDATE groups SET name = $1 WHERE id = $2`, req.Name, req.ID)
			if err != nil {
				respondErrorSimple(w, "Failed to update group name (may already exist)", http.StatusConflict)
				log.Printf("GROUP UPDATE NAME ERROR: %v", err)
				return
			}
		}
		if req.Description != "" {
			_, err := h.db.Exec(`UPDATE groups SET description = $1 WHERE id = $2`, req.Description, req.ID)
			if err != nil {
				respondErrorSimple(w, "Failed to update group description", http.StatusInternalServerError)
				log.Printf("GROUP UPDATE DESC ERROR: %v", err)
				return
			}
		}
		// Update members if provided
		if req.Members != nil {
			// Get current group name (to handle renames or just use current)
			var currentName string
			err := h.db.QueryRow(`SELECT name FROM groups WHERE id = $1`, req.ID).Scan(&currentName)
			if err != nil {
				respondErrorSimple(w, "Group not found", http.StatusNotFound)
				return
			}

			_, err = h.db.Exec(`DELETE FROM group_members WHERE group_name = $1`, currentName)
			if err != nil {
				respondErrorSimple(w, "Failed to update group members", http.StatusInternalServerError)
				log.Printf("GROUP UPDATE MEMBERS DELETE ERROR: %v", err)
				return
			}

			for _, uid := range req.Members {
				var uname string
				err := h.db.QueryRow(`SELECT username FROM users WHERE id = $1`, uid).Scan(&uname)
				if err != nil {
					log.Printf("GROUP UPDATE MEMBER: user %d not found, skipping", uid)
					continue
				}
				_, err = h.db.Exec(`INSERT INTO group_members (group_name, username) VALUES ($1, $2)`, currentName, uname)
				if err != nil {
					respondErrorSimple(w, "Failed to add group member", http.StatusInternalServerError)
					log.Printf("GROUP UPDATE MEMBER INSERT ERROR: %v", err)
					return
				}
			}
		}
		respondJSON(w, http.StatusOK, map[string]interface{}{
			"success": true, "message": "Group updated",
		})

		// GITOPS HOOK: write state back to git
		gitops.CommitAllAsync(h.db)

	case "delete":
		if req.ID == 0 {
			respondErrorSimple(w, "Group ID required", http.StatusBadRequest)
			return
		}
		var groupName string
		if err := h.db.QueryRow(`SELECT name FROM groups WHERE id = $1`, req.ID).Scan(&groupName); err != nil {
			respondErrorSimple(w, "Group not found", http.StatusNotFound)
			return
		}

		_, err := h.db.Exec(`DELETE FROM group_members WHERE group_name = $1`, groupName)
		if err != nil {
			respondErrorSimple(w, "Failed to delete group members", http.StatusInternalServerError)
			log.Printf("GROUP DELETE MEMBERS ERROR: %v", err)
			return
		}
		_, err = h.db.Exec(`DELETE FROM groups WHERE id = $1`, req.ID)
		if err != nil {
			respondErrorSimple(w, "Failed to delete group", http.StatusInternalServerError)
			log.Printf("GROUP DELETE ERROR: %v", err)
			return
		}
		respondJSON(w, http.StatusOK, map[string]interface{}{
			"success": true, "message": "Group deleted",
		})

		// GITOPS HOOK: write state back to git
		gitops.CommitAllAsync(h.db)

	default:
		respondErrorSimple(w, "Unknown action", http.StatusBadRequest)
	}
}

