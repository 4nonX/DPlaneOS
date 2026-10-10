-- +goose Up
-- The single-repository git sync (git_sync_config, /api/git-sync/config|pull|
-- status|stacks|deploy|export|push) was replaced by git_sync_repos.
DROP TABLE IF EXISTS git_sync_config;

-- +goose Down
CREATE TABLE IF NOT EXISTS git_sync_config (
    id            BIGINT PRIMARY KEY CHECK (id = 1),
    repo_url      TEXT NOT NULL DEFAULT '',
    branch        TEXT NOT NULL DEFAULT 'main',
    local_path    TEXT NOT NULL DEFAULT '/var/lib/dplaneos/git-stacks',
    sync_interval INTEGER NOT NULL DEFAULT 0,
    auto_deploy   INTEGER NOT NULL DEFAULT 0,
    auth_type     TEXT NOT NULL DEFAULT 'none',
    auth_token    TEXT NOT NULL DEFAULT '',
    ssh_key_path  TEXT NOT NULL DEFAULT '',
    host_key_mode TEXT NOT NULL DEFAULT 'accept',
    commit_name   TEXT NOT NULL DEFAULT 'DPlaneOS',
    commit_email  TEXT NOT NULL DEFAULT 'dplaneos@localhost',
    last_sync_at  TEXT NOT NULL DEFAULT '',
    last_commit   TEXT NOT NULL DEFAULT '',
    last_error    TEXT NOT NULL DEFAULT ''
);
