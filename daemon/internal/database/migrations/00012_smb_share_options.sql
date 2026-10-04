-- +goose Up

-- Per-share SMB options. Until now Time Machine, shadow copies (Previous
-- Versions) and the recycle bin were global toggles applied to every share.
ALTER TABLE smb_shares ADD COLUMN IF NOT EXISTS time_machine       INTEGER NOT NULL DEFAULT 0;
ALTER TABLE smb_shares ADD COLUMN IF NOT EXISTS time_machine_quota TEXT    NOT NULL DEFAULT '';
ALTER TABLE smb_shares ADD COLUMN IF NOT EXISTS shadow_copy        INTEGER NOT NULL DEFAULT 0;
ALTER TABLE smb_shares ADD COLUMN IF NOT EXISTS recycle_bin        INTEGER NOT NULL DEFAULT 0;
ALTER TABLE smb_shares ADD COLUMN IF NOT EXISTS hosts_allow        TEXT    NOT NULL DEFAULT '';
ALTER TABLE smb_shares ADD COLUMN IF NOT EXISTS hosts_deny         TEXT    NOT NULL DEFAULT '';

-- Keep existing behaviour: a share inherits each global toggle that was on.
UPDATE smb_shares SET time_machine = 1
 WHERE EXISTS (SELECT 1 FROM settings WHERE key = 'smb_time_machine' AND value = '1');
UPDATE smb_shares SET shadow_copy = 1
 WHERE EXISTS (SELECT 1 FROM settings WHERE key = 'smb_shadow_copy' AND value = '1');
UPDATE smb_shares SET recycle_bin = 1
 WHERE EXISTS (SELECT 1 FROM settings WHERE key = 'smb_recycle_bin' AND value = '1');

-- +goose Down
ALTER TABLE smb_shares DROP COLUMN IF EXISTS hosts_deny;
ALTER TABLE smb_shares DROP COLUMN IF EXISTS hosts_allow;
ALTER TABLE smb_shares DROP COLUMN IF EXISTS recycle_bin;
ALTER TABLE smb_shares DROP COLUMN IF EXISTS shadow_copy;
ALTER TABLE smb_shares DROP COLUMN IF EXISTS time_machine_quota;
ALTER TABLE smb_shares DROP COLUMN IF EXISTS time_machine;
