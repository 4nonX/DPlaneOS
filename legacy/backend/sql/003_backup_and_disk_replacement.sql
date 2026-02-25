-- D-PlaneOS Database Migration: v1.13.0
-- Config Backup & Disk Replacement Features

-- Config Backups Table
CREATE TABLE IF NOT EXISTS config_backups (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    filename TEXT NOT NULL UNIQUE,
    password_hash TEXT NOT NULL,
    size INTEGER NOT NULL,
    metadata TEXT,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    notes TEXT
);

CREATE INDEX IF NOT EXISTS idx_backups_created ON config_backups(created_at DESC);

-- Disk Replacement Tracking
CREATE TABLE IF NOT EXISTS disk_replacements (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    pool TEXT NOT NULL,
    old_device TEXT NOT NULL,
    new_device TEXT NOT NULL,
    started_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    completed_at DATETIME,
    status TEXT NOT NULL CHECK(status IN ('resilvering', 'completed', 'failed')),
    notes TEXT
);

CREATE INDEX IF NOT EXISTS idx_replacements_pool ON disk_replacements(pool);
CREATE INDEX IF NOT EXISTS idx_replacements_status ON disk_replacements(status);

-- Disk Actions Log
CREATE TABLE IF NOT EXISTS disk_actions (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    pool TEXT NOT NULL,
    device TEXT NOT NULL,
    action TEXT NOT NULL CHECK(action IN ('offline', 'online', 'replace', 'remove', 'attach')),
    details TEXT,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_disk_actions_pool ON disk_actions(pool);
CREATE INDEX IF NOT EXISTS idx_disk_actions_created ON disk_actions(created_at DESC);

-- System Settings (if not exists)
CREATE TABLE IF NOT EXISTS system_settings (
    key TEXT PRIMARY KEY,
    value TEXT NOT NULL,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

-- Update version
INSERT OR REPLACE INTO system_settings (key, value) VALUES ('version', '1.13.0');
INSERT OR REPLACE INTO system_settings (key, value) VALUES ('migration_date', datetime('now'));
