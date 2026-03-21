# D-PlaneOS Administrator Guide

Complete reference for system administration, storage management, sharing protocols, and identity management.

---

## Table of Contents

1. [User Management](#user-management)
2. [Role Management](#role-management)
3. [Storage Management](#storage-management)
4. [File Management](#file-management)
5. [Container Management](#container-management)
6. [System Settings](#system-settings)
7. [Monitoring and Alerts](#monitoring-and-alerts)
8. [Backup and Recovery](#backup-and-recovery)
9. [Security Best Practices](#security-best-practices)
10. [Directory Service (LDAP / Active Directory)](#directory-service-ldap--active-directory)
11. [Custom Container Icons](#custom-container-icons)
12. [Troubleshooting](#troubleshooting)

---

## User Management

### Creating Users

**Via UI:**
1. Navigate to **Settings → Users**
2. Click **Create User**
3. Fill in username (lowercase, no spaces), email, and password (12+ characters)
4. Click **Create**
5. Assign a role (see Role Management)

### Assigning Roles

1. Settings → Users → click on the user
2. Roles section → **Assign Role**
3. Select a role: Admin, Operator, Viewer, or User
4. Optionally set an expiry date
5. Click **Assign**

Role changes take effect immediately (permission cache expires within ~5 minutes).

### Removing Users

Users cannot be deleted while they have active sessions. User ID 1 (the initial admin) cannot be deleted.

1. Settings → Users → click on the user
2. Click **Delete User** and confirm

On deletion: the account, role assignments, and active sessions are all removed. An audit log entry is created.

---

## Role Management

### Built-in Roles

**Admin** - All 34 permissions. Can manage users, roles, and system settings. Cannot be deleted (system role).

**Operator** - Storage, Docker, and file management. Cannot create users or assign roles.

**Viewer** - Read-only access to all data. Can download files and view logs. Cannot modify anything.

**User** - Upload and download files, view own storage usage, read system status.

### Creating Custom Roles

1. Settings → Roles and Permissions → **Create Role**
2. Enter a name, display name, and description
3. Save, then click on the new role
4. Select the permissions it should have
5. Click **Save Permissions**

### Permission Reference

**Storage:** `storage:read` `storage:write` `storage:delete` `storage:scrub` `storage:import` `storage:export`

**Docker:** `docker:read` `docker:write` `docker:delete` `docker:logs` `docker:exec`

**Files:** `files:read` `files:write` `files:delete` `files:share`

**System:** `system:read` `system:write` `system:reboot` `system:update`

**Users:** `users:read` `users:write` `users:delete` `users:reset_password`

**Roles:** `roles:read` `roles:write` `roles:delete` `roles:assign`

---

## Storage Management

### Creating Pools

| Disks | Configuration | Usable Space | Failure Tolerance |
|-------|---------------|--------------|-------------------|
| 2 | Mirror | 50% | 1 disk |
| 3 | RAID-Z1 | 67% | 1 disk |
| 4 | RAID-Z2 | 50% | 2 disks |
| 6 | RAID-Z2 | 67% | 2 disks |
| 6 | RAID-Z3 | 50% | 3 disks |

**Steps:** Storage → Pools → **Create Pool** → select disks → choose RAID level → enter pool name → Create.

### Managing Datasets

**Create:** Storage → Datasets → **Create Dataset**. Recommended settings: compression `lz4`, set quotas per user.

**Best practices:**
- Use hierarchical names: `pool/data/users/alice`
- Enable compression (saves ~30% on typical data)
- Set quotas for user datasets
- Take regular snapshots (see Backup section)

### Scrubbing Pools

Scrubs verify on-disk data integrity and auto-repair from parity. Recommended monthly.

**Via UI:** Storage → Pools → select pool → **Start Scrub**.

### Pool Maintenance (Clear/Online)

For troubleshooting pool errors or managing hot-swap replacements:

- **Clear Errors**: Storage → Pools → select pool → **Clear**. Resets the pool's error counters (useful after a known cable issue or transient fault).
- **Online Device**: If a device was previously detached or disconnected, use the **Online** action in the disk management view to attempt to bring it back into the pool.

**Via cron:**
```bash
sudo crontab -e
# Add:
0 2 1 * * /usr/sbin/zpool scrub tank
```

## File Management

### File Explorer

D-PlaneOS includes a web-based file explorer accessible via the **Files** navigation item.

- **Navigation**: Browse datasets and directories in real-time.
- **Uploads**: Supports large, chunked multi-gigabyte uploads directly to the server.
- **Operations**: Rename, Copy, Move, and Delete files/directories.

### ACL Management (POSIX ACLs)

For granular access control beyond standard owner/group permissions, the File Explorer supports POSIX ACLs.

1. Navigate to **Files**.
2. Right-click any file or directory.
3. Select **Manage Permissions (ACL)**.
4. From this dialog, you can:
   - View current ACL entries.
   - Add new entries for specific users or groups.
   - Set permissions (Read, Write, Execute).
   - Apply changes recursively to directory contents.

> [!IMPORTANT]
> To use ACLs, the underlying ZFS dataset must have `acltype=posixacl` set. The installer enables this by default for new pools created through the UI.

---

---

## Container Management

Requires permission: `docker:write`.

### Deploying Containers

Containers → **Deploy Container** → fill in image, name, ports, volumes, and environment variables → **Deploy**.

### Container Icons

Each container displays an icon resolved in this order:

1. **`dplaneos.icon` label** on the container (set in `docker-compose.yaml`):
   - A Material Symbol name (e.g. `database`) - renders as a vector icon
   - A filename ending in `.svg`, `.png`, or `.webp` - served from `/var/lib/dplaneos/custom_icons/`
   - A full URL starting with `http` or `/` - loaded as an image
2. **Built-in image-name mapping** - covers 80+ well-known images (Jellyfin, Plex, Nextcloud, Grafana, etc.)
3. **Fallback** - generic `deployed_code` icon

**Adding a custom icon:**
1. Copy your image file to `/var/lib/dplaneos/custom_icons/myapp.svg`
2. In your `docker-compose.yaml` or container config, add:
   ```yaml
   labels:
     dplaneos.icon: myapp.svg
   ```

Custom icons are served via `GET /api/assets/custom-icons/<filename>`. The full icon list is available at `GET /api/assets/custom-icons/list`.

### Managing Containers

- **Start/Stop:** click on container → Start or Stop
- **Logs:** click on container → Logs tab
- **Exec:** click on container → Exec tab → enter `bash` or any command
- **Remove:** click on container → Delete → confirm

---

## System Settings

### Network Configuration

Settings → System → Network → configure interface, IP address, gateway, and DNS → **Apply**.

From this page, you can also quickly access the **Firewall** management UI to configure port rules and security settings.

### Notifications

Settings → System → Notifications - configure SMTP for email alerts. The ZED hook also supports direct Telegram alerts for critical ZFS events (pool degraded, disk faulted). See `install/zed/dplaneos-notify.sh` for configuration details (reads `telegram_config` from the database).

---

## Monitoring and Alerts

### Dashboard Metrics

CPU, RAM, disk I/O, network traffic, pool health, and container status are displayed on the dashboard in real time via WebSocket.

### Alert Thresholds

Monitoring → Settings - configure thresholds for CPU, RAM, disk capacity, and I/O wait. Alerts route to email, webhook, Telegram, and the dashboard.

### Viewing Logs

- System logs: Monitoring → Logs → System
- Audit log: Settings → Roles and Permissions → Audit Log (HMAC-chained, tamper-evident)
- Container logs: Containers → select container → Logs

---

## Backup and Recovery

### ZFS Snapshots

```bash
# Manual snapshot
zfs snapshot tank/data@backup-$(date +%Y%m%d)

# Automatic snapshots (via zfs-auto-snapshot)
sudo apt install zfs-auto-snapshot
```

### Database Backup

The daemon takes an automatic `VACUUM INTO` backup on startup and every 24 hours:

```bash
# Default backup location
/var/lib/dplaneos/dplaneos.db.backup

# Manual backup
sudo sqlite3 /var/lib/dplaneos/dplaneos.db \
  ".backup /backup/dplaneos-$(date +%Y%m%d).db"

# Restore
sudo systemctl stop dplaned
sudo cp /backup/dplaneos-20260309.db /var/lib/dplaneos/dplaneos.db
sudo systemctl start dplaned
```

### Pool Export/Import

```bash
# Export (for moving to another system)
sudo zpool export tank

# Import
sudo zpool import        # list importable pools
sudo zpool import tank   # import by name
```

---

## Security Best Practices

**Hardened Execution Whitelist (v6.0.6):** The daemon uses a strict, "sentence-based" allowlist for all system commands (`zfs`, `zpool`, `ufw`, etc.). This means only predefined, safe command structures are allowed. Modification of critical ZFS properties (like `mountpoint`, `quota`, `atime`) and firewall rules is restricted to validated patterns to prevent accidental or malicious system disruption.

**Path Normalization:** D-PlaneOS is now fully path-agnostic. It no longer relies on hardcoded absolute paths (`/usr/bin/`, `/bin/`) for key binaries, instead using the system's `PATH` for resolution. This ensures full compatibility with NixOS, Debian, and other specialized Linux distributions.

**Allowed Base Paths:** File operations (create, delete, rename, chown, chmod) are restricted to a defined set of "safe" base paths:
- `/mnt/*` (Main storage pools)
- `/home/*` (User directories)
- `/tank/*`, `/data/*`, `/media/*`, `/opt/*`, `/srv/*` (Common storage mountpoints)
- `/tmp/*` (Temporary files)
- `/var/lib/dplaneos/` (Application data)

**Passwords:** Minimum 12 characters, mixed case, numbers, and symbols. The installer generates a random password on first install and requires you to change it on first login.

**HTTPS:** Set up a TLS certificate via certbot or your reverse proxy. The nginx config ships with appropriate security headers.

**Firewall:**
```bash
sudo ufw allow 80/tcp
sudo ufw allow 443/tcp
sudo ufw allow 22/tcp
sudo ufw enable
```

**Fail2ban:** Install `fail2ban` and configure it to monitor `/var/log/dplaneos/access.log` (maxretry 5, bantime 3600).

**Audit logs:** Review monthly via Settings → Audit Log. Look for failed logins, unexpected role changes, and out-of-hours access.

**Least privilege:** Use Operator or Viewer roles for daily-use accounts; reserve Admin for administrative tasks only. Review role assignments quarterly.

**Database path for direct queries:**
```bash
sudo sqlite3 /var/lib/dplaneos/dplaneos.db "SELECT r.name FROM roles r \
  JOIN user_roles ur ON r.id = ur.role_id WHERE ur.user_id = X;"
```

---

## Directory Service (LDAP / Active Directory)

### Quick Setup

1. Navigate to **Identity → Directory Service**
2. Select a preset: **Active Directory**, **OpenLDAP**, **FreeIPA**, or **Custom**
3. Enter server address, Bind DN, Bind Password, and Base DN
4. Click **Test Connection**
5. Click **Save Configuration** and enable the toggle

### Group to Role Mapping

| AD Group | D-PlaneOS Role | Access Level |
|----------|----------------|--------------|
| `IT_Admins` | Administrator | Full system access |
| `Storage_Team` | Operator | Storage, Docker, Shares |
| `Domain Users` | User | Files, Dashboard |
| `Auditors` | Viewer | Read-only |

To add a mapping: click **Add Mapping**, enter the LDAP group name, select the role, click **Add Mapping**.

### Authentication Model

D-PlaneOS uses **directory-sourced user provisioning with local authentication**. This is intentionally different from live LDAP auth:

- Users are **synced from the directory** into the local database (`POST /api/ldap/sync`), with group-to-role mappings applied at sync time.
- At login, synced users authenticate via **LDAP bind** - the daemon connects to the directory server and verifies credentials in real time.
- Local accounts (including the system administrator) always authenticate via bcrypt regardless of LDAP state.
- If the LDAP server is unreachable, **all local accounts continue to work** - the UI is never fully locked out.

This model gives you directory-controlled access without making the management UI dependent on directory availability.

> **Note:** Unlike TrueNAS Scale and Unraid, which only use LDAP/AD for SMB share authentication, D-PlaneOS uses LDAP credentials to authenticate web UI logins for directory-sourced accounts.

### Sync vs Live Auth

| Account type | How it authenticates |
|---|---|
| `source=local` | bcrypt against local `password_hash` |
| `source=ldap` | Real-time LDAP bind against the configured server |
| User ID 1 (admin) | Always local bcrypt, even if `source=ldap` |

### Security Notes

- The system administrator (user ID 1) always uses local authentication, even when LDAP is enabled, preventing lockout if the directory server goes down.
- TLS is enforced by default (TLS 1.2+).
- The bind password is stored in SQLite - use a read-only service account.
- If the LDAP server is unreachable, local accounts continue to work. Directory-sourced accounts will fail login until the server is reachable again.

### LDAP API

```
GET    /api/ldap/config
POST   /api/ldap/config
POST   /api/ldap/test
GET    /api/ldap/status
POST   /api/ldap/sync
POST   /api/ldap/search-user
GET    /api/ldap/mappings
POST   /api/ldap/mappings
DELETE /api/ldap/mappings?id=N
GET    /api/ldap/sync-log
```

### Troubleshooting LDAP

| Issue | Solution |
|-------|----------|
| Connection failed | Verify server address, port, and firewall rules |
| Bind failed | Verify Bind DN and password; check the service account is not locked |
| User not found | Check User Filter: AD uses `sAMAccountName`, OpenLDAP uses `uid` |
| No groups mapped | Verify Group Base DN and Group Filter |
| Admin locked out | User ID 1 always uses local auth - log in with local credentials |

---

## Custom Container Icons

The daemon serves custom icons from `/var/lib/dplaneos/custom_icons/`.

Supported formats: `.svg`, `.png`, `.webp`

**API endpoints:**
- `GET /api/assets/custom-icons/<filename>` - serve a single icon file
- `GET /api/assets/custom-icons/list` - JSON list of available filenames
- `GET /api/docker/icon-map` - built-in image-name to Material Symbol mapping (80+ entries)

**Usage:** Drop an image file into `/var/lib/dplaneos/custom_icons/`, then reference it via the `dplaneos.icon` container label. No daemon restart is required.

---

## Troubleshooting

See [TROUBLESHOOTING.md](TROUBLESHOOTING.md) for comprehensive troubleshooting steps.

### Common Issues

**Permission Denied (403):**
```bash
# Check user's roles
sqlite3 /var/lib/dplaneos/dplaneos.db \
  "SELECT r.name FROM roles r JOIN user_roles ur ON r.id = ur.role_id WHERE ur.user_id = X;"
sudo systemctl restart dplaned  # clears permission cache
```

**Pool Will Not Mount:**
```bash
sudo zpool status
sudo zpool import -f tank
sudo zpool status -v  # check for errors
```

**High Memory Usage:**
```bash
arc_summary  # ZFS ARC breakdown
# To limit ARC (in /etc/modprobe.d/zfs.conf):
# options zfs zfs_arc_max=17179869184  (16 GB in bytes)
```

**Slow Web Interface:**
```bash
htop      # CPU
iotop     # disk I/O
sudo systemctl restart dplaned
```
