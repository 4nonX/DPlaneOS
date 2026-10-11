# Disk Reservations: Which Disks Have Them

A shared storage group keeps its disks reserved for the node that serves it (SCSI-3 persistent reservations): the disks themselves refuse writes from every other node. This page says which kinds of disks can do that, how to find out for yours, and what has actually been tested.

Reservations are an additional protection. A shared storage group works without them (the watchdog, the third vote and ZFS multihost protect it); see [High Availability](../admin/HIGH-AVAILABILITY.md).

---

## What has been tested

| Disks | How | Result |
|-------|-----|--------|
| iSCSI LUNs from a Linux target (LIO, as in DPlaneOS itself, TrueNAS SCALE, and most Linux-based storage) | Automated VM test in this repository (`nixos/tests/reservations.nix`), run for every release: two nodes, a mirror of two LUNs, planned moves, and a takeover from an owner that is cut off from the cluster but still writing | Works: the owner holds the disks, a takeover shuts the previous owner out, the pool is intact |

**No physical SAS shelf, HBA or disk model has been validated by the project.** There is no list of supported hardware. Reservations are a standard SCSI feature and the commands used are the ones cluster software has used for decades, but firmware differs: run the probe on your disks, and before you rely on it, do the takeover test below on your hardware.

If you run this on real hardware, please open an issue with the enclosure, HBA and disk models and what the probe and the takeover test showed. That is how this page gets a hardware list.

---

## What to expect by kind of disk

| Kind | Reservations | Why |
|------|--------------|-----|
| SAS disks (dual-ported, in a shelf cabled to both nodes) | Normally yes | Persistent reservations are part of the SCSI standard that SAS disks implement |
| SATA disks behind a SAS HBA or expander | Usually no | The translation layer between SCSI and ATA mostly does not implement the reservation commands. Some answer the read commands and refuse the write ones, which is why the probe writes |
| SATA disks behind SAS interposers | Depends on the interposer | Some interposers implement reservations for the SATA disk behind them |
| iSCSI or Fibre Channel LUNs from a storage array | Usually yes | Arrays implement them for cluster software; some need it switched on per LUN or host group |
| NVMe (local, or NVMe over Fabrics) | Not used | NVMe has reservations of its own, with different commands. DPlaneOS does not use them |
| Virtual disks (virtio, files, QEMU's emulated SCSI disks) | No | The emulation does not implement the commands |
| Disks of a virtual machine passed through as SCSI block devices | Only with the hypervisor's help | QEMU passes reservation commands on only with its persistent reservation helper (`qemu-pr-helper`) configured for the disk; passing the whole HBA to the VM avoids the question |

Disks without reservations are left alone and shown as "Not supported by the disks". Nothing fails because of them.

---

## Find out for your disks

**HA › Watchdog and power fencing › Disk reservations › Probe the pool disks**, or:

```
POST /api/ha/scsi/probe
```

The probe looks at every SCSI disk of the pools imported on the node. On each it registers a test key, checks that the disk lists it, and removes it again. A disk that already carries reservations is reported as supported without being touched. The answer lists every disk:

```json
{
  "success": true,
  "auto_enumerated": true,
  "device_count": 2,
  "all_supported": true,
  "results": [
    { "device": "/dev/sdb", "supported": true },
    { "device": "/dev/sdc", "supported": true }
  ]
}
```

A disk that is not supported comes with the error the disk gave. Run the probe on **both** nodes: each node talks to the disks through its own HBA and cables.

The current state is on the same card and in `GET /api/ha/scsi/status`: for every pool of a shared storage group, every disk, and which node holds it.

---

## Takeover test on your hardware

Do this once before you rely on reservations, with data you can afford to lose access to for a few minutes.

1. Create the pool and the shared storage group. On the owner, the card must show the pool as **Reserved**, every disk held by this node. On the other node it shows the disks reserved for the owner.
2. On the node that does **not** serve the group, try to write to one of the disks: `dd if=/dev/zero of=/dev/sdX bs=512 count=8 seek=$(( $(blockdev --getsz /dev/sdX) - 8192 )) oflag=direct`. This writes into the 8 MiB that ZFS leaves unused at the end of a whole disk. It must fail with an input/output error. If it succeeds, the disk does not enforce the reservation: do not rely on it (and nothing was damaged).
3. Move the group to the other node and back (**Move**). After each move the reservation must be with the new owner, and the write test must fail on the other node.
4. Disconnect the owner from the cluster network, leaving it running and connected to the disks. On the other node, use **Take over**. It must succeed; the previous owner's pool stops (state SUSPENDED) and it raises the alert "this node has been shut out". Restart that node and reconnect it: it comes back as standby.

---

## When a node was shut out

A node whose disks were taken by another one cannot write to the pool any more: ZFS stops the pool there (SUSPENDED). The node reports it (alert `ha.fenced`, and the card says "This node is shut out") and never takes the disks back by itself. Restart it; it then follows the new owner.

## The tolerance setting

**Disk tolerance** (HA › Watchdog and power fencing › power fencing settings) is the share of a pool's disks that may fail to be reserved before the import of the pool is refused. Disks without reservations do not count. Default 10 %: one disk of ten that does not answer does not stop a takeover; more than that does, because importing would mean writing to a pool whose disks another node may still hold.
