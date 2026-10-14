-- +goose Up
-- Design 0001 phase 5: the Git backend (section 5.8, ADR-0007).

-- Repository settings, on top of the existing GitOps settings row.
ALTER TABLE gitops_config ADD COLUMN IF NOT EXISTS branch            TEXT    NOT NULL DEFAULT 'main';
ALTER TABLE gitops_config ADD COLUMN IF NOT EXISTS layout            TEXT    NOT NULL DEFAULT 'single';
ALTER TABLE gitops_config ADD COLUMN IF NOT EXISTS policy            TEXT    NOT NULL DEFAULT 'direct';
ALTER TABLE gitops_config ADD COLUMN IF NOT EXISTS provider          TEXT    NOT NULL DEFAULT 'none';
ALTER TABLE gitops_config ADD COLUMN IF NOT EXISTS provider_api_url  TEXT    NOT NULL DEFAULT '';
ALTER TABLE gitops_config ADD COLUMN IF NOT EXISTS provider_repo     TEXT    NOT NULL DEFAULT '';
ALTER TABLE gitops_config ADD COLUMN IF NOT EXISTS provider_cred     TEXT    NOT NULL DEFAULT '';   -- git_credentials.name
ALTER TABLE gitops_config ADD COLUMN IF NOT EXISTS auto_apply        BOOLEAN NOT NULL DEFAULT FALSE;
ALTER TABLE gitops_config ADD COLUMN IF NOT EXISTS prune             BOOLEAN NOT NULL DEFAULT FALSE;
ALTER TABLE gitops_config ADD COLUMN IF NOT EXISTS self_heal         BOOLEAN NOT NULL DEFAULT FALSE;
ALTER TABLE gitops_config ADD COLUMN IF NOT EXISTS verify_signatures BOOLEAN NOT NULL DEFAULT FALSE;
ALTER TABLE gitops_config ADD COLUMN IF NOT EXISTS allowed_signers   TEXT    NOT NULL DEFAULT '';
ALTER TABLE gitops_config ADD COLUMN IF NOT EXISTS gpg_keys          TEXT    NOT NULL DEFAULT '';
ALTER TABLE gitops_config ADD COLUMN IF NOT EXISTS sign_commits      BOOLEAN NOT NULL DEFAULT FALSE;
ALTER TABLE gitops_config ADD COLUMN IF NOT EXISTS interval_secs     INTEGER NOT NULL DEFAULT 300;
ALTER TABLE gitops_config ADD COLUMN IF NOT EXISTS webhook_secret    TEXT    NOT NULL DEFAULT '';   -- sealed
ALTER TABLE gitops_config ADD COLUMN IF NOT EXISTS settings_version  BIGINT  NOT NULL DEFAULT 0;
ALTER TABLE gitops_config ADD COLUMN IF NOT EXISTS settings_node     TEXT    NOT NULL DEFAULT '';

-- Token credentials: the user name sent with the token (GitHub accepts any,
-- Gitea, Forgejo and GitLab may want the account's). '' = x-access-token.
ALTER TABLE git_credentials ADD COLUMN IF NOT EXISTS username TEXT NOT NULL DEFAULT '';

-- Remotes of the repository, in order (several remotes, ADR-0007). Each
-- names the credentials it fetches and pushes with (a read-only deploy key
-- can fetch while another credential pushes).
CREATE TABLE IF NOT EXISTS git_remotes (
    position   INTEGER PRIMARY KEY CHECK (position >= 0),
    url        TEXT    NOT NULL,
    fetch_cred TEXT    NOT NULL DEFAULT '',  -- git_credentials.name, '' = none
    push_cred  TEXT    NOT NULL DEFAULT ''
);

-- The existing single repository becomes the first remote.
INSERT INTO git_remotes (position, url, fetch_cred, push_cred)
SELECT 0, r.repo_url,
       COALESCE((SELECT c.name FROM git_credentials c WHERE r.auth_type = 'cred' AND c.id::text = r.auth_token), ''),
       COALESCE((SELECT c.name FROM git_credentials c WHERE r.auth_type = 'cred' AND c.id::text = r.auth_token), '')
FROM gitops_config g JOIN git_sync_repos r ON r.id = g.repo_id
WHERE g.id = 1 AND r.repo_url <> ''
ON CONFLICT (position) DO NOTHING;
UPDATE gitops_config g SET branch = r.branch FROM git_sync_repos r WHERE g.id = 1 AND r.id = g.repo_id AND r.branch <> '';

-- Per scope (cluster, group:<name>, node:<name>, all): the commit this node
-- last reconciled with, the open pull request, and what is outstanding.
CREATE TABLE IF NOT EXISTS git_scopes (
    scope        TEXT PRIMARY KEY,
    synced_sha   TEXT        NOT NULL DEFAULT '',
    synced_at    TIMESTAMPTZ,
    fetched_sha  TEXT        NOT NULL DEFAULT '',
    fetched_at   TIMESTAMPTZ,
    pr_number    INTEGER     NOT NULL DEFAULT 0,
    pr_url       TEXT        NOT NULL DEFAULT '',
    pr_state     TEXT        NOT NULL DEFAULT '',
    pr_branch    TEXT        NOT NULL DEFAULT '',
    -- content of a pull request that was closed without merging: not
    -- proposed again until this node's version changes
    declined     TEXT        NOT NULL DEFAULT '',
    pr_content   TEXT        NOT NULL DEFAULT '',  -- content of the open pull request
    last_error   TEXT        NOT NULL DEFAULT '',
    attempts     INTEGER     NOT NULL DEFAULT 0,
    next_try     TIMESTAMPTZ,
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- The version of each resource at the reconciled commit: the base of the
-- three-way comparison (which side changed it).
CREATE TABLE IF NOT EXISTS git_base (
    scope    TEXT NOT NULL,
    resource TEXT NOT NULL,   -- kind/key
    uid      TEXT NOT NULL,   -- config_revisions.uid of that version
    hash     TEXT NOT NULL,
    PRIMARY KEY (scope, resource)
);

-- Holds (section 5.8): a scope whose apply failed, whose commits are not
-- signed by an allowed key, or that the empty-state guard stopped stays on
-- its last good revision; automatic apply pauses until Resume or Revert.
CREATE TABLE IF NOT EXISTS config_holds (
    id          BIGSERIAL PRIMARY KEY,
    scope       TEXT        NOT NULL,
    reason      TEXT        NOT NULL,
    commit_sha  TEXT        NOT NULL DEFAULT '',  -- the commit that could not be taken
    good_sha    TEXT        NOT NULL DEFAULT '',  -- the last commit taken successfully
    since       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    set_by      TEXT        NOT NULL DEFAULT '',
    cleared_at  TIMESTAMPTZ,
    cleared_by  TEXT        NOT NULL DEFAULT '',
    resolution  TEXT        NOT NULL DEFAULT ''   -- resumed, reverted
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_config_holds_open ON config_holds (scope) WHERE cleared_at IS NULL;

-- +goose Down
DROP TABLE IF EXISTS config_holds;
DROP TABLE IF EXISTS git_base;
DROP TABLE IF EXISTS git_scopes;
DROP TABLE IF EXISTS git_remotes;
ALTER TABLE gitops_config DROP COLUMN IF EXISTS settings_node;
ALTER TABLE gitops_config DROP COLUMN IF EXISTS settings_version;
ALTER TABLE gitops_config DROP COLUMN IF EXISTS webhook_secret;
ALTER TABLE gitops_config DROP COLUMN IF EXISTS interval_secs;
ALTER TABLE gitops_config DROP COLUMN IF EXISTS sign_commits;
ALTER TABLE gitops_config DROP COLUMN IF EXISTS gpg_keys;
ALTER TABLE gitops_config DROP COLUMN IF EXISTS allowed_signers;
ALTER TABLE gitops_config DROP COLUMN IF EXISTS verify_signatures;
ALTER TABLE gitops_config DROP COLUMN IF EXISTS self_heal;
ALTER TABLE gitops_config DROP COLUMN IF EXISTS prune;
ALTER TABLE gitops_config DROP COLUMN IF EXISTS auto_apply;
ALTER TABLE gitops_config DROP COLUMN IF EXISTS provider_cred;
ALTER TABLE gitops_config DROP COLUMN IF EXISTS provider_repo;
ALTER TABLE gitops_config DROP COLUMN IF EXISTS provider_api_url;
ALTER TABLE gitops_config DROP COLUMN IF EXISTS provider;
ALTER TABLE gitops_config DROP COLUMN IF EXISTS policy;
ALTER TABLE gitops_config DROP COLUMN IF EXISTS layout;
ALTER TABLE gitops_config DROP COLUMN IF EXISTS branch;
ALTER TABLE git_credentials DROP COLUMN IF EXISTS username;
