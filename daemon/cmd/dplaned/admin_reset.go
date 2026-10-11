package main

import (
	"database/sql"
	"errors"
	"fmt"
	"log"
	"os"
	"strings"

	"dplaned/internal/audit"
)

// adminResetPath is where the installation medium leaves a new password
// for the administrator of an installed system ("Reset the administrator
// password" in its menu): one line, a bcrypt hash. Whoever can write this
// file has the machine in front of them and its system disk in their hands.
var adminResetPath = "/var/lib/dplaneos/.reset-admin-password"

// applyAdminPasswordReset gives the administrator the password left by the
// installation medium, once, and removes the file. The way back in when
// the password is lost:
//
//   - the account is "admin" if there is one; otherwise the oldest local
//     administrator; if there is none at all, "admin" is created;
//   - the account is active again, without two-factor sign-in (the second
//     factor may be what was lost) and without the duty to change the
//     password; every session of it ends;
//   - nothing else changes: other accounts, roles and data stay.
func applyAdminPasswordReset(db *sql.DB) error {
	data, err := os.ReadFile(adminResetPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("reading %s: %w", adminResetPath, err)
	}
	hash := strings.TrimSpace(string(data))
	// The file is used once, whatever is in it.
	if err := os.Remove(adminResetPath); err != nil {
		return fmt.Errorf("the password reset was not applied: %s cannot be removed (%w), and a reset that stays would be applied at every start", adminResetPath, err)
	}
	if !strings.HasPrefix(hash, "$2") || len(hash) != 60 {
		return fmt.Errorf("the password reset was not applied: %s did not contain a password hash", adminResetPath)
	}

	var id int64
	var name string
	err = db.QueryRow(`SELECT id, username FROM users WHERE role = 'admin' AND COALESCE(source, 'local') = 'local'
		ORDER BY (username = 'admin') DESC, id LIMIT 1`).Scan(&id, &name)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		name = "admin"
		if err := db.QueryRow(`INSERT INTO users (username, display_name, email, password_hash, role, active)
			VALUES ('admin', 'Administrator', 'admin@localhost', $1, 'admin', 1)
			ON CONFLICT (username) DO UPDATE SET password_hash = EXCLUDED.password_hash, role = 'admin', source = 'local', active = 1
			RETURNING id`, hash).Scan(&id); err != nil {
			return fmt.Errorf("password reset: %w", err)
		}
	case err != nil:
		return fmt.Errorf("password reset: %w", err)
	}

	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck
	// The hashes derived from the old password go with it; they are made
	// again at the first sign-in with the new one.
	if _, err := tx.Exec(`UPDATE users SET password_hash = $2, active = 1, must_change_password = 0, totp_enabled = 0,
		scram_salt = '', scram_iterations = 0, scram_stored_key = '', scram_server_key = '',
		nt_hash = '', unix_hash = '', updated_at = NOW() WHERE id = $1`, id, hash); err != nil {
		return fmt.Errorf("password reset: %w", err)
	}
	if _, err := tx.Exec(`DELETE FROM totp_secrets WHERE user_id = $1`, id); err != nil {
		return fmt.Errorf("password reset: %w", err)
	}
	if _, err := tx.Exec(`DELETE FROM sessions WHERE user_id = $1`, id); err != nil {
		return fmt.Errorf("password reset: %w", err)
	}
	if _, err := tx.Exec(`INSERT INTO user_roles (user_id, role_id, granted_by)
		SELECT $1, id, 'system' FROM roles WHERE name = 'admin' ON CONFLICT DO NOTHING`, id); err != nil {
		return fmt.Errorf("password reset: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	log.Printf("RECOVERY: the password of %q was reset from the installation medium; its sessions were ended and two-factor sign-in switched off for it", name)
	audit.LogAction("admin_password_reset", name, "Password reset from the installation medium (file left on the system disk)", true, 0)
	return nil
}
