# DPlaneOS

ZFS-first NAS operating system for homelab and small-office deployments. Runs as a single Go daemon behind nginx, backed by PostgreSQL, with a React UI served locally - no cloud dependencies, no mandatory subscriptions.

[![Version](https://img.shields.io/github/v/release/4nonX/DPlaneOS?style=flat-square&label=version)](https://github.com/4nonX/DPlaneOS/releases/latest)
[![License](https://img.shields.io/badge/license-AGPLv3-blue?style=flat-square)](https://github.com/4nonX/DPlaneOS/blob/main/LICENSE)
[![NixOS](https://img.shields.io/badge/platform-NixOS-5277C3?style=flat-square&logo=nixos&logoColor=white)](https://github.com/4nonX/DPlaneOS/blob/main/nixos/README.md)
[![CI Status](https://github.com/4nonX/DPlaneOS/actions/workflows/ci.yml/badge.svg?branch=main)](https://github.com/4nonX/DPlaneOS/actions)

*Clicking the Demo-Image leads to a demo-page, take a tour of the interface.*

[![DPlaneOS Demo](docs/assets/demo.png)](https://dplane.d-net.me/demo.html)
---

## Architecture

DPlaneOS is built for modern hardware. It ships as a single, statically-linked Go binary that manages your entire storage stack with zero external dependencies at runtime. The entire system - OS, services, firewall, ZFS configuration - is declared in code and reproduced exactly on every build.

- **Native Architecture Support:** Full performance on both `x86_64` (amd64) and `aarch64` (arm64/Raspberry Pi 5).
- **ZFS-First:** Native ZFS integration for data integrity, snapshots, and replication.
- **GitOps Driven:** Declarative state reconciliation for pools, datasets, and containers.
- **NixOS Appliance:** Ships as a bootable ISO. The entire OS is declared, reproducible, and rollback-safe.

## Install

Boot the installer ISO. It handles everything.

```bash
# Write the ISO to USB (Linux/macOS)
dd if=dplaneos-v*.iso of=/dev/sdX bs=4M status=progress conv=fsync
```

1. Boot the target machine from USB
2. The installer launches automatically - enter disk, hostname, SSH key
3. DPlaneOS is installed and running within minutes
4. Open `http://<your-server-ip>/` - change the admin password on first login

**Download:** [Latest release](https://github.com/4nonX/DPlaneOS/releases/latest) - grab `dplaneos-v*-installer-amd64.iso` (x86_64) or `...-arm64.iso` (aarch64/Raspberry Pi 5). The combined installer handles both NAS and HA witness node installation via a boot menu.

**Offline / air-gapped:** the ISO contains the complete NixOS closure. No internet access needed during installation.

**Rebuilding from source:** `nix build .#iso` - see [nixos/README.md](nixos/README.md).

---

## What It Does

| Area | Capabilities |
|------|-------------|
| **Storage** | ZFS pools, datasets, snapshots, replication (`zfs send`), encryption, quotas, S.M.A.R.T., POSIX ACLs, file explorer with chunked uploads |
| **Hot-swap** | udev rules detect disk add/remove; daemon auto-imports FAULTED/UNAVAIL pools and suggests vdev replacements via the UI |
| **Sharing** | SMB shares with Time Machine support (Samba vfs_fruit), NFS exports, iSCSI block targets - all configured through the UI |
| **Containers** | Docker management, Compose stacks, template library (one-click deploy), ephemeral ZFS-clone sandboxes, atomic updates with automatic rollback |
| **Network** | Interface config, bonding, VLANs, routing, DNS |
| **Identity** | Local users, LDAP / Active Directory with group-to-role mapping and directory-sourced UI login, TOTP 2FA, API tokens |
| **Security** | RBAC (4 roles, 34 permissions), HMAC audit chain, CSRF protection, firewall, TLS certificates, **Hardened Execution Whitelist** |
| **System** | Dashboard, logs, UPS (NUT), IPMI / sensors, hardware auto-tuning, cloud sync (rclone), HA node monitoring, **PostgreSQL HA (Patroni/etcd)** |
| **GitOps** | Git-sync repositories, state reconciliation |

---

## System Layout

```
Browser
  └── nginx :80/:443          static files from /opt/dplaneos/app/
        └── proxy /api/ /ws/
              └── dplaned :9000 (Go, ~8 MB)
                    ├── PostgreSQL /var/lib/dplaneos/pgsql/ (embedded/Patroni)
                    ├── ZFS     kernel module via exec.Command
                    ├── Docker  socket
                    └── LDAP/AD optional
```

| Component | Detail |
|-----------|--------|
| Frontend | React 19 + TypeScript + Vite, pre-built - no Node.js needed at runtime |
| Backend | Go daemon, ~256 API routes, **strict allowlist-validated exec calls**, no shell |
| Database | PostgreSQL 15+ (Patroni-managed High Availability) |
| Auth | bcrypt passwords (local), LDAP bind (directory accounts), TOTP 2FA, 32-byte session tokens, CSRF double-submit |
| ZFS events | ZED hook delivers pool fault/scrub/resilver events in real time |

---

## Key Paths

| Item | Path |
|------|------|
| Daemon binary | `/opt/dplaneos/daemon/dplaned` |
| Web UI (static) | `/opt/dplaneos/app/` |
| Version file | `/opt/dplaneos/VERSION` |
| Database state | `/var/lib/dplaneos/pgsql/` |
| DB configuration | `/etc/dplaneos/patroni.yaml` |
| Custom container icons | `/var/lib/dplaneos/custom_icons/` |
| Logs | `/var/log/dplaneos/` |
| ZED hook | `/etc/zfs/zed.d/dplaneos-notify.sh` |

---

## Documentation

### Installation and operation

| Document | What it covers |
|----------|---------------|
| [Installation Guide](docs/admin/INSTALLATION-GUIDE.md) | System requirements, ISO install flow, post-install checklist, OTA upgrades |
| [NixOS Install Guide](nixos/NIXOS-INSTALL-GUIDE.md) | Step-by-step for NixOS beginners: from empty hardware to running NAS |
| [Administrator Guide](docs/admin/ADMIN-GUIDE.md) | Users, roles, permissions, storage management, containers, LDAP/AD, custom icons, security practices |
| [Backup and Replication](docs/admin/BACKUP-REPLICATION.md) | ZFS snapshots, ZFS Send/Receive, Cloud Sync, Cold Tier, rsync, database backup, recovery |
| [High Availability](docs/admin/HIGH-AVAILABILITY.md) | HA cluster setup (Patroni, etcd, Keepalived, STONITH), failover, rolling upgrades |
| [OTA Updates](docs/admin/OTA-UPDATES.md) | A/B slot system, health check, auto-revert, manual rollback, HA rolling upgrades |
| [Optional Protocols](docs/admin/OPTIONAL-PROTOCOLS.md) | iSCSI, NVMe-oF, FTP/FTPS, MinIO S3-compatible object store |
| [Alerts and Authentication](docs/admin/ALERTS.md) | SMTP, webhook, Telegram alerting; alert taxonomy; TOTP 2FA setup and backup codes |
| [Troubleshooting](docs/admin/TROUBLESHOOTING.md) | Build failures, ZFS issues, DB init race, Docker behind proxy, browser cache, ZED setup |
| [Recovery Guide](docs/admin/RECOVERY.md) | Service management, database restore, admin lockout, ZFS recovery, hot-swap auto-import, rollback procedure |

### Reference

| Document | What it covers |
|----------|---------------|
| [Design Philosophy](docs/reference/PHILOSOPHY.md) | Why DPlaneOS works the way it does: four core principles, design decisions, trade-offs |
| [Architecture](docs/reference/ARCHITECTURE.md) | Three-layer model, persistence, single-node and HA architecture, data flow |
| [GitOps Reference](docs/reference/GITOPS-REFERENCE.md) | state.yaml format, reconciliation engine, drift detection, Capture workflow |
| [Changelog](docs/reference/CHANGELOG.md) | Full version history |
| [Error Reference](docs/reference/ERROR-REFERENCE.md) | Every HTTP error code the API returns, with cause and fix |
| [NixOS Rationale](docs/reference/NIXOS-RATIONALE.md) | Why DPlaneOS is NixOS-exclusive and how it leverages NixOS primitives |
| [Porting Guide](docs/reference/PORTING-GUIDE.md) | Forking DPlaneOS for other Linux distributions - what it takes, what you lose |
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
| [Release Workflow](.agents/workflows/release.md) | Step-by-step release procedure: version bump, build, tag, and push sequence |

### Legal

| Document | What it covers |
|----------|---------------|
| [License](LICENSE) | GNU Affero General Public License v3.0 |
| [Security Policy](SECURITY.md) | Vulnerability reporting, safe harbour, supported versions |
| [Individual CLA](docs/legal/CLA-INDIVIDUAL.md) | Contributor license agreement for individuals |
| [Entity CLA](docs/legal/CLA-ENTITY.md) | Contributor license agreement for organisations |
| [PostgreSQL Migration Guide](MIGRATION.md) | Upgrading from SQLite to PostgreSQL (v7.0.0) |

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

# Database access
sudo -u postgres psql dplaneos -c "\dt"
sudo -u postgres psql dplaneos -c "SELECT count(*) FROM users;"

# ZFS
zpool status
zpool import

# OTA upgrade
sudo dplaneos-ota-update
```

---

## License

[GNU Affero General Public License v3.0](https://www.gnu.org/licenses/agpl-3.0.html)
