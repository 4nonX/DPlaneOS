# DPlaneOS High Availability Guide

Run a full HA NAS on two mini-PCs and a shared disk shelf, no third box required. On shared-SAS and shared-block topologies where the disks support SCSI-3 Persistent Reservations, DPlaneOS enforces split-brain protection at the disk-controller level, eliminating the need for a separate witness machine. On replicated or stretched-ZFS topologies where the two nodes do not share physical storage, a lightweight witness node is still the recommended approach.

Before reading further, review [ARCHITECTURE.md](../reference/ARCHITECTURE.md#multi-node-ha-architecture) for the overall system model. HA adds infrastructure complexity; it is the right choice when unplanned downtime is unacceptable, not as a default for every deployment.

---

## Deployment Topologies

Two supported paths. Choose based on how the storage is connected.

| Topology | Machines required | Fencing mechanism | When to use |
|----------|-------------------|-------------------|-------------|
| **Shared-SAS / shared-block** | 2 data nodes | SCSI-3 Persistent Reservations | Two nodes connected to one DAS shelf or SAN LUN; disks physically accessible from both nodes simultaneously |
| **Replicated / stretched-ZFS** | 2 data nodes + 1 witness | IPMI, SBD, or SCSI-3 | Nodes in separate enclosures with ZFS send/recv replication; no shared physical disk path |

If you are running two mini-PCs against a single JBODs or a dual-controller array, use Path A. If the nodes are in separate racks or buildings and connected only by network replication, use Path B.

---

## Path A: Two-Node HA on Shared Storage (SCSI-3 PR)

### Why this works without a witness

The traditional witness exists to break the symmetry of a network partition: when node A loses contact with node B, A cannot tell whether B is dead or whether A itself is the isolated side. A witness is an external observer both nodes can try to reach.

SCSI-3 Persistent Reservations replace that logical referee with a physical one. The shared disks hold a Write Exclusive Registrants Only (WERO) reservation on behalf of the current primary. When the surviving node executes `FencedPreempt`, the disk controller evicts the failed node's registration and rejects its I/O with a RESERVATION CONFLICT sense key, regardless of what the failed node believes about its own status. The disk firmware is the arbiter, not a software vote. Split-brain at the storage layer becomes physically impossible: the faulted node cannot corrupt shared state because the controller refuses its writes below the OS.

This is the same mechanism used by dual-controller arrays, Veritas Cluster, Pacemaker with `fence_scsi`, and VMware HA. On shared-SAS topologies it is the industry-standard answer for a reason.

**Database coordination layer (Patroni/etcd):** SCSI-3 PR handles the storage split-brain question. Patroni's etcd cluster still benefits from an odd number of members to reliably elect a PostgreSQL primary. On a two-machine deployment, run a co-located etcd witness member on one of the two data nodes (a second etcd process on a different port and data directory). It consumes roughly 64 MB of RAM and requires no additional machine.

### Prerequisites

- Two physical machines with identical NIC and disk configurations
- A shared SAS JBOD, SAS expander, or SAN LUN accessible from both nodes simultaneously
- Disks that support SCSI-3 Persistent Reservations. Verify before starting:
  ```bash
  sg_persist --in -k /dev/sdX
  # Should return a key list, not an error. An error means PR is not supported.
  ```
- Static IP addresses for both nodes and one VIP address
- SSH access to both nodes

### Architecture

```
  ┌──────────────────────────┐        ┌──────────────────────────┐
  │  Node A (Primary)         │        │  Node B (Standby)         │
  │                           │        │                           │
  │  dplaned                  │◄──────►│  dplaned                  │
  │  Patroni (PG primary)     │  HA    │  Patroni (PG replica)     │
  │  etcd member A            │  peer  │  etcd member B            │
  │  etcd witness (co-located)│        │                           │
  │  dplane-fenced            │        │  dplane-fenced            │
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

On failover: node B calls `FencedPreempt` for each disk. The controller evicts A's registration. B's ZFS gate imports the pools. Patroni promotes B's PostgreSQL. VIP moves. Total RTO: 10-30 seconds.

### Installation

#### Step 1: Install DPlaneOS on both nodes

Install from the ISO on each machine. Complete first-boot setup and verify both nodes are healthy single-node systems before proceeding. Do not configure pools on the standby yet.

#### Step 2: Wire the NixOS HA module

On each node, add the HA block to `configuration.nix`. The co-located etcd witness runs by pointing `witnessAddress` to node A's IP with a separate etcd instance (port 2381) on that same node. This three-member etcd cluster (A, B, witness-on-A) gives Patroni the quorum it needs with no third machine.

```nix
services.dplaneos.ha = {
  enable = true;
  role   = "primary";    # "standby" on node B

  localAddress   = "NODE_A_IP";
  peerAddress    = "NODE_B_IP";
  witnessAddress = "NODE_A_IP";  # co-located on node A; see etcd note below

  etcdEndpoints = [
    "http://NODE_A_IP:2379"
    "http://NODE_B_IP:2379"
    "http://NODE_A_IP:2381"    # co-located witness etcd member
  ];

  fence = {
    mechanism = "scsi3";
    # dplane-fenced manages PR keys automatically.
    # No bmcIP or SBD device required.
  };

  keepalived = {
    vip       = "VIP_ADDRESS";
    interface = "eth0";
    priority  = 100;           # 90 on node B
    authPass  = "KEEPALIVED_PASS";
  };
};
```

Apply on each node:
```bash
sudo nixos-rebuild switch
```

#### Step 3: Verify SCSI-3 reservation acquisition

On the primary node, confirm `dplane-fenced` holds the reservation on all shared disks:

```bash
dplane-fenced-ctl keys
# Shows the registered key for this node on each disk

dplane-fenced-ctl status
# Shows reservation holder and registered key count
```

Via the API:
```
GET /api/ha/fenced/status
```

#### Step 4: Check Patroni and etcd

```bash
# On any node - all three etcd members should be healthy
etcdctl --endpoints=http://NODE_A_IP:2379,http://NODE_B_IP:2379,http://NODE_A_IP:2381 endpoint health

# Patroni cluster state
patronictl -c /etc/patroni/patroni.yml list
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

#### Step 5: Verify HAProxy and VIP

```bash
# HAProxy health check - returns 200 on primary, 503 on standby
curl http://localhost:8008/primary

# VIP is on the primary
ip addr show eth0 | grep VIP_ADDRESS
```

#### Step 6: Verify ZFS split-brain gate

On the standby, ZFS pools must not be imported:

```bash
zpool list          # should show no pools on the standby
systemctl status dplaneos-zfs-gate
```

---

## Path B: Three-Node HA with Witness

Use this path when:
- The two data nodes do not share physical storage (ZFS send/recv replication between them)
- Disks do not support SCSI-3 PR
- IPMI BMC or SBD fencing is preferred over PR

The witness is a lightweight third node (Raspberry Pi 4, spare VM, or x86 mini PC with 512 MB RAM) that acts as the external quorum referee. It runs only etcd; it carries no NAS traffic and has no ZFS.

### Architecture

```
  ┌──────────────────┐        ┌──────────────────┐
  │  Node A (Primary) │        │  Node B (Standby) │
  │                   │◄──────►│                   │
  │  dplaned          │        │  dplaned          │
  │  Patroni (leader) │  ZFS   │  Patroni (replica)│
  │  etcd member A    │  repl  │  etcd member B    │
  │  Keepalived (VIP) │        │                   │
  └────────┬──────────┘        └────────┬──────────┘
           │                            │
           └──────────┬─────────────────┘
                      │ etcd peer links
              ┌───────┴────────┐
              │  Witness node   │
              │  etcd member    │
              │  (vote only)    │
              └────────────────┘
```

When node A fails, node B and the witness form etcd quorum (2 of 3). Fencing (IPMI or SBD) ensures node A cannot write before B promotes.

### Prerequisites

- Two data nodes plus one witness machine (512 MB RAM, 4 GB disk minimum)
- For IPMI fencing: BMC interfaces on both data nodes reachable from the other
- For SBD fencing: a shared block device (zvol or iSCSI LUN) accessible from both data nodes
- Static IPs for all three machines and one VIP

### Installing the Witness Node

**Option A: Installer ISO (recommended)**

Download `dplaneos-vX.Y.Z-installer-amd64.iso` (or `...-arm64.iso`) from the [releases page](https://github.com/4nonX/DPlaneOS/releases/latest) and boot the witness machine from it. Select "Install Witness Node." The wizard prompts for the three node IPs, an SSH key, and the target disk.

**Option B: NixOS flake**

```nix
# /etc/nixos/configuration.nix on the witness machine
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
  system.stateVersion = "25.11";
}
```

```bash
sudo nixos-rebuild switch
```

### Configuring the Data Nodes

```nix
services.dplaneos.ha = {
  enable = true;
  role   = "primary";    # "standby" on node B

  localAddress   = "NODE_A_IP";
  peerAddress    = "NODE_B_IP";
  witnessAddress = "WITNESS_IP";

  etcdEndpoints = [
    "http://NODE_A_IP:2379"
    "http://NODE_B_IP:2379"
    "http://WITNESS_IP:2379"
  ];

  fence = {
    mechanism = "ipmi";    # or "sbd"
    ipmi = {
      peerBmcAddress = "PEER_BMC_IP";
      username       = "admin";
      passwordFile   = "/etc/dplaneos/ipmi-fence.pw";
    };
    # SBD alternative:
    # sbd.device = "/dev/disk/by-id/...";
  };

  keepalived = {
    vip       = "VIP_ADDRESS";
    interface = "eth0";
    priority  = 100;    # 90 on node B
    authPass  = "KEEPALIVED_PASS";
  };
};
```

**Note on SBD without a witness:** SBD alone in a two-node cluster cannot distinguish "peer failed" from "I am the isolated side." Always pair SBD with the witness node in this topology.

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

Also available in the UI under Settings: High Availability.

### Manual Failover

To fail over intentionally (e.g., for maintenance):

```bash
# Graceful switchover via Patroni (no data loss)
patronictl -c /etc/patroni/patroni.yml switchover dplaneos

# Or via the daemon API
POST /api/ha/failover
{"reason": "planned maintenance", "dry_run": false}
```

Patroni stops writes on the current primary, waits for the replica to fully catch up, promotes it, then demotes the old primary to replica. HAProxy and Keepalived detect the change within 2 seconds and the VIP moves.

### Maintenance Mode

Maintenance mode suppresses automatic failover for a node:

```
POST /api/ha/maintenance
{"enable": true, "duration_minutes": 60}
```

Enable this before rebooting or making changes to prevent a false-positive automated failover.

### Rolling OTA Update (zero downtime)

1. Put node B (standby) in maintenance mode
2. Trigger OTA on node B: `POST /api/ota/update`
3. Node B reboots into the new system slot
4. Verify node B is healthy: `GET /api/ha/status` from node B
5. Switchover to node B: `POST /api/ha/failover`
6. Put node A in maintenance mode
7. Trigger OTA on node A
8. Node A reboots and rejoins as standby on the new version

See also [OTA-UPDATES.md](OTA-UPDATES.md#ha-rolling-upgrade).

### Recovering from Node Failure

When node A fails:

1. Patroni detects the failure via etcd membership loss
2. etcd quorum is maintained by node B + the third member (witness or co-located)
3. Fencing executes:
   - **SCSI-3 PR:** Node B's `dplane-fenced` calls `FencedPreempt` for each shared disk. The controller evicts node A's registration; A's I/O is rejected at the controller with RESERVATION CONFLICT.
   - **IPMI:** Node B powers off node A via its BMC, then waits for power confirmation.
   - **SBD:** Node A's lease expires; it is treated as fenced.
4. Patroni promotes node B's PostgreSQL
5. HAProxy and Keepalived detect the new primary; VIP moves to node B
6. Node B's ZFS gate imports the pools via `libzfs.PoolImportAll`; dataset `readonly` property and clone origins are updated
7. The daemon reconnects to PostgreSQL (now local) and resumes operations

Total RTO: approximately 10-30 seconds.

When node A is repaired and reboots, it rejoins as a Patroni replica, streams missing WAL from node B, and waits in standby with pools unmounted. To fail back:

```bash
patronictl -c /etc/patroni/patroni.yml switchover dplaneos --master node-b --candidate node-a
```

### Adding a Disk to the Active Pool

Disk operations (pool expansion, resilvering) only execute on the primary. The standby reflects changes automatically through ZFS replication. Use the Storage UI or API as normal; the HA daemon ensures write operations only run when Patroni confirms local primary status.

---

## Fencing Reference

### SCSI-3 Persistent Reservations (dplane-fenced)

Each node registers an 8-byte key derived from `/etc/machine-id` at startup. The primary holds a Write Exclusive Registrants Only (WERO) reservation on every ZFS pool member disk. APTPL=1 ensures the reservation survives power cycles.

**On graceful failover:** `dplaned` calls `FencedRelease()` via the fenced socket before exporting pools, releasing the WERO reservation cleanly.

**On unclean failover:** The surviving node's `dplane-fenced` calls `FencedPreempt(device)` for each disk. The PROUT PREEMPT command evicts the faulted node's registration at the controller. Subsequent I/O from the faulted node receives RESERVATION CONFLICT, regardless of what that node believes about its own state. This guarantee comes from disk firmware, not software coordination.

Requirements:
- Disks must support SCSI-3 PR: `sg_persist --in -k /dev/sdX`
- Disks physically accessible to both nodes (shared SAS JBOD or SAS switch)
- `dplane-fenced.service` running on both nodes

```bash
dplane-fenced-ctl status    # active reservations and key state
dplane-fenced-ctl keys      # registered keys per disk
GET /api/ha/fenced/status   # same via daemon API
```

### IPMI / Redfish

Powers off the peer node via BMC when a partition is detected and the node is confirmed as the non-isolated side via the witness.

Requirements:
- BMC interfaces on both data nodes, reachable from each other
- IPMI credentials at the path set in `fence.ipmi.passwordFile`
- `ipmitool` (included in the DPlaneOS system closure)

Test the fence path before enabling automated fencing:
```bash
ipmitool -H PEER_BMC_IP -U admin -P PASSWORD chassis power status
```

### SBD (STONITH Block Device)

A shared block device serves as a "poison pill" mechanism. Each node writes a lease timestamp periodically; an expired lease triggers fencing.

Requirements:
- Block device (zvol, LUN, or iSCSI target) accessible from both nodes
- Must be paired with a witness node in two-node clusters (SBD cannot distinguish partition from failure without a quorum tiebreaker)

Initialize the SBD device:
```bash
sbd -d /dev/disk/by-id/... create
sbd -d /dev/disk/by-id/... message <node-b-hostname> test
```

---

## Troubleshooting

| Issue | Check | Resolution |
|-------|-------|------------|
| Both nodes claim primary | `patronictl list` | Fencing not working; check PR reservation state or IPMI connectivity |
| VIP not moving after failover | `journalctl -u keepalived` | Keepalived not running or daemon API unresponsive on new primary |
| PostgreSQL replication lag increasing | `patronictl list` | Check network between nodes; check standby disk I/O |
| ZFS pools not importing after promotion | `systemctl status dplaneos-zfs-gate` | Patroni must show node as Leader before gate opens |
| SCSI-3 reservation not acquired | `dplane-fenced-ctl status` | Verify `dplane-fenced.service` is running; test PR support with `sg_persist` |
| Old primary still writing after SCSI-3 failover | `sg_persist --in -k /dev/sdX` on old primary | RESERVATION CONFLICT confirms fencing is working; if writes succeed, the HBA or disk firmware does not support SCSI-3 PR |
| `dplane-fenced` preempt fails | `journalctl -u dplane-fenced` | Check machine-id key derivation; verify physical disk path is reachable from both nodes |
| etcd quorum lost | `etcdctl endpoint health` | At least 2 of 3 etcd members must be reachable |
| Patroni not finding etcd | `journalctl -u patroni` | Verify etcd endpoints in `/etc/patroni/patroni.yml`; check firewall on etcd ports |
| Witness not reachable (Path B) | `etcdctl endpoint health http://WITNESS_IP:2379` | Check firewall rules; restart etcd on witness |
