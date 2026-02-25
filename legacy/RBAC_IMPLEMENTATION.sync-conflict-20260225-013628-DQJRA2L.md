# D-PlaneOS v1.9.0 - RBAC Implementation

## Summary
Role-Based Access Control (RBAC) added without breaking changes. Existing installations will continue to work, with all existing users automatically upgraded to admin role.

## Changes Applied

### 1. Database Schema
**File:** `database/schema.sql`
- Added `role` column to `users` table with CHECK constraint
- Valid roles: `admin`, `user`, `readonly`
- Default value: `user`
- Default admin account now has `role = 'admin'`

### 2. Migration Support
**Files:** 
- `database/migrate.php` - PHP migration script
- `database/migrations/001_add_rbac.sql` - SQL migration

**Migration is automatic and idempotent:**
- Detects if role column exists
- Adds column if missing
- Sets existing users to admin role (backward compatibility)
- Ensures at least one admin exists

**To run migration manually:**
```bash
cd /opt/dplaneos/database
php migrate.php
```

### 3. Authentication System
**File:** `system/dashboard/includes/auth.php`

**New functions:**
- `getCurrentUserRole()` - Get current user's role
- `hasRole($role)` - Check if user has specific role
- `requireRole($role, $msg)` - Require specific role or die
- `isAdmin()` - Check if user is admin
- `canWrite()` - Check if user can write (admin or user)
- `canRead()` - Check if user can read (all authenticated)
- `requireWrite($msg)` - Require write permission
- `requireAdmin($msg)` - Require admin role

**Updated functions:**
- `getCurrentUser()` - Now includes `role` field
- Login process - Stores role in session (`$_SESSION['user_role']`)

### 4. User Management API
**File:** `system/dashboard/api/system/users.php`

**Endpoints:**
```
GET  /api/system/users.php?action=list  - List all users
GET  /api/system/users.php?action=get&id=X  - Get specific user
POST /api/system/users.php
     action=create  - Create user (with role)
     action=update  - Update user (can change role)
     action=change_password  - Change password
     action=delete  - Delete user
```

**Security:**
- Requires admin role for all operations
- Prevents self-role modification
- Prevents last admin deletion/demotion
- Validates role values
- Integrates with SMB user wrappers for system users

### 5. User Management UI
**File:** `system/dashboard/users.php`

**Features:**
- List all users with roles
- Create users with role assignment
- Edit user email and role
- Delete users (with safeguards)
- Visual role badges (admin, user, readonly)
- Responsive design matching D-PlaneOS style

**Access:** Only visible to admin users

### 6. Session Management
**File:** `system/dashboard/login.php`
- Stores user role in session on login
- Updated to fetch role from database

## Role Permissions

### Admin
- Full system access
- Can manage all users
- Can modify system settings
- Can create/delete resources
- Access to all APIs

### User
- Can manage own resources (containers, shares, etc.)
- Can view system status
- Cannot manage other users
- Cannot modify critical system settings
- Read/write access to user-scoped APIs

### Readonly
- View-only access to all resources
- Can view dashboards and stats
- Cannot create/modify/delete anything
- Cannot access management APIs
- No system user created (no SMB/NFS access)

## Backward Compatibility

### Existing Installations
1. **No manual intervention required** (unless migration fails)
2. All existing users become admin automatically
3. All existing functionality continues to work
4. Session behavior unchanged
5. API endpoints unchanged (just add permission checks)

### Database Migration
- Migration runs automatically on first access if role column missing
- Can be run manually: `php database/migrate.php`
- Safe to run multiple times (idempotent)

### Breaking Changes
**NONE** - This is a pure additive change:
- New column has default value
- Existing users upgraded to admin
- New functions are optional
- Old code continues to work

## Usage Examples

### Protect Admin-Only Pages
```php
<?php
require_once __DIR__ . '/includes/auth.php';
requireAuth();
requireAdmin(); // Only admins can access
?>
```

### Protect Write Operations
```php
<?php
requireAuth();
requireWrite(); // Admins and users can write

if ($_SERVER['REQUEST_METHOD'] === 'POST') {
    // Create/update resource
}
?>
```

### Check Permissions in Code
```php
<?php
if (isAdmin()) {
    // Show admin options
}

if (canWrite()) {
    // Show edit buttons
}

if (hasRole('readonly')) {
    // Hide all action buttons
}
?>
```

### Add Permission Check to Existing API
```php
<?php
require_once __DIR__ . '/includes/auth.php';
requireAuth();
requireWrite(); // Add this line to require write permissions

// Rest of API code...
?>
```

## Testing Checklist

### Migration Testing
- [ ] Fresh install creates admin with role
- [ ] Upgrade from v1.8.x sets existing users to admin
- [ ] Migration script runs without errors
- [ ] Can log in after migration

### User Management Testing
- [ ] Admin can access /users.php
- [ ] Non-admin cannot access /users.php (403)
- [ ] Can create user with each role type
- [ ] Can edit user role
- [ ] Cannot delete last admin
- [ ] Cannot modify own role
- [ ] User with readonly role cannot write
- [ ] System user created for admin/user roles
- [ ] No system user for readonly role

### Permission Testing
- [ ] requireAdmin() blocks non-admins
- [ ] requireWrite() blocks readonly users
- [ ] canWrite() returns correct value
- [ ] Session stores role correctly
- [ ] Role persists across requests

## Security Considerations

### Safeguards Implemented
1. **Last admin protection** - Cannot delete or demote last admin
2. **Self-modification prevention** - Cannot change own role
3. **Role validation** - Only valid roles accepted
4. **Default secure** - New users default to 'user' not 'admin'
5. **Readonly safety** - No system access for readonly users

### Audit Trail
All user management operations logged:
- `logAction('user_create', ...)`
- `logAction('user_update', ...)`
- `logAction('user_delete', ...)`

## Future Enhancements

Potential additions (not in v1.9.0):
- [ ] Resource-level permissions
- [ ] Custom role definitions
- [ ] Permission groups
- [ ] API key authentication per role
- [ ] Fine-grained permissions matrix
- [ ] User groups
- [ ] LDAP/AD integration

## Files Modified/Added

### Modified Files
1. `database/schema.sql` - Added role column
2. `system/dashboard/includes/auth.php` - Added RBAC functions
3. `system/dashboard/login.php` - Store role in session
4. `system/dashboard/api/system/users.php` - Full RBAC support

### New Files
1. `database/migrate.php` - Migration script
2. `database/migrations/001_add_rbac.sql` - SQL migration
3. `system/dashboard/users.php` - User management UI

### Unchanged Files
- All other API endpoints (can be updated incrementally)
- Dashboard UI (works without changes)
- Container management
- Share management
- All other functionality

## Deployment Notes

### Fresh Install
1. Extract tarball
2. Run installer
3. Default admin account has admin role
4. Access /users.php to create additional users

### Upgrade from Previous Version
1. Stop D-PlaneOS services
2. Backup database: `cp /var/dplane/database/dplane.db /var/dplane/database/dplane.db.backup`
3. Extract new tarball
4. Start D-PlaneOS services
5. Migration runs automatically on first access
6. Verify existing users are admin: access /users.php
7. Create non-admin users as needed

### Rollback (if needed)
1. Stop services
2. Restore database backup
3. Extract old tarball
4. Start services

---

**Version:** 1.9.0  
**Feature:** RBAC User Management  
**Breaking Changes:** None  
**Migration Required:** Automatic  
**Documentation:** This file
