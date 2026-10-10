package handlers

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"dplaned/internal/middleware"
	"dplaned/internal/security"
)

func TestTokenScopeAllows(t *testing.T) {
	for _, c := range []struct {
		scopes, action string
		want           bool
	}{
		{"read", "read", true},
		{"read", "write", false},
		{"read", "admin", false},
		{"write", "read", true},
		{"write", "write", true},
		{"write", "admin", false},
		{"read, admin", "admin", true},
		{"admin", "something-new", true},
		{"write", "something-new", false},
	} {
		if got := middleware.TokenScopeAllows(c.scopes, c.action); got != c.want {
			t.Errorf("scopes %q action %q: %v", c.scopes, c.action, got)
		}
	}
}

func TestTokenAllows(t *testing.T) {
	rules := []TokenResourceRule{{Method: "GET", Resource: "/api/zfs/*"}, {Method: "*", Resource: "/api/system/status"}}
	for _, c := range []struct {
		method, path string
		want         bool
	}{
		{"GET", "/api/zfs/pools", true},
		{"POST", "/api/zfs/pools", false},
		{"GET", "/api/zfs/pools/tank", false}, // "*" does not cross "/"
		{"DELETE", "/api/system/status", true},
		{"GET", "/api/users", false},
	} {
		if got := TokenAllows(rules, c.method, c.path); got != c.want {
			t.Errorf("%s %s: %v", c.method, c.path, got)
		}
	}
	if !TokenAllows(nil, "POST", "/anything") {
		t.Error("no rules means unrestricted by resource")
	}
}

// createToken makes a token through the handler and returns the secret.
func createToken(t *testing.T, h *APITokenHandler, user string, body map[string]any) string {
	t.Helper()
	body["action"] = "create"
	r := call(t, h.HandleTokens, req{method: "POST", user: user, body: body})
	if !r.ok() {
		t.Fatalf("create token: %s", r)
	}
	tok, _ := r.body["token"].(string)
	if len(tok) < 20 {
		t.Fatalf("token %q in %s", tok, r)
	}
	return tok
}

func TestAPITokenLifecycle(t *testing.T) {
	db := testDB(t)
	h := NewAPITokenHandler(db)
	addUser(t, db, "ops", "Ops-Passw0rd!", "admin", true)

	if r := call(t, h.HandleTokens, req{method: "POST", user: "ops", body: map[string]any{"action": "create", "name": "x", "scopes": "root"}}); r.code != 400 {
		t.Errorf("invalid scope: %s", r)
	}
	tok := createToken(t, h, "ops", map[string]any{"name": "ci", "scopes": "read"})
	var stored string
	_ = db.QueryRow(`SELECT token_hash FROM api_tokens WHERE name = 'ci'`).Scan(&stored)
	if stored == tok || stored != HashToken(tok) {
		t.Error("the token must be stored hashed")
	}
	u, err := security.ValidateAPITokenAndGetUser(tok)
	if err != nil || u.Username != "ops" || u.Scopes != "read" {
		t.Fatalf("validate: %+v %v", u, err)
	}
	if _, err := security.ValidateAPITokenAndGetUser(tok + "x"); err == nil {
		t.Error("wrong token accepted")
	}

	// Expired tokens are refused (expires_at is a TIMESTAMPTZ).
	if _, err := db.Exec(`UPDATE api_tokens SET expires_at = NOW() - interval '1 minute' WHERE name = 'ci'`); err != nil {
		t.Fatal(err)
	}
	if _, err := security.ValidateAPITokenAndGetUser(tok); err == nil {
		t.Error("expired token accepted")
	}
	if _, err := ValidateAPIToken(db, tok); err == nil {
		t.Error("expired token accepted (handlers.ValidateAPIToken)")
	}
	if _, err := db.Exec(`UPDATE api_tokens SET expires_at = NOW() + interval '1 day' WHERE name = 'ci'`); err != nil {
		t.Fatal(err)
	}
	if _, err := security.ValidateAPITokenAndGetUser(tok); err != nil {
		t.Errorf("token before its expiry: %v", err)
	}

	// A token created with an expiry is valid now.
	tok2 := createToken(t, h, "ops", map[string]any{"name": "short", "scopes": "write", "expires_in_days": 1})
	if _, err := security.ValidateAPITokenAndGetUser(tok2); err != nil {
		t.Errorf("fresh token with expiry: %v", err)
	}

	// Malformed allowlist: refused, not unrestricted.
	if _, err := db.Exec(`UPDATE api_tokens SET allowed_resources = '{not json' WHERE name = 'short'`); err != nil {
		t.Fatal(err)
	}
	if _, err := ValidateAPIToken(db, tok2); err == nil {
		t.Error("malformed allowlist accepted")
	}

	// Revoke: only the owner's token.
	var id int
	_ = db.QueryRow(`SELECT id FROM api_tokens WHERE name = 'ci'`).Scan(&id)
	addUser(t, db, "other", "Other-Passw0rd!", "user", true)
	if r := call(t, h.HandleTokens, req{method: "POST", user: "other", body: map[string]any{"action": "revoke", "id": id}}); r.ok() {
		t.Errorf("revoking someone else's token: %s", r)
	}
	if r := call(t, h.HandleTokens, req{method: "POST", user: "ops", body: map[string]any{"action": "revoke", "id": id}}); !r.ok() {
		t.Fatalf("revoke: %s", r)
	}
	if _, err := security.ValidateAPITokenAndGetUser(tok); err == nil {
		t.Error("revoked token accepted")
	}
}

// A read-scoped token is refused write actions even when its user has them.
func TestRequirePermissionHonoursTokenScope(t *testing.T) {
	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) })
	run := func(u *middleware.User, action string) int {
		r := httptest.NewRequest("POST", "/", nil)
		r = r.WithContext(contextWithUser(r.Context(), u))
		w := httptest.NewRecorder()
		middleware.RequirePermission("storage", action)(ok).ServeHTTP(w, r)
		return w.Code
	}
	// User 1 has every permission.
	if c := run(&middleware.User{ID: 1, Username: "admin", TokenScopes: "read"}, "write"); c != 403 {
		t.Errorf("read token, write action: %d", c)
	}
	if c := run(&middleware.User{ID: 1, Username: "admin", TokenScopes: "read"}, "read"); c != 204 {
		t.Errorf("read token, read action: %d", c)
	}
	if c := run(&middleware.User{ID: 1, Username: "admin", TokenScopes: "write"}, "admin"); c != 403 {
		t.Errorf("write token, admin action: %d", c)
	}
	if c := run(&middleware.User{ID: 1, Username: "admin"}, "admin"); c != 204 {
		t.Errorf("session (no token), admin action: %d", c)
	}
}
