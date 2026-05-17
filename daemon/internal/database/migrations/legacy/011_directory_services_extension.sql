-- D-PlaneOS v7.3.0 - Enterprise Directory Services
-- Migration 011: AD / Open Directory Extension

-- Add new columns to ldap_config for AD and multi-provider support
ALTER TABLE ldap_config ADD COLUMN provider_type TEXT NOT NULL DEFAULT 'ad';
ALTER TABLE ldap_config ADD COLUMN realm TEXT NOT NULL DEFAULT '';
ALTER TABLE ldap_config ADD COLUMN ad_domain TEXT NOT NULL DEFAULT '';
ALTER TABLE ldap_config ADD COLUMN netbios_name TEXT NOT NULL DEFAULT '';
ALTER TABLE ldap_config ADD COLUMN join_status TEXT NOT NULL DEFAULT 'not_joined';
ALTER TABLE ldap_config ADD COLUMN idmap_range_start INTEGER NOT NULL DEFAULT 10000;
ALTER TABLE ldap_config ADD COLUMN idmap_range_end INTEGER NOT NULL DEFAULT 999999;

-- Migration 012: Open Directory Presets
-- (Already handled by logic, but ensuring fields exist)
