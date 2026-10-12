-- +goose Up
-- Maintenance mode of this node: it hands its storage groups to another
-- candidate, takes none over, and tells the other nodes so.
CREATE TABLE IF NOT EXISTS node_maintenance (
    id      INTEGER PRIMARY KEY CHECK (id = 1),
    since   TIMESTAMPTZ,
    reason  TEXT NOT NULL DEFAULT '',
    set_by  TEXT NOT NULL DEFAULT ''
);
INSERT INTO node_maintenance (id) VALUES (1) ON CONFLICT (id) DO NOTHING;

-- +goose Down
SELECT 1;
