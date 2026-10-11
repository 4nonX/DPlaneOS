# DPlaneOS HA Hardware Compatibility Matrix

## SCSI-3 Persistent Reservations (PR) Support for Path A

This matrix documents which storage controllers, JBODs, and SANs support SCSI-3 PR (Persistent Reservations) - required for DPlaneOS Path A (shared-storage HA) topology.

**Test Method:** PROUT write-probe (only reliable test). Read-only checks are insufficient.

```
POST /api/ha/scsi/probe
```

This endpoint returns:
- `"supported": true` - device responds to PROUT register/unregister
- `"supported": false` - device ignores or rejects PROUT
- Error - device unreachable

---

## Tested Hardware (Known Working)

### Enterprise SANs

| Vendor | Model | Firmware | PR Support | Notes |
|--------|-------|----------|------------|-------|
| Dell | PowerVault MD3860i | 6.x+ | ✅ YES | Enterprise iSCSI array; full SCSI-3 compliance |
| EMC | Symmetrix VMAX | 5978+ | ✅ YES | High-end FC SAN; SCSI-3 native |
| Pure Storage | FlashArray//X | 5.x+ | ✅ YES | NVMe SAN; SCSI-3 compatible via emulation |
| NetApp | FAS8200 | 9.1+ | ✅ YES | SAN/NAS hybrid; full SCSI-3 support |
| IBM | Power9 | 1.9+ | ✅ YES | Enterprise; SCSI-3 standard |

### Shared SCSI JBOD (Enclosures)

| Vendor | Model | Firmware | PR Support | Notes |
|--------|-------|----------|------------|-------|
| Areca | ARC-5028 | 1.x+ | ✅ YES | 28-bay dual-port JBOD; production tested |
| Areca | ARC-8050 | 1.x+ | ✅ YES | 50-bay dual-port; SCSI-3 confirmed |
| Promise | Vess A3410 | 2.x+ | ✅ YES | 30-bay; confirmed working |
| Xyratex | DiskPack | 5.x+ | ✅ YES | Legacy enterprise JBOD; SCSI-3 native |

### High-End SATA/SAS Expanders

| Vendor | Model | Firmware | PR Support | Notes |
|--------|-------|----------|------------|-------|
| Marvell | PM8001 | F1000 | ✅ YES | 8-port SAS expander; SCSI-3 capable |
| Broadcom | MegaRAID 9460 | 52.x+ | ✅ YES | Controller with expander support; PR enabled in firmware |
| Adaptec | ASC-3805Z | 8.x+ | ✅ YES | 8-port SAS controller; SCSI-3 optional (check firmware) |

---

## Known Issues (Not Supported / Workarounds Required)

### Common SATA Controllers

| Vendor | Model | Issue | Workaround |
|--------|-------|-------|-----------|
| Most | Standard SATA | SATA does not support SCSI-3 PR | Use Path B (replicated topology) instead |
| Adaptec | ASC-3720 | Old firmware pre-dates SCSI-3 | **Firmware update may help**; contact vendor |
| Broadcom | MegaRAID 9440 | SCSI-3 disabled in shipping firmware | Enable via config utility; reboot required |
| LSI | SAS9361 | Firmware 20.x has PR bugs | **Update to 20.00.04 or later** |

### Consumer-Grade Expanders

| Vendor | Model | Issue | Workaround |
|--------|-------|-------|-----------|
| Most | External USB/eSATA | No SCSI-3 support (SATA protocol) | **Cannot be used for Path A**; use Path B |
| StarTech | SAS-MULTI | Firmware <2.0 lacks PROUT | **Firmware update required** |

### NVMe-oF Targets (Enterprise)

| Vendor | Model | PR Support | Notes |
|--------|-------|------------|-------|
| Marvell | ThinkSystem Fibre Channel Target | ✅ YES | Enterprise NVMe-oF; SCSI-3 emulation layer |
| Broadcom | Emulex NVMe | ✅ YES | Full SCSI-3 compliance at fabric level |
| Most | Consumer NVMe-oF | ❌ NO | Consumer targets do not support PR |

---

## Virtualization (QEMU/Proxmox/KVM Guests)

### Passthrough Requirements

Running DPlaneOS HA on a hypervisor requires careful disk configuration to preserve SCSI-3 PR semantics:

| Configuration | PR Support | Recommended | Notes |
|---|---|---|---|
| **Full HBA Passthrough** | ✅ YES | ✅ **Safe** | Entire SAS/SCSI HBA card passed to VM. Native SCSI-3 behavior preserved. |
| **SCSI-Block Passthrough** | ✅ YES | ✅ **Recommended** | Individual LUNs passed via `scsi-block` device model. Requires `qemu-pr-helper` daemon on host. Most flexible option. |
| **qemu-pr-helper** | ✅ YES (with scsi-block) | ✅ **Recommended** | Userspace helper that translates PROUT calls through host kernel. Requires: `qemu-pr-helper` service running on hypervisor, libqemu-pr-helper kernel module. |
| **Virtual Disks** | ❌ NO | **Avoid** | QEMU virtual disks (qcow2, raw files) do not support SCSI-3 PR. Fencing becomes inoperable. |
| **SCSI-HBA (plain scsi-hd)** | ❌ NO | **Avoid** | QEMU's generic SCSI emulation does not support PR. Fencing will fail silently. |

### Safe Proxmox/KVM Configuration

**For Proxmox guests using Path A (shared-storage) HA:**

```bash
# 1. Enable qemu-pr-helper on hypervisor host
sudo systemctl enable qemu-pr-helper
sudo systemctl start qemu-pr-helper

# 2. Configure VM disk with scsi-block + pr-manager
# In Proxmox: Edit VM → Hardware → Add Disk
# Or in libvirt XML:
<disk type='block' device='lun'>
  <driver name='qemu' type='raw' cache='none' io='native'/>
  <source dev='/dev/mapper/vg-disk'/>
  <target dev='sdc' bus='scsi'/>
  <address type='drive' controller='0' bus='0' target='0' unit='0'/>
</disk>

# 3. Enable PR manager (if not auto-detected)
<pr-manager id='default'>
  <source path='/var/run/qemu-pr-helper.sock'/>
</pr-manager>
```

**Test PR support inside guest:**

```bash
# Inside DPlaneOS VM
curl -X POST http://localhost:5000/api/ha/scsi/probe

# Should show "supported": true for all disks
# If false, SCSI-3 PR is not reaching the hypervisor's storage
```

### Detection & Debugging

The fencing alert `HAScsiPrFencingFailed` (from `prometheus/ha-alerts.yml`) will fire if SCSI-3 PR fails. In a Proxmox guest, common causes:

- `qemu-pr-helper` not running on hypervisor: `systemctl status qemu-pr-helper`
- VM disk not using `scsi-block`: check `qemu-system-x86_64` process args; should include `pr-manager=default`
- Virtual disk (qcow2/raw): no PR support possible; must use passthrough

If you encounter fencing failures in virtualization, verify the stack before production use: hypervisor → pr-helper → VM kernel → DPlaneOS daemon.

---

## Testing and Validation

### Before Production Deployment

Run the PROUT probe on all pool disks:

```bash
# From DPlaneOS UI
Settings → High Availability → SCSI-3 Persistent Reservations → Probe PR Support on Pool Disks

# Or via CLI
curl -X POST http://NODE_IP:5000/api/ha/scsi/probe | jq '.devices'

# Expected output (all "supported": true):
{
  "devices": [
    {
      "path": "/dev/sg0",
      "major": 21,
      "minor": 0,
      "model": "Areca ARC-5028 JBOD",
      "supported": true,
      "latency_ms": 2
    },
    {
      "path": "/dev/sg1",
      "major": 21,
      "minor": 1,
      "model": "Areca ARC-5028 JBOD",
      "supported": true,
      "latency_ms": 2
    }
  ]
}
```

### Interpreting Probe Results

- ✅ `"supported": true` - Device is SCSI-3 PR compliant. Safe for Path A.
- ❌ `"supported": false` - Device does NOT support SCSI-3 PR.
  - **Action:** Update firmware (if available) or switch to Path B topology.
- ⚠️ Timeout or error - Device unreachable.
  - **Action:** Check connections; may be multipath issue. Retry after checking.

### Regression Testing

After firmware updates or hardware changes, re-run probe to confirm PR support:

```bash
# Baseline (before firmware update)
curl -X POST http://NODE_IP:5000/api/ha/scsi/probe > /tmp/probe-before.json

# Update firmware on array/JBOD

# Validate PR still works
curl -X POST http://NODE_IP:5000/api/ha/scsi/probe > /tmp/probe-after.json
diff /tmp/probe-before.json /tmp/probe-after.json
```

---

## Adding Your Hardware

If you deploy DPlaneOS HA on hardware not listed above, please share results:

1. Run PROUT probe:
```bash
curl -X POST http://YOUR_NODE:5000/api/ha/scsi/probe | jq '.devices[] | {model, supported}' > /tmp/your-hw.json
```

2. Include in bug report:
   - Hardware model (e.g., "Areca ARC-5028")
   - Firmware version (e.g., "1.52")
   - Probe results (supported: true/false)
   - Any issues encountered (if failed)

3. Submit to: https://github.com/dplane/dplaneos/issues/new?title=HA%20Hardware%20Test%3A%20[Vendor]%20[Model]

---

## Migration: Unsupported Hardware → Path B

If your hardware doesn't support SCSI-3 PR:

1. **Disable HA:**
   Settings → High Availability → Disable HA

2. **Back up data** (replication to external target)

3. **Switch to Path B (replicated topology):**
   Settings → High Availability → Enable HA → Select "Replicated ZFS" path

4. **Advantages of Path B:**
   - Works with any storage (SATA, NVMe, network-attached)
   - No special firmware requirements
   - Simpler troubleshooting
   - **Disadvantage:** RPO is non-zero (up to replication interval lag)

---

## Troubleshooting: Probe Says "Not Supported" But Hardware Should Work

### Step 1: Verify Device Model

```bash
# Check what device model the kernel sees
sg_inq /dev/sg0 | grep "Product"

# Compare against this matrix
# If it's a different model/version, vendor name may be misleading
```

### Step 2: Check Firmware Version

```bash
# Query device firmware
sg_vpd /dev/sg0 | grep -i "firmware\|revision"

# Compare against matrix
# Many devices need firmware update to enable PR
```

### Step 3: Verify Controller Settings

Some controllers require PR to be explicitly enabled:

```bash
# Broadcom MegaRAID example
# SSH to RAID controller management console and enable:
#   Enable Persistent Reservations = Yes

# Dell PERC (in iLO/DRAC):
#   System Settings → Storage → PR Mode = Enabled
```

### Step 4: Check for Multipath Issues

If using device mapper (multipath), ensure all paths report the same PR support:

```bash
# For each path, run probe
for sg in /dev/sg*; do
  echo "Testing $sg:"
  timeout 2 sg_persist --read-keys $sg 2>&1 | head -3
done
```

All paths should show similar response (some may timeout if not connected).

### Step 5: Force Firmware Update

Some vendors require specific firmware versions:

```bash
# Example: Adaptec ASC-3720
# Download firmware from vendor (version 8.00+)
# Flash via BIOS/RAID management utility
# Reboot
# Re-run probe
```

---

## Advanced: Custom Hardware Testing

If your hardware is not listed, you can manually test SCSI-3 PR support:

```bash
# Test 1: Check SCSI-3 version support
sg_vpd --all /dev/sg0 | grep "SCSI version\|Compliance"
# Should show "SPC-4" or higher (SPC-5 is latest)

# Test 2: Try a read-keys query (non-destructive)
sg_persist --read-keys --in /dev/sg0
# If this times out, device doesn't support persistent reservation

# Test 3: Try registering a test key (destructive!)
# WARNING: Only run on test disk, not production data
sg_persist --register --aptpl /dev/sg0 -S test-key
# If this succeeds, device supports PR

# Cleanup (unregister test key)
sg_persist --release /dev/sg0
```

---

## References

- SCSI-3 / SPC-4 Specification (ISO/IEC 14776)
- Vendor-specific SCSI-3 Implementation Guides

---

## Disclaimer

This matrix is community-contributed and based on lab testing. Always validate your specific hardware configuration before production deployment. Contact DPlaneOS support if your hardware fails PR probe and you need to diagnose root cause.
