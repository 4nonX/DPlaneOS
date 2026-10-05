# ADR-0002: Revision-based config store as source of truth; Git optional

| | |
|---|---|
| **Status** | Proposed |
| **Date** | 2026-10-05 |
| **Design** | [Design 0001, section 5.1, 5.8](../0001-distributed-state-gitops-ha.md) |

## Decision

The source of truth for desired configuration is a **revision log in the node-local store**. Git is an optional backend per scope that exports revisions as commits and imports commits as revisions. `state.yaml` remains the export/import format.

## Context

DPlaneOS promises that system state is derivable from two text files (PHILOSOPHY.md), and the GitOps guide treats `state.yaml` in Git as the single source of truth. At the same time most users operate through the GUI, many never use Git, and a Git outage must not block changes. The current implementation regenerates `state.yaml` from live state on every GUI change and pushes inline, which loses changes on push failures and cannot represent pending or conflicting changes.

## Details

- Every change becomes a revision with scope, resource key, payload, base revision, origin and epoch (design section 5.1).
- The applied tables remain the projection the daemon reads; history, diff and rollback come from the revision log, on every deployment profile.
- The current revision of any scope can always be exported as `state.yaml` and imported back, with or without Git: the "two text files" property becomes an export guarantee.
- Git integration works through an outbox (ADR-0007), never inline in the request.

## Consequences

- Users without Git get history, diff and rollback in the GUI.
- PHILOSOPHY.md and GITOPS-DRIVEN-NAS.md must be updated: Git is the recommended long-term record, not a requirement.
- Existing `state.yaml` repositories are imported as the first revision.

## Alternatives considered

- **Git as the only source of truth:** excludes non-Git users and makes Git availability a runtime dependency.
- **Mutable rows without history:** no way to express base revisions, so conflicts cannot be detected reliably.
