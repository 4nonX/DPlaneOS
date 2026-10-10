package handlers

// Shared test helpers for handler tests:
//
//   testDB(t)       a PostgreSQL schema of its own with all migrations applied
//                   (skipped without DATABASE_DSN; CI's Build & Verify job has one)
//   fakeCommands(t) records every command a handler runs, after the security
//                   whitelist accepted it, and answers with canned output
//   call(...)       runs a handler on a JSON request and decodes the answer

import (
	"bytes"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"

	"dplaned/internal/cmdutil"
	"dplaned/internal/database"
	"dplaned/internal/middleware"
	"dplaned/internal/security"

	"github.com/gorilla/mux"
	"golang.org/x/crypto/bcrypt"
	_ "github.com/jackc/pgx/v5/stdlib"
)

// testDSN returns the DSN of a fresh schema (migrated), or skips the test.
func testDSN(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv("DATABASE_DSN")
	if dsn == "" {
		t.Skip("DATABASE_DSN not set: database-backed handler test")
	}
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	schema := "t_" + hex.EncodeToString(b)
	admin, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(`CREATE SCHEMA ` + schema); err != nil {
		admin.Close()
		t.Fatalf("creating test schema: %v", err)
	}
	t.Cleanup(func() {
		_, _ = admin.Exec(`DROP SCHEMA ` + schema + ` CASCADE`)
		admin.Close()
	})
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	q.Set("search_path", schema+",public")
	u.RawQuery = q.Encode()
	return u.String()
}

// testDB opens a migrated schema of its own; the security package's session
// store points at it too.
func testDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := testDSN(t)
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(4)
	t.Cleanup(func() { db.Close() })
	if err := database.RunMigrations(db); err != nil {
		t.Fatalf("migrations: %v", err)
	}
	if err := security.InitDatabase(dsn); err != nil {
		t.Fatalf("security database: %v", err)
	}
	return db
}

// ── Commands ──────────────────────────────────────────────────────────────────

type fakeCmds struct {
	mu      sync.Mutex
	calls   []cmdutil.Call
	answers map[string]func(args []string) ([]byte, error)
}

// fakeCommands intercepts command execution for the test. answers maps a
// whitelist key (or binary name) to its canned answer; unknown commands
// answer with empty output and no error.
func fakeCommands(t *testing.T, answers map[string]func(args []string) ([]byte, error)) *fakeCmds {
	t.Helper()
	f := &fakeCmds{answers: answers}
	restore := cmdutil.SetFakeForTest(func(c cmdutil.Call) ([]byte, error) {
		f.mu.Lock()
		f.calls = append(f.calls, c)
		fn := f.answers[c.Key]
		f.mu.Unlock()
		if fn != nil {
			return fn(c.Args)
		}
		return nil, nil
	})
	t.Cleanup(restore)
	return f
}

func out(s string) func([]string) ([]byte, error) {
	return func([]string) ([]byte, error) { return []byte(s), nil }
}

func fail(msg string) func([]string) ([]byte, error) {
	return func([]string) ([]byte, error) { return []byte(msg), fmt.Errorf("exit status 1") }
}

// ran reports whether a command with this key ran with args starting with prefix.
func (f *fakeCmds) ran(key string, prefix ...string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.calls {
		if c.Key != key || len(c.Args) < len(prefix) {
			continue
		}
		match := true
		for i, p := range prefix {
			if c.Args[i] != p {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}

func (f *fakeCmds) keys() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, c := range f.calls {
		out = append(out, c.Key+" "+strings.Join(c.Args, " "))
	}
	return out
}

// ── Requests ──────────────────────────────────────────────────────────────────

type req struct {
	method string
	path   string
	body   any               // marshalled to JSON unless string
	vars   map[string]string // mux route variables
	user   string            // X-User header and context user (default admin)
	header map[string]string
	remote string // client address (default 192.0.2.1:1234)
}

type resp struct {
	code int
	body map[string]any
	raw  string
}

func call(t *testing.T, h http.HandlerFunc, r req) resp {
	t.Helper()
	var body io.Reader
	switch b := r.body.(type) {
	case nil:
	case string:
		body = strings.NewReader(b)
	default:
		raw, err := json.Marshal(b)
		if err != nil {
			t.Fatal(err)
		}
		body = bytes.NewReader(raw)
	}
	if r.method == "" {
		r.method = http.MethodGet
	}
	if r.path == "" {
		r.path = "/"
	}
	hr := httptest.NewRequest(r.method, r.path, body)
	hr.Header.Set("Content-Type", "application/json")
	if r.remote != "" {
		hr.RemoteAddr = r.remote
	}
	user := r.user
	if user == "" {
		user = "admin"
	}
	hr.Header.Set("X-User", user)
	for k, v := range r.header {
		hr.Header.Set(k, v)
	}
	ctx := hr.Context()
	hr = hr.WithContext(contextWithUser(ctx, &middleware.User{ID: 1, Username: user}))
	if r.vars != nil {
		hr = mux.SetURLVars(hr, r.vars)
	}
	rr := httptest.NewRecorder()
	h(rr, hr)
	res := resp{code: rr.Code, raw: rr.Body.String()}
	_ = json.Unmarshal(rr.Body.Bytes(), &res.body)
	return res
}

// ok reports a JSON success answer (HTTP 2xx and success != false).
func (r resp) ok() bool {
	if r.code < 200 || r.code > 299 {
		return false
	}
	if s, has := r.body["success"]; has {
		return s == true
	}
	return true
}

func (r resp) String() string { return fmt.Sprintf("HTTP %d %s", r.code, r.raw) }

// addUser creates a local user with a bcrypt password hash.
func addUser(t *testing.T, db *sql.DB, name, password, role string, active bool) int {
	t.Helper()
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	a := 0
	if active {
		a = 1
	}
	var id int
	if err := db.QueryRow(`INSERT INTO users (username, password_hash, role, active) VALUES ($1, $2, $3, $4)
		ON CONFLICT (username) DO UPDATE SET password_hash = EXCLUDED.password_hash, role = EXCLUDED.role, active = EXCLUDED.active
		RETURNING id`, name, string(hash), role, a).Scan(&id); err != nil {
		t.Fatalf("adding user %s: %v", name, err)
	}
	return id
}
