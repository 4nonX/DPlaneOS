package middleware

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

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
	// TokenScopes is set for API token requests ("read", "write", "admin",
	// comma-separated); empty for browser sessions.
	TokenScopes string `json:"-"`
}

var scopeRank = map[string]int{"read": 1, "list": 1, "write": 2, "admin": 3}

// TokenScopeAllows reports whether a token with these scopes may perform an
// action: an action up to the highest scope's rank (read < write < admin).
// Unknown actions need admin.
func TokenScopeAllows(scopes, action string) bool {
	max := 0
	for _, s := range strings.Split(scopes, ",") {
		if r := scopeRank[strings.TrimSpace(s)]; r > max {
			max = r
		}
	}
	need, ok := scopeRank[action]
	if !ok {
		need = scopeRank["admin"]
	}
	return max >= need
}

// TokenReadOnly reports whether the scopes only allow reading.
func TokenReadOnly(scopes string) bool { return !TokenScopeAllows(scopes, "write") }

// RequirePermission middleware checks if user has required permission
func RequirePermission(resource, action string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Get user from context (set by auth middleware)
			user, ok := r.Context().Value(UserContextKey).(*User)
			if !ok || user == nil {
				respondJSON(w, http.StatusUnauthorized, map[string]string{
					"error": "Unauthorized - no valid session",
				})
				return
			}
			// Degraded mode (ID=0): DB unavailable, allow READ, block mutations
			if user.ID == 0 {
				if action == "read" || action == "list" {
					next.ServeHTTP(w, r)
					return
				}
				respondJSON(w, http.StatusForbidden, map[string]string{
					"error":   "Forbidden - database unavailable",
					"message": "Cannot mutate during degraded state",
				})
				return
			}

			// An API token never exceeds its scopes, whatever its user may do.
			if user.TokenScopes != "" && !TokenScopeAllows(user.TokenScopes, action) {
				respondJSON(w, http.StatusForbidden, map[string]string{
					"error":    "Forbidden - token scope",
					"required": resource + ":" + action,
					"message":  "This API token's scopes (" + user.TokenScopes + ") do not include " + action,
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
					"error":    "Forbidden - insufficient permissions",
					"required": resource + ":" + action,
					"message":  "You do not have permission to perform this action",
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
		sessionToken := r.Header.Get("X-Session-ID")
		if sessionToken == "" {
			// Try cookie fallback
			cookie, err := r.Cookie("session_id")
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

		// Enforce must_change_password restriction: allow only the change-password
		// endpoint until the user sets a new password. This prevents a compromised
		// temporary password from being used to access any other resource.
		if secUser.MustChangePassword && r.URL.Path != "/api/auth/change-password" {
			respondJSON(w, http.StatusForbidden, map[string]string{
				"error":  "Your password must be changed before accessing this resource.",
				"guide":  "An administrator has set a temporary password for your account. Go to Settings > Account > Change Password to set a new password. All other operations are blocked until this is done.",
				"code":   "must_change_password",
				"action": "change_password",
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

// RequireAAL2 wraps a handler and rejects requests whose session has not been
// upgraded to AAL2 (password + TOTP). Apply this to routes where the operation
// is irreversible or has elevated blast radius: user deletion, password reset,
// pool destroy, fencing configuration, and ALUA state flip.
//
// API token requests (Authorization: Bearer dpl_...) do not carry a session ID
// and will receive HTTP 401 "authentication required". This is intentional policy:
// irreversible operations with large blast radius require a human MFA-verified
// session. Automation must not be able to destroy pools or reset passwords without
// human involvement. If a specific route needs to be accessible by API tokens,
// apply permRoute without RequireAAL2 and document the rationale.
//
// The response body contains action:"enable_totp" so the UI can prompt the user
// to set up TOTP before retrying.
func RequireAAL2(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sessionToken := r.Header.Get("X-Session-ID")
		if sessionToken == "" {
			if cookie, err := r.Cookie("session_id"); err == nil {
				sessionToken = cookie.Value
			}
		}
		if sessionToken == "" {
			respondJSON(w, http.StatusUnauthorized, map[string]string{
				"error":  "authentication required",
				"guide":  "Please log in to access this resource.",
				"action": "login",
			})
			return
		}
		secUser, err := security.ValidateSessionAndGetUser(sessionToken)
		if err != nil || secUser == nil {
			respondJSON(w, http.StatusUnauthorized, map[string]string{
				"error":  "invalid or expired session",
				"guide":  "Your session has expired. Please log in again. Sessions expire after 24 hours of inactivity.",
				"code":   "session_expired",
				"action": "login",
			})
			return
		}
		if secUser.AAL < 2 {
			respondJSON(w, http.StatusForbidden, map[string]string{
				"error":  "This operation requires two-factor authentication.",
				"guide":  "Your session was authenticated with a password only. Enable TOTP under Settings > Security > Two-Factor Authentication, then log out and log in again.",
				"code":   "aal2_required",
				"action": "enable_totp",
			})
			return
		}
		next.ServeHTTP(w, r)
	})
}

// GetUserFromContext retrieves user from request context
func GetUserFromContext(r *http.Request) (*User, bool) {
	user, ok := r.Context().Value(UserContextKey).(*User)
	return user, ok
}

// respondJSON sends a JSON response
func respondJSON(w http.ResponseWriter, status int, payload any) {
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
