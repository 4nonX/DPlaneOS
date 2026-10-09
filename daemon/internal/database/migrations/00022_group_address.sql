-- +goose Up
-- Design 0001, phase 3e: a storage group's floating address (replaces the
-- keepalived VIP of the Patroni-based HA setup). Empty: none.
ALTER TABLE storage_groups ADD COLUMN IF NOT EXISTS address   TEXT NOT NULL DEFAULT '';
ALTER TABLE storage_groups ADD COLUMN IF NOT EXISTS interface TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE storage_groups DROP COLUMN IF EXISTS interface;
ALTER TABLE storage_groups DROP COLUMN IF EXISTS address;
