-- +goose Up
-- Settings › Features listed states that nothing checked, and no operation
-- ever wrote to the journal: both tables are gone with the code.
DROP TABLE IF EXISTS feature_flags;
DROP TABLE IF EXISTS operation_journal;

-- +goose Down
SELECT 1;
