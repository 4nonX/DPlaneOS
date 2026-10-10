-- +goose Up
-- Node-level protection used by storage groups (ADR-0009): watchdog and power
-- fencing settings. These tables were created at runtime by the legacy HA
-- engine (removed with Patroni); created here so a fresh database has them.
-- Tables of the legacy engine (ha_nodes, ha_witness_config, ha_sbd_config,
-- ha_cluster_secret, ha_network_witness, ...) stay in existing databases
-- unused; nothing reads them any more.
CREATE TABLE IF NOT EXISTS ha_fencing_config (
    id                       INTEGER PRIMARY KEY CHECK (id = 1),
    enable                   BOOLEAN NOT NULL DEFAULT FALSE,
    bmc_ip                   TEXT    NOT NULL DEFAULT '',
    bmc_user                 TEXT    NOT NULL DEFAULT '',
    bmc_password_file        TEXT    NOT NULL DEFAULT '',
    jitter_max_ms            INTEGER NOT NULL DEFAULT 3000,
    disk_fault_tolerance_pct INTEGER NOT NULL DEFAULT 10
);
ALTER TABLE ha_fencing_config ADD COLUMN IF NOT EXISTS bmc_tls_fingerprint TEXT NOT NULL DEFAULT '';
ALTER TABLE ha_fencing_config ADD COLUMN IF NOT EXISTS bmc_tls_pinned_at TIMESTAMPTZ;
ALTER TABLE ha_fencing_config ADD COLUMN IF NOT EXISTS bmc_protocol TEXT NOT NULL DEFAULT 'auto';

CREATE TABLE IF NOT EXISTS ha_pdu_config (
    id              INTEGER PRIMARY KEY CHECK (id = 1),
    enable          BOOLEAN NOT NULL DEFAULT FALSE,
    outlet_off_url  TEXT    NOT NULL DEFAULT '',
    method          TEXT    NOT NULL DEFAULT 'GET',
    username        TEXT    NOT NULL DEFAULT '',
    password_file   TEXT    NOT NULL DEFAULT '',
    timeout_secs    INTEGER NOT NULL DEFAULT 10,
    expected_status INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS ha_watchdog_config (
    id               INTEGER PRIMARY KEY CHECK (id = 1),
    enable           BOOLEAN NOT NULL DEFAULT FALSE,
    device           TEXT    NOT NULL DEFAULT '/dev/watchdog',
    timeout_secs     INTEGER NOT NULL DEFAULT 30,
    pet_interval_sec INTEGER NOT NULL DEFAULT 10
);
INSERT INTO ha_watchdog_config (id) VALUES (1) ON CONFLICT (id) DO NOTHING;

-- +goose Down
-- Kept: the settings predate this migration on existing installations.
SELECT 1;
