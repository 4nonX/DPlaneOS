-- +goose Up
-- Git-backed stacks deploy only when the commit changes (as Komodo compares
-- its deployed and latest hash), report deploy failures, and can be deployed
-- by a push notification (webhook) signed with a per-repository secret.
ALTER TABLE git_sync_repos ADD COLUMN IF NOT EXISTS deployed_commit TEXT NOT NULL DEFAULT '';
ALTER TABLE git_sync_repos ADD COLUMN IF NOT EXISTS deploy_error    TEXT NOT NULL DEFAULT '';
ALTER TABLE git_sync_repos ADD COLUMN IF NOT EXISTS webhook_secret  TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE git_sync_repos DROP COLUMN IF EXISTS webhook_secret;
ALTER TABLE git_sync_repos DROP COLUMN IF EXISTS deploy_error;
ALTER TABLE git_sync_repos DROP COLUMN IF EXISTS deployed_commit;
