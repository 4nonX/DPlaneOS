-- +goose Up
-- File service accounts (internal/fileaccounts): every local user gets a
-- Unix and a Samba account so that SMB shares and FTP accept its password.
--   uid        the account's number (file ownership), stable once assigned
--   nt_hash    what SMB authenticates with (sealed like other secrets)
--   unix_hash  crypt(3) entry for /etc/shadow (FTP)
ALTER TABLE users
    ADD COLUMN IF NOT EXISTS uid       INTEGER UNIQUE,
    ADD COLUMN IF NOT EXISTS nt_hash   TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS unix_hash TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE users
    DROP COLUMN IF EXISTS uid,
    DROP COLUMN IF EXISTS nt_hash,
    DROP COLUMN IF EXISTS unix_hash;
