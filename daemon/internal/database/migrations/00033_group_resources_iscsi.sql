-- +goose Up
-- iSCSI targets on a storage group's volumes follow the group, like its
-- stacks and NVMe-oF exports.
ALTER TABLE group_resources DROP CONSTRAINT IF EXISTS group_resources_kind_check;
ALTER TABLE group_resources ADD CONSTRAINT group_resources_kind_check CHECK (kind IN ('stack', 'nvme', 'iscsi'));

-- +goose Down
DELETE FROM group_resources WHERE kind = 'iscsi';
ALTER TABLE group_resources DROP CONSTRAINT IF EXISTS group_resources_kind_check;
ALTER TABLE group_resources ADD CONSTRAINT group_resources_kind_check CHECK (kind IN ('stack', 'nvme'));
