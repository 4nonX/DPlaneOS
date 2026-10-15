-- +goose Up
-- Design 0001 phase 4.
--
-- Default conflict rules (section 5.6): when the same resource was changed on
-- two nodes, the version written by the storage group owner (higher group
-- epoch) or by the cluster coordinator (higher coordinator term) wins. The
-- change that lost is never dropped: the node that made it keeps it here as a
-- proposal the operator can apply again or dismiss.
CREATE TABLE IF NOT EXISTS config_proposals (
    id            BIGSERIAL PRIMARY KEY,
    peer_id       TEXT        NOT NULL,           -- the node whose version won
    scope_type    TEXT        NOT NULL,
    scope_id      TEXT        NOT NULL DEFAULT '',
    resource_kind TEXT        NOT NULL,
    resource_key  TEXT        NOT NULL,
    payload       JSONB,                          -- the change that lost; NULL = a deletion
    source_uid    UUID        NOT NULL UNIQUE,    -- this node's revision that lost
    winner_uid    UUID        NOT NULL,
    reason        TEXT        NOT NULL,
    status        TEXT        NOT NULL DEFAULT 'open' CHECK (status IN ('open', 'applied', 'dismissed')),
    closed_by     TEXT        NOT NULL DEFAULT '',
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    closed_at     TIMESTAMPTZ
);
CREATE INDEX IF NOT EXISTS idx_config_proposals_open ON config_proposals (status) WHERE status = 'open';

-- Which node's version the default rule makes win ('' = no rule applies: the
-- operator decides). Shown in the review screen while the winner resolves it.
ALTER TABLE config_conflicts ADD COLUMN IF NOT EXISTS auto_winner TEXT NOT NULL DEFAULT '';
ALTER TABLE config_conflicts ADD COLUMN IF NOT EXISTS auto_reason TEXT NOT NULL DEFAULT '';

-- Maintenance mode of this node (section 5.10): it is never chosen as the
-- target of an automatic failover or a planned move.
CREATE TABLE IF NOT EXISTS node_maintenance (
    id         BOOLEAN     PRIMARY KEY DEFAULT TRUE CHECK (id),
    enabled    BOOLEAN     NOT NULL DEFAULT FALSE,
    reason     TEXT        NOT NULL DEFAULT '',
    set_by     TEXT        NOT NULL DEFAULT '',
    since      TIMESTAMPTZ
);

-- +goose Down
DROP TABLE IF EXISTS node_maintenance;
ALTER TABLE config_conflicts DROP COLUMN IF EXISTS auto_reason;
ALTER TABLE config_conflicts DROP COLUMN IF EXISTS auto_winner;
DROP TABLE IF EXISTS config_proposals;
