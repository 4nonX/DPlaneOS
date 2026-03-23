# D-PlaneOS on Non-ECC Hardware

## The Core Issue

ZFS protects your data against corruption *on disk*. It cannot protect data that was already corrupted *in RAM* before being written.

```
1. User writes a file - it enters system RAM
2. A bit flips in RAM (cosmic ray, voltage, thermal)
3. ZFS writes the corrupted data to disk
4. ZFS calculates a checksum of the corrupted version and stores it
5. ZFS reports: data written successfully, checksum verified
6. The corruption is now permanent and undetected
```

ZFS cannot detect this because the corruption happened before ZFS received the data. From ZFS's perspective, the checksum is valid.

ECC RAM detects and corrects single-bit errors in hardware before they reach software. It is the only solution to this class of problem.

---

## Risk Assessment

### Bit Flip Rate (Consumer RAM)

- ~1 bit flip per 8 GB per week (cosmic ray baseline)
- ~1 bit flip per 16 GB per month (thermal/voltage)
- On a 16 GB system: 2–4 statistical bit flips per month

Not all flips hit active data. Most hit unused memory or data that is immediately discarded. But the files that are affected show no symptoms - no error, no alert, no corrupted-file marker.

### By Data Type

| Data Type | Risk Level | Recommendation |
|-----------|------------|----------------|
| Static media (photos, video, music) | Low–Medium | Acceptable with regular backups |
| Documents (infrequent writes) | Low | Acceptable with regular backups |
| Databases (frequent writes) | High | ECC required |
| Virtual machine storage | Critical | ECC required |
| Production business data | Critical | ECC required |

---

## What D-PlaneOS Does to Reduce Exposure

### ZFS ARC Limit

`install.sh` sets `zfs_arc_max` proportional to available RAM:

| RAM | ARC Limit |
|-----|-----------|
| 4 GB | 1 GB |
| 8 GB | 2 GB |
| 16 GB | 4 GB |
| 32 GB | 8 GB |
| 64 GB | 16 GB |

Less data held in ARC at any moment means less exposure time per byte. Configured in `/etc/modprobe.d/zfs.conf`.

### Non-ECC Advisory

The dashboard detects non-ECC RAM via `dmidecode` at startup and displays an informational notice. This is advisory - it never blocks operation or prevents pool creation.

### Other Software Mitigations

- Pool heartbeat with active I/O - detects pool stalls immediately
- PostgreSQL with `FULL` sync - ensures database consistency
- Inotify usage monitoring - warns at 90% capacity
- ZFS mount gate - prevents Docker-before-ZFS data loss race

These mitigations reduce risk at the software layer. They cannot address the fundamental hardware limitation.

---

## What Cannot Be Fixed in Software

RAM bit flips are a hardware problem. No amount of software can detect a bit flip that occurred before the software received the data. The only solution is ECC RAM.

---

## If You Choose to Run Without ECC

**Acceptable for:**
- Home media library (movies, music, photos) with regular offsite backups
- Document archive with backups and monthly scrubs

**Not acceptable for:**
- Databases
- Virtual machines
- Critical business data
- Any data without redundant backups

**Minimum recommended practices:**
1. Schedule weekly scrubs: `0 2 * * 0 /sbin/zpool scrub <pool>`
2. Follow the 3-2-1 backup rule: 3 copies, 2 different media types, 1 off-site
3. Run memtest86+ for at least 4 passes before first use
4. Plan to migrate to ECC hardware as budget allows

---

## ECC Hardware Requirements

Any of the following CPU/motherboard combinations support ECC:

- Intel Xeon (E3, E5, E-2xxx, Scalable series)
- AMD EPYC (any)
- AMD Ryzen Pro (any)
- Any server or workstation board advertising ECC UDIMM or RDIMM support

Consumer platforms (Intel Core, AMD Ryzen standard) do not support ECC regardless of what the RAM modules are rated for - the CPU and chipset must also support it.

**Rough cost estimate (2026):**
- Server motherboard + CPU: $400–1000
- 32 GB ECC DDR4: $150–300
- Total for a basic ECC-capable build: $550–1300

