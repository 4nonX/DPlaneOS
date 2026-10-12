-- +goose Up
-- The licence record of the Compliance Engine, an optional closed program
-- that was fetched and started on activation. Licensing and that
-- integration are removed; nothing reads this table.
DROP TABLE IF EXISTS enterprise_license;
-- The API token that program was given at start (dplaned -ce-token-file).
DELETE FROM api_tokens WHERE name = 'compliance-engine-token';

-- +goose Down
SELECT 1;
