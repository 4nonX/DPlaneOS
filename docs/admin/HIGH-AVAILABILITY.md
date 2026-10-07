# DPlaneOS High Availability Guide

Run a full HA NAS on two mini-PCs and a shared disk shelf, no third box required. DPlaneOS supports two distinct HA topologies depending on the hardware available. Both are first-class paths with dedicated wizard workflows and matching dissolution procedures.

**HA is optional and off by default.** A freshly installed DPlaneOS node runs as a fully functional standalone system: all storage, shares (SMB/NFS), iSCSI, NVMe-oF, Docker, and monitoring features work without HA. Enable HA only when unplanned downtime is unacceptable and you have a second node ready.

**Enabling and disabling HA:** The HA on/off toggle is on the Settings - High Availability page. Turning HA on triggers a NixOS configuration rebuild to activate the cluster agent, Keepalived VIP, Patroni, and fencing services. Turning it off reverses this. The system remains fully operational in either state.

Before reading further, review [ARCHITECTURE.md](../reference/ARCHITECTURE.md#multi-node-ha-architecture) for the overall system model.

**If something goes wrong:** See [HA-FAILURE-MODES.md](HA-FAILURE-MODES.md) for recovery procedures. It covers 8 real failure scenarios (node unreachable, failover stalled, split-brain detected, witness down, replication lag, etcd quorum lost, fencing failed, and subordinate mode) with step-by-step recovery instructions.

---

## Cluster Quorum and the Third Vote

*New in the unreleased version (Design 0001, phase 3a). The topologies below describe the existing HA paths; quorum moves to Corosync as described here, step by step, during phase 3.*

Nodes in a cluster have to agree on who is in charge. DPlaneOS uses **Corosync** for this, the same cluster engine Proxmox VE uses. Everything is set up from System › High Availability › **Cluster quorum**; there is nothing to edit on the command line.

**1. Pair the nodes.** In System › Configuration Sync, pair the two nodes (join code on one, *Join another node* on the other). This establishes the trusted channel the cluster setup uses.

**2. Form the cluster.** On one node, choose the paired node and **Form cluster**. The cluster addresses are suggested from the route between the nodes; change them to a dedicated network if you have one. Both nodes must reach each other on UDP 5405. The nodes then share a cluster key and keep quorum together.

**3. Add a third vote.** With only two votes, a network split leaves both nodes believing they are in charge, so DPlaneOS does **not** fail over automatically with two votes: if a node fails, you take over on the other one manually. A third vote fixes this. It is tiny (no storage, no DPlaneOS installation needed) and only has to be always on and reachable from both nodes on TCP 5403. Click **Add a third vote**; you get a one-time code and two choices:

- **Any Linux machine** (Raspberry Pi, a small VM, an existing server, a cloud instance; Debian, Ubuntu, Raspberry Pi OS, Fedora, RHEL, openSUSE): paste the one command shown, for example

  ```
  curl -fsSk https://nas1.lan/api/quorum/witness-setup.sh | sudo sh -s -- https://nas1.lan dpq_…
  ```

  It installs `corosync-qnetd` with the system's package manager, creates its certificate authority, starts it, opens TCP 5403 in ufw or firewalld if one is active, and registers with the cluster. No SSH access or passwords are involved: the certificates are exchanged with the one-time code.
- **Another DPlaneOS system** (e.g. a NAS at another site): on it, open System › High Availability › **Serve as third vote** and enter the node's address and the code.

The panel closes by itself when the third vote has registered and then shows *Automatic failover is possible*.

**What the panel shows:** members and whether they are online, the third vote and whether it is voting, the vote count (e.g. *3 of 3 votes*) and whether automatic failover is possible, with the reason when it is not.

**How it protects the data (as in Proxmox):** a node that loses quorum stops resetting its watchdog and is reset by it before the surviving node takes over; the survivor waits for that. With the third vote, a network split leaves exactly one side quorate. See [ADR-0009](../design/adr/ADR-0009-fencing-layers.md) for the protection layers.

**Removing:** *Remove third vote* turns automatic failover off again; *Remove cluster* stops Corosync on all members (storage and configuration are not touched) and HA falls back to the heartbeat check.

### Storage groups

A **storage group** is what moves between nodes: one or more pools and everything on them (shares, exports, later also apps). System › High Availability › **Storage groups** › *New group*: give it a name, choose the topology and the pools (imported on this node), and the other nodes that may own it.

| Topology | Disks | On failover |
|---|---|---|
| Standalone | one node | no failover |
| Shared storage | all nodes see the same disks: SAS or SATA in a shared JBOD, SAN, NVMe-oF | no data loss; one node imports the pools at a time (enable ZFS multihost on the pool) |
| Replicated | each node has its own disks, any type | up to one replication interval is lost |

Exactly one node **owns** a group. The **epoch** shown next to the owner increases with every change of owner and works as a fencing token: configuration changes for the group carry it, and the other nodes refuse changes written with an older epoch (a node that was cut off and still believes it is the owner). Such a node learns the new owner within seconds of reconnecting and releases the pools.

**Move to …** (owner only, shared storage) hands a group over: the pools are exported here and imported on the other node; if that node cannot import them, the pools come back. A move takes about half a minute: ZFS multihost checks during the import that no other node still writes to the pool.

### Automatic failover and how the setup is protected

The card **How this setup is protected** lists each protection layer, what it adds, and what is missing on your hardware:

| Layer | Needs | Adds |
|---|---|---|
| Third vote | a small always-on device (see above) | automatic failover; without it, failover is a manual takeover |
| Watchdog self-fence | `/dev/watchdog` (hardware, or the kernel's softdog) | a node that owns storage and loses quorum stops resetting its watchdog and is restarted before another node takes over |
| ZFS multihost | shared-storage pools, any drives | refuses to import a pool another node still writes to |
| Disk reservations | disks that pass the SCSI-3 PR probe | the disks reject writes from a fenced node |
| Power fencing | IPMI/Redfish BMC or switched PDU | the surviving node powers the failed one off first |

Nothing is refused because a layer is missing; the card says what each missing layer would add. Automatic failover needs a third vote and a fencing method (the watchdog, or power fencing).

**Automatic failover** (shared-storage groups): when the owner leaves the cluster's quorate part, the first other candidate waits the fencing delay (watchdog timeout plus 15 seconds, shown on the card), powers the old owner off if power fencing is configured, and takes the group over (the epoch increases). The group's line shows the countdown. If the old owner is in fact still running, ZFS multihost refuses the import and nothing is changed.

**Take over** (on a group, shown on a candidate when the owner is unreachable and automatic failover is off, e.g. two nodes without a third vote): use it only when the owner is switched off or disconnected from the disks. Multihost still refuses the import if it is not.

### Replicated groups

Each node of a replicated group has its own disks (any type) and its own pool with the same name; the copies on the other nodes stay **read-only**. The owner replicates every few minutes (set per group: this interval is the most data a failover can lose) by streaming `zfs send` to the other nodes over the same encrypted channel as configuration sync, so no SSH keys are needed. The group's line shows when each node last received a replication; **Replicate now** runs one immediately.

- **Move to …**: the owner's copy becomes read-only, a final replication brings the target up to date, and the target's copy becomes the writable one; replication then runs from the new owner.
- **Failover**: off by default for replicated groups, because changes since the last replication are lost. With *Fail over automatically* checked (and a third vote), another node takes over its copy after the fencing delay; otherwise *Take over* does it on request.
- **Split copies**: a copy that was changed after the last replication it received (for example, a former owner that kept working during a network split) is never overwritten silently. Replication to that node stops, and its group line shows what happened with **Discard the changes here**: after copying off anything needed, that rolls the copy back to the owner's data and replication continues.

### Apps and NVMe-oF exports follow the group

A Docker stack whose volumes are on a group's pool (`/mnt/<pool>/...` in its compose file) and an NVMe-oF export of one of the group's zvols belong to the group, not to a node. They run only on the group's owner:

- The owner publishes their definitions; every other node of the group keeps a copy (the group's line lists them), so a node that takes over has them even when the old owner is down.
- On a **Move to …**, the stacks stop and the exports are removed on the old owner *before* its pools are released; if a stack cannot be stopped, the move is refused and the group stays where it was. The new owner writes the stacks and exports and starts them once the pools are writable there.
- After a **failover** or **Take over**, the new owner starts them as soon as it has imported the pools. A node that finds itself running a group's stack without owning the group stops it.
- Edit these stacks on the owner. Other stacks and exports (on pools in no group, or without pool volumes) stay per node.

Container images must be available on every node of the group (from a registry, or loaded on each node); a stack that cannot start on the new owner is shown as a problem on the group's line.

**Limitations in this release:** clusters of two nodes plus a third vote (more nodes follow with storage groups); certificates of the third vote are exchanged over the node's HTTPS without checking its (usually self-signed) certificate, protected by the single-use code.

---

## Deployment Topologies

Two supported paths. The **Setup Wizard** (Settings - High Availability - Setup Wizard) auto-detects which path applies to your hardware and shows only the relevant steps.

| | **Path A': Shared Storage** | **Path B: Replicated ZFS** |
|---|---|---|
| Hardware | SAS JBOD, SAN LUN, or NVMe-oF fabric | Any - SATA, NVMe, network-attached |
| Storage protection | SCSI-3 PR disk reservation (hardware arbiter) | ZFS replication + watchdog self-fence |
| Third node required | No - co-located etcd witness on Node A | Recommended (RPi, VM) for witness |
| RPO | Zero - single shared pool, no replication lag | Non-zero - up to one replication interval |
| Dissolution | Clean - disable HA, pool stays on survivor, no migration | Requires choosing canonical copy |
| Fencing method | SCSI-3 PR (primary) + watchdog floor | Watchdog (primary) + IPMI/PDU |

**If you are running two nodes against a single JBOD or dual-controller array**, use Path A'. If the nodes are in separate enclosures connected only by network, use Path B.

The Setup Wizard scans your hardware at Step 1 and recommends the appropriate path. You can override the detection manually.

---

## Hardware Topology Detection

```
GET /api/ha/hardware/detect
```

Non-destructive scan returning:
- `watchdog_available` / `watchdog_device` - whether `/dev/watchdog` is present
- `fenced_running` / `fenced_devices` - dplane-fenced reservation state
- `pool_sg_devices` - SCSI generic devices found in current ZFS pools
- `provisional_path` - `shared_storage` or `replicated`
- `provisional_reason` - explanation of the recommendation
- `probe_required` - whether a full PROUT write probe is needed to confirm PR support

This endpoint does not write to disks. The **PROUT write probe** (the only reliable PR capability test) is a separate operator-triggered action via `POST /api/ha/scsi/probe`.

---

## Path A': Two-Node HA on Shared Storage (SCSI-3 PR + Watchdog)

### Why this works without a third node

SCSI-3 Persistent Reservations replace the logical quorum referee with a physical one. The shared disks hold a Write Exclusive Registrants Only (WERO) reservation on behalf of the current primary. When the surviving node calls `FencedPreempt`, the disk controller evicts the failed node's registration and rejects its I/O with RESERVATION CONFLICT - regardless of what the failed node believes about its own status. The disk firmware is the arbiter, not a software vote. Split-brain at the storage layer is physically impossible: the faulted node cannot corrupt shared state because the controller refuses its writes below the OS.

The hardware watchdog is the complementary floor: if this node loses quorum and cannot reach any witness, it stops petting `/dev/watchdog` and the kernel hard-resets the node after the timeout. This removes the BMC/PDU network-reachability requirement from fencing. See [Hardware Watchdog Self-Fence](#hardware-watchdog-self-fence) below.

**Database coordination (Patroni/etcd):** SCSI-3 PR handles the storage split-brain question. Patroni's etcd cluster benefits from an odd number of members. On a two-machine deployment, run a co-located etcd witness member on Node A (a second etcd process on port 2381). This three-member etcd cluster (A, B, witness-on-A) gives Patroni quorum with no additional machine.

### Prerequisites

- Two physical machines with identical NIC and disk configurations
- A shared SAS JBOD, SAS expander, or SAN LUN accessible from both nodes simultaneously
- Disks that support SCSI-3 PR write operations - **verify with the PROUT round-trip probe, not a read-only check:**
  ```
  POST /api/ha/scsi/probe
  ```
  or from the UI: Settings - High Availability - SCSI-3 Persistent Reservations - Probe PR Support on Pool Disks.
  The probe registers a test key, reads it back, and unregisters. A drive that responds to READ KEYS but rejects REGISTER fails silently here, rather than at failover time.
- Static IP addresses for both nodes and one VIP address

> **NVMe-oF note:** Enterprise NVMe-oF fabric targets support NVMe reservations (spec 1.3+). The PROUT probe does not apply to NVMe devices; use `nvme resv-register` to validate NVMe reservation support on fabric targets. Consumer M.2 NVMe does not support reservations and belongs on Path B.

### Architecture

```
  ┌──────────────────────────┐        ┌──────────────────────────┐
  │  Node A (Primary)         │        │  Node B (Standby)         │
  │                           │        │                           │
  │  dplaned                  │◄──────►│  dplaned                  │
  │  Patroni (PG primary)     │  HA    │  Patroni (PG replica)     │
  │  etcd member A            │  peer  │  etcd member B            │
  │  etcd witness (co-located)│        │                           │
  │  dplane-fenced (PR held)  │        │  dplane-fenced            │
  │  /dev/watchdog (petting)  │        │  /dev/watchdog (petting)  │
  │  Keepalived (VIP owner)   │        │  Keepalived (BACKUP)      │
  └──────────┬───────────────┘        └──────────┬───────────────┘
             │                                    │
             └──────────────┬─────────────────────┘
                            │ shared SAS / block
                    ┌───────┴────────┐
                    │  Disk shelf    │
                    │  SCSI-3 PR     │
                    │  reservation   │
                    │  held by A     │
                    └────────────────┘
```

On failover: Node B calls `FencedPreempt` for each disk. The controller evicts A's registration. B's ZFS gate imports the pools. Patroni promotes B's PostgreSQL. VIP moves. Total RTO: 10-30 seconds.

**Dissolution:** Disable HA from the dashboard. This node keeps the pool imported with every byte intact. No replication lag, no canonical-copy decision, no data migration. The second node can be repurposed immediately.

### Installation

#### Step 1: Install DPlaneOS on both nodes

Install from the ISO on each machine. Complete first-boot setup and verify both nodes are healthy standalone systems before proceeding.

#### Step 2: Wire the NixOS HA module

```nix
services.dplaneos.ha = {
  enable = true;
  role   = "primary";    # "secondary" on Node B

  localAddress = "NODE_A_IP";
  peerAddress  = "NODE_B_IP";

  # co-located etcd witness - no third machine required (Path A')
  # Starts a vote-only etcd process on port 2381/2382 on Node A automatically.
  colocatedWitness = true;

  # Hardware watchdog self-fence (recommended - removes BMC requirement)
  watchdog = {
    enable      = true;
    timeoutSecs = 30;   # must be < failover_after_seconds in timing config
  };

  virtualIP    = "VIP_ADDRESS";
  interface    = "eth0";
  vrrpPassword = "KEEPALIVED_PASS";
};
```

Apply on each node:
```bash
sudo nixos-rebuild switch
```

#### Step 3: Verify SCSI-3 PR support with the PROUT probe

```
POST /api/ha/scsi/probe
```

The probe auto-enumerates pool disks and runs the full write round-trip on each. Check that all devices show `"supported": true`. Any device showing `"supported": false` will fail to fence at failover time and must be replaced or the cluster moved to Path B.

#### Step 4: Check dplane-fenced reservation status

```
GET /api/ha/scsi/status
```

Returns `"running": true`, the reservation key, and the list of currently reserved `/dev/sgN` devices. All ZFS pool member disks should appear in the list.

#### Step 5: Verify Patroni and etcd

```bash
# All three etcd members should be healthy (co-located witness on Node A port 2381)
etcdctl --endpoints=http://NODE_A_IP:2379,http://NODE_B_IP:2379,http://NODE_A_IP:2381 endpoint health

# Patroni cluster state
patronictl -c /etc/dplaneos/patroni.yaml list
```

Expected Patroni output:
```
+ Cluster: dplaneos +---------+----+-----------+
| Member | Host         | Role    | State   | TL | Lag in MB |
+--------+--------------+---------+---------+----+-----------+
| node-a | NODE_A_IP    | Leader  | running |  1 |           |
| node-b | NODE_B_IP    | Replica | running |  1 |         0 |
+--------+--------------+---------+---------+----+-----------+
```

#### Step 6: Verify ZFS split-brain gate

On the standby, ZFS pools must not be imported:
```bash
zpool list          # should show no pools on the standby
systemctl status dplaneos-zfs-gate
```

---

## Path B: Two-Node HA with Replicated ZFS

Use this path when:
- The two data nodes do not share physical storage (ZFS send/recv replication between them)
- Disks do not support SCSI-3 PR (consumer SATA, NVMe without fabric)
- IPMI BMC, PDU, or SBD fencing is preferred

**RPO is non-zero.** The standby holds ZFS snapshots replicated from the primary. It lags by up to the replication interval. Before disabling HA on a replicated cluster, compare ZFS TXG values on both nodes to identify the canonical copy. The UI prompts you to confirm the lag before tearing down the replication link.

The witness is a lightweight third node (Raspberry Pi 4, spare VM, or x86 mini-PC with 512 MB RAM) that acts as an external quorum referee. It runs only etcd. The hardware watchdog on each data node uses the witness to distinguish a live partition from a dead peer before self-fencing - without a witness, the watchdog cannot safely distinguish "peer died" from "I am the isolated side."

### Architecture

```
  ┌──────────────────┐        ┌──────────────────┐
  │  Node A (Primary) │        │  Node B (Standby) │
  │                   │◄──────►│                   │
  │  dplaned          │        │  dplaned          │
  │  Patroni (leader) │  ZFS   │  Patroni (replica)│
  │  etcd member A    │  repl  │  etcd member B    │
  │  /dev/watchdog    │        │  /dev/watchdog    │
  │  Keepalived (VIP) │        │                   │
  └────────┬──────────┘        └────────┬──────────┘
           │                            │
           └──────────┬─────────────────┘
                      │ etcd + witness probes
              ┌───────┴────────┐
              │  Witness node   │
              │  etcd member    │
              │  (vote + probe) │
              └────────────────┘
```

When Node A fails, Node B and the witness form etcd quorum (2 of 3). The watchdog self-fences Node A if it loses quorum and cannot reach the witness. Node B then promotes.

### Prerequisites

- Two data nodes plus one witness machine (512 MB RAM, 4 GB disk minimum)
- Watchdog device available on both data nodes (`/dev/watchdog` - present on most x86 and ARM SoCs; `softdog` kernel module is loaded automatically as fallback)
- At least one witness configured so the watchdog can safely distinguish partition from dead peer
- For IPMI fencing: BMC interfaces on both data nodes reachable from the other (optional, secondary to watchdog)
- For SBD fencing: a shared block device accessible from both data nodes (optional, secondary to watchdog)

### Installing the Witness Node

**Option A: Installer ISO (recommended)**

Download `dplaneos-vX.Y.Z-installer-amd64.iso` from the [releases page](https://github.com/4nonX/DPlaneOS/releases/latest) and boot the witness machine from it. Select "Install Witness Node."

**Option B: NixOS flake**

```nix
{ ... }:
{
  imports = [ /path/to/DPlaneOS/nixos/patroni-witness.nix ];

  services.dplaneos.ha.witness = {
    enable       = true;
    localAddress = "WITNESS_IP";
    nodeAAddress = "NODE_A_IP";
    nodeBAddress = "NODE_B_IP";
  };

  networking.hostName = "dplaneos-witness";
  time.timeZone       = "UTC";
  boot.loader.systemd-boot.enable      = true;
  boot.loader.efi.canTouchEfiVariables = true;
  users.users.root.openssh.authorizedKeys.keys = [ "ssh-ed25519 AAAA..." ];
  system.stateVersion = "26.05";
}
```

### Configuring the Data Nodes

```nix
services.dplaneos.ha = {
  enable = true;
  role   = "primary";    # "secondary" on Node B

  localAddress   = "NODE_A_IP";
  peerAddress    = "NODE_B_IP";
  witnessAddress = "WITNESS_IP";

  etcdEndpoints = [
    "http://NODE_A_IP:2379"
    "http://NODE_B_IP:2379"
    "http://WITNESS_IP:2379"
  ];

  # Hardware watchdog self-fence (critical on Path B - primary fencing mechanism)
  watchdog = {
    enable      = true;
    timeoutSecs = 30;
  };

  # IPMI fencing (optional secondary - watchdog fires first)
  fencing = {
    enable          = true;
    bmcIP           = "PEER_BMC_IP";
    bmcUser         = "admin";
    bmcPasswordFile = "/etc/dplaneos/ipmi-fence.pw";
  };
  # SBD alternative (set fencing.enable = false if using SBD instead of IPMI):
  # sbd.pool    = "tank";
  # sbd.dataset = "sbd-lease";

  virtualIP    = "VIP_ADDRESS";
  interface    = "eth0";
  vrrpPassword = "KEEPALIVED_PASS";
};
```

**Why watchdog is critical on Path B:** Without SCSI-3 PR, there is no hardware-level write exclusivity. The watchdog self-fence ensures a partitioned node resets itself before the survivor promotes - as long as a witness is configured to let the watchdog distinguish "I am isolated" from "peer died." Without a witness, the watchdog cannot safely self-fence in a two-node cluster (see [Hardware Watchdog Self-Fence](#hardware-watchdog-self-fence)).

**Dissolution:** The standby snapshot may lag behind the primary. Before disabling HA, compare ZFS TXG on both nodes to confirm which side has the most recent data:
```bash
zfs get -H -p txg <pool>   # run on each node; higher TXG = more recent
```
The UI prompts you to confirm before tearing down the replication link.

---

## Hardware Watchdog Self-Fence

The hardware watchdog removes the BMC/PDU network-reachability assumption from fencing. Instead of requiring the survivor to reach and power off the peer, the loser resets itself via local kernel hardware.

### How it works

The daemon writes to `/dev/watchdog` on every heartbeat tick while the cluster has quorum. If quorum is lost, the daemon stops writing. The kernel resets the node after the timeout if petting is not resumed.

**Isolation detection (safe two-node behavior):** The watchdog does not stop petting simply because the peer is unreachable. It stops only when:
1. Quorum is lost (peer unreachable), AND
2. At least one witness is configured, AND
3. All configured witnesses are unreachable

Without a witness configured, or when any witness is reachable, the watchdog keeps petting - indicating this node has network connectivity and is not the isolated side. This prevents the survivor from accidentally self-fencing when the peer simply dies without a partition.

**Timing invariant:** `watchdog_timeout_secs` must be less than `failover_after_seconds` (from timing config). This guarantees the loser has fully reset before the survivor's failover threshold fires and begins promotion.

### Driver

On NixOS with `ha.watchdog.enable = true`, the `softdog` kernel module is loaded automatically as a fallback when no hardware watchdog driver is present. Hardware watchdog drivers (iTCO_wdt for Intel, sp5100_tco for AMD, bcm2835_wdt for Raspberry Pi) take precedence when the hardware is present.

Verify the device is available:
```bash
ls -la /dev/watchdog   # or /dev/watchdog0
systemctl status systemd-watchdog
```

### Configuration

Via Settings - High Availability - Hardware Watchdog, or API:
```
GET  /api/ha/watchdog/configure
POST /api/ha/watchdog/configure   (AAL2 required)
```

| Field | Default | Notes |
|-------|---------|-------|
| `enable` | false | Arms the watchdog; takes effect immediately in the running daemon |
| `device` | `/dev/watchdog` | Device path; change requires daemon restart |
| `timeout_secs` | 30 | Kernel resets node if not pet within this interval; must be < `failover_after_seconds` |
| `pet_interval_sec` | 10 | How often the daemon writes to the device; must be < `timeout_secs` |

---

## SCSI-3 PR Capability Probe

**Use the write probe, not a read-only check.** `sg_persist --in -k` and similar PRIN-only tests verify that the drive responds to read commands, but many drives (particularly SATA behind SAT translation layers, and some enterprise SATA SSDs) respond to PRIN while silently rejecting PROUT REGISTER. A cluster built on a false-positive PRIN result appears healthy but fails to fence during a real partition.

The correct probe runs a full write round-trip:
1. PROUT REGISTER - register a test key
2. PRIN READ KEYS - verify the test key appears
3. PROUT REGISTER (unregister) - clean up

This proves the drive accepts write-side PR commands before you trust your split-brain protection to them.

```
POST /api/ha/scsi/probe
{"devices": ["/dev/sg0", "/dev/sg1"]}   // optional; empty = auto-enumerate pool disks
```

Response includes per-device `supported: true/false` and exact firmware error text for any rejected device.

**SATA hardware note:** Consumer SATA HDDs and SSDs do not support SCSI-3 PR. Enterprise SATA drives (Seagate Exos, WD Gold) occasionally do, but this depends on the specific SAT firmware implementation and must be verified with the write probe - not assumed. SATA drives behind a SAS HBA may or may not forward PROUT through the SAT layer, and when they do, the reservation is enforced by the HBA rather than the drive itself. If the write probe fails, move to Path B.

### Checking live reservation state

```
GET /api/ha/scsi/status
```

Returns `"running": true/false`, the 8-byte reservation key currently in use, and the list of `/dev/sgN` devices holding active WERO reservations. Available from the UI under Settings - High Availability - SCSI-3 Persistent Reservations.

---

## Day-2 Operations

The following procedures apply to both paths.

### Monitoring the Cluster

```
GET /api/ha/status
```

```json
{
  "local_node":      {"id": "node-a", "role": "active",  "state": "healthy"},
  "peers":           [{"id": "node-b", "role": "standby", "state": "healthy"}],
  "quorum":          true,
  "active_node":     {"id": "node-a"},
  "ha_enabled":      true,
  "last_failover_at": 0
}
```

Also available in the UI under Settings - High Availability.

### Manual Failover

```bash
# Graceful switchover via Patroni (no data loss)
patronictl -c /etc/dplaneos/patroni.yaml switchover dplaneos

# Or via the daemon API
POST /api/ha/switchover
```

Patroni stops writes on the current primary, waits for the replica to catch up, promotes it, then demotes the old primary to replica. HAProxy and Keepalived detect the change within 2 seconds and the VIP moves.

### Maintenance Mode

Suppresses automatic failover for a configurable duration:
```
POST /api/ha/maintenance
{"seconds": 1800}
```

Enable before rebooting or making changes to prevent false-positive automated failover. Auto-resumes after the timeout.

### Tuning Cluster Timing

```
GET  /api/ha/timing
POST /api/ha/timing   (AAL2 required)
```

| Parameter | Default | Notes |
|-----------|---------|-------|
| `failover_after_seconds` | 45 | Peer must be unreachable this long before STONITH fires; minimum `heartbeat * 3` |
| `heartbeat_interval_seconds` | 15 | How often nodes ping each other; takes effect after daemon restart |
| `hysteresis_window_minutes` | 60 | Auto-failover suppressed for this long after a failover; takes effect immediately |

If watchdog is enabled: `timeout_secs` must be less than `failover_after_seconds`. The recommended margin is at least `2 * heartbeat_interval_seconds` above the watchdog timeout.

### Rolling OTA Update (zero downtime)

1. Put Node B (standby) in maintenance mode
2. Trigger OTA on Node B: Settings - System - Updates
3. Node B reboots into the new system slot
4. Verify Node B is healthy: `GET /api/ha/status` from Node B
5. Switchover to Node B: `POST /api/ha/switchover`
6. Put Node A in maintenance mode
7. Trigger OTA on Node A
8. Node A reboots and rejoins as standby

See also [OTA-UPDATES.md](OTA-UPDATES.md#ha-rolling-upgrade).

### GitOps Writer

When HA is enabled, only the **GitOps writer** acts on the GitOps repository: the active node, with quorum, not in subordinate mode and not fencing. Quorum alone is not enough, since both nodes of a healthy pair have it.

- Apply (`POST /api/gitops/apply`) on any other node is refused with HTTP 409 and the reason.
- Web UI changes are written back to Git only from the writer. A standby has no pools imported (Path A') or only replication targets (Path B); committing its live state would push a `state.yaml` without the active node's pools.
- Drift checks run only on the writer; a standby reports "drift checks run on the GitOps writer" instead of false drift.
- Pool ownership operations (create, import, reshape, destroy) in an apply are additionally deferred when the writer check fails during the apply. On Path A' SCSI-3 PR is the hardware backstop; on Path B this is the software protection.

### Shared Secrets Key

Secrets stored by DPlaneOS (Git tokens and SSH keys, LDAP/AD/OIDC/SMTP passwords, Telegram token, TOTP seeds) are encrypted with the key in `services.dplaneos.secrets.keyFile` (default `/var/lib/dplaneos/secrets.key`, created on first start). Both nodes of a pair share one database, so **both must use the same key**; otherwise a node cannot decrypt secrets the other one stored, and after a failover Git sync, LDAP and alerting fail.

Each node checks all stored secrets at startup and reports the result at `GET /api/system/secrets/status` (`healthy`, `undecryptable`). An `ERROR: SECRETS:` line in `journalctl -u dplaned` names the affected values.

**New pair:** before configuring any secrets on node B, copy node A's key to node B (`/var/lib/dplaneos/secrets.key`, owner root, mode 0600), or deploy one key file to both nodes with agenix or sops-nix and set `services.dplaneos.secrets.keyFile` to it. Keep the key out of the Nix store.

**Existing pair with two different keys:**

1. Copy node A's key to node B as `/var/lib/dplaneos/secrets.shared.key` (root, 0600). Node A keeps using its key unchanged.
2. On node B set:
   ```nix
   services.dplaneos.secrets.keyFile         = "/var/lib/dplaneos/secrets.shared.key";
   services.dplaneos.secrets.fallbackKeyFile = "/var/lib/dplaneos/secrets.key";  # B's old key
   ```
   and rebuild. On start, node B re-seals every value only its old key could open under the shared key.
3. Check `GET /api/system/secrets/status` on both nodes: `healthy: true`.
4. Remove `fallbackKeyFile` on node B and rebuild.

Key rotation (`POST /api/system/secrets/rotate`) is refused while HA is enabled: it would change the key on one node only.

### Recovering from Node Failure

When Node A fails:

1. Patroni detects the failure via etcd membership loss
2. etcd quorum is maintained by Node B + the third member
3. Fencing executes in order:
   - **Hardware watchdog (if enabled):** Node A stops petting its watchdog (quorum lost, witnesses unreachable) and the kernel resets it after the timeout. Node B waits the guaranteed interval before promoting.
   - **SCSI-3 PR (Path A'):** Node B's `dplane-fenced` calls `FencedPreempt` for each shared disk. The controller evicts Node A's registration; A's I/O is rejected with RESERVATION CONFLICT.
   - **IPMI:** Node B powers off Node A via its BMC.
   - **SBD:** Node A's lease expires; it is treated as fenced.
4. Patroni promotes Node B's PostgreSQL
5. HAProxy and Keepalived detect the new primary; VIP moves to Node B
6. Node B's ZFS gate imports the pools
7. The daemon reconnects to PostgreSQL (now local) and resumes operations

Total RTO: approximately 10-30 seconds.

**Client behavior during failover:** NFS and SMB clients lose their connections when the VIP moves and reconnect automatically. Active byte-range locks are not preserved. Applications that depend on lock state continuity across failover will need recovery. This is an inherent property of VIP-based active-passive failover.

When Node A is repaired and reboots, it rejoins as a Patroni replica, streams missing WAL from Node B, and waits in standby with pools unmounted. To fail back:
```bash
patronictl -c /etc/dplaneos/patroni.yaml switchover dplaneos --primary node-b --candidate node-a
```

### iSCSI ALUA and NVMe-oF ANA Path State Management

For ALUA-enabled iSCSI targets or ANA-enabled NVMe-oF exports, the Keepalived `notify_backup` script performs path-state negotiation before pool export.

**Daemon-up path (normal failover):**
1. Keepalived detects role change to BACKUP
2. `notify_backup` probes `GET /health` on the daemon socket (2-second timeout)
3. If the daemon responds: calls `POST /api/ha/alua-standby` (ALUA targets to Standby), then `POST /api/ha/standby` (4-second pool export deadline, self-reboot on timeout)

**Daemon-down path:**
If the daemon socket is unavailable, `notify_backup` falls back to direct `zpool export` via the `zpool(8)` binary. This path has no ALUA pre-notification but prevents the node from holding imported pools after the VIP has moved. All actions are logged to syslog at `daemon.crit` level.

**Residual risk:** If both the daemon-mediated export and the direct `zpool export` fail (pool I/O stuck in D-state), the node cannot yield its pools. SCSI-3 PR fencing held by `dplane-fenced` is the last defence: the peer's promotion preempts this node's reservations. `dplane-fenced` runs in a separate systemd slice specifically to survive daemon restarts.

---

## Failover Mechanics

### Heartbeat and Detection

`heartbeatLoop` runs on both nodes, ticking at `heartbeat_interval_seconds` (default 15s, configurable via `POST /api/ha/timing`). Each tick:

1. `pingAllPeers()` - sends an HTTP GET to each peer's `/health` and records the last-seen timestamp
2. `checkFailover()` - evaluates whether the peer has been silent long enough to attempt promotion
3. `petWatchdogIfQuorum()` - pets the hardware watchdog if quorum is healthy

The failover threshold is `failover_after_seconds` (default 45s, configurable). A peer must be unreachable for longer than this threshold before STONITH fires.

### Promotion Guards

`checkFailover()` tests five conditions in order. All five must pass before any fencing or promotion occurs:

| Guard | Condition | Notes |
|-------|-----------|-------|
| SubordinateMode | Node must not be in subordinate mode | Set when a node boots with a stale ZFS TXG; cleared after TXG catch-up completes |
| HysteresisWindow | Configurable window must have elapsed since last promotion | Prevents rapid re-promotion after a flap; configurable via timing API |
| Fencing method | At least one fencing method must be configured | No configured method means no promotion; the daemon refuses to promote without a confirmed fence path |
| Quorum Witness | If a witness is configured, at least the required number must agree the peer is unreachable | Prevents false promotions during network partition |
| MaintenanceMode | Node must not be in maintenance mode | Set via `POST /api/ha/maintenance` before planned work |

### STONITH Execution

When all guards pass, a STONITH goroutine runs:

1. Try IPMI: `ipmitool -H <bmc_ip> power off`
2. If IPMI fails, try PDU: HTTP call to the PDU outlet
3. If both fail: **abort**. A node that cannot confirm the peer is fenced will never promote.

On Path A', the watchdog self-fence may fire before the IPMI/PDU path is reached (if the loser loses quorum and witnesses are unreachable). The survivor waits the guaranteed watchdog timeout + margin before importing pools, knowing the loser has reset.

### Post-Promotion Reconciliation and Git Reachability

After promotion, `runPostPromotionStacksApply` reads `state.yaml` from the local filesystem and reconciles Docker compose stacks against the desired state.

**Git is never contacted during promotion.** The function calls `os.ReadFile(stateYAMLPath)` against a local path. There is no `git pull`, no HTTP call to a remote, and no network dependency in the promotion path.

**GitOps writer:** after promotion this node becomes the GitOps writer (see [GitOps Writer](#gitops-writer)). Pool ownership operations in a GitOps apply are deferred whenever the writer check fails, so an isolated node does not act on a Git-desired state that says "create/import this pool."

**Behavior when Git is unreachable at promotion time:** The promoted node proceeds on whatever `state.yaml` its clone holds. Both nodes fetch the repository every 5 minutes (the standby fetches but does not evaluate drift), so the clone is at most one interval behind.

---

## Fencing Reference

### SCSI-3 Persistent Reservations (dplane-fenced)

Each node registers an 8-byte key derived from `/etc/machine-id` at startup. The primary holds a WERO reservation on every ZFS pool member disk. APTPL=1 ensures the reservation survives power cycles.

**On graceful failover:** `dplaned` calls `FencedRelease()` via the fenced socket before exporting pools, releasing the reservation cleanly.

**On unclean failover:** The surviving node's `dplane-fenced` calls `FencedPreempt(device)` for each disk. The PROUT PREEMPT command evicts the faulted node's registration at the controller. Subsequent I/O from the faulted node receives RESERVATION CONFLICT. This guarantee comes from disk firmware, not software coordination.

**Verifying PR support:** Use `POST /api/ha/scsi/probe` for the full PROUT write round-trip. Do not rely on `sg_persist --in -k` alone - it tests only read-side capability and produces false positives on drives that reject PROUT REGISTER.

```
GET  /api/ha/scsi/status   - reservation key + list of fenced /dev/sgN devices
POST /api/ha/scsi/probe    - PROUT write round-trip probe per device
```

```bash
# Service and journal logs
systemctl status dplane-fenced
journalctl -u dplane-fenced -n 50
```

### Hardware Watchdog

See [Hardware Watchdog Self-Fence](#hardware-watchdog-self-fence) above.

```
GET  /api/ha/watchdog/configure
POST /api/ha/watchdog/configure   (AAL2 required)
```

```bash
# Verify device and driver
ls -la /dev/watchdog
dmesg | grep -E "watchdog|iTCO|sp5100|softdog"
```

### IPMI / Redfish

Powers off the peer node via BMC when a partition is detected and the node is confirmed as the non-isolated side via the witness. On Path A', secondary to SCSI-3 PR. On Path B, secondary to hardware watchdog.

```bash
# Test the fence path before enabling
ipmitool -H PEER_BMC_IP -U admin -P PASSWORD chassis power status
```

### SBD (ZFS Token)

A ZFS dataset property serves as a poison-pill mechanism. Each node renews a lease periodically; an expired lease triggers fencing.

Requirements:
- ZFS pool accessible from both nodes
- Must be paired with a witness node in two-node clusters (SBD alone cannot distinguish partition from failure)
- The SBD lease dataset is created automatically at first boot by `dplaneos-sbd-init` when `ha.sbd.pool` is non-empty

### Network Quorum Witness

A neutral IP or URL that both nodes independently probe to detect network isolation. No software installation required on the target.

| This node reaches witness | Peer reaches witness | Decision |
|--------------------------|---------------------|----------|
| Yes | No | Peer is isolated - safe to promote |
| No | No | Full partition - do not promote |
| No | Yes | This node is isolated - do not promote |
| Yes | Yes | Both have network; use IPMI/PDU/watchdog fencing |

```
GET  /api/ha/network-witness          - read configuration
POST /api/ha/network-witness          - save configuration (AAL2 required)
POST /api/ha/network-witness/probe    - test connectivity
```

**Probe method `icmp` (default):** TCP dial to port 80. SYN reaching the host - even if port is filtered - proves network reachability.

**Choosing a target:** Any stable internet endpoint works: a rented VPS, `1.1.1.1`, `8.8.8.8`, a cloud metadata IP, or an internal router. The target must be independent from both HA nodes.

---

## Troubleshooting

| Issue | Check | Resolution |
|-------|-------|------------|
| Both nodes claim primary | `patronictl list` | Fencing not working; check PR reservation state or IPMI connectivity |
| VIP not moving after failover | `journalctl -u keepalived` | Keepalived not running or daemon API unresponsive on new primary |
| PostgreSQL replication lag increasing | `patronictl list` | Check network between nodes; check standby disk I/O |
| ZFS pools not importing after promotion | `systemctl status dplaneos-zfs-gate` | Patroni must show node as Leader before gate opens |
| SCSI-3 reservation not acquired | `GET /api/ha/scsi/status` | Verify `dplane-fenced.service` running; run PROUT probe to confirm drive PR support |
| PR probe fails (supported: false) | `POST /api/ha/scsi/probe` | Drive does not support PROUT write commands; move to Path B or replace drive |
| Old primary still writing after SCSI-3 failover | `GET /api/ha/scsi/status` on old primary | RESERVATION CONFLICT confirms fencing is working; if writes succeed, the HBA or drive firmware does not support SCSI-3 PR |
| `dplane-fenced` preempt fails | `journalctl -u dplane-fenced` | Check machine-id key derivation; verify physical disk path reachable from both nodes |
| etcd quorum lost | `etcdctl endpoint health` | At least 2 of 3 etcd members must be reachable |
| Patroni not finding etcd | `journalctl -u patroni` | Verify etcd endpoints in `/etc/dplaneos/patroni.yaml`; check firewall on etcd ports |
| Witness not reachable (Path B) | `etcdctl endpoint health http://WITNESS_IP:2379` | Check firewall rules; restart etcd on witness |
| Watchdog not petting (armed but no quorum) | `journalctl -u dplaned \| grep WATCHDOG` | Expected if quorum is genuinely lost; check witness reachability. If false alarm, verify witness config |
| Watchdog fires unexpectedly | `dmesg \| grep -i watchdog` after reboot | Node lost quorum AND could not reach witnesses; check witness endpoints are reachable from both nodes |
| Node self-resets during maintenance | `journalctl -u dplaned -b-1` | Watchdog fired; ensure maintenance mode is set before taking a node offline |
| Pool ownership operations deferred | `GET /api/gitops/status` | Quorum-aware reconciler held pool ops; check `GET /api/ha/status` for quorum state; resolves automatically on quorum restore |
| Docker stacks not matching desired state after failover | `GET /api/gitops/state` | Standby's last git sync may predate the primary's; wait for auto-sync or trigger manual sync |
