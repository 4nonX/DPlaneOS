package handlers

import (
	"os"
	"path/filepath"
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

// A system that shows a setup code on its console (the live image) gives the
// administrator account to whoever can read that console, not to whoever
// reaches the web interface first.
func TestSetupAdminNeedsTheConsoleCode(t *testing.T) {
	db := testDB(t)
	h := NewSystemStatusHandler(db, "test")
	prev := setupCodePath
	setupCodePath = filepath.Join(t.TempDir(), "setup-code")
	t.Cleanup(func() { setupCodePath = prev })
	if err := os.WriteFile(setupCodePath, []byte("1234-5678\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO users (username, password_hash, role, active) VALUES ('admin', '', 'admin', 1)`); err != nil {
		t.Fatal(err)
	}
	if st := call(t, h.HandleStatus, req{}); st.body["setup_code_required"] != true {
		t.Errorf("status does not ask for the code: %s", st)
	}
	for name, code := range map[string]string{"none": "", "wrong": "0000-0000", "part": "1234"} {
		r := call(t, h.HandleSetupAdmin, req{method: "POST", body: map[string]any{"username": "admin", "password": "Boss-Passw0rd", "setup_code": code}})
		if r.code != 403 || r.body["code"] != "setup_code" {
			t.Errorf("%s code: %s", name, r)
		}
	}
	var hash string
	_ = db.QueryRow(`SELECT password_hash FROM users WHERE username = 'admin'`).Scan(&hash)
	if hash != "" {
		t.Fatal("the administrator was set up without the code")
	}
	// As shown, or typed without the dash.
	if r := call(t, h.HandleSetupAdmin, req{method: "POST", body: map[string]any{"username": "admin", "password": "Boss-Passw0rd", "setup_code": "12345678"}}); !r.ok() {
		t.Fatalf("with the code: %s", r)
	}
	// The password rules of the system apply here too.
	db2 := testDB(t)
	h2 := NewSystemStatusHandler(db2, "test")
	if r := call(t, h2.HandleSetupAdmin, req{method: "POST", body: map[string]any{"username": "admin", "password": "alllowercase", "setup_code": "1234-5678"}}); r.code != 400 {
		t.Errorf("a password without the required characters: %s", r)
	}
}
