# D-PlaneOS v3.3.1 Administrator Guide

**Complete guide for system administration and user management**

> Updated for v3.3.0: RBAC, LDAP/AD, ZFS encryption, injection-hardened, OOM-protected

---

## Table of Contents

1. [User Management](#user-management)
2. [Role Management](#role-management)
3. [Storage Management](#storage-management)
4. [Container Management](#container-management)
5. [System Settings](#system-settings)
6. [Monitoring & Alerts](#monitoring--alerts)
7. [Backup & Recovery](#backup--recovery)
8. [Security Best Practices](#security-best-practices)
9. [Troubleshooting](#troubleshooting)

---

## User Management

### Creating Users

**Via UI:**
1. Navigate to: **Settings → Users**
2. Click **"Create User"**
3. Fill in details:
   - Username (lowercase, no spaces)
   - Email address
   - Password (12+ characters, mix of upper/lower/numbers/symbols)
4. Click **"Create"**
5. Assign role (see Role Management)

**Via CLI:**
```bash
# Not recommended - use UI for proper RBAC integration
```

### Assigning Roles

**Via UI:**
1. Settings → Users
2. Click on user
3. "Roles" section → **"Assign Role"**
4. Select role:
   - **Admin** - Full access
   - **Operator** - Daily operations
   - **Viewer** - Read-only
   - **User** - Basic file access
5. Optional: Set expiry date
6. Click **"Assign"**

**Role takes effect immediately (after permission cache expires ~5min)**

### Removing Users

**Important:** Cannot delete user ID 1 (god mode protection)

1. Settings → Users
2. Click on user
3. Click **"Delete User"**
4. Confirm deletion

**What happens:**
- User account deleted
- Role assignments removed
- Sessions invalidated
- Audit log entry created

---

## Role Management

### Understanding Roles

**Admin (Default):**
- All 30+ permissions
- Can manage users and roles
- Can reboot/shutdown system
- Cannot be deleted (system role)

**Operator:**
- Storage, Docker, Files management
- Cannot create users
- Cannot assign roles
- Cannot update system

**Viewer:**
- Read-only access to everything
- Can download files
- Can view logs
- Cannot modify anything

**User:**
- Upload/download files
- View own storage usage
- Read system status
- Limited access

### Creating Custom Roles

**Example: Developer Role**

1. Settings → Roles & Permissions
2. Click **"Create Role"**
3. Fill in:
   - Name: `developer`
   - Display Name: `Developer`
   - Description: `Software development team with Docker access`
4. Click **"Save"**
5. Click on new role
6. Select permissions:
   - ✅ docker:read, docker:write, docker:logs, docker:exec
   - ✅ files:read, files:write
   - ✅ system:read
7. Click **"Save Permissions"**

**Use case:** Developers can manage containers and files, but not system settings or users.

### Modifying Roles

**System Roles (admin, operator, viewer, user):**
- Cannot be modified or deleted
- Protected by database constraints

**Custom Roles:**
- Can be edited at any time
- Can add/remove permissions
- Can delete role (removes from all users)

### Permission Reference

**Storage:**
- `storage:read` - View pools, datasets
- `storage:write` - Create/modify pools
- `storage:delete` - Delete pools/datasets
- `storage:scrub` - Run scrub operations
- `storage:import` - Import existing pools
- `storage:export` - Export pools

**Docker:**
- `docker:read` - View containers/images
- `docker:write` - Start/stop containers
- `docker:delete` - Remove containers
- `docker:logs` - View container logs
- `docker:exec` - Execute commands in containers

**Files:**
- `files:read` - Browse/download files
- `files:write` - Upload/modify files
- `files:delete` - Delete files
- `files:share` - Create file shares

**System:**
- `system:read` - View system status
- `system:write` - Modify settings
- `system:reboot` - Reboot/shutdown
- `system:update` - Install updates

**Users:**
- `users:read` - View users
- `users:write` - Create/modify users
- `users:delete` - Delete users
- `users:reset_password` - Reset passwords

**Roles:**
- `roles:read` - View roles/permissions
- `roles:write` - Create/modify roles
- `roles:delete` - Delete custom roles
- `roles:assign` - Assign roles to users

---

## Storage Management

### Creating Pools

**Recommended Pool Configurations:**

| Disks | Configuration | Usable Space | Failure Tolerance |
|-------|---------------|--------------|-------------------|
| 2 | Mirror | 50% | 1 disk |
| 3 | RAID-Z1 | 67% | 1 disk |
| 4 | RAID-Z2 | 50% | 2 disks |
| 6 | RAID-Z2 | 67% | 2 disks |
| 6 | RAID-Z3 | 50% | 3 disks |

**Steps:**
1. Storage → Pools → **"Create Pool"**
2. Select disks (click to toggle)
3. Choose RAID level
4. Enter pool name (lowercase, no spaces)
5. Click **"Create"**

**Time:** 1-2 minutes

### Managing Datasets

**Create Dataset:**
1. Storage → Datasets → **"Create Dataset"**
2. Parent pool: Select pool
3. Name: `data/documents`
4. Quota: Optional (e.g., `500G`)
5. Compression: `lz4` (recommended)
6. Click **"Create"**

**Dataset best practices:**
- Use hierarchical names: `pool/data/users/john`
- Enable compression (saves ~30% space)
- Set quotas for user datasets
- Regular snapshots (see Backup section)

### Scrubbing Pools

**What is scrubbing?**
- Verifies data integrity
- Fixes silent corruption
- Recommended: Monthly

**How to scrub:**
1. Storage → Pools → Select pool
2. Click **"Start Scrub"**
3. Monitor progress (can take hours for large pools)

**Automatic scrubbing:**
```bash
# Add to crontab
sudo crontab -e
# Add line:
0 2 1 * * /usr/sbin/zpool scrub tank
```

---

## Container Management

**Requires permission:** `docker:write`

### Deploying Containers

**Example: Nextcloud**

1. Containers → **"Deploy Container"**
2. Image: `nextcloud:latest`
3. Name: `nextcloud`
4. Ports: `8080:80`
5. Volumes: `/mnt/tank/data/nextcloud:/var/www/html`
6. Environment:
   - `MYSQL_HOST=db`
   - `MYSQL_DATABASE=nextcloud`
7. Click **"Deploy"**

### Managing Containers

**Start/Stop:**
- Click on container → **"Start"** / **"Stop"**

**View Logs:**
- Click on container → **"Logs"** tab

**Execute Commands:**
- Click on container → **"Exec"** tab
- Enter command: `bash`
- Click **"Execute"**

**Remove Container:**
- Click on container → **"Delete"**
- Confirm deletion

---

## System Settings

### Network Configuration

**Static IP:**
1. Settings → System → Network
2. Interface: `eth0`
3. IP Address: `192.168.1.100/24`
4. Gateway: `192.168.1.1`
5. DNS: `8.8.8.8, 8.8.4.4`
6. Click **"Apply"**

### Time & Date

**Set Timezone:**
1. Settings → System → Time & Date
2. Timezone: `Europe/Berlin`
3. NTP Servers: `pool.ntp.org`
4. Click **"Save"**

### Notifications

**Email Alerts:**
1. Settings → System → Notifications
2. SMTP Server: `smtp.gmail.com`
3. Port: `587`
4. Username: `your-email@gmail.com`
5. Password: (app password)
6. Click **"Test"** → **"Save"**

---

## Monitoring & Alerts

### Dashboard Overview

**Metrics displayed:**
- CPU usage
- RAM usage
- Disk I/O
- Network traffic
- Pool health
- Container status

### Setting Alert Thresholds

1. Monitoring → Settings
2. Configure thresholds:
   - CPU: 80%
   - RAM: 90%
   - Disk: 85%
   - I/O Wait: 20%
3. Click **"Save"**

**Alerts trigger:**
- Email notification
- Dashboard warning
- Log entry

### Viewing Logs

**System Logs:**
- Monitoring → Logs → System

**Audit Logs:**
- Settings → Roles & Permissions → Audit Log

**Container Logs:**
- Containers → Select container → Logs

---

## Backup & Recovery

### ZFS Snapshots

**Manual Snapshot:**
```bash
zfs snapshot tank/data@backup-$(date +%Y%m%d)
```

**Automatic Snapshots:**
```bash
# Install zfs-auto-snapshot
sudo apt install zfs-auto-snapshot

# Snapshots created:
# - Every 15min (keep 4)
# - Hourly (keep 24)
# - Daily (keep 31)
# - Weekly (keep 8)
# - Monthly (keep 12)
```

### Database Backup

```bash
# Backup RBAC database
sudo sqlite3 /var/lib/dplaneos/dplaneos.db ".backup /backup/dplaneos-$(date +%Y%m%d).db"

# Restore
sudo systemctl stop dplaned
sudo cp /backup/dplaneos-20261210.db /var/lib/dplaneos/dplaneos.db
sudo systemctl start dplaned
```

### Pool Export/Import

**Export (for moving to another system):**
```bash
sudo zpool export tank
```

**Import:**
```bash
# Scan for importable pools
sudo zpool import

# Import specific pool
sudo zpool import tank
```

---

## Security Best Practices

### 1. Strong Passwords

- Minimum 12 characters
- Mix of upper/lower/numbers/symbols
- Change every 90 days
- Never share admin password

### 2. TLS/HTTPS Only

```bash
# Force HTTPS redirect in nginx
sudo nano /etc/nginx/sites-available/dplaneos
# Add:
server {
    listen 80;
    return 301 https://$host$request_uri;
}
```

### 3. Fail2Ban

- Already configured during install
- Bans IP after 5 failed logins
- 1 hour ban duration

### 4. Regular Updates

```bash
# Update D-PlaneOS
sudo /usr/bin/dplaneos-update

# Update system
sudo apt update && sudo apt upgrade -y
```

### 5. Audit Logs

- Review monthly: Settings → Audit Log
- Look for:
  - Failed login attempts
  - Unexpected role changes
  - After-hours access

### 6. Least Privilege

- Don't give everyone admin role
- Use custom roles for specific needs
- Review permissions quarterly

### 7. Firewall

```bash
# Only allow necessary ports
sudo ufw status
# Should show:
# 80/tcp (HTTP)
# 443/tcp (HTTPS)
# 22/tcp (SSH) - consider changing port
```

---

## Troubleshooting

### Permission Denied Errors

**Problem:** User gets "403 Forbidden" but should have access

**Solution:**
```bash
# Check user's roles
sqlite3 /var/lib/dplaneos/dplaneos.db "SELECT r.name FROM roles r JOIN user_roles ur ON r.id = ur.role_id WHERE ur.user_id = X;"

# Clear permission cache
sudo systemctl restart dplaned
```

### Pool Won't Mount

**Problem:** Pool offline after reboot

**Solution:**
```bash
# Check pool status
sudo zpool status

# Import if exported
sudo zpool import -f tank

# Check for errors
sudo zpool status -v
```

### High Memory Usage

**Problem:** System using all RAM

**Solution:**
```bash
# Check ZFS ARC
arc_summary

# Limit ARC (in /etc/modprobe.d/zfs.conf)
options zfs zfs_arc_max=17179869184  # 16GB in bytes

# Reload
sudo modprobe -r zfs
sudo modprobe zfs
```

### Slow Web Interface

**Problem:** Pages take >3 seconds to load

**Solutions:**
1. Check CPU usage: `htop`
2. Check disk I/O: `iotop`
3. Check network: `iftop`
4. Restart daemon: `sudo systemctl restart dplaned`
5. Clear browser cache

### Can't Delete User

**Problem:** "Cannot delete system user" error

**Reason:** User ID 1 has god mode protection

**Solution:** Cannot delete user ID 1. Create another admin and use that.

---

## Advanced Administration

### Bulk User Import

```bash
# Create users.csv
cat > users.csv << EOF
username,email,role
alice,alice@company.com,operator
bob,bob@company.com,user
charlie,charlie@company.com,viewer
EOF

# Import script (create this)
while IFS=, read -r username email role; do
    # Use API to create users
    curl -X POST http://localhost/api/v1/users \
        -H "X-Session-Token: $ADMIN_TOKEN" \
        -d "{\"username\":\"$username\",\"email\":\"$email\"}"
done < users.csv
```

### Performance Tuning

**ZFS:**
```bash
# Increase ARC size
echo "options zfs zfs_arc_max=17179869184" | sudo tee -a /etc/modprobe.d/zfs.conf

# Tune recordsize for workload
zfs set recordsize=1M tank/media  # Large files
zfs set recordsize=8K tank/database  # Small files
```

**System:**
```bash
# Already set by installer:
# vm.swappiness = 10
# vm.dirty_ratio = 40
# vm.vfs_cache_pressure = 50
```

---

**For RBAC details, see:** `RBAC-COMPLETE-README.md`  
**For API reference, see:** `API-REFERENCE.md`  
**For troubleshooting, see:** `TROUBLESHOOTING.md`

---

## Directory Service (LDAP / Active Directory)

### Overview

D-PlaneOS supports centralized authentication through LDAP and Active Directory. When enabled, users can log in with their existing corporate credentials, and group memberships automatically assign D-PlaneOS roles.

**Location:** Identity → Directory Service

### Quick Setup

1. Navigate to **Identity → Directory Service**
2. Select a preset:
   - **Active Directory** — Windows Server AD DS (port 636, TLS)
   - **OpenLDAP** — Standard OpenLDAP (port 389, TLS)
   - **FreeIPA** — Red Hat IdM / FreeIPA (port 636, TLS)
   - **Custom** — Manual configuration
3. Enter your server address, Bind DN, Bind Password, and Base DN
4. Click **Test Connection** to verify
5. Click **Save Configuration**
6. Enable the toggle

### Group → Role Mapping

Map your AD/LDAP groups to D-PlaneOS roles:

| AD Group Example | D-PlaneOS Role | Access Level |
|------------------|----------------|--------------|
| `IT_Admins` | Administrator | Full system access |
| `Storage_Team` | Power User | Storage, Docker, Shares |
| `Domain Users` | User | Files, Dashboard |
| `Auditors` | Read Only | View only |

**To add a mapping:**
1. Click **Add Mapping** in the Group → Role Mapping section
2. Enter the exact LDAP group name (case-insensitive match)
3. Select the D-PlaneOS role
4. Click **Add Mapping**

### Just-In-Time Provisioning

When enabled (default), users who authenticate via LDAP are automatically created as local accounts on first login. Their D-PlaneOS role is determined by:

1. Group mappings (if any match)
2. Default role (configurable, defaults to "User")

### Sync Behavior

- **Automatic:** Runs at the configured interval (default: every hour)
- **Manual:** Click "Sync Now" in Advanced Settings
- **Sync Log:** View history in the collapsible Sync History section

### Security Notes

- **God mode:** The system administrator (user #1) always uses local authentication, even when LDAP is enabled. This prevents lockout if the LDAP server goes down.
- **TLS:** Enforced by default (TLS 1.2+). Can be disabled for testing but not recommended.
- **Bind password:** Stored in the database. Consider using a read-only service account with minimal permissions.
- **Fallback:** If the LDAP server is unreachable, local authentication continues to work for all local accounts.

### Troubleshooting

| Issue | Solution |
|-------|----------|
| "Connection failed" | Verify server address, port, and network connectivity. Check firewall rules. |
| "Bind failed" | Verify Bind DN and password. Ensure the service account exists and is not locked. |
| "User not found" | Check the User Filter. For AD: `(&(objectClass=user)(sAMAccountName={username}))`. For OpenLDAP: `(&(objectClass=inetOrgPerson)(uid={username}))` |
| "No groups mapped" | Verify Group Base DN and Group Filter. Check that the user is actually a member of the LDAP groups. |
| "Admin locked out" | The admin user (#1) always uses local auth. Log in with local credentials and reconfigure LDAP. |

### API Reference

All LDAP endpoints require authentication (session token).

```
GET    /api/ldap/config       → Get configuration (password masked)
POST   /api/ldap/config       → Save configuration
POST   /api/ldap/test         → Test connection
GET    /api/ldap/status       → Connection & sync status
POST   /api/ldap/sync         → Trigger manual sync
POST   /api/ldap/search-user  → Search directory for user
GET    /api/ldap/mappings     → List group→role mappings
POST   /api/ldap/mappings     → Add mapping
DELETE /api/ldap/mappings?id=N → Remove mapping
GET    /api/ldap/sync-log     → Sync history (default: last 20)
```
