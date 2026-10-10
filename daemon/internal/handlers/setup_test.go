package handlers

import (
	"testing"

	"golang.org/x/crypto/bcrypt"
)

func TestSetupAdmin(t *testing.T) {
	db := testDB(t)
	h := NewSystemStatusHandler(db, "test")
	// The startup seed: an "admin" account without a password.
	if _, err := db.Exec(`INSERT INTO users (username, password_hash, role, active) VALUES ('admin', '', 'admin', 1)`); err != nil {
		t.Fatal(err)
	}
	if r := call(t, h.HandleSetupAdmin, req{method: "POST", body: map[string]any{"username": "boss", "password": "short"}}); r.code != 400 {
		t.Errorf("short password: %s", r)
	}
	if r := call(t, h.HandleSetupAdmin, req{method: "POST", body: map[string]any{"username": "boss", "password": "Boss-Passw0rd"}}); !r.ok() {
		t.Fatalf("setup admin: %s", r)
	}
	var hash string
	_ = db.QueryRow(`SELECT password_hash FROM users WHERE username = 'boss'`).Scan(&hash)
	if bcrypt.CompareHashAndPassword([]byte(hash), []byte("Boss-Passw0rd")) != nil {
		t.Fatal("password not set")
	}
	// A second call (anyone on the network, before setup is finished) must
	// not replace the password.
	if r := call(t, h.HandleSetupAdmin, req{method: "POST", body: map[string]any{"username": "evil", "password": "Evil-Passw0rd"}}); r.code != 409 {
		t.Errorf("second setup-admin: %s", r)
	}
	_ = db.QueryRow(`SELECT password_hash FROM users WHERE username = 'boss'`).Scan(&hash)
	if bcrypt.CompareHashAndPassword([]byte(hash), []byte("Boss-Passw0rd")) != nil {
		t.Error("password replaced")
	}
}

// The installer seeds the admin with a password: it is never overwritten.
func TestSetupAdminKeepsInstallerPassword(t *testing.T) {
	db := testDB(t)
	h := NewSystemStatusHandler(db, "test")
	pw, _ := bcrypt.GenerateFromPassword([]byte("Installer-Passw0rd"), bcrypt.MinCost)
	if _, err := db.Exec(`INSERT INTO users (username, password_hash, role, active) VALUES ('admin', $1, 'admin', 1)`, string(pw)); err != nil {
		t.Fatal(err)
	}
	r := call(t, h.HandleSetupAdmin, req{method: "POST", body: map[string]any{"username": "admin", "password": "Other-Passw0rd"}})
	if r.code != 409 || r.body["code"] != "admin_password_set" {
		t.Fatalf("setup-admin over an installer password: %s", r)
	}
	var hash string
	_ = db.QueryRow(`SELECT password_hash FROM users WHERE username = 'admin'`).Scan(&hash)
	if hash != string(pw) {
		t.Error("installer password replaced")
	}
}

func TestSetupAdminAfterSetup(t *testing.T) {
	db := testDB(t)
	h := NewSystemStatusHandler(db, "test")
	if _, err := db.Exec(`INSERT INTO system_config (key, value) VALUES ('setup_complete', '1')`); err != nil {
		t.Fatal(err)
	}
	if r := call(t, h.HandleSetupAdmin, req{method: "POST", body: map[string]any{"username": "x", "password": "X-Passw0rd-123"}}); r.code != 403 {
		t.Errorf("after setup: %s", r)
	}
}
