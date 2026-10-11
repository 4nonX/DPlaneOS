-- +goose Up
-- Applying new commits automatically (off by default), and the webhook a Git
-- server calls after a push.
ALTER TABLE gitops_config ADD COLUMN IF NOT EXISTS auto_apply        INTEGER NOT NULL DEFAULT 0;
ALTER TABLE gitops_config ADD COLUMN IF NOT EXISTS webhook_secret    TEXT    NOT NULL DEFAULT '';
-- The last commit that was tried automatically, and how that went
-- (applied, held, failed). A commit is tried once.
ALTER TABLE gitops_config ADD COLUMN IF NOT EXISTS last_auto_commit  TEXT    NOT NULL DEFAULT '';
ALTER TABLE gitops_config ADD COLUMN IF NOT EXISTS last_auto_result  TEXT    NOT NULL DEFAULT '';
ALTER TABLE gitops_config ADD COLUMN IF NOT EXISTS last_auto_message TEXT    NOT NULL DEFAULT '';
ALTER TABLE gitops_config ADD COLUMN IF NOT EXISTS last_auto_at      TIMESTAMPTZ;

-- +goose Down
ALTER TABLE gitops_config DROP COLUMN IF EXISTS last_auto_at;
ALTER TABLE gitops_config DROP COLUMN IF EXISTS last_auto_message;
ALTER TABLE gitops_config DROP COLUMN IF EXISTS last_auto_result;
ALTER TABLE gitops_config DROP COLUMN IF EXISTS last_auto_commit;
ALTER TABLE gitops_config DROP COLUMN IF EXISTS webhook_secret;
ALTER TABLE gitops_config DROP COLUMN IF EXISTS auto_apply;
