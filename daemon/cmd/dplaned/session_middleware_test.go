package main

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"testing"
	"time"

	"dplaned/internal/database"
	"dplaned/internal/middleware"
	"dplaned/internal/security"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// A migrated schema of its own (skipped without DATABASE_DSN).
func middlewareDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv("DATABASE_DSN")
	if dsn == "" {
		t.Skip("DATABASE_DSN not set: database-backed test")
	}
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	schema := "t_" + hex.EncodeToString(b)
	admin, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(`CREATE SCHEMA ` + schema); err != nil {
		t.Fatalf("creating test schema: %v", err)
	}
	t.Cleanup(func() {
		_, _ = admin.Exec(`DROP SCHEMA ` + schema + ` CASCADE`)
		admin.Close()
	})
	u, _ := url.Parse(dsn)
	q := u.Query()
	q.Set("search_path", schema+",public")
	u.RawQuery = q.Encode()
	db, err := sql.Open("pgx", u.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := database.RunMigrations(db); err != nil {
		t.Fatalf("migrations: %v", err)
	}
	if err := security.InitDatabase(u.String()); err != nil {
		t.Fatal(err)
	}
	if err := security.SeedRBAC(db); err != nil {
		t.Fatal(err)
	}
	return db
}

// The session middleware in front of a read route and a route outside
// permRoute: what an unauthenticated client can reach.
func TestSessionMiddlewareRefusesWithoutValidSession(t *testing.T) {
	db := middlewareDB(t)
	var uid int
	if err := db.QueryRow(`INSERT INTO users (username, password_hash, role, active) VALUES ('admin', 'x', 'admin', 1)
		ON CONFLICT (username) DO UPDATE SET active = 1 RETURNING id`).Scan(&uid); err != nil {
		t.Fatal(err)
	}
	now := time.Now().Unix()
	if _, err := db.Exec(`INSERT INTO sessions (session_id, user_id, username, created_at, expires_at, status, csrf_token)
		VALUES ('good-session', $1, 'admin', $2, $3, 'active', 'csrf-1')`, uid, now, now+3600); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO sessions (session_id, user_id, username, created_at, expires_at, status)
		VALUES ('old-session', $1, 'admin', $2, $3, 'active')`, uid, now-7200, now-3600); err != nil {
		t.Fatal(err)
	}

	reached := ""
	mark := func(name string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			u, _ := middleware.GetUserFromContext(r)
			reached = name
			if u == nil || u.ID != uid {
				t.Errorf("%s reached without the session's user: %+v", name, u)
			}
		}
	}
	mw := sessionMiddleware(db, "cron-token")
	read := mw(permRoute("storage", "read", mark("read")))
	plain := mw(mark("plain")) // e.g. /api/auth/tokens, /api/rbac/me/*
	write := mw(permRoute("storage", "write", mark("write")))

	do := func(h http.Handler, method, session, user, csrf string) int {
		reached = ""
		r := httptest.NewRequest(method, "/api/files/read?path=/mnt/tank/secret", nil)
		if session != "" {
			r.Header.Set("X-Session-ID", session)
		}
		if user != "" {
			r.Header.Set("X-User", user)
		}
		if csrf != "" {
			r.Header.Set("X-CSRF-Token", csrf)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code
	}

	for _, c := range []struct{ name, session, user string }{
		{"no headers", "", ""},
		{"invented session", "anything", "admin"},
		{"expired session", "old-session", "admin"},
		{"another user's session", "good-session", "root"},
	} {
		for hn, h := range map[string]http.Handler{"read": read, "plain": plain, "write": write} {
			method := "GET"
			if hn == "write" {
				method = "POST"
			}
			if code := do(h, method, c.session, c.user, "csrf-1"); code != http.StatusUnauthorized || reached != "" {
				t.Errorf("%s, %s route: HTTP %d, reached %q", c.name, hn, code, reached)
			}
		}
	}

	if code := do(read, "GET", "good-session", "admin", ""); code != 200 || reached != "read" {
		t.Errorf("valid session, read: HTTP %d, reached %q", code, reached)
	}
	if code := do(plain, "GET", "good-session", "admin", ""); code != 200 || reached != "plain" {
		t.Errorf("valid session, plain: HTTP %d, reached %q", code, reached)
	}
	if code := do(write, "POST", "good-session", "admin", "wrong"); code != http.StatusForbidden || reached != "" {
		t.Errorf("valid session, wrong CSRF token: HTTP %d, reached %q", code, reached)
	}
	if code := do(write, "POST", "good-session", "admin", "csrf-1"); code != 200 || reached != "write" {
		t.Errorf("valid session and CSRF token, write: HTTP %d, reached %q", code, reached)
	}

	// API tokens: scope and allowlist are enforced here.
	addToken := func(raw, scopes, allow string) {
		if _, err := db.Exec(`INSERT INTO api_tokens (user_id, name, token_hash, token_prefix, scopes, allowed_resources)
			VALUES ($1, $2, $3, 'dpl_', $4, $5)`, uid, raw, security.HashToken(raw), scopes, allow); err != nil {
			t.Fatal(err)
		}
	}
	addToken("dpl_readonly", "read", "[]")
	addToken("dpl_broken", "admin", "{not json")
	addToken("dpl_elsewhere", "admin", `[{"method":"GET","resource":"/api/zfs/pools"}]`)
	bearer := func(h http.Handler, method, token string) int {
		reached = ""
		r := httptest.NewRequest(method, "/api/files/read", nil)
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code
	}
	if code := bearer(read, "GET", "dpl_readonly"); code != 200 || reached != "read" {
		t.Errorf("read token, read: HTTP %d, reached %q", code, reached)
	}
	if code := bearer(write, "POST", "dpl_readonly"); code != http.StatusForbidden || reached != "" {
		t.Errorf("read token, write: HTTP %d, reached %q", code, reached)
	}
	if code := bearer(plain, "POST", "dpl_readonly"); code != http.StatusForbidden || reached != "" {
		t.Errorf("read token, write outside permRoute: HTTP %d, reached %q", code, reached)
	}
	if code := bearer(read, "GET", "dpl_broken"); code != http.StatusForbidden || reached != "" {
		t.Errorf("malformed allowlist must refuse: HTTP %d, reached %q", code, reached)
	}
	if code := bearer(read, "GET", "dpl_elsewhere"); code != http.StatusForbidden || reached != "" {
		t.Errorf("path outside the allowlist: HTTP %d, reached %q", code, reached)
	}
	if code := bearer(read, "GET", "dpl_unknown"); code != http.StatusUnauthorized || reached != "" {
		t.Errorf("unknown token: HTTP %d, reached %q", code, reached)
	}

	// The session store cannot be read: refused, never let through.
	db.Close()
	_ = security.InitDatabase("postgres://127.0.0.1:1/none?sslmode=disable&connect_timeout=1")
	if code := do(read, "GET", "good-session", "admin", ""); code != http.StatusServiceUnavailable || reached != "" {
		t.Errorf("session store down: HTTP %d, reached %q", code, reached)
	}
}
