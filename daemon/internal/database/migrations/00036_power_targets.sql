-- +goose Up
-- Power control, one entry per machine.
--
-- Until now there was one BMC record (in ha_fencing_config) used for two
-- different things: the hardware page of this system (sensors, power
-- buttons), and powering off "the previous owner" before a takeover,
-- whoever that owner was. In a pair that is one machine or the other,
-- never both; with three nodes a takeover could power off a healthy one.
--
--   node = ''          this system's own BMC (hardware page, metrics)
--   node = <node key>  how this node powers that node off (fencing)
--
-- The password is sealed with the node's secrets key; it used to be a path
-- to a file somebody had to create on the system by hand.
CREATE TABLE IF NOT EXISTS power_targets (
    node            TEXT PRIMARY KEY,
    kind            TEXT    NOT NULL DEFAULT 'bmc' CHECK (kind IN ('bmc', 'pdu')),
    enabled         BOOLEAN NOT NULL DEFAULT FALSE,
    host            TEXT    NOT NULL DEFAULT '',
    username        TEXT    NOT NULL DEFAULT '',
    password        TEXT    NOT NULL DEFAULT '',
    tls_fingerprint TEXT    NOT NULL DEFAULT '',
    tls_pinned_at   TIMESTAMPTZ,
    off_url         TEXT    NOT NULL DEFAULT '',
    status_url      TEXT    NOT NULL DEFAULT '',
    http_method     TEXT    NOT NULL DEFAULT 'GET',
    expected_status INTEGER NOT NULL DEFAULT 0,
    timeout_secs    INTEGER NOT NULL DEFAULT 10,
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

ALTER TABLE ha_fencing_config DROP COLUMN IF EXISTS enable;
ALTER TABLE ha_fencing_config DROP COLUMN IF EXISTS bmc_ip;
ALTER TABLE ha_fencing_config DROP COLUMN IF EXISTS bmc_user;
ALTER TABLE ha_fencing_config DROP COLUMN IF EXISTS bmc_password_file;
ALTER TABLE ha_fencing_config DROP COLUMN IF EXISTS bmc_tls_fingerprint;
ALTER TABLE ha_fencing_config DROP COLUMN IF EXISTS bmc_tls_pinned_at;
ALTER TABLE ha_fencing_config DROP COLUMN IF EXISTS bmc_protocol;
INSERT INTO ha_fencing_config (id) VALUES (1) ON CONFLICT (id) DO NOTHING;
DROP TABLE IF EXISTS ha_pdu_config;

-- +goose Down
SELECT 1;
