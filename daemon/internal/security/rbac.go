package security

import (
	"database/sql"
	"fmt"
	"strconv"
	"sync"
	"time"
)

// Permission represents a single permission
type Permission struct {
	ID          int    `json:"id"`
	Resource    string `json:"resource"`
	Action      string `json:"action"`
	DisplayName string `json:"display_name"`
	Description string `json:"description"`
	Category    string `json:"category"`
	CreatedAt   string `json:"created_at"`
}

// Role represents a user role
type Role struct {
	ID          int          `json:"id"`
	Name        string       `json:"name"`
	DisplayName string       `json:"display_name"`
	Description string       `json:"description"`
	IsSystem    bool         `json:"is_system"`
	Permissions []Permission `json:"permissions,omitempty"`
	// ExpiresAt is set in the roles of a user when that assignment ends
	// by itself (RFC 3339).
	ExpiresAt *string `json:"expires_at,omitempty"`
	CreatedAt string  `json:"created_at"`
	UpdatedAt string  `json:"updated_at"`
}

// UserRole represents a user's role assignment
type UserRole struct {
	UserID    int     `json:"user_id"`
	RoleID    int     `json:"role_id"`
	GrantedAt string  `json:"granted_at"`
	GrantedBy *int    `json:"granted_by,omitempty"`
	ExpiresAt *string `json:"expires_at,omitempty"`
}

// PermissionCache caches user permissions to avoid DB hits
type PermissionCache struct {
	permissions map[int][]Permission
	roles       map[int][]Role
	mutex       sync.RWMutex
	ttl         time.Duration
	lastUpdate  map[int]time.Time
}

var permCache = &PermissionCache{
	permissions: make(map[int][]Permission),
	roles:       make(map[int][]Role),
	lastUpdate:  make(map[int]time.Time),
	ttl:         5 * time.Minute, // Cache for 5 minutes
}

// Database connection (should be injected)
var db *sql.DB

// ============================================================================
// PERMISSION CHECKING
// ============================================================================

// UserHasPermission checks if a user has a specific permission
func UserHasPermission(userID int, resource, action string) (bool, error) {
	// God mode: user ID 1 always has all permissions
	if userID == 1 {
		return true, nil
	}

	// Check cache first
	permissions, cached := permCache.GetPermissions(userID)
	if !cached {
		// Load from database
		perms, err := loadUserPermissions(userID)
		if err != nil {
			return false, fmt.Errorf("failed to load permissions: %w", err)
		}
		permissions = perms
		permCache.SetPermissions(userID, perms)
	}

	// Check if permission exists
	for _, perm := range permissions {
		if perm.Resource == resource && perm.Action == action {
			return true, nil
		}

		// Check wildcard permissions
		if perm.Resource == resource && perm.Action == "*" {
			return true, nil
		}
		if perm.Resource == "*" && perm.Action == "*" {
			return true, nil
		}
	}

	return false, nil
}

// LoadUserPermissions loads all permissions for a user from database (exported for API)
func LoadUserPermissions(userID int) ([]Permission, error) {
	return loadUserPermissions(userID)
}

// loadUserPermissions loads all permissions for a user from database
func loadUserPermissions(userID int) ([]Permission, error) {
	query := `
		SELECT DISTINCT p.id, p.resource, p.action, p.display_name, p.description, p.category, p.created_at
		FROM permissions p
		JOIN role_permissions rp ON p.id = rp.permission_id
		JOIN user_roles ur ON rp.role_id = ur.role_id
		WHERE ur.user_id = $1
		AND (ur.expires_at IS NULL OR ur.expires_at > NOW())
		ORDER BY p.category, p.resource, p.action
	`

	rows, err := db.Query(query, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var permissions []Permission
	for rows.Next() {
		var perm Permission
		err := rows.Scan(
			&perm.ID,
			&perm.Resource,
			&perm.Action,
			&perm.DisplayName,
			&perm.Description,
			&perm.Category,
			&perm.CreatedAt,
		)
		if err != nil {
			return nil, err
		}
		permissions = append(permissions, perm)
	}

	return permissions, nil
}

// ============================================================================
// ROLE CHECKING
// ============================================================================

// GetUserRoles returns all roles for a user
func GetUserRoles(userID int) ([]Role, error) {
	// Check cache first
	roles, cached := permCache.GetRoles(userID)
	if cached {
		return roles, nil
	}

	query := `
		SELECT r.id, r.name, r.display_name, r.description, r.is_system, r.created_at, r.updated_at,
		       TO_CHAR(ur.expires_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"')
		FROM roles r
		JOIN user_roles ur ON r.id = ur.role_id
		WHERE ur.user_id = $1
		AND (ur.expires_at IS NULL OR ur.expires_at > NOW())
		ORDER BY r.name
	`

	rows, err := db.Query(query, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var roleList []Role
	for rows.Next() {
		var role Role
		err := rows.Scan(
			&role.ID,
			&role.Name,
			&role.DisplayName,
			&role.Description,
			&role.IsSystem,
			&role.CreatedAt,
			&role.UpdatedAt,
			&role.ExpiresAt,
		)
		if err != nil {
			return nil, err
		}
		roleList = append(roleList, role)
	}

	permCache.SetRoles(userID, roleList)
	return roleList, nil
}

// ============================================================================
// ROLE MANAGEMENT
// ============================================================================

// CreateRole creates a new role
func CreateRole(name, displayName, description string) (*Role, error) {
	query := `
		INSERT INTO roles (name, display_name, description, is_system)
		VALUES ($1, $2, $3, 0)
		RETURNING id
	`

	var id int64
	err := db.QueryRow(query, name, displayName, description).Scan(&id)
	if err != nil {
		return nil, err
	}

	return GetRoleByID(int(id))
}

// GetRoleByID retrieves a role by ID
func GetRoleByID(roleID int) (*Role, error) {
	query := `
		SELECT id, name, display_name, description, is_system, created_at, updated_at
		FROM roles
		WHERE id = $1
	`

	var role Role
	err := db.QueryRow(query, roleID).Scan(
		&role.ID,
		&role.Name,
		&role.DisplayName,
		&role.Description,
		&role.IsSystem,
		&role.CreatedAt,
		&role.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}

	// Load permissions
	role.Permissions, err = GetRolePermissions(roleID)
	if err != nil {
		return nil, err
	}

	return &role, nil
}

// UpdateRole updates a role
func UpdateRole(roleID int, displayName, description string) error {
	// Prevent updating system roles
	var isSystem bool
	err := db.QueryRow("SELECT is_system FROM roles WHERE id = $1", roleID).Scan(&isSystem)
	if err != nil {
		return err
	}
	if isSystem {
		return fmt.Errorf("cannot modify system role")
	}

	query := `
		UPDATE roles
		SET display_name = $1, description = $2, updated_at = NOW()
		WHERE id = $3 AND is_system = 0
	`

	_, err = db.Exec(query, displayName, description, roleID)
	return err
}

// DeleteRole deletes a role
func DeleteRole(roleID int) error {
	// Prevent deleting system roles
	var isSystem bool
	err := db.QueryRow("SELECT is_system FROM roles WHERE id = $1", roleID).Scan(&isSystem)
	if err != nil {
		return err
	}
	if isSystem {
		return fmt.Errorf("cannot delete system role")
	}

	query := "DELETE FROM roles WHERE id = $1 AND is_system = 0"
	_, err = db.Exec(query, roleID)
	return err
}

// ============================================================================
// PERMISSION MANAGEMENT
// ============================================================================

// GetRolePermissions retrieves all permissions for a role
func GetRolePermissions(roleID int) ([]Permission, error) {
	query := `
		SELECT p.id, p.resource, p.action, p.display_name, p.description, p.category, p.created_at
		FROM permissions p
		JOIN role_permissions rp ON p.id = rp.permission_id
		WHERE rp.role_id = $1
		ORDER BY p.category, p.resource, p.action
	`

	rows, err := db.Query(query, roleID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var permissions []Permission
	for rows.Next() {
		var perm Permission
		err := rows.Scan(
			&perm.ID,
			&perm.Resource,
			&perm.Action,
			&perm.DisplayName,
			&perm.Description,
			&perm.Category,
			&perm.CreatedAt,
		)
		if err != nil {
			return nil, err
		}
		permissions = append(permissions, perm)
	}

	return permissions, nil
}

// GetAllPermissions retrieves all available permissions
func GetAllPermissions() ([]Permission, error) {
	query := `
		SELECT id, resource, action, display_name, description, category, created_at
		FROM permissions
		ORDER BY category, resource, action
	`

	rows, err := db.Query(query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var permissions []Permission
	for rows.Next() {
		var perm Permission
		err := rows.Scan(
			&perm.ID,
			&perm.Resource,
			&perm.Action,
			&perm.DisplayName,
			&perm.Description,
			&perm.Category,
			&perm.CreatedAt,
		)
		if err != nil {
			return nil, err
		}
		permissions = append(permissions, perm)
	}

	return permissions, nil
}

// ============================================================================
// USER ROLE ASSIGNMENT
// ============================================================================

// AssignRoleToUser assigns a role to a user
func AssignRoleToUser(userID, roleID int, grantedBy *int, expiresAt *string) error {
	query := `
		INSERT INTO user_roles (user_id, role_id, granted_by, expires_at)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (user_id, role_id) DO UPDATE SET
			granted_by = EXCLUDED.granted_by,
			expires_at = EXCLUDED.expires_at
	`
	// granted_by is a TEXT column (who granted it: a user id, "system", ...).
	by := "system"
	if grantedBy != nil {
		by = strconv.Itoa(*grantedBy)
	}

	_, err := db.Exec(query, userID, roleID, by, expiresAt)
	if err != nil {
		return err
	}

	// Invalidate cache
	permCache.Invalidate(userID)
	return nil
}

// RemoveRoleFromUser removes a role from a user
func RemoveRoleFromUser(userID, roleID int) error {
	query := "DELETE FROM user_roles WHERE user_id = $1 AND role_id = $2"
	_, err := db.Exec(query, userID, roleID)
	if err != nil {
		return err
	}

	// Invalidate cache
	permCache.Invalidate(userID)
	return nil
}

// ============================================================================
// CACHE MANAGEMENT
// ============================================================================

// GetPermissions retrieves permissions from cache
func (c *PermissionCache) GetPermissions(userID int) ([]Permission, bool) {
	c.mutex.RLock()
	defer c.mutex.RUnlock()

	lastUpdate, exists := c.lastUpdate[userID]
	if !exists || time.Since(lastUpdate) > c.ttl {
		return nil, false
	}

	perms, exists := c.permissions[userID]
	return perms, exists
}

// SetPermissions stores permissions in cache
func (c *PermissionCache) SetPermissions(userID int, perms []Permission) {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	c.permissions[userID] = perms
	c.lastUpdate[userID] = time.Now()
}

// GetRoles retrieves roles from cache
func (c *PermissionCache) GetRoles(userID int) ([]Role, bool) {
	c.mutex.RLock()
	defer c.mutex.RUnlock()

	lastUpdate, exists := c.lastUpdate[userID]
	if !exists || time.Since(lastUpdate) > c.ttl {
		return nil, false
	}

	roles, exists := c.roles[userID]
	return roles, exists
}

// SetRoles stores roles in cache
func (c *PermissionCache) SetRoles(userID int, roles []Role) {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	c.roles[userID] = roles
	c.lastUpdate[userID] = time.Now()
}

// Invalidate removes a user from cache
func (c *PermissionCache) Invalidate(userID int) {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	delete(c.permissions, userID)
	delete(c.roles, userID)
	delete(c.lastUpdate, userID)
}

// InvalidateAll clears entire cache
func (c *PermissionCache) InvalidateAll() {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	c.permissions = make(map[int][]Permission)
	c.roles = make(map[int][]Role)
	c.lastUpdate = make(map[int]time.Time)
}

// InvalidateUserPermissions drops a user's cached permissions and roles,
// for code that changes user_roles directly (directory sync).
func InvalidateUserPermissions(userID int) {
	permCache.Invalidate(userID)
}
