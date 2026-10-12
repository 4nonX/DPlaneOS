-- +goose Up
-- The network configuration (interfaces, bonds, VLANs). These tables were
-- created by the daemon while it started, after the point where a one-off
-- "apply state.yaml and exit" reads them: on a new database that failed.
-- They are part of the schema now. (IF NOT EXISTS: a database that has
-- them keeps them as they are.)
CREATE TABLE IF NOT EXISTS network_interfaces (
    id         BIGSERIAL PRIMARY KEY,
    interface  TEXT        NOT NULL UNIQUE,
    type       TEXT        NOT NULL DEFAULT 'dhcp',   -- 'dhcp' or 'static'
    cidr       TEXT        NOT NULL DEFAULT '',
    gateway    TEXT        NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE TABLE IF NOT EXISTS network_bonds (
    id         BIGSERIAL PRIMARY KEY,
    name       TEXT        NOT NULL UNIQUE,
    slaves     TEXT        NOT NULL DEFAULT '',        -- comma-separated
    mode       TEXT        NOT NULL DEFAULT '802.3ad',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE TABLE IF NOT EXISTS network_vlans (
    id         BIGSERIAL PRIMARY KEY,
    name       TEXT        NOT NULL UNIQUE,
    parent     TEXT        NOT NULL,
    vid        INTEGER     NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- +goose Down
-- The tables hold the network configuration: they stay.
SELECT 1;
