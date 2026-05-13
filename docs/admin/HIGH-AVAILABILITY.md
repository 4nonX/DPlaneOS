# DPlaneOS High Availability Guide

This guide covers planning, setup, and day-2 operations for a DPlaneOS HA cluster. Before reading this, understand the [architecture](../reference/ARCHITECTURE.md#multi-node-ha-architecture) - specifically PostgreSQL HA via Patroni, ZFS split-brain protection, STONITH fencing, and the witness node.

HA is a significant operational commitment. It eliminates planned downtime (rolling upgrades) and tolerated unplanned node failure (~10-30s RTO), but adds infrastructure complexity and requires a witness node and fencing hardware or shared storage.

---

## Overview

A DPlaneOS HA cluster consists of three components:

| Component | Count | Purpose |
|-----------|-------|---------|
| Main node (Primary) | 1 | Serves NAS traffic; ZFS pools mounted; Patroni PostgreSQL primary |
| Main node (Standby) | 1 | Hot standby; ZFS pools held unmounted; Patroni PostgreSQL replica |
| Witness node | 1 | etcd quorum voter only; no NAS traffic |

The cluster uses:
- **Patroni** for PostgreSQL leader election and streaming replication
- **etcd** for distributed lock and quorum (one member on each of the 3 nodes)
- **HAProxy** on each main node to route PostgreSQL connections to the current primary
- **Keepalived** for the floating VIP that clients connect to
- **STONITH fencing** (IPMI/Redfish or SBD) to prevent split-brain writes

---

## Prerequisites

Before starting:

- Two physical or virtual machines meeting the hardware requirements, with identical NIC and disk configurations
- One witness node: any machine with 512 MB RAM and reliable network access to both main nodes (a Raspberry Pi 4 works)
- Shared or replicated ZFS storage accessible by both main nodes (direct-attached with replicated topology, or shared SAS JBOD)
- For IPMI fencing: BMC/IPMI interfaces with network access between nodes
- For SBD fencing: a ZFS zvol or block device accessible by both nodes (small, ~1 GB)
- Static IP addresses for all three nodes and one VIP address
- SSH access to all nodes

---

## Planning

Before configuring, decide:

**Fencing mechanism:** IPMI fencing requires BMC credentials and network access from each node to the other's BMC. SBD fencing requires a shared block device. If neither is available, HA can run without fencing but split-brain protection is weakened - not recommended for production.

**VIP address:** Choose an IP address on the same subnet as the main nodes that is not assigned to any host. Clients will connect to this address permanently.

**etcd endpoints:** All three nodes must be able to reach each other on the etcd peer port (2380) and client port (2379).

---

## Installation

### Step 1: Install DPlaneOS on Both Main Nodes

Install DPlaneOS normally from the ISO on each node. Complete first-boot setup and ensure both nodes are healthy single-node systems before proceeding.

Do not configure any pools or data on the standby yet - it will be brought in via replication.

### Step 2: Install the Witness Node

The witness node runs only etcd. Any machine with 512 MB RAM, 4 GB disk, and network access to both nodes qualifies (Raspberry Pi 4, spare VM, or x86 mini PC).

**Option A: Installer ISO (recommended)**

Download `dplaneos-vX.Y.Z-installer-amd64.iso` (or `...-arm64.iso`) from the [releases page](https://github.com/4nonX/DPlaneOS/releases/latest) and flash it to a USB drive.

```bash
dd if=dplaneos-vX.Y.Z-installer-amd64.iso of=/dev/sdX bs=4M status=progress conv=fsync
```

Boot the machine and select "Install Witness Node" from the menu. The setup wizard asks for the three node IPs, an SSH key, and the target disk. Installation takes 5-15 minutes and requires internet access to fetch packages.

**Option B: Flake (for existing NixOS users)**

Clone the DPlaneOS repo onto the witness machine and write a `configuration.nix` that imports the witness module:

```bash
git clone https://github.com/4nonX/DPlaneOS
```

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

Apply:
```bash
sudo nixos-rebuild switch
```

Replace `WITNESS_IP`, `NODE_A_IP`, and `NODE_B_IP` with the actual static IP addresses. The witness module configures etcd with the naming scheme (`etcd-witness`, `etcd-<IP>`) that matches the main node HA module automatically.

### Step 3: Enable HA in the DPlaneOS NixOS Module

On each main node, add the HA configuration to `configuration.nix`:

```nix
services.dplaneos = {
  ha = {
    enable = true;
    role = "primary";    # or "standby" on node B
    
    patroni = {
      enable   = true;
      name     = "node-a";    # unique per node
      restPort = 8008;
      
      etcdEndpoints = [
        "http://NODE_A_IP:2379"
        "http://NODE_B_IP:2379"
        "http://WITNESS_IP:2379"
      ];
      
      postgresql = {
        listenAddr = "0.0.0.0";
        connectAddr = "NODE_A_IP";
        dataDir    = "/var/lib/dplaneos/pgsql";
      };
      
      replication = {
        username = "replicator";
        password = "STRONG_RANDOM_PASSWORD";
      };
    };
    
    haproxy = {
      enable = true;
      listenPort = 5000;
      patroniHealthPort = 8008;
      backends = [
        { name = "node-a"; address = "NODE_A_IP"; }
        { name = "node-b"; address = "NODE_B_IP"; }
      ];
    };
    
    keepalived = {
      enable = true;
      vip    = "VIP_ADDRESS";
      interface = "eth0";          # interface to bind the VIP on
      priority = 100;              # higher on primary (use 90 on standby)
      authPass = "KEEPALIVED_PASS";
      checkInterval = 2;           # seconds between daemon API checks
    };
    
    fence = {
      mechanism = "ipmi";          # "ipmi" or "sbd"
      
      # IPMI fencing (requires BMC access):
      ipmi = {
        peerBmcAddress = "PEER_BMC_IP";
        username = "admin";
        passwordFile = "/etc/dplaneos/ipmi-fence.pw";
      };
      
      # SBD fencing (alternative to IPMI):
      # sbd = {
      #   device = "/dev/disk/by-id/...";
      #   watchdogTimeout = 30;
      # };
    };
    
    peer = {
      address = "NODE_B_IP";
      port    = 9000;
    };
    
    zfsGate = {
      enable = true;    # enables Patroni-gated ZFS pool import
    };
  };
};
```

Apply on each node:
```bash
sudo nixos-rebuild switch
```

### Step 4: Initialize the Patroni Cluster

On the primary node, Patroni will initialize PostgreSQL and elect itself as the leader. On the standby, Patroni will detect the primary and begin streaming replication.

Check Patroni status:
```bash
# On any node
patronictl -c /etc/patroni.yml list

# Expected output:
+ Cluster: dplaneos (xxxxxxxx) +---------+----+-----------+
| Member | Host        | Role    | State   | TL | Lag in MB |
+--------+-------------+---------+---------+----+-----------+
| node-a | NODE_A_IP:5432 | Leader | running |  1 |           |
| node-b | NODE_B_IP:5432 | Replica| running |  1 |         0 |
+--------+-------------+---------+---------+----+-----------+
```

Replication lag should reach 0 within minutes. A persistent non-zero lag indicates a network or I/O issue.

### Step 5: Verify HAProxy

HAProxy on each node routes PostgreSQL connections to the current primary:

```bash
# Check which backend HAProxy considers healthy
curl http://localhost:8008/primary    # 200 on primary, 503 on standby

# Confirm the daemon connects via HAProxy
sudo -u postgres psql "host=localhost port=5000 dbname=dplaneos" -c "SELECT pg_is_in_recovery();"
# Should return: f (false = not a replica = this is the primary)
```

### Step 6: Configure the VIP (Keepalived)

Keepalived assigns the VIP to whichever node is currently running as primary. The daemon API is checked every 2 seconds.

```bash
# Verify VIP is assigned on the primary
ip addr show eth0 | grep VIP_ADDRESS

# Check Keepalived status
systemctl status keepalived
journalctl -u keepalived -f
```

### Step 7: Enable Fencing

**IPMI fencing:** Verify each node can fence the other before enabling automated fencing:

```bash
# From node A, test power control of node B:
ipmitool -H PEER_BMC_IP -U admin -P PASSWORD chassis power status

# Test the fence action (DRY RUN - do not run this in production without understanding it):
ipmitool -H PEER_BMC_IP -U admin -P PASSWORD chassis power off
```

**SBD fencing:** Initialize the SBD device:

```bash
sbd -d /dev/disk/by-id/... create
sbd -d /dev/disk/by-id/... message <node-b-hostname> test
```

### Step 8: Verify Split-Brain Protection

On the standby node, verify that ZFS pools are not imported:

```bash
# On standby: this should show no pools
zpool list

# The ZFS gate service should be in a held state
systemctl status dplaneos-zfs-gate
# Should show: active (running) - waiting for Patroni primary status
```

---

## Day-2 Operations

### Manual Failover

To fail over intentionally (e.g., for maintenance):

```bash
# Initiate switchover via Patroni (graceful, no data loss)
patronictl -c /etc/patroni.yml switchover dplaneos

# Or via the DPlaneOS HA API
POST /api/ha/failover
{"reason": "planned maintenance", "dry_run": false}
```

Patroni will:
1. Stop writes on the current primary
2. Ensure the replica is fully caught up
3. Promote the replica
4. Demote the old primary to replica
5. HAProxy and Keepalived detect the change within ~2 seconds
6. VIP moves to the new primary

### Monitoring the Cluster

```
GET /api/ha/status
```

Returns:
```json
{
  "local_node": {"id": "node-a", "role": "active", "state": "healthy"},
  "peers": [{"id": "node-b", "role": "standby", "state": "healthy"}],
  "quorum": true,
  "active_node": {"id": "node-a"},
  "ha_enabled": true,
  "last_failover_at": 0
}
```

Also available in the UI: Settings: High Availability.

### Placing a Node in Maintenance Mode

Maintenance mode suppresses automatic failover for a node:

```
POST /api/ha/maintenance
{"enable": true, "duration_minutes": 60}
```

Use maintenance mode before rebooting or performing work on a node to prevent Keepalived from triggering a failover due to temporary daemon unresponsiveness.

### Adding a Disk to the Active Pool

Disk operations (pool expansion, resilvering) only run on the primary. The standby reflects the changes automatically through ZFS replication.

Use the Storage UI or API as normal - the HA daemon enforces that write operations only execute when Patroni confirms local primary status.

### Rolling OTA Update

HA clusters support zero-downtime upgrades by updating one node at a time:

1. Put node B (standby) in maintenance mode
2. Trigger OTA on node B: `POST /api/ota/update` (or `dplaneos-ota-update` on the node)
3. Node B reboots into the new system slot
4. Verify node B is healthy: `GET /api/ha/status` from node B
5. Initiate switchover: `POST /api/ha/failover` - node B becomes primary, node A becomes standby
6. Put node A in maintenance mode
7. Trigger OTA on node A
8. Node A reboots, becomes the standby on the new version
9. Both nodes now run the new version

This procedure is also described in [OTA-UPDATES.md](OTA-UPDATES.md#ha-rolling-upgrade).

### Recovering from Node Failure

If node A fails:

1. Patroni detects the failure (via etcd membership, not just ping)
2. etcd requires quorum - with node A down, node B and the witness form quorum (2-of-3)
3. If STONITH fencing is configured, node B fences node A (IPMI power-off or SBD lease expiry)
4. Patroni promotes node B
5. HAProxy and Keepalived detect the new primary; VIP moves to node B
6. Node B's ZFS gate imports the pools
7. The daemon reconnects to PostgreSQL (now local) and resumes operations

Total RTO: approximately 10-30 seconds.

When node A is repaired and rebooted:
- It joins as Patroni replica
- Patroni streams missing WAL from node B
- ZFS gate on node A remains in standby (pools not mounted)
- Node A is ready to fail back or serve as standby

To fail back after node A recovers:
```bash
patronictl -c /etc/patroni.yml switchover dplaneos --master node-b --candidate node-a
```

---

## Fencing Reference

### IPMI/Redfish

IPMI fencing powers off the peer node via its BMC when a split-brain is detected.

Requirements:
- Both nodes must have IPMI/BMC interfaces on a management network accessible by the other node
- IPMI credentials stored at the path configured in `fence.ipmi.passwordFile`
- `ipmitool` available (included in the DPlaneOS system closure)

When triggered, the surviving node runs:
```
ipmitool -H <peer-bmc> -U <user> -P <password> chassis power off
```

Then waits for confirmation before importing ZFS pools.

### SBD (STONITH Block Device)

SBD uses a small shared block device as a "poison pill" fencing mechanism. Each node writes a lease timestamp to the SBD device periodically. If a node's lease expires, the other node treats it as fenced.

Requirements:
- A block device (zvol, LUN, or iSCSI target) accessible by both nodes
- Device does not need to be shared simultaneously during normal operation - it is only read during failover

SBD does not require BMC hardware, making it useful for software-only environments.

**SBD is not sufficient alone in a two-node cluster without a witness.** Without quorum, SBD cannot distinguish "peer failed" from "network partition." Always deploy the witness node.

---

## Troubleshooting

| Issue | Check | Resolution |
|-------|-------|------------|
| Both nodes claim to be primary | `patronictl list` | Fencing is likely not working; check IPMI connectivity or SBD lease |
| VIP not moving after failover | `journalctl -u keepalived` | Verify Keepalived is running; check daemon API is responding on the new primary |
| PostgreSQL lag increasing | `patronictl list` | Check network between nodes; check disk I/O on standby |
| ZFS pools not importing after promotion | `systemctl status dplaneos-zfs-gate` | Confirm Patroni shows node as Leader; restart zfs-gate service |
| etcd quorum lost | `etcdctl endpoint health` | Ensure at least 2 of 3 nodes (including witness) are reachable |
| Witness not reachable | `etcdctl endpoint health http://WITNESS_IP:2379` | Check firewall; restart etcd on witness |
| Patroni not finding etcd | `journalctl -u patroni` | Verify etcd endpoints in patroni.yml; check TLS if configured |
