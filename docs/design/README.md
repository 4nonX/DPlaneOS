# Design Documents and Architecture Decisions

Design documents describe larger changes to how DPlaneOS works before they are implemented. Architecture Decision Records (ADRs) record one decision each, with its context and the alternatives that were rejected.

## Design documents

| # | Title | Status |
|---|---|---|
| [0001](0001-distributed-state-gitops-ha.md) | Distributed State, GitOps and High Availability | Proposed |

## Architecture decisions

| ADR | Decision | Status |
|---|---|---|
| [ADR-0001](adr/ADR-0001-local-first-node-state.md) | Local-first node state | Proposed |
| [ADR-0002](adr/ADR-0002-config-store-source-of-truth.md) | Revision-based config store as source of truth; Git optional | Proposed |
| [ADR-0003](adr/ADR-0003-scopes-and-storage-groups.md) | Scopes and storage groups | Proposed |
| [ADR-0004](adr/ADR-0004-quorum-corosync.md) | Corosync votequorum for cluster and storage-group quorum | Proposed (open question) |
| [ADR-0005](adr/ADR-0005-fleet-overlay.md) | Fleet management as a loose overlay | Proposed (open question) |
| [ADR-0006](adr/ADR-0006-merge-engine.md) | One merge engine; conflicts are never resolved silently | Proposed |
| [ADR-0007](adr/ADR-0007-gitops-defaults.md) | GitOps defaults: outbox, pull, no automatic revert | Proposed |
| [ADR-0008](adr/ADR-0008-secrets-and-credentials.md) | Secrets keys per group; no secrets in Git | Proposed |
| [ADR-0009](adr/ADR-0009-fencing-layers.md) | Watchdog self-fencing as the baseline; stronger fencing optional; inform, do not block | Proposed |

## Process

1. A design document starts as **Proposed** and links the ADRs it depends on.
2. Each ADR has one decision. Its status moves from **Proposed** to **Accepted**, **Rejected** or **Superseded by ADR-NNNN**; it is never rewritten after acceptance. A changed decision gets a new ADR.
3. Implementation work references the ADR it implements in its commit messages.
4. When a design is implemented, the user-facing guides (`docs/admin`, `docs/reference`) are updated and the design document's status becomes **Implemented**.
