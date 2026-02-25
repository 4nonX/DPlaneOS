# D-PlaneOS — Recovery & Administration Guide

## Quick Reference

| Problem | Solution |
|---------|----------|
| Service won't start | `journalctl -u dplaneos -n 50` |
| Database locked | `systemctl restart dplaneos` |
| Database corrupted | Restore from backup (see below) |
| Lost admin access | Reset session table (see below) |
| Pool not importing | `zpool import -f <pool>` |
| Web UI unreachable | Check reverse proxy + `curl http://127.0.0.1:9000/health` |
| High memory usage | Check `MemoryMax` in systemd, restart service |

---

## 1. Service Management

### Check Status

```bash
systemctl status dplaneos
```

### View Logs

```bash
# Last 50 lines
journalctl -u dplaneos -n 50

# Follow live
journalctl -u dplaneos -f

# Since last boot
journalctl -u dplaneos -b
```

### Restart

```bash
sudo systemctl restart dplaneos
```

Graceful shutdown: the daemon drains active connections before stopping.

### Verify Health

```bash
curl http://127.0.0.1:9000/health
# Expected: {"status":"ok","version":"2.0.0"}
```

---

## 2. Database Recovery

The SQLite database lives at `/var/lib/dplaneos/dplaneos.db`.

### Automatic Backups

`dplaned` creates a backup on every startup and daily via `VACUUM INTO`:

- Default: `/var/lib/dplaneos/dplaneos.db.backup`
- Custom: set via `-backup-path /path/to/backup.db`

### Restore from Backup

```bash
# Stop the service
sudo systemctl stop dplaneos

# Replace corrupted database
sudo cp /var/lib/dplaneos/dplaneos.db.backup /var/lib/dplaneos/dplaneos.db

# Remove WAL files (they belong to the old DB)
sudo rm -f /var/lib/dplaneos/dplaneos.db-wal
sudo rm -f /var/lib/dplaneos/dplaneos.db-shm

# Start the service (schema auto-init will fill any missing tables)
sudo systemctl start dplaneos
```

### Database Corruption Check

```bash
sudo sqlite3 /var/lib/dplaneos/dplaneos.db "PRAGMA integrity_check;"
# Expected: ok
```

### Complete Database Reset

If both primary and backup are corrupted, you can start fresh. All tables and default data (roles, permissions, admin user) are recreated automatically:

```bash
sudo systemctl stop dplaneos
sudo rm -f /var/lib/dplaneos/dplaneos.db*
sudo systemctl start dplaneos
# Check logs: should show "Seeded 4 built-in RBAC roles" etc.
```

**Note**: this resets all sessions, user accounts, LDAP config, and notification settings. ZFS pools, shares, and Docker containers are unaffected (they live on disk, not in the DB).

---

## 3. Authentication Recovery

### Locked Out (No Valid Session)

Sessions are stored in SQLite. If all sessions are expired or the DB is corrupted:

```bash
sudo systemctl stop dplaneos

# Clear all sessions (forces re-login)
sudo sqlite3 /var/lib/dplaneos/dplaneos.db "DELETE FROM sessions;"

sudo systemctl start dplaneos
```

### Reset Admin User

```bash
sudo sqlite3 /var/lib/dplaneos/dplaneos.db "
  INSERT OR REPLACE INTO users (id, username, display_name, email, active)
  VALUES (1, 'admin', 'Administrator', 'admin@localhost', 1);
"
```

---

## 4. ZFS Recovery

### Pool Won't Import

```bash
# List available pools
sudo zpool import

# Force import (e.g., after hardware move)
sudo zpool import -f <poolname>

# Import with different mountpoint
sudo zpool import -f -R /mnt <poolname>
```

### Pool Degraded

```bash
# Check pool status
sudo zpool status <poolname>

# Replace failed disk
sudo zpool replace <poolname> <old-device> <new-device>

# Start scrub (verify data integrity)
sudo zpool scrub <poolname>
```

### Encrypted Dataset Locked

```bash
# List locked datasets
sudo zfs list -o name,keystatus -r <poolname>

# Unlock (prompts for password)
sudo zfs load-key <poolname/dataset>

# Mount after unlocking
sudo zfs mount <poolname/dataset>
```

Or use the web UI: Storage → Encryption → Unlock.

---

## 5. Reverse Proxy Issues

`dplaned` listens on `127.0.0.1:9000` by default. If the web UI is unreachable:

### Check Direct Access

```bash
curl http://127.0.0.1:9000/health
```

If this works but the browser doesn't, the issue is in the reverse proxy.

### nginx Example Config

```nginx
server {
    listen 443 ssl;
    server_name nas.example.com;

    ssl_certificate /etc/ssl/certs/nas.pem;
    ssl_certificate_key /etc/ssl/private/nas.key;

    location / {
        proxy_pass http://127.0.0.1:9000;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
    }

    location /ws/ {
        proxy_pass http://127.0.0.1:9000;
        proxy_http_version 1.1;
        proxy_set_header Upgrade $http_upgrade;
        proxy_set_header Connection "upgrade";
    }
}
```

---

## 6. Performance Issues

### High Memory

```bash
# Check daemon memory
systemctl status dplaneos | grep Memory

# The systemd service limits memory to 512M
# If hitting limits, check for:
# - Large audit log table → purge old entries
# - Many active WebSocket connections
# - ZFS ARC pressure (kernel, not dplaned)
```

### Database Slow

```bash
# Force WAL checkpoint (reduces WAL file size)
sudo sqlite3 /var/lib/dplaneos/dplaneos.db "PRAGMA wal_checkpoint(TRUNCATE);"

# Check database size
ls -lh /var/lib/dplaneos/dplaneos.db*

# Manual VACUUM (rewrites entire DB, compacts space)
sudo systemctl stop dplaneos
sudo sqlite3 /var/lib/dplaneos/dplaneos.db "VACUUM;"
sudo systemctl start dplaneos
```

### Audit Log Growth

```bash
# Count audit entries
sudo sqlite3 /var/lib/dplaneos/dplaneos.db "SELECT COUNT(*) FROM audit_logs;"

# Purge entries older than 90 days
sudo sqlite3 /var/lib/dplaneos/dplaneos.db "
  DELETE FROM audit_logs WHERE timestamp < datetime('now', '-90 days');
"
```

---

## 7. Reinstallation

### Clean Install (Preserves Data)

```bash
# Stop service
sudo systemctl stop dplaneos

# Install new version
tar xzf dplaneos-v3.3.0.tar.gz
cd dplaneos
sudo make install

# Start service (auto-migrates schema if needed)
sudo systemctl start dplaneos
```

ZFS pools, Docker containers, and network configuration are not affected — they live in the kernel/system, not in `dplaned`.

### Complete Uninstall

```bash
sudo systemctl stop dplaneos
sudo systemctl disable dplaneos
sudo rm -f /etc/systemd/system/dplaned.service
sudo rm -rf /opt/dplaneos
sudo rm -rf /var/lib/dplaneos
sudo rm -rf /var/log/dplaneos
sudo systemctl daemon-reload
```

---

## 8. Diagnostic Commands

```bash
# Service status
systemctl status dplaneos

# Health check
curl -s http://127.0.0.1:9000/health | python3 -m json.tool

# API test (requires valid session)
curl -s -H "X-Session-ID: <token>" -H "X-User: admin" \
  http://127.0.0.1:9000/api/zfs/pools

# Database tables
sudo sqlite3 /var/lib/dplaneos/dplaneos.db ".tables"

# Active sessions
sudo sqlite3 /var/lib/dplaneos/dplaneos.db \
  "SELECT username, datetime(created_at, 'unixepoch') FROM sessions;"

# RBAC roles
sudo sqlite3 /var/lib/dplaneos/dplaneos.db \
  "SELECT name, display_name, is_system FROM roles;"

# Recent audit entries
sudo sqlite3 /var/lib/dplaneos/dplaneos.db \
  "SELECT timestamp, user, action, resource FROM audit_logs ORDER BY id DESC LIMIT 20;"

# Binary version
/opt/dplaneos/daemon/dplaned -version 2>/dev/null || \
  curl -s http://127.0.0.1:9000/health | grep version
```
