# ADR-0003: Scopes and storage groups

| | |
|---|---|
| **Status** | Proposed |
| **Date** | 2026-10-05 |
| **Design** | [Design 0001, section 5.2, 5.3](../0001-distributed-state-gitops-ha.md) |

## Decision

Configuration is organised in four **scopes** (node, group, cluster, fleet). The **storage group** (one or more pools, their resources and a list of candidate nodes) is the unit of ownership and failover, with a per-group **topology**: standalone, shared storage (SAS or SATA JBOD, SAN, NVMe-oF), or replicated (any drives).

## Context

A single `state.yaml` mixes cluster-wide resources with node-specific ones (hostname, interfaces, tuning). Today's HA treats the whole pair as one "active node", which does not scale to a fabric where different nodes own different pools, and gives no clean place for node-only settings. Users must be able to choose the topology per use case.

## Details

- Scopes and owners: node (the node), group (the owner, i.e. epoch holder), cluster (the cluster coordinator), fleet (the overlay). Effective config merges fleet ⟶ cluster ⟶ group ⟶ node; the GUI shows the origin of every effective setting.
- HA topology and fencing settings are node scope and excluded from Git.
- Group-scope resources are defined on every candidate node and active only on the owner (stacks using the group's pools, NVMe-oF exports, shares, NFS exports). Node scope is limited to what really belongs to one machine (hostname, interfaces, BMC credentials, tuning, SMART tasks, stacks without pool volumes).
- Topologies (design section 5.3): standalone; shared storage with SCSI-3 PR where the disks support it (proposed: ZFS multihost plus mandatory power fencing where they do not, e.g. SATA), watchdog and IPMI/PDU fencing, zero RPO; replicated with ZFS send/receive, an RPO of one interval shown in the GUI, automatic direction flip on planned moves, and promotion during a partition only when configured with witness quorum.
- Apply, config writes in group scope and sync carry the group epoch; a stale epoch cannot write.
- A reconcile failure never triggers a failover; a failover never triggers a revert.

## Consequences

- `state.yaml` gains a scope layout (`fleet/`, `clusters/<id>/`, `groups/<id>/`, `nodes/<host>.yaml`); single-node repositories stay a single file.
- HA Path A' maps to a two-node shared-storage group, Path B to a two-node replicated group.

## Alternatives considered

- **One active node per cluster:** cannot express a fabric with many owners; fails over everything at once.
- **Per-resource ownership:** too fine-grained; pools are the real unit of storage ownership.
