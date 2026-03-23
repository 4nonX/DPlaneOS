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

The PostgreSQL database state is managed by Patroni and stored at `/var/lib/dplaneos/pgsql/`.

### Automatic Snapshots

We recommend using ZFS snapshots for database recovery. The installer configures a separate dataset for the PostgreSQL state if a pool exists.

```bash
# View available snapshots
zfs list -t snapshot -r tank/dplaneos/pgsql

# Restore a snapshot (rollback)
sudo systemctl stop dplaned patroni postgresql
sudo zfs rollback tank/dplaneos/pgsql@backup-20260323
sudo systemctl start postgresql patroni dplaned
```

### Manual Backup (Logical)

```bash
# Backup the entire dplaneos database
sudo -u postgres pg_dump dplaneos > /backup/dplaneos-$(date +%Y%m%d).sql

# Restore from SQL dump
sudo systemctl stop dplaned
sudo -u postgres psql dplaneos < /backup/dplaneos-20260323.sql
sudo systemctl start dplaned
```

### Complete Database Reset

If the database is unrecoverable, you can drop and let the daemon recreate the schema from scratch (roles, permissions, and the default admin user will be seeded automatically):

```bash
sudo systemctl stop dplaned
sudo -u postgres psql -c "DROP DATABASE dplaneos;"
sudo -u postgres psql -c "CREATE DATABASE dplaneos;"
sudo systemctl start dplaned
# Check logs: should show "Seeded 4 built-in RBAC roles"
```

**Note:** This resets all sessions, user accounts, roles, and settings. ZFS pools, shares, and Docker containers are preserved as they live outside the database.

---

## 3. Authentication Recovery

### Interactive Recovery CLI

The fastest option:

```bash
sudo dplaneos-recovery
# Select option 5: Reset Admin Password
```

### Locked Out - Clear All Sessions

Sessions are stored in the `sessions` table. To force-logout all users:

```bash
sudo -u postgres psql dplaneos -c "DELETE FROM sessions;"
# All users must log in again
```

### Reset Admin User Directly

```bash
# Generate a bcrypt hash (example using python)
NEW_HASH=$(python3 -c "import bcrypt; print(bcrypt.hashpw(b'your-password', bcrypt.gensalt(12)).decode())")

sudo -u postgres psql dplaneos -c "
  INSERT INTO users (id, username, password_hash, display_name, email, active, source)
  VALUES (1, 'admin', '${NEW_HASH}', 'Administrator', 'admin@localhost', 1, 'local')
  ON CONFLICT (id) DO UPDATE SET password_hash = EXCLUDED.password_hash;
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

### Database Maintenance (PostgreSQL)

```bash
# Reclaim storage and update statistics
sudo -u postgres vacuumdb --all --analyze --verbose

# Check connection status
sudo -u postgres psql -c "\conninfo"
```

### Audit Log Growth

```bash
# Count audit entries
sudo -u postgres psql dplaneos -c "SELECT COUNT(*) FROM audit_logs;"

# Purge entries older than 90 days
sudo -u postgres psql dplaneos -c "
  DELETE FROM audit_logs WHERE timestamp < (EXTRACT(EPOCH FROM NOW()) - (90 * 86400));
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
systemctl status dplaned postgresql patroni etcd

# Health check
curl -s http://127.0.0.1:9000/health | python3 -m json.tool

# Database tables
sudo -u postgres psql dplaneos -c "\dt"

# Active sessions
sudo -u postgres psql dplaneos -c \
  "SELECT username, to_timestamp(created_at) FROM sessions WHERE status='active';"

# RBAC roles
sudo -u postgres psql dplaneos -c \
  "SELECT name, display_name, is_system FROM roles;"

# Recent audit entries
sudo -u postgres psql dplaneos -c \
  "SELECT to_timestamp(timestamp), \"user\", action, resource FROM audit_logs ORDER BY id DESC LIMIT 20;"

# Daemon version
/opt/dplaneos/daemon/dplaned -version 2>/dev/null || \
  curl -s http://127.0.0.1:9000/health | grep version
```
