# ADR-0008: Secrets keys per group; no secrets in Git

| | |
|---|---|
| **Status** | Accepted, amended 2026-10-10 (no shared key) |
| **Date** | 2026-10-05 |
| **Design** | [Design 0001, section 5.9](../0001-distributed-state-gitops-ha.md) |

## Amendment (2026-10-10)

Implemented without a shared group key: every node keeps its own secrets key, and secret **material** travels between paired nodes when a revision needs it, encrypted with a key derived from the pair's peer secret (HKDF-SHA256, AES-GCM, bound to the resource) and checked against the revision's plaintext fingerprint before the receiver seals it under its own key. Reasons: no key distribution or key rotation across nodes, a compromised node does not expose the other nodes' sealed data at rest, and pairing already provides an authenticated channel. The parts on Git below are unchanged.

## Decision (original)

Shared configuration is sealed with a **group/cluster secrets key** established at join time; node-only secrets keep a node key. Secrets are never stored in Git — only references. Git credentials exist only where Git I/O happens, with separate read and write keys.

## Context

Each node generates its own `/var/lib/dplaneos/secrets.key`. In today's HA the sealed values live in the shared database, so after a failover the new active node cannot decrypt Git tokens, SSH keys or LDAP passwords. TrueNAS explicitly sends its secret-encryption key to the peer controller. The `state.yaml` schema also accepts `password_hash` and `bind_password` from Git, inviting plain-text secrets in repositories.

## Details

- Join flow: a single-use, short-lived join token from the GUI establishes trust and transfers the group key to the new member.
- `state.yaml` carries references such as `secret://ldap-bind`; plain-text secret fields are rejected. Teams may use sops/age-encrypted files instead.
- Read-only deploy keys for pulling; write keys limited by branch protection to `scope/<id>/*`; optional commit signing per node or overlay.
- Interim fix (Phase 0): share the existing key between the two nodes of current HA pairs.

## Consequences

- Rotation of the group key must re-seal all group secrets; the GUI needs a rotation action.
- Existing repositories with plain-text secrets fail validation until migrated; the import offers to move them into the local store.

## Alternatives considered

- **One key per node with re-encryption on sync:** every sync must decrypt and re-encrypt, and a failed re-encryption silently breaks secrets on the peer.
- **Secrets in Git, encrypted by default:** key distribution problem moves to every repository user.
