# D-PlaneOS Troubleshooting Guide

---

## Build Issues

### gcc Not Found

**Symptom:** `make build` fails with `cgo: C compiler "gcc" not found`

**Cause:** CGO is required for SQLite (`mattn/go-sqlite3` compiles C code).

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
CGO_ENABLED=1 go build -mod=vendor -tags "sqlite_fts5" \
  -ldflags="-s -w -X main.Version=$(cat ../VERSION)" \
  -o ../build/dplaned ./cmd/dplaned/
```

The release tarball for the current version is available at:
`https://github.com/4nonX/D-PlaneOS/releases/latest`

### Off-Pool Database Backup

**Symptom:** You want the configuration database on a separate drive (not the boot disk).

**Fix:** Edit `/etc/systemd/system/dplaned.service` and add `-backup-path`:

```
ExecStart=/opt/dplaneos/daemon/dplaned \
  -db /var/lib/dplaneos/dplaneos.db \
  -backup-path /mnt/usb-backup/dplaneos.db.backup
```

The daemon creates a `VACUUM INTO` backup on startup and every 24 hours.

### ZED Hook Not Installed

**Symptom:** ZFS disk failures do not appear in the UI immediately; alerts are delayed until the next poll cycle.

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
- Storage tabs are empty — no pools or datasets visible
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

**Cause:** Race condition — the daemon starts before the DB file is fully initialized. The `dplaneos-init-db.service` unit normally prevents this; if it is absent the daemon may start too early.

**Diagnosis:**
```bash
sudo systemctl status dplaned
sudo journalctl -u dplaned -n 50

# Check whether the database file was created
ls -lh /var/lib/dplaneos/dplaneos.db

# Check whether admin user exists
sqlite3 /var/lib/dplaneos/dplaneos.db "SELECT username, role FROM users WHERE username='admin';"
```

**Fix — Option 1: Wait for automatic recovery**

The daemon restarts via systemd. Wait 30 seconds, then refresh the browser. The admin user should exist on the second start.

**Fix — Option 2: Interactive recovery CLI**
```bash
sudo dplaneos-recovery
# Select option 5: Reset Admin Password
```

**Fix — Option 3: Manual admin reset**
```bash
sudo systemctl stop dplaned

# Generate a bcrypt hash for your chosen password
NEW_HASH=$(python3 -c "import bcrypt; print(bcrypt.hashpw(b'your-new-password', bcrypt.gensalt(12)).decode())")

sudo sqlite3 /var/lib/dplaneos/dplaneos.db "
  INSERT OR REPLACE INTO users (id, username, password_hash, display_name, email, role, active, source)
  VALUES (1, 'admin', '${NEW_HASH}', 'Administrator', 'admin@localhost', 'admin', 1, 'local');
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

**Cause:** Docker cannot reach Docker Hub. The GUI has no download progress indicator — check the Docker daemon log for pull progress.

**Diagnosis:**
```bash
docker pull hello-world:latest
sudo journalctl -u docker -n 50
docker info | grep -i proxy
```

**Fix — Configure Docker proxy:**
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

**Fix — Firewall rules:**
```bash
sudo ufw allow out 443/tcp comment 'Docker Hub'
```

**Workaround — pre-pull the image manually:**
```bash
docker pull <image-name>
# Then configure the container via the GUI
```

---

### 4. Browser Cache After Upgrade

**Symptom:**
- Upgrade from an older version with the browser still open
- After upgrade: layout breaks, buttons do not work, white screen, or mixed UI elements

**Cause:** The browser has cached old JavaScript/CSS bundles from the previous version. The frontend assets use content-hash filenames (e.g. `index-Cx-Q8_77.js`) — after an upgrade, the old cached bundle references files that no longer exist.

**Note:** There is no service worker in D-PlaneOS. This is a plain browser cache issue, not a service worker mismatch.

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
| Configuration database | `/var/lib/dplaneos/dplaneos.db` |
| Automatic DB backup | `/var/lib/dplaneos/dplaneos.db.backup` |
| Custom container icons | `/var/lib/dplaneos/custom_icons/` |
| Application logs | `/var/log/dplaneos/` |
| Install log | `/var/log/dplaneos-install.log` |
| Version file | `/opt/dplaneos/VERSION` |
| nginx config | `/etc/nginx/sites-available/dplaneos` |
| Daemon systemd unit | `/etc/systemd/system/dplaned.service` |
| ZED hook | `/etc/zfs/zed.d/dplaneos-notify.sh` |
| udev hot-swap rules | `/etc/udev/rules.d/99-dplaneos-hotswap.rules` |

### Step 4: Check Permissions

```bash
ls -la /opt/dplaneos/
ls -la /var/lib/dplaneos/
ls -la /var/log/dplaneos/

# DB must be readable by root (daemon runs as root)
stat /var/lib/dplaneos/dplaneos.db

# Web UI files should be readable by www-data
ls -la /opt/dplaneos/app/
```

### Step 5: Browser Console

Open Developer Tools (F12) and check:
- Console tab: JavaScript errors
- Network tab: failed API requests (look for 4xx or 5xx responses)

---

## Quick Diagnostic Checklist

Run these and share the output when filing a bug report:

```bash
# System info
uname -a
cat /etc/os-release

# D-PlaneOS version
cat /opt/dplaneos/VERSION

# ZFS status
lsmod | grep zfs
zpool list
zfs list

# Service status
systemctl status dplaned --no-pager
systemctl status nginx --no-pager

# Docker status
docker ps
docker info | grep -i proxy

# Database
ls -lh /var/lib/dplaneos/dplaneos.db
sqlite3 /var/lib/dplaneos/dplaneos.db "SELECT COUNT(*) FROM users;"

# Last 20 daemon log lines
sudo journalctl -u dplaned -n 20 --no-pager

# Disk space
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

This provides a menu-driven TUI for: service restart, database checks, admin password reset, ZFS pool import/export, permission fixes, log viewing, and full diagnostics. It does not require the web UI to be functional.

### Manual Full Reset

```bash
# Stop services
sudo systemctl stop dplaned nginx

# Backup data
sudo tar -czf /tmp/dplaneos-backup.tar.gz /var/lib/dplaneos

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
