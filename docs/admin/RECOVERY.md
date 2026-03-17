# D-PlaneOS Recovery Guide

## Quick Reference

| Problem | Command |
|---------|---------|
| Service will not start | `journalctl -u dplaned -n 50` |
| Database locked | `systemctl restart dplaned` |
| Database corrupted | Restore from backup (see below) |
| Lost admin access | `sudo dplaneos-recovery` (option 5) |
| Pool not importing | `zpool import -f <pool>` |
| Web UI unreachable | `curl http://127.0.0.1:9000/health` |
| High memory usage | Check `MemoryMax` in systemd unit, restart service |

---

## 1. Service Management

### Check Status

```bash
systemctl status dplaned
```

### View Logs

```bash
# Last 50 lines
journalctl -u dplaned -n 50

# Follow live
journalctl -u dplaned -f

# Since last boot
journalctl -u dplaned -b
```

### Restart

```bash
sudo systemctl restart dplaned
```

Graceful shutdown: the daemon drains active connections before stopping (15 s timeout).

### Verify Health

```bash
curl http://127.0.0.1:9000/health
# Expected: {"status":"ok","version":"4.x.x"}
```

---

## 2. Database Recovery

The SQLite database lives at `/var/lib/dplaneos/dplaneos.db`.

### Automatic Backups

`dplaned` creates a backup on every startup and once daily via `VACUUM INTO`:

- Default path: `/var/lib/dplaneos/dplaneos.db.backup`
- Custom path: set via `-backup-path /path/to/backup.db` in the systemd unit

### Restore from Backup

```bash
sudo systemctl stop dplaned

# Replace corrupted database with backup
sudo cp /var/lib/dplaneos/dplaneos.db.backup /var/lib/dplaneos/dplaneos.db

# Remove WAL files - they belong to the old DB
sudo rm -f /var/lib/dplaneos/dplaneos.db-wal
sudo rm -f /var/lib/dplaneos/dplaneos.db-shm

# Start service (schema auto-init fills any missing tables)
sudo systemctl start dplaned
```

### Database Integrity Check

```bash
sudo sqlite3 /var/lib/dplaneos/dplaneos.db "PRAGMA integrity_check;"
# Expected output: ok
```

### Complete Database Reset

If both primary and backup are corrupted, delete and let the daemon recreate from scratch. All tables and default data (roles, permissions, admin user) are seeded automatically on startup:

```bash
sudo systemctl stop dplaned
sudo rm -f /var/lib/dplaneos/dplaneos.db*
sudo systemctl start dplaned
# Check logs: should show "Seeded 4 built-in RBAC roles"
```

**Note:** This resets all sessions, user accounts, LDAP configuration, and notification settings. ZFS pools, shares, and Docker containers are not affected - they live outside the database.

---

## 3. Authentication Recovery

### Interactive Recovery CLI

The fastest option:

```bash
sudo dplaneos-recovery
# Select option 5: Reset Admin Password
```

### Locked Out - Clear All Sessions

Sessions are stored in SQLite. If all sessions are expired or the DB is corrupted:

```bash
sudo systemctl stop dplaned
sudo sqlite3 /var/lib/dplaneos/dplaneos.db "DELETE FROM sessions;"
sudo systemctl start dplaned
# All users must log in again
```

### Reset Admin User Directly

```bash
# Generate a bcrypt hash
NEW_HASH=$(python3 -c "import bcrypt; print(bcrypt.hashpw(b'your-password', bcrypt.gensalt(12)).decode())")

sudo sqlite3 /var/lib/dplaneos/dplaneos.db "
  INSERT OR REPLACE INTO users (id, username, password_hash, display_name, email, role, active, source)
  VALUES (1, 'admin', '${NEW_HASH}', 'Administrator', 'admin@localhost', 'admin', 1, 'local');
"
```

---

## 4. ZFS Recovery

### Pool Will Not Import

```bash
# List available pools
sudo zpool import

# Force import (e.g. after hardware move)
sudo zpool import -f <poolname>

# Import with different mountpoint
sudo zpool import -f -R /mnt <poolname>
```

### Pool Degraded

```bash
sudo zpool status <poolname>

# Replace failed disk
sudo zpool replace <poolname> <old-device> <new-device>

# Start scrub to verify data integrity
sudo zpool scrub <poolname>
```

### Automatic Pool Import on Hot-Swap

When a disk is physically reconnected, the udev hot-swap rules (`99-dplaneos-hotswap.rules`) fire automatically. The daemon receives the event via `POST /api/internal/disk-event` and:

1. Enriches the device with stable identifiers (by-id path, WWN, size)
2. Cross-checks the disk registry for pool membership
3. Runs `zpool import -d /dev/disk/by-id <poolname>` automatically if the disk belongs to a known FAULTED, UNAVAIL, or importable pool
4. Broadcasts a `poolHealthChanged` WebSocket event to update the UI
5. If faulted vdevs exist, broadcasts `diskReplacementAvailable` and pre-populates the Replace modal

No manual `zpool import` is needed for pools the daemon has previously seen.

### Encrypted Dataset Locked

```bash
# List locked datasets
sudo zfs list -o name,keystatus -r <poolname>

# Unlock (prompts for passphrase)
sudo zfs load-key <poolname/dataset>

# Mount after unlocking
sudo zfs mount <poolname/dataset>
```

Or use the web UI: **Storage → Encryption → Unlock**.

---

## 5. Reverse Proxy Issues

`dplaned` listens on `127.0.0.1:9000` by default. If the web UI is unreachable, test the daemon directly first:

```bash
curl http://127.0.0.1:9000/health
```

If this returns `{"status":"ok"}` but the browser cannot reach the UI, the issue is in nginx.

### Check nginx

```bash
systemctl status nginx
sudo nginx -t
sudo journalctl -u nginx -n 20
```

### nginx Configuration Location

```
/etc/nginx/sites-available/dplaneos
/etc/nginx/sites-enabled/dplaneos  (symlink)
```

The installer regenerates this file on every install/upgrade. To restore a known-good config, re-run the installer with `--upgrade`.

---

## 6. Performance Issues

### High Memory

```bash
# Check daemon memory usage
systemctl status dplaned | grep Memory

# The systemd unit caps daemon memory at 512 MB (MemoryMax=512M)
# If hitting limits, check for:
#   - Large audit log table → purge old entries (see below)
#   - Many open WebSocket connections
#   - ZFS ARC pressure (kernel, not dplaned - check with arc_summary)
```

### Database Slow

```bash
# Force WAL checkpoint (reduces WAL file size)
sudo sqlite3 /var/lib/dplaneos/dplaneos.db "PRAGMA wal_checkpoint(TRUNCATE);"

# Check database and WAL sizes
ls -lh /var/lib/dplaneos/dplaneos.db*

# Manual VACUUM (rewrites and compacts the entire DB)
sudo systemctl stop dplaned
sudo sqlite3 /var/lib/dplaneos/dplaneos.db "VACUUM;"
sudo systemctl start dplaned
```

### Audit Log Growth

```bash
# Count audit entries
sudo sqlite3 /var/lib/dplaneos/dplaneos.db "SELECT COUNT(*) FROM audit_logs;"

# Purge entries older than 90 days
sudo sqlite3 /var/lib/dplaneos/dplaneos.db "
  DELETE FROM audit_logs WHERE timestamp < strftime('%s', 'now', '-90 days');
"
```

---

## 7. Reinstallation

### Upgrade (Preserves Data)

```bash
sudo bash install.sh --upgrade
```

The installer backs up the database, nginx config, and systemd unit before making any changes. On failure, it automatically restores from backup.

To roll back a failed upgrade manually:
```bash
ls /var/lib/dplaneos/backups/
sudo bash /var/lib/dplaneos/backups/pre-upgrade-<timestamp>/rollback.sh
```

### Clean Install (Preserves Data)

```bash
sudo systemctl stop dplaned
# Re-run installer from the new release directory
sudo bash install.sh
sudo systemctl start dplaned
```

ZFS pools, Docker containers, and network configuration are unaffected - they live in the kernel, not in `dplaned`.

### Complete Uninstall

```bash
sudo systemctl stop dplaned nginx
sudo systemctl disable dplaned nginx
sudo rm -f /etc/systemd/system/dplaned.service
sudo rm -f /etc/systemd/system/dplaneos-*.service
sudo rm -rf /opt/dplaneos
sudo rm -rf /var/lib/dplaneos
sudo rm -rf /var/log/dplaneos
sudo rm -f /etc/nginx/sites-available/dplaneos
sudo rm -f /etc/nginx/sites-enabled/dplaneos
sudo rm -f /etc/sudoers.d/dplaneos
sudo systemctl daemon-reload
```

---

## 8. Diagnostic Commands

```bash
# Service status
systemctl status dplaned

# Health check
curl -s http://127.0.0.1:9000/health | python3 -m json.tool

# Database tables
sudo sqlite3 /var/lib/dplaneos/dplaneos.db ".tables"

# Active sessions
sudo sqlite3 /var/lib/dplaneos/dplaneos.db \
  "SELECT username, datetime(created_at, 'unixepoch') FROM sessions WHERE status='active';"

# RBAC roles
sudo sqlite3 /var/lib/dplaneos/dplaneos.db \
  "SELECT name, display_name, is_system FROM roles;"

# Recent audit entries
sudo sqlite3 /var/lib/dplaneos/dplaneos.db \
  "SELECT datetime(timestamp,'unixepoch'), user, action, resource FROM audit_logs ORDER BY id DESC LIMIT 20;"

# Daemon version
/opt/dplaneos/daemon/dplaned -version 2>/dev/null || \
  curl -s http://127.0.0.1:9000/health | grep version
```

