-- +goose Up
-- Design 0001, phase 3a: Corosync cluster and third vote (QDevice).

-- The cluster this node belongs to (one row). authkey is the sealed corosync
-- authkey (base64), the same on every member.
CREATE TABLE IF NOT EXISTS cluster_quorum (
    id           INT PRIMARY KEY DEFAULT 1 CHECK (id = 1),
    config       JSONB       NOT NULL,
    authkey      TEXT        NOT NULL,
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Codes this node issued for adding a third vote, and the enrollment in
-- progress (CA received, certificate request sent).
CREATE TABLE IF NOT EXISTS quorum_enrollments (
    token_hash   TEXT PRIMARY KEY,
    expires_at   TIMESTAMPTZ NOT NULL,
    ca_pem       TEXT        NOT NULL DEFAULT '',
    used_at      TIMESTAMPTZ
);

-- Clusters this node serves as third vote for (corosync-qnetd).
CREATE TABLE IF NOT EXISTS quorum_witness_clusters (
    cluster_name TEXT PRIMARY KEY,
    node_url     TEXT        NOT NULL DEFAULT '',
    enrolled_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- +goose Down
DROP TABLE IF EXISTS quorum_witness_clusters;
DROP TABLE IF EXISTS quorum_enrollments;
DROP TABLE IF EXISTS cluster_quorum;
