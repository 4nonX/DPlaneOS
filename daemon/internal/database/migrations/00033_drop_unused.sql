-- +goose Up
-- Tables and a setting that nothing uses.
DROP TABLE IF EXISTS enterprise_audit_usage;
DROP TABLE IF EXISTS enterprise_license;
DELETE FROM system_config WHERE key = 'license_key';

-- +goose Down
SELECT 1;
