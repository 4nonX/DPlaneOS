# Why DPlaneOS is NixOS-Exclusive

DPlaneOS is not a daemon that happens to run on NixOS. It is a NixOS appliance - the operating system itself is part of the product. This document explains which NixOS primitives DPlaneOS relies on and why they make storage operating systems categorically more reliable.

---

## The Core Claim

A NAS is only as trustworthy as the system underneath it. Traditional Linux distributions leave most of that system undefined: packages are installed imperatively, configuration files accumulate over time, and the exact system state that was running six months ago is unrecoverable. NixOS eliminates this class of problem by making the entire system declarative.

Every component DPlaneOS builds on - ZFS, PostgreSQL, Samba, Docker, the firewall, systemd units, kernel modules, kernel parameters - is declared in the flake and reproduced exactly on every build. The same `git clone` on different hardware produces the same running system.

---

## NixOS Primitives DPlaneOS Uses

### Declarative System State

The entire OS is expressed as code (`nixos/configuration.nix`, `nixos/module.nix`, `flake.nix`). There are no imperative installation steps. `nixos-rebuild switch` is idempotent - running it a hundred times on the same input produces the same state. Drift between declared and actual system state is impossible as long as rebuilds are applied.

For a NAS operator this means: if you can restore your `configuration.nix` and import your ZFS pools, your system is fully recovered. No reinstallation procedures, no "I think I had this package installed" uncertainty.

### Impermanence

`nixos/impermanence.nix` mounts `/` as a tmpfs that is wiped on every reboot. Only explicitly declared paths under `/persist/` survive across reboots. This means:

- Accumulated cruft, stale config fragments, and half-applied changes cannot persist
- Every boot starts from a known-good declared state
- The difference between "what was declared" and "what is running" is always zero after a reboot

DPlaneOS persists exactly what it needs: daemon database and keys, PostgreSQL data, Docker container/image/volume state, Samba and NFS state, network leases, gitops state, logs, and system identity files (machine-id, SSH host keys, ZFS hostid). Everything else is ephemeral.

**What survives every reboot** (`/persist` bind-mounts, declared in `nixos/impermanence.nix`):

| Path | Contents | Why persisted |
|------|----------|---------------|
| `/var/lib/dplaneos` | PostgreSQL data, daemon DB, audit HMAC key, gitops repo | Core application state - losing this means losing all users, pools config, history |
| `/var/lib/docker` | Container state, images, overlay2 volumes | Containers and their data must survive reboots; without this every image re-pulls on boot |
| `/var/lib/samba` | passdb.tdb, secrets.tdb, Samba user accounts | Samba passwords are stored in tdb files separate from the system user database; losing them breaks all SMB share access |
| `/var/lib/nfs` | NFS export state, client lease tracking | NFS clients hold stateful leases; losing state causes stale mounts on the client side |
| `/var/lib/NetworkManager` | DHCP lease files | Helps maintain stable DHCP addresses on networks that honour client-id across reboots |
| `/var/lib/systemd/network` | systemd-networkd lease files | Same reason as above for networkd-managed interfaces |
| `/var/lib/avahi-daemon` | mDNS host database | Avahi caches discovered hostnames; losing it causes a re-discovery flood on boot |
| `/var/lib/nixos` | UID/GID allocation map | NixOS allocates UIDs/GIDs dynamically; persisting the map ensures file ownership is stable across rebuilds |
| `/etc/dplaneos` | smb-shares.conf, rclone config, daemon config fragments | The daemon writes share and protocol config here at runtime; losing it reverts all share definitions |
| `/var/log/dplaneos` | Daemon operational and audit logs | Audit log continuity is a compliance requirement; HMAC chain must be preserved |
| `/var/log/samba` | Samba access logs | Operational visibility and audit continuity |
| `/etc/machine-id` | Stable system identity | journald uses this as a cursor anchor; without it log forwarding and rotation break across reboots |
| `/etc/ssh/ssh_host_*` | SSH host keys (RSA + Ed25519) | Without stable host keys, every OTA update to a new slot triggers "host key changed" warnings on every SSH client |
| `/etc/hostid` | ZFS host identifier | ZFS uses this to detect pool ownership; a mismatch causes "pool was last used by another system" errors after an A/B slot swap |

**What does not survive reboots** (intentionally ephemeral):

| Path | Why ephemeral |
|------|---------------|
| `/tmp`, `/run`, `/var/run` | Standard POSIX contract - these are always tmpfs; no service should persist state here |
| `/var/cache` | Package caches and build artefacts - fully reproducible from the Nix store, so caching across reboots provides no correctness benefit |
| Package manager state | DPlaneOS uses Nix: packages are content-addressed store paths installed at activation time, not imperatively tracked state |
| Unmanaged `/etc` fragments | Any config file not explicitly listed is ephemeral - this is intentional. It enforces that all configuration goes through either NixOS declarations or the daemon's own persisted paths, with no hidden hand-edited files accumulating over time |
| `/var/lib/*` for services not listed above | Services not listed have no runtime state worth preserving, or their state is regenerated correctly at startup (e.g. nginx pid files, systemd transient units) |
| Core system binaries and libraries | These live in `/nix/store` and are always reinstated from the closure at activation - they cannot drift between rebuilds |

### Flakes and Reproducible Builds

The flake lockfile (`flake.lock`) pins every input: nixpkgs revision, disko, impermanence. Two builds from the same lockfile are byte-for-byte identical (modulo timestamps). This means:

- The ISO you download was built from the same inputs as the system you can build locally
- Upgrades are predictable: you know exactly which package versions are changing
- Rolling back to a previous build is equivalent to rolling back the lockfile

### disko - Declarative Disk Layout

`nixos/disko.nix` declares the partition layout of the boot disk: ESP, swap, and ZFS root pool. `nixos-install` uses disko to partition and format before installing, so the physical disk layout is also version-controlled and reproducible.

The installer does not prompt for partition sizes or filesystem choices. The disk layout matches what is declared - exactly.

### A/B OTA Upgrades

`nixos/ota-module.nix` implements atomic over-the-air upgrades using systemd-boot's boot counting and the NixOS generation system. An upgrade:

1. Fetches the new NixOS closure from the update source
2. Writes it to the inactive boot slot (the previous generation stays untouched)
3. Reboots into the new slot
4. Runs a post-boot health check (daemon API, ZFS pool health, database, Samba)
5. If the health check fails, automatically reverts to the previous slot and reboots

This is only possible because NixOS generations are immutable store paths. There is no "partially upgraded" state. Either the new closure boots successfully or the previous one does.

### NixOS Module System

`nixos/module.nix` declares `services.dplaneos` as a proper NixOS option namespace. Operators configure DPlaneOS the same way they configure any other NixOS service - in `configuration.nix`, with type-checked options, assertions, and `nixos-rebuild` applying the result atomically. There are no config files to hand-edit, no service restart scripts to run.

Options include: listen address/port, database DSN, SSH authorized keys, NVIDIA GPU support, Cold Tier mount path, HA clustering, and OTA health-check timing.

### Nix Store and Content-Addressed Binaries

All installed software lives in `/nix/store/<hash>-<name>`. Two versions of the same package coexist without conflict. Rolling back a system-level package upgrade is a bootloader entry change, not an uninstall/reinstall procedure. The daemon binary is a static musl build linked against the pinned nixpkgs revision - it cannot silently pick up a different libc version on a system update.

### ZFS Integration

NixOS has first-class ZFS support: `boot.supportedFilesystems`, `boot.zfs.package`, `services.zfs.autoScrub`, `services.zfs.trim`, ZED (ZFS Event Daemon) configuration - all declared. DPlaneOS pins the kernel to Linux 6.6 LTS and ZFS to the 2.3 LTS branch, with NixOS assertions that fail the build if either pin drifts:

```nix
assertions = [
  { assertion = lib.versionAtLeast config.boot.kernelPackages.kernel.version "6.6"; }
  { assertion = lib.versionAtLeast config.boot.zfs.package.version "2.3"; }
];
```

A misconfigured ZFS/kernel combination is a build error, not a runtime surprise.

---

## What You Cannot Have Without NixOS

| Capability | NixOS | Traditional Linux |
|------------|-------|-------------------|
| Atomic OS upgrades with automatic rollback | Yes - NixOS generations | No - partial upgrades possible |
| System state fully in version control | Yes - `configuration.nix` + flake | No - imperative drift accumulates |
| Reproducible builds (ISO = local build) | Yes - content-addressed store | No - mirrors diverge, build dates differ |
| Impermanent `/` - no boot-to-boot drift | Yes - tmpfs root + persist | Requires complex overlay setup |
| Declarative disk layout | Yes - disko | No - manual fdisk/mkfs |
| Type-checked configuration with assertions | Yes - NixOS module system | No - wrong config discovered at runtime |
| Kernel/ZFS version pinning with build-time enforcement | Yes - assertions in flake | No - package manager resolves at install time |

---

## The Feedback Loop

Building DPlaneOS as a NixOS appliance imposes a discipline that benefits users directly: every feature must work within the NixOS module system, every system dependency must be declared, and every upgrade path must go through the OTA mechanism. Features that require manual system intervention cannot exist. This forces the right design choices rather than leaving them as optional best practices.
