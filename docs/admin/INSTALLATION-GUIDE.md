# D-PlaneOS Installation Guide

---

## System Requirements

### Minimum

| Component | Minimum |
|-----------|---------|
| CPU | Dual-core x86_64 or ARM64 |
| RAM | 2 GB (4 GB recommended) |
| OS disk | 8 GB free |
| Network | 100 Mbps Ethernet |

### Recommended

| Component | Recommended |
|-----------|-------------|
| CPU | Quad-core Intel/AMD or better |
| RAM | 16 GB+ |
| OS disk | 20 GB+ SSD |
| Network | 1 Gbps+ Ethernet |

### ECC RAM

ECC RAM is strongly recommended for ZFS at any scale. ZFS protects data against on-disk corruption, but it cannot detect corruption that originates in RAM before a write. ECC hardware closes this gap entirely.

D-PlaneOS detects ECC presence via `dmidecode` at startup and shows an advisory notice on the dashboard if non-ECC RAM is found. It never blocks installation. See [NON-ECC-WARNING.md](../hardware/NON-ECC-WARNING.md) for a detailed risk assessment.

### Supported Platforms

- Ubuntu 24.04 LTS (recommended)
- Ubuntu 22.04 LTS
- Debian 12
- Debian 11
- Raspberry Pi OS (64-bit)
- NixOS — see [nixos/README.md](../../nixos/README.md)

---

## Pre-Installation Checklist

- Fresh OS install with root/sudo access
- Internet connection (or use the offline/vendored build path)
- At least 2 drives: 1 for the OS, 1 or more for storage
- Static IP configured (recommended)
- Hostname set: `hostnamectl set-hostname nas`

---

## Installation

### Standard Install

```bash
# Download the latest release
wget https://github.com/4nonX/D-PlaneOS/releases/latest/download/dplaneos.tar.gz

# Verify SHA256 (published alongside each release on GitHub)
sha256sum dplaneos.tar.gz

# Extract and install
tar -xzf dplaneos.tar.gz
cd dplaneos
sudo bash install.sh
```

### Install with Options

```bash
sudo bash install.sh --port 8080           # custom web UI port (default 80)
sudo bash install.sh --unattended          # no prompts (CI/automation)
sudo bash install.sh --upgrade             # upgrade existing install, preserve data
sudo bash install.sh --upgrade --unattended
```

### What the Installer Does

The installer runs in 12 phases:

1. Pre-flight checks (OS, RAM, disk space, port availability)
2. Backup of existing installation (on upgrade)
3. System dependencies (nginx, zfsutils-linux, sqlite3, smartmontools, samba, nfs-kernel-server, etc.)
4. ZFS setup (loads kernel module, configures ARC based on available RAM)
5. File installation to `/opt/dplaneos/`
6. Daemon binary (builds from source with Go if present; downloads pre-built binary otherwise)
7. sudoers configuration
8. SQLite database initialization at `/var/lib/dplaneos/dplaneos.db`
9. nginx configuration
10. Kernel tuning (inotify, TCP buffers, swappiness)
11. Docker installation (optional, skipped if already present)
12. Service enablement and validation

**Installation time:** 5–10 minutes on a typical internet connection. Offline installs with the vendored tarball take 2–3 minutes.

### One-Liner Install

```bash
curl -fsSL https://get.dplaneos.io | sudo bash
```

---

## First Access

```bash
ip addr show  # find your server IP
```

Open `http://<your-server-ip>/` in a browser.

The installer prints the admin password at completion. You are required to change it on first login.

**Default credentials:** `admin` / (printed by installer — not a fixed default)

---

## Optional Protocols

These packages are auto-detected and fully managed once installed. Install any that you need after the initial setup:

```bash
# NFS exports
sudo apt install nfs-kernel-server

# iSCSI block targets
sudo apt install targetcli-fb

# SMB / Windows shares and AFP / Time Machine
sudo apt install samba

# UPS monitoring
sudo apt install nut
```

The UI shows a clear install prompt for any protocol package that is not present.

---

## Post-Installation

### Verify Services

```bash
# Main daemon
sudo systemctl status dplaned

# nginx
sudo systemctl status nginx

# Check all D-PlaneOS units
systemctl list-units 'dplaneos-*' 'dplaned*'

# Database
sudo sqlite3 /var/lib/dplaneos/dplaneos.db "SELECT COUNT(*) FROM roles;"
# Expected: 4 (admin, operator, viewer, user)
```

### Enable HTTPS (Recommended)

```bash
sudo apt install certbot python3-certbot-nginx
sudo certbot --nginx -d nas.yourdomain.com
sudo systemctl enable certbot.timer
```

### Configure Firewall

```bash
sudo ufw allow 80/tcp
sudo ufw allow 443/tcp
sudo ufw allow 22/tcp
sudo ufw enable
```

---

## Upgrading

```bash
# Always upgrade via the installer — it handles backup and rollback automatically
sudo bash install.sh --upgrade

# To roll back a failed upgrade
ls /var/lib/dplaneos/backups/
sudo bash /var/lib/dplaneos/backups/pre-upgrade-<timestamp>/rollback.sh
```

All data is preserved. ZFS pools, Docker containers, and network configuration are not affected.

---

## NixOS Installation

See [nixos/README.md](../../nixos/README.md) for the full NixOS guide.

D-PlaneOS is licensed under AGPLv3. NixOS correctly recognises it as free software — no `allowUnfreePredicate` or `allowUnfree` is needed.

---

## Uninstall

```bash
sudo systemctl stop dplaned nginx
sudo systemctl disable dplaned nginx
sudo rm -f /etc/systemd/system/dplaned.service
sudo rm -f /etc/systemd/system/dplaneos-*.service
sudo rm -rf /opt/dplaneos
sudo rm -rf /etc/dplaneos
sudo rm -f /etc/nginx/sites-{available,enabled}/dplaneos
sudo systemctl daemon-reload

# Remove data (irreversible)
sudo rm -rf /var/lib/dplaneos
sudo rm -rf /var/log/dplaneos
```

---

## Troubleshooting Installation

### ZFS not found

```bash
sudo apt update
sudo apt install zfsutils-linux
sudo modprobe zfs
```

### Daemon will not start

```bash
sudo journalctl -u dplaned -n 50
# Common cause: port conflict
sudo lsof -i :9000
```

### Cannot access web interface

```bash
sudo systemctl status nginx
sudo ufw status
curl http://127.0.0.1:9000/health  # test daemon directly
```

### Database initialization failed

```bash
sudo rm /var/lib/dplaneos/dplaneos.db
sudo systemctl restart dplaned
ls -lh /var/lib/dplaneos/dplaneos.db  # should be recreated
```

---

## Key Paths

| Item | Path |
|------|------|
| Install directory | `/opt/dplaneos/` |
| Database | `/var/lib/dplaneos/dplaneos.db` |
| DB backups | `/var/lib/dplaneos/backups/` |
| Web UI files | `/opt/dplaneos/app/` |
| Daemon binary | `/opt/dplaneos/daemon/dplaned` |
| Version | `/opt/dplaneos/VERSION` |
| nginx config | `/etc/nginx/sites-available/dplaneos` |
| Systemd unit | `/etc/systemd/system/dplaned.service` |
| Logs | `/var/log/dplaneos/` |
| Install log | `/var/log/dplaneos-install.log` |
