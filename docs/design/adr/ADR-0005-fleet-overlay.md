# ADR-0005: Fleet management as a loose overlay

| | |
|---|---|
| **Status** | Proposed |
| **Date** | 2026-10-05 |
| **Design** | [Design 0001, section 5.7](../0001-distributed-state-gitops-ha.md) |

## Decision

Fleets are managed by an optional **overlay** role that registers independent nodes and clusters as remotes (API tokens), distributes desired configuration per scope, orchestrates rollouts and can own the Git integration. Remotes keep working without it.

## Context

A storage fabric of hundreds of servers cannot be one consensus domain: a partition would stop management everywhere, and membership churn grows with size. Proxmox Datacenter Manager manages many independent clusters this way; a down manager does not affect them.

## Details

- The overlay is a D-PlaneOS role that any node or cluster can enable; dedicated hosts are an option.
- It holds fleet scope, pushes or offers revisions to remotes, shows inventory and health, and runs rollouts (canary, waves).
- Remotes accept fleet-scope changes like any other revision source, subject to the merge rules (ADR-0006).
- The overlay can be made highly available by running it on a cluster.

## Consequences

- No fleet-wide strong consistency: fleet changes reach remotes when they are reachable.
- **Status: open question** — role on any node (default) versus a separate product.

## Alternatives considered

- **One big cluster:** does not scale; couples failures.
- **No fleet layer (Git only):** pushes the coordination problem onto every node and excludes non-Git users.
