package handlers

import (
	"strings"
	"regexp"
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"dplaned/internal/middleware"
	"dplaned/internal/security"

	"github.com/gorilla/mux"
)

// ============================================================================
// ROLE HANDLERS
// ============================================================================

// ListRoles returns all roles
var roleNameRe = regexp.MustCompile(`^[a-z][a-z0-9_-]{1,31}$`)

func HandleListRoles(w http.ResponseWriter, r *http.Request) {
	// Permissions as "resource:action" strings, as the Users page edits them.
	roles, err := security.ListRoleSummaries()
	if err != nil {
		respondError(w, http.StatusInternalServerError, "Failed to fetch roles", err)
		return
	}

	respondJSON(w, http.StatusOK, map[string]any{
		"success": true,
		"roles":   roles,
		"count":   len(roles),
	})
}

// GetRole returns a specific role with its permissions
func HandleGetRole(w http.ResponseWriter, r *http.Request) {
	roleID, err := strconv.Atoi(mux.Vars(r)["id"])
	if err != nil {
		respondError(w, http.StatusBadRequest, "Invalid role ID", err)
		return
	}

	role, err := security.GetRoleByID(roleID)
	if err != nil {
		respondError(w, http.StatusNotFound, "Role not found", err)
		return
	}

	respondJSON(w, http.StatusOK, role)
}

// CreateRole creates a new custom role
func HandleCreateRole(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name        string   `json:"name"`
		DisplayName string   `json:"display_name"`
		Description string   `json:"description"`
		Permissions []string `json:"permissions"` // "resource:action"
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "Invalid request body", err)
		return
	}

	if !roleNameRe.MatchString(req.Name) {
		respondErrorSimple(w, "Invalid role name (lower case letters, digits, - and _; 2-32)", http.StatusBadRequest)
		return
	}
	if req.DisplayName == "" {
		req.DisplayName = req.Name
	}

	role, err := security.CreateRole(req.Name, req.DisplayName, req.Description)
	if err != nil {
		if strings.Contains(err.Error(), "duplicate") || strings.Contains(err.Error(), "unique") {
			respondErrorSimple(w, "A role named "+req.Name+" already exists", http.StatusConflict)
			return
		}
		respondError(w, http.StatusInternalServerError, "Failed to create role", err)
		return
	}
	if len(req.Permissions) > 0 {
		if err := security.SetRolePermissions(role.ID, req.Permissions); err != nil {
			_ = security.DeleteRole(role.ID)
			respondErrorSimple(w, err.Error(), http.StatusBadRequest)
			return
		}
	}

	respondJSON(w, http.StatusCreated, map[string]any{"success": true, "role": role})
}

// UpdateRole updates an existing role
func HandleUpdateRole(w http.ResponseWriter, r *http.Request) {
	roleID, err := strconv.Atoi(mux.Vars(r)["id"])
	if err != nil {
		respondError(w, http.StatusBadRequest, "Invalid role ID", err)
		return
	}

	var req struct {
		DisplayName string    `json:"display_name"`
		Description *string   `json:"description"`
		Permissions *[]string `json:"permissions"` // replaces the role's permissions
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "Invalid request body", err)
		return
	}

	current, err := security.GetRoleByID(roleID)
	if err != nil {
		respondError(w, http.StatusNotFound, "Role not found", err)
		return
	}
	// Built-in roles keep their name and description; their permissions
	// (except admin's) can be adjusted.
	if !current.IsSystem && (req.DisplayName != "" || req.Description != nil) {
		dn, desc := current.DisplayName, current.Description
		if req.DisplayName != "" {
			dn = req.DisplayName
		}
		if req.Description != nil {
			desc = *req.Description
		}
		if err := security.UpdateRole(roleID, dn, desc); err != nil {
			respondError(w, http.StatusInternalServerError, "Failed to update role", err)
			return
		}
	}
	if req.Permissions != nil {
		if err := security.SetRolePermissions(roleID, *req.Permissions); err != nil {
			respondErrorSimple(w, err.Error(), http.StatusBadRequest)
			return
		}
	}

	role, err := security.GetRoleByID(roleID)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "Failed to fetch updated role", err)
		return
	}

	respondJSON(w, http.StatusOK, role)
}

// DeleteRole deletes a custom role
func HandleDeleteRole(w http.ResponseWriter, r *http.Request) {
	roleID, err := strconv.Atoi(mux.Vars(r)["id"])
	if err != nil {
		respondError(w, http.StatusBadRequest, "Invalid role ID", err)
		return
	}

	if err := security.DeleteRole(roleID); err != nil {
		respondError(w, http.StatusInternalServerError, "Failed to delete role", err)
		return
	}

	respondJSON(w, http.StatusOK, map[string]string{
		"message": "Role deleted successfully",
	})
}

// ============================================================================
// PERMISSION HANDLERS
// ============================================================================

// ListPermissions returns all available permissions
func HandleListPermissions(w http.ResponseWriter, r *http.Request) {
	permissions, err := security.GetAllPermissions()
	if err != nil {
		respondError(w, http.StatusInternalServerError, "Failed to fetch permissions", err)
		return
	}

	// Group permissions by category
	grouped := make(map[string][]security.Permission)
	for _, perm := range permissions {
		grouped[perm.Category] = append(grouped[perm.Category], perm)
	}

	respondJSON(w, http.StatusOK, map[string]any{
		"permissions": permissions,
		"grouped":     grouped,
		"count":       len(permissions),
	})
}

// GetRolePermissions returns permissions for a specific role
func HandleGetRolePermissions(w http.ResponseWriter, r *http.Request) {
	roleID, err := strconv.Atoi(mux.Vars(r)["id"])
	if err != nil {
		respondError(w, http.StatusBadRequest, "Invalid role ID", err)
		return
	}

	permissions, err := security.GetRolePermissions(roleID)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "Failed to fetch permissions", err)
		return
	}

	respondJSON(w, http.StatusOK, map[string]any{
		"permissions": permissions,
		"count":       len(permissions),
	})
}

// AssignPermissionToRole assigns a permission to a role
func HandleAssignPermissionToRole(w http.ResponseWriter, r *http.Request) {
	roleID, err := strconv.Atoi(mux.Vars(r)["id"])
	if err != nil {
		respondError(w, http.StatusBadRequest, "Invalid role ID", err)
		return
	}

	var req struct {
		PermissionID int `json:"permission_id"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "Invalid request body", err)
		return
	}

	if err := security.AssignPermissionToRole(roleID, req.PermissionID); err != nil {
		respondError(w, http.StatusInternalServerError, "Failed to assign permission", err)
		return
	}

	respondJSON(w, http.StatusOK, map[string]string{
		"message": "Permission assigned successfully",
	})
}

// RemovePermissionFromRole removes a permission from a role
func HandleRemovePermissionFromRole(w http.ResponseWriter, r *http.Request) {
	roleID, err := strconv.Atoi(mux.Vars(r)["id"])
	if err != nil {
		respondError(w, http.StatusBadRequest, "Invalid role ID", err)
		return
	}

	permissionID, err := strconv.Atoi(mux.Vars(r)["permissionId"])
	if err != nil {
		respondError(w, http.StatusBadRequest, "Invalid permission ID", err)
		return
	}

	if err := security.RemovePermissionFromRole(roleID, permissionID); err != nil {
		respondError(w, http.StatusInternalServerError, "Failed to remove permission", err)
		return
	}

	respondJSON(w, http.StatusOK, map[string]string{
		"message": "Permission removed successfully",
	})
}

// ============================================================================
// USER ROLE HANDLERS
// ============================================================================

// GetUserRoles returns all roles for a user
func HandleGetUserRoles(w http.ResponseWriter, r *http.Request) {
	userID, err := strconv.Atoi(mux.Vars(r)["id"])
	if err != nil {
		respondError(w, http.StatusBadRequest, "Invalid user ID", err)
		return
	}

	roles, err := security.GetUserRoles(userID)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "Failed to fetch user roles", err)
		return
	}

	respondJSON(w, http.StatusOK, map[string]any{
		"roles": roles,
		"count": len(roles),
	})
}

// GetUserPermissions returns all effective permissions for a user
func HandleGetUserPermissions(w http.ResponseWriter, r *http.Request) {
	userID, err := strconv.Atoi(mux.Vars(r)["id"])
	if err != nil {
		respondError(w, http.StatusBadRequest, "Invalid user ID", err)
		return
	}

	// Force load from database (bypass cache)
	permissions, err := security.LoadUserPermissions(userID)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "Failed to fetch user permissions", err)
		return
	}

	// Group by category
	grouped := make(map[string][]security.Permission)
	for _, perm := range permissions {
		grouped[perm.Category] = append(grouped[perm.Category], perm)
	}

	respondJSON(w, http.StatusOK, map[string]any{
		"permissions": permissions,
		"grouped":     grouped,
		"count":       len(permissions),
	})
}

// AssignRoleToUser assigns a role to a user
func HandleAssignRoleToUser(w http.ResponseWriter, r *http.Request) {
	userID, err := strconv.Atoi(mux.Vars(r)["id"])
	if err != nil {
		respondError(w, http.StatusBadRequest, "Invalid user ID", err)
		return
	}

	var req struct {
		RoleID    int    `json:"role_id"`
		ExpiresAt string `json:"expires_at,omitempty"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "Invalid request body", err)
		return
	}

	// Get granting user
	grantingUser, ok := middleware.GetUserFromContext(r)
	if !ok {
		respondError(w, http.StatusUnauthorized, "No authenticated user", nil)
		return
	}
	grantedBy := &grantingUser.ID

	// Parse expiry if provided
	var expiresAt *string
	if req.ExpiresAt != "" {
		// Validate format
		if _, err := time.Parse(time.RFC3339, req.ExpiresAt); err != nil {
			respondError(w, http.StatusBadRequest, "Invalid expiry date format", err)
			return
		}
		expiresAt = &req.ExpiresAt
	}

	if err := security.AssignRoleToUser(userID, req.RoleID, grantedBy, expiresAt); err != nil {
		respondError(w, http.StatusInternalServerError, "Failed to assign role", err)
		return
	}

	respondJSON(w, http.StatusOK, map[string]string{
		"message": "Role assigned successfully",
	})
}

// RemoveRoleFromUser removes a role from a user
func HandleRemoveRoleFromUser(w http.ResponseWriter, r *http.Request) {
	userID, err := strconv.Atoi(mux.Vars(r)["id"])
	if err != nil {
		respondError(w, http.StatusBadRequest, "Invalid user ID", err)
		return
	}

	roleID, err := strconv.Atoi(mux.Vars(r)["roleId"])
	if err != nil {
		respondError(w, http.StatusBadRequest, "Invalid role ID", err)
		return
	}

	if err := security.RemoveRoleFromUser(userID, roleID); err != nil {
		respondError(w, http.StatusInternalServerError, "Failed to remove role", err)
		return
	}

	respondJSON(w, http.StatusOK, map[string]string{
		"message": "Role removed successfully",
	})
}

// ============================================================================
// CURRENT USER ENDPOINTS
// ============================================================================

// GetMyPermissions returns permissions for the current user
func HandleGetMyPermissions(w http.ResponseWriter, r *http.Request) {
	user, ok := middleware.GetUserFromContext(r)
	if !ok {
		respondError(w, http.StatusUnauthorized, "No authenticated user", nil)
		return
	}

	permissions, err := security.LoadUserPermissions(user.ID)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "Failed to fetch permissions", err)
		return
	}

	// Create permission map for frontend
	permMap := make(map[string]bool)
	for _, perm := range permissions {
		permMap[perm.Resource+":"+perm.Action] = true
	}

	respondJSON(w, http.StatusOK, map[string]any{
		"success":     true,
		"permissions": permissions,
		"can":         permMap,
		"count":       len(permissions),
	})
}

// GetMyRoles returns roles for the current user
func HandleGetMyRoles(w http.ResponseWriter, r *http.Request) {
	user, ok := middleware.GetUserFromContext(r)
	if !ok {
		respondError(w, http.StatusUnauthorized, "No authenticated user", nil)
		return
	}

	roles, err := security.GetUserRoles(user.ID)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "Failed to fetch roles", err)
		return
	}

	respondJSON(w, http.StatusOK, map[string]any{
		"roles": roles,
		"count": len(roles),
	})
}

// CheckPermission checks if current user has a specific permission
func HandleCheckPermission(w http.ResponseWriter, r *http.Request) {
	user, ok := middleware.GetUserFromContext(r)
	if !ok {
		respondError(w, http.StatusUnauthorized, "No authenticated user", nil)
		return
	}

	resource := r.URL.Query().Get("resource")
	action := r.URL.Query().Get("action")

	if resource == "" || action == "" {
		respondError(w, http.StatusBadRequest, "Resource and action parameters required", nil)
		return
	}

	hasPermission, err := security.UserHasPermission(user.ID, resource, action)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "Failed to check permission", err)
		return
	}

	respondJSON(w, http.StatusOK, map[string]any{
		"resource":  resource,
		"action":    action,
		"permitted": hasPermission,
	})
}

// ============================================================================
// HELPER FUNCTIONS
// ============================================================================

// respondJSON and respondError are defined in helpers.go
