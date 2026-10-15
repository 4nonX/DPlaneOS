# ADR-0005: Fleet management as a loose overlay

| | |
|---|---|
| **Status** | Accepted; implemented in phase 6 ([FLEET.md](../../admin/FLEET.md)) |
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
- Decided: a role any node can switch on, not a separate product.

## Implementation (phase 6)

- Remotes are registered with an API token (admin scope) of the remote; the remote's TLS certificate is pinned when it is added.
- Fleet scope covers users, groups and the shared settings. On the remote, the fleet's version wins and a local change made since is kept as a proposal (section 5.6, cluster/fleet rule); the change is recorded with origin `overlay`.
- Rollouts in waves with soak time, automatic or manual continuation, three attempts per remote, pause on failure, no automatic revert. On a cluster only the coordinator receives the change; the members get it through the cluster's synchronization.
- The manager's inventory poll tells each remote who is calling; a remote that has not heard from its manager for five minutes shows "fleet manager unreachable".
- Not implemented: the manager's own data (remotes, fleet resources, rollouts) is kept on the manager node only, so running the role on a cluster does not make it highly available; the fleet does not own a Git repository. Both are possible follow-ups and do not affect the remotes.

## Alternatives considered

- **One big cluster:** does not scale; couples failures.
- **No fleet layer (Git only):** pushes the coordination problem onto every node and excludes non-Git users.
