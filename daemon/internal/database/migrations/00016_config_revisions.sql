-- +goose Up
-- Design 0001, Phase 1: revision history of managed configuration.
-- One row per change of one resource. payload holds the resource in its
-- state.yaml form (secrets as fingerprints); NULL means the resource was
-- deleted. Rows written together share a changeset id.

CREATE TABLE IF NOT EXISTS config_revisions (
    id            BIGSERIAL PRIMARY KEY,
    changeset     TEXT        NOT NULL,
    scope_type    TEXT        NOT NULL CHECK (scope_type IN ('node', 'group', 'cluster', 'fleet')),
    scope_id      TEXT        NOT NULL DEFAULT '',
    resource_kind TEXT        NOT NULL,
    resource_key  TEXT        NOT NULL,
    payload       JSONB,
    base_revision BIGINT      REFERENCES config_revisions(id),
    origin        TEXT        NOT NULL CHECK (origin IN ('baseline', 'gui', 'detected', 'import', 'rollback', 'git')),
    origin_node   TEXT        NOT NULL DEFAULT '',
    author        TEXT        NOT NULL DEFAULT '',
    note          TEXT        NOT NULL DEFAULT '',
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_config_revisions_resource
    ON config_revisions (resource_kind, resource_key, id DESC);
CREATE INDEX IF NOT EXISTS idx_config_revisions_changeset
    ON config_revisions (changeset);
CREATE INDEX IF NOT EXISTS idx_config_revisions_created
    ON config_revisions (created_at DESC);

-- +goose Down
DROP TABLE IF EXISTS config_revisions;
