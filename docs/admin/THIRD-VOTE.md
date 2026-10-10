# Choosing a Third Vote

A cluster can only fail over **automatically** when a majority of votes agrees that the owner of a storage group is gone. Two nodes alone cannot tell "the other node died" from "the cable between us broke": both halves of a split would think they are in charge. So a two-node cluster in DPlaneOS fails over only on request (*Take over*), and anything that adds a third vote turns automatic failover on.

The third vote stores no data and serves no clients. It only has to be **always on** and **reachable from every node**. This page compares the ways to provide it, so you can pick what fits the hardware you have.

All paths start the same way: System › High Availability › **Cluster quorum** › *Add a third vote* (or *Add a vote or node* once you have three). The dialog shows the four paths below with a one-time code and the command or steps for each. The code works once and expires after 30 minutes.

---

## At a glance

| | QDevice on a small Linux machine | Voter (Pi / mini PC as a member) | Another DPlaneOS system as QDevice | Third DPlaneOS node |
|---|---|---|---|---|
| What runs there | `corosync-qnetd` | `corosync` | `corosync-qnetd` (inside DPlaneOS) | full DPlaneOS |
| Typical hardware | Raspberry Pi, any VM, NAS container host, cloud instance | Raspberry Pi 3/4/5, mini PC, VM on the same LAN | an existing DPlaneOS box, e.g. at another site | a node like the other two |
| Network | TCP 5403 from every node; latency up to ~100 ms is fine; can be at another site or over a VPN | UDP 5405 both ways; same LAN, latency below ~2 ms like the nodes | TCP 5403 from every node | UDP 5405, same LAN |
| Votes | 1 (for 2 nodes) | 1 | 1 | 1, and it can own storage |
| Serves several clusters | yes | no | yes | no |
| Setup | one command | one command | form in its web interface | pair, then *Add to cluster* |
| Keeps itself up to date | nothing to update (the cluster's members are not part of its configuration) | pulls the cluster configuration every minute | through DPlaneOS | through DPlaneOS |
| Best for | most setups with two nodes | three or more members on one LAN, no extra service model | sites that already run another DPlaneOS | growing to three full nodes |

**If unsure: use a QDevice on a small Linux machine.** It is what Proxmox recommends for two-node clusters, it is the lightest, and it keeps working over a slow or distant link.

---

## 1. QDevice on a small Linux machine

**What it is.** `corosync-qnetd` is a tiny vote server. Each node runs `corosync-qdevice`, which connects to it over TLS. In a split, the QDevice gives its vote to exactly one side.

**Hardware.** Anything that runs a current Linux and is always on: a Raspberry Pi (any model from the 3 on, Raspberry Pi OS), a small VM on another host, a container host, a NAS that runs VMs, a cloud instance. It needs a few MB of RAM and almost no CPU.

**Supported systems.** The setup command installs the package with the system's package manager: Debian 11+, Ubuntu 22.04+, Raspberry Pi OS 11+, Fedora, RHEL/Rocky/Alma 8+, openSUSE, Alpine. Elsewhere, install `corosync-qnetd` yourself and run the command again.

**Setup.** On the machine, run the command the dialog shows, for example:

```
curl -fsSk https://nas1.lan/api/quorum/witness-setup.sh | sudo sh -s -- https://nas1.lan dpq_…
```

It installs `corosync-qnetd`, creates its certificate authority, starts it (without restarting it if it already serves other clusters), opens TCP 5403 in ufw or firewalld if one is active, and registers with the cluster. The cluster's certificate is signed on the machine and exchanged with the one-time code: no SSH, no passwords. If another firewall is active, the script says so and the cluster panel warns when port 5403 is not reachable.

**Good:**
- Very light, nothing else to maintain.
- One QDevice can serve several DPlaneOS clusters.
- Tolerates latency: it can live at another site or behind a VPN, which also protects against losing the whole server room.
- Nothing to update when nodes change: the QDevice does not know the member list.

**Limits:**
- It only votes. If it is down, the cluster keeps running with two votes and automatic failover is off until it is back (the panel and the alert `DPlaneOSNoAutomaticFailover` say so).
- With an odd number of members (three or more full members) a QDevice is not recommended: it can only add the "last man standing" algorithm (lms), which favours keeping one partition alive over a clean majority. DPlaneOS picks the algorithm automatically (ffsplit for an even member count, lms otherwise).
- Its certificate is exchanged over the node's HTTPS without checking the node's (usually self-signed) certificate; the one-time code protects that exchange.

---

## 2. Voter: a Raspberry Pi or mini PC as a full member

**What it is.** The machine runs `corosync` itself and becomes a member of the cluster with its own vote, like a node that never owns storage. DPlaneOS marks it as a *Voter* in the member list.

**Hardware.** A Raspberry Pi 3/4/5 or a mini PC on the **same LAN** as the nodes, or a VM on a host in that LAN.

**Supported systems.** Corosync 3 (knet), which means Debian 11+, Ubuntu 22.04+, Raspberry Pi OS 11+, Fedora 34+, RHEL 9. Older corosync 2 cannot join; the script warns.

**Setup.** On the machine, run the voter command the dialog shows:

```
curl -fsSk https://nas1.lan/api/quorum/witness-setup.sh | sudo sh -s -- --voter https://nas1.lan dpq_…
```

It installs `corosync`, joins the cluster with the code (the node adds it to the cluster and sends it the configuration and the cluster key), opens UDP 5405 in ufw or firewalld, starts corosync, and installs a timer (`dplaneos-voter-sync.timer`) that pulls the cluster configuration every minute. If the node is reached over HTTPS and `openssl` is available, the node's TLS key is pinned at this moment and every later pull checks it.

**Good:**
- A plain corosync member: the vote count is simply the number of members, no separate vote service and no certificate authority.
- Good when you run three or more members on one LAN, where a QDevice is not recommended.
- Its configuration follows the cluster: when you add or remove nodes, it picks up the new member list within a minute.

**Limits:**
- Needs a low-latency LAN (below ~2 ms) and UDP 5405 both ways, exactly like the nodes. Do not put it at another site or behind a VPN; use a QDevice there.
- It must run corosync 3, compatible with the nodes'.
- It pulls from the node it joined. If that node is replaced for good, run the command again (with a new code) on the voter.
- If the voter is down, the remaining votes decide as usual; with two nodes and one voter, losing the voter turns automatic failover off until it is back.

**Removing.** In the member list, use the remove button on the voter, then on the machine: `sudo systemctl disable --now corosync dplaneos-voter-sync.timer`.

---

## 3. Another DPlaneOS system as QDevice

**What it is.** The same vote server as path 1, run by a DPlaneOS system you already have (for example a backup NAS at another site).

**Setup.** On the other system, open System › High Availability › **Serve as third vote**, enter the cluster node's address and the code from the dialog, and register. That system then lists the clusters it serves.

**Good:**
- No extra machine and no command line.
- Same latency tolerance as path 1: a second site is ideal.

**Limits:**
- That system must stay up and reachable on TCP 5403 from every node; its own storage is not involved.
- Same certificate note as path 1.

---

## 4. A third DPlaneOS node (n+1)

**What it is.** Another full DPlaneOS node joins the cluster. It votes, and it can be a candidate of storage groups: it can own pools and take over from the others.

**Setup.**
1. Install DPlaneOS on it and pair it with one of the nodes in System › Configuration Sync.
2. On a cluster member: *Add a vote or node* › **A third DPlaneOS node**, choose it, check the suggested cluster address, and *Add to cluster*.
3. Add it as a candidate to the storage groups it should be able to take over.

**Good:**
- Real redundancy: three nodes need no other vote, and any one of them can fail.
- It can carry load: storage groups can be spread over all nodes.

**Limits:**
- Needs the hardware of a node and, for shared storage, access to the same disks.
- With four DPlaneOS nodes, the vote count is even again: add a QDevice or a voter.

---

## Which one for my setup?

| Your setup | Recommended |
|---|---|
| Two nodes in one rack, nothing else | A Raspberry Pi as QDevice (path 1) |
| Two nodes, a VM host elsewhere in the building or at another site | A small VM as QDevice (path 1) |
| Two nodes, an existing DPlaneOS box at another site | Path 3 |
| Two nodes, want a third full member but no third server | A Pi or mini PC as voter (path 2) |
| Three or more servers | Path 4; for an even count add a QDevice or voter |
| No third device possible | Run with two votes: everything works, failover is *Take over* by hand |

---

## What happens when the third vote is down

| Situation | Effect |
|---|---|
| QDevice or voter down, both nodes fine | Nothing changes for storage or clients. Automatic failover is off until it is back; the cluster panel shows *Not voting* / *Not reachable*, the alert `DPlaneOSNoAutomaticFailover` fires after 15 minutes. |
| QDevice/voter down **and** then a node fails | No automatic failover. Use *Take over* on the surviving node after making sure the failed node is off. |
| Network split with a working third vote | Exactly one side is quorate. A storage owner on the other side stops resetting its watchdog and is reset; the quorate side takes its groups over after the fencing delay. |

See [HA-FAILURE-MODES.md](HA-FAILURE-MODES.md) for these and other failure scenarios step by step.

---

## Ports

| Port | Between | Used by |
|---|---|---|
| UDP 5405 | all members (nodes and voters) | corosync (knet) |
| TCP 5403 | every node → QDevice | corosync-qdevice → corosync-qnetd |
| TCP 443 / 80 | QDevice/voter → the node it registers with | setup and, for voters, the configuration pull |
