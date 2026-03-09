-- D-PlaneOS — Disk Registry
-- Migration 010: Persistent disk lifecycle tracking
-- Tracks every disk ever seen, stable identifiers, pool membership, and removal events.
-- ZERO BREAKING CHANGES: all new tables.
-- Rollback: DROP TABLE disk_registry;

CREATE TABLE IF NOT EXISTS disk_registry (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    dev_name     TEXT NOT NULL,          -- sda, sdb, nvme0n1
    by_id_path   TEXT UNIQUE,            -- /dev/disk/by-id/wwn-0x...
    serial       TEXT,
    wwn          TEXT,
    model        TEXT,
    size_bytes   INTEGER,
    disk_type    TEXT,
    pool_name    TEXT,
    health       TEXT NOT NULL DEFAULT 'UNKNOWN',
    last_seen    TEXT NOT NULL,          -- RFC3339
    first_seen   TEXT NOT NULL,
    removed_at   TEXT,                   -- NULL if currently present
    temp_c       INTEGER NOT NULL DEFAULT 0,
    UNIQUE(dev_name, by_id_path)
);

CREATE INDEX IF NOT EXISTS idx_disk_registry_by_id  ON disk_registry(by_id_path);
CREATE INDEX IF NOT EXISTS idx_disk_registry_serial ON disk_registry(serial);
