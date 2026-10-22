package handlers

import (
	"bytes"
	"crypto/hmac"
	"crypto/pbkdf2"
	"crypto/sha512"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"dplaned/internal/scram"

	"golang.org/x/crypto/bcrypt"
)

// testClientIP: login throttling is per address; each test uses its own.
var testClientIP = "192.0.2.10"

func postJSON(t *testing.T, h http.HandlerFunc, body any) map[string]any {
	t.Helper()
	b, _ := json.Marshal(body)
	req := httptest.NewRequest("POST", "/", bytes.NewReader(b))
	req.RemoteAddr = testClientIP + ":1234"
	rec := httptest.NewRecorder()
	h(rec, req)
	var out map[string]any
	json.Unmarshal(rec.Body.Bytes(), &out) //nolint:errcheck
	return out
}

// scramLogin performs the client side of SCRAM-SHA-512 against the handlers.
func scramLogin(t *testing.T, h *AuthHandler, user, password string) map[string]any {
	t.Helper()
	nonce, _ := scram.RandomNonce()
	ch := postJSON(t, h.SCRAMChallenge, map[string]any{"username": user, "client_nonce": nonce})
	if ch["success"] != true {
		return ch
	}
	salt, _ := scram.DecodeBase64(ch["salt"].(string))
	iter := int(ch["iterations"].(float64))
	salted, _ := pbkdf2.Key(sha512.New, password, salt, iter, sha512.Size)
	mac := func(k, m []byte) []byte { h := hmac.New(sha512.New, k); h.Write(m); return h.Sum(nil) }
	clientKey := mac(salted, []byte("Client Key"))
	stored := sha512.Sum512(clientKey)
	authMsg := scram.AuthMessage(user, nonce, ch["server_nonce"].(string), ch["salt"].(string), iter)
	sig := mac(stored[:], authMsg)
	proof := make([]byte, len(sig))
	for i := range sig {
		proof[i] = clientKey[i] ^ sig[i]
	}
	return postJSON(t, h.SCRAMVerify, map[string]any{"challenge_id": ch["challenge_id"], "client_proof": scram.EncodeBase64(proof)})
}

func TestRecoveryResetAndLogins(t *testing.T) {
	db := testDB(t)
	db.Exec(`DELETE FROM users WHERE username = 'rescue'`) //nolint:errcheck
	var id int64
	if err := db.QueryRow(`INSERT INTO users (username, password_hash, role, active, totp_enabled) VALUES ('rescue', 'x', 'admin', 0, 1) RETURNING id`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	db.Exec(`INSERT INTO totp_secrets (user_id, secret, enabled) VALUES ($1, 'sealed', 1)`, id)                                     //nolint:errcheck
	db.Exec(`INSERT INTO sessions (session_id, user_id, username, expires_at) VALUES ('old-rescue', $1, 'rescue', 9999999999)`, id) //nolint:errcheck

	if err := RecoverResetPassword(db, "rescue", "short"); err == nil {
		t.Error("a weak password was accepted")
	}
	if err := RecoverResetPassword(db, "nobody-here", "Good-Pass-123!"); err == nil {
		t.Error("an unknown user was accepted")
	}
	const pw = "Rescue-Pass-123!"
	if err := RecoverResetPassword(db, "rescue", pw); err != nil {
		t.Fatal(err)
	}
	var n int
	db.QueryRow(`SELECT COUNT(*) FROM sessions WHERE username = 'rescue'`).Scan(&n) //nolint:errcheck
	if n != 0 {
		t.Error("the user's sessions were not ended")
	}
	h := NewAuthHandler(db)
	// Password login: still asks for the second factor.
	if r := postJSON(t, h.Login, map[string]any{"username": "rescue", "password": pw}); r["requires_totp"] != true || r["session_id"] != nil {
		t.Fatalf("expected the second factor to be asked for: %v", r)
	}
	users, err := RecoverListUsers(db)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, u := range users {
		if u.Username == "rescue" {
			found = true
			if !u.TOTP || !u.Active || u.Role != "admin" {
				t.Errorf("listed as %+v", u)
			}
		}
	}
	if !found {
		t.Fatal("rescue not listed")
	}
	if err := RecoverDisableTOTP(db, "rescue"); err != nil {
		t.Fatal(err)
	}
	r := postJSON(t, h.Login, map[string]any{"username": "rescue", "password": pw})
	if r["success"] != true || r["session_id"] == nil {
		t.Fatalf("password login after the reset: %v", r)
	}
	// SCRAM login with the reset password (the keys were derived too).
	if r := scramLogin(t, h, "rescue", pw); r["success"] != true {
		t.Fatalf("SCRAM login after the reset: %v", r)
	}
	if r := scramLogin(t, h, "rescue", "Wrong-Pass-123!"); r["success"] == true {
		t.Fatal("SCRAM login with a wrong password succeeded")
	}
}

// A user created with only a bcrypt hash (the installer's first admin) gets
// SCRAM keys at the first password login, after which SCRAM works.
func TestLoginBackfillsSCRAM(t *testing.T) {
	db := testDB(t)
	testClientIP = "192.0.2.11"
	t.Cleanup(func() { testClientIP = "192.0.2.10" })
	db.Exec(`DELETE FROM users WHERE username = 'installed'`) //nolint:errcheck
	const pw = "Installed-Pass-1!"
	hash, _ := bcrypt.GenerateFromPassword([]byte(pw), bcrypt.MinCost)
	if _, err := db.Exec(`INSERT INTO users (username, password_hash, role, active) VALUES ('installed', $1, 'admin', 1)`, string(hash)); err != nil {
		t.Fatal(err)
	}
	h := NewAuthHandler(db)
	if r := scramLogin(t, h, "installed", pw); r["success"] == true {
		t.Fatal("SCRAM login without keys succeeded")
	}
	if r := postJSON(t, h.Login, map[string]any{"username": "installed", "password": pw}); r["success"] != true {
		t.Fatalf("password login: %v", r)
	}
	if r := scramLogin(t, h, "installed", pw); r["success"] != true {
		t.Fatalf("SCRAM login after the first password login: %v", r)
	}
}
