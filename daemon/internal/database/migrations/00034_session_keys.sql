-- +goose Up
-- The sessions table stores the SHA-256 of a session token (hex), not the
-- token: a copy of the table holds nothing a browser can log in with.
-- Sessions created before keep working.
UPDATE sessions SET session_id = encode(sha256(convert_to(session_id, 'UTF8')), 'hex');

-- +goose Down
-- The tokens cannot be recovered: end every session.
DELETE FROM sessions;
