# Why DPlaneOS is NixOS-Exclusive

DPlaneOS is not a daemon that happens to run on NixOS. It is a NixOS appliance - the operating system itself is part of the product. This document explains which NixOS primitives DPlaneOS relies on and why they make a storage operating system more reliable.

---

## The Core Claim

A NAS is only as trustworthy as the system underneath it. Traditional Linux distributions leave most of that system undefined: packages are installed imperatively, configuration files accumulate over time, and the exact system state that was running six months ago is unrecoverable. NixOS removes this class of problem by building the entire system from a declaration.

Every component DPlaneOS builds on - ZFS, PostgreSQL, Samba, Docker, the firewall, systemd units, kernel modules, kernel parameters - is declared in the flake and built the same way for every machine of a release.

---

## NixOS Primitives DPlaneOS Uses

### Declarative System State

The OS is expressed as code (`flake.nix`, `nixos/module.nix`, `nixos/configuration-standalone.nix` and the other modules in `nixos/`). An installed system is built from `/etc/nixos/flake.nix`: the DPlaneOS release plus the machine's `hardware-configuration.nix` and an optional `local.nix`. There are no imperative installation steps, and a rebuild from the same inputs produces the same system.

For a NAS operator this means: the system itself is the release; what makes a machine yours is its state on `/persist` (and a configuration backup of it on a pool) and your ZFS pools.

### `/persist`: state on its own partition

The system partition (`/`, ext4) holds the Nix store and the generations. All state lives on `/persist` (ext4), bind-mounted into place at boot by the [impermanence](https://github.com/nix-community/impermanence) module from `nixos/impermanence.nix`. The root is not wiped; the split means the system partition can be reinstalled, and every generation boots with the same state.

**Kept on `/persist`** (each at the same path below `/persist`):

| Path | Contents | Why persisted |
|------|----------|---------------|
| `/var/lib/dplaneos` | PostgreSQL data, secrets key, audit HMAC key, settings, stacks, Git clone | Core application state - losing this means losing all users, settings and history |
| `/var/lib/docker` | Container state, images, overlay2 volumes | Containers and their data must survive reboots |
| `/var/lib/samba` | passdb.tdb, secrets.tdb | Samba's own state; losing it breaks SMB authentication until passwords are set again |
| `/var/lib/nfs` | NFS server state | Clients hold stateful leases; losing state causes stale mounts |
| `/etc/target` | iSCSI (LIO) configuration | Restored into configfs at every boot |
| `/var/lib/NetworkManager`, `/var/lib/systemd/network` | DHCP lease files | Stable addresses on networks that honour client IDs |
| `/var/lib/avahi-daemon` | mDNS state | |
| `/var/lib/nixos` | UID/GID allocation map | File ownership stays stable across rebuilds |
| `/etc/dplaneos` | Schedules, certificates, ACME account, rclone remotes | Written by the daemon at runtime |
| `/var/log/dplaneos`, `/var/log/samba` | Logs | Operational visibility and audit continuity |
| `/etc/machine-id` | Stable system identity | journald and the SCSI-3 PR key derive from it |
| `/etc/ssh/ssh_host_*` | SSH host keys | No "host key changed" warnings after a reinstall of the system partition |
| `/persist/etc/hostid` | The machine's own ZFS host ID | ZFS multihost and the protection against a second import of a pool need a unique, non-zero ID; installed as `/etc/hostid` at every activation |

### Flakes and Pinned Inputs

The lockfile (`flake.lock`) pins every input: nixpkgs, disko, impermanence. Every machine of a release runs the same store paths. This means:

- The ISO you download was built from the same inputs as the system you can build locally (`nix build .#iso`), and the daemon's store path can be compared with your own build ([SHOWSTOPPER-MITIGATION-GUIDE.md](SHOWSTOPPER-MITIGATION-GUIDE.md#mitigation-build-from-source))
- Updates are predictable: you know exactly which package versions are changing
- Going back to a previous release is going back to its generation

### disko - Declarative Disk Layout

`nixos/disko.nix` declares the partition layout of the system disk: the ESP (512 MB), the system partition (40 GB, or 24 GB on disks under 64 GB) and `/persist` (the rest). The installer partitions with it; it does not prompt for sizes or file systems. Data disks are never touched.

### Updates through Generations

`nixos/ota-module.nix` and the daemon's Updates page build the next release as a new NixOS generation, reboot into it and check the system (daemon, nginx, database, pools, Samba). If the check fails, the previous generation becomes the boot default again and the machine reboots into it. Every older generation also stays selectable in the boot menu.

This is only possible because NixOS generations are immutable store paths: there is no partially upgraded state. See [OTA-UPDATES.md](../admin/OTA-UPDATES.md).

### NixOS Module System

`nixos/module.nix` declares `services.dplaneos` as a NixOS option namespace with type-checked options and assertions: the daemon and frontend packages, the database, the memory cap, Samba, NFS, FTP, S3, UPS, the cluster and fencing services, the Cold Tier path and the update check. Settings made in the web UI that NixOS applies go through a JSON file the release reads (`nixos/dplane-generated.nix`); your own NixOS settings go in `/etc/nixos/local.nix`.

### Nix Store and Content-Addressed Binaries

All installed software lives in `/nix/store/<hash>-<name>`. Two versions of the same package coexist without conflict. Going back to a previous release is a boot entry, not an uninstall/reinstall procedure. The production daemon is a glibc build linked against libzfs from the same nixpkgs revision as the kernel module, so the library and the module always match.

### ZFS Integration

NixOS has first-class ZFS support: `boot.supportedFilesystems`, `boot.zfs.package`, `services.zfs.autoScrub`, `services.zfs.trim`, ZED configuration - all declared. DPlaneOS uses the Linux 6.12 LTS kernel and OpenZFS 2.4 from nixpkgs, with assertions that fail the build if either falls below the supported versions:

```nix
assertions = [
  { assertion = lib.versionAtLeast config.boot.kernelPackages.kernel.version "6.12"; }
  { assertion = lib.versionAtLeast config.boot.zfs.package.version "2.3"; }
];
```

A misconfigured ZFS/kernel combination is a build error, not a runtime surprise.

---

## What You Cannot Have Without NixOS

| Capability | NixOS | Traditional Linux |
|------------|-------|-------------------|
| Atomic OS updates with automatic rollback | Yes - NixOS generations | No - partial upgrades possible |
| System built from version-controlled code | Yes - the flake | No - imperative drift accumulates |
| Comparable builds (ISO = local build) | Yes - content-addressed store | No - mirrors diverge, build dates differ |
| State separated from the system | Yes - `/persist` bind mounts | Requires a hand-made setup |
| Declarative disk layout | Yes - disko | No - manual fdisk/mkfs |
| Type-checked configuration with assertions | Yes - NixOS module system | No - wrong config discovered at runtime |
| Kernel/ZFS version enforcement at build time | Yes - assertions | No - package manager resolves at install time |

---

## The Feedback Loop

Building DPlaneOS as a NixOS appliance imposes a discipline that benefits users directly: every feature must work within the NixOS module system, every system dependency must be declared, and every update goes through the same mechanism. Features that require manual system intervention cannot exist. This forces the right design choices rather than leaving them as optional best practices.
