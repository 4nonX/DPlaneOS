# DPlaneOS on NixOS

DPlaneOS is a NixOS appliance: the whole system, from the boot loader to the shares, is built from the flake in this repository.

**License:** [GNU Affero General Public License v3.0 (AGPLv3)](https://www.gnu.org/licenses/agpl-3.0.html). See [NIXOS-README.md](NIXOS-README.md#license) for how this works with Nix's unfree package handling (short version: nothing to configure).

## Ways to run it

### Install to disk (the installer ISO)

Download `dplaneos-v<version>-installer-amd64.iso` (or `-arm64`) from the [releases](https://github.com/4nonX/DPlaneOS/releases/latest), write it to a USB stick and boot it in UEFI mode. The installer asks for the system disk and the admin password and installs offline. See the [Installation Guide](../docs/admin/INSTALLATION-GUIDE.md).

The installed system is built from `/etc/nixos/flake.nix`, which the daemon writes: the DPlaneOS release plus `hardware-configuration.nix` and, if present, your own `local.nix`. Updates come from the Updates page; see [Updates](../docs/admin/OTA-UPDATES.md).

### Run it from USB (the live ISO)

`dplaneos-v<version>-live-x86_64.iso` (or `-arm64`) boots straight into D-PlaneOS in RAM: the web interface, the database and existing ZFS pools (imported automatically). Nothing is installed; with an ext4 drive labelled `dplane-persist` attached the settings are kept. See the [Live Boot Quick Start](../docs/admin/LIVE-BOOT-QUICKSTART.md).

### Build it yourself

```bash
git clone https://github.com/4nonX/DPlaneOS && cd DPlaneOS
nix build .#iso          # installer ISO   (result/iso/*.iso)
nix build .#iso-live     # live ISO
nix build .#witness-qcow2-x86_64   # third-vote witness (VM image); also witness-image-x86_64, witness-sd-aarch64
```

### The NixOS module

`nixosModules.dplaneos` (`module.nix`) is the module the appliance is built from: the daemon, nginx, PostgreSQL, Samba, NFS, the firewall and the cluster services. It needs the packages set (`services.dplaneos.daemonPackage`, `frontendPackage`; the flake exports `dplaneos-daemon-cgo` and `dplaneos-frontend`). The VM tests use it this way. A DPlaneOS system on top of a NixOS configuration of your own is not tested as such; the appliance (`nixosConfigurations.dplaneos`) is.

## Files

| File | Description |
|------|-------------|
| `../flake.nix` | The flake: the appliance, ISO, live and witness configurations, packages and checks (VM tests) |
| `module.nix` | Core NixOS module: daemon, web server, database, services, firewall |
| `configuration-standalone.nix` | Base configuration of the appliance (boot loader, kernel, ZFS, network defaults) |
| `configuration-live.nix` | The live system (runs in RAM; persistence drive optional) |
| `disko.nix` | Partition layout of the system disk: ESP, system, `/persist` |
| `impermanence.nix` | What lives on `/persist` and is bound into place at boot; the machine's own ZFS host ID |
| `ota-module.nix` | Updates: the check after the reboot and the rollback to the previous generation |
| `dplaneos-update-health.sh` | The check after an update's reboot (run by ota-module.nix) |
| `dplane-generated.nix` | JSON-to-Nix bridge: reads `/var/lib/dplaneos/dplane-state.json` (written by the daemon) at build time |
| `installer.nix`, `installer-tools.nix`, `install.sh` | The installer ISO and its installer |
| `live-iso.nix`, `live-persistence.nix`, `live-zfs-auto-import.nix` | The live ISO, its persistence drive, pool import at boot |
| `console-network-wizard.nix` | Console tool for a static address when there is no DHCP |
| `witness.nix`, `witness-image-x86.nix`, `witness-image-pi.nix` | The third-vote witness and its images |
| `modules/` | Samba, NFS, cluster (Corosync, QDevice), fencing, UPS, CTDB |
| `hardware-configuration.nix` | Placeholder; an installed system uses the one the installer generated |
| `tests/` | The VM tests (`nix build .#checks.x86_64-linux.<name> -L`) |
| `NIXOS-INSTALL-GUIDE.md` | Step-by-step guide for people new to NixOS |
| `NIXOS-README.md` | Technical reference: generations and rollback, `/persist`, your own NixOS settings |

## Documentation

### Installation and operation

| Document | What it covers |
|----------|---------------|
| [Installation Guide](../docs/admin/INSTALLATION-GUIDE.md) | System requirements, ISO install flow, post-install checklist |
| [NixOS Install Guide](NIXOS-INSTALL-GUIDE.md) | Step-by-step for NixOS beginners: from empty hardware to running NAS |
| [Administrator Guide](../docs/admin/ADMIN-GUIDE.md) | Users, roles, storage management, containers, LDAP/AD, security practices |
| [Git-Driven NAS](../docs/admin/GITOPS-DRIVEN-NAS.md) | The configuration in a Git repository: setup, approval, holds, signatures, HA |
| [Backup and Replication](../docs/admin/BACKUP-REPLICATION.md) | ZFS snapshots, ZFS Send/Receive, Cloud Sync, Cold Tier, rsync, database backup |
| [High Availability](../docs/admin/HIGH-AVAILABILITY.md) | Clusters, the third vote, storage groups (shared or replicated), protection layers |
| [Updates](../docs/admin/OTA-UPDATES.md) | Release updates, the check after the reboot, automatic rollback, HA one node at a time |
| [Optional Protocols](../docs/admin/OPTIONAL-PROTOCOLS.md) | iSCSI, NVMe-oF, FTP/FTPS, MinIO S3-compatible object store |
| [Alerts and Authentication](../docs/admin/ALERTS.md) | SMTP, webhook, Telegram alerting; TOTP 2FA setup and backup codes |
| [Troubleshooting](../docs/admin/TROUBLESHOOTING.md) | Build failures, ZFS issues, ZED setup |
| [Recovery Guide](../docs/admin/RECOVERY.md) | Database restore, admin lockout, ZFS recovery, rollback procedure |

### Reference

| Document | What it covers |
|----------|---------------|
| [Features](../FEATURES.md) | Complete feature list by category |
| [Design Philosophy](../docs/reference/PHILOSOPHY.md) | Why DPlaneOS works the way it does: core principles, design decisions |
| [Architecture](../docs/reference/ARCHITECTURE.md) | Layers, persistence, single-node and HA architecture, data flow |
| [GitOps Reference](../docs/reference/GITOPS-REFERENCE.md) | `state.yaml` format and the reconciliation engine |
| [NixOS Rationale](../docs/reference/NIXOS-RATIONALE.md) | The NixOS primitives DPlaneOS relies on: `/persist`, disko, generations |
| [NixOS Technical Reference](NIXOS-README.md) | Generations and rollback, `/persist`, your own NixOS settings |
| [Porting Guide](../docs/reference/PORTING-GUIDE.md) | Forking DPlaneOS for other Linux distributions - what it takes, what you lose |
| [Changelog](../docs/reference/CHANGELOG.md) | Full version history |
| [Port Reference](../docs/reference/PORT-REFERENCE.md) | Every port the system listens on, its service, interface binding, and safe ranges for Docker containers |
| [Error Reference](../docs/reference/ERROR-REFERENCE.md) | HTTP error codes the API returns, with cause and fix |
| [Showstopper Mitigation Guide](../docs/reference/SHOWSTOPPER-MITIGATION-GUIDE.md) | Honest assessment of HA limits, binary trust, resolved vs open issues |
| [Threat Model](../docs/reference/THREAT-MODEL.md) | Security architecture, threat scenarios, mitigations, residual risks |
| [Dependencies](../docs/reference/DEPENDENCIES.md) | Bundled Go and frontend deps, system requirements, build instructions |
| [Pitch](../PITCH.md) | Git as control plane: the case for fleets |

### Hardware

| Document | What it covers |
|----------|---------------|
| [Hardware Compatibility](../docs/hardware/HARDWARE-COMPATIBILITY.md) | Supported CPUs, RAM, disk types, network, RAID controllers |
| [Non-ECC RAM Warning](../docs/hardware/NON-ECC-WARNING.md) | Why ZFS + non-ECC is risky, probability analysis, mitigations |

### Development

| Document | What it covers |
|----------|---------------|
| [Contributing](../CONTRIBUTING.md) | Dev setup, project structure, coding conventions, security requirements for PRs |
| [Codebase Diagram](../docs/development/CODEBASE-DIAGRAM.md) | Mermaid diagrams: system overview, daemon internals, request lifecycle, disk events, auth flow |
| [Design System](../app-react/.stitch/DESIGN.md) | Frontend design tokens, spacing grid, component patterns, anti-pattern list |

### Legal

| Document | What it covers |
|----------|---------------|
| [Notice](../NOTICE.md) | Third-party components, attributions, and licenses |
| [Security Policy](../SECURITY.md) | Vulnerability reporting, safe harbour, supported versions |
| [Individual CLA](../docs/legal/CLA-INDIVIDUAL.md) | Contributor license agreement for individuals |
| [Entity CLA](../docs/legal/CLA-ENTITY.md) | Contributor license agreement for organisations |

## What the appliance includes

| Component | Details |
|-----------|---------|
| **ZFS** | OpenZFS 2.4; monthly scrub of every pool (more schedules on the web interface); snapshot schedules set in the web interface; ZED events to the daemon |
| **DPlaneOS daemon** | `dplaned`, memory-capped (`MemoryMax=1G`, `services.dplaneos.memoryMax`), hardened (`NoNewPrivileges`, bounded capabilities; no mount namespace, so ZFS mounts stay visible to Samba and NFS) |
| **nginx** | Serves the web interface and proxies the API; security headers |
| **PostgreSQL 17** | Node-local database; configuration exchanged between paired nodes |
| **Docker** | overlay2 storage driver, weekly prune of unused images; stacks managed by the daemon |
| **Samba, NFS** | Shares managed by the daemon; WS-Discovery and mDNS for discovery |
| **S.M.A.R.T.** | Disk health and scheduled tests |
| **Firewall** | On: 22 (SSH), 80/443 (web), and the ports of enabled services (SMB, NFS, WS-Discovery, the cluster's third vote); rules from the Firewall page on top |
| **mDNS** | The NAS is discoverable as `<hostname>.local` (`dplaneos.local` by default) |
| **SSH** | Keys only (no password login, no root login); add keys under Identity › SSH Keys |
