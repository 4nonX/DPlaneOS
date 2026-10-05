# ADR-0006: One merge engine; conflicts are never resolved silently

| | |
|---|---|
| **Status** | Proposed |
| **Date** | 2026-10-05 |
| **Design** | [Design 0001, section 5.6](../0001-distributed-state-gitops-ha.md) |

## Decision

A single merge engine handles every source of concurrent change: reconnect after a partition, GUI versus Git, and pull requests. Different resources merge automatically; the same resource changed on both sides goes to a review screen. Data on diverged replicas is never merged.

## Context

Without a defined policy, concurrent changes are decided by timing (whichever push wins a rebase). The v14.8.0 fixes removed many places where failures were hidden; conflicts must not become the next silent failure.

## Details

- Three-way merge per resource using the base revision recorded with every change.
- Default winners per scope (configurable): node scope → the node; group scope → the current epoch holder; cluster/fleet → the coordinator/overlay, with the local change kept as a proposal, never dropped.
- Diverged replicated data: the operator chooses the canonical copy; the other side is preserved as `<dataset>@split-<time>` and a clone before replication resumes.

## Consequences

- One implementation and one test suite instead of three.
- The GUI needs a review screen and a proposals list.

## Alternatives considered

- **Last writer wins:** silently loses changes.
- **CRDTs:** converge automatically, which hides conflicts an operator must see.
