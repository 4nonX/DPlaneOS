package main

import (
	"database/sql"
	"fmt"
	"log"
	"os"
	"strings"
)

// seedDefaults populates essential data on first run only.
// Schema creation is handled by RunMigrations (internal/database/migrate.go).
func seedDefaults(db *sql.DB) error {
	// ── Ensure singleton config rows exist ──
	singletons := []struct {
		table string
		stmt  string
	}{
		{"ldap_config", "INSERT INTO ldap_config (id) VALUES (1) ON CONFLICT (id) DO NOTHING"},
		{"telegram_config", "INSERT INTO telegram_config (id, bot_token, chat_id, enabled) VALUES (1, '', '', 0) ON CONFLICT (id) DO NOTHING"},
		{"git_sync_config", "INSERT INTO git_sync_config (id) VALUES (1) ON CONFLICT (id) DO NOTHING"},
		{"acme_config", "INSERT INTO acme_config (id) VALUES (1) ON CONFLICT (id) DO NOTHING"},
		{"gitops_config", "INSERT INTO gitops_config (id, enabled) VALUES (1, 0) ON CONFLICT (id) DO NOTHING"},
	}
	for _, s := range singletons {
		if _, err := db.Exec(s.stmt); err != nil {
			return fmt.Errorf("singleton seed %s: %w", s.table, err)
		}
	}

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
		{"snapshots", "read", "View Snapshots", "List snapshots and schedules", "storage"},
		{"snapshots", "write", "Manage Snapshots", "Create and schedule snapshots", "storage"},
		{"shares", "read", "View Shares", "List shared folders", "storage"},
		{"shares", "write", "Manage Shares", "Create and modify shares", "storage"},
		{"files", "read", "Browse Files", "Browse and download files", "storage"},
		{"files", "write", "Manage Files", "Upload, move, and delete files", "storage"},
		// Compute
		{"docker", "read", "View Containers", "List containers and images", "compute"},
		{"docker", "write", "Manage Containers", "Start, stop, create containers", "compute"},
		{"docker", "delete", "Remove Containers", "Delete containers and images", "compute"},
		// Network
		{"network", "read", "View Network", "View network configuration", "network"},
		{"network", "write", "Manage Network", "Modify network settings", "network"},
		{"firewall", "read", "View Firewall", "View firewall rules", "network"},
		{"firewall", "write", "Manage Firewall", "Add and modify firewall rules", "network"},
		// Identity
		{"users", "read", "View Users", "List users and groups", "identity"},
		{"users", "write", "Manage Users", "Create and modify users", "identity"},
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

		// Assign all permissions to the admin role (only on first seed)
		var adminID int
		if err := db.QueryRow("SELECT id FROM roles WHERE name = 'admin'").Scan(&adminID); err == nil {
			rows, _ := db.Query("SELECT id FROM permissions")
			if rows != nil {
				defer rows.Close()
				for rows.Next() {
					var pid int
					rows.Scan(&pid)
					db.Exec("INSERT INTO role_permissions (role_id, permission_id) VALUES ($1, $2) ON CONFLICT DO NOTHING", adminID, pid)
				}
				log.Printf("Assigned all permissions to admin role")
			}
		}
	}

	// ── Ensure admin user exists ──
	var userCount int
	db.QueryRow("SELECT COUNT(*) FROM users").Scan(&userCount)
	if userCount == 0 {
		passwordHash := ""
		const firstBootPassPath = "/var/lib/dplaneos/.first-boot-password"
		if data, err := os.ReadFile(firstBootPassPath); err == nil {
			passwordHash = strings.TrimSpace(string(data))
			log.Printf("BOOTSTRAP: Found first-boot password hash, seeding admin user")
			_ = os.Remove(firstBootPassPath)
		}

		if _, err := db.Exec(
			"INSERT INTO users (username, display_name, email, password_hash, active) VALUES ('admin', 'Administrator', 'admin@localhost', $1, 1)",
			passwordHash,
		); err != nil {
			return fmt.Errorf("admin user seed: %w", err)
		}

		var adminRoleID, adminUserID int
		db.QueryRow("SELECT id FROM roles WHERE name = 'admin'").Scan(&adminRoleID)
		db.QueryRow("SELECT id FROM users WHERE username = 'admin'").Scan(&adminUserID)
		if adminRoleID > 0 && adminUserID > 0 {
			db.Exec("INSERT INTO user_roles (user_id, role_id, granted_by) VALUES ($1, $2, 'system') ON CONFLICT DO NOTHING", adminUserID, adminRoleID)
		}
		log.Printf("Created default admin user")
	}

	// ── Ensure default system settings ──
	db.Exec(`INSERT INTO system_config (key, value) VALUES ('audit_retention_days', '90') ON CONFLICT (key) DO NOTHING`)

	return nil
}
