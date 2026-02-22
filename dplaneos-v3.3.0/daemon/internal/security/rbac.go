package security

import (
	"database/sql"
	"fmt"
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
	CreatedAt   string       `json:"created_at"`
	UpdatedAt   string       `json:"updated_at"`
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

// SetDatabase sets the database connection
func SetDatabase(database *sql.DB) {
	db = database
}

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

// UserHasAnyPermission checks if user has at least one of the specified permissions
func UserHasAnyPermission(userID int, permissions []Permission) (bool, error) {
	for _, perm := range permissions {
		has, err := UserHasPermission(userID, perm.Resource, perm.Action)
		if err != nil {
			return false, err
		}
		if has {
			return true, nil
		}
	}
	return false, nil
}

// UserHasAllPermissions checks if user has all specified permissions
func UserHasAllPermissions(userID int, permissions []Permission) (bool, error) {
	for _, perm := range permissions {
		has, err := UserHasPermission(userID, perm.Resource, perm.Action)
		if err != nil {
			return false, err
		}
		if !has {
			return false, nil
		}
	}
	return true, nil
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
		WHERE ur.user_id = ?
		AND (ur.expires_at IS NULL OR ur.expires_at > datetime('now'))
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

// UserHasRole checks if a user has a specific role
func UserHasRole(userID int, roleName string) (bool, error) {
	query := `
		SELECT 1
		FROM user_roles ur
		JOIN roles r ON ur.role_id = r.id
		WHERE ur.user_id = ? AND r.name = ?
		AND (ur.expires_at IS NULL OR ur.expires_at > datetime('now'))
		LIMIT 1
	`

	var exists int
	err := db.QueryRow(query, userID, roleName).Scan(&exists)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}

	return true, nil
}

// GetUserRoles returns all roles for a user
func GetUserRoles(userID int) ([]Role, error) {
	// Check cache first
	roles, cached := permCache.GetRoles(userID)
	if cached {
		return roles, nil
	}

	query := `
		SELECT r.id, r.name, r.display_name, r.description, r.is_system, r.created_at, r.updated_at
		FROM roles r
		JOIN user_roles ur ON r.id = ur.role_id
		WHERE ur.user_id = ?
		AND (ur.expires_at IS NULL OR ur.expires_at > datetime('now'))
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
		VALUES (?, ?, ?, 0)
	`

	result, err := db.Exec(query, name, displayName, description)
	if err != nil {
		return nil, err
	}

	id, err := result.LastInsertId()
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
		WHERE id = ?
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

// GetAllRoles retrieves all roles
func GetAllRoles() ([]Role, error) {
	query := `
		SELECT id, name, display_name, description, is_system, created_at, updated_at
		FROM roles
		ORDER BY is_system DESC, name ASC
	`

	rows, err := db.Query(query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var roles []Role
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
		)
		if err != nil {
			return nil, err
		}
		roles = append(roles, role)
	}

	return roles, nil
}

// UpdateRole updates a role
func UpdateRole(roleID int, displayName, description string) error {
	// Prevent updating system roles
	var isSystem bool
	err := db.QueryRow("SELECT is_system FROM roles WHERE id = ?", roleID).Scan(&isSystem)
	if err != nil {
		return err
	}
	if isSystem {
		return fmt.Errorf("cannot modify system role")
	}

	query := `
		UPDATE roles
		SET display_name = ?, description = ?, updated_at = datetime('now')
		WHERE id = ? AND is_system = 0
	`

	_, err = db.Exec(query, displayName, description, roleID)
	return err
}

// DeleteRole deletes a role
func DeleteRole(roleID int) error {
	// Prevent deleting system roles
	var isSystem bool
	err := db.QueryRow("SELECT is_system FROM roles WHERE id = ?", roleID).Scan(&isSystem)
	if err != nil {
		return err
	}
	if isSystem {
		return fmt.Errorf("cannot delete system role")
	}

	query := "DELETE FROM roles WHERE id = ? AND is_system = 0"
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
		WHERE rp.role_id = ?
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

// AssignPermissionToRole assigns a permission to a role
func AssignPermissionToRole(roleID, permissionID int) error {
	query := `
		INSERT OR IGNORE INTO role_permissions (role_id, permission_id)
		VALUES (?, ?)
	`
	_, err := db.Exec(query, roleID, permissionID)
	return err
}

// RemovePermissionFromRole removes a permission from a role
func RemovePermissionFromRole(roleID, permissionID int) error {
	// Prevent modifying system roles
	var isSystem bool
	err := db.QueryRow("SELECT is_system FROM roles WHERE id = ?", roleID).Scan(&isSystem)
	if err != nil {
		return err
	}
	if isSystem {
		return fmt.Errorf("cannot modify system role permissions")
	}

	query := "DELETE FROM role_permissions WHERE role_id = ? AND permission_id = ?"
	_, err = db.Exec(query, roleID, permissionID)
	return err
}

// ============================================================================
// USER ROLE ASSIGNMENT
// ============================================================================

// AssignRoleToUser assigns a role to a user
func AssignRoleToUser(userID, roleID int, grantedBy *int, expiresAt *string) error {
	query := `
		INSERT OR REPLACE INTO user_roles (user_id, role_id, granted_by, expires_at)
		VALUES (?, ?, ?, ?)
	`

	_, err := db.Exec(query, userID, roleID, grantedBy, expiresAt)
	if err != nil {
		return err
	}

	// Invalidate cache
	permCache.Invalidate(userID)
	return nil
}

// RemoveRoleFromUser removes a role from a user
func RemoveRoleFromUser(userID, roleID int) error {
	query := "DELETE FROM user_roles WHERE user_id = ? AND role_id = ?"
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
