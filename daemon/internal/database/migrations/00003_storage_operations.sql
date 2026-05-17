-- +goose Up
CREATE TABLE IF NOT EXISTS storage_operations (
    id             BIGSERIAL     PRIMARY KEY,
    operation_type TEXT          NOT NULL,
    target         TEXT          NOT NULL,
    state          TEXT          NOT NULL DEFAULT 'pending'
                                 CHECK (state IN ('pending','committed','failed')),
    error          TEXT          NOT NULL DEFAULT '',
    started_at     TIMESTAMPTZ   NOT NULL DEFAULT NOW(),
    completed_at   TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_storage_ops_target_state ON storage_operations (target, state);
CREATE INDEX IF NOT EXISTS idx_storage_ops_started_at   ON storage_operations (started_at DESC);

-- +goose Down
DROP TABLE IF EXISTS storage_operations;
