-- +goose Up
-- File-sharing identities: every local user is also a system account (SMB,
-- NFS and file ownership need one) with a UID from 3000 up, and Samba
-- authenticates with the NT hash of the password (sealed; set whenever the
-- password is set). Users without it cannot use SMB until their password is
-- set again.
ALTER TABLE users ADD COLUMN IF NOT EXISTS uid INTEGER UNIQUE;
ALTER TABLE users ADD COLUMN IF NOT EXISTS smb_nt_hash TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE users DROP COLUMN IF EXISTS smb_nt_hash;
ALTER TABLE users DROP COLUMN IF EXISTS uid;
