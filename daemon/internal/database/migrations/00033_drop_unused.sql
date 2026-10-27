-- +goose Up
-- Tables of add-on hooks that no longer exist (they all began with enterprise_).
-- +goose StatementBegin
DO $$
DECLARE t text;
BEGIN
  FOR t IN SELECT tablename FROM pg_tables WHERE schemaname = current_schema() AND tablename LIKE 'enterprise\_%' LOOP
    EXECUTE format('DROP TABLE IF EXISTS %I', t);
  END LOOP;
END $$;
-- +goose StatementEnd

-- +goose Down
SELECT 1;
