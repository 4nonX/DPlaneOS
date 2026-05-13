# DPlaneOS on NixOS

> Your complete NAS defined in a single text file. Reproducible, versioned, rollback-safe.

**License:** DPlaneOS uses [GNU Affero General Public License v3.0 (AGPLv3)](https://www.gnu.org/licenses/agpl-3.0.html).
Free to use and modify - see [NIXOS-README.md](NIXOS-README.md#a-note-on-licensing-and-nix-unfree) for
how this works with Nix's unfree package handling (short version: it's handled automatically).

## Three Installation Paths

### Path 1: Boot the ISO (easiest: no NixOS knowledge needed)

Flash the ISO to a USB stick, boot it, and you're in a live DPlaneOS
environment. The web UI runs immediately. Type `dplaneos-install` to
install permanently.

```bash
# Build the ISO (or download a release)
nix build .#nixosConfigurations.dplaneos-iso.config.system.build.isoImage
sudo dd if=result/iso/dplaneos-3.0.0-x86_64-linux.iso of=/dev/sdX bs=4M status=progress
```

### Path 2: Flake (for existing NixOS users)

Reproducible, pinned versions, one command to update.

```bash
# On a running NixOS:
git clone https://github.com/4nonX/DPlaneOS
cd DPlaneOS/nixos

# Run setup helper (fills in host ID, timezone, etc.)
sudo bash setup-nixos.sh

# Build the system
sudo nixos-rebuild switch --flake .#dplaneos

# Open browser → http://dplaneos.local
```

**Update:**
```bash
cd DPlaneOS/nixos && git pull
sudo nixos-rebuild switch --flake .#dplaneos
```

**Rollback:**
```bash
sudo nixos-rebuild switch --rollback
```

### Path 3: Standalone (without Flake)

Simpler if you don't want to use Flakes yet.

```bash
sudo cp configuration-standalone.nix /etc/nixos/configuration.nix
sudo bash setup-nixos.sh
sudo nixos-rebuild switch
```

## Files

| File | Description |
|------|-------------|
| `flake.nix` | Flake definition: packages, inputs, dev shell |
| `configuration.nix` | NAS config (Flake version, receives packages via `specialArgs`) |
| `configuration-standalone.nix` | NAS config (standalone, packages defined inline) |
| `disko.nix` | Declarative disk partitioning: ZFS pools and datasets on root |
| `module.nix` | Core NixOS module: all packages, services, users, and firewall rules |
| `installer.nix` | Offline-capable bootable installer ISO NixOS configuration |
| `ha.nix` | High Availability module: Patroni, etcd, and HAProxy for PostgreSQL failover |
| `ota-module.nix` | OTA update module: installs the update script and health-check systemd units |
| `impermanence.nix` | Impermanence layer: declares which paths persist across reboots (ZFS-backed) |
| `console-network-wizard.nix` | Interactive static-IP console TUI for when DHCP is not available at install time |
| `dplane-generated.nix` | JSON-to-Nix bridge: static file written once by the installer, never modified by the daemon |
| `hardware-configuration.nix` | Auto-generated hardware config; overridden by the installer for target hardware |
| `patroni-witness.nix` | Minimal config for a third-node Patroni/etcd quorum witness (e.g. Raspberry Pi) |
| `modules/samba.nix` | Samba integration NixOS module: dynamic share management via daemon |
| `setup-nixos.sh` | Setup helper: generates host ID, detects boot loader, patches flake.nix |
| `install.sh` | First-boot installer script: partitions, formats, and installs DPlaneOS to disk |
| `ota-update.sh` | OTA update shell script: A/B slot swap, health check, and auto-revert logic |
| `NIXOS-INSTALL-GUIDE.md` | Complete step-by-step guide for beginners |
| `NIXOS-README.md` | Technical reference: rollback, licensing, impermanence, advanced options |

## Documentation

### Installation and operation

| Document | What it covers |
|----------|---------------|
| [Installation Guide](../docs/admin/INSTALLATION-GUIDE.md) | System requirements, ISO install flow, post-install checklist |
| [NixOS Install Guide](NIXOS-INSTALL-GUIDE.md) | Step-by-step for NixOS beginners: from empty hardware to running NAS |
| [Administrator Guide](../docs/admin/ADMIN-GUIDE.md) | Users, roles, storage management, containers, LDAP/AD, security practices |
| [Backup and Replication](../docs/admin/BACKUP-REPLICATION.md) | ZFS snapshots, ZFS Send/Receive, Cloud Sync, Cold Tier, rsync, database backup |
| [High Availability](../docs/admin/HIGH-AVAILABILITY.md) | HA cluster setup (Patroni, etcd, Keepalived, STONITH), failover, rolling upgrades |
| [OTA Updates](../docs/admin/OTA-UPDATES.md) | A/B slot system, health check, auto-revert, manual rollback, HA rolling upgrades |
| [Optional Protocols](../docs/admin/OPTIONAL-PROTOCOLS.md) | iSCSI, NVMe-oF, FTP/FTPS, MinIO S3-compatible object store |
| [Alerts and Authentication](../docs/admin/ALERTS.md) | SMTP, webhook, Telegram alerting; TOTP 2FA setup and backup codes |
| [Troubleshooting](../docs/admin/TROUBLESHOOTING.md) | Build failures, ZFS issues, DB init race, ZED setup |
| [Recovery Guide](../docs/admin/RECOVERY.md) | Database restore, admin lockout, ZFS recovery, rollback procedure |

### Reference

| Document | What it covers |
|----------|---------------|
| [Design Philosophy](../docs/reference/PHILOSOPHY.md) | Why DPlaneOS works the way it does: four core principles, design decisions |
| [Architecture](../docs/reference/ARCHITECTURE.md) | Three-layer model, persistence, single-node and HA architecture, data flow |
| [GitOps Reference](../docs/reference/GITOPS-REFERENCE.md) | state.yaml format, reconciliation engine, drift detection, Capture workflow |
| [NixOS Rationale](../docs/reference/NIXOS-RATIONALE.md) | NixOS primitives DPlaneOS relies on: impermanence, disko, A/B OTA, Patroni |
| [Porting Guide](../docs/reference/PORTING-GUIDE.md) | Forking DPlaneOS for other Linux distributions - what it takes, what you lose |
| [Changelog](../docs/reference/CHANGELOG.md) | Full version history |
| [Error Reference](../docs/reference/ERROR-REFERENCE.md) | Every HTTP error code the API returns, with cause and fix |
| [Showstopper Mitigation Guide](../docs/reference/SHOWSTOPPER-MITIGATION-GUIDE.md) | Honest assessment of HA limits, binary-trust, resolved vs open issues |
| [Threat Model](../docs/reference/THREAT-MODEL.md) | Security architecture, threat scenarios, mitigations, residual risks |
| [Dependencies](../docs/reference/DEPENDENCIES.md) | All bundled Go and frontend deps, system requirements, build instructions |

### Hardware

| Document | What it covers |
|----------|---------------|
| [Hardware Compatibility](../docs/hardware/HARDWARE-COMPATIBILITY.md) | Supported CPUs, RAM, disk types, network, RAID controllers |
| [Non-ECC RAM Warning](../docs/hardware/NON-ECC-WARNING.md) | Why ZFS + non-ECC is risky, probability analysis, mitigations |

## System Requirements

- NixOS 25.11 (stable)
- Minimum 8 GB RAM (more RAM = larger ZFS ARC read cache)
- Separate boot disk (SSD) + data disks (HDD/SSD for ZFS pool)
- Network connection

## What's Included

| Component | Details |
|-----------|---------|
| **ZFS** | Auto-import, monthly scrub, auto-snapshots (15min/hourly/daily/weekly/monthly) |
| **DPlaneOS Daemon** | systemd service, OOM-protected (1 GB), **path-agnostic execution**, hardened (ProtectSystem=strict) |
| **nginx** | Reverse proxy, security headers, PHP blocked |
| **Docker** | ZFS storage driver, weekly prune |
| **Samba** | Performance-tuned, dynamic shares via daemon |
| **NFS** | Server enabled |
| **S.M.A.R.T.** | Automatic disk health monitoring |
| **Firewall** | Only ports 80, 443, 445, 2049 open |
| **mDNS** | NAS discoverable as `dplaneos.local` |
| **SSH** | Password login for initial setup, SSH keys recommended after |
| **Database** | Automated PostgreSQL HA management via Patroni/etcd |
