# D-PlaneOS

ZFS-first NAS operating system for advanced homelab and small-office deployments.

---

## Quick Install

```bash
tar xzf dplaneos-*.tar.gz
cd dplaneos
sudo bash install.sh
```

Access the web UI at `http://your-server-ip` immediately after install. The installer prints a randomly generated admin password at completion; you are required to change it on first login.

**NixOS:**
```bash
cd nixos
sudo bash setup-nixos.sh
sudo nixos-rebuild switch --flake .#dplaneos
```

**Rebuilding from source:** Go 1.22+ and gcc are required — `make build` compiles everything from scratch.

---

## Features

**Storage:** ZFS pools, datasets, snapshots, replication, encryption, quotas, S.M.A.R.T. monitoring, file explorer with chunked uploads, hot-swap disk detection with automatic pool re-import.

**Sharing:** SMB / AFP / Time Machine, NFS exports, iSCSI block targets — all managed via the UI.

**Compute:** Docker container management, Compose stacks, ephemeral sandbox clones, safe rollback-aware updates.

**Network:** Interface configuration, bonding, VLANs, routing, DNS.

**Identity:** Users, groups, LDAP / Active Directory, 2FA (TOTP), API tokens.

**Security:** RBAC (4 roles, 34 permissions), bcrypt passwords, HMAC audit chain, CSRF protection, firewall management, TLS certificates.

**System:** Dashboard, logs, UPS management, IPMI / sensors, hardware detection, cloud sync (rclone), HA node monitoring.

**GitOps:** Git-sync repositories, state reconciliation.

**UI:** Material Design 3, dark theme, responsive, realtime metrics via WebSocket.

---

## Architecture

| Component | Details |
|-----------|---------|
| Frontend | React 19 + TypeScript + Vite, pre-built, zero runtime dependencies |
| Backend | Go daemon (`dplaned`, ~8 MB), port 9000, ~256 API routes |
| Database | SQLite with WAL mode, FTS5 full-text search, daily `VACUUM INTO` backup |
| Web server | nginx reverse proxy |
| Storage | ZFS kernel module + ZED hook for real-time disk failure alerts |
| Auth | bcrypt, TOTP 2FA, session tokens, CSRF double-submit |

---

## Optional Protocols

Install separately; auto-detected and fully managed by D-PlaneOS once present.

| Protocol | Install |
|----------|---------|
| SMB / Windows shares and AFP / Time Machine | `sudo apt install samba` |
| NFS exports | `sudo apt install nfs-kernel-server` |
| iSCSI block targets | `sudo apt install targetcli-fb` |
| UPS monitoring | `sudo apt install nut` |

---

## Documentation

### Getting Started

- [Installation Guide](docs/admin/INSTALLATION-GUIDE.md) — system requirements, install steps, upgrade, uninstall
- [Administrator Guide](docs/admin/ADMIN-GUIDE.md) — users, roles, storage, containers, LDAP, custom icons
- [Troubleshooting](docs/admin/TROUBLESHOOTING.md) — build issues, ZED setup, common failures, recovery
- [Recovery Guide](docs/admin/RECOVERY.md) — database recovery, authentication recovery, ZFS recovery, rollback

### Reference

- [Changelog](docs/reference/CHANGELOG.md) — full version history
- [Error Reference](docs/reference/ERROR-REFERENCE.md) — API error codes and diagnostics
- [Showstopper Mitigation Guide](docs/reference/SHOWSTOPPER-MITIGATION-GUIDE.md) — honest assessment of limitations and workarounds
- [Threat Model](docs/reference/THREAT-MODEL.md) — security architecture, threats, mitigations, known gaps
- [Dependencies](docs/reference/DEPENDENCIES.md) — bundled and system dependencies, build instructions

### Hardware

- [Hardware Compatibility](docs/hardware/HARDWARE-COMPATIBILITY.md) — supported CPUs, RAM, storage, network, auto-tuning
- [Non-ECC Warning](docs/hardware/NON-ECC-WARNING.md) — ZFS on non-ECC RAM: risks, mitigations, decision guidance

### Development

- [Contributing](CONTRIBUTING.md) — how to contribute, coding conventions, security requirements
- [Codebase Diagram](docs/development/CODEBASE-DIAGRAM.md) — architecture diagrams (Mermaid)
- [NixOS Guide](nixos/README.md) — NixOS module, flake setup, declarative configuration

### Legal

- [License](LICENSE) — GNU AGPLv3
- [Individual CLA](CLA-INDIVIDUAL.md) — contributor license agreement for individuals
- [Entity CLA](CLA-ENTITY.md) — contributor license agreement for organisations
- [Security Policy](SECURITY.md) — vulnerability reporting and safe harbour

---

## License

Licensed under the [GNU Affero General Public License v3.0 (AGPLv3)](https://www.gnu.org/licenses/agpl-3.0.html).
