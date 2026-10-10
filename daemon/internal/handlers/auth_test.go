package handlers

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"
)

func TestGetLoginDelay(t *testing.T) {
	for _, c := range []struct {
		failures int
		want     time.Duration
	}{{0, 0}, {1, 0}, {2, 2 * time.Second}, {3, 4 * time.Second}, {5, 16 * time.Second}, {6, 30 * time.Second}, {40, 30 * time.Second}} {
		if got := getLoginDelay(c.failures); got != c.want {
			t.Errorf("getLoginDelay(%d) = %v, want %v", c.failures, got, c.want)
		}
	}
}

func TestValidatePasswordStrength(t *testing.T) {
	for _, c := range []struct {
		pw string
		ok bool
	}{
		{"Short1!", false},
		{"alllowercase1!", false},
		{"ALLUPPERCASE1!", false},
		{"NoDigitsHere!", false},
		{"NoSpecial123", false},
		{"Good-Passw0rd", true},
	} {
		if ok, msg := validatePasswordStrength(c.pw); ok != c.ok {
			t.Errorf("%q: got %v (%s)", c.pw, ok, msg)
		}
	}
}

func TestRespondErrorJSON(t *testing.T) {
	w := httptest.NewRecorder()
	respondErrorSimple(w, "Test error", http.StatusBadRequest)
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "Test error") {
		t.Errorf("got %d %s", w.Code, w.Body.String())
	}
	if ct := w.Header().Get("Content-Type"); !strings.Contains(ct, "application/json") {
		t.Errorf("content type %q", ct)
	}
}

func TestRespondErrorWithDetails(t *testing.T) {
	w := httptest.NewRecorder()
	respondError(w, http.StatusInternalServerError, "test error", errors.New("detail message"))
	if w.Code != http.StatusInternalServerError {
		t.Errorf("got %d", w.Code)
	}
}

func TestLoginValidation(t *testing.T) {
	h := NewAuthHandler(nil)
	if r := call(t, h.Login, req{method: "GET"}); r.code != 405 {
		t.Errorf("GET: %s", r)
	}
	if r := call(t, h.Login, req{method: "POST", body: "{", remote: "198.51.100.1:1"}); r.code != 400 {
		t.Errorf("bad JSON: %s", r)
	}
	if r := call(t, h.Login, req{method: "POST", body: map[string]any{"username": "x' OR 1=1--", "password": "p"}, remote: "198.51.100.1:1"}); r.code != 400 {
		t.Errorf("bad username: %s", r)
	}
}

func TestLogin(t *testing.T) {
	db := testDB(t)
	h := NewAuthHandler(db)
	addUser(t, db, "alice", "Alice-Passw0rd", "admin", true)
	addUser(t, db, "bob", "Bob-Passw0rd!", "user", false)

	login := func(user, pw, ip string) resp {
		return call(t, h.Login, req{method: "POST", body: map[string]any{"username": user, "password": pw}, remote: ip + ":4000"})
	}
	if r := login("nobody", "x", "198.51.100.10"); r.code != 401 {
		t.Errorf("unknown user: %s", r)
	}
	if r := login("alice", "wrong", "198.51.100.11"); r.code != 401 {
		t.Errorf("wrong password: %s", r)
	}
	if r := login("bob", "Bob-Passw0rd!", "198.51.100.12"); r.code != 401 || !strings.Contains(r.raw, "disabled") {
		t.Errorf("disabled account: %s", r)
	}

	r := login("alice", "Alice-Passw0rd", "198.51.100.13")
	if !r.ok() {
		t.Fatalf("login: %s", r)
	}
	sid, _ := r.body["session_id"].(string)
	if len(sid) != 64 {
		t.Fatalf("session id %q", sid)
	}
	var status string
	if err := db.QueryRow(`SELECT status FROM sessions WHERE session_id = $1`, sid).Scan(&status); err != nil || status != "active" {
		t.Errorf("session row: %q %v", status, err)
	}

	// The session works for check/session/CSRF, then logout ends it.
	hdr := map[string]string{"X-Session-ID": sid}
	if r := call(t, h.Check, req{header: hdr}); r.body["authenticated"] != true {
		t.Errorf("check: %s", r)
	}
	if r := call(t, h.Session, req{header: hdr}); !r.ok() || !strings.Contains(r.raw, `"role":"admin"`) {
		t.Errorf("session: %s", r)
	}
	if r := call(t, h.CSRFToken, req{header: hdr}); !r.ok() || len(r.body["csrf_token"].(string)) != 64 {
		t.Errorf("csrf: %s", r)
	}
	if r := call(t, h.CSRFToken, req{}); r.code != 401 {
		t.Errorf("csrf without session: %s", r)
	}
	if r := call(t, h.Logout, req{method: "POST", header: hdr}); !r.ok() {
		t.Errorf("logout: %s", r)
	}
	if r := call(t, h.Check, req{header: hdr}); r.body["authenticated"] != false {
		t.Errorf("session survived logout: %s", r)
	}
	if r := call(t, h.Session, req{header: hdr}); r.code != 401 {
		t.Errorf("session after logout: %s", r)
	}
}

func TestLoginRestrictedSessions(t *testing.T) {
	db := testDB(t)
	h := NewAuthHandler(db)
	addUser(t, db, "carol", "Carol-Passw0rd", "user", true)
	if _, err := db.Exec(`UPDATE users SET must_change_password = 1 WHERE username = 'carol'`); err != nil {
		t.Fatal(err)
	}
	r := call(t, h.Login, req{method: "POST", body: map[string]any{"username": "carol", "password": "Carol-Passw0rd"}, remote: "198.51.100.20:1"})
	if !r.ok() || r.body["must_change_password"] != true {
		t.Fatalf("login: %s", r)
	}
	var status string
	_ = db.QueryRow(`SELECT status FROM sessions WHERE session_id = $1`, r.body["session_id"]).Scan(&status)
	if status != "must_change_password" {
		t.Errorf("restricted session status %q", status)
	}

	addUser(t, db, "dave", "Dave-Passw0rd!", "user", true)
	if _, err := db.Exec(`UPDATE users SET totp_enabled = 1 WHERE username = 'dave'`); err != nil {
		t.Fatal(err)
	}
	r = call(t, h.Login, req{method: "POST", body: map[string]any{"username": "dave", "password": "Dave-Passw0rd!"}, remote: "198.51.100.21:1"})
	if !r.ok() || r.body["requires_totp"] != true || r.body["session_id"] != nil {
		t.Fatalf("a TOTP user gets a pending token, not a session: %s", r)
	}
	_ = db.QueryRow(`SELECT status FROM sessions WHERE session_id = $1`, r.body["pending_token"]).Scan(&status)
	if status != "pending_totp" {
		t.Errorf("pending session status %q", status)
	}
}

func TestLoginThrottle(t *testing.T) {
	db := testDB(t)
	h := NewAuthHandler(db)
	addUser(t, db, "erin", "Erin-Passw0rd", "user", true)
	ip := "198.51.100.30:1"
	bad := map[string]any{"username": "erin", "password": "nope"}
	if r := call(t, h.Login, req{method: "POST", body: bad, remote: ip}); r.code != 401 {
		t.Fatalf("first failure: %s", r)
	}
	if r := call(t, h.Login, req{method: "POST", body: bad, remote: ip}); r.code != 401 {
		t.Fatalf("second failure: %s", r)
	}
	// Two failures lock the address for 2 s, even with the right password.
	r := call(t, h.Login, req{method: "POST", body: map[string]any{"username": "erin", "password": "Erin-Passw0rd"}, remote: ip})
	if r.code != 429 {
		t.Errorf("throttled login: %s", r)
	}
	// Another address is not affected.
	if r := call(t, h.Login, req{method: "POST", body: map[string]any{"username": "erin", "password": "Erin-Passw0rd"}, remote: "198.51.100.31:1"}); !r.ok() {
		t.Errorf("other address: %s", r)
	}
}

func TestChangePassword(t *testing.T) {
	db := testDB(t)
	h := NewAuthHandler(db)
	addUser(t, db, "frank", "Frank-Passw0rd", "user", true)
	login := func(pw string) resp {
		return call(t, h.Login, req{method: "POST", body: map[string]any{"username": "frank", "password": pw}, remote: "198.51.100.40:1"})
	}
	s1 := login("Frank-Passw0rd").body["session_id"].(string)
	s2 := login("Frank-Passw0rd").body["session_id"].(string)
	hdr := map[string]string{"X-Session-ID": s1}

	if r := call(t, h.ChangePassword, req{method: "POST", body: map[string]any{"current_password": "Frank-Passw0rd", "new_password": "x"}}); r.code != 401 {
		t.Errorf("without session: %s", r)
	}
	if r := call(t, h.ChangePassword, req{method: "POST", header: hdr, body: map[string]any{"current_password": "Frank-Passw0rd", "new_password": "weak"}}); r.code != 400 {
		t.Errorf("weak password: %s", r)
	}
	if r := call(t, h.ChangePassword, req{method: "POST", header: hdr, body: map[string]any{"current_password": "wrong", "new_password": "New-Passw0rd!"}}); r.code != 401 {
		t.Errorf("wrong current password: %s", r)
	}
	if r := call(t, h.ChangePassword, req{method: "POST", header: hdr, body: map[string]any{"current_password": "Frank-Passw0rd", "new_password": " New-Passw0rd! "}}); !r.ok() {
		t.Fatalf("change: %s", r)
	}
	var hash, salt string
	_ = db.QueryRow(`SELECT password_hash, scram_salt FROM users WHERE username = 'frank'`).Scan(&hash, &salt)
	if bcrypt.CompareHashAndPassword([]byte(hash), []byte("New-Passw0rd!")) != nil {
		t.Error("new password (trimmed) not stored")
	}
	if salt == "" {
		t.Error("SCRAM keys not derived")
	}
	// Other sessions are revoked, the current one stays.
	var n int
	_ = db.QueryRow(`SELECT COUNT(*) FROM sessions WHERE session_id = $1`, s2).Scan(&n)
	if n != 0 {
		t.Error("other session not revoked")
	}
	_ = db.QueryRow(`SELECT COUNT(*) FROM sessions WHERE session_id = $1`, s1).Scan(&n)
	if n != 1 {
		t.Error("current session revoked")
	}
}

func TestRevokeSession(t *testing.T) {
	db := testDB(t)
	h := NewAuthHandler(db)
	addUser(t, db, "gina", "Gina-Passw0rd", "user", true)
	addUser(t, db, "hank", "Hank-Passw0rd", "user", true)
	sid := func(u, pw string) string {
		r := call(t, h.Login, req{method: "POST", body: map[string]any{"username": u, "password": pw}, remote: "198.51.100.50:1"})
		return r.body["session_id"].(string)
	}
	g := sid("gina", "Gina-Passw0rd")
	hk := sid("hank", "Hank-Passw0rd")

	if r := call(t, h.RevokeSession, req{method: "DELETE", user: "gina", body: map[string]any{"id": hk}}); r.code != 403 {
		t.Errorf("revoking another user's session: %s", r)
	}
	if r := call(t, h.RevokeSession, req{method: "DELETE", user: "gina", body: map[string]any{"id": "0123456789abcdef"}}); r.code != 404 {
		t.Errorf("unknown session: %s", r)
	}
	if r := call(t, h.RevokeSession, req{method: "DELETE", user: "gina", body: map[string]any{"id": g}}); !r.ok() {
		t.Errorf("own session: %s", r)
	}
	if r := call(t, h.RevokeSession, req{method: "DELETE", user: "admin", body: map[string]any{"id": hk}}); !r.ok() {
		t.Errorf("admin revokes any session: %s", r)
	}
}
