# D-PlaneOS on NixOS — The Immutable NAS

NixOS is a **first-class platform** for D-PlaneOS. The combination gives you
something no other NAS can offer:

| Layer | Technology | What it means |
|-------|-----------|--------------|
| System | NixOS | Declarative, reproducible, rollback with one command |
| Data | ZFS | Snapshots, checksums, encryption, compression |
| Containers | GitOps | Docker stacks version-controlled in Git repos |

Every piece of your NAS state is either **declarative** (Nix), **snapshotted** (ZFS),
or **version-controlled** (Git). Nothing is ever lost.

## Quick Start

There are two ways to get D-PlaneOS running on NixOS:

### Option A: Boot the ISO (recommended for new installs)

Download or build the installer ISO, flash it to a USB stick, and boot it.
The live environment runs D-PlaneOS immediately — web UI, recovery tools,
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

The ISO also works as a recovery disk — boot it to mount and repair
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

## A Note on Licensing and Nix "Unfree"

D-PlaneOS is licensed under **PolyForm Shield 1.0.0**. This means:

- You **can** use it for free, forever, on any hardware you own
- You **can** modify the source code and contribute changes
- You **can** distribute copies (with the license attached)
- You **cannot** use it to build a competing commercial NAS product

This is not an OSI-approved open-source license. It exists to prevent large
companies from rebranding a solo developer's work as their own commercial
product. The code is fully source-available and you can read every line.

**What this means on NixOS:** Nix classifies non-OSI licenses as "unfree."
The D-PlaneOS flake handles this automatically — it marks only the three
D-PlaneOS packages (dplaned, frontend, recovery CLI) as unfree via
`allowUnfreePredicate`. All system packages (ZFS, Samba, Docker, nginx, etc.)
remain under their own open-source licenses and are unaffected.

If your system-wide Nix config has `allowUnfree = false` (the default), the
flake still works because the predicate is scoped to the flake's own package
set. You do not need to change your global Nix settings.

**If you hit an "unfree" error anyway**, add this to your system
`/etc/nixos/configuration.nix`:

```nix
nixpkgs.config.allowUnfreePredicate = pkg:
  builtins.elem (lib.getName pkg) [ "dplaned" "dplaneos-frontend" "dplaneos-recovery" ];
```

This allows only D-PlaneOS packages through. Nothing else changes.

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

- **ZFS** — pools, auto-scrub, auto-snapshots (15min/hourly/daily/weekly/monthly)
- **Samba** — SMB file sharing with performance tuning
- **NFS** — Unix/Linux file sharing
- **Docker** — native ZFS storage driver, weekly auto-prune
- **Docker-ZFS boot gate** — Docker waits for ZFS pools before starting
- **SMART monitoring** — disk health with wall notifications
- **Avahi** — `dplaneos.local` mDNS discovery
- **Nginx** — reverse proxy with 7-day WebSocket timeout
- **Firewall** — HTTP, SMB, NFS, SSH ports open
- **Daily DB backups** — systemd timer with 30-day retention
- **Recovery CLI** — `sudo dplaneos-recovery`
- **Removable media** — udev rules for USB device detection

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
| `flake.nix` | Nix flake — builds dplaned, frontend, recovery CLI |
| `configuration.nix` | Full NixOS system config (flake version) |
| `configuration-standalone.nix` | Standalone version (no flake) |
| `setup-nixos.sh` | Interactive setup helper |

## Why NixOS for a NAS?

- **Atomic upgrades**: System updates are all-or-nothing. No partial states.
- **Generations**: Every config change creates a bootable snapshot. Pick any one.
- **Reproducible**: Same flake.nix = same system, anywhere, anytime.
- **Git-native**: Your entire NAS config is a Git repo. `git log` is your changelog.
- **No drift**: The system *is* the config file. Nothing else.
