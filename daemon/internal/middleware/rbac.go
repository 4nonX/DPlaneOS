package middleware

import (
	"context"
	"encoding/json"
	"net/http"
	
	"dplaned/internal/security"
)

// User context key
type contextKey string

const (
	UserContextKey contextKey = "user"
)

// User represents an authenticated user
type User struct {
	ID       int    `json:"id"`
	Username string `json:"username"`
	Email    string `json:"email"`
}

// RequirePermission middleware checks if user has required permission
func RequirePermission(resource, action string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Get user from context (set by auth middleware)
			user, ok := r.Context().Value(UserContextKey).(*User)
			if !ok {
				respondJSON(w, http.StatusUnauthorized, map[string]string{
					"error": "Unauthorized - no valid session",
				})
				return
			}

			// Check permission
			hasPermission, err := security.UserHasPermission(user.ID, resource, action)
			if err != nil {
				respondJSON(w, http.StatusInternalServerError, map[string]string{
					"error": "Failed to check permissions",
				})
				return
			}

			if !hasPermission {
				respondJSON(w, http.StatusForbidden, map[string]string{
					"error":      "Forbidden - insufficient permissions",
					"required":   resource + ":" + action,
					"message":    "You do not have permission to perform this action",
				})
				return
			}

			// Permission granted
			next.ServeHTTP(w, r)
		})
	}
}

// RequireAnyPermission - user needs at least one of the listed permissions
func RequireAnyPermission(permissions ...security.Permission) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			user := r.Context().Value(UserContextKey).(*User)
			if user == nil {
				respondJSON(w, http.StatusUnauthorized, map[string]string{
					"error": "Unauthorized",
				})
				return
			}

			hasPermission, err := security.UserHasAnyPermission(user.ID, permissions)
			if err != nil {
				respondJSON(w, http.StatusInternalServerError, map[string]string{
					"error": "Failed to check permissions",
				})
				return
			}

			if !hasPermission {
				respondJSON(w, http.StatusForbidden, map[string]string{
					"error":   "Forbidden - insufficient permissions",
					"message": "You do not have any of the required permissions",
				})
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

// RequireAllPermissions - user needs ALL listed permissions
func RequireAllPermissions(permissions ...security.Permission) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			user := r.Context().Value(UserContextKey).(*User)
			if user == nil {
				respondJSON(w, http.StatusUnauthorized, map[string]string{
					"error": "Unauthorized",
				})
				return
			}

			hasPermission, err := security.UserHasAllPermissions(user.ID, permissions)
			if err != nil {
				respondJSON(w, http.StatusInternalServerError, map[string]string{
					"error": "Failed to check permissions",
				})
				return
			}

			if !hasPermission {
				respondJSON(w, http.StatusForbidden, map[string]string{
					"error":   "Forbidden - insufficient permissions",
					"message": "You do not have all required permissions",
				})
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

// RequireRole - simpler role-based check
func RequireRole(roleName string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			user := r.Context().Value(UserContextKey).(*User)
			if user == nil {
				respondJSON(w, http.StatusUnauthorized, map[string]string{
					"error": "Unauthorized",
				})
				return
			}

			hasRole, err := security.UserHasRole(user.ID, roleName)
			if err != nil {
				respondJSON(w, http.StatusInternalServerError, map[string]string{
					"error": "Failed to check role",
				})
				return
			}

			if !hasRole {
				respondJSON(w, http.StatusForbidden, map[string]string{
					"error":    "Forbidden - insufficient role",
					"required": roleName,
					"message":  "You do not have the required role",
				})
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

// RequireAuth ensures user is authenticated (basic check)
func RequireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Get session token from header
		sessionToken := r.Header.Get("X-Session-Token")
		if sessionToken == "" {
			// Try cookie fallback
			cookie, err := r.Cookie("session_token")
			if err == nil {
				sessionToken = cookie.Value
			}
		}

		if sessionToken == "" {
			respondJSON(w, http.StatusUnauthorized, map[string]string{
				"error": "No session token provided",
			})
			return
		}

		// Validate session and get user
		secUser, err := security.ValidateSessionAndGetUser(sessionToken)
		if err != nil {
			respondJSON(w, http.StatusUnauthorized, map[string]string{
				"error": "Invalid session token",
			})
			return
		}

		// Convert to middleware User type
		user := &User{
			ID:       secUser.ID,
			Username: secUser.Username,
			Email:    secUser.Email,
		}

		// Add user to context
		ctx := context.WithValue(r.Context(), UserContextKey, user)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// GetUserFromContext retrieves user from request context
func GetUserFromContext(r *http.Request) (*User, bool) {
	user, ok := r.Context().Value(UserContextKey).(*User)
	return user, ok
}

// respondJSON sends a JSON response
func respondJSON(w http.ResponseWriter, status int, payload interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(payload)
}

// PermissionChecker is a helper for checking permissions in handlers
type PermissionChecker struct {
	UserID int
}

// Can checks if user has a specific permission
func (pc *PermissionChecker) Can(resource, action string) bool {
	has, _ := security.UserHasPermission(pc.UserID, resource, action)
	return has
}

// CanAny checks if user has any of the permissions
func (pc *PermissionChecker) CanAny(permissions ...security.Permission) bool {
	has, _ := security.UserHasAnyPermission(pc.UserID, permissions)
	return has
}

// CanAll checks if user has all permissions
func (pc *PermissionChecker) CanAll(permissions ...security.Permission) bool {
	has, _ := security.UserHasAllPermissions(pc.UserID, permissions)
	return has
}

// HasRole checks if user has a specific role
func (pc *PermissionChecker) HasRole(roleName string) bool {
	has, _ := security.UserHasRole(pc.UserID, roleName)
	return has
}

// NewPermissionChecker creates a new permission checker for a user
func NewPermissionChecker(userID int) *PermissionChecker {
	return &PermissionChecker{UserID: userID}
}
