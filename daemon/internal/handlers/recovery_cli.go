package handlers

import (
	"database/sql"
	"errors"
	"fmt"

	"dplaned/internal/scram"

	"golang.org/x/crypto/bcrypt"
)

// Console recovery (dplaned -recover-..., used by dplaneos-recovery) for an
// administrator who cannot log in. The former recovery script wrote a Python
// bcrypt hash for a user literally named "admin" over TCP and left the SCRAM
// keys (which the login checks) and the SMB password as they were.

// RecoveryUser is one line of RecoverListUsers.
type RecoveryUser struct {
	Username string
	Role     string
	Active   bool
	TOTP     bool
}

// RecoverListUsers lists the local users, administrators first.
func RecoverListUsers(db *sql.DB) ([]RecoveryUser, error) {
	rows, err := db.Query(`SELECT u.username, u.role, u.active = 1,
		COALESCE(u.totp_enabled, 0) = 1 OR EXISTS (SELECT 1 FROM totp_secrets t WHERE t.user_id = u.id AND t.enabled = 1)
		FROM users u WHERE u.source = 'local' ORDER BY (u.role = 'admin') DESC, u.username`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RecoveryUser
	for rows.Next() {
		var u RecoveryUser
		if err := rows.Scan(&u.Username, &u.Role, &u.Active, &u.TOTP); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// RecoverResetPassword sets a local user's password the way the web UI
// does (bcrypt, SCRAM keys, SMB password), activates the account and ends
// its sessions.
func RecoverResetPassword(db *sql.DB, username, password string) error {
	if ok, msg := validatePasswordStrength(password); !ok {
		return errors.New(msg)
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	keys, err := scram.Derive(password)
	if err != nil {
		return err
	}
	res, err := db.Exec(`UPDATE users SET password_hash = $1, scram_salt = $2, scram_iterations = $3,
		scram_stored_key = $4, scram_server_key = $5, must_change_password = 0, active = 1, updated_at = NOW()
		WHERE username = $6 AND source = 'local'`,
		string(hash), scram.EncodeBase64(keys.Salt), keys.Iterations,
		scram.EncodeBase64(keys.StoredKey), scram.EncodeBase64(keys.ServerKey), username)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("no local user %q", username)
	}
	setSMBPassword(db, username, password)
	if _, err := db.Exec(`DELETE FROM sessions WHERE username = $1`, username); err != nil {
		return fmt.Errorf("password set, but its sessions were not ended: %w", err)
	}
	if w := syncShareIdentities(db); w != "" {
		return errors.New("password set; " + w)
	}
	return nil
}

// RecoverDisableTOTP turns off two-factor authentication for a user who
// lost the device (and ends the user's sessions).
func RecoverDisableTOTP(db *sql.DB, username string) error {
	var id int64
	if err := db.QueryRow(`SELECT id FROM users WHERE username = $1`, username).Scan(&id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("no user %q", username)
		}
		return err
	}
	if _, err := db.Exec(`DELETE FROM totp_secrets WHERE user_id = $1`, id); err != nil {
		return err
	}
	if _, err := db.Exec(`UPDATE users SET totp_enabled = 0, updated_at = NOW() WHERE id = $1`, id); err != nil {
		return err
	}
	_, err := db.Exec(`DELETE FROM sessions WHERE username = $1`, username)
	return err
}
