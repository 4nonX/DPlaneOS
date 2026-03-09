# D-PlaneOS Hardware Compatibility

D-PlaneOS runs on any reasonable Linux-capable hardware, from Raspberry Pi to enterprise servers. The installer automatically detects hardware capabilities and tunes the system accordingly.

---

## Requirements

### Absolute Minimum

| Component | Minimum |
|-----------|---------|
| CPU | 1 core, x86_64 / ARM64 / ARMv7 |
| RAM | 2 GB |
| OS storage | 8 GB |
| Network | 100 Mbps Ethernet |

Limitations at this tier: slow ZFS operations, limited concurrent users, no real-time file indexing.

### Recommended

| Component | Recommended |
|-----------|-------------|
| CPU | 4 cores / 8 threads |
| RAM | 16 GB+ (ECC preferred) |
| OS storage | NVMe SSD, 20 GB+ |
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
| Raspberry Pi 4/5 (4 GB+) | Supported | ZFS operations slow; consider ext4 |
| Generic ARM64 | Supported | |
| 32-bit ARM | Not supported | 64-bit required |

---

## Memory

### ECC vs Non-ECC

| Type | Status | Notes |
|------|--------|-------|
| ECC Unbuffered (UDIMM) | Recommended | Best for data integrity |
| ECC Registered (RDIMM) | Recommended | Enterprise servers |
| Non-ECC | Supported with caveats | See [NON-ECC-WARNING.md](NON-ECC-WARNING.md) |

### Auto-Tuned ARC Limits

| RAM | ARC Limit | Inotify Watches |
|-----|-----------|-----------------|
| 2 GB | 512 MB | 65K |
| 4 GB | 1 GB | 131K |
| 8 GB | 2 GB | 262K |
| 16 GB | 4 GB | 524K |
| 32 GB | 8 GB | 1M |
| 64 GB | 16 GB | 1M |
| 128 GB+ | 32 GB | 1M |

On virtualised systems, ARC is reduced by 25% and inotify by 50%.

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
| ZFS RAID-Z | Full support | Recommended |
| Software RAID (mdadm) | Full support | Use with ext4 or XFS |
| HBA in passthrough mode | Full support | Passes individual disks to ZFS |
| Hardware RAID in RAID mode | Limited | ZFS cannot see individual disks |

---

## Network

| Interface | Status | Notes |
|-----------|--------|-------|
| 1 GbE | Full support | Standard |
| 2.5 GbE | Full support | Good upgrade |
| 10 GbE | Full support | High performance |
| Bonding / LACP | Full support | Multiple NICs |
| WiFi | Not recommended | Use wired for NAS |

---

## Hardware That Will Not Work

| Hardware | Reason |
|----------|--------|
| 32-bit x86 | 64-bit CPU required |
| < 2 GB RAM | Insufficient for daemon + ZFS |
| Hardware RAID in RAID mode | ZFS requires individual disk visibility |

---

## Auto-Tuning

On install, the system detects:
- CPU vendor, cores, threads
- Total RAM and ECC status
- Disk types (NVMe, SSD, HDD)
- Platform (bare metal vs virtualised)

Settings automatically configured:
- ZFS ARC limit (`/etc/modprobe.d/zfs.conf`)
- inotify max watches (`/etc/sysctl.d/99-dplaneos.conf`)
- ZFS DKMS headers for the running kernel

---

## Example Configurations

### Budget Home Server (8 GB, non-ECC)

```
CPU:     Intel Celeron J4125
RAM:     8 GB DDR4 Non-ECC
Storage: 500 GB SSD + 4x 4TB HDD (RAID-Z1)

Auto-tuned:
  ZFS ARC:   2 GB
  Inotify:   262,144 watches
```

### Enterprise Server (128 GB ECC)

```
CPU:     AMD EPYC 7452
RAM:     128 GB DDR4 ECC
Storage: 2x 1TB NVMe + 24x 8TB HDD (RAID-Z2)

Auto-tuned:
  ZFS ARC:   32 GB
  Inotify:   1,048,576 watches
```

### Raspberry Pi 4

```
CPU:     ARM Cortex-A72 (4 cores)
RAM:     4 GB LPDDR4
Storage: 64 GB microSD + USB 3.0 HDD

Auto-tuned:
  ZFS ARC:   1 GB
  Inotify:   131,072 watches
  Note:      ZFS operations slow; ext4 performs better here
```

### VM on Proxmox (16 GB allocated)

```
CPU:     4 vCPUs
RAM:     16 GB virtual (host has 64 GB ECC)
Storage: Virtual disk on ZFS pool

Auto-tuned:
  ZFS ARC:   3 GB (VM reduction applied)
  Inotify:   262,144 watches
```

---

## Troubleshooting by Hardware Type

### Raspberry Pi

- Use ext4 instead of ZFS for ZFS-on-sdcard setups
- Boot from SSD rather than microSD if possible
- Limit ZFS ARC manually if RAM < 4 GB

### Low RAM (< 8 GB)

- ARC is automatically reduced
- Consider periodic-only file indexing (disable real-time)
- Monitor swap usage

### Virtualised Systems

- Pass disks through directly (HBA passthrough) rather than using virtual disks
- Do not nest ZFS (host on ZFS + VM using ZFS = poor performance and potential corruption)
- Allocate at least 8 GB RAM to the VM
- Use virtio drivers

### Old Hardware (pre-2015)

- Use ext4 or XFS instead of ZFS if CPU is a bottleneck
- Reduce worker threads in system settings
