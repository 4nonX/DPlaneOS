-- +goose Up
-- Group memberships are keyed by group and user name: renaming either
-- failed on the foreign keys (no ON UPDATE action). Memberships now follow
-- the rename.
ALTER TABLE group_members DROP CONSTRAINT IF EXISTS group_members_group_name_fkey;
ALTER TABLE group_members ADD CONSTRAINT group_members_group_name_fkey
    FOREIGN KEY (group_name) REFERENCES groups(name) ON DELETE CASCADE ON UPDATE CASCADE;
ALTER TABLE group_members DROP CONSTRAINT IF EXISTS group_members_username_fkey;
ALTER TABLE group_members ADD CONSTRAINT group_members_username_fkey
    FOREIGN KEY (username) REFERENCES users(username) ON DELETE CASCADE ON UPDATE CASCADE;

-- +goose Down
ALTER TABLE group_members DROP CONSTRAINT IF EXISTS group_members_group_name_fkey;
ALTER TABLE group_members ADD CONSTRAINT group_members_group_name_fkey
    FOREIGN KEY (group_name) REFERENCES groups(name) ON DELETE CASCADE;
ALTER TABLE group_members DROP CONSTRAINT IF EXISTS group_members_username_fkey;
ALTER TABLE group_members ADD CONSTRAINT group_members_username_fkey
    FOREIGN KEY (username) REFERENCES users(username) ON DELETE CASCADE;
