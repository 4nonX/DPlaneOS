-- +goose Up
-- Design 0001 phase 6: the fleet overlay (section 5.7, ADR-0005).
--
-- Any node can be the fleet manager: it registers remotes (standalone nodes
-- and cluster members) with an API token, shows their inventory and health,
-- holds fleet-wide configuration and rolls it out in waves. Nothing on a
-- remote depends on it: a remote works the same with the manager down.

-- Manager side.
CREATE TABLE IF NOT EXISTS fleet_settings (
    id      BOOLEAN PRIMARY KEY DEFAULT TRUE CHECK (id),
    enabled BOOLEAN NOT NULL DEFAULT FALSE,
    name    TEXT    NOT NULL DEFAULT ''
);

CREATE TABLE IF NOT EXISTS fleet_remotes (
    id          BIGSERIAL PRIMARY KEY,
    name        TEXT        NOT NULL UNIQUE,
    url         TEXT        NOT NULL UNIQUE,
    token       TEXT        NOT NULL,              -- sealed API token (admin scope) of the remote
    fingerprint TEXT        NOT NULL DEFAULT '',   -- pinned TLS certificate (https remotes)
    labels      JSONB       NOT NULL DEFAULT '{}',
    node_id     TEXT        NOT NULL DEFAULT '',
    added_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_seen   TIMESTAMPTZ,
    last_error  TEXT        NOT NULL DEFAULT '',
    status      JSONB                              -- last inventory report
);

-- Fleet-wide configuration: the desired version of each fleet resource.
CREATE TABLE IF NOT EXISTS fleet_resources (
    kind       TEXT        NOT NULL,
    key        TEXT        NOT NULL,
    payload    JSONB,                              -- NULL = to be deleted on the remotes
    version    BIGINT      NOT NULL DEFAULT 1,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_by TEXT        NOT NULL DEFAULT '',
    PRIMARY KEY (kind, key)
);

-- Rollouts: a snapshot of resources applied to the remotes wave by wave.
CREATE TABLE IF NOT EXISTS fleet_rollouts (
    id           BIGSERIAL PRIMARY KEY,
    name         TEXT        NOT NULL,
    state        TEXT        NOT NULL DEFAULT 'running'
                 CHECK (state IN ('running', 'paused', 'done', 'failed', 'cancelled')),
    resources    JSONB       NOT NULL,             -- [{kind, key, payload, version}]
    waves        JSONB       NOT NULL,             -- [{name}]
    current_wave INTEGER     NOT NULL DEFAULT 0,
    soak_secs    INTEGER     NOT NULL DEFAULT 0,   -- wait after a wave before the next
    auto_advance BOOLEAN     NOT NULL DEFAULT TRUE,
    wave_done_at TIMESTAMPTZ,
    message      TEXT        NOT NULL DEFAULT '',
    created_by   TEXT        NOT NULL DEFAULT '',
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS fleet_rollout_targets (
    rollout_id BIGINT      NOT NULL REFERENCES fleet_rollouts(id) ON DELETE CASCADE,
    remote_id  BIGINT      NOT NULL REFERENCES fleet_remotes(id) ON DELETE CASCADE,
    wave       INTEGER     NOT NULL,
    state      TEXT        NOT NULL DEFAULT 'pending'
               CHECK (state IN ('pending', 'done', 'failed', 'skipped')),
    detail     TEXT        NOT NULL DEFAULT '',
    attempts   INTEGER     NOT NULL DEFAULT 0,
    applied_at TIMESTAMPTZ,
    PRIMARY KEY (rollout_id, remote_id)
);

-- Remote side: the fleet this node takes configuration from (shown on the
-- node; informational, the node never depends on it).
CREATE TABLE IF NOT EXISTS fleet_membership (
    id            BOOLEAN PRIMARY KEY DEFAULT TRUE CHECK (id),
    fleet         TEXT        NOT NULL DEFAULT '',
    manager       TEXT        NOT NULL DEFAULT '',
    last_apply_at TIMESTAMPTZ,
    last_rollout  TEXT        NOT NULL DEFAULT '',
    -- the manager's last call (inventory every minute); a long silence is
    -- shown as "fleet manager unreachable"
    last_contact_at TIMESTAMPTZ
);

-- Revisions applied from the fleet manager.
ALTER TABLE config_revisions DROP CONSTRAINT IF EXISTS config_revisions_origin_check;
ALTER TABLE config_revisions ADD CONSTRAINT config_revisions_origin_check
    CHECK (origin IN ('baseline', 'gui', 'detected', 'import', 'rollback', 'git', 'peer', 'merge', 'overlay'));

-- A proposal kept when the fleet's version replaced a node's change has no
-- winning revision on the node.
ALTER TABLE config_proposals ALTER COLUMN winner_uid DROP NOT NULL;

-- +goose Down
ALTER TABLE config_revisions DROP CONSTRAINT IF EXISTS config_revisions_origin_check;
ALTER TABLE config_revisions ADD CONSTRAINT config_revisions_origin_check
    CHECK (origin IN ('baseline', 'gui', 'detected', 'import', 'rollback', 'git', 'peer', 'merge'));
DROP TABLE IF EXISTS fleet_membership;
DROP TABLE IF EXISTS fleet_rollout_targets;
DROP TABLE IF EXISTS fleet_rollouts;
DROP TABLE IF EXISTS fleet_resources;
DROP TABLE IF EXISTS fleet_remotes;
DROP TABLE IF EXISTS fleet_settings;
