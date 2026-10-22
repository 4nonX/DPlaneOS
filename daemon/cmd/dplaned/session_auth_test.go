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

	"dplaned/internal/database"
	"dplaned/internal/middleware"
	"dplaned/internal/security"

	"github.com/gorilla/mux"
	_ "github.com/jackc/pgx/v5/stdlib"
)

// A session ID that does not exist must not authenticate: before, every
// failed session lookup was taken for "database unavailable" and the request
// went on as a degraded user that may read everything.
func TestUnknownSessionIsRejected(t *testing.T) {
	dsn := os.Getenv("DATABASE_DSN")
	if dsn == "" {
		t.Skip("DATABASE_DSN not set: database-backed test")
	}
	admin, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	b := make([]byte, 6)
	rand.Read(b) //nolint:errcheck
	schema := "sa_" + hex.EncodeToString(b)
	if _, err := admin.Exec(`CREATE SCHEMA ` + schema); err != nil {
		t.Fatal(err)
	}
	defer admin.Exec(`DROP SCHEMA ` + schema + ` CASCADE`) //nolint:errcheck
	u, _ := url.Parse(dsn)
	q := u.Query()
	q.Set("search_path", schema+",public")
	u.RawQuery = q.Encode()
	if err := security.InitDatabase(u.String()); err != nil {
		t.Fatal(err)
	}
	db, _ := sql.Open("pgx", u.String())
	defer db.Close()
	if err := database.RunMigrations(db); err != nil {
		t.Fatal(err)
	}
	security.SetDatabase(db)

	r := mux.NewRouter()
	r.Use(sessionMiddleware(db, "tok"))
	reached := false
	r.Handle("/api/rbac/users", middleware.RequirePermission("users", "read")(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = true
		w.WriteHeader(http.StatusOK)
	}))).Methods("GET")

	req := httptest.NewRequest("GET", "/api/rbac/users", nil)
	req.Header.Set("X-Session-ID", "made-up-session")
	req.Header.Set("X-User", "admin")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if reached || rec.Code != http.StatusUnauthorized {
		t.Fatalf("an unknown session reached the handler: %d %s", rec.Code, rec.Body.String())
	}

	// Database unreachable: refused too (no anonymous "degraded" reads).
	db2, _ := sql.Open("pgx", "postgres://nobody:x@127.0.0.1:1/none?sslmode=disable&connect_timeout=1")
	security.SetDatabase(db2)
	defer security.SetDatabase(db)
	rec = httptest.NewRecorder()
	reached = false
	r.ServeHTTP(rec, req)
	if reached || rec.Code < 400 {
		t.Fatalf("with the database down a request reached the handler: %d", rec.Code)
	}
}
