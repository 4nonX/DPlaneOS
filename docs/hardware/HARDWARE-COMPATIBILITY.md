# DPlaneOS Hardware Compatibility

DPlaneOS runs on 64-bit x86 and ARM machines with UEFI firmware, from small home servers to enterprise servers. A Raspberry Pi can serve as a cluster's third vote (witness image); see the ARM section.

---

## Requirements

### Absolute Minimum

| Component | Minimum |
|-----------|---------|
| CPU | 2 cores, x86_64 or ARM64 (64-bit only) |
| RAM | 4 GB |
| OS storage | 32 GB (the installer refuses smaller disks) |
| Firmware | UEFI (legacy BIOS is refused) |
| Network | 100 Mbps Ethernet |

Limitations at this tier: slow ZFS operations, limited concurrent users.

### Recommended

| Component | Recommended |
|-----------|-------------|
| CPU | 4 cores / 8 threads |
| RAM | 16 GB+ (ECC preferred) |
| OS storage | NVMe SSD, 64 GB+ (gives a 40 GB system partition) |
| Network | 1 Gbps+ Ethernet |

### Optimal

| Component | Optimal |
|-----------|---------|
| CPU | 8+ cores with SMT |
| RAM | 32 GB+ ECC |
| OS storage | NVMe |
| Network | 10 GbE |

---

## CPU Compatibility

### x86_64

| Vendor | Series | Status |
|--------|--------|--------|
| Intel | Core i3/i5/i7/i9 (all generations) | Supported |
| Intel | Xeon (E3, E5, Scalable) | Supported |
| Intel | Pentium / Celeron | Supported (limited performance) |
| Intel | Atom | Supported (minimal performance) |
| AMD | Ryzen 3/5/7/9 (all series) | Supported |
| AMD | Threadripper | Supported |
| AMD | EPYC | Supported |
| AMD | Athlon / A-Series | Supported (limited performance) |

### ARM

| Platform | Status | Notes |
|----------|--------|-------|
| ARM64 servers and boards with UEFI firmware | Supported | The arm64 installer and live ISOs |
| Raspberry Pi 4/5 | Not as a NAS | No UEFI by default; the Pi 4 (and 3) runs the witness SD image for a cluster's third vote, see [THIRD-VOTE.md](../admin/THIRD-VOTE.md) |
| 32-bit ARM | Not supported | 64-bit required |

---

## Memory

### ECC vs Non-ECC

| Type | Status | Notes |
|------|--------|-------|
| ECC Unbuffered (UDIMM) | Recommended | Best for data integrity |
| ECC Registered (RDIMM) | Recommended | Enterprise servers |
| Non-ECC | Supported with caveats | See [NON-ECC-WARNING.md](NON-ECC-WARNING.md) |

### ZFS ARC and inotify

The ZFS ARC is capped at 16 GB (kernel parameter `zfs.zfs_arc_max`). System › Settings → Maintenance → **Memory tuning** sets another limit (and the swappiness) for this machine; it applies at once and is kept across reboots and updates. On a machine with little RAM, set it lower so containers and the daemon keep room.

`fs.inotify.max_user_watches` is 524288 (file-watching apps like Plex and Syncthing need many); System › Inotify Watches shows how many are in use.

---

## Storage

### Disk Types

| Type | Status | Notes |
|------|--------|-------|
| NVMe | Excellent | Best for OS and cache |
| SSD (SATA) | Good | Suitable for OS and data |
| HDD 7200 RPM | Good | Bulk storage |
| HDD 5400 RPM | Acceptable | Archive |
| USB 3.0+ | Supported | External backup only |
| USB 2.0 | Not recommended | Too slow |
| SMR HDD | Not recommended | Very poor ZFS performance; use CMR |

### Filesystems

| Filesystem | Status | Notes |
|------------|--------|-------|
| ZFS | Primary | Full features |
| Btrfs | Supported | Good alternative |
| ext4 | Supported | Lightweight, no COW |
| XFS | Supported | Large files |
| NTFS | Limited | Read-only via ntfs-3g |
| FAT32 / exFAT | Limited | Removable media only |

### RAID Controllers

| Type | Status | Notes |
|------|--------|-------|
| HBA / IT mode (passthrough) | Full support | Passes individual disks to ZFS: the recommended setup |
| Onboard SATA (AHCI) | Full support | |
| Hardware RAID in RAID mode | Not supported | ZFS cannot see individual disks, so it cannot repair them |

Pools are ZFS (mirror, RAID-Z1/2/3, dRAID); DPlaneOS does not manage mdadm or hardware RAID volumes.

---

## Network

| Interface | Status | Notes |
|-----------|--------|-------|
| 1 GbE | Full support | Standard |
| 2.5 GbE | Full support | Good upgrade |
| 10 GbE | Full support | High performance |
| Bonding / LACP | Full support | Multiple NICs |
| WiFi | Not supported for the NAS | Use wired; the installer and the system configure Ethernet |

---

## Hardware That Will Not Work

| Hardware | Reason |
|----------|--------|
| 32-bit x86 or ARM | 64-bit CPU required |
| Legacy BIOS only (no UEFI) | The system boots with systemd-boot (UEFI) |
| System disk under 32 GB | The installer refuses it |
| < 4 GB RAM | Insufficient for the daemon, PostgreSQL and ZFS |
| Hardware RAID in RAID mode | ZFS requires individual disk visibility |

---

## Example Configurations

### Home Server (8 GB, non-ECC)

```
CPU:     Intel Celeron J4125
RAM:     8 GB DDR4 Non-ECC
Storage: 128 GB SSD (system) + 4x 4TB HDD (RAID-Z1)
Tuning:  ARC limit 4 GB (Memory tuning), so containers keep room
```

### Small Business Server (128 GB ECC)

```
CPU:     AMD EPYC 7452
RAM:     128 GB DDR4 ECC
Storage: 2x 1TB NVMe + 24x 8TB HDD (RAID-Z2), HBA in IT mode
Tuning:  ARC limit raised (Memory tuning) above the 16 GB default
```

### VM on Proxmox (16 GB allocated)

```
CPU:     4 vCPUs
RAM:     16 GB virtual
Storage: HBA passed through to the VM; the system disk virtual
Tuning:  ARC limit 6 GB (Memory tuning)
```

---

## Troubleshooting by Hardware Type

### Low RAM (< 8 GB)

- Set a lower ARC limit (System › Settings → Maintenance → Memory tuning)
- Run fewer containers
- Watch memory on the dashboard

### Virtualised Systems

- Pass disks through directly (HBA passthrough) rather than using virtual disks
- Do not put the VM's ZFS pools on virtual disks that live on a ZFS pool of the host (double caching, poor performance)
- Allocate at least 8 GB RAM to the VM
- Use virtio drivers, and UEFI (OVMF) firmware
