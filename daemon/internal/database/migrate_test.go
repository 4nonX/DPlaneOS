package database

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"net/url"
	"os"
	"testing"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
)

func testSchemaDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv("DATABASE_DSN")
	if dsn == "" {
		t.Skip("DATABASE_DSN not set")
	}
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	schema := "m_" + hex.EncodeToString(b)
	admin, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(`CREATE SCHEMA ` + schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = admin.Exec(`DROP SCHEMA ` + schema + ` CASCADE`); admin.Close() })
	u, _ := url.Parse(dsn)
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	db, err := sql.Open("pgx", u.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// 00024 must upgrade databases that already hold duplicate pending
// operations (left by the race it closes) instead of failing at startup.
func TestMigration00024WithDuplicates(t *testing.T) {
	db := testSchemaDB(t)
	goose.SetBaseFS(migrationFiles)
	goose.SetLogger(goose.NopLogger())
	if err := goose.SetDialect("postgres"); err != nil {
		t.Fatal(err)
	}
	if err := goose.UpTo(db, "migrations", 23); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO storage_operations (operation_type, target, state, started_at) VALUES
		('replace','tank:a','pending',NOW()-interval '2 hours'),
		('replace','tank:a','pending',NOW()-interval '1 hour'),
		('replace','tank:a','pending',NOW()),
		('add','tank','pending',NOW()),
		('add','tank','committed',NOW())`); err != nil {
		t.Fatal(err)
	}
	if err := RunMigrations(db); err != nil {
		t.Fatalf("migrating a database with duplicates: %v", err)
	}
	var pending int
	_ = db.QueryRow(`SELECT COUNT(*) FROM storage_operations WHERE target = 'tank:a' AND state = 'pending'
		AND started_at > NOW() - interval '1 minute'`).Scan(&pending)
	if pending != 1 {
		t.Errorf("the newest pending operation should stay pending, got %d", pending)
	}
	var failed int
	_ = db.QueryRow(`SELECT COUNT(*) FROM storage_operations WHERE target = 'tank:a' AND state = 'failed'`).Scan(&failed)
	if failed != 2 {
		t.Errorf("older duplicates closed: %d", failed)
	}
	if _, err := db.Exec(`INSERT INTO storage_operations (operation_type, target, state, started_at) VALUES ('add','tank','pending',NOW())`); err == nil {
		t.Error("a second pending operation on one target was accepted")
	}
}
