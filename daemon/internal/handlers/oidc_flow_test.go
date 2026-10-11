package handlers

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	jose "github.com/go-jose/go-jose/v4"

	"dplaned/internal/secrets"
)

// fakeIdP is an OpenID Connect provider as far as the code flow needs one:
// discovery, keys, and a token endpoint that checks what a real one checks.
type fakeIdP struct {
	t      *testing.T
	srv    *httptest.Server
	priv   *rsa.PrivateKey
	mu     sync.Mutex
	codes  map[string]fakeGrant // authorization code → what was asked for
	claims map[string]any       // claims of the user who "signs in" next
	secret string
}

type fakeGrant struct {
	nonce, challenge, redirectURI string
}

func newFakeIdP(t *testing.T) *fakeIdP {
	t.Helper()
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	idp := &fakeIdP{t: t, priv: priv, codes: map[string]fakeGrant{}, secret: "client-secret-1"}
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"issuer":                 idp.srv.URL,
			"authorization_endpoint": idp.srv.URL + "/authorize",
			"token_endpoint":         idp.srv.URL + "/token",
			"jwks_uri":               idp.srv.URL + "/keys",
		})
	})
	mux.HandleFunc("/keys", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{
			{Key: &priv.PublicKey, KeyID: "k1", Algorithm: "RS256", Use: "sig"},
		}})
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		idp.mu.Lock()
		g, ok := idp.codes[r.Form.Get("code")]
		delete(idp.codes, r.Form.Get("code"))
		claims := idp.claims
		idp.mu.Unlock()
		sum := sha256.Sum256([]byte(r.Form.Get("code_verifier")))
		switch {
		case !ok, r.Form.Get("grant_type") != "authorization_code",
			r.Form.Get("client_id") != "dplaneos", r.Form.Get("client_secret") != idp.secret,
			r.Form.Get("redirect_uri") != g.redirectURI,
			base64.RawURLEncoding.EncodeToString(sum[:]) != g.challenge:
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]string{"error": "invalid_grant"})
			return
		}
		all := map[string]any{
			"iss": idp.srv.URL, "aud": "dplaneos", "nonce": g.nonce,
			"iat": time.Now().Unix(), "exp": time.Now().Add(5 * time.Minute).Unix(),
		}
		for k, v := range claims {
			all[k] = v
		}
		signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: priv},
			(&jose.SignerOptions{}).WithHeader(jose.HeaderKey("kid"), "k1"))
		if err != nil {
			t.Error(err)
		}
		payload, _ := json.Marshal(all)
		obj, _ := signer.Sign(payload)
		tok, _ := obj.CompactSerialize()
		json.NewEncoder(w).Encode(map[string]string{"id_token": tok, "access_token": "x", "token_type": "Bearer"})
	})
	idp.srv = httptest.NewServer(mux)
	t.Cleanup(idp.srv.Close)
	return idp
}

// authorize plays the browser at the provider: the user signs in there and
// is sent back with a code.
func (idp *fakeIdP) authorize(location string, claims map[string]any) (code, state string) {
	u, err := url.Parse(location)
	if err != nil {
		idp.t.Fatal(err)
	}
	q := u.Query()
	if !strings.HasPrefix(location, idp.srv.URL+"/authorize?") || q.Get("response_type") != "code" ||
		q.Get("client_id") != "dplaneos" || q.Get("code_challenge_method") != "S256" || q.Get("code_challenge") == "" ||
		q.Get("nonce") == "" || q.Get("state") == "" || !strings.Contains(q.Get("scope"), "openid") {
		idp.t.Fatalf("authorization request: %s", location)
	}
	code = "code-" + q.Get("state")[:16]
	idp.mu.Lock()
	idp.codes[code] = fakeGrant{nonce: q.Get("nonce"), challenge: q.Get("code_challenge"), redirectURI: q.Get("redirect_uri")}
	idp.claims = claims
	idp.mu.Unlock()
	return code, q.Get("state")
}

// signIn runs the whole flow for one user as a browser would, through the
// web server in front of the daemon (which names the host and the scheme).
// It returns the session, or the error the login page is sent to.
func oidcSignIn(t *testing.T, h *OIDCHandler, idp *fakeIdP, claims map[string]any) (session, loginError string) {
	t.Helper()
	browser := func(target string) *http.Request {
		r := httptest.NewRequest("GET", target, nil)
		r.Host = "nas.example.test"
		r.Header.Set("X-Forwarded-Proto", "https")
		r.RemoteAddr = "@"
		r.Header.Set("X-Real-IP", "203.0.113.50")
		return r
	}
	w := httptest.NewRecorder()
	h.Start(w, browser("/api/auth/oidc/start"))
	if w.Code != http.StatusFound {
		t.Fatalf("start: %d %s", w.Code, w.Body.String())
	}
	loc := w.Header().Get("Location")
	if u, _ := url.Parse(loc); u.Query().Get("redirect_uri") != "https://nas.example.test/api/auth/oidc/callback" {
		t.Fatalf("the provider is told to send the user back to %q", u.Query().Get("redirect_uri"))
	}
	code, state := idp.authorize(loc, claims)

	w = httptest.NewRecorder()
	h.Callback(w, browser("/api/auth/oidc/callback?code="+url.QueryEscape(code)+"&state="+url.QueryEscape(state)))
	if w.Code != http.StatusFound {
		t.Fatalf("callback: %d %s", w.Code, w.Body.String())
	}
	back, _ := url.Parse(w.Header().Get("Location"))
	if e := back.Query().Get("error"); e != "" {
		return "", e
	}
	handoff := back.Query().Get("oidc_handoff")
	if back.Path != "/login" || handoff == "" {
		t.Fatalf("after the callback: %s", w.Header().Get("Location"))
	}
	// The same answer from the provider a second time is not accepted.
	w = httptest.NewRecorder()
	h.Callback(w, browser("/api/auth/oidc/callback?code="+url.QueryEscape(code)+"&state="+url.QueryEscape(state)))
	if !strings.Contains(w.Header().Get("Location"), "error=oidc_invalid_state") {
		t.Errorf("a replayed callback: %s", w.Header().Get("Location"))
	}

	r := call(t, h.Exchange, req{method: "POST", body: map[string]any{"handoff_code": handoff}})
	if !r.ok() {
		t.Fatalf("exchange: %s", r)
	}
	if again := call(t, h.Exchange, req{method: "POST", body: map[string]any{"handoff_code": handoff}}); again.code != http.StatusUnauthorized {
		t.Errorf("the hand-over code worked twice: %s", again)
	}
	session, _ = r.body["session_id"].(string)
	return session, ""
}

func rolesOfUser(t *testing.T, db *sql.DB, username string) []string {
	t.Helper()
	rows, err := db.Query(`SELECT r.name FROM user_roles ur JOIN roles r ON r.id = ur.role_id
		JOIN users u ON u.id = ur.user_id WHERE u.username = $1 ORDER BY r.name`, username)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var n string
		_ = rows.Scan(&n)
		out = append(out, n)
	}
	return out
}

// Single sign-on, from the button on the login page to a working session.
func TestOIDCSignIn(t *testing.T) {
	db := testDB(t)
	if err := secrets.Init(filepath.Join(t.TempDir(), "secrets.key")); err != nil {
		t.Fatal(err)
	}
	idp := newFakeIdP(t)
	h := NewOIDCHandler(db)
	auth := NewAuthHandler(db)

	var viewer int64
	if err := db.QueryRow(`SELECT id FROM roles WHERE name = 'viewer'`).Scan(&viewer); err != nil {
		t.Fatal(err)
	}
	save := func(body map[string]any) {
		t.Helper()
		if r := call(t, h.SaveConfig, req{method: "POST", body: body}); !r.ok() {
			t.Fatalf("saving the settings: %s", r)
		}
	}
	settings := map[string]any{
		"enabled": true, "issuer": idp.srv.URL, "client_id": "dplaneos", "client_secret": idp.secret,
		"scopes": "openid email profile", "allowed_algs": "RS256", "button_label": "Company sign-in",
		"auto_provision": false, "default_role_id": viewer, "group_claim": "groups",
	}
	save(settings)
	if r := call(t, h.Info, req{}); r.body["enabled"] != true || r.body["button_label"] != "Company sign-in" {
		t.Fatalf("what the login page is told: %s", r)
	}
	// The secret is stored sealed, and not handed out again.
	var stored string
	_ = db.QueryRow(`SELECT client_secret FROM oidc_config WHERE id = 1`).Scan(&stored)
	if stored == "" || strings.Contains(stored, idp.secret) {
		t.Errorf("the client secret is stored as %q", stored)
	}
	if r := call(t, h.GetConfig, req{}); strings.Contains(r.raw, idp.secret) {
		t.Errorf("the settings hand the client secret out: %s", r)
	}

	// Nobody here yet, and accounts are not created at sign-in: no.
	mallory := map[string]any{"sub": "u-mallory", "email": "mallory@example.test", "email_verified": true, "preferred_username": "mallory"}
	if s, e := oidcSignIn(t, h, idp, mallory); s != "" || e != "oidc_user_denied" {
		t.Fatalf("somebody without an account got in: %q %q", s, e)
	}

	// An account here with the same, verified address: that account.
	addUser(t, db, "carol", "Carol-Passw0rd", "user", true)
	if _, err := db.Exec(`UPDATE users SET email = 'carol@example.test' WHERE username = 'carol'`); err != nil {
		t.Fatal(err)
	}
	carol := map[string]any{"sub": "u-carol", "email": "carol@example.test", "email_verified": true, "name": "Carol C"}
	s, e := oidcSignIn(t, h, idp, carol)
	if e != "" {
		t.Fatalf("carol: %s", e)
	}
	if r := call(t, auth.Session, req{header: map[string]string{"X-Session-ID": s}}); !r.ok() || !strings.Contains(r.raw, `"carol"`) {
		t.Fatalf("the session: %s", r)
	}
	var ip string
	_ = db.QueryRow(`SELECT ip_address FROM sessions WHERE session_id = $1`, s).Scan(&ip)
	if ip != "203.0.113.50" {
		t.Errorf("the session records the address %q", ip)
	}

	// The same address, not verified by the provider, under another identity:
	// neither carol's account nor a new one.
	settings["auto_provision"] = true
	save(settings)
	thief := map[string]any{"sub": "u-thief", "email": "carol@example.test", "email_verified": false, "preferred_username": "carol"}
	if s, e := oidcSignIn(t, h, idp, thief); s != "" || e != "oidc_user_denied" {
		t.Fatalf("an unverified address got somebody into an account: %q %q", s, e)
	}

	// Accounts created at sign-in get the default role; groups named like
	// roles give those roles, and leaving the group takes them away.
	dave := map[string]any{"sub": "u-dave", "email": "dave@example.test", "email_verified": true, "preferred_username": "dave", "groups": []string{"operator", "something-else"}}
	if _, e := oidcSignIn(t, h, idp, dave); e != "" {
		t.Fatalf("dave: %s", e)
	}
	if got := strings.Join(rolesOfUser(t, db, "dave"), ","); got != "operator,viewer" {
		t.Errorf("dave's roles: %s", got)
	}
	var source string
	_ = db.QueryRow(`SELECT source FROM users WHERE username = 'dave'`).Scan(&source)
	if source != "oidc" {
		t.Errorf("source %q", source)
	}
	dave["groups"] = []string{}
	if _, e := oidcSignIn(t, h, idp, dave); e != "" {
		t.Fatalf("dave again: %s", e)
	}
	if got := strings.Join(rolesOfUser(t, db, "dave"), ","); got != "viewer" {
		t.Errorf("dave's roles after leaving the group: %s", got)
	}
	var n int
	_ = db.QueryRow(`SELECT COUNT(*) FROM users WHERE username LIKE 'dave%'`).Scan(&n)
	if n != 1 {
		t.Errorf("%d accounts for dave", n)
	}

	// Providers name the group claim differently.
	settings["group_claim"] = "roles"
	save(settings)
	dave["roles"] = "operator"
	if _, e := oidcSignIn(t, h, idp, dave); e != "" {
		t.Fatalf("dave with a roles claim: %s", e)
	}
	if got := strings.Join(rolesOfUser(t, db, "dave"), ","); got != "operator,viewer" {
		t.Errorf("roles from the configured claim: %s", got)
	}

	// An account that is switched off here stays out, and gets no second one.
	if _, err := db.Exec(`UPDATE users SET active = 0 WHERE username = 'dave'`); err != nil {
		t.Fatal(err)
	}
	if s, e := oidcSignIn(t, h, idp, dave); s != "" || e != "oidc_user_denied" {
		t.Fatalf("a disabled account got in: %q %q", s, e)
	}
	_ = db.QueryRow(`SELECT COUNT(*) FROM users WHERE username LIKE 'dave%'`).Scan(&n)
	if n != 1 {
		t.Errorf("a disabled user got a new account (%d)", n)
	}

	// Switched off: no button, no flow.
	settings["enabled"] = false
	save(settings)
	if r := call(t, h.Info, req{}); r.body["enabled"] != false {
		t.Errorf("info: %s", r)
	}
	w := httptest.NewRecorder()
	h.Start(w, httptest.NewRequest("GET", "/api/auth/oidc/start", nil))
	if w.Code != http.StatusNotFound {
		t.Errorf("start while switched off: %d", w.Code)
	}
}
