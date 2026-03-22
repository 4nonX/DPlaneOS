# D-PlaneOS on NixOS : The Immutable NAS

NixOS is a **first-class platform** for D-PlaneOS. The combination gives you
a deeply coherent approach to NAS reliability:

| Layer | Technology | What it means |
|-------|-----------|--------------|
| System | NixOS | Declarative, reproducible, rollback with one command |
| Data | ZFS | Snapshots, checksums, encryption, compression |
| Containers | GitOps | Docker stacks version-controlled in Git repos |

Every piece of your NAS state is either **declarative** (Nix), **snapshotted** (ZFS),
or **version-controlled** (Git). Recovering from a bad change is a one-command operation at every layer.

## Quick Start

There are two ways to get D-PlaneOS running on NixOS:

### Option A: Boot the ISO (recommended for new installs)

Download or build the installer ISO, flash it to a USB stick, and boot it.
The live environment runs D-PlaneOS immediately: web UI, recovery tools,
everything. From there, run `dplaneos-install` to install to disk.

```bash
# Build the ISO yourself (requires Nix with flakes enabled)
git clone https://github.com/4nonX/D-PlaneOS
cd D-PlaneOS/nixos
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
git clone https://github.com/4nonX/D-PlaneOS
cd D-PlaneOS/nixos
sudo bash setup-nixos.sh
sudo nixos-rebuild switch --flake .#dplaneos
```

`setup-nixos.sh` auto-detects your ZFS pools, timezone, and boot loader.

## License

D-PlaneOS is licensed under the **GNU Affero General Public License v3.0 (AGPLv3)**.

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
cd D-PlaneOS/nixos
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

The NixOS configuration sets up everything the Debian installer does:

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
| `setup-nixos.sh` | Interactive setup helper |

## Why NixOS for a NAS?

- **Atomic upgrades**: System updates are all-or-nothing. No partial states.
- **Generations**: Every config change creates a bootable snapshot. Pick any one.
- **Reproducible**: Same flake.nix = same system, anywhere, anytime.
- **Git-native**: Your entire NAS config is a Git repo. `git log` is your changelog.
- **No drift**: The system *is* the config file. Nothing else.
