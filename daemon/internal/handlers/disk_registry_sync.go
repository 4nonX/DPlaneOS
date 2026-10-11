package handlers

import (
	"context"
	"database/sql"
	"log"
	"time"
)

// The disk registry remembers every disk the system has seen and the pool it
// belonged to. That memory is what lets the daemon import a known pool when
// its disks come back (an enclosure that was switched on after boot, a USB
// disk that was unplugged): see attemptPoolImport.

// UpsertDisk records a present disk. A disk is identified by its
// /dev/disk/by-id path; its kernel name (sdb) changes between plugs and is
// only the key for disks without a stable path.
//
// The pool is only ever set here, never cleared: a disk whose pool is not
// imported right now (exported, not imported yet at boot) still belongs to
// it. ForgetPoolDisks and ForgetDiskPool clear it on purpose.
func UpsertDisk(db *sql.DB, disk DiskInfo) error {
	health := disk.Health
	if health == "" {
		health = "UNKNOWN"
	}
	if disk.ByIDPath != "" {
		_, err := db.Exec(`
			INSERT INTO disk_registry
				(dev_name, by_id_path, serial, wwn, model, size_bytes, disk_type, pool_name, health,
				 last_seen, first_seen, removed_at, temp_c)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, NOW(), NOW(), NULL, $10)
			ON CONFLICT (by_id_path) DO UPDATE SET
				dev_name   = excluded.dev_name,
				serial     = excluded.serial,
				wwn        = excluded.wwn,
				model      = excluded.model,
				size_bytes = excluded.size_bytes,
				disk_type  = excluded.disk_type,
				pool_name  = CASE WHEN excluded.pool_name <> '' THEN excluded.pool_name ELSE disk_registry.pool_name END,
				health     = excluded.health,
				last_seen  = NOW(),
				removed_at = NULL,
				temp_c     = excluded.temp_c`,
			disk.Name, disk.ByIDPath, disk.Serial, disk.WWN, disk.Model,
			int64(disk.SizeBytes), disk.Type, disk.PoolName, health, disk.Temp)
		return err
	}
	res, err := db.Exec(`
		UPDATE disk_registry SET
			serial = $2, wwn = $3, model = $4, size_bytes = $5, disk_type = $6,
			pool_name = CASE WHEN $7 <> '' THEN $7 ELSE pool_name END,
			health = $8, last_seen = NOW(), removed_at = NULL, temp_c = $9
		WHERE dev_name = $1 AND by_id_path IS NULL`,
		disk.Name, disk.Serial, disk.WWN, disk.Model, int64(disk.SizeBytes), disk.Type, disk.PoolName, health, disk.Temp)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n > 0 {
		return nil
	}
	_, err = db.Exec(`
		INSERT INTO disk_registry
			(dev_name, by_id_path, serial, wwn, model, size_bytes, disk_type, pool_name, health,
			 last_seen, first_seen, removed_at, temp_c)
		VALUES ($1, NULL, $2, $3, $4, $5, $6, $7, $8, NOW(), NOW(), NULL, $9)`,
		disk.Name, disk.Serial, disk.WWN, disk.Model, int64(disk.SizeBytes), disk.Type, disk.PoolName, health, disk.Temp)
	return err
}

// MarkDiskRemoved records that a disk is gone: by its by-id path, or by its
// kernel name when it has no stable path. The pool it belonged to is kept.
func MarkDiskRemoved(db *sql.DB, byID, devName string) error {
	if byID != "" {
		_, err := db.Exec(`UPDATE disk_registry SET removed_at = NOW(), health = 'REMOVED' WHERE by_id_path = $1 AND removed_at IS NULL`, byID)
		return err
	}
	if devName == "" {
		return nil
	}
	_, err := db.Exec(`UPDATE disk_registry SET removed_at = NOW(), health = 'REMOVED' WHERE dev_name = $1 AND removed_at IS NULL`, devName)
	return err
}

// ForgetPoolDisks: the pool was exported or destroyed on purpose; its disks
// coming back must not import it again.
func ForgetPoolDisks(db *sql.DB, pool string) {
	if db == nil || pool == "" {
		return
	}
	if _, err := db.Exec(`UPDATE disk_registry SET pool_name = '' WHERE pool_name = $1`, pool); err != nil {
		log.Printf("DISK REGISTRY: forgetting the disks of %s: %v", pool, err)
	}
}

// ForgetDiskPool: the disk was wiped, detached or replaced.
func ForgetDiskPool(db *sql.DB, devName string) {
	if db == nil || devName == "" {
		return
	}
	if _, err := db.Exec(`UPDATE disk_registry SET pool_name = '' WHERE dev_name = $1 AND removed_at IS NULL`, devName); err != nil {
		log.Printf("DISK REGISTRY: forgetting the pool of %s: %v", devName, err)
	}
}

// discover is discoverDisks; replaced in tests.
var discover = discoverDisks

// SyncDiskRegistry records every disk that is present now (with the pool it
// is in) and marks registered disks that are gone as removed.
func SyncDiskRegistry(db *sql.DB) error {
	disks, err := discover()
	if err != nil {
		return err
	}
	present := map[string]bool{}
	for _, d := range disks {
		if err := UpsertDisk(db, d); err != nil {
			log.Printf("DISK REGISTRY: %s: %v", d.Name, err)
			continue
		}
		present[d.Name] = true
	}
	rows, err := db.Query(`SELECT id, dev_name FROM disk_registry WHERE removed_at IS NULL`)
	if err != nil {
		return err
	}
	var gone []int64
	for rows.Next() {
		var id int64
		var name string
		if err := rows.Scan(&id, &name); err == nil && !present[name] {
			gone = append(gone, id)
		}
	}
	rows.Close()
	for _, id := range gone {
		if _, err := db.Exec(`UPDATE disk_registry SET removed_at = NOW(), health = 'REMOVED' WHERE id = $1`, id); err != nil {
			return err
		}
	}
	return nil
}

var registrySyncNow = make(chan struct{}, 1)

// RequestDiskRegistrySync asks for a sync soon: a pool was created,
// imported or changed (ZFS events), so disks may belong to another pool now.
func RequestDiskRegistrySync() {
	select {
	case registrySyncNow <- struct{}{}:
	default:
	}
}

// StartDiskRegistrySync keeps the registry current: at start, every
// interval, and shortly after RequestDiskRegistrySync.
func StartDiskRegistrySync(ctx context.Context, db *sql.DB, interval time.Duration) {
	go func() {
		for {
			if err := SyncDiskRegistry(db); err != nil {
				log.Printf("DISK REGISTRY: sync: %v", err)
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(interval):
			case <-registrySyncNow:
				// Events come in bursts: wait for the burst to pass.
				select {
				case <-ctx.Done():
					return
				case <-time.After(3 * time.Second):
				}
			}
		}
	}()
}
