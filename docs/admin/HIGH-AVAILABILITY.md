# DPlaneOS High Availability Guide

High availability (HA) in DPlaneOS means: if a node fails, another node takes over its pools, shares, exports and apps, and clients reconnect to the same address. It is optional. A single DPlaneOS node is a complete NAS; add HA when unplanned downtime is not acceptable and you have a second node.

Everything below is set up in the web interface (System › Configuration Sync and System › High Availability). There is nothing to edit on the command line and no extra software to install on the nodes.

**If something goes wrong:** [HA-FAILURE-MODES.md](HA-FAILURE-MODES.md) walks through each failure scenario: what you see, what happens on its own, and what to do.

---

## 1. How it works

Five building blocks, each with one job:

| Building block | What it does | Where |
|---|---|---|
| **Paired nodes** | Every node keeps its own database. Paired nodes exchange configuration changes (shares, users, settings, passwords and other secrets) and keep working on their own if they are cut off. | System › Configuration Sync |
| **Cluster** | The nodes agree who is in charge, with Corosync (the engine Proxmox VE uses). A majority of votes decides. | HA › Cluster quorum |
| **Third vote** | Lets the cluster tell a dead node from a broken network, so failover can be automatic. A Raspberry Pi, a VM, another DPlaneOS box, or a third node. | HA › Add a third vote, see [THIRD-VOTE.md](THIRD-VOTE.md) |
| **Storage groups** | What moves between nodes: pools with their shares, exports, apps and a floating address. Exactly one node owns a group at a time. | HA › Storage groups |
| **Protection layers** | Make sure a failed or cut-off owner stops writing before another node takes over: the watchdog, ZFS multihost, disk reservations, power fencing. | HA › How this setup is protected |

**Why a node that is cut off keeps working.** Configuration is local first: a node that loses its partner keeps its own database, keeps serving what it owns as long as it is in the quorate part of the cluster, and lets you change its configuration. When the nodes see each other again, changes merge per resource; the same resource changed on both sides becomes a conflict you resolve on the Configuration Sync page, never silently.

**Why no shared database.** Earlier versions used a database replicated by Patroni (with etcd, HAProxy and keepalived). That made a node's management depend on reaching the database primary. It has been removed; every node now stands on its own.

---

## 2. Choosing a storage topology

A storage group has one of three topologies:

| | Shared storage | Replicated | Standalone |
|---|---|---|---|
| Disks | every candidate node sees the same disks: SAS or SATA in a JBOD cabled to both, a SAN, NVMe-oF | each node has its own disks, any type | one node |
| On failover | no data loss: the next node imports the same pools | up to one replication interval is lost (you choose it, e.g. 5 minutes) | no failover |
| Hardware | a shared shelf (dual-ported SAS, or a SATA JBOD with an expander both hosts reach) | nothing special: two independent boxes | — |
| Cost of a planned move | about 30 seconds (ZFS multihost activity check) | a final replication, then the switch | — |
| Typical use | business NAS, VMs on NFS/iSCSI/NVMe-oF | homelab, branch office, two sites | services that should follow the group model without HA |

**Shared storage, good:** zero data loss on failover, a single copy of the data, fast moves. **Limits:** needs a shared disk shelf; both nodes must never import the pool at once, so enable **ZFS multihost** on these pools (DPlaneOS refuses nothing, but the protection card tells you when it is off).

**Replicated, good:** any hardware, the two nodes can be in different rooms or sites, each node has a full copy (also a backup of sorts). **Limits:** the newest changes since the last replication are lost on a failover, so automatic failover is off by default for replicated groups; double the disks.

You can mix topologies: different groups on the same cluster can be shared and replicated.

---

## 3. Setting it up

### Step 1: pair the nodes

On node A: System › Configuration Sync › *Create a join code*. On node B: *Join another node*, enter A's address and the code, review the merge preview (what will be exchanged in each direction), and join. The nodes pin each other's TLS certificate on first contact and from then on exchange changes every 30 seconds and right after a change.

What is exchanged: datasets, shares, NFS exports, users and groups (with their passwords, SCRAM verifiers and TOTP secrets), replication jobs, shared system settings (time zone, DNS, NTP, firewall ports, Samba and SSH settings), LDAP, ACME and certificates. Secrets never appear in the history or in transit in plain text: each node seals them with its own key, and they travel encrypted with a key derived from the pair's secret. What stays per node: hostname, network interfaces, SMART tasks, and anything on a pool that is not imported there.

### Step 2: form the cluster

System › High Availability › Cluster quorum: choose the paired node and *Form cluster*. The cluster addresses are suggested from the route between the nodes; use a dedicated network if you have one. The nodes must reach each other on **UDP 5405**.

With two nodes the cluster runs in Corosync's two-node mode: everything works, but **failover is manual** (*Take over*), because two votes cannot tell a dead node from a broken link.

### Step 3: add a third vote

*Add a third vote* opens a dialog with four ways to provide it:

- a **QDevice** on any small Linux machine (Raspberry Pi, VM, cloud instance), set up with one pasted command;
- a **voter**: a Raspberry Pi or mini PC on the LAN that runs corosync as a full member;
- **another DPlaneOS system** serving as QDevice, set up in its web interface;
- a **third DPlaneOS node**.

[THIRD-VOTE.md](THIRD-VOTE.md) compares them, with what each is good at and its limits. Once the vote has registered, the panel shows *Automatic failover is possible*.

### Step 4: enable the watchdog on every node

High Availability › *Watchdog and power fencing* › **Watchdog**: enable it on every node (default timeout 30 s; 60 s on slow hardware). A node that owns a storage group and loses quorum stops resetting its watchdog and the kernel restarts it, before another node takes over. If the board has no hardware watchdog (iTCO, sp5100_tco, the Raspberry Pi's bcm2835), DPlaneOS loads the kernel's `softdog`, which works the same way except when the kernel itself hangs.

Optional: **power fencing** with the nodes' BMCs (IPMI/Redfish) or a switched PDU. The surviving node then also powers the failed one off before taking over.

### Step 5: create storage groups

HA › Storage groups › *New group*: a name, the topology, the pools (imported on this node), the other nodes that may own it, and optionally a **floating address** (e.g. `192.168.1.50/24` on `eth0`; the interface needs the same name on every node). Point clients at the floating address.

For replicated groups also choose the replication interval and whether the group may fail over automatically (off by default: changes since the last replication would be lost).

### Step 6: check the protection card

*How this setup is protected* lists each layer, whether it is active on this hardware, and what a missing one would add. Nothing is refused because a layer is missing. Automatic failover needs a third vote and a fencing method (the watchdog, or power fencing).

---

## 4. What a storage group does

**One owner.** Exactly one node owns a group. The **epoch** next to the owner goes up with every change of owner and works as a fencing token: a node that was cut off and still thinks it is the owner is refused by the others and releases the pools as soon as it reconnects.

**Pools.** On the owner, the group's pools are imported and writable. Pools of a shared group are never imported at boot (on shared disks the boot cannot know who owns them); the owner's daemon imports them once the other nodes confirm they do not use them, or once a silent node has left the cluster and the fencing delay has passed. Until then the group's line says what it waits for; **Import here** imports on request when you know the other nodes are off. Manual imports of a shared group's pools are refused on nodes that do not own it.

**Floating address.** Held by the owner while it serves the group and announced on the network (gratuitous ARP) when it takes the group over; removed before a planned move releases the pools.

**Apps and NVMe-oF exports.** Docker stacks with volumes on the group's pools (`/mnt/<pool>/...`) and NVMe-oF exports of its zvols run only on the owner. The owner publishes their definitions to the other candidates, so a node that takes over has them even if the old owner is gone. Container images must be available on every candidate (from a registry, or loaded on each node). Edit these stacks on the owner.

**Shares and NFS exports** live on the pools and follow them; their configuration is exchanged between the paired nodes anyway.

**Replicated groups.** The owner sends `zfs send` streams to the other candidates over the same encrypted channel as configuration sync (no SSH keys). The other copies are read-only. A copy that was changed behind the cluster's back (for example a former owner that kept working during a split) is never overwritten silently: replication to it stops and its line offers **Discard the changes here** once you have copied off anything you need.

---

## 5. How failover works

Automatic failover of a group (a third vote and a fencing method are present):

1. The owner leaves the quorate part of the cluster: it crashed, lost power, or its network broke.
2. If it is still running but cut off, it notices that it has no quorum while owning storage, stops resetting its watchdog and is restarted by the kernel within the watchdog timeout.
3. The first other candidate in the quorate part waits the **fencing delay** (watchdog timeout + 15 seconds; shown on the protection card and as a countdown on the group's line).
4. If power fencing is configured, it powers the old owner off.
5. It imports the pools (shared) or makes its copy writable (replicated), raises the epoch, takes the floating address and starts the group's apps and exports.

If the old owner is in fact still writing to a shared pool, ZFS multihost refuses the import and nothing changes.

Without a third vote, or for replicated groups with automatic failover off, step 3 stops with an explanation, and **Take over** on the surviving node does steps 4 and 5 on request. Use it only when the old owner is off or disconnected from the disks.

---

## 6. Day-to-day operations

**Planned move** (maintenance, updates): on the owner, *Move to …* on the group. Shared: apps and exports stop, the address is released, the pools are exported and imported on the target (about 30 s for the multihost check); if the target cannot import them, they come back. Replicated: the owner's copy becomes read-only, a final replication brings the target up to date, the target becomes the owner.

**Updating nodes one at a time:** move all groups off node A, update and reboot A, move them back (or leave them), then do the same for B. The cluster stays quorate as long as one node and the third vote are up.

**Adding a node:** pair it, *Add a vote or node* › *A third DPlaneOS node*, then add it as a candidate to groups. **Removing a node:** move its groups away first, then remove it in the member list.

**Configuration changes:** make them on any node; they reach the others within seconds. Conflicts (the same resource changed on two nodes while they were apart) appear on the Configuration Sync page with both versions.

**Removing HA:** remove the groups (only the definitions; pools and data stay), then *Remove cluster*, then detach the nodes in Configuration Sync if you want them fully independent.

---

## 7. Monitoring

`GET /api/metrics` on each node exports, among others:

| Metric | Meaning |
|---|---|
| `dplaneos_ha_enabled`, `dplaneos_ha_quorum` | in a cluster; in the quorate part |
| `dplaneos_ha_expected_votes`, `dplaneos_ha_online_nodes` | votes expected; members this node sees |
| `dplaneos_ha_auto_failover` | automatic failover possible |
| `dplaneos_ha_self_fence_armed` | owns storage, lost quorum, about to reset |
| `dplaneos_group_owner_here`, `dplaneos_group_serving`, `dplaneos_group_epoch`, `dplaneos_group_problems` | per storage group (label `group`) |

Ready-made alert rules are in `prometheus/ha-alerts.yml` (not quorate, self-fence armed, no automatic failover, member missing, group not served, group problems, owner changed). See [ALERTS.md](ALERTS.md).

---

## 8. Ports

| Port | Between | Used by |
|---|---|---|
| UDP 5405 | all cluster members | Corosync |
| TCP 5403 | nodes → QDevice | third vote (QDevice) |
| TCP 443 / 80 | node ↔ node | configuration sync, storage group coordination, replication streams |
| Floating addresses | clients → owner | SMB, NFS, iSCSI, NVMe-oF |

---

## 9. Samba clustering (CTDB)

CTDB is optional and experimental: it shares Samba's lock and session state between nodes so SMB clients keep byte-range locks across a failover. Without it, SMB clients reconnect after a failover (which modern clients do transparently). See [HA-CTDB-SETUP.md](HA-CTDB-SETUP.md); set `services.dplaneos.ctdb.nodes` to the addresses of all nodes.

---

## 10. Limits of this release

- The third vote's certificate (QDevice) and a voter's first configuration are exchanged over the node's HTTPS without checking its (usually self-signed) certificate; the one-time code protects that exchange, and voters pin the node's key from then on.
- Storage groups of more than one pool move together; a pool belongs to one group.
- Replicated groups replicate the whole pool (`zfs send -R`); datasets cannot be excluded.

For the design behind all this, see [Design 0001](../design/0001-distributed-state-gitops-ha.md) and [ADR-0009 (fencing layers)](../design/adr/ADR-0009-fencing-layers.md).
