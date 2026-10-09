-- +goose Up
-- Design 0001, phase 3e: moving an HA pair off the shared Patroni database.
-- Written while both nodes still share the database, so after the split each
-- node's own copy holds the whole plan.
CREATE TABLE IF NOT EXISTS ha_split (
    id           INTEGER PRIMARY KEY DEFAULT 1 CHECK (id = 1),
    state        TEXT        NOT NULL,          -- planned, go, finished, cancelled
    coordinator  TEXT        NOT NULL,          -- machine id of the node that started it (holds the pools)
    secret       TEXT        NOT NULL,          -- sealed; the pair's config-sync secret
    group_name   TEXT        NOT NULL,
    topology     TEXT        NOT NULL,
    pools        JSONB       NOT NULL,
    address      TEXT        NOT NULL DEFAULT '',
    interface    TEXT        NOT NULL DEFAULT '',
    error        TEXT        NOT NULL DEFAULT '',
    started_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    finished_at  TIMESTAMPTZ
);

-- One row per node, written by that node.
CREATE TABLE IF NOT EXISTS ha_split_nodes (
    machine_id     TEXT PRIMARY KEY,               -- /etc/machine-id, first 8 characters
    hostname       TEXT        NOT NULL,
    ip             TEXT        NOT NULL,           -- address on the HA network (Patroni)
    config_node_id TEXT        NOT NULL,           -- this node's configuration identity after the split
    url            TEXT        NOT NULL,           -- how the other node reaches this one
    patroni_role   TEXT        NOT NULL DEFAULT '',
    status         TEXT        NOT NULL,           -- ready, failed, switching, split
    error          TEXT        NOT NULL DEFAULT '',
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- +goose Down
DROP TABLE IF EXISTS ha_split_nodes;
DROP TABLE IF EXISTS ha_split;
