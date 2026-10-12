# DPlaneOS Hardware Compatibility

What DPlaneOS needs, what it has been tested on, and what to avoid.

**What the project tests.** Every release is installed and exercised in x86_64 virtual machines (KVM), with virtual disks and network cards. The project has no hardware lab: no list of servers, controllers or disk shelves below is a list of tested products. DPlaneOS runs the Linux kernel and OpenZFS of the pinned NixOS release; hardware that works with those works with DPlaneOS, and the notes below are about what suits a ZFS NAS.

---

## Requirements

| | Minimum | Sensible |
|---|---|---|
| CPU | 64-bit x86 (x86_64), or 64-bit ARM (aarch64) | four cores or more |
| Firmware | UEFI | |
| Memory | 8 GB | 16 GB or more; ECC |
| System disk | 32 GB, used by nothing else | an SSD of 64 GB or more |
| Data disks | one | two or more, for redundancy |
| Network | one wired Ethernet port | |

The installed system starts through a UEFI boot loader; machines with BIOS-only firmware cannot start it.

**ARM.** Images for aarch64 are built for every release, for machines with UEFI firmware (ARM servers, virtual machines). The automated tests do not run on ARM. Single-board computers that start through their own boot firmware instead of UEFI (a Raspberry Pi out of the box) are not supported as a NAS. For the third vote of a cluster there is a separate small image for the Raspberry Pi ([Choosing a third vote](../admin/THIRD-VOTE.md)).

**Virtual machines.** DPlaneOS runs as a VM (that is how it is tested). For real data, give the VM whole disks or a whole controller (passthrough), not virtual disks on a host file system that does its own redundancy and caching.

## Memory

ZFS uses free memory as a read cache (the ARC) and gives it back when programs need it; by default up to half of the memory. The limit can be set under System › Settings. More memory makes a NAS faster; it does not need a fixed amount per terabyte.

ECC memory is strongly recommended: ZFS detects damage on disk, not data that was damaged in memory before it was written. See [Non-ECC RAM Warning](NON-ECC-WARNING.md); the dashboard says when the machine has none.

## Disks and controllers

| | |
|---|---|
| SATA, SAS, NVMe disks | all fine for pools. Disks are addressed by their stable names (`/dev/disk/by-id`) |
| Controller | an HBA, or a RAID controller in pass-through ("IT", "JBOD") mode: ZFS has to see every disk itself |
| Hardware RAID | **not for pools.** ZFS on top of a RAID volume cannot tell which disk returned bad data and cannot repair it, and the controller hides SMART data |
| SMR disks | avoid in pools that are rebuilt (RAID-Z, mirrors): resilvers take days and disks drop out |
| USB disks | for backups and removable media, not for pools: USB bridges drop disks under load and often hide their identity and SMART data |
| The system disk | one disk, not redundant. It holds the system and its configuration, never your files: if it fails, reinstall and import the pools ([Recovery](../admin/RECOVERY.md#7-the-system-disk-is-gone)) |

Pools are ZFS. Other file systems appear only on removable media (USB sticks and disks mounted under Removable Media: what the kernel can mount, such as ext4, FAT and exFAT); they are not tested by the project.

Disk enclosures with SES (slot LEDs, slot numbers) and SMART monitoring are used where the hardware provides them; a disk behind a bridge that passes neither through is shown without them.

## Network

Wired Ethernet, any speed the kernel has a driver for. Bonds (LACP and the other Linux modes), VLANs and static or DHCP addressing are configured under Network. Wi-Fi is not configured by DPlaneOS.

## Clusters

Two nodes, a third vote, and for shared storage a disk shelf both nodes are cabled to. What the layers of protection need from the hardware (a watchdog, disks with SCSI-3 persistent reservations, a BMC or a switched PDU), and how to find out what a given shelf supports, is in the [HA Hardware Matrix](../reference/HA-HARDWARE-MATRIX.md).

## Other hardware

| | |
|---|---|
| UPS | through NUT: USB and serial models with a NUT driver, or a NUT server on another machine |
| BMC | IPMI and Redfish, for sensors, power control and fencing |
| GPUs | passed to containers (NVIDIA through its container runtime; Intel and AMD as devices). The drivers for transcoding are those the container image brings |

## Will not work

- 32-bit CPUs.
- BIOS-only machines.
- A RAID controller that cannot pass disks through, if you want ZFS to protect your data.
- Less than 8 GB of memory: the installer does not refuse, but the system with a database, containers and ZFS has no room.
