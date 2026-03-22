package main

import (
	"database/sql"
	"fmt"
	"log"
	"strings"
)

// initSchema creates all required tables if they don't exist.
// Uses IF NOT EXISTS - safe to call on every startup.
// No data is modified if tables already exist.
func initSchema(db *sql.DB) error {
	tables := []string{
		// ── Core user management ──
		`CREATE TABLE IF NOT EXISTS users (
			id BIGSERIAL PRIMARY KEY,
			username TEXT NOT NULL UNIQUE,
			password_hash TEXT NOT NULL DEFAULT '',
			display_name TEXT NOT NULL DEFAULT '',
			email TEXT NOT NULL DEFAULT '',
			role TEXT NOT NULL DEFAULT 'user',
			active INTEGER NOT NULL DEFAULT 1,
			must_change_password INTEGER NOT NULL DEFAULT 0,
			source TEXT NOT NULL DEFAULT 'local',
			totp_enabled INTEGER NOT NULL DEFAULT 0,
			last_login TIMESTAMPTZ,
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		)`,

		// ── Session management ──
		`CREATE TABLE IF NOT EXISTS sessions (
			id BIGSERIAL PRIMARY KEY,
			session_id TEXT NOT NULL UNIQUE,
			user_id BIGINT,
			username TEXT NOT NULL,
			ip_address TEXT NOT NULL DEFAULT '',
			user_agent TEXT NOT NULL DEFAULT '',
			created_at BIGINT NOT NULL DEFAULT EXTRACT(EPOCH FROM NOW())::BIGINT,
			expires_at BIGINT,
			last_activity BIGINT NOT NULL DEFAULT EXTRACT(EPOCH FROM NOW())::BIGINT,
			status TEXT NOT NULL DEFAULT 'active',
			FOREIGN KEY (username) REFERENCES users(username)
		)`,

		// ── RBAC: Roles ──
		`CREATE TABLE IF NOT EXISTS roles (
			id BIGSERIAL PRIMARY KEY,
			name TEXT NOT NULL UNIQUE,
			display_name TEXT NOT NULL DEFAULT '',
			description TEXT NOT NULL DEFAULT '',
			is_system INTEGER NOT NULL DEFAULT 0,
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		)`,

		// ── RBAC: Permissions ──
		`CREATE TABLE IF NOT EXISTS permissions (
			id BIGSERIAL PRIMARY KEY,
			resource TEXT NOT NULL,
			action TEXT NOT NULL,
			display_name TEXT NOT NULL DEFAULT '',
			description TEXT NOT NULL DEFAULT '',
			category TEXT NOT NULL DEFAULT 'general',
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			UNIQUE(resource, action)
		)`,

		// ── RBAC: Role-Permission mapping ──
		`CREATE TABLE IF NOT EXISTS role_permissions (
			role_id BIGINT NOT NULL,
			permission_id BIGINT NOT NULL,
			PRIMARY KEY (role_id, permission_id),
			FOREIGN KEY (role_id) REFERENCES roles(id) ON DELETE CASCADE,
			FOREIGN KEY (permission_id) REFERENCES permissions(id) ON DELETE CASCADE
		)`,

		// ── RBAC: User-Role mapping ──
		`CREATE TABLE IF NOT EXISTS user_roles (
			user_id BIGINT NOT NULL,
			role_id BIGINT NOT NULL,
			granted_by TEXT NOT NULL DEFAULT 'system',
			expires_at TIMESTAMPTZ,
			PRIMARY KEY (user_id, role_id),
			FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE,
			FOREIGN KEY (role_id) REFERENCES roles(id) ON DELETE CASCADE
		)`,

		// ── Audit logging ──
		`CREATE TABLE IF NOT EXISTS audit_logs (
			id BIGSERIAL PRIMARY KEY,
			timestamp BIGINT NOT NULL DEFAULT EXTRACT(EPOCH FROM NOW())::BIGINT,
			actor TEXT NOT NULL DEFAULT '',
			action TEXT NOT NULL DEFAULT '',
			resource TEXT NOT NULL DEFAULT '',
			details TEXT NOT NULL DEFAULT '',
			ip_address TEXT NOT NULL DEFAULT '',
			success INTEGER NOT NULL DEFAULT 1,
			prev_hash TEXT NOT NULL DEFAULT '',
			row_hash TEXT NOT NULL DEFAULT ''
		)`,

		// ── Telegram notifications ──
		`CREATE TABLE IF NOT EXISTS telegram_config (
			id BIGINT PRIMARY KEY,
			bot_token TEXT NOT NULL DEFAULT '',
			chat_id TEXT NOT NULL DEFAULT '',
			enabled INTEGER NOT NULL DEFAULT 0,
			updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		)`,

		// ── LDAP/AD configuration ──
		`CREATE TABLE IF NOT EXISTS ldap_config (
			id BIGINT PRIMARY KEY DEFAULT 1,
			enabled INTEGER NOT NULL DEFAULT 0,
			server TEXT NOT NULL DEFAULT '',
			port INTEGER NOT NULL DEFAULT 389,
			use_tls INTEGER NOT NULL DEFAULT 0,
			bind_dn TEXT NOT NULL DEFAULT '',
			bind_password TEXT NOT NULL DEFAULT '',
			base_dn TEXT NOT NULL DEFAULT '',
			user_filter TEXT NOT NULL DEFAULT '(&(objectClass=user)(sAMAccountName={username}))',
			user_id_attr TEXT NOT NULL DEFAULT 'sAMAccountName',
			user_name_attr TEXT NOT NULL DEFAULT 'displayName',
			user_email_attr TEXT NOT NULL DEFAULT 'mail',
			group_base_dn TEXT NOT NULL DEFAULT '',
			group_filter TEXT NOT NULL DEFAULT '(&(objectClass=group)(member={user_dn}))',
			group_member_attr TEXT NOT NULL DEFAULT 'member',
			jit_provisioning INTEGER NOT NULL DEFAULT 0,
			default_role TEXT NOT NULL DEFAULT 'user',
			sync_interval INTEGER NOT NULL DEFAULT 3600,
			timeout INTEGER NOT NULL DEFAULT 10,
			last_test_at TIMESTAMPTZ,
			last_test_ok INTEGER NOT NULL DEFAULT 0,
			last_test_msg TEXT,
			last_sync_at TIMESTAMPTZ,
			last_sync_ok INTEGER NOT NULL DEFAULT 0,
			last_sync_count INTEGER NOT NULL DEFAULT 0,
			last_sync_msg TEXT,
			updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		)`,

		// ── LDAP group-to-role mappings ──
		`CREATE TABLE IF NOT EXISTS ldap_group_mappings (
			id BIGSERIAL PRIMARY KEY,
			ldap_group TEXT NOT NULL,
			role_name TEXT NOT NULL DEFAULT '',
			role_id BIGINT,
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			UNIQUE(ldap_group)
		)`,

		// ── LDAP sync history ──
		`CREATE TABLE IF NOT EXISTS ldap_sync_log (
			id BIGSERIAL PRIMARY KEY,
			sync_type TEXT NOT NULL DEFAULT 'manual',
			success INTEGER NOT NULL DEFAULT 0,
			users_synced INTEGER NOT NULL DEFAULT 0,
			users_created INTEGER NOT NULL DEFAULT 0,
			users_updated INTEGER NOT NULL DEFAULT 0,
			users_disabled INTEGER NOT NULL DEFAULT 0,
			error_msg TEXT NOT NULL DEFAULT '',
			duration_ms BIGINT NOT NULL DEFAULT 0,
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		)`,

		// ── Indexes for performance ──
		`CREATE INDEX IF NOT EXISTS idx_sessions_session_id ON sessions(session_id)`,
		`CREATE INDEX IF NOT EXISTS idx_sessions_expires ON sessions(expires_at)`,

		// Migration: Add columns if missing (handled by initSchema loop)

		// ── Git Sync ──
		`CREATE TABLE IF NOT EXISTS git_sync_config (
			id BIGINT PRIMARY KEY CHECK (id = 1),
			repo_url TEXT NOT NULL DEFAULT '',
			branch TEXT NOT NULL DEFAULT 'main',
			local_path TEXT NOT NULL DEFAULT '/var/lib/dplaneos/git-stacks',
			sync_interval INTEGER NOT NULL DEFAULT 0,
			auto_deploy INTEGER NOT NULL DEFAULT 0,
			auth_type TEXT NOT NULL DEFAULT 'none',
			auth_token TEXT NOT NULL DEFAULT '',
			ssh_key_path TEXT NOT NULL DEFAULT '',
			host_key_mode TEXT NOT NULL DEFAULT 'accept',
			commit_name TEXT NOT NULL DEFAULT 'D-PlaneOS',
			commit_email TEXT NOT NULL DEFAULT 'dplaneos@localhost',
			last_sync_at TIMESTAMPTZ,
			last_commit TEXT,
			last_error TEXT
		)`,
		`INSERT INTO git_sync_config (id) VALUES (1) ON CONFLICT (id) DO NOTHING`,

		// Migration: add auth columns if missing (handled by initSchema loop)

		// ── ACME configuration ──
		`CREATE TABLE IF NOT EXISTS acme_config (
			id BIGINT PRIMARY KEY CHECK (id = 1),
			email TEXT NOT NULL DEFAULT '',
			server TEXT NOT NULL DEFAULT 'https://acme-v02.api.letsencrypt.org/directory',
			resolver TEXT NOT NULL DEFAULT 'http',
			dns_config TEXT NOT NULL DEFAULT '{}', -- JSON map of env vars
			domains TEXT NOT NULL DEFAULT '[]',     -- JSON array
			enabled INTEGER NOT NULL DEFAULT 0,
			updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		)`,
		`INSERT INTO acme_config (id) VALUES (1) ON CONFLICT (id) DO NOTHING`,

		// ── Certificate store ──
		`CREATE TABLE IF NOT EXISTS certificates (
			id BIGSERIAL PRIMARY KEY,
			name TEXT NOT NULL UNIQUE,
			cert_pem TEXT NOT NULL,
			key_pem TEXT NOT NULL,
			is_managed INTEGER NOT NULL DEFAULT 0, -- 1 if ACME-managed
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		)`,

		// ── SMART healthy monitoring/scheduling ──
		`CREATE TABLE IF NOT EXISTS smart_schedules (
			id BIGSERIAL PRIMARY KEY,
			device TEXT NOT NULL,
			test_type TEXT NOT NULL, -- short, long, conveyance, offline
			schedule TEXT NOT NULL,  -- cron expression
			enabled INTEGER NOT NULL DEFAULT 1,
			last_run TIMESTAMPTZ,
			next_run TIMESTAMPTZ,
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			UNIQUE(device, test_type)
		)`,
		`ALTER TABLE git_sync_config ADD COLUMN commit_email TEXT NOT NULL DEFAULT 'dplaneos@localhost'`,
		// ── Git Sync: Multi-Repo Support (v2.1.1) ──
		`CREATE TABLE IF NOT EXISTS git_sync_repos (
			id          BIGSERIAL PRIMARY KEY,
			name        TEXT NOT NULL UNIQUE,
			repo_url    TEXT NOT NULL DEFAULT '',
			branch      TEXT NOT NULL DEFAULT 'main',
			local_path  TEXT NOT NULL DEFAULT '',
			compose_path TEXT NOT NULL DEFAULT 'docker-compose.yml',
			auto_sync   INTEGER NOT NULL DEFAULT 0,
			sync_interval INTEGER NOT NULL DEFAULT 5,
			auth_type   TEXT NOT NULL DEFAULT 'none',
			auth_token  TEXT NOT NULL DEFAULT '',
			ssh_key_path TEXT NOT NULL DEFAULT '',
			host_key_mode TEXT NOT NULL DEFAULT 'accept',
			commit_name TEXT NOT NULL DEFAULT 'D-PlaneOS',
			commit_email TEXT NOT NULL DEFAULT 'dplaneos@localhost',
			last_sync_at TIMESTAMPTZ,
			last_commit TEXT,
			last_error  TEXT,
			enabled     INTEGER NOT NULL DEFAULT 1,
			created_at  TIMESTAMPTZ DEFAULT NOW()
		)`,

		// ── GitHub PAT Store (shared credentials, referenced by name) ──
		`CREATE TABLE IF NOT EXISTS git_credentials (
			id          BIGSERIAL PRIMARY KEY,
			name        TEXT NOT NULL UNIQUE,
			host        TEXT NOT NULL DEFAULT 'github.com',
			auth_type   TEXT NOT NULL DEFAULT 'token',
			token       TEXT NOT NULL DEFAULT '',
			ssh_key     TEXT NOT NULL DEFAULT '',
			notes       TEXT NOT NULL DEFAULT '',
			created_at  TIMESTAMPTZ DEFAULT NOW()
		)`,

		// API Tokens - long-lived bearer tokens for automation/CLI
		`CREATE TABLE IF NOT EXISTS api_tokens (
			id          BIGSERIAL PRIMARY KEY,
			user_id     BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
			name        TEXT NOT NULL,
			token_hash  TEXT NOT NULL UNIQUE,
			token_prefix TEXT NOT NULL,
			scopes      TEXT NOT NULL DEFAULT 'read',
			last_used   TIMESTAMPTZ,
			expires_at  TIMESTAMPTZ,
			created_at  TIMESTAMPTZ DEFAULT NOW(),
			UNIQUE(user_id, name)
		)`,

		// TOTP 2FA secrets
		`CREATE TABLE IF NOT EXISTS totp_secrets (
			user_id     BIGINT PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
			secret      TEXT NOT NULL,
			enabled     INTEGER NOT NULL DEFAULT 0,
			backup_codes TEXT NOT NULL DEFAULT '',
			created_at  TIMESTAMPTZ DEFAULT NOW(),
			verified_at TIMESTAMPTZ
		)`,

		// ── Phase 1: Pre-upgrade snapshots ──
		`CREATE TABLE IF NOT EXISTS pre_upgrade_snapshots (
			id          BIGSERIAL PRIMARY KEY,
			snapshot    TEXT NOT NULL,
			pool        TEXT NOT NULL,
			created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			nixos_apply TEXT NOT NULL DEFAULT '',
			success     INTEGER NOT NULL DEFAULT 1,
			error       TEXT NOT NULL DEFAULT ''
		)`,

		// ── Phase 1: Webhook alerting ──
		`CREATE TABLE IF NOT EXISTS webhook_configs (
			id            BIGSERIAL PRIMARY KEY,
			name          TEXT NOT NULL UNIQUE,
			url           TEXT NOT NULL,
			secret_header TEXT NOT NULL DEFAULT '',
			secret_value  TEXT NOT NULL DEFAULT '',
			content_type  TEXT NOT NULL DEFAULT 'application/json',
			body_template TEXT NOT NULL DEFAULT '',
			enabled       INTEGER NOT NULL DEFAULT 1,
			events        TEXT NOT NULL DEFAULT '[]',
			created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			updated_at    TIMESTAMPTZ NOT NULL DEFAULT NOW()
		)`,

		// Migration: add content_type + body_template to webhook_configs (handled by initSchema loop)

		// ── Phase 1: Audit HMAC chain columns ──
		`ALTER TABLE audit_logs ADD COLUMN prev_hash TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE audit_logs ADD COLUMN row_hash  TEXT NOT NULL DEFAULT ''`,

		// ── Phase 3: GitOps approvals ──
		`CREATE TABLE IF NOT EXISTS gitops_approvals (
			kind        TEXT NOT NULL,
			name        TEXT NOT NULL,
			reason      TEXT NOT NULL DEFAULT '',
			approved_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			PRIMARY KEY (kind, name)
		)`,

		// ── Phase 6: Optional Granular GitOps ──
		`CREATE TABLE IF NOT EXISTS gitops_config (
			id BIGINT PRIMARY KEY CHECK (id = 1),
			enabled INTEGER NOT NULL DEFAULT 0,
			repo_id BIGINT,
			state_path TEXT NOT NULL DEFAULT 'state.yaml',
			sync_storage INTEGER NOT NULL DEFAULT 1,
			sync_access INTEGER NOT NULL DEFAULT 1,
			sync_app INTEGER NOT NULL DEFAULT 1,
			sync_identity INTEGER NOT NULL DEFAULT 1,
			sync_protection INTEGER NOT NULL DEFAULT 1,
			sync_system INTEGER NOT NULL DEFAULT 1,
			nixos_repo_id BIGINT,
			updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			FOREIGN KEY (repo_id) REFERENCES git_sync_repos(id),
			FOREIGN KEY (nixos_repo_id) REFERENCES git_sync_repos(id)
		)`,

		// Migration: add nixos_repo_id to gitops_config (handled by initSchema loop)

		`CREATE INDEX IF NOT EXISTS idx_git_repos_name ON git_sync_repos(name)`,
		`CREATE INDEX IF NOT EXISTS idx_audit_timestamp ON audit_logs(timestamp)`,
		`CREATE INDEX IF NOT EXISTS idx_audit_user ON audit_logs(actor)`,
		`CREATE INDEX IF NOT EXISTS idx_user_roles_user ON user_roles(user_id)`,
		`CREATE INDEX IF NOT EXISTS idx_user_roles_role ON user_roles(role_id)`,
		`CREATE INDEX IF NOT EXISTS idx_role_permissions_role ON role_permissions(role_id)`,

		// ── Migration 010: Disk lifecycle registry ──
		// Tracks every disk ever seen: stable identifiers, pool membership,
		// removal events, and temperature history.
		`CREATE TABLE IF NOT EXISTS disk_registry (
			id           BIGSERIAL PRIMARY KEY,
			dev_name     TEXT NOT NULL,
			by_id_path   TEXT UNIQUE,
			serial       TEXT,
			wwn          TEXT,
			model        TEXT,
			size_bytes   BIGINT,
			disk_type    TEXT,
			pool_name    TEXT,
			health       TEXT NOT NULL DEFAULT 'UNKNOWN',
			last_seen    TIMESTAMPTZ NOT NULL,
			first_seen   TIMESTAMPTZ NOT NULL,
			removed_at   TIMESTAMPTZ,
			temp_c       INTEGER NOT NULL DEFAULT 0,
			UNIQUE(dev_name, by_id_path)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_disk_registry_by_id  ON disk_registry(by_id_path)`,
		`CREATE INDEX IF NOT EXISTS idx_disk_registry_serial ON disk_registry(serial)`,
		// ── System-level key-value configuration ──
		`CREATE TABLE IF NOT EXISTS system_config (
			key TEXT PRIMARY KEY,
			value TEXT NOT NULL,
			updated_at TIMESTAMPTZ DEFAULT NOW()
		)`,
		// ── Settings table (alerting_smtp.go writes here) ──
		`CREATE TABLE IF NOT EXISTS settings (
			key TEXT PRIMARY KEY,
			value TEXT NOT NULL,
			updated_at TIMESTAMPTZ DEFAULT NOW()
		)`,
		// ── SMB Shares ──
		`CREATE TABLE IF NOT EXISTS smb_shares (
			id BIGSERIAL PRIMARY KEY,
			name TEXT NOT NULL UNIQUE,
			path TEXT NOT NULL,
			comment TEXT DEFAULT '',
			browsable INTEGER DEFAULT 1,
			read_only INTEGER DEFAULT 0,
			guest_ok INTEGER DEFAULT 0,
			valid_users TEXT DEFAULT '',
			write_list TEXT DEFAULT '',
			create_mask TEXT DEFAULT '0664',
			directory_mask TEXT DEFAULT '0775',
			enabled INTEGER DEFAULT 1,
			created_at TIMESTAMPTZ DEFAULT NOW(),
			updated_at TIMESTAMPTZ DEFAULT NOW()
		)`,
		// ── NFS Exports ──
		`CREATE TABLE IF NOT EXISTS nfs_exports (
			id        BIGSERIAL PRIMARY KEY,
			path      TEXT NOT NULL,
			clients   TEXT NOT NULL DEFAULT '*',
			options   TEXT NOT NULL DEFAULT 'rw,sync,no_subtree_check,no_root_squash',
			enabled   INTEGER NOT NULL DEFAULT 1,
			created_at TIMESTAMPTZ DEFAULT NOW(),
			updated_at TIMESTAMPTZ DEFAULT NOW()
		)`,
		// ── Groups & Members ──
		`CREATE TABLE IF NOT EXISTS groups (
			id BIGSERIAL PRIMARY KEY,
			name TEXT NOT NULL UNIQUE,
			description TEXT DEFAULT '',
			gid INTEGER,
			created_at TIMESTAMPTZ DEFAULT NOW()
		)`,
		`CREATE TABLE IF NOT EXISTS group_members (
			group_name TEXT,
			username TEXT,
			PRIMARY KEY (group_name, username),
			FOREIGN KEY (group_name) REFERENCES groups(name) ON DELETE CASCADE,
			FOREIGN KEY (username) REFERENCES users(username) ON DELETE CASCADE
		)`,
	}

	for _, stmt := range tables {
		if strings.Contains(stmt, "ALTER TABLE") {
			// Wrap ALTER TABLE in a DO block to make it idempotent
			// Extract table and column name for better logging if needed, 
			// but here we just wrap the provided statement.
			stmt = fmt.Sprintf(`DO $$ BEGIN %s; EXCEPTION WHEN duplicate_column THEN NULL; END $$;`, 
				strings.TrimSuffix(stmt, ";"))
		}
		if _, err := db.Exec(stmt); err != nil {
			displayStmt := stmt
			if len(stmt) > 80 {
				displayStmt = stmt[:80] + "..."
			}
			return fmt.Errorf("schema init failed: %w\nStatement: %s", err, displayStmt)
		}
	}

	// Seed default data if tables are empty
	if err := seedDefaults(db); err != nil {
		return fmt.Errorf("seed defaults failed: %w", err)
	}

	return nil
}

// seedDefaults populates essential data on first run only.
func seedDefaults(db *sql.DB) error {
	// ── Ensure LDAP config row exists ──
	var ldapCount int
	db.QueryRow("SELECT COUNT(*) FROM ldap_config").Scan(&ldapCount)
	if ldapCount == 0 {
		if _, err := db.Exec("INSERT INTO ldap_config (id) VALUES (1)"); err != nil {
			return fmt.Errorf("ldap default: %w", err)
		}
	}

	// ── Ensure telegram config row exists ──
	var telegramCount int
	db.QueryRow("SELECT COUNT(*) FROM telegram_config").Scan(&telegramCount)
	if telegramCount == 0 {
		if _, err := db.Exec("INSERT INTO telegram_config (id, bot_token, chat_id, enabled) VALUES (1, '', '', 0)"); err != nil {
			return fmt.Errorf("telegram default: %w", err)
		}
	}

	// ── Seed built-in roles ──
	var roleCount int
	db.QueryRow("SELECT COUNT(*) FROM roles").Scan(&roleCount)
	if roleCount == 0 {
		roles := []struct {
			name, display, desc string
		}{
			{"admin", "Administrator", "Full system access"},
			{"operator", "Operator", "Manage services and storage"},
			{"user", "User", "Read storage, manage own files"},
			{"viewer", "Viewer", "Read-only access"},
		}
		for _, r := range roles {
			if _, err := db.Exec(
				"INSERT INTO roles (name, display_name, description, is_system) VALUES ($1, $2, $3, 1) ON CONFLICT (name) DO NOTHING",
				r.name, r.display, r.desc,
			); err != nil {
				return fmt.Errorf("role seed %s: %w", r.name, err)
			}
		}
		log.Printf("Seeded %d built-in RBAC roles", len(roles))
	}

	// ── Seed built-in permissions ──
	var permCount int
	db.QueryRow("SELECT COUNT(*) FROM permissions").Scan(&permCount)
	if permCount == 0 {
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
			// System
			{"system", "read", "View System", "View system settings and logs", "system"},
			{"system", "write", "Manage System", "Modify system settings", "system"},
			{"system", "admin", "System Admin", "Full system administration", "system"},
			{"monitoring", "read", "View Monitoring", "View system metrics and health", "system"},
			{"audit", "read", "View Audit Logs", "Access audit trail", "security"},
			{"certificates", "read", "View Certificates", "List SSL certificates", "security"},
			{"certificates", "write", "Manage Certificates", "Create and install certificates", "security"},
		}
		for _, p := range perms {
			if _, err := db.Exec(
				"INSERT INTO permissions (resource, action, display_name, description, category) VALUES ($1, $2, $3, $4, $5) ON CONFLICT (resource, action) DO NOTHING",
				p.resource, p.action, p.display, p.desc, p.category,
			); err != nil {
				return fmt.Errorf("perm seed %s:%s: %w", p.resource, p.action, err)
			}
		}
		log.Printf("Seeded %d built-in permissions", len(perms))

		// ── Assign all permissions to admin role ──
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
		if _, err := db.Exec(
			"INSERT INTO users (username, display_name, email, active) VALUES ('admin', 'Administrator', 'admin@localhost', 1)",
		); err != nil {
			return fmt.Errorf("admin user seed: %w", err)
		}

		// Assign admin role
		var adminRoleID, adminUserID int
		db.QueryRow("SELECT id FROM roles WHERE name = 'admin'").Scan(&adminRoleID)
		db.QueryRow("SELECT id FROM users WHERE username = 'admin'").Scan(&adminUserID)
		if adminRoleID > 0 && adminUserID > 0 {
			db.Exec("INSERT INTO user_roles (user_id, role_id, granted_by) VALUES ($1, $2, 'system') ON CONFLICT DO NOTHING", adminUserID, adminRoleID)
		}
		log.Printf("Created default admin user")
	}

	// ── Ensure gitops config row exists ──
	var gitopsCount int
	db.QueryRow("SELECT COUNT(*) FROM gitops_config").Scan(&gitopsCount)
	if gitopsCount == 0 {
		if _, err := db.Exec("INSERT INTO gitops_config (id, enabled) VALUES (1, 0)"); err != nil {
			return fmt.Errorf("gitops default: %w", err)
		}
	}

	// ── Ensure default system settings ──
	db.Exec(`INSERT INTO system_config (key, value) VALUES ('audit_retention_days', '90') ON CONFLICT (key) DO NOTHING`)

	return nil
}
