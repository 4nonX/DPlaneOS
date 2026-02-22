-- Migration 008: RBAC System
-- Adds role-based access control without breaking existing functionality
-- Author: D-PlaneOS v5.8.0
-- Date: 2026-02-09

BEGIN TRANSACTION;

-- ============================================================================
-- ROLES TABLE
-- ============================================================================
CREATE TABLE IF NOT EXISTS roles (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    name TEXT UNIQUE NOT NULL,
    display_name TEXT NOT NULL,
    description TEXT,
    is_system BOOLEAN DEFAULT 0,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX idx_roles_name ON roles(name);
CREATE INDEX idx_roles_system ON roles(is_system);

-- ============================================================================
-- PERMISSIONS TABLE
-- ============================================================================
CREATE TABLE IF NOT EXISTS permissions (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    resource TEXT NOT NULL,
    action TEXT NOT NULL,
    display_name TEXT NOT NULL,
    description TEXT,
    category TEXT DEFAULT 'general',
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(resource, action)
);

CREATE INDEX idx_permissions_resource ON permissions(resource);
CREATE INDEX idx_permissions_category ON permissions(category);

-- ============================================================================
-- ROLE_PERMISSIONS JUNCTION TABLE
-- ============================================================================
CREATE TABLE IF NOT EXISTS role_permissions (
    role_id INTEGER NOT NULL,
    permission_id INTEGER NOT NULL,
    granted_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY(role_id) REFERENCES roles(id) ON DELETE CASCADE,
    FOREIGN KEY(permission_id) REFERENCES permissions(id) ON DELETE CASCADE,
    PRIMARY KEY(role_id, permission_id)
);

CREATE INDEX idx_role_permissions_role ON role_permissions(role_id);
CREATE INDEX idx_role_permissions_permission ON role_permissions(permission_id);

-- ============================================================================
-- USER_ROLES JUNCTION TABLE
-- ============================================================================
CREATE TABLE IF NOT EXISTS user_roles (
    user_id INTEGER NOT NULL,
    role_id INTEGER NOT NULL,
    granted_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    granted_by INTEGER,
    expires_at DATETIME,
    FOREIGN KEY(user_id) REFERENCES users(id) ON DELETE CASCADE,
    FOREIGN KEY(role_id) REFERENCES roles(id) ON DELETE CASCADE,
    FOREIGN KEY(granted_by) REFERENCES users(id) ON DELETE SET NULL,
    PRIMARY KEY(user_id, role_id)
);

CREATE INDEX idx_user_roles_user ON user_roles(user_id);
CREATE INDEX idx_user_roles_role ON user_roles(role_id);
CREATE INDEX idx_user_roles_expiry ON user_roles(expires_at);

-- ============================================================================
-- AUDIT LOG FOR RBAC CHANGES
-- ============================================================================
CREATE TABLE IF NOT EXISTS rbac_audit_log (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id INTEGER,
    action TEXT NOT NULL,
    resource_type TEXT NOT NULL,
    resource_id INTEGER,
    old_value TEXT,
    new_value TEXT,
    ip_address TEXT,
    user_agent TEXT,
    timestamp DATETIME DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY(user_id) REFERENCES users(id) ON DELETE SET NULL
);

CREATE INDEX idx_rbac_audit_user ON rbac_audit_log(user_id);
CREATE INDEX idx_rbac_audit_timestamp ON rbac_audit_log(timestamp);
CREATE INDEX idx_rbac_audit_action ON rbac_audit_log(action);

-- ============================================================================
-- SEED DEFAULT ROLES
-- ============================================================================
INSERT INTO roles (name, display_name, description, is_system) VALUES
('admin', 'Administrator', 'Full system access with all permissions', 1),
('operator', 'Operator', 'Can manage storage, containers, and files but not users', 1),
('viewer', 'Viewer', 'Read-only access to all resources', 1),
('user', 'Standard User', 'Basic file access and personal storage', 1);

-- ============================================================================
-- SEED DEFAULT PERMISSIONS
-- ============================================================================

-- Storage Permissions
INSERT INTO permissions (resource, action, display_name, description, category) VALUES
('storage', 'read', 'View Storage', 'View storage pools, datasets, and disk information', 'storage'),
('storage', 'write', 'Manage Storage', 'Create and modify storage pools and datasets', 'storage'),
('storage', 'delete', 'Delete Storage', 'Delete pools, datasets, and snapshots', 'storage'),
('storage', 'scrub', 'Scrub Pools', 'Start and stop pool scrubbing operations', 'storage'),
('storage', 'import', 'Import Pools', 'Import existing ZFS pools', 'storage'),
('storage', 'export', 'Export Pools', 'Export ZFS pools', 'storage');

-- Docker Permissions
INSERT INTO permissions (resource, action, display_name, description, category) VALUES
('docker', 'read', 'View Containers', 'View Docker containers and images', 'docker'),
('docker', 'write', 'Manage Containers', 'Start, stop, and restart containers', 'docker'),
('docker', 'delete', 'Delete Containers', 'Remove containers and images', 'docker'),
('docker', 'logs', 'View Container Logs', 'Access container logs', 'docker'),
('docker', 'exec', 'Execute Commands', 'Execute commands inside containers', 'docker');

-- File Permissions
INSERT INTO permissions (resource, action, display_name, description, category) VALUES
('files', 'read', 'Browse Files', 'Browse and download files', 'files'),
('files', 'write', 'Upload Files', 'Upload and modify files', 'files'),
('files', 'delete', 'Delete Files', 'Delete files and directories', 'files'),
('files', 'share', 'Share Files', 'Create and manage file shares', 'files');

-- System Permissions
INSERT INTO permissions (resource, action, display_name, description, category) VALUES
('system', 'read', 'View System', 'View system status, metrics, and settings', 'system'),
('system', 'write', 'Manage System', 'Modify system settings and configuration', 'system'),
('system', 'reboot', 'Reboot System', 'Reboot or shutdown the system', 'system'),
('system', 'update', 'Update System', 'Install system updates', 'system');

-- User Management Permissions
INSERT INTO permissions (resource, action, display_name, description, category) VALUES
('users', 'read', 'View Users', 'View user accounts and profiles', 'users'),
('users', 'write', 'Manage Users', 'Create and modify user accounts', 'users'),
('users', 'delete', 'Delete Users', 'Delete user accounts', 'users'),
('users', 'reset_password', 'Reset Passwords', 'Reset user passwords', 'users');

-- Role Management Permissions
INSERT INTO permissions (resource, action, display_name, description, category) VALUES
('roles', 'read', 'View Roles', 'View roles and permissions', 'roles'),
('roles', 'write', 'Manage Roles', 'Create and modify roles', 'roles'),
('roles', 'delete', 'Delete Roles', 'Delete custom roles', 'roles'),
('roles', 'assign', 'Assign Roles', 'Assign roles to users', 'roles');

-- Network Permissions
INSERT INTO permissions (resource, action, display_name, description, category) VALUES
('network', 'read', 'View Network', 'View network configuration', 'network'),
('network', 'write', 'Manage Network', 'Configure network settings', 'network');

-- Monitoring Permissions
INSERT INTO permissions (resource, action, display_name, description, category) VALUES
('monitoring', 'read', 'View Monitoring', 'View system monitoring and alerts', 'monitoring'),
('monitoring', 'write', 'Manage Monitoring', 'Configure monitoring and alerts', 'monitoring');

-- Backup Permissions
INSERT INTO permissions (resource, action, display_name, description, category) VALUES
('backup', 'read', 'View Backups', 'View backup status and history', 'backup'),
('backup', 'write', 'Manage Backups', 'Create and manage backups', 'backup'),
('backup', 'restore', 'Restore Backups', 'Restore from backups', 'backup');

-- ============================================================================
-- ASSIGN PERMISSIONS TO ROLES
-- ============================================================================

-- Admin: ALL permissions
INSERT INTO role_permissions (role_id, permission_id)
SELECT 
    (SELECT id FROM roles WHERE name = 'admin'),
    id
FROM permissions;

-- Operator: All except user/role management
INSERT INTO role_permissions (role_id, permission_id)
SELECT 
    (SELECT id FROM roles WHERE name = 'operator'),
    id
FROM permissions
WHERE category NOT IN ('users', 'roles');

-- Viewer: Only read permissions
INSERT INTO role_permissions (role_id, permission_id)
SELECT 
    (SELECT id FROM roles WHERE name = 'viewer'),
    id
FROM permissions
WHERE action = 'read';

-- User: Basic file and storage read
INSERT INTO role_permissions (role_id, permission_id)
SELECT 
    (SELECT id FROM roles WHERE name = 'user'),
    id
FROM permissions
WHERE (resource = 'files' AND action IN ('read', 'write'))
   OR (resource = 'storage' AND action = 'read')
   OR (resource = 'system' AND action = 'read');

-- ============================================================================
-- MIGRATE EXISTING USERS TO ADMIN ROLE
-- ============================================================================
-- All existing users become admins to maintain functionality
INSERT INTO user_roles (user_id, role_id, granted_by)
SELECT 
    u.id,
    (SELECT id FROM roles WHERE name = 'admin'),
    NULL
FROM users u
WHERE NOT EXISTS (
    SELECT 1 FROM user_roles ur WHERE ur.user_id = u.id
);

-- ============================================================================
-- UPDATE TRIGGER FOR ROLES
-- ============================================================================
CREATE TRIGGER update_roles_timestamp 
AFTER UPDATE ON roles
BEGIN
    UPDATE roles SET updated_at = CURRENT_TIMESTAMP WHERE id = NEW.id;
END;

-- ============================================================================
-- AUDIT TRIGGER FOR ROLE ASSIGNMENTS
-- ============================================================================
CREATE TRIGGER audit_role_assignment 
AFTER INSERT ON user_roles
BEGIN
    INSERT INTO rbac_audit_log (user_id, action, resource_type, resource_id, new_value)
    VALUES (
        NEW.granted_by,
        'role_assigned',
        'user_role',
        NEW.user_id,
        (SELECT name FROM roles WHERE id = NEW.role_id)
    );
END;

CREATE TRIGGER audit_role_removal 
AFTER DELETE ON user_roles
BEGIN
    INSERT INTO rbac_audit_log (action, resource_type, resource_id, old_value)
    VALUES (
        'role_removed',
        'user_role',
        OLD.user_id,
        (SELECT name FROM roles WHERE id = OLD.role_id)
    );
END;

COMMIT;

-- ============================================================================
-- VERIFICATION QUERIES (For testing)
-- ============================================================================
-- SELECT 'Roles created:', COUNT(*) FROM roles;
-- SELECT 'Permissions created:', COUNT(*) FROM permissions;
-- SELECT 'Admin permissions:', COUNT(*) FROM role_permissions WHERE role_id = (SELECT id FROM roles WHERE name = 'admin');
-- SELECT 'Users migrated to admin:', COUNT(*) FROM user_roles WHERE role_id = (SELECT id FROM roles WHERE name = 'admin');
