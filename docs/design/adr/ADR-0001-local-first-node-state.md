# ADR-0001: Local-first node state

| | |
|---|---|
| **Status** | Proposed |
| **Date** | 2026-10-05 |
| **Design** | [Design 0001, section 5.1](../0001-distributed-state-gitops-ha.md) |

## Decision

Every node keeps its complete configuration in its own PostgreSQL database and can operate, and be managed, without any other machine. `dplaned` never depends on a database on another node.

## Context

In HA mode today, both daemons write through HAProxy to one Patroni primary (`dbDSN` default `localhost:5000`). A node cut off from that primary cannot save configuration, and Patroni demotes a primary that loses the etcd majority, so the node serving storage can lose its own management during a network glitch. Users expect TrueNAS-style independent operation: a node must keep serving and stay manageable whatever happens to the network.

## Details

- Each node runs a local PostgreSQL (`database.createLocally`); configuration is shared between nodes by **replicating revisions** (ADR-0002), not by sharing a database.
- Node states: **connected**, **isolated** (automatic) and **independent** (operator-detached). Isolation never makes configuration read-only.
- What an isolated node may change is limited by scope and topology (design section 5.5); storage arbitration (reservations, quorum) decides about shared data, never mere network reachability.
- Offline operation includes a local break-glass administrator, cached directory credentials with a lifetime, the cached group secrets key and locally running timers.

## Consequences

- Patroni, HAProxy and etcd are no longer required for `dplaned` (they may remain for other purposes).
- Configuration sync becomes an explicit protocol with conflict handling (ADR-0006) instead of an implicit property of a shared database.
- Existing HA installations need a migration from the shared database to node-local databases (design section 6).

## Alternatives considered

- **Keep the shared database:** incompatible with independent operation.
- **Proxmox model (replicated config, read-only without quorum):** a lost quorum would block management of a healthy node.
