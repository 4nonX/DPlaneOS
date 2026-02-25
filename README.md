# D-PlaneOS v3.3.1 — Enterprise NAS Operating System

Source-available NAS OS with Material Design 3 UI, ZFS storage, Docker containers, RBAC, and LDAP/Active Directory integration.

## Quick Start

### Debian/Ubuntu
```bash
tar xzf dplaneos-v3.3.0.tar.gz
cd dplaneos
sudo make install   # Pre-built binary, no compiler needed
sudo systemctl start dplaned
```

### NixOS
```bash
cd nixos
sudo bash setup-nixos.sh
sudo nixos-rebuild switch --flake .#dplaneos
```

See [nixos/README.md](nixos/README.md) for the full NixOS guide.

Web UI: `http://your-server` (nginx reverse proxy on port 80 → daemon on 9000)

**Default login:** `admin` / `admin` (change immediately after first login)

> **Rebuilding from source?** You need Go 1.22+ and gcc: `make build` compiles fresh.

### Off-Pool Database Backup (recommended for large pools)

Edit `/etc/systemd/system/dplaned.service` and add `-backup-path`:
```
ExecStart=/opt/dplaneos/daemon/dplaned -db /var/lib/dplaneos/dplaneos.db -backup-path /mnt/usb/dplaneos.db.backup
```
Creates a VACUUM INTO backup on startup + every 24 hours.

## Features

- **Storage:** ZFS pools, snapshots, replication, encryption, quotas, file explorer
- **Compute:** Docker container management, app modules, Docker Compose
- **Network:** Interface config, routing, DNS
- **Identity:** User management, groups, LDAP/Active Directory
- **Security:** RBAC (4 roles), audit logging, API tokens, firewall
- **System:** Settings, logs, UPS management, hardware detection
- **UI:** Material Design 3, dark theme, responsive, keyboard shortcuts

## Features (v3.3.0)

### Safe Container Updates
`POST /api/docker/update` — ZFS snapshot → pull → restart → health check. On failure: instant rollback. No other NAS OS does this.

### ZFS Time Machine
Browse any snapshot like a folder. Find a deleted file from yesterday, restore just that one file — no full rollback needed.

### Ephemeral Docker Sandbox
Test any container on a ZFS clone (zero disk cost). Stop the container, the clone disappears. No residue.

### ZFS Health Predictor
Deep pool health monitoring: per-disk error tracking, checksum error detection, risk levels (low/medium/high/critical), S.M.A.R.T. integration. Warns you before a disk dies, not after.

### NixOS Config Guard
On NixOS systems: validate `configuration.nix` before applying, list/rollback generations, dry-activate checks. Cannot brick your system.

### ZFS Replication (Remote)
Native `zfs send | ssh remote zfs recv` — block-level replication that's 100x faster than rsync and preserves all snapshots on the remote.

## LDAP / Active Directory

Navigate to **Identity → Directory Service** to configure. Supports:

- Active Directory, OpenLDAP, FreeIPA (one-click presets)
- Group → Role mapping (auto-assign permissions)
- Just-In-Time user provisioning
- Background sync with audit trail
- TLS 1.2+ enforced

## Architecture

- **Frontend:** HTML5 + Material Design 3, flyout navigation, no framework dependencies
- **Backend:** Go daemon (`dplaned`, 8MB) on port 9000, 171 API routes
- **Database:** SQLite with WAL mode, `synchronous=FULL`, daily `.backup` (WAL-safe)
- **Web Server:** nginx reverse proxy (TLS termination)
- **Storage:** ZFS (native kernel module) + ZED hook for real-time disk failure alerts
- **Security:** Input validation on all exec.Command (regex whitelist), RBAC (4 roles, 34 permissions), injection-hardened, OOM-protected (1 GB limit)
- **NixOS:** Full support via Flake — entire NAS defined in a single `configuration.nix`

## Documentation

- `CHANGELOG.md` — Full version history including v3.0.0 through v3.3.1
- `CHANGELOG.md` — Full version history
- `ADMIN-GUIDE.md` — Full administration guide
- `ERROR-REFERENCE.md` — API error codes and diagnostics
- `TROUBLESHOOTING.md` — Build issues, ZED setup, common fixes
- `nixos/README.md` — NixOS installation and configuration
- `LDAP-REFERENCE.md` — LDAP technical reference
- `INSTALLATION-GUIDE.md` — Detailed installation steps

## License

Source-available under PolyForm Shield 1.0.0. See LICENSE file.
