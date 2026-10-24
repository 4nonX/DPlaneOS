# DPlaneOS HA Hardware: SCSI-3 Persistent Reservations

A **shared** storage group (disks that every candidate node sees: a SAS shelf, a SAN) can use SCSI-3 persistent reservations (PR) as one of its protection layers: the disks themselves refuse writes from a node that does not hold the reservation. See [High Availability](../admin/HIGH-AVAILABILITY.md) for the layers and how they combine; a group works without PR (watchdog self-fencing and ZFS multihost protect it), PR adds a layer in the hardware.

Whether a disk implements PR is decided per device, by a write probe: the only reliable test, since many devices answer the read commands and still ignore reservations.

---

## The probe

System › High Availability → Watchdog and power fencing → SCSI-3 persistent reservations → probe the pool disks, or:

```
POST /api/ha/scsi/probe            {}                       # the member disks of the imported pools
POST /api/ha/scsi/probe            {"devices": ["/dev/sg2"]} # chosen devices
```

```json
{
  "success": true,
  "auto_enumerated": true,
  "all_supported": false,
  "results": [
    { "device": "/dev/sg2", "supported": true },
    { "device": "/dev/sg3", "supported": false, "error": "..." }
  ]
}
```

- `"supported": true` - the device registered a key, took a reservation and released it: PR works on it.
- `"supported": false` - the device does not implement PR, or something between it and the node does not pass it on (a controller in RAID mode, a hypervisor, multipath). `error` says what failed.

Run the probe again after firmware updates and hardware changes.

DPlaneOS has no list of tested hardware: the probe decides for your devices.

---

## What to expect from a kind of storage

| Storage | PR | Notes |
|---|---|---|
| SAS disks in a dual-ported shelf or JBOD, on an HBA (IT mode) | usually yes | The common case for shared-storage HA. Check every disk with the probe. |
| FC or iSCSI SAN LUNs | usually yes | Arrays implement PR for cluster file systems; some need it enabled per host or LUN. |
| SATA disks (also behind a SAS expander) | no | SATA has no persistent reservations. Use watchdog and multihost, or a replicated group. |
| RAID controllers presenting logical volumes | often no | The controller answers for its volume; use HBA/IT mode for ZFS. |
| USB and eSATA enclosures | no | Not suitable for a shared group. |
| NVMe-oF namespaces | not used | DPlaneOS takes SCSI-3 PR only on SCSI devices. |

A replicated group (each node has its own disks, ZFS replication between them) needs no shared disks and no PR.

---

## Virtual machines (QEMU, Proxmox, KVM)

Reservations must reach the real storage:

| Disk configuration | PR | |
|---|---|---|
| Whole HBA passed through (PCI passthrough) | yes | Native SCSI behaviour |
| `scsi-block` LUN with `qemu-pr-helper` on the host | yes | The helper issues PR commands on behalf of the guest |
| Virtual disks (qcow2, raw), `scsi-hd`, virtio-blk | no | Use watchdog and multihost, or a replicated group |

libvirt example for a `scsi-block` LUN with the PR helper:

```xml
<disk type='block' device='lun'>
  <driver name='qemu' type='raw' cache='none' io='native'/>
  <source dev='/dev/mapper/shared-lun'>
    <reservations managed='yes'/>
  </source>
  <target dev='sdc' bus='scsi'/>
</disk>
```

On the host: `systemctl status qemu-pr-helper` (libvirt starts it for `managed='yes'`). Then run the probe inside the guest.

---

## When the probe says "not supported"

The tools below are not on the shell's PATH: `nix-shell -p lsscsi sg3_utils` provides them.

1. **Is it a SCSI device?** `lsscsi -g` lists the `/dev/sg*` device of each disk; SATA disks report `ATA` and do not support PR.
2. **Controller mode:** an HBA in IT mode passes the commands to the disks; a RAID controller usually does not.
3. **Firmware:** check the disk, shelf and controller firmware; some need an update or a setting for PR.
4. **Multipath:** every path to a disk must give the same result:
   ```bash
   for sg in /dev/sg*; do echo "$sg:"; timeout 2 sg_persist --read-keys "$sg" 2>&1 | head -3; done
   ```
5. **Without PR:** a shared group still has watchdog self-fencing and ZFS multihost; or use a replicated group (no shared disks; a failover loses at most the changes since the last replication).

---

## Reporting hardware

Results for hardware not described here help others: open an issue at <https://github.com/4nonX/DPlaneOS/issues> with the model, firmware version and the probe's `results`.
