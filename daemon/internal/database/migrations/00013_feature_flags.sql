-- +goose Up
-- Moved from daemon/migrations/ (not embedded, so it was never applied).
-- Phase 2.3: Feature flag system for experimental/beta features
-- Allows safe disable/enable of features without data loss

CREATE TABLE IF NOT EXISTS feature_flags (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL,
  description TEXT,
  state TEXT NOT NULL CHECK (state IN ('disabled', 'beta', 'stable', 'deprecated')),
  enabled_at TIMESTAMP,
  disabled_at TIMESTAMP,
  error_msg TEXT,
  created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_feature_flags_state ON feature_flags(state);

-- Initial feature flag registrations
-- These will be inserted by the application on startup if they don't exist
-- HA features: experimental (require testing)
-- - ha_clustering: Active/passive or active/active clustering
-- - patroni_ha: PostgreSQL HA via Patroni
-- - network_witness: Network-based quorum witness

-- Storage features: experimental or beta
-- - nvmeof_support: NVMe over Fabrics (TCP)
-- - kerberos_integration: Kerberos authentication

-- Enterprise features: stable (released)
-- - enterprise_licensing: Ed25519 license verification
-- - compliance_engine: Compliance Engine integration

-- +goose Down
DROP TABLE IF EXISTS feature_flags;
