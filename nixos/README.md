# The NixOS side of DPlaneOS

DPlaneOS is a NixOS system: the kernel, ZFS, Samba, PostgreSQL, nginx and everything else come from the NixOS release pinned in `flake.lock`, put together by the modules in this directory. It is delivered as an image. You do not need to know NixOS to use it, and an installed system is not changed with `nixos-rebuild`: settings are made in the web interface and take effect at once; the system itself is replaced as a whole by an update.

To install, see the [Installation Guide](../docs/admin/INSTALLATION-GUIDE.md). This page is for people who want to build the images, read the modules, or use them in a NixOS configuration of their own.

## How an installed system is put together

| Part | Where | Changes when |
|---|---|---|
| The system (kernel, services, the daemon, the web interface) | `/nix/store`, one generation per installed version, chosen by the boot loader (systemd-boot) | an update is installed |
| State: database, settings, keys, certificates, Samba and network configuration written by the daemon | `/persist` (a partition of its own), mounted to `/var/lib/dplaneos` and a few other places ([impermanence.nix](impermanence.nix)) | you change something in the web interface |
| Data | ZFS pools on the other disks | you use the NAS |

Settings that on an ordinary NixOS system are part of the configuration (host name, time zone, DNS and NTP servers, firewall ports, SSH, Samba, UPS, network interfaces) are runtime state here: the daemon stores them and applies them at once, and a unit applies them again at every start (`dplaneos-system-settings`, `dplaneos-network`). That is what makes one image work for everybody.

## Build

```bash
nix build .#iso            # installer ISO (x86_64; .#packages.aarch64-linux.iso on aarch64)
nix build .#iso-live       # live image: the system running from the medium
nix build .#packages.aarch64-linux.witness-sd-aarch64   # third vote: Raspberry Pi SD card image
nix build .#packages.x86_64-linux.witness-image-x86_64   # third vote: x86 disk image
```

The appliance's Samba is built with Active Directory support ([samba-package.nix](samba-package.nix)), which is not in the public binary cache: the first build takes a while.

An update file for a system built this way is made with [`scripts/make-update-file.sh`](../scripts/make-update-file.sh) and signed with your own key; set `services.dplaneos.ota.publicKeyFile` to its public half, or the systems you build will only accept the project's releases.

## Test

Every test starts real systems in virtual machines (KVM) and uses them through the API, as the web interface does:

```bash
nix build .#checks.x86_64-linux.appliance -L      # the installed system, end to end
nix build .#checks.x86_64-linux.install -L        # the installer, then the installed disk
nix build .#checks.x86_64-linux.update -L         # updates: trial, keep, return
nix build .#checks.x86_64-linux.domain -L         # Active Directory membership
nix build .#checks.x86_64-linux.reservations -L   # disk reservations on shared disks
nix build .#checks.x86_64-linux.groups -L         # storage groups on a shared disk
nix build .#checks.x86_64-linux.failover -L       # automatic failover
nix build .#checks.x86_64-linux.replicated -L     # replicated storage groups
nix build .#checks.x86_64-linux.quorum -L         # cluster and third vote
nix build .#checks.x86_64-linux.votes -L          # voters
nix build .#checks.x86_64-linux.witness-appliance -L
nix build .#checks.x86_64-linux.live-boot -L      # the live image
```

## Files

| File | What it is |
|---|---|
| `../flake.nix` | Inputs, the daemon and web interface packages, the system configurations (installed system, installer ISO, live ISO, witness images), the checks |
| `module.nix` | The DPlaneOS module: the daemon, nginx, PostgreSQL, ZFS, Docker, firewall, SSH, the units that apply settings at start |
| `modules/samba.nix`, `modules/nfs.nix`, `modules/ups.nix`, `modules/cluster.nix` | The services whose configuration the daemon writes |
| `configuration-standalone.nix` | The installed system: `module.nix` plus boot loader, kernel and hardware settings |
| `impermanence.nix` | What is kept on `/persist` |
| `ota-module.nix`, `dplaneos-update.sh` | System updates: verify, install next to the running version, trial start, keep or return |
| `update-signing.pub` | The public key update files of the project's releases are checked against |
| `disko.nix` | The layout of the system disk: boot, system, persist |
| `installer.nix`, `installer-common.nix`, `install.sh` | The installer ISO and what it runs |
| `console-network-wizard.nix` | `dplaneos-console-net`: an address from the console when there is no DHCP |
| `configuration-live.nix`, `live-iso.nix`, `live-persistence.nix`, `live-zfs-auto-import.nix` | The live image |
| `witness.nix`, `witness-image-pi.nix`, `witness-image-x86.nix` | The third-vote appliance |
| `samba-package.nix` | Samba with Active Directory support |
| `tests/` | The VM tests |

## Using the module in your own NixOS configuration

The flake exports `nixosModules.dplaneos` ([module.nix](module.nix)). This is how the VM tests build their systems (see [tests/groups.nix](tests/groups.nix) for a small one), so it works; it is not how the project is delivered, and updating such a system is yours to do with `nixos-rebuild`, not the web interface's. The settings pages work the same way there: the daemon applies them at runtime, and where your Nix configuration and the web interface set the same thing, the web interface wins while the system runs.

## License

DPlaneOS is AGPL-3.0. Nix does not treat it as unfree; no `allowUnfree` is needed.
