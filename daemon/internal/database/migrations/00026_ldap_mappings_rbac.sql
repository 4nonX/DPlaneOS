-- +goose Up
-- LDAP group mappings name RBAC roles now (sync assigns them in user_roles).
-- The API used to accept power_user and readonly, which are no RBAC roles:
-- mapped to the closest built-in roles. role_id was never set.
UPDATE ldap_group_mappings SET role_name = 'operator' WHERE role_name = 'power_user';
UPDATE ldap_group_mappings SET role_name = 'viewer'   WHERE role_name = 'readonly';
UPDATE ldap_group_mappings m SET role_id = r.id FROM roles r WHERE r.name = m.role_name;
UPDATE ldap_config SET default_role = 'operator' WHERE default_role = 'power_user';
UPDATE ldap_config SET default_role = 'viewer'   WHERE default_role = 'readonly';

-- +goose Down
SELECT 1;
