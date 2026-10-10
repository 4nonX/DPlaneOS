-- +goose Up
-- At most one pending storage operation per target, enforced by the
-- database: storageops.Begin checked and inserted in two statements, so two
-- concurrent requests (a double-clicked "Replace") could both start.
-- Older duplicates left by that race are closed first, or the index could
-- not be created and the daemon would not start.
UPDATE storage_operations s
SET state = 'failed',
    error = 'closed by migration 00024: duplicate pending operation on the same target',
    completed_at = NOW()
WHERE s.state = 'pending'
  AND EXISTS (
    SELECT 1 FROM storage_operations o
    WHERE o.target = s.target AND o.state = 'pending'
      AND (o.started_at > s.started_at OR (o.started_at = s.started_at AND o.id > s.id))
  );
CREATE UNIQUE INDEX IF NOT EXISTS uq_storage_ops_pending_target
    ON storage_operations (target) WHERE state = 'pending';

-- +goose Down
DROP INDEX IF EXISTS uq_storage_ops_pending_target;
