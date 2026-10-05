-- +goose Up
-- Moved from daemon/migrations/ (not embedded, so it was never applied).
-- Phase 3.1: Operation Journal for crash recovery
-- Every long-running operation writes state transitions here.
-- Immutable history allows resuming interrupted operations.

CREATE TABLE IF NOT EXISTS operation_journal (
  id TEXT PRIMARY KEY,
  operation_type TEXT NOT NULL,
  state TEXT NOT NULL CHECK (state IN ('declared', 'validating', 'in_progress', 'completed', 'failed', 'rolled_back')),
  details JSONB NOT NULL DEFAULT '{}',
  error_msg TEXT,
  started_at TIMESTAMP NOT NULL,
  completed_at TIMESTAMP,
  updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- Index for finding incomplete operations at startup
CREATE INDEX IF NOT EXISTS idx_operation_journal_state ON operation_journal(state)
  WHERE state NOT IN ('completed', 'failed', 'rolled_back');

-- Index for finding operations by type
CREATE INDEX IF NOT EXISTS idx_operation_journal_type ON operation_journal(operation_type);

-- Index for finding recent operations
CREATE INDEX IF NOT EXISTS idx_operation_journal_started ON operation_journal(started_at DESC);

-- +goose Down
DROP TABLE IF EXISTS operation_journal;
