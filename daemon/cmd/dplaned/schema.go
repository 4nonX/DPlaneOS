package main

import (
	"database/sql"
	"fmt"
	"log"
	"os"
	"strings"

	"dplaned/internal/security"
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

	// ── Built-in RBAC roles and permissions ──
	if err := security.SeedRBAC(db); err != nil {
		return err
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
			"INSERT INTO users (username, display_name, email, password_hash, role, active) VALUES ('admin', 'Administrator', 'admin@localhost', $1, 'admin', 1)",
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
