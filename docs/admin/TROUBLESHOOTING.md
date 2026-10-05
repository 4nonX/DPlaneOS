# DPlaneOS Troubleshooting Guide

---

## Build Issues

### gcc Not Found

**Symptom:** `make build` fails with `cgo: C compiler "gcc" not found`

**Cause:** CGO is required for ZFS and system-level interop (though the main database is now PostgreSQL).

**Fix:** Use the Nix dev shell, which provides `gcc` and all build tools:

```bash
nix develop
```

Alternatively, enter the flake dev shell explicitly: `nix develop github:4nonX/DPlaneOS`.

### Go Not Found

**Symptom:** `make build` fails with `go: command not found`

**Fix:** Use the Nix dev shell - Go is pinned and provided automatically:

```bash
nix develop
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
`https://github.com/4nonX/DPlaneOS/releases/latest`

### ZED Hook Not Installed

**Symptom:** ZFS disk failures do not appear in the UI immediately; alerts are delayed until the next poll cycle (30 seconds).

**Fix:**

The ZED hook is managed by the NixOS module. If it is missing, the system state has diverged from the declared configuration. Restore it by rebuilding the system:

```bash
sudo nixos-rebuild switch
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

**Cause:** ZFS is declared in the NixOS configuration and loaded at boot. If the module is not loaded, the configuration has diverged from the running system.

**Diagnosis:**
```bash
lsmod | grep zfs
zpool list
sudo systemctl status dplaneos-zfs-gate
```

**Fix:** The ZFS gate service waits up to 120 seconds for pools to come online. If it timed out:

```bash
sudo systemctl restart dplaneos-zfs-gate
sudo systemctl restart dplaned
```

If the module itself is not present, run `sudo nixos-rebuild switch` to restore the declared system state.

---

### 2. Database Initialization Fails on First Boot

**Symptom:**
- First boot after install: daemon crashes once
- Login screen appears but no admin credentials work
- Daemon logs: `Failed to initialize database tables`
- Second boot works

**Cause:** Race condition - the daemon starts before PostgreSQL is fully initialized. The `dplaneos-init-db.service` unit normally prevents this; if it is absent the daemon may start too early.

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

PASS_TMP=$(mktemp)
chmod 600 "$PASS_TMP"
printf '%s' 'your-new-password' > "$PASS_TMP"

NEW_HASH=$(python3 -c "
import bcrypt, sys
with open(sys.argv[1], 'rb') as f:
    pw = f.read().strip()
print(bcrypt.hashpw(pw, bcrypt.gensalt(rounds=12)).decode())
" "$PASS_TMP")
rm -f "$PASS_TMP"

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

**Fix - Configure Docker proxy (NixOS - persistent):**

Add to `configuration.nix` and run `sudo nixos-rebuild switch`:
```nix
systemd.services.docker.serviceConfig.Environment = [
  "HTTP_PROXY=http://proxy.example.com:8080"
  "HTTPS_PROXY=http://proxy.example.com:8080"
  "NO_PROXY=localhost,127.0.0.1"
];
```

**Fix - Configure Docker proxy (temporary, cleared on next nixos-rebuild switch):**
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

**Note:** There is no service worker in DPlaneOS. This is a plain browser cache issue.

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

# Ensure the notification scripts are executable
sudo chmod +x /opt/dplaneos/install/scripts/notify-disk-added.sh
sudo chmod +x /opt/dplaneos/install/scripts/notify-disk-removed.sh

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
  http://localhost/api/zfs/resilver/status?pool=<pool>
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
# smartmontools is declared in the NixOS module - if missing, rebuild:
sudo nixos-rebuild switch

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
- SSH key not installed on the remote - test with: `ssh -i /root/.ssh/dplaneos_replication <user>@<host> zfs list`
- Remote host is unreachable - the pre-flight test in ReplicationPage verifies connectivity

---

### Post-Snapshot Replication Not Triggering

**Symptom:**
- `trigger_on_snapshot: true` is set on the replication schedule
- Snapshots are created on schedule but replication does not follow

**Cause:** Snapshot schedules use systemd timer units. If a schedule was created before the systemd timer migration, a legacy cron file may still be active instead of the current timer.

**Fix - Regenerate the snapshot timer:**
1. Open Snapshot Scheduler page
2. Edit the schedule and click Save (even with no changes)
3. This removes any legacy cron file and registers a new systemd timer unit that calls `POST /api/zfs/snapshots/cron-hook`

**Verify:**
```bash
# Current schedules run as systemd timers
systemctl list-timers 'dplaneos-snapshot-*'

# Confirm the timer unit calls the cron-hook endpoint
cat /etc/systemd/system/dplaneos-snapshot-<name>.service
# Should show: curl ... /api/zfs/snapshots/cron-hook
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
curl -I http://localhost/api/assets/custom-icons/myapp.svg

# Check container labels are being returned
curl -s -H "X-Session-ID: <id>" \
  http://localhost/api/docker/containers | \
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
curl -s http://localhost/api/docker/icon-map | \
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

# Web server (nginx logs go to journald on NixOS)
sudo journalctl -u nginx -f

# DPlaneOS application log
sudo journalctl -u dplaned -p err -f
```

### Step 3: Check File Paths

The installer places files in these locations:

| Item | Path |
|------|------|
| Application binary | `/opt/dplaneos/daemon/dplaned` |
| Web UI (static files) | `/opt/dplaneos/app/` |
| Configuration database | `/var/lib/dplaneos/pgsql/` |
| Disk registry | Stored in the main PostgreSQL database (`disk_registry` table) |
| Application logs | journald (`journalctl -u dplaned`) and `/var/log/dplaneos/` |
| Version file | `/opt/dplaneos/VERSION` |
| nginx config | Managed by NixOS (`services.nginx` in `configuration.nix`); read-only at `/etc/nginx/nginx.conf` |
| Daemon systemd unit | `/etc/systemd/system/dplaned.service` |
| ZED hook | `/etc/zfs/zed.d/all-dplaneos-notify.sh` (managed by NixOS module) |
| udev hot-swap rules | `/etc/udev/rules.d/99-dplaneos-hotswap.rules` |
| udev removable media rules | `/etc/udev/rules.d/99-dplaneos-removable-media.rules` |
| Snapshot schedules | systemd timers: `/etc/systemd/system/dplaneos-snapshot-*.timer` |
| Scrub schedules | systemd timers: `/etc/systemd/system/dplaneos-scrub-*.timer` |

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

DPlaneOS runs on NixOS. A full reset means reinstalling the OS from the ISO and then restoring configuration from Git and data from ZFS pools:

1. Boot from the DPlaneOS ISO and reinstall to the OS disk
2. After first boot, import data pools: `sudo zpool import -a`
3. Configure the GitOps repository: Settings - GitOps
4. Apply: `POST /api/gitops/apply` or run `sudo dplaned --apply-only`
5. Restore the PostgreSQL database from backup if needed (see [BACKUP-REPLICATION.md](BACKUP-REPLICATION.md))

ZFS data pools are on separate disks and are completely unaffected by a boot disk reinstall.

Before reinstalling, back up the PostgreSQL database if the data pools do not hold a ZFS snapshot of it:

```bash
sudo -u postgres pg_dump dplaneos | gzip > /tmp/dplaneos-db-$(date +%Y%m%d).sql.gz
# Copy to a safe location before reinstalling
```

### Rollback an Upgrade

Updates use an A/B slot system. To revert to the previous version:

```bash
# Revert to the previous NixOS generation (other slot)
sudo dplaneos-ota-update --revert

# If the system will not boot at all:
# Select the previous NixOS generation from the systemd-boot menu,
# press 'd' to set it as default, then Enter to boot it.
```

See [OTA-UPDATES.md](OTA-UPDATES.md) for full details on the A/B slot mechanism and automatic post-boot health checks.

---

## Getting Help

1. Run `sudo dplaneos-recovery` and use option 12 (Run Diagnostics)
2. Check logs: `sudo journalctl -u dplaned -n 50`
3. Note the exact error message
4. Include browser console errors (F12)

Report bugs at: https://github.com/4nonX/DPlaneOS/issues

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
| User locked out / forgot password | Medium | Lost credentials | Admin uses Settings → Users → lock_reset icon to set a temp password; user must change on next login |
| LDAP user sees password change form | Low | `source` not yet in session cache | Re-login refreshes the session; LDAP accounts display an informational notice and the form is disabled |
| Session survives browser close unexpectedly | Low | "Remember me" was checked at login | User checked Remember me; token is in localStorage; log out explicitly to revoke |
| Docker installs silently fail | Medium | Firewall or proxy | Configure Docker proxy or pre-pull images |
| UI broken after upgrade | Low | Browser cache | Hard refresh (Ctrl+Shift+R) |
| Hot-swap disk not detected | Medium | udev rules not installed | Reinstall udev rules, reload |
| Pool import not automatic | Medium | Disk not in registry | Manual `zpool import -d /dev/disk/by-id` |
| Pool creation rejected | Low | Raw `/dev/sdX` path submitted | Reload udev, re-open Hardware page |
| Alerts not firing | Medium | Alert not wired or de-duplicated | Test each channel; check event subscription |
| Custom icon not showing | Low | File missing or label typo | Verify file in `custom_icons/`, check label |
| Post-snapshot replication not firing | Low | Stale schedule (pre-timer migration) | Resave schedule to regenerate systemd timer |

