package handlers

// disk_registry.go - DPlaneOS disk lifecycle registry
//
// Provides a persistent PostgreSQL-backed store for every disk the system has ever
// seen.  Records survive across reboots, pool imports, and disk replacements,
// giving operators a full audit trail of disk history.
//
// The table is created by schema.go (initSchema) alongside all other core
// tables.  This file owns the data-access layer only; no HTTP handlers here.

import (
	"database/sql"
	"time"
)

// DiskRecord is the in-memory representation of a disk_registry row.
type DiskRecord struct {
	ID        int64
	DevName   string // sda, nvme0n1, etc.
	ByIDPath  string // /dev/disk/by-id/wwn-0x...  (empty if unknown)
	Serial    string
	WWN       string
	Model     string
	SizeBytes int64
	DiskType  string
	PoolName  string
	Health    string
	LastSeen  time.Time
	FirstSeen time.Time
	RemovedAt *time.Time // nil when currently present
	TempC     int
}

// registryDB is the package-level database reference set by SetRegistryDB.
// It is initialised in main.go via handlers.SetRegistryDB(db).
var registryDB *sql.DB

// SetRegistryDB injects the shared database connection into the handlers
// package so that disk_registry.go and disk_event_handler.go can use it
// without passing *sql.DB through every call chain.
func SetRegistryDB(db *sql.DB) {
	registryDB = db
}

// GetDiskByByID returns a disk record by its /dev/disk/by-id path.
func GetDiskByByID(db *sql.DB, byID string) (*DiskRecord, error) {
	if byID == "" {
		return nil, nil
	}
	row := db.QueryRow(`
		SELECT id, dev_name, by_id_path, serial, wwn, model, size_bytes, disk_type,
		       pool_name, health, last_seen, first_seen, removed_at, temp_c
		FROM disk_registry
		WHERE by_id_path = $1
		LIMIT 1
	`, byID)
	return scanDiskRecord(row)
}

// GetDiskByDevName returns the most-recently-seen disk record for a bare device
// name (e.g. "sda").  Used when a by-id path is not yet known.
func GetDiskByDevName(db *sql.DB, devName string) (*DiskRecord, error) {
	if devName == "" {
		return nil, nil
	}
	row := db.QueryRow(`
		SELECT id, dev_name, by_id_path, serial, wwn, model, size_bytes, disk_type,
		       pool_name, health, last_seen, first_seen, removed_at, temp_c
		FROM disk_registry
		WHERE dev_name = $1 AND removed_at IS NULL
		ORDER BY last_seen DESC
		LIMIT 1
	`, devName)
	return scanDiskRecord(row)
}

func scanDiskRecord(row *sql.Row) (*DiskRecord, error) {
	var rec DiskRecord
	var removedAt sql.NullTime
	err := row.Scan(
		&rec.ID, &rec.DevName, &rec.ByIDPath, &rec.Serial, &rec.WWN, &rec.Model,
		&rec.SizeBytes, &rec.DiskType, &rec.PoolName, &rec.Health,
		&rec.LastSeen, &rec.FirstSeen, &removedAt, &rec.TempC,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if removedAt.Valid {
		rec.RemovedAt = &removedAt.Time
	}
	return &rec, nil
}
