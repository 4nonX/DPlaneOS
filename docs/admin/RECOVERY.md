# DPlaneOS Recovery Guide

## Quick Reference

| Problem | Command |
|---------|---------|
| Service will not start | `journalctl -u dplaned -n 50` |
| Database locked | `systemctl restart dplaned` |
| Database corrupted | Restore a configuration backup (Settings → Maintenance; daily copies on the first pool) |
| Lost admin access | `sudo dplaneos-recovery` (option 5; option 6 for a lost 2FA device) |
| Pool not importing | `zpool import -f <pool>` |
| Web UI unreachable | `curl --unix-socket /run/dplaneos/dplaned.sock http://localhost/health` |
| High memory usage | Check `MemoryMax` in systemd unit, restart service |

---

## System Halts on Boot

If the system halts immediately after booting with no console output beyond early boot messages, the `persist-health-check` service has determined that `/persist` is not mounted or not writable. This is intentional: DPlaneOS cannot safely operate without its persistent state partition and halts rather than continuing in a degraded state that could corrupt the database or ZFS state.

To diagnose: attach a serial console or IPMI SOL, then look for journal entries from `dplaneos-persist-check`. The most common cause is a failed `/persist` ext4 partition (fsck failure) or a missing partition entry after a disk replacement. Boot from the installer ISO, run `fsck.ext4 -f /dev/sdX2` (the persist partition), mount it, and verify `/persist/dplaneos/` is readable.

If `/persist` is permanently lost, follow the Complete Database Reset procedure in section 2.

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
curl --unix-socket /run/dplaneos/dplaned.sock http://localhost/health
# Expected: {"status":"ok","version":"4.x.x"}
```

---

## 2. Database Recovery

The PostgreSQL database is node-local and stored at `/var/lib/dplaneos/pgsql/`.

### Configuration backup and restore

Settings → Maintenance downloads the whole configuration (database and the settings kept in files) and restores it; the daemon also writes one a day to the first data pool (`<pool>/.dplaneos-config/`, newest 14). A restore is checked, applied at the next daemon start in one transaction (all or nothing), and the configuration it replaced is kept as a backup you can download or restore. See [Backup and Replication](BACKUP-REPLICATION.md#configuration-backup).

Without the web UI (stored backups are under `<pool>/.dplaneos-config/`):

```bash
sudo dplaned -config-dir /etc/dplaneos -recover-restore /tank/.dplaneos-config/dplaneos-config-<host>-<date>.tar.gz
sudo systemctl restart dplaned     # applies it
```

Add `-recover-network` to restore the network settings too (same hardware only).

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

The fastest option for most recovery tasks. Run from SSH or a local console:

```bash
sudo dplaneos-recovery
```

| # | Option | What it does |
|---|--------|--------------|
| 1 | System Status | Hostname, OS, kernel, uptime, DPlaneOS version, memory, disk, service states |
| 2 | Check Services | dplaned, PostgreSQL, nginx, Samba, NFS, and whether the web ports listen |
| 3 | Restart Services | Restarts PostgreSQL, dplaned and nginx |
| 4 | Check Database | PostgreSQL state, connection, and the local users |
| 5 | Reset a User's Password | Pick a local user, set a new password (as the web UI does: bcrypt, SCRAM keys and SMB password); the account is activated and its sessions end |
| 6 | Turn Off a User's Two-Factor Authentication | For a lost authenticator without backup codes; set it up again after logging in |
| 7 | Check ZFS Pools | `zpool status` |
| 8 | Import/Export Pool | Import or export a pool by name |
| 9 | Check Network | Addresses, routes, DNS servers |
| 10 | UPS Status | NUT (`upsc`) |
| 11 | View Logs | dplaned (all or errors), the update check after a reboot, kernel errors, last 5 minutes |
| 12 | Run Diagnostics | Services, pools, disk space, memory, load |
| 13 | Emergency Shutdown | Power off or reboot after a confirmation |
| 14 | Exit | |

Options 4 to 6 use the daemon binary's recovery commands, which also work without the menu:

```bash
dplaned -db-dsn "<DSN>" -recover-list-users
printf '%s\n' 'New-Password-1!' | dplaned -db-dsn "<DSN>" -secrets-key <key> -recover-reset-password admin
dplaned -db-dsn "<DSN>" -recover-disable-2fa admin
```

(`<DSN>` and `<key>`: the `-db-dsn` and `-secrets-key` of `systemctl cat dplaned`.)

### Locked Out - Clear All Sessions

Sessions are stored in the `sessions` table. To force-logout all users:

```bash
sudo -u postgres psql dplaneos -c "DELETE FROM sessions;"
# All users must log in again
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

`dplaned` communicates with nginx via a Unix socket (`/run/dplaneos/dplaned.sock`). If the web UI is unreachable, test the daemon directly first:

```bash
curl --unix-socket /run/dplaneos/dplaned.sock http://localhost/health
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
systemctl status dplaned postgresql dplaneos-corosync

# Health check
curl -s --unix-socket /run/dplaneos/dplaned.sock http://localhost/health | python3 -m json.tool

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
  curl -s --unix-socket /run/dplaneos/dplaned.sock http://localhost/health | grep version
```
