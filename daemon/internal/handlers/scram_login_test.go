package handlers

import (
	"crypto/hmac"
	"crypto/pbkdf2"
	"crypto/sha512"
	"encoding/base64"
	"testing"

	"dplaned/internal/scram"
)

// A complete SCRAM-SHA-512 sign-in, computed the way a client does it: the
// password never leaves the client, and both sides prove they know it.
func TestSCRAMLogin(t *testing.T) {
	db := testDB(t)
	h := NewAuthHandler(db)
	const user, password = "sam", "Sam-Passw0rd-1"
	addUser(t, db, user, password, "admin", true)
	keys, err := scram.Derive(password)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE users SET scram_salt = $2, scram_iterations = $3, scram_stored_key = $4, scram_server_key = $5 WHERE username = $1`,
		user, scram.EncodeBase64(keys.Salt), keys.Iterations, scram.EncodeBase64(keys.StoredKey), scram.EncodeBase64(keys.ServerKey)); err != nil {
		t.Fatal(err)
	}
	mac := func(key, msg []byte) []byte {
		m := hmac.New(sha512.New, key)
		m.Write(msg)
		return m.Sum(nil)
	}

	attempt := func(pw, ip string) resp {
		t.Helper()
		const clientNonce = "client-nonce-0123456789"
		c := call(t, h.SCRAMChallenge, req{method: "POST", remote: ip + ":4000",
			body: map[string]any{"username": user, "client_nonce": clientNonce}})
		if !c.ok() {
			t.Fatalf("challenge: %s", c)
		}
		saltB64, _ := c.body["salt"].(string)
		serverNonce, _ := c.body["server_nonce"].(string)
		iterations := int(c.body["iterations"].(float64))
		salt, _ := base64.StdEncoding.DecodeString(saltB64)
		salted, err := pbkdf2.Key(sha512.New, pw, salt, iterations, sha512.Size)
		if err != nil {
			t.Fatal(err)
		}
		clientKey := mac(salted, []byte("Client Key"))
		storedKey := sha512.Sum512(clientKey)
		authMsg := scram.AuthMessage(user, clientNonce, serverNonce, saltB64, iterations)
		sig := mac(storedKey[:], authMsg)
		proof := make([]byte, len(clientKey))
		for i := range proof {
			proof[i] = clientKey[i] ^ sig[i]
		}
		v := call(t, h.SCRAMVerify, req{method: "POST", remote: ip + ":4000",
			body: map[string]any{"challenge_id": c.body["challenge_id"], "client_proof": base64.StdEncoding.EncodeToString(proof)}})
		if v.ok() {
			// The server proves it knows the password too.
			want := base64.StdEncoding.EncodeToString(mac(mac(salted, []byte("Server Key")), authMsg))
			if v.body["server_proof"] != want {
				t.Errorf("server proof %v, want %s", v.body["server_proof"], want)
			}
		}
		return v
	}

	if r := attempt("wrong-password", "198.51.100.40"); r.code != 401 {
		t.Errorf("wrong password: %s", r)
	}
	r := attempt(password, "198.51.100.41")
	if !r.ok() {
		t.Fatalf("sign-in: %s", r)
	}
	token, _ := r.body["token"].(string)
	if c := call(t, h.Check, req{header: map[string]string{"X-Session-ID": token}}); c.body["authenticated"] != true {
		t.Errorf("the session does not work: %s", c)
	}

	// With two-factor sign-in switched on, the password alone is not enough.
	if _, err := db.Exec(`UPDATE users SET totp_enabled = 1 WHERE username = $1`, user); err != nil {
		t.Fatal(err)
	}
	r = attempt(password, "198.51.100.42")
	if !r.ok() || r.body["requires_totp"] != true || r.body["token"] != nil {
		t.Errorf("with TOTP: %s", r)
	}
}
