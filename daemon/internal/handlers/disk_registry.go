package handlers

// disk_registry.go - D-PlaneOS disk lifecycle registry
//
// Provides a persistent SQLite-backed store for every disk the system has ever
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

// UpsertDisk inserts or updates a disk_registry row from a live DiskInfo.
// The UNIQUE(dev_name, by_id_path) constraint means a disk is uniquely
// identified by the pair; by_id_path may be empty for devices that lack
// a stable symlink (e.g., some virtual disks).
//
// On conflict the existing row is updated with the latest observations while
// preserving first_seen and removed_at = NULL (marks the disk as present).
func UpsertDisk(db *sql.DB, disk DiskInfo) error {

	// Coerce NULL-unfriendly empty strings to empty (SQLite stores them as '').
	byID := disk.ByIDPath
	serial := disk.Serial
	wwn := disk.WWN
	model := disk.Model
	poolName := disk.PoolName
	health := disk.Health
	if health == "" {
		health = "UNKNOWN"
	}

	_, err := db.Exec(`
		INSERT INTO disk_registry
			(dev_name, by_id_path, serial, wwn, model, size_bytes, disk_type, pool_name, health,
			 last_seen, first_seen, removed_at, temp_c)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, NOW(), NOW(), NULL, $10)
		ON CONFLICT(dev_name, by_id_path) DO UPDATE SET
			serial     = excluded.serial,
			wwn        = excluded.wwn,
			model      = excluded.model,
			size_bytes = excluded.size_bytes,
			disk_type  = excluded.disk_type,
			pool_name  = excluded.pool_name,
			health     = excluded.health,
			last_seen  = NOW(),
			removed_at = NULL,
			temp_c     = excluded.temp_c
	`,
		disk.Name, byID, serial, wwn, model,
		int64(disk.SizeBytes), disk.Type, poolName, health,
		disk.Temp,
	)
	return err
}

// GetDiskBySerial returns the most-recently-seen disk record with the given
// serial number, or nil if not found.
func GetDiskBySerial(db *sql.DB, serial string) (*DiskRecord, error) {
	if serial == "" {
		return nil, nil
	}
	row := db.QueryRow(`
		SELECT id, dev_name, by_id_path, serial, wwn, model, size_bytes, disk_type,
		       pool_name, health, last_seen, first_seen, removed_at, temp_c
		FROM disk_registry
		WHERE serial = $1
		ORDER BY last_seen DESC
		LIMIT 1
	`, serial)
	return scanDiskRecord(row)
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

// MarkDiskRemoved sets the removed_at timestamp on a disk_registry row,
// indicating the disk has been physically removed from the system.
// Identified by by_id_path; if byID is empty the call is a no-op.
func MarkDiskRemoved(db *sql.DB, byID string) error {
	if byID == "" {
		return nil
	}
	_, err := db.Exec(
		`UPDATE disk_registry SET removed_at = NOW(), health = 'REMOVED' WHERE by_id_path = $1`,
		byID,
	)
	return err
}

// GetAllDisks returns all disk_registry rows, including removed disks.
// Rows are ordered by last_seen descending so the most recent observation
// appears first.
func GetAllDisks(db *sql.DB) ([]DiskRecord, error) {
	rows, err := db.Query(`
		SELECT id, dev_name, by_id_path, serial, wwn, model, size_bytes, disk_type,
		       pool_name, health, last_seen, first_seen, removed_at, temp_c
		FROM disk_registry
		ORDER BY last_seen DESC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var records []DiskRecord
	for rows.Next() {
		rec, err := scanDiskRecordFromRows(rows)
		if err != nil {
			return nil, err
		}
		records = append(records, *rec)
	}
	return records, rows.Err()
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

func scanDiskRecordFromRows(rows *sql.Rows) (*DiskRecord, error) {
	var rec DiskRecord
	var removedAt sql.NullTime
	err := rows.Scan(
		&rec.ID, &rec.DevName, &rec.ByIDPath, &rec.Serial, &rec.WWN, &rec.Model,
		&rec.SizeBytes, &rec.DiskType, &rec.PoolName, &rec.Health,
		&rec.LastSeen, &rec.FirstSeen, &removedAt, &rec.TempC,
	)
	if err != nil {
		return nil, err
	}
	if removedAt.Valid {
		rec.RemovedAt = &removedAt.Time
	}
	return &rec, nil
}

