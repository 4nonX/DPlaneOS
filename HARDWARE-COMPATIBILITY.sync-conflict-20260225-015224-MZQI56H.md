# D-PlaneOS Hardware Compatibility

## Overview

D-PlaneOS is designed to run on **any reasonable Linux-capable hardware**, from Raspberry Pi to enterprise servers. The system **automatically detects** hardware capabilities and **tunes itself** accordingly.

---

## Minimum Requirements

### Absolute Minimum (Will Run, But Slow)
- **CPU:** 1 core, any architecture (x86_64, ARM64, ARM)
- **RAM:** 2GB
- **Storage:** 8GB for OS, plus your data storage
- **Network:** 100 Mbps Ethernet

**Use Cases:**
- Raspberry Pi 4 (4GB)
- Old desktop repurposed as NAS
- Testing/development

**Limitations:**
- Slow ZFS operations
- Limited concurrent users
- Periodic indexing only (no real-time)

---

### Recommended Minimum (Good Performance)
- **CPU:** 2 cores, 4 threads (any vendor)
- **RAM:** 8GB
- **Storage:** 16GB for OS + SSD for system
- **Network:** 1 Gbps Ethernet

**Use Cases:**
- Home NAS (up to 20TB)
- Small office file server
- Media server (Plex, Jellyfin)

**Performance:**
- Good ZFS performance
- 5-10 concurrent users
- Hybrid indexing (some real-time)

---

### Optimal (Full Features)
- **CPU:** 4+ cores, 8+ threads
- **RAM:** 16GB+ (32GB+ with ECC recommended)
- **Storage:** NVMe for OS, multiple HDDs/SSDs for data
- **Network:** 10 Gbps

**Use Cases:**
- Home enthusiast NAS (50TB+)
- Small business server
- Production data storage

**Performance:**
- Excellent ZFS performance
- 20+ concurrent users
- Full real-time indexing

---

## Tested Hardware Configurations

### CPU Compatibility

#### Intel
- ✅ **Core i3/i5/i7/i9** (All generations)
- ✅ **Xeon** (E3, E5, Scalable)
- ✅ **Pentium/Celeron** (Limited performance)
- ✅ **Atom** (Minimal performance, works)

**Special Notes:**
- Intel QuickSync: Not used (future feature)
- AVX/AVX2: Not required
- VT-x/VT-d: Optional (for Docker)

#### AMD
- ✅ **Ryzen** (All series: 3, 5, 7, 9)
- ✅ **Threadripper** (Excellent)
- ✅ **EPYC** (Enterprise, excellent)
- ✅ **Athlon/A-Series** (Limited performance)

**Special Notes:**
- SMT (Simultaneous Multithreading): Fully supported
- AMD-V: Optional (for Docker)

#### ARM
- ✅ **Raspberry Pi 4/5** (4GB+ RAM required)
- ✅ **NVIDIA Jetson** (Excellent)
- ✅ **Apple M1/M2/M3** (via Asahi Linux, experimental)
- ✅ **Generic ARM64** (Most boards)

**Special Notes:**
- 32-bit ARM: Not supported
- 64-bit ARM required

---

### Memory Compatibility

#### ECC vs Non-ECC

| RAM Type | Status | Notes |
|----------|--------|-------|
| **ECC Unbuffered** | ✅ Recommended | Best for data integrity |
| **ECC Registered** | ✅ Recommended | Enterprise servers |
| **Non-ECC** | ⚠️ Supported | See NON-ECC-WARNING.md |

**Non-ECC Limitations:**
- Risk of silent data corruption (2-4 bit flips/month on 16GB)
- ZFS protects disk, NOT RAM
- Acceptable for:
  - Home use with backups
  - Non-critical data
  - Media libraries
- NOT recommended for:
  - Production databases
  - Critical business data
  - Compliance requirements

#### Memory Sizes

| RAM Size | Status | Auto-Tuned Settings |
|----------|--------|---------------------|
| **2GB** | ⚠️ Minimal | ARC: 512MB, Inotify: 65K |
| **4GB** | ✅ Low-End | ARC: 1GB, Inotify: 131K |
| **8GB** | ✅ Good | ARC: 2GB, Inotify: 262K |
| **16GB** | ✅ Great | ARC: 4GB, Inotify: 524K |
| **32GB** | ✅ Excellent | ARC: 8GB, Inotify: 1M |
| **64GB** | ✅ Enterprise | ARC: 16GB, Inotify: 1M |
| **128GB+** | ✅ Enterprise | ARC: 32GB, Inotify: 1M |

**Virtualized Systems:**
- ARC reduced by 25%
- Inotify reduced by 50%
- Worker threads unchanged

---

### Storage Compatibility

#### Filesystems

| Filesystem | Status | Notes |
|------------|--------|-------|
| **ZFS** | ✅ Primary | Recommended, full features |
| **Btrfs** | ✅ Supported | Good alternative |
| **ext4** | ✅ Supported | Lightweight, no COW |
| **XFS** | ✅ Supported | Large files |
| **NTFS** | ⚠️ Read-only | Via ntfs-3g |
| **FAT32/exFAT** | ⚠️ Limited | Removable media only |

#### Disk Types

| Disk Type | Status | Performance | Notes |
|-----------|--------|-------------|-------|
| **NVMe** | ✅ Excellent | Best | For OS + cache |
| **SSD** | ✅ Great | Good | For OS + data |
| **HDD 7200 RPM** | ✅ Good | Fair | Bulk storage |
| **HDD 5400 RPM** | ✅ Acceptable | Slow | Archive |
| **USB 3.0+** | ✅ Supported | Variable | External backup |
| **USB 2.0** | ⚠️ Slow | Poor | Not recommended |

#### RAID Controllers

| Type | Status | Notes |
|------|--------|-------|
| **Software RAID (mdadm)** | ✅ Full Support | Recommended with ext4/XFS |
| **ZFS RAID-Z** | ✅ Full Support | Recommended |
| **Btrfs RAID** | ✅ Supported | RAID 1/10 stable |
| **Hardware RAID (HBA mode)** | ✅ Full Support | Pass-through to ZFS |
| **Hardware RAID (RAID mode)** | ⚠️ Limited | ZFS can't see disks |

---

### Network Compatibility

| Interface | Status | Notes |
|-----------|--------|-------|
| **1 Gbps Ethernet** | ✅ Full Support | Standard |
| **2.5 Gbps Ethernet** | ✅ Full Support | Good upgrade |
| **10 Gbps Ethernet** | ✅ Full Support | High performance |
| **WiFi** | ⚠️ Not Recommended | For NAS, use wired |
| **Bonding/LACP** | ✅ Full Support | Multiple NICs |

---

### Motherboard Compatibility

#### Chipsets

**Intel:**
- ✅ All desktop chipsets (B, H, Z series)
- ✅ All server chipsets (C series)

**AMD:**
- ✅ All AM4/AM5 chipsets (A, B, X series)
- ✅ All TR4/sTRX4 chipsets
- ✅ All server chipsets

**ARM:**
- ✅ Raspberry Pi
- ✅ Most SBCs (Single Board Computers)

#### Special Features

| Feature | Status | Notes |
|---------|--------|-------|
| **IPMI/BMC** | ✅ Supported | Server management |
| **TPM** | ⚠️ Not Used | Future feature |
| **Secure Boot** | ⚠️ Disabled | Requires signed kernel |
| **UEFI** | ✅ Full Support | Recommended |
| **Legacy BIOS** | ✅ Supported | Works |

---

## Auto-Tuning Behavior

### How It Works

1. **Installation starts** → Hardware detection runs
2. **System scans:**
   - CPU: Vendor, cores, threads
   - RAM: Total, ECC status
   - Storage: ZFS/Btrfs availability, disk types
   - Platform: Bare metal vs virtualized
3. **Auto-tune settings:**
   - ZFS ARC limit
   - Inotify watches
   - Worker threads
4. **Apply configuration**
5. **Save profile** for future reference

### Example Auto-Tuning

#### Scenario 1: Budget Home Server
```
CPU: Intel Celeron J4125 (4 cores)
RAM: 8GB DDR4 (Non-ECC)
Storage: 500GB SSD + 4x 4TB HDD (ZFS RAID-Z1)
Platform: Bare Metal

Auto-Tuned:
  ZFS ARC: 2GB
  Inotify: 262,144 watches
  Workers: 2 threads
  
Result: ✅ Smooth operation for home media server
```

#### Scenario 2: Enterprise Server
```
CPU: AMD EPYC 7452 (32 cores, 64 threads)
RAM: 128GB DDR4 ECC
Storage: 2x 1TB NVMe + 24x 8TB HDD (ZFS RAID-Z2)
Platform: Bare Metal

Auto-Tuned:
  ZFS ARC: 32GB
  Inotify: 1,048,576 watches
  Workers: 8 threads
  
Result: ✅ High-performance production storage
```

#### Scenario 3: Raspberry Pi 4
```
CPU: ARM Cortex-A72 (4 cores)
RAM: 4GB LPDDR4
Storage: 64GB microSD + USB 3.0 external HDD
Platform: Bare Metal

Auto-Tuned:
  ZFS ARC: 1GB
  Inotify: 131,072 watches
  Workers: 2 threads
  Recommendation: Use ext4 instead of ZFS for better performance
  
Result: ✅ Works, but ZFS operations slow
```

#### Scenario 4: VM on Proxmox
```
CPU: 4 vCPUs (host: Ryzen 9 5950X)
RAM: 16GB (host has 64GB ECC)
Storage: Virtual disk on ZFS pool
Platform: KVM (Proxmox)

Auto-Tuned:
  ZFS ARC: 3GB (reduced for VM)
  Inotify: 262,144 watches (reduced for VM)
  Workers: 2 threads
  
Result: ✅ Conservative tuning for virtualized environment
```

---

## Unsupported / Problematic Hardware

### Will NOT Work

❌ **32-bit x86** - Requires 64-bit CPU  
❌ **<2GB RAM** - Insufficient for D-PlaneOS + ZFS  
❌ **Proprietary RAID cards in RAID mode** - ZFS can't see individual disks

### Works But Not Recommended

⚠️ **Non-ECC RAM >32GB** - High bit-flip risk  
⚠️ **WiFi only** - Unstable for NAS  
⚠️ **USB 2.0 storage** - Too slow  
⚠️ **SMR HDDs** - Very slow with ZFS (use CMR)

---

## Performance Expectations

### File Operations

| Hardware Tier | Sequential Read | Sequential Write | Random IOPS |
|---------------|----------------|------------------|-------------|
| **Budget (4GB, HDD)** | 100-150 MB/s | 80-120 MB/s | 50-100 |
| **Mid-Range (16GB, SSD)** | 400-550 MB/s | 350-500 MB/s | 5K-10K |
| **High-End (32GB+, NVMe)** | 1-3 GB/s | 1-2.5 GB/s | 50K-200K |

### Concurrent Users

| Hardware Tier | Light Use | Medium Use | Heavy Use |
|---------------|-----------|------------|-----------|
| **Budget** | 5 users | 2 users | 1 user |
| **Mid-Range** | 20 users | 10 users | 5 users |
| **High-End** | 50+ users | 30 users | 15 users |

---

## Upgrade Paths

### Budget → Mid-Range
1. Add RAM (8GB → 16GB)
2. Add SSD for OS
3. Result: 3-5x performance boost

### Mid-Range → High-End
1. Upgrade to ECC RAM
2. Add NVMe for cache
3. Upgrade to 10GbE
4. Result: Enterprise-grade performance

### Home → Production
1. **Must have:**
   - ECC RAM
   - Redundant power supplies
   - IPMI/out-of-band management
2. **Recommended:**
   - Hot-swap drive bays
   - Dual NICs with bonding
   - UPS integration

---

## Troubleshooting by Hardware Type

### Raspberry Pi Issues

**Problem:** System slow/unstable  
**Solution:**
1. Use ext4 instead of ZFS
2. Boot from SSD, not microSD
3. Disable swap
4. Limit indexing to on-demand only

### Low RAM Systems (<8GB)

**Problem:** Out of memory errors  
**Solution:**
1. Reduce ZFS ARC manually
2. Use periodic indexing only
3. Disable unnecessary services
4. Consider adding swap (on SSD)

### Virtualized Systems

**Problem:** Poor performance  
**Solution:**
1. Give VM direct access to disks (passthrough)
2. Don't nest ZFS (host uses ZFS, VM uses ext4)
3. Allocate at least 8GB RAM to VM
4. Use virtio drivers

### Old Hardware (<2015)

**Problem:** Slow, high CPU usage  
**Solution:**
1. Use ext4/XFS instead of ZFS
2. Disable real-time indexing
3. Reduce worker threads
4. Consider hardware upgrade

---

## Conclusion

**D-PlaneOS works on virtually any hardware:**
- ✅ Auto-detects capabilities
- ✅ Tunes itself accordingly
- ✅ Degrades gracefully on low-end systems
- ✅ Scales up to enterprise servers

**Minimum to run:** 2GB RAM, 1 CPU core  
**Recommended for good experience:** 8GB RAM, 4 CPU threads  
**Optimal for full features:** 16GB+ RAM, 8+ CPU threads, ECC

**The system will NEVER refuse to install** - it adapts to your hardware.
