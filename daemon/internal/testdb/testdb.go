// Package testdb gives a test a PostgreSQL schema of its own with all
// migrations applied. Tests are skipped without DATABASE_DSN (CI sets it).
package testdb

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"net/url"
	"os"
	"testing"

	"dplaned/internal/database"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// DSN creates the schema and returns a DSN that selects it.
func DSN(t *testing.T) string {
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

// Open returns a migrated database in a schema of its own.
func Open(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("pgx", DSN(t))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(4)
	t.Cleanup(func() { db.Close() })
	if err := database.RunMigrations(db); err != nil {
		t.Fatalf("migrations: %v", err)
	}
	return db
}
