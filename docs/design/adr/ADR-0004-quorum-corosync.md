# ADR-0004: Corosync votequorum for cluster and storage-group quorum

| | |
|---|---|
| **Status** | Proposed |
| **Date** | 2026-10-05 |
| **Design** | [Design 0001, section 5.4](../0001-distributed-state-gitops-ha.md) |

## Decision

Clusters use **Corosync votequorum** for membership and quorum, with `two_node` or a **QDevice** for two-node deployments. Storage groups derive their owner from it, combined with watchdog self-fencing and, where available, topology-specific arbitration (reservations, ZFS multihost) ([ADR-0009](ADR-0009-fencing-layers.md)). There is no fleet-wide consensus.

## Context

Today quorum comes from HTTP reachability witnesses and, for Patroni, etcd. Reachability is not agreement, and etcd needs three voters and has no two-node mode. Proxmox VE and Pacemaker use Corosync for exactly this role, with tested options for two-node clusters (`two_node`, `wait_for_all`), tie breaking (`auto_tie_breaker`), shrinking clusters (`last_man_standing`) and an external vote (QDevice).

## Details

- Corosync votequorum per cluster; QDevice optional (Raspberry Pi, VM, another D-PlaneOS node, small cloud instance).
- Two nodes without a QDevice run `two_node` (with `wait_for_all`) for membership only: both halves of a split stay quorate, so automatic failover requires the QDevice; without it, takeover is manual ([ADR-0009](ADR-0009-fencing-layers.md)).
- The group epoch increments on every promotion and is stored in the revision log and, for shared storage, in the reservation key, so authority can be checked locally.
- The HTTP witnesses remain as an additional isolation signal, not as quorum.
- The GUI presents the votequorum options in plain language.

## Consequences

- Adds Corosync and, optionally, corosync-qnetd to the NixOS modules.
- Removes the dependency on etcd for daemon-level quorum.
- **Status: open question** — confirm Corosync, or keep the etcd witness for shared-storage pairs.

## Alternatives considered

- **etcd:** three voters minimum, no two-node mode.
- **Embedded Raft in dplaned:** our own consensus code to maintain and test.
- **HTTP witnesses only:** proves reachability, not agreement; unsafe beyond two nodes.
