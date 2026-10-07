-- +goose Up
-- Design 0001, phase 3c: replicated storage groups.

-- Per-group settings: replication interval, and whether a replicated group
-- may fail over automatically (data since the last replication is lost).
ALTER TABLE storage_groups ADD COLUMN IF NOT EXISTS interval_secs INT NOT NULL DEFAULT 300;
ALTER TABLE storage_groups ADD COLUMN IF NOT EXISTS auto_failover BOOLEAN NOT NULL DEFAULT FALSE;

-- Replication state of this node, as owner (direction 'out', one row per
-- target and pool) or as receiver (direction 'in': a refused receive, e.g. a
-- diverged copy, and the operator's decision to discard local changes).
CREATE TABLE IF NOT EXISTS group_replication (
    group_name    TEXT        NOT NULL,
    direction     TEXT        NOT NULL CHECK (direction IN ('out', 'in')),
    peer          TEXT        NOT NULL,        -- target (out) or sender (in) node key
    pool          TEXT        NOT NULL,
    last_snapshot TEXT        NOT NULL DEFAULT '',
    last_ok_at    TIMESTAMPTZ,
    last_error    TEXT        NOT NULL DEFAULT '',
    discard_ok    BOOLEAN     NOT NULL DEFAULT FALSE, -- 'in': operator accepted losing local changes once
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (group_name, direction, peer, pool)
);

-- +goose Down
DROP TABLE IF EXISTS group_replication;
ALTER TABLE storage_groups DROP COLUMN IF EXISTS auto_failover;
ALTER TABLE storage_groups DROP COLUMN IF EXISTS interval_secs;
