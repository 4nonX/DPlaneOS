-- D-PlaneOS v2.0.0 - LDAP / Active Directory Integration
-- Migration 009: Directory Service Support
-- ZERO BREAKING CHANGES: All new tables + non-destructive ALTER TABLEs
-- Rollback: DROP TABLE ldap_group_mappings; DROP TABLE ldap_sync_log; DROP TABLE ldap_config;

-- ============================================================
-- LDAP Configuration (singleton row)
-- ============================================================
CREATE TABLE IF NOT EXISTS ldap_config (
    id              INTEGER PRIMARY KEY CHECK (id = 1),
    enabled         INTEGER NOT NULL DEFAULT 0,
    server          TEXT NOT NULL DEFAULT '',
    port            INTEGER NOT NULL DEFAULT 389,
    use_tls         INTEGER NOT NULL DEFAULT 1,
    bind_dn         TEXT NOT NULL DEFAULT '',
    bind_password   TEXT NOT NULL DEFAULT '',
    base_dn         TEXT NOT NULL DEFAULT '',
    user_filter     TEXT NOT NULL DEFAULT '(&(objectClass=user)(sAMAccountName={username}))',
    user_id_attr    TEXT NOT NULL DEFAULT 'sAMAccountName',
    user_name_attr  TEXT NOT NULL DEFAULT 'displayName',
    user_email_attr TEXT NOT NULL DEFAULT 'mail',
    group_base_dn   TEXT NOT NULL DEFAULT '',
    group_filter    TEXT NOT NULL DEFAULT '(&(objectClass=group)(member={user_dn}))',
    group_member_attr TEXT NOT NULL DEFAULT 'member',
    jit_provisioning INTEGER NOT NULL DEFAULT 1,
    default_role    TEXT NOT NULL DEFAULT 'user',
    sync_interval   INTEGER NOT NULL DEFAULT 3600,
    timeout         INTEGER NOT NULL DEFAULT 10,
    last_test_at    TEXT,
    last_test_ok    INTEGER DEFAULT 0,
    last_test_msg   TEXT DEFAULT '',
    last_sync_at    TEXT,
    last_sync_ok    INTEGER DEFAULT 0,
    last_sync_count INTEGER DEFAULT 0,
    last_sync_msg   TEXT DEFAULT '',
    created_at      TEXT NOT NULL DEFAULT (datetime('now')),
    updated_at      TEXT NOT NULL DEFAULT (datetime('now'))
);

INSERT OR IGNORE INTO ldap_config (id) VALUES (1);

-- ============================================================
-- LDAP Group → D-PlaneOS Role Mappings
-- ============================================================
CREATE TABLE IF NOT EXISTS ldap_group_mappings (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    ldap_group  TEXT NOT NULL,
    role_name   TEXT NOT NULL,
    role_id     INTEGER,
    created_at  TEXT NOT NULL DEFAULT (datetime('now')),
    UNIQUE(ldap_group, role_name)
);

-- ============================================================
-- LDAP Sync Log (audit trail)
-- ============================================================
CREATE TABLE IF NOT EXISTS ldap_sync_log (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    sync_type       TEXT NOT NULL,
    success         INTEGER NOT NULL DEFAULT 0,
    users_synced    INTEGER NOT NULL DEFAULT 0,
    users_created   INTEGER NOT NULL DEFAULT 0,
    users_updated   INTEGER NOT NULL DEFAULT 0,
    users_disabled  INTEGER NOT NULL DEFAULT 0,
    error_msg       TEXT DEFAULT '',
    duration_ms     INTEGER NOT NULL DEFAULT 0,
    created_at      TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE INDEX IF NOT EXISTS idx_ldap_sync_log_created ON ldap_sync_log(created_at);
