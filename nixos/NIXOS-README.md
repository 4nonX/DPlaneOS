# DPlaneOS on NixOS : The Immutable NAS

NixOS is a **first-class platform** for DPlaneOS. The combination gives you
a deeply coherent approach to NAS reliability:

| Layer | Technology | What it means |
|-------|-----------|--------------|
| System | NixOS | Declarative, reproducible, rollback with one command |
| Data | ZFS | Snapshots, checksums, encryption, compression |
| Containers | GitOps | Docker stacks version-controlled in Git repos |

Every piece of your NAS state is either **declarative** (Nix), **snapshotted** (ZFS),
or **version-controlled** (Git). Recovering from a bad change is a one-command operation at every layer.

## Quick Start

There are two ways to get DPlaneOS running on NixOS:

### Option A: Boot the ISO (recommended for new installs)

Download or build the installer ISO, flash it to a USB stick, and boot it.
The live environment runs DPlaneOS immediately: web UI, recovery tools,
everything. From there, run `dplaneos-install` to install to disk.

```bash
# Build the ISO yourself (requires Nix with flakes enabled)
git clone https://github.com/4nonX/DPlaneOS
cd DPlaneOS/nixos
nix build .#nixosConfigurations.dplaneos-iso.config.system.build.isoImage

# Flash to USB
sudo dd if=result/iso/dplaneos-3.0.0-x86_64-linux.iso of=/dev/sdX bs=4M status=progress
```

Boot the USB. The web UI is at `http://dplaneos.local` (or the IP shown
on screen). To install permanently, run `dplaneos-install` from the terminal.

The ISO also works as a recovery disk: boot it to mount and repair
existing ZFS pools.

### Option B: Install on existing NixOS

If you already have NixOS running:

```bash
git clone https://github.com/4nonX/DPlaneOS
cd DPlaneOS/nixos
sudo bash setup-nixos.sh
sudo nixos-rebuild switch --flake .#dplaneos
```

> **Note:** The `#dplaneos` suffix is required. Without it, `nixos-rebuild` tries
> to find a configuration named after your hostname (e.g. `nixos` on a fresh VM),
> which does not exist in this flake.

`setup-nixos.sh` auto-detects your ZFS pools, timezone, and boot loader.

## License

DPlaneOS is licensed under the **GNU Affero General Public License v3.0 (AGPLv3)**.

This means:

- You **can** use it for free, forever, on any hardware you own
- You **can** modify the source code
- You **can** distribute copies (with the license and source attached)
- If you run a **modified version as a network service**, you must make your
  modified source available to users of that service

AGPLv3 is an OSI-approved open-source license. **NixOS correctly recognises it
as free software** - no `allowUnfreePredicate` configuration is needed.

Contributors sign a CLA before their first pull request is merged.
See `CLA-INDIVIDUAL.md` (individuals) and `CLA-ENTITY.md` (organisations).

## Update

```bash
cd DPlaneOS/nixos
git pull
sudo nixos-rebuild switch --flake .#dplaneos
```

## Rollback

Something broke? One command:

```bash
sudo nixos-rebuild switch --rollback
```

Or pick a specific generation:

```bash
nixos-rebuild list-generations
sudo nixos-rebuild switch --generation 42
```

## What's Included

The NixOS configuration provides:

- **ZFS** - pools, auto-scrub, auto-snapshots (15min/hourly/daily/weekly/monthly)
- **High Availability (HA)** - Patroni, etcd, and HAProxy integration for seamless PostgreSQL failover (`ha.nix`)
- **Samba** - SMB file sharing with performance tuning
- **NFS** - Unix/Linux file sharing
- **Docker** - native ZFS storage driver, weekly auto-prune
- **Docker-ZFS boot gate** - Docker waits for ZFS pools before starting
- **SMART monitoring** - disk health with wall notifications
- **Avahi** - `dplaneos.local` mDNS discovery
- **Nginx** - reverse proxy with 7-day WebSocket timeout
- **Firewall** - HTTP, SMB, NFS, SSH ports open
- **Daily DB backups** - systemd timer with 30-day retention
- **Recovery CLI** - `sudo dplaneos-recovery`
- **Removable media** - udev rules for USB device detection

## Vendor Hash (First Build)

Nix requires a hash of Go dependencies. On first build, you may see:

```
hash mismatch in fixed-output derivation:
  got: sha256-AbCdEf1234...=
```

Copy that hash into `flake.nix` replacing `vendorHash = null;` and rebuild.
`setup-nixos.sh` attempts to do this automatically.

## Files

| File | Purpose |
|------|---------|
| `flake.nix` | Nix flake - builds dplaned, frontend, recovery CLI |
| `configuration.nix` | Full NixOS system config (flake version) |
| `configuration-standalone.nix` | Standalone version (no flake) |
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
| `setup-nixos.sh` | Interactive setup helper: generates host ID, detects boot loader, patches flake.nix |
| `install.sh` | First-boot installer script: partitions, formats, and installs DPlaneOS to disk |
| `witness-installer.nix` | Standalone witness-only ISO NixOS config (advanced; built with `nix build .#iso-witness`) |
| `witness-setup.sh` | Witness node setup wizard: collects IPs, partitions disk, installs NixOS with etcd |
| `ota-update.sh` | OTA update shell script: A/B slot swap, health check, and auto-revert logic |
| `README.md` | NixOS installation paths overview: ISO, Flake, and standalone |
| `NIXOS-INSTALL-GUIDE.md` | Complete step-by-step install guide for NixOS beginners |

## Why NixOS for a NAS?

- **Atomic upgrades**: System updates are all-or-nothing. No partial states.
- **Generations**: Every config change creates a bootable snapshot. Pick any one.
- **Reproducible**: Same flake.nix = same system, anywhere, anytime.
- **Git-native**: Your entire NAS config is a Git repo. `git log` is your changelog.
- **No drift**: The system *is* the config file. Nothing else.

For the full explanation of how DPlaneOS leverages each NixOS primitive (impermanence, disko, A/B OTA, Patroni HA, NixOS module system), see [NIXOS-RATIONALE.md](../docs/reference/NIXOS-RATIONALE.md).

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
