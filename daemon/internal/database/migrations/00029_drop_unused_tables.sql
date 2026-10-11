-- +goose Up
-- Tables of mechanisms that were never connected to anything:
--   feature_flags      toggles under Settings > Features that no code consulted
--   operation_journal  a crash-recovery journal no operation was ever written to
--                      (storage operations are journalled in storage_operations)
--   audit_events       a second event table beside the audit log (audit_logs)
DROP TABLE IF EXISTS feature_flags;
DROP TABLE IF EXISTS operation_journal;
DROP TABLE IF EXISTS audit_events CASCADE;
DROP FUNCTION IF EXISTS audit_prevent_modification() CASCADE;
DROP FUNCTION IF EXISTS audit_compute_hmac() CASCADE;
DROP FUNCTION IF EXISTS audit_verify_chain(BIGINT, BIGINT) CASCADE;

-- +goose Down
SELECT 1;
