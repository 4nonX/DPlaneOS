-- +goose Up
-- Design 0001, phase 3d: resources of a storage group that run only on its
-- owner (Docker stacks with volumes on the group's pools, NVMe-oF exports of
-- its zvols). The owner publishes their definitions; every candidate keeps
-- them here so it can start them when it becomes the owner.
CREATE TABLE IF NOT EXISTS group_resources (
    group_name TEXT        NOT NULL,
    kind       TEXT        NOT NULL CHECK (kind IN ('stack', 'nvme')),
    key        TEXT        NOT NULL,   -- stack name, subsystem NQN
    payload    JSONB       NOT NULL,   -- {"yaml": "..."} or the nvmet export
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (group_name, kind, key)
);

-- +goose Down
DROP TABLE IF EXISTS group_resources;
