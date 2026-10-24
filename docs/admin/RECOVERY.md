# DPlaneOS Recovery Guide

## Quick Reference

| Problem | Command |
|---------|---------|
| Service will not start | `journalctl -u dplaned -n 50` |
| Database corrupted | Restore a configuration backup (Settings → Maintenance; daily copies on the first pool) |
| Lost admin access | `sudo dplaneos-recovery` (option 5; option 6 for a lost 2FA device) |
| Pool not importing | `zpool import -f <pool>` |
| Web UI unreachable | `curl --unix-socket /run/dplaneos/dplaned.sock http://localhost/health` |
| High memory usage | `systemctl status dplaned` (memory cap 1 GB, `services.dplaneos.memoryMax`) |

---

## Boot Stops Before the System Starts

`/persist` (the database, keys and settings) is mounted early in the boot. If it cannot be mounted, the boot stops there with an emergency shell instead of starting DPlaneOS without its state. The usual causes are a damaged ext4 file system or a missing partition after a disk change.

Boot the installer ISO, choose **Shell**, and check the partition:

```bash
fsck.ext4 -f /dev/disk/by-partlabel/dplaneos-persist     # installs before 16.0: /dev/disk/by-partlabel/persist
mount /dev/disk/by-partlabel/dplaneos-persist /mnt
ls /mnt/var/lib/dplaneos                                  # the daemon's state
```

If `/persist` is lost for good, reinstall and restore the configuration backup from the first data pool (see section 2).

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
# Expected: {"status":"ok","version":"16.0.0"}
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

If the database is unrecoverable and there is no backup, drop it and let the daemon recreate the schema (roles, permissions and an `admin` user without a password are seeded):

```bash
sudo systemctl stop dplaned
sudo -u postgres psql -c "DROP DATABASE dplaneos;"
sudo -u postgres psql -c "CREATE DATABASE dplaneos OWNER dplaneos;"
sudo systemctl start dplaned
# Check logs: should show "Seeded 4 built-in RBAC roles"
```

Set the admin password at once: until then the setup step of the web interface can set it from the network.

```bash
printf '%s\n' 'New-Password-1!' | sudo dplaned -db-dsn "<DSN>" -secrets-key <key> -recover-reset-password admin
```

(`<DSN>` and `<key>`: the `-db-dsn` and `-secrets-key` of `systemctl cat dplaned`.)

**Note:** This resets all sessions, user accounts, roles, and settings. Data on the ZFS pools stays; shares, stacks and other settings have to be set up again.

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

When a disk is connected or removed, a udev rule of the NixOS module passes the event to the daemon (`POST /api/internal/disk-event`, through a short-lived `dplaneos-disk-event` unit). The daemon:

1. Enriches the device with stable identifiers (by-id path, WWN, size)
2. Cross-checks the disk registry for pool membership
3. Brings the disk back online if it is a member of an imported pool, or imports its exported pool (`zpool import -d /dev/disk/by-id <guid>`) if the disk was last seen in that pool on this machine; a disk from another system never imports its pool, and a shared storage group's pool is imported only by its owner
4. Broadcasts a `poolHealthChanged` WebSocket event to update the UI
5. If faulted vdevs exist, broadcasts `diskReplacementAvailable` and pre-populates the Replace modal

No manual `zpool import` is needed for pools the daemon has previously seen. At boot, the pools this machine had imported are imported by `dplaneos-zfs-import` (from `/etc/zfs/zpool.cache`).

### Encrypted Dataset Locked

```bash
# List locked datasets
sudo zfs list -o name,keystatus -r <poolname>

# Unlock (prompts for passphrase)
sudo zfs load-key <poolname/dataset>

# Mount after unlocking
sudo zfs mount <poolname/dataset>
```

Or use the web UI: **Storage › Datasets** → the locked dataset → **Unlock**.

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

### nginx Configuration

The nginx configuration is part of the NixOS system (`services.nginx` in `module.nix`); it is not edited on the machine. Show it with `sudo nginx -T`. Going back to the previous generation (`sudo nixos-rebuild switch --rollback`) also brings back its nginx configuration.

---

## 6. Performance Issues

### High Memory

```bash
# Check daemon memory usage
systemctl status dplaned | grep Memory

# The systemd unit caps daemon memory at 1 GB (MemoryMax=1G, services.dplaneos.memoryMax)
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

The audit log is rotated once a week: entries older than 90 days are removed (`audit_retention_days`). Security › Security Settings rotates it at once with another number of days to keep.

```bash
# Count audit entries
sudo -u postgres psql dplaneos -c "SELECT COUNT(*) FROM audit_logs;"
```

---

## 7. Reinstallation

DPlaneOS is the operating system: there is no in-place reinstall or uninstall.

- **A newer release:** System › System Updates (see [Updates](OTA-UPDATES.md)).
- **A broken system disk:** boot the installer ISO and install onto the system disk again (it is erased). Then import the pools (Storage › ZFS Pools › Import) and restore the latest configuration backup from `<pool>/.dplaneos-config/` (Settings › Maintenance).
- **Removing DPlaneOS:** install another operating system on the system disk. The ZFS pools on the data disks are not touched and can be imported by any system with OpenZFS.

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
  "SELECT to_timestamp(timestamp), actor, action, resource FROM audit_logs ORDER BY id DESC LIMIT 20;"

# Daemon version
dplaned -version
```
