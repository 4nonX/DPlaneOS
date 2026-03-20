# D-PlaneOS

ZFS-first NAS operating system for homelab and small-office deployments. Runs as a single Go daemon behind nginx, backed by SQLite, with a React UI served locally no cloud dependencies, no mandatory subscriptions.

[![Version](https://img.shields.io/github/v/release/4nonX/D-PlaneOS?style=flat-square&label=version)](https://github.com/4nonX/D-PlaneOS/releases/latest)
[![License](https://img.shields.io/badge/license-AGPLv3-blue?style=flat-square)](https://github.com/4nonX/D-PlaneOS/blob/main/LICENSE)
[![NixOS](https://img.shields.io/badge/platform-NixOS-5277C3?style=flat-square&logo=nixos&logoColor=white)](https://github.com/4nonX/D-PlaneOS/blob/main/nixos/README.md)
[![Debian](https://img.shields.io/badge/platform-Debian-A81D33?style=flat-square&logo=debian&logoColor=white)](https://github.com/4nonX/D-PlaneOS/blob/main/docs/admin/INSTALLATION-GUIDE.md)
[![Ubuntu](https://img.shields.io/badge/platform-Ubuntu-E95420?style=flat-square&logo=ubuntu&logoColor=white)](https://github.com/4nonX/D-PlaneOS/blob/main/docs/admin/INSTALLATION-GUIDE.md)
[![CI Status](https://github.com/4nonX/D-PlaneOS/actions/workflows/unified-ci.yml/badge.svg?branch=main)](https://github.com/4nonX/D-PlaneOS/actions)

*Demo recorded in preview mode, "demo" data, no real hardware required to evaluate.*

![D-PlaneOS Demo](demo.gif)

---

## High-Performance, Zero-Pollution Architecture

D-PlaneOS is built for modern hardware. It ships as a single, statically-linked Go binary that manages your entire storage stack with zero external dependencies at runtime.

- **Native Architecture Support:** Full performance on both `x86_64` (amd64) and `aarch64` (arm64/Raspberry Pi 5).
- **ZFS-First:** Native ZFS integration for data integrity, snapshots, and replication.
- **GitOps Driven:** Declarative state reconciliation for pools, datasets, and containers.
- **NixOS Optimized:** Designed to run as a native service on NixOS, but fully supports Debian/Ubuntu.

## Install

```bash
tar xzf dplaneos-*.tar.gz
cd dplaneos
sudo bash install.sh
```

The installer handles everything: ZFS kernel module, nginx, database, systemd units, udev rules. It prints a randomly generated admin password on completion — you must change it on first login.

| Option | Effect |
|--------|--------|
| `--port 8080` | Serve on a non-standard port (default: 80) |
| `--upgrade` | Upgrade in place, preserve all data |
| `--unattended` | Skip all confirmation prompts (CI/automation) |

**NixOS** - see [nixos/README.md](nixos/README.md) for ISO, Flake, and standalone paths.

**Air-gapped / offline** - the release tarball ships with a vendored `daemon/vendor/` directory. No internet access needed to build or install.

**Rebuilding from source:** requires Go 1.22+ and gcc. Run `make build`.

---

## What It Does

| Area | Capabilities |
|------|-------------|
| **Storage** | ZFS pools, datasets, snapshots, replication (`zfs send`), encryption, quotas, S.M.A.R.T., POSIX ACLs, file explorer with chunked uploads |
| **Hot-swap** | udev rules detect disk add/remove; daemon auto-imports FAULTED/UNAVAIL pools and suggests vdev replacements via the UI |
| **Sharing** | SMB shares with Time Machine support (Samba vfs_fruit), NFS exports, iSCSI block targets — all configured through the UI |
| **Containers** | Docker management, Compose stacks, template library (one-click deploy), ephemeral ZFS-clone sandboxes, atomic updates with automatic rollback |
| **Network** | Interface config, bonding, VLANs, routing, DNS |
| **Identity** | Local users, LDAP / Active Directory with group-to-role mapping and directory-sourced UI login, TOTP 2FA, API tokens |
| **Security** | RBAC (4 roles, 34 permissions), HMAC audit chain, CSRF protection, firewall, TLS certificates |
| **System** | Dashboard, logs, UPS (NUT), IPMI / sensors, hardware auto-tuning, cloud sync (rclone), HA node monitoring |
| **GitOps** | Git-sync repositories, state reconciliation |

---

## Architecture

```
Browser
  └── nginx :80/:443          static files from /opt/dplaneos/app/
        └── proxy /api/ /ws/
              └── dplaned :9000 (Go, ~8 MB)
                    ├── SQLite  /var/lib/dplaneos/dplaneos.db
                    ├── ZFS     kernel module via exec.Command
                    ├── Docker  socket
                    └── LDAP/AD optional
```

| Component | Detail |
|-----------|--------|
| Frontend | React 19 + TypeScript + Vite, pre-built — no Node.js needed at runtime |
| Backend | Go daemon, ~256 API routes, allowlist-validated exec calls, no shell |
| Database | SQLite WAL + FTS5, daily `VACUUM INTO` backup, HMAC audit chain |
| Auth | bcrypt passwords (local), LDAP bind (directory accounts), TOTP 2FA, 32-byte session tokens, CSRF double-submit |
| ZFS events | ZED hook delivers pool fault/scrub/resilver events in real time |

---

## Optional Protocols

Auto-detected and fully managed once installed. The UI shows an install prompt for anything not yet present.

```bash
sudo apt install samba            # SMB shares + Time Machine (vfs_fruit)
sudo apt install nfs-kernel-server   # NFS exports
sudo apt install targetcli-fb     # iSCSI block targets
sudo apt install nut              # UPS monitoring
```

---

## Key Paths

| Item | Path |
|------|------|
| Install directory | `/opt/dplaneos/` |
| Daemon binary | `/opt/dplaneos/daemon/dplaned` |
| Web UI (static) | `/opt/dplaneos/app/` |
| Version file | `/opt/dplaneos/VERSION` |
| Database | `/var/lib/dplaneos/dplaneos.db` |
| DB backup | `/var/lib/dplaneos/dplaneos.db.backup` |
| Custom container icons | `/var/lib/dplaneos/custom_icons/` |
| Logs | `/var/log/dplaneos/` |
| nginx config | `/etc/nginx/sites-available/dplaneos` |
| Systemd unit | `/etc/systemd/system/dplaned.service` |
| ZED hook | `/etc/zfs/zed.d/dplaneos-notify.sh` |
| udev hot-swap rules | `/etc/udev/rules.d/99-dplaneos-hotswap.rules` |

---

## Documentation

### Installation and operation

| Document | What it covers |
|----------|---------------|
| [Installation Guide](docs/admin/INSTALLATION-GUIDE.md) | System requirements, step-by-step install, post-install checklist, upgrade, uninstall, NixOS path |
| [Administrator Guide](docs/admin/ADMIN-GUIDE.md) | Users, roles, permissions, storage management, containers, LDAP/AD, custom icons, security practices |
| [Troubleshooting](docs/admin/TROUBLESHOOTING.md) | Build failures, ZFS module missing, DB init race, Docker behind proxy, browser cache, ZED setup, all correct paths |
| [Recovery Guide](docs/admin/RECOVERY.md) | Service management, database restore, admin lockout, ZFS recovery, hot-swap auto-import, rollback procedure |

### Reference

| Document | What it covers |
|----------|---------------|
| [Changelog](docs/reference/CHANGELOG.md) | Full version history |
| [Error Reference](docs/reference/ERROR-REFERENCE.md) | Every HTTP error code the API returns, with cause and fix |
| [Showstopper Mitigation Guide](docs/reference/SHOWSTOPPER-MITIGATION-GUIDE.md) | Honest assessment of HA limits, binary-trust, resolved vs open issues |
| [Threat Model](docs/reference/THREAT-MODEL.md) | Security architecture, all 13 threat scenarios, mitigations, residual risks, known gaps |
| [Dependencies](docs/reference/DEPENDENCIES.md) | All bundled Go and frontend deps, system requirements, build instructions |

### Hardware

| Document | What it covers |
|----------|---------------|
| [Hardware Compatibility](docs/hardware/HARDWARE-COMPATIBILITY.md) | Supported CPUs, RAM sizes, disk types, network, RAID controllers, auto-tuning examples |
| [Non-ECC RAM Warning](docs/hardware/NON-ECC-WARNING.md) | Why ZFS + non-ECC is risky, probability analysis, mitigations, upgrade guidance |

### NixOS

| Document | What it covers |
|----------|---------------|
| [NixOS Overview](nixos/README.md) | ISO, Flake, and standalone install paths; what is included; system requirements |
| [NixOS Install Guide](nixos/NIXOS-INSTALL-GUIDE.md) | Step-by-step for NixOS beginners: from empty hardware to running NAS |
| [NixOS Technical Reference](nixos/NIXOS-README.md) | Declarative config details, rollback, licensing, advanced options |

### Development

| Document | What it covers |
|----------|---------------|
| [Contributing](CONTRIBUTING.md) | Dev setup, project structure, coding conventions, security requirements for PRs |
| [Codebase Diagram](docs/development/CODEBASE-DIAGRAM.md) | Mermaid diagrams: system overview, daemon internals, request lifecycle, disk event flow, auth flow |

### Legal

| Document | What it covers |
|----------|---------------|
| [License](LICENSE) | GNU Affero General Public License v3.0 |
| [Security Policy](SECURITY.md) | Vulnerability reporting, safe harbour, supported versions |
| [Individual CLA](CLA-INDIVIDUAL.md) | Contributor license agreement for individuals |
| [Entity CLA](CLA-ENTITY.md) | Contributor license agreement for organisations |

---

## Quick Command Reference

```bash
# Service control
sudo systemctl status dplaned
sudo systemctl restart dplaned
sudo journalctl -u dplaned -f

# Health check
curl http://127.0.0.1:9000/health

# Interactive recovery (locked out, DB issues, ZFS problems)
sudo dplaneos-recovery

# Database
sqlite3 /var/lib/dplaneos/dplaneos.db "SELECT COUNT(*) FROM users;"
sqlite3 /var/lib/dplaneos/dplaneos.db "PRAGMA integrity_check;"

# ZFS
zpool status
zpool import

# Upgrade
sudo bash install.sh --upgrade
```

---

## License

[GNU Affero General Public License v3.0](https://www.gnu.org/licenses/agpl-3.0.html)
