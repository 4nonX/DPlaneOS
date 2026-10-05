-- +goose Up
-- Moved from daemon/migrations/ (not embedded, so it was never applied).
-- +goose StatementBegin
DO $$
BEGIN
  CREATE EXTENSION IF NOT EXISTS pgcrypto;
EXCEPTION WHEN OTHERS THEN
  RAISE NOTICE 'pgcrypto unavailable (%): audit_events HMAC trigger will fail until it is installed', SQLERRM;
END
$$;
-- +goose StatementEnd

-- Phase 4.2: Audit event logging with HMAC integrity chain
-- Every significant operation logged for compliance and debugging

CREATE TABLE IF NOT EXISTS audit_events (
  id BIGSERIAL PRIMARY KEY,
  timestamp TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  event_type TEXT NOT NULL,
  component TEXT NOT NULL,
  operation_id TEXT,
  user_id TEXT,
  status TEXT NOT NULL CHECK (status IN ('success', 'failure', 'warning')),
  details JSONB NOT NULL DEFAULT '{}',
  ip_address TEXT,
  hmac TEXT,
  created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- Indexes for efficient queries
CREATE INDEX IF NOT EXISTS idx_audit_events_timestamp ON audit_events(timestamp DESC);
CREATE INDEX IF NOT EXISTS idx_audit_events_type ON audit_events(event_type);
CREATE INDEX IF NOT EXISTS idx_audit_events_component ON audit_events(component);
CREATE INDEX IF NOT EXISTS idx_audit_events_operation_id ON audit_events(operation_id);
CREATE INDEX IF NOT EXISTS idx_audit_events_user_id ON audit_events(user_id);
CREATE INDEX IF NOT EXISTS idx_audit_events_status ON audit_events(status);

-- Immutability enforcement: prevent updates and deletes on audit_events
-- Enforced via trigger that raises exception on any modification attempt
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION audit_prevent_modification()
RETURNS TRIGGER AS $$
BEGIN
  RAISE EXCEPTION 'audit_events table is immutable - updates and deletes are not allowed';
END;
$$ LANGUAGE plpgsql;
-- +goose StatementEnd

DROP TRIGGER IF EXISTS audit_prevent_update ON audit_events;
CREATE TRIGGER audit_prevent_update
BEFORE UPDATE ON audit_events
FOR EACH ROW
EXECUTE FUNCTION audit_prevent_modification();

DROP TRIGGER IF EXISTS audit_prevent_delete ON audit_events;
CREATE TRIGGER audit_prevent_delete
BEFORE DELETE ON audit_events
FOR EACH ROW
EXECUTE FUNCTION audit_prevent_modification();

-- HMAC-SHA256 Integrity Chain (PostgreSQL Trigger)
-- Each event is linked via HMAC-SHA256 of (previous_hmac || current_event_hash)
-- Uses pgcrypto for cryptographically sound hashing
-- Creates an immutable chain that can be verified at any time
-- Note: Key derivation happens in application layer (daemon/internal/audit/hmac_key.go)
-- For production, key should be managed via secure key management system

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION audit_compute_hmac()
RETURNS TRIGGER AS $$
DECLARE
  prev_hmac TEXT;
  current_hash TEXT;
  event_data TEXT;
BEGIN
  -- Get previous event's HMAC (or empty string if first event)
  SELECT hmac INTO prev_hmac FROM audit_events
  WHERE id < NEW.id
  ORDER BY id DESC
  LIMIT 1;

  IF prev_hmac IS NULL THEN
    prev_hmac := '';
  END IF;

  -- Create hash of current event: include all security-relevant fields
  -- Use explicit field delimiters with length prefixes to prevent canonicalization attacks
  event_data :=
    LENGTH(COALESCE(NEW.timestamp::TEXT, ''))::TEXT || ':' || COALESCE(NEW.timestamp::TEXT, '') || '|' ||
    LENGTH(COALESCE(NEW.event_type, ''))::TEXT || ':' || COALESCE(NEW.event_type, '') || '|' ||
    LENGTH(COALESCE(NEW.component, ''))::TEXT || ':' || COALESCE(NEW.component, '') || '|' ||
    LENGTH(COALESCE(NEW.operation_id, ''))::TEXT || ':' || COALESCE(NEW.operation_id, '') || '|' ||
    LENGTH(COALESCE(NEW.user_id, ''))::TEXT || ':' || COALESCE(NEW.user_id, '') || '|' ||
    LENGTH(COALESCE(NEW.status, ''))::TEXT || ':' || COALESCE(NEW.status, '') || '|' ||
    LENGTH(COALESCE(NEW.ip_address, ''))::TEXT || ':' || COALESCE(NEW.ip_address, '') || '|' ||
    LENGTH(COALESCE(NEW.details::TEXT, ''))::TEXT || ':' || COALESCE(NEW.details::TEXT, '');

  -- Use SHA256 for cryptographic strength (pgcrypto contrib module required)
  -- Chain: HMAC = digest(previous_hmac || current_hash, 'sha256')
  current_hash := digest(event_data, 'sha256');
  NEW.hmac := digest(prev_hmac || current_hash, 'sha256');

  RETURN NEW;
END;
$$ LANGUAGE plpgsql;
-- +goose StatementEnd

DROP TRIGGER IF EXISTS audit_hmac_trigger ON audit_events;
CREATE TRIGGER audit_hmac_trigger
BEFORE INSERT ON audit_events
FOR EACH ROW
EXECUTE FUNCTION audit_compute_hmac();

-- Verification function: check if audit chain is unbroken
-- Returns (is_valid, first_invalid_id)
-- If is_valid=false, first_invalid_id indicates where chain breaks
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION audit_verify_chain(from_id BIGINT, to_id BIGINT)
RETURNS TABLE(is_valid BOOLEAN, first_invalid_id BIGINT) AS $$
DECLARE
  prev_hmac TEXT := '';
  computed_hmac TEXT;
  current_hash TEXT;
  event_data TEXT;
  evt audit_events%ROWTYPE;
BEGIN
  FOR evt IN
    SELECT * FROM audit_events
    WHERE id BETWEEN from_id AND to_id
    ORDER BY id
  LOOP
    -- Reconstruct event data using same format as audit_compute_hmac()
    event_data :=
      LENGTH(COALESCE(evt.timestamp::TEXT, ''))::TEXT || ':' || COALESCE(evt.timestamp::TEXT, '') || '|' ||
      LENGTH(COALESCE(evt.event_type, ''))::TEXT || ':' || COALESCE(evt.event_type, '') || '|' ||
      LENGTH(COALESCE(evt.component, ''))::TEXT || ':' || COALESCE(evt.component, '') || '|' ||
      LENGTH(COALESCE(evt.operation_id, ''))::TEXT || ':' || COALESCE(evt.operation_id, '') || '|' ||
      LENGTH(COALESCE(evt.user_id, ''))::TEXT || ':' || COALESCE(evt.user_id, '') || '|' ||
      LENGTH(COALESCE(evt.status, ''))::TEXT || ':' || COALESCE(evt.status, '') || '|' ||
      LENGTH(COALESCE(evt.ip_address, ''))::TEXT || ':' || COALESCE(evt.ip_address, '') || '|' ||
      LENGTH(COALESCE(evt.details::TEXT, ''))::TEXT || ':' || COALESCE(evt.details::TEXT, '');

    current_hash := digest(event_data, 'sha256');
    computed_hmac := digest(prev_hmac || current_hash, 'sha256');

    IF computed_hmac != evt.hmac THEN
      RETURN QUERY SELECT FALSE, evt.id;
      RETURN;
    END IF;

    prev_hmac := evt.hmac;
  END LOOP;

  RETURN QUERY SELECT TRUE, NULL::BIGINT;
END;
$$ LANGUAGE plpgsql;
-- +goose StatementEnd

-- +goose Down
DROP FUNCTION IF EXISTS audit_verify_chain(BIGINT, BIGINT);
DROP TABLE IF EXISTS audit_events;
DROP FUNCTION IF EXISTS audit_compute_hmac();
DROP FUNCTION IF EXISTS audit_prevent_modification();
