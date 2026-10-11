package main

import (
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/crypto/bcrypt"
)

// The way back in after a lost password: a hash left on the system disk by
// the installation medium.
func TestAdminPasswordReset(t *testing.T) {
	db := middlewareDB(t)
	if err := seedDefaults(db); err != nil {
		t.Fatal(err)
	}
	old := adminResetPath
	adminResetPath = filepath.Join(t.TempDir(), "reset")
	t.Cleanup(func() { adminResetPath = old })

	// No file: nothing happens.
	if err := applyAdminPasswordReset(db); err != nil {
		t.Fatal(err)
	}

	// An administrator who is locked out in every way there is, a session
	// somebody else may hold, and a second user who must not be touched.
	var adminID, otherID int64
	if err := db.QueryRow(`UPDATE users SET password_hash = 'x', active = 0, must_change_password = 1, totp_enabled = 1,
		scram_salt = 's', scram_iterations = 5, scram_stored_key = 'k', nt_hash = 'n', unix_hash = 'u'
		WHERE username = 'admin' RETURNING id`).Scan(&adminID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO totp_secrets (user_id, secret, enabled) VALUES ($1, 'sealed', 1)`, adminID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO sessions (session_id, user_id, username) VALUES ('s1', $1, 'admin')`, adminID); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`INSERT INTO users (username, password_hash, role, active) VALUES ('other', 'keep', 'user', 1) RETURNING id`).Scan(&otherID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO sessions (session_id, user_id, username) VALUES ('s2', $1, 'other')`, otherID); err != nil {
		t.Fatal(err)
	}

	hash, _ := bcrypt.GenerateFromPassword([]byte("New-Passw0rd!"), bcrypt.MinCost)
	if err := os.WriteFile(adminResetPath, append(hash, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := applyAdminPasswordReset(db); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(adminResetPath); !os.IsNotExist(err) {
		t.Error("the file is still there: the reset would be applied at every start")
	}

	var stored, scram, nt string
	var active, mustChange, totp, secrets, sessions int
	if err := db.QueryRow(`SELECT password_hash, active, must_change_password, totp_enabled, scram_stored_key, nt_hash FROM users WHERE id = $1`, adminID).
		Scan(&stored, &active, &mustChange, &totp, &scram, &nt); err != nil {
		t.Fatal(err)
	}
	if bcrypt.CompareHashAndPassword([]byte(stored), []byte("New-Passw0rd!")) != nil || active != 1 || mustChange != 0 || totp != 0 || scram != "" || nt != "" {
		t.Errorf("after the reset: active=%d must_change=%d totp=%d scram=%q nt=%q", active, mustChange, totp, scram, nt)
	}
	_ = db.QueryRow(`SELECT COUNT(*) FROM totp_secrets WHERE user_id = $1`, adminID).Scan(&secrets)
	_ = db.QueryRow(`SELECT COUNT(*) FROM sessions WHERE user_id = $1`, adminID).Scan(&sessions)
	if secrets != 0 || sessions != 0 {
		t.Errorf("second factor (%d) or sessions (%d) of the administrator survived", secrets, sessions)
	}
	// The other user is as before.
	var otherHash string
	_ = db.QueryRow(`SELECT password_hash FROM users WHERE id = $1`, otherID).Scan(&otherHash)
	_ = db.QueryRow(`SELECT COUNT(*) FROM sessions WHERE user_id = $1`, otherID).Scan(&sessions)
	if otherHash != "keep" || sessions != 1 {
		t.Errorf("another user was touched: %q, %d sessions", otherHash, sessions)
	}

	// A file that is not a password hash changes nothing, and is removed.
	if err := os.WriteFile(adminResetPath, []byte("plain text password\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := applyAdminPasswordReset(db); err == nil {
		t.Error("something that is not a hash was accepted")
	}
	if _, err := os.Stat(adminResetPath); !os.IsNotExist(err) {
		t.Error("the unusable file is still there")
	}
	_ = db.QueryRow(`SELECT password_hash FROM users WHERE id = $1`, adminID).Scan(&stored)
	if bcrypt.CompareHashAndPassword([]byte(stored), []byte("New-Passw0rd!")) != nil {
		t.Error("the password changed")
	}

	// No administrator left at all: there is one again.
	if _, err := db.Exec(`UPDATE users SET role = 'user' WHERE id = $1`, adminID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE users SET username = 'renamed' WHERE id = $1`, adminID); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(adminResetPath, hash, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := applyAdminPasswordReset(db); err != nil {
		t.Fatal(err)
	}
	var role string
	var roles int
	if err := db.QueryRow(`SELECT role FROM users WHERE username = 'admin'`).Scan(&role); err != nil || role != "admin" {
		t.Fatalf("no administrator after the reset: %q %v", role, err)
	}
	_ = db.QueryRow(`SELECT COUNT(*) FROM user_roles ur JOIN roles r ON r.id = ur.role_id JOIN users u ON u.id = ur.user_id
		WHERE u.username = 'admin' AND r.name = 'admin'`).Scan(&roles)
	if roles != 1 {
		t.Error("the new administrator has no administrator permissions")
	}
}
