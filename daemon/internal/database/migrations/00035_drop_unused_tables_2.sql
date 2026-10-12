-- +goose Up
-- Tables nothing reads or writes:
--   ha_cluster_config, ha_network_witness  settings of the Patroni-based HA
--                                          that this release removed
--   enterprise_audit_usage                 a usage counter no code ever wrote
DROP TABLE IF EXISTS ha_cluster_config;
DROP TABLE IF EXISTS ha_network_witness;
DROP TABLE IF EXISTS enterprise_audit_usage;

-- Filled only by an "acme:" or "certificates:" section of state.yaml and
-- read by nothing that requests or serves a certificate (certificates are
-- files under the certificate directory; Let's Encrypt is driven from the
-- Certificates page). Both sections are no longer part of state.yaml.
DROP TABLE IF EXISTS acme_config;
DROP TABLE IF EXISTS certificates;

-- +goose Down
SELECT 1;
