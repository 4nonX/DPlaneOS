# ADR-0007: GitOps defaults — outbox, pull, no automatic revert

| | |
|---|---|
| **Status** | Proposed |
| **Date** | 2026-10-05 |
| **Design** | [Design 0001, section 5.8](../0001-distributed-state-gitops-ha.md) |

## Decision

Git integration uses an **outbox** drained by the scope's Git owner, **pull-based** apply with optional signature verification, Argo CD-style safe defaults and a **hold** instead of automatic revert on failure.

## Context

Today GUI changes push inline from a background goroutine (failures only logged, failed rebases ignored), remote changes are never fetched, and every HA node commits independently. Argo CD and comin show proven defaults: self-heal and prune off, an empty-state guard, retry with backoff, no automatic rollback, pull per machine, signed commits, several remotes.

## Details

- GUI change → revision + outbox entry in one transaction; the Git owner (overlay, or group owner without overlay) drains the outbox idempotently. The local clone is disposable.
- Repository policy chosen in the GUI: *direct* (409 when the remote changed the same resource since the base revision) or *pull request* (`scope/<id>` branches, "pending PR" state).
- Apply: fetch on schedule or webhook → verify signatures (optional) → plan → apply. Self-heal off, prune off, BLOCKED destructive items (existing), BLOCKED empty-state plans, retry with backoff.
- On failure: **hold** the scope at its last good revision, pause auto-apply, report (GUI, alert, commit status); "Revert" is a one-click action, never automatic.
- Several remotes per repository.

## Consequences

- Implements the "Auto-Apply on Push" that GITOPS-DRIVEN-NAS.md documents but the code does not provide.
- Pull-request mode needs provider APIs (GitHub, Gitea/Forgejo, GitLab).

## Alternatives considered

- **Inline push from the request:** loses changes on failure.
- **Automatic `git revert`:** flaps across nodes and can loop.
- **Push-based deployment from a central CI:** requires inbound access to every node and makes CI a runtime dependency.
