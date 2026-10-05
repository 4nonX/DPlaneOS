-- +goose Up
-- Design 0001, Phase 2: revisions exchanged between nodes, three-way merge.
--
-- Revisions get a global identity (uid) so the same revision is recognised on
-- every node; base_uid is the revision a change was made against and
-- merge_uid the second parent of a merge (conflict resolution or two
-- identical changes made independently). Ancestry over these two links
-- decides fast-forward, already-have and conflict.

ALTER TABLE config_revisions ADD COLUMN IF NOT EXISTS uid       UUID NOT NULL DEFAULT gen_random_uuid();
ALTER TABLE config_revisions ADD COLUMN IF NOT EXISTS base_uid  UUID;
ALTER TABLE config_revisions ADD COLUMN IF NOT EXISTS merge_uid UUID;
CREATE UNIQUE INDEX IF NOT EXISTS idx_config_revisions_uid ON config_revisions (uid);
UPDATE config_revisions r SET base_uid = b.uid
    FROM config_revisions b WHERE r.base_revision = b.id AND r.base_uid IS NULL;

ALTER TABLE config_revisions DROP CONSTRAINT IF EXISTS config_revisions_origin_check;
ALTER TABLE config_revisions ADD CONSTRAINT config_revisions_origin_check
    CHECK (origin IN ('baseline', 'gui', 'detected', 'import', 'rollback', 'git', 'peer', 'merge'));

-- Nodes this node exchanges revisions with. secret is sealed (internal/secrets).
-- cursor: the peer's revision id up to which we have pulled.
-- served_cursor: our revision id up to which the peer has pulled from us.
CREATE TABLE IF NOT EXISTS config_peers (
    id                TEXT PRIMARY KEY,
    name              TEXT        NOT NULL DEFAULT '',
    url               TEXT        NOT NULL,
    secret            TEXT        NOT NULL,
    tls_fingerprint   TEXT        NOT NULL DEFAULT '',
    cursor            BIGINT      NOT NULL DEFAULT 0,
    served_cursor     BIGINT      NOT NULL DEFAULT 0,
    last_contact      TIMESTAMPTZ,
    unreachable_since TIMESTAMPTZ,
    last_error        TEXT        NOT NULL DEFAULT '',
    created_at        TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Revisions received from peers that are not (yet) part of this node's log.
-- status: pending (not evaluated), adopted (in config_revisions), superseded
-- (this node is ahead), conflict, waiting (e.g. pool not imported here),
-- blocked (the engine refused to apply it).
CREATE TABLE IF NOT EXISTS config_peer_revisions (
    uid           UUID PRIMARY KEY,
    peer_id       TEXT        NOT NULL,
    peer_rev_id   BIGINT      NOT NULL,
    base_uid      UUID,
    merge_uid     UUID,
    scope_type    TEXT        NOT NULL,
    scope_id      TEXT        NOT NULL DEFAULT '',
    resource_kind TEXT        NOT NULL,
    resource_key  TEXT        NOT NULL,
    payload       JSONB,
    origin        TEXT        NOT NULL DEFAULT '',
    origin_node   TEXT        NOT NULL DEFAULT '',
    author        TEXT        NOT NULL DEFAULT '',
    note          TEXT        NOT NULL DEFAULT '',
    created_at    TIMESTAMPTZ NOT NULL,
    received_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    status        TEXT        NOT NULL DEFAULT 'pending',
    status_detail TEXT        NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_config_peer_revisions_resource
    ON config_peer_revisions (peer_id, resource_kind, resource_key, peer_rev_id DESC);

-- The same resource changed on this node and on a peer since their common
-- revision. Never resolved silently: the operator chooses in the review screen.
CREATE TABLE IF NOT EXISTS config_conflicts (
    id            BIGSERIAL PRIMARY KEY,
    peer_id       TEXT        NOT NULL,
    resource_kind TEXT        NOT NULL,
    resource_key  TEXT        NOT NULL,
    local_uid     UUID        NOT NULL,
    remote_uid    UUID        NOT NULL,
    status        TEXT        NOT NULL DEFAULT 'open' CHECK (status IN ('open', 'resolved', 'dismissed')),
    resolution    TEXT        NOT NULL DEFAULT '',
    resolved_by   TEXT        NOT NULL DEFAULT '',
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    resolved_at   TIMESTAMPTZ
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_config_conflicts_open
    ON config_conflicts (peer_id, resource_kind, resource_key) WHERE status = 'open';

-- Single-use join tokens (stored as SHA-256).
CREATE TABLE IF NOT EXISTS config_join_tokens (
    token_hash TEXT PRIMARY KEY,
    expires_at TIMESTAMPTZ NOT NULL,
    used_at    TIMESTAMPTZ
);

-- +goose Down
DROP TABLE IF EXISTS config_join_tokens;
DROP TABLE IF EXISTS config_conflicts;
DROP TABLE IF EXISTS config_peer_revisions;
DROP TABLE IF EXISTS config_peers;
-- The origin check keeps 'peer' and 'merge': rows with them may exist and are
-- referenced as base revisions.
DROP INDEX IF EXISTS idx_config_revisions_uid;
ALTER TABLE config_revisions DROP COLUMN IF EXISTS merge_uid;
ALTER TABLE config_revisions DROP COLUMN IF EXISTS base_uid;
ALTER TABLE config_revisions DROP COLUMN IF EXISTS uid;
