-- +goose Up
-- Design 0001, phase 3b: storage groups and epochs.
--
-- A storage group is the unit of ownership and failover: pools, candidate
-- nodes, topology, the current owner and the epoch. Every member keeps a
-- copy; a change with a higher (epoch, version) replaces it. The epoch
-- increases with every change of owner; version with every edit of the
-- definition. A node whose epoch is stale must not write (fencing token).

CREATE TABLE IF NOT EXISTS storage_groups (
    name        TEXT PRIMARY KEY,
    topology    TEXT        NOT NULL CHECK (topology IN ('standalone', 'shared', 'replicated')),
    pools       JSONB       NOT NULL,              -- [{"name": "tank", "guid": "123..."}]
    candidates  JSONB       NOT NULL,              -- node keys in priority order
    owner       TEXT        NOT NULL,              -- node key
    epoch       BIGINT      NOT NULL DEFAULT 1,
    version     BIGINT      NOT NULL DEFAULT 1,
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_by  TEXT        NOT NULL DEFAULT ''    -- node key that made the change
);

-- Group-scope revisions carry the epoch of the owner that wrote them;
-- revisions from a stale owner are refused by the other members.
ALTER TABLE config_revisions ADD COLUMN IF NOT EXISTS epoch BIGINT NOT NULL DEFAULT 0;
ALTER TABLE config_peer_revisions ADD COLUMN IF NOT EXISTS epoch BIGINT NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE config_peer_revisions DROP COLUMN IF EXISTS epoch;
ALTER TABLE config_revisions DROP COLUMN IF EXISTS epoch;
DROP TABLE IF EXISTS storage_groups;
