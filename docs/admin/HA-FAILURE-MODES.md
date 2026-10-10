# HA Failure Modes and Recovery

What happens in a DPlaneOS cluster when something fails, what you see, what the system does on its own, and what you do. The model is described in [HIGH-AVAILABILITY.md](HIGH-AVAILABILITY.md); the third vote in [THIRD-VOTE.md](THIRD-VOTE.md).

**Where to look first:**

| Place | Shows |
|---|---|
| HA › Cluster quorum | members online, votes, whether automatic failover is possible and why not |
| HA › How this setup is protected | protection layers, the fencing delay, a warning when this node is about to reset |
| HA › Storage groups | owner, epoch, *Serving* / *Not serving*, problems and countdowns per group |
| System › Configuration Sync | pairing state (connected, isolated, independent), changes waiting, conflicts |
| `journalctl -u dplaned` | lines starting with `QUORUM:`, `GROUPS:`, `HA WATCHDOG:`, `CONFIG SYNC:` |
| `corosync-quorumtool -s` | Corosync's own view (run on a node) |

---

## 1. The owner of a storage group crashes or loses power

**With a third vote and the watchdog (or power fencing):**
- You see on the other node: the group's line shows *taking over at HH:MM:SS* (the fencing delay: watchdog timeout + 15 s).
- On its own: after the delay the other candidate (with power fencing: after powering the old owner off) imports the pools, raises the epoch, takes the floating address and starts the group's apps. Clients reconnect to the same address.
- You: nothing. When the old owner comes back, it sees the higher epoch and stays standby (see 5).

**With two votes (no third vote):**
- You see: the cluster panel says automatic failover is off (*no third vote*); the group's line on the surviving node offers **Take over**.
- On its own: nothing; two votes cannot tell a crash from a broken link.
- You: make sure the failed node is really off (or disconnected from the shared disks), then **Take over**. ZFS multihost still refuses the import if the old owner is in fact writing.

**Replicated group with automatic failover off (the default):** the same as with two votes: **Take over** makes this node's copy writable; changes since the last replication (shown on the group's line) are lost.

## 2. The network between the nodes breaks (split)

**With a third vote:**
- Exactly one side keeps quorum (the one that still reaches the third vote, or the larger part).
- On the other side: a node that owns a group sees it has no quorum, stops resetting its watchdog (`HA WATCHDOG: … no longer resetting the watchdog`; the protection card shows a red warning) and is restarted by the kernel. The quorate side takes its groups over after the fencing delay, as in 1.
- Configuration: both sides keep working and keep changes locally; Configuration Sync shows *isolated* with the number of changes waiting. After the network is back they merge; conflicts appear on the Configuration Sync page.
- You: repair the network. Check Configuration Sync for conflicts.

**With two votes:** both halves stay quorate (two-node mode), nobody fails over, and both keep serving what they own. Each node keeps its configuration; changes merge when the link is back. This is why automatic failover needs a third vote.

## 3. The third vote is down (QDevice or voter)

- You see: the cluster panel shows the third vote as *Not voting* / *Not reachable*; *automatic failover is off*; the alert `DPlaneOSNoAutomaticFailover` fires after 15 minutes.
- On its own: nothing changes for storage or clients.
- You: bring it back (power, network, `systemctl status corosync-qnetd` or `corosync` on it). If a node fails meanwhile, failover is manual (*Take over*).
- A QDevice that does not vote although it is running: check TCP 5403 from every node; the enrollment already warns when the port is closed.
- A voter that does not show up after nodes were added: it pulls its configuration every minute; see `journalctl -u dplaneos-voter-sync` on it. If the node it joined has been replaced, run the setup command again with a new code.

## 4. Both nodes are off and start again (power cut)

- Corosync needs a majority to become quorate: with two nodes and a third vote, any two of the three; with two nodes and no third vote, both nodes (two-node mode waits for all at first start).
- Pools of shared groups are not imported at boot. When a node is quorate and owns a group, its daemon imports the pools once the other candidates confirm they do not have them, or once a silent candidate has been absent from the cluster for the fencing delay. The group's line says what it waits for.
- Replicated pools are each node's own and are imported normally; the copies of non-owners stay read-only.
- You: usually nothing. If only one node can come back and there is no third vote, the group's line on the owner offers **Import here** (or **Take over** on a non-owner); use it when the other node is definitely off.

## 5. A former owner comes back after a takeover

- You see: on the returning node the group shows the new owner and a higher epoch within seconds of reconnecting; the node is standby.
- On its own: it releases anything it still holds for that group (for shared storage it cannot import the pool anyway: the new owner holds it and multihost refuses); its apps for the group stay stopped.
- Replicated: if its copy was changed after the last replication (it kept working during a split), replication to it stops and its line offers **Discard the changes here**. Copy off what you need first; discarding rolls the copy back to the owner's data and replication resumes.

## 6. A node restarts by itself every few minutes

- Likely cause: it owns a storage group, loses quorum, and the watchdog restarts it (as designed), for example because its cluster network is down while its management network works.
- You: check `corosync-quorumtool -s` and the cluster network (UDP 5405). As a stop-gap, move its groups to another node (or, without a third vote, remove the cluster); disabling the watchdog on this node also stops the restarts but removes the protection.

## 7. A shared pool cannot be imported

- *activity check* / *pool is in use from another system*: another node still writes to it (ZFS multihost). Find out which node imports it (`zpool list` on each); if a node that should not have it does, move or take the group over properly instead of forcing.
- *waiting for <node>* on the group's line: the owner waits for the other candidate to confirm it does not use the pool. Make that node reachable, or use **Import here** if it is off.
- Disk paths missing on one node: check the shelf cabling (`ls /dev/disk/by-id`); a group can only move to a node that sees all its disks.

## 8. The floating address does not move

- The owner holds the address only while it *serves* the group (quorate, pools imported). The group's line shows *floating address* problems, for example a wrong interface name on one node (the interface must have the same name on every node).
- Clients still talk to the old node: their ARP cache; the new owner sends gratuitous ARP when it takes the address, but some switches and clients cache for a while.

## 9. An app does not start on the new owner

- The group's line lists the problem, often *image not found*: container images must be available on every candidate (registry, or `docker load` on each node).
- Stacks that use the group's pools are edited on the owner; edits on a non-owner are overwritten by the owner's definitions.

## 10. Configuration changes do not arrive, or wait

| Shown on Configuration Sync | Meaning | You |
|---|---|---|
| *isolated*, N changes waiting | the other node is not reachable | repair the network; they merge when it is back |
| conflict | the same resource was changed on both nodes | choose this node's version, the other's, or edit a combined one |
| waiting: pool X is not imported | the change belongs to a storage group owned elsewhere | nothing: it is applied when this node owns the group |
| waiting for the secret from … | a password, key or credential is being fetched from the node that changed it | nothing; if it persists, check that node is reachable |
| the secret changed again on … | the other node changed the secret once more | nothing: the newer change follows |

## 11. Corosync does not start or the nodes do not see each other

- `journalctl -u dplaneos-corosync` on the node; `corosync-cfgtool -s` shows link status.
- UDP 5405 must be open between all members (the NixOS module opens it on DPlaneOS nodes; check firewalls in between and on voters).
- The cluster addresses must be reachable from each other; change them by removing and re-forming the cluster if the network changed.

## 12. Disk reservations (SCSI-3 PR) report errors

Disk reservations are an optional layer for shared SAS disks. If the probe on the protection card reports disks without support, those disks rely on ZFS multihost and the watchdog alone. Run *Probe PR Support on Pool Disks* again (HA › Watchdog and power fencing) after changing the shelf.

---

**Collect information for a bug report:** System › Support Bundle (includes the daemon log), plus the output of `corosync-quorumtool -s` on each node and a screenshot of the HA page.
