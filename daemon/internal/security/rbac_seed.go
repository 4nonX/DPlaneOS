package security

import (
	"database/sql"
	"fmt"
	"log"
)

// SeedRBAC creates the built-in roles and permissions (idempotent; run at
// every start): the admin role gets every permission, the other built-in
// roles their defaults while they have none.
func SeedRBAC(db *sql.DB) error {
	// ── Seed built-in roles (idempotent) ──
	roles := []struct {
		name, display, desc string
	}{
		{"admin", "Administrator", "Full system access"},
		{"operator", "Operator", "Manage services and storage"},
		{"user", "User", "Read storage, manage own files"},
		{"viewer", "Viewer", "Read-only access"},
	}
	var seededRoles int
	for _, r := range roles {
		res, err := db.Exec(
			"INSERT INTO roles (name, display_name, description, is_system) VALUES ($1, $2, $3, 1) ON CONFLICT (name) DO NOTHING",
			r.name, r.display, r.desc,
		)
		if err != nil {
			return fmt.Errorf("role seed %s: %w", r.name, err)
		}
		if n, _ := res.RowsAffected(); n > 0 {
			seededRoles++
		}
	}
	if seededRoles > 0 {
		log.Printf("Seeded %d built-in RBAC roles", seededRoles)
	}

	// ── Seed built-in permissions (idempotent) ──
	perms := []struct {
		resource, action, display, desc, category string
	}{
		// Storage
		{"storage", "read", "View Storage", "View pools, datasets, and shares", "storage"},
		{"storage", "write", "Manage Storage", "Create and modify pools and datasets", "storage"},
		{"storage", "delete", "Delete Storage", "Destroy pools and datasets", "storage"},
		{"storage", "admin", "Storage Admin", "Advanced storage operations", "storage"},
		{"snapshots", "read", "View Snapshots", "List snapshots and schedules", "storage"},
		{"snapshots", "write", "Manage Snapshots", "Create and schedule snapshots", "storage"},
		{"shares", "read", "View Shares", "List shared folders", "storage"},
		{"shares", "write", "Manage Shares", "Create and modify shares", "storage"},
		{"shares", "admin", "Shares Admin", "Advanced share management", "storage"},
		{"files", "read", "Browse Files", "Browse and download files", "storage"},
		{"files", "write", "Manage Files", "Upload, move, and delete files", "storage"},
		// Compute
		{"docker", "read", "View Containers", "List containers and images", "compute"},
		{"docker", "write", "Manage Containers", "Start, stop, create containers", "compute"},
		{"docker", "delete", "Remove Containers", "Delete containers and images", "compute"},
		{"docker", "admin", "Docker Admin", "Advanced container operations", "compute"},
		// Network
		{"network", "read", "View Network", "View network configuration", "network"},
		{"network", "write", "Manage Network", "Modify network settings", "network"},
		{"firewall", "read", "View Firewall", "View firewall rules", "network"},
		{"firewall", "write", "Manage Firewall", "Add and modify firewall rules", "network"},
		// Identity
		{"users", "read", "View Users", "List users and groups", "identity"},
		{"users", "write", "Manage Users", "Create and modify users", "identity"},
		{"users", "admin", "Users Admin", "Advanced user management", "identity"},
		{"roles", "read", "View Roles", "List roles and permissions", "identity"},
		{"roles", "write", "Manage Roles", "Assign and modify roles", "identity"},
		// System / Security
		{"system", "read", "View System", "View system settings and logs", "system"},
		{"system", "write", "Manage System", "Modify system settings", "system"},
		{"system", "admin", "System Admin", "Full system administration", "system"},
		{"monitoring", "read", "View Monitoring", "View system metrics and health", "system"},
		{"audit", "read", "View Audit Logs", "Access audit trail", "security"},
		{"certificates", "read", "View Certificates", "List SSL certificates", "security"},
		{"certificates", "write", "Manage Certificates", "Create and install certificates", "security"},
	}
	var seededPerms int
	for _, p := range perms {
		res, err := db.Exec(
			"INSERT INTO permissions (resource, action, display_name, description, category) VALUES ($1, $2, $3, $4, $5) ON CONFLICT (resource, action) DO NOTHING",
			p.resource, p.action, p.display, p.desc, p.category,
		)
		if err != nil {
			return fmt.Errorf("perm seed %s:%s: %w", p.resource, p.action, err)
		}
		if n, _ := res.RowsAffected(); n > 0 {
			seededPerms++
		}
	}
	if seededPerms > 0 {
		log.Printf("Seeded %d built-in permissions", seededPerms)
	}

	// The admin role has every permission, also ones added by an update.
	if _, err := db.Exec(`INSERT INTO role_permissions (role_id, permission_id)
		SELECT r.id, p.id FROM roles r CROSS JOIN permissions p WHERE r.name = 'admin'
		ON CONFLICT DO NOTHING`); err != nil {
		return fmt.Errorf("admin role permissions: %w", err)
	}
	// The other built-in roles get their defaults while they have none (an
	// admin's later changes are kept).
	for role, allowed := range BuiltinRolePermissions {
		var roleID, n int
		if err := db.QueryRow(`SELECT r.id, (SELECT COUNT(*) FROM role_permissions rp WHERE rp.role_id = r.id) FROM roles r WHERE r.name = $1`, role).Scan(&roleID, &n); err != nil || n > 0 {
			continue
		}
		rows, err := db.Query(`SELECT id, resource, action FROM permissions`)
		if err != nil {
			return fmt.Errorf("role %s permissions: %w", role, err)
		}
		var ids []int
		for rows.Next() {
			var id int
			var res, act string
			if rows.Scan(&id, &res, &act) == nil && allowed(res, act) {
				ids = append(ids, id)
			}
		}
		rows.Close()
		for _, id := range ids {
			if _, err := db.Exec(`INSERT INTO role_permissions (role_id, permission_id) VALUES ($1, $2) ON CONFLICT DO NOTHING`, roleID, id); err != nil {
				return fmt.Errorf("role %s permissions: %w", role, err)
			}
		}
	}

	return nil
}
