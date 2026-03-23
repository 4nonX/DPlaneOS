# D-PlaneOS Troubleshooting Guide

---

## Build Issues

### gcc Not Found

**Symptom:** `make build` fails with `cgo: C compiler "gcc" not found`

**Cause:** CGO is required for ZFS and system-level interop (though the main database is now PostgreSQL).

**Fix:**
```bash
sudo apt install build-essential   # Debian/Ubuntu
sudo dnf install gcc               # Fedora/RHEL
```

### Go Not Found

**Symptom:** `make build` fails with `go: command not found`

**Fix:**
```bash
sudo apt install golang-go         # Debian/Ubuntu (Go 1.22+)
# Or download directly from https://go.dev/dl/
```

### Offline / Air-Gapped Build

**Symptom:** `go mod tidy` fails with network errors.

**Fix:** The release tarball ships with a vendored `daemon/vendor/` directory. Build against it directly:

```bash
cd daemon
CGO_ENABLED=1 go build -mod=vendor \
  -ldflags="-s -w -X main.Version=$(cat ../VERSION)" \
  -o ../build/dplaned ./cmd/dplaned/
```

The release tarball for the current version is at:
`https://github.com/4nonX/D-PlaneOS/releases/latest`

**Fix:** D-PlaneOS uses PostgreSQL. To move the data directory, follow standard PostgreSQL/Patroni procedures or update the mountpoint for `/var/lib/dplaneos/pgsql`.

### ZED Hook Not Installed

**Symptom:** ZFS disk failures do not appear in the UI immediately; alerts are delayed until the next poll cycle (30 seconds).

**Fix:**
```bash
sudo cp /opt/dplaneos/install/zed/dplaneos-notify.sh /etc/zfs/zed.d/
sudo chmod +x /etc/zfs/zed.d/dplaneos-notify.sh
sudo systemctl restart zed
```

---

## Common Showstoppers

### 1. ZFS Kernel Module Missing

**Symptom:**
- Installer completes without errors
- Web interface loads
- Storage tabs are empty - no pools or datasets visible
- Daemon logs: `ZFS is not available on this system`

**Cause:** D-PlaneOS delivers the management layer and daemon, not the ZFS kernel module. On a system where ZFS has never been installed, the storage subsystem cannot function.

**Diagnosis:**
```bash
lsmod | grep zfs
modprobe -n zfs
zpool list
```

**Fix:**
```bash
sudo apt install zfsutils-linux
sudo modprobe zfs
lsmod | grep zfs
zpool list
sudo systemctl restart dplaned
```

**Note:** `install.sh` stops hard if the ZFS kernel module cannot be loaded after installation. If you encounter this on an existing install, the module must be loaded before the daemon will serve storage data.

---

### 2. Database Initialization Fails on First Boot

**Symptom:**
- First boot after install: daemon crashes once
- Login screen appears but no admin credentials work
- Daemon logs: `Failed to initialize database tables`
- Second boot works

**Cause:** Race condition - the daemon starts before the DB file is fully initialized. The `dplaneos-init-db.service` unit normally prevents this; if it is absent the daemon may start too early.

**Diagnosis:**
```bash
sudo systemctl status dplaned
sudo journalctl -u dplaned -n 50

# Check whether admin user exists
sudo -u postgres psql dplaneos -c "SELECT username FROM users WHERE username='admin';"
```

**Fix - Option 1: Wait for automatic recovery**

The daemon restarts via systemd. Wait 30 seconds, then refresh the browser. The admin user should exist on the second start.

**Fix - Option 2: Interactive recovery CLI**
```bash
sudo dplaneos-recovery
# Select option 5: Reset Admin Password
```

**Fix - Option 3: Manual admin reset**
```bash
sudo systemctl stop dplaned

# Generate a bcrypt hash (example using python)
NEW_HASH=$(python3 -c "import bcrypt; print(bcrypt.hashpw(b'your-new-password', bcrypt.gensalt(12)).decode())")

sudo -u postgres psql dplaneos -c "
  INSERT INTO users (id, username, password_hash, display_name, email, active, source)
  VALUES (1, 'admin', '${NEW_HASH}', 'Administrator', 'admin@localhost', 1, 'local')
  ON CONFLICT (id) DO UPDATE SET password_hash = EXCLUDED.password_hash;
"

sudo systemctl start dplaned
```

---

### 3. Docker Behind Firewall or Proxy

**Symptom:**
- One-click install modules shown in the GUI
- Clicking Install shows a loading spinner, then nothing
- No error message
- `journalctl -u docker` shows pull failures or timeouts

**Cause:** Docker cannot reach Docker Hub. The GUI shows job progress via the async job queue - check `GET /api/jobs/<job_id>` for the actual error.

**Diagnosis:**
```bash
docker pull hello-world:latest
sudo journalctl -u docker -n 50
docker info | grep -i proxy
```

**Fix - Configure Docker proxy:**
```bash
sudo mkdir -p /etc/systemd/system/docker.service.d

sudo tee /etc/systemd/system/docker.service.d/http-proxy.conf <<EOF
[Service]
Environment="HTTP_PROXY=http://proxy.example.com:8080"
Environment="HTTPS_PROXY=http://proxy.example.com:8080"
Environment="NO_PROXY=localhost,127.0.0.1"
EOF

sudo systemctl daemon-reload
sudo systemctl restart docker
docker pull hello-world:latest
```

**Fix - Firewall rules:**
```bash
sudo ufw allow out 443/tcp comment 'Docker Hub'
```

**Workaround - pre-pull the image manually:**
```bash
docker pull <image-name>
# Then configure the container via the GUI
```

---

### 4. Browser Cache After Upgrade

**Symptom:**
- Upgrade from an older version with the browser still open
- After upgrade: layout breaks, buttons do not work, white screen, or mixed UI elements

**Cause:** The browser has cached old JavaScript/CSS bundles from the previous version. The frontend assets use content-hash filenames (e.g. `index-Cx-Q8_77.js`) - after an upgrade, the old cached bundle references files that no longer exist.

**Note:** There is no service worker in D-PlaneOS. This is a plain browser cache issue.

**Fix:**

Hard refresh:
```
Chrome / Edge (Windows/Linux) : Ctrl + Shift + R
Chrome / Edge (Mac)           : Cmd + Shift + R
Firefox (Windows/Linux)       : Ctrl + F5
Firefox (Mac)                 : Cmd + Shift + R
Safari                        : Cmd + Option + R
```

Or clear the site cache:
- Chrome: Settings → Privacy → Clear browsing data → Cached images and files
- Firefox: Preferences → Privacy & Security → Cookies and Site Data → Clear Data

---

## Disk Lifecycle Issues

### Hot-Swap Disk Not Detected

**Symptom:**
- Physically inserted a disk but it does not appear in Hardware page
- Pool import did not trigger automatically
- No `diskAdded` toast notification

**Cause:** The udev hot-swap rules may not be installed or the daemon notification channel is down.

**Diagnosis:**
```bash
# Verify udev rules are installed
ls /etc/udev/rules.d/99-dplaneos-hotswap.rules

# Check udev is processing events
udevadm monitor --subsystem-match=block

# Insert the disk and watch for events in the monitor output
# Should see: KERNEL[...] add /devices/... (block)

# Check daemon received the disk-added event
sudo journalctl -u dplaned -n 20 | grep -i "disk"
```

**Fix:**
```bash
# Reinstall udev rules
sudo cp /opt/dplaneos/install/udev/99-dplaneos-hotswap.rules /etc/udev/rules.d/
sudo udevadm control --reload-rules

# Reinstall notification scripts
sudo cp /opt/dplaneos/install/scripts/notify-disk-added.sh /opt/dplaneos/scripts/
sudo cp /opt/dplaneos/install/scripts/notify-disk-removed.sh /opt/dplaneos/scripts/
sudo chmod +x /opt/dplaneos/scripts/notify-disk-added.sh
sudo chmod +x /opt/dplaneos/scripts/notify-disk-removed.sh

# Trigger a manual rescan
udevadm trigger --subsystem-match=block
```

---

### Automatic Pool Import Did Not Trigger

**Symptom:**
- Disk was detected (appears in Hardware page)
- Pool remains UNAVAIL or does not appear
- Expected automatic re-import did not happen

**Cause:** The daemon matches arriving disks to known pools by serial number and WWN via the disk registry. If the disk has never been seen before, or the registry entry was cleared, the match will fail.

**Diagnosis:**
```bash
# Check whether the pool is importable
zpool import -d /dev/disk/by-id

# Check daemon logs for import attempt
sudo journalctl -u dplaned -n 50 | grep -i "import"

# Check disk registry
sudo -u postgres psql dplaneos -c "SELECT dev_name, by_id_path, pool_name, health FROM disk_registry;"
```

**Fix - Manual import:**
```bash
# Import using stable by-id paths (required - raw /dev/sdX paths are rejected)
sudo zpool import -d /dev/disk/by-id <pool-name>

# Force import if necessary
sudo zpool import -f -d /dev/disk/by-id <pool-name>
```

**Fix - Rebuild the disk registry entry:**
```bash
# Delete the stale registry entry so the daemon re-learns the disk
sudo -u postgres psql dplaneos -c \
  "DELETE FROM disk_registry WHERE dev_name = 'sdb';"

# Then trigger re-detection
udevadm trigger --subsystem-match=block --attr-match=removable=0
```

---

### Pool Creation Rejected: Unstable Disk Path

**Symptom:**
- Pool creation fails with: `disk paths must use stable /dev/disk/by-id/ identifiers`

**Cause:** The UI enforces `/dev/disk/by-id/` paths for pool creation. Raw `/dev/sdX` paths are rejected because they change across reboots. The disk discovery endpoint normally auto-promotes names to by-id, but on some systems the by-id symlinks may not exist yet.

**Diagnosis:**
```bash
# Check whether by-id symlinks exist for your disk
ls -la /dev/disk/by-id/ | grep sdb
```

**Fix:**
```bash
# If the symlinks are missing, reload udev
udevadm trigger --subsystem-match=block
udevadm settle

# Re-open the Hardware page - disk discovery runs fresh on page load
# The by-id path should now appear and pool creation will succeed
```

---

### Disk Replacement Not Progressing

**Symptom:**
- Replaced a failed disk via the Hardware page Replace modal
- `job_id` was returned but job shows no progress
- `GET /api/zfs/resilver/status?pool=<name>` returns `resilvering: false`

**Cause:** `zpool replace` is asynchronous - the job tracks the command exit code, not the resilver itself. The resilver is a ZFS background operation that continues independently.

**Diagnosis:**
```bash
# Check resilver status directly
zpool status <pool-name>
# Look for "scan: resilver in progress" line

# Check resilver via API
curl -s -H "X-Session-ID: <id>" \
  http://localhost:9000/api/zfs/resilver/status?pool=<pool>
```

**Note:** On large pools a resilver may take hours to days. The PoolsPage resilver progress card auto-refreshes every 10 seconds while active.

---

## Alert and Notification Issues

### Alerts Not Firing

**Symptom:**
- Pool goes DEGRADED but no Telegram / email / webhook received
- Alert configuration appears correct in the Alerts page

**Diagnosis:**
```bash
# Test each channel individually from the Alerts page:
# Telegram → "Send Test Message"
# SMTP     → "Send Test Email"
# Webhooks → "Test" button per webhook

# Check daemon logs for dispatch errors
sudo journalctl -u dplaned -n 50 | grep -iE "alert|dispatch|webhook|smtp|telegram"
```

**Common causes:**

| Symptom | Cause |
|---------|-------|
| Telegram test works, pool alerts don't | Pool heartbeat interval (30s) has not fired yet |
| SMTP test works, SMART alerts don't | SMART monitor runs every 6 hours; no alerts until next cycle |
| Webhooks not firing | Check `events` field - webhook must include the relevant event type |
| Capacity alerts missing | Pool must stay above threshold for one full check cycle |

**Note:** All alert channels are de-duplicated per resource per event type. A DEGRADED pool fires once when it transitions to DEGRADED, not on every heartbeat cycle. Alerts clear when the pool recovers to ONLINE.

---

### SMART Prediction Not Running

**Symptom:**
- `GET /api/zfs/smart/predict?device=sda` returns an error
- No SMART alerts received despite bad sectors

**Diagnosis:**
```bash
# Verify smartmontools is installed
which smartctl
smartctl --version

# Test manually
smartctl -A -j /dev/sda | python3 -m json.tool | head -30

# Check SMART monitor logs
sudo journalctl -u dplaned | grep -i "smart"
```

**Fix:**
```bash
# Install smartmontools if missing
sudo apt install smartmontools

# Restart daemon to reload the background SMART monitor
sudo systemctl restart dplaned
```

**Note:** The SMART background monitor runs every 6 hours. For an immediate check use `GET /api/zfs/smart/predict?device=<name>` directly.

---

## Replication Issues

### Scheduled Replication Not Running

**Symptom:**
- Replication schedule is configured and enabled
- `last_run` field stays null or stale
- No jobs appear in the job queue for replication

**Diagnosis:**
```bash
# Check replication schedule config
cat /etc/dplaneos/replication-schedules.json

# Check daemon logs
sudo journalctl -u dplaned | grep -i "replicate\|replication"
```

**Common causes:**
- Daemon was restarted after the schedule was created - the in-memory monitor reinitializes on start and will pick up the schedule on the next tick (within 5 minutes)
- SSH key not installed on the remote - test with: `ssh -i /etc/dplaneos/replication-key <user>@<host> zfs list`
- Remote host is unreachable - the pre-flight test in ReplicationPage verifies connectivity

---

### Post-Snapshot Replication Not Triggering

**Symptom:**
- `trigger_on_snapshot: true` is set on the replication schedule
- Snapshots are created on schedule but replication does not follow

**Cause:** The cron hook endpoint is called by the snapshot cron job. If the cron job was set up before v4.3.0, it may still call `zfs snapshot` directly rather than the hook endpoint.

**Fix - Regenerate the snapshot cron:**
1. Open Snapshot Scheduler page
2. Edit the schedule and click Save (even with no changes)
3. This regenerates the cron entry to use `POST /api/zfs/snapshots/cron-hook`

**Verify:**
```bash
cat /etc/cron.d/dplaneos-snapshots
# Should show: curl ... /api/zfs/snapshots/cron-hook
# Not: zfs snapshot ...
```

---

## Container Icons

### Custom Icon Not Showing

**Symptom:**
- `dplaneos.icon=myapp.svg` label is set in docker-compose.yaml
- Container still shows the generic `deployed_code` icon

**Diagnosis:**
```bash
# Verify the file exists in the custom icons directory
ls /var/lib/dplaneos/custom_icons/

# Verify the daemon can serve it
curl -I http://localhost:9000/api/assets/custom-icons/myapp.svg

# Check container labels are being returned
curl -s -H "X-Session-ID: <id>" \
  http://localhost:9000/api/docker/containers | \
  python3 -m json.tool | grep -A5 "Labels"
```

**Common causes:**

| Cause | Fix |
|-------|-----|
| File not in `/var/lib/dplaneos/custom_icons/` | Copy the file there: `sudo cp myapp.svg /var/lib/dplaneos/custom_icons/` |
| Wrong filename in label (case-sensitive) | Label value must exactly match the filename including extension |
| Container not restarted after label change | `docker compose up -d` to apply new labels |
| File is not `.svg`, `.png`, or `.webp` | Only these formats are served |

**Supported icon label formats:**
```yaml
labels:
  # Material Symbol name (no file extension)
  - "dplaneos.icon=database"

  # File in /var/lib/dplaneos/custom_icons/
  - "dplaneos.icon=myapp.svg"

  # Absolute URL (served via img tag, subject to CSP)
  - "dplaneos.icon=https://example.com/icon.png"
```

---

### Icon Map Not Resolving Known Images

**Symptom:**
- A well-known image like `jellyfin/jellyfin:latest` shows the generic icon
- No `dplaneos.icon` label is set

**Diagnosis:**
```bash
# Check the icon map is being served
curl -s http://localhost:9000/api/docker/icon-map | \
  python3 -m json.tool | grep jellyfin
```

**Cause:** The built-in map matches on lowercase substrings of the image name. The match checks both the full image string and the name portion (after the last `/`, before the `:`). If the image name contains an unexpected registry prefix or tag format it may not match.

**Fix:** Set the label explicitly:
```yaml
labels:
  - "dplaneos.icon=play_circle"
```

Available Material Symbol names: https://fonts.google.com/icons

---

## General Troubleshooting Steps

### Step 1: Check Service Status

```bash
# Main daemon
systemctl status dplaned

# Web server
systemctl status nginx

# Docker (if containers are involved)
systemctl status docker

# Supporting services
systemctl status dplaneos-init-db
systemctl status dplaneos-zfs-mount-wait
systemctl status dplaneos-realtime
```

### Step 2: Check Logs

```bash
# Daemon (most useful)
sudo journalctl -u dplaned -f

# Web server
sudo journalctl -u nginx -f
sudo tail -f /var/log/nginx/error.log

# D-PlaneOS application log
sudo tail -f /var/log/dplaneos/error.log
```

### Step 3: Check File Paths

The installer places files in these locations:

| Item | Path |
|------|------|
| Application binary | `/opt/dplaneos/daemon/dplaned` |
| Web UI (static files) | `/opt/dplaneos/app/` |
| Configuration database | `/var/lib/dplaneos/pgsql/` |
| Disk registry | Stored in the main PostgreSQL database (`disk_registry` table) |
| Application logs | `/var/log/dplaneos/` |
| Install log | `/var/log/dplaneos-install.log` |
| Version file | `/opt/dplaneos/VERSION` |
| nginx config | `/etc/nginx/sites-available/dplaneos` |
| Daemon systemd unit | `/etc/systemd/system/dplaned.service` |
| ZED hook | `/etc/zfs/zed.d/dplaneos-notify.sh` |
| udev hot-swap rules | `/etc/udev/rules.d/99-dplaneos-hotswap.rules` |
| udev removable media rules | `/etc/udev/rules.d/99-dplaneos-removable-media.rules` |
| Snapshot cron | `/etc/cron.d/dplaneos-snapshots` |
| Scrub cron | `/etc/cron.d/dplaneos-scrub` |

### Step 4: Check Permissions

```bash
ls -la /opt/dplaneos/
ls -la /var/lib/dplaneos/
ls -la /var/log/dplaneos/

# DB state must be readable by postgres/dplaned
stat /var/lib/dplaneos/pgsql/

# Web UI files should be readable by www-data
ls -la /opt/dplaneos/app/

# Custom icons must be readable
ls -la /var/lib/dplaneos/custom_icons/
```

### Step 5: Browser Console

Open Developer Tools (F12) and check:
- Console tab: JavaScript errors
- Network tab: failed API requests (look for 4xx or 5xx responses on `/api/` calls)

---

## Quick Diagnostic Checklist

Run these and share the output when filing a bug report:

```bash
# System info
uname -a
cat /etc/os-release
cat /opt/dplaneos/VERSION

# ZFS status
lsmod | grep zfs
zpool list
zfs list

# Disk registry
sudo -u postgres psql dplaneos -c \
  "SELECT dev_name, by_id_path, pool_name, health, last_seen FROM disk_registry;"

# Service status
systemctl status dplaned postgresql patroni etcd --no-pager

# Database
sudo -u postgres psql dplaneos -c "SELECT COUNT(*) FROM users;"

# Alert config sanity check
sudo -u postgres psql dplaneos -c \
  "SELECT name, events FROM webhook_configs;"

# Last 20 daemon log lines
sudo journalctl -u dplaned -n 20 --no-pager

# Disk and memory
df -h /opt /var/lib
free -h
```

---

## Emergency Recovery

### Interactive Recovery CLI

The recovery CLI is available on any installed system:

```bash
sudo dplaneos-recovery
```

This provides a menu-driven interface for: service restart, database checks, admin password reset, ZFS pool import/export, permission fixes, log viewing, and full diagnostics. It does not require the web UI to be functional.

### Manual Full Reset

```bash
# Stop services
sudo systemctl stop dplaned nginx

# Backup data (database + config + custom icons)
sudo tar -czf /tmp/dplaneos-backup.tar.gz \
  /var/lib/dplaneos \
  /etc/dplaneos

# Remove and reinstall
sudo rm -rf /opt/dplaneos
sudo ./install.sh

# Restore data
sudo tar -xzf /tmp/dplaneos-backup.tar.gz -C /
sudo systemctl restart dplaned
```

### Rollback an Upgrade

Each upgrade creates a timestamped backup. To roll back:

```bash
ls /var/lib/dplaneos/backups/
sudo bash /var/lib/dplaneos/backups/pre-upgrade-<timestamp>/rollback.sh
```

---

## Getting Help

1. Run `sudo dplaneos-recovery` and use option 12 (Run Diagnostics)
2. Check logs: `sudo journalctl -u dplaned -n 50`
3. Note the exact error message
4. Include browser console errors (F12)

Report bugs at: https://github.com/4nonX/D-PlaneOS/issues

Include:
```bash
uname -a
cat /etc/os-release
cat /opt/dplaneos/VERSION
sudo journalctl -u dplaned -n 50 > daemon.log
```

---

## Issue Summary

| Issue | Severity | Likely Cause | Resolution |
|-------|----------|--------------|------------|
| Storage tabs empty | High | ZFS module not loaded | `modprobe zfs` |
| No admin on first boot | Medium | DB init race | Second boot auto-recovers; or use `dplaneos-recovery` |
| Docker installs silently fail | Medium | Firewall or proxy | Configure Docker proxy or pre-pull images |
| UI broken after upgrade | Low | Browser cache | Hard refresh (Ctrl+Shift+R) |
| Hot-swap disk not detected | Medium | udev rules not installed | Reinstall udev rules, reload |
| Pool import not automatic | Medium | Disk not in registry | Manual `zpool import -d /dev/disk/by-id` |
| Pool creation rejected | Low | Raw `/dev/sdX` path submitted | Reload udev, re-open Hardware page |
| Alerts not firing | Medium | Alert not wired or de-duplicated | Test each channel; check event subscription |
| Custom icon not showing | Low | File missing or label typo | Verify file in `custom_icons/`, check label |
| Post-snapshot replication not firing | Low | Old cron format (pre-4.3.0) | Resave schedule to regenerate cron hook |

