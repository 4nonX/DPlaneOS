-- +goose Up
-- Tables nothing reads or writes:
--   ha_cluster_config, ha_network_witness  settings of the Patroni-based HA
--                                          that this release removed
--   enterprise_audit_usage                 a usage counter no code ever wrote
DROP TABLE IF EXISTS ha_cluster_config;
DROP TABLE IF EXISTS ha_network_witness;
DROP TABLE IF EXISTS enterprise_audit_usage;

-- +goose Down
SELECT 1;
