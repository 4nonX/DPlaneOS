# Why DPlaneOS is built on NixOS

DPlaneOS is delivered as a whole system, not as a program to install on one. This page says what NixOS contributes to that, and what it does not.

---

## What it is used for

**One image, built from one commit.** The kernel, OpenZFS, Samba, PostgreSQL, nginx, Docker, the daemon and the web interface, with all their configuration, are described in the flake and built into one system. `flake.lock` pins the exact revision of everything that goes in. The installer ISO, the update file and the test systems of a release are built from the same description: what the tests ran is what is installed.

**Versions next to each other.** Every version of the system is a directory tree in the Nix store that never changes after it is built. Installing an update adds the new version next to the running one; nothing of the running one is overwritten. That is what makes it possible to start a new version once on trial and go back to the old one, complete and untouched, if the trial fails or never finishes ([System Updates](../admin/OTA-UPDATES.md)). There is no state in which half of the packages are new.

**The kernel and ZFS belong together.** OpenZFS is not part of the Linux kernel and must match it. Both come from the same pinned NixOS release, and the build fails if the pair is not what DPlaneOS expects (a kernel of 6.12 or newer, OpenZFS 2.3 or newer). A combination that does not fit is a failed build, not a NAS that cannot import its pools after an update.

**State in one place.** The system disk has three partitions: the boot loader, the system (the Nix store), and `persist`. Everything the system remembers is on `persist`, and the list of what that is, is part of the system's description (`nixos/impermanence.nix`):

| | |
|---|---|
| `/var/lib/dplaneos` | the database, settings, keys, certificates, the Samba and network configuration the daemon writes |
| `/etc/dplaneos` | configuration files of services the daemon manages (cloud remotes, schedules) |
| `/var/lib/docker` | images and containers |
| `/var/lib/samba`, `/var/lib/nfs`, `/etc/target` | what Samba, NFS and the iSCSI target keep between restarts |
| `/var/log/dplaneos`, `/var/log/samba` | logs |
| `/etc/machine-id`, SSH host keys, the ZFS host ID | the identity of the machine |

An update replaces the system and leaves `persist` alone; a backup of the configuration is a copy of the database from there.

**Tests that start real systems.** NixOS can build a set of virtual machines from the same modules and drive them from a script. Every release is tested that way: installed from the ISO, updated, clustered, failed over, joined to a domain, with clients on other machines using the shares and disks ([what is tested](SHOWSTOPPER-MITIGATION-GUIDE.md)).

## What it is not used for

**You do not configure DPlaneOS in Nix.** On an installed system there is no `configuration.nix` to edit and no `nixos-rebuild` to run. The settings a NixOS user would put in the configuration (host name, network, time zone, firewall, SSH, shares) are runtime state: set in the web interface or in `state.yaml`, stored in the database, applied at once, and applied again at every start. That is what lets one image serve every installation.

**The system partition is not wiped at a restart.** State belongs on `persist` because the system's description says so, not because everything else is thrown away.

**Builds are not bit-for-bit reproducible by promise.** The inputs are pinned and the build is hermetic; the project does not verify that two builds of a release are identical byte for byte. What a release offers instead is a signature and an SBOM for the images that CI built from the tagged commit.

## What it costs

- The only supported way to run DPlaneOS is its own image. Using the modules in a NixOS configuration of your own works (the tests do it) but is yours to maintain ([the NixOS side](../../nixos/README.md)).
- Changing something in the system itself (another kernel module, another package) means building your own image.
- Installing takes a disk of its own.

A port to another distribution would have to replace the image, the side-by-side versions and the test harness: see the [Porting Guide](PORTING-GUIDE.md).
