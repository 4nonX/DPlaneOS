-- +goose Up
-- Samba clustering (CTDB) was removed: it needs a file system mounted on all
-- nodes at once, which ZFS is not. The tables were created at daemon start.
DROP TABLE IF EXISTS ctdb_public_addresses;
DROP TABLE IF EXISTS ctdb_config;

-- +goose Down
-- Nothing to restore: the tables held settings nothing read.
SELECT 1;
