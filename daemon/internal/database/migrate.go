package database

import (
	"database/sql"
	"embed"
	"fmt"
	"log"

	"github.com/pressly/goose/v3"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

// RunMigrations applies all pending schema migrations to the database.
// Safe to call on every startup: already-applied migrations are tracked in
// the goose_db_version table and skipped automatically.
func RunMigrations(db *sql.DB) error {
	goose.SetBaseFS(migrationFiles)
	goose.SetLogger(goose.NopLogger())

	if err := goose.SetDialect("postgres"); err != nil {
		return fmt.Errorf("migration dialect: %w", err)
	}

	current, err := goose.GetDBVersion(db)
	if err != nil && err != goose.ErrNoCurrentVersion {
		return fmt.Errorf("migration version check: %w", err)
	}

	if err := goose.Up(db, "migrations"); err != nil {
		return fmt.Errorf("migration failed: %w", err)
	}

	next, _ := goose.GetDBVersion(db)
	if next != current {
		log.Printf("DB: migrated schema from version %d to %d", current, next)
	} else {
		log.Printf("DB: schema up to date (version %d)", current)
	}

	return nil
}
