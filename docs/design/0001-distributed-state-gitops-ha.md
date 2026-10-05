# Design 0001: Distributed State, GitOps and High Availability

| | |
|---|---|
| **Status** | Proposed |
| **Created** | 2026-10-05 |
| **Decisions** | [ADR-0001](adr/ADR-0001-local-first-node-state.md) to [ADR-0008](adr/ADR-0008-secrets-and-credentials.md) |
| **Supersedes (when accepted)** | The shared-database model in [HIGH-AVAILABILITY.md](../admin/HIGH-AVAILABILITY.md); parts of [GITOPS-DRIVEN-NAS.md](../admin/GITOPS-DRIVEN-NAS.md) |

This document describes how DPlaneOS keeps configuration consistent across one node, an HA pair, a cluster and a fleet of hundreds of servers, how Git fits in, and how every node keeps working on its own when the network does not. It is the reference for the ADRs listed above; each ADR records one decision and its alternatives.

---

## 1. Goals and non-goals

### Goals

1. **One codebase from homelab to fabric.** The same software serves a single node, a two-node HA pair, a cluster and a 200-server storage fabric. Larger deployments add roles; they do not switch products.
2. **Every node can operate independently.** A network glitch, a dead switch, a lost controller or an unreachable Git server never takes away a node's ability to serve storage or to be managed from its own GUI ("TrueNAS-style").
3. **Power of choice.** Topology (standalone, shared SAS, replicated), failover behaviour, conflict policy and the use of Git are per-deployment choices, not fixed assumptions.
4. **GUI-first.** Everything, including cluster setup, topology choice, fencing, conflict resolution and Git integration, is manageable from the web UI in plain language. Git and the CLI are optional power tools, never requirements.
5. **No silent outcomes.** No change is lost, overwritten or half-applied without the operator seeing it. (The bug classes fixed in v14.8.0, where failures were logged while the API reported success, are exactly what this rules out at the architecture level.)
6. **Data safety before availability.** Two nodes never write the same disks; data on diverged replicas is never merged automatically.

### Non-goals

- A distributed file system or scale-out storage (Ceph, Gluster). Storage groups stay ZFS pools owned by one node at a time.
- Fleet-wide strong consistency. Large deployments are loosely coupled by design (see [ADR-0005](adr/ADR-0005-fleet-overlay.md)).
- Merging user data. Diverged datasets are preserved and the operator chooses; DPlaneOS never merges file contents.

---

## 2. Current state (v14.8.0)

What exists today, with the problems that motivate this design. Code references are to `daemon/` unless noted.

| Area | Today | Problem |
|---|---|---|
| Daemon database in HA | Both nodes write through HAProxy (`127.0.0.1:5000`) to the single Patroni primary (`nixos/module.nix`, `dbDSN` default). | A node cut off from the primary cannot save configuration; its GUI is effectively unusable. Patroni demotes a primary that loses the etcd majority, so the node serving storage can lose its own management during a glitch. Independent operation is impossible. |
| GUI → Git | Every UI write calls `gitops.CommitAllAsync`, which regenerates the whole `state.yaml` from live state, commits, runs `git pull --rebase` and pushes (`internal/gitops/commit.go`, `git_util.go`). | A failed rebase is logged and ignored (the clone can stay mid-rebase); a failed push is only logged by a background goroutine. Runs on every node, including an HA standby with no pools imported, which would push a state without pools. |
| Git → node | The drift detector compares against the local clone only; its fetch is a placeholder (`internal/gitops/drift.go`). Apply is manual (`POST /api/gitops/apply`) or `dplaned -apply`. | Remote changes are never fetched. [GITOPS-DRIVEN-NAS.md](../admin/GITOPS-DRIVEN-NAS.md) documents "Auto-Apply on Push" (polling and webhook), which has no implementation (`auto_apply` appears only in a comment in `internal/gitops/diff.go`). |
| Apply safety | The diff engine classifies destructive items as BLOCKED (destroying a dataset with data, removing a share with active connections) and never applies them automatically. | Sound; kept. |
| HA ownership guard | Manual apply checks `clusterMgr.Status().Quorum` (`cmd/dplaned/main.go`). | In a healthy pair with a witness both nodes have quorum, so both may apply. Role is not checked. |
| HA quorum | HTTP reachability witnesses (`internal/ha/witness.go`), etcd for Patroni, SCSI-3 PR and SBD fencing, watchdog. | The HTTP witness proves reachability, not agreement; it is not a quorum mechanism for more than two nodes. |
| Secrets | AES key in `/var/lib/dplaneos/secrets.key`, generated per node (`internal/secrets`). Sealed values (Git tokens, SSH keys, LDAP) live in the database. | In HA the database is shared but the key is not: after a failover the new active node cannot decrypt the secrets. |
| `state.yaml` scope | One flat document; `system` mixes cluster-wide settings with node-specific ones (hostname, interfaces, tuning, UPS). | Sharing it between nodes would give them the same hostname. |
| Secrets in Git | The schema accepts `password_hash` and LDAP `bind_password` from Git (`internal/gitops/state.go`); the node never writes them back. | Encourages plain-text secrets in repositories. |

The full list of immediate fixes is Phase 0 in section 9.

---

## 3. Lessons from established systems

None of these systems does everything required here; each solves one part well. The design combines them.

| System | What it does | What we take | What we do differently |
|---|---|---|---|
| **Proxmox VE** (pmxcfs, Corosync, HA manager) | Configuration in a database-backed file system replicated to all nodes over Corosync; **read-only when a node loses quorum**. HA needs 3 nodes or 2 + QDevice; nodes self-fence with a watchdog (60 s). Leaving a cluster officially means reinstalling. | Watchdog self-fencing; QDevice for two-node quorum. | Configuration never becomes read-only because of quorum loss (Goal 2). Leaving and rejoining are GUI actions. |
| **Proxmox storage replication** | Scheduled ZFS send/receive (1 min to 1 week); **replication direction flips automatically** on migration; data loss up to the last run is documented and accepted. | Interval-based RPO shown honestly; automatic direction flip on planned moves. | — |
| **Proxmox Datacenter Manager** | Manages many independent clusters and nodes ("remotes") via API tokens; no shared Corosync ring; remotes keep running when the manager is down. | The fleet model: a loose overlay, not a big cluster ([ADR-0005](adr/ADR-0005-fleet-overlay.md)). | Also distributes desired configuration and optional Git integration. |
| **TrueNAS Enterprise HA** | Dual controllers on dual-ported disks, active/standby, VRRP. **Each controller has its own database**; the MASTER forwards every write to the BACKUP, which applies it only while it is BACKUP, with a full database send as fallback. The secret-encryption key is sent to the peer. | Local database per node with active→standby replication inside a shared-storage group; sharing the secrets key ([ADR-0001](adr/ADR-0001-local-first-node-state.md), [ADR-0008](adr/ADR-0008-secrets-and-credentials.md)). | Generalised from "pair" to "storage group"; the standby stays independently manageable for its node scope. |
| **Corosync votequorum** | `two_node`, `wait_for_all`, `auto_tie_breaker`, `last_man_standing`, QDevice as an external vote. | The quorum layer for storage groups and small clusters ([ADR-0004](adr/ADR-0004-quorum-corosync.md)). | Options exposed in the GUI in plain language. |
| **Argo CD** | Self-heal and prune **off** by default; `allowEmpty` guard; retry with exponential backoff; **no automatic rollback**. | GitOps defaults ([ADR-0007](adr/ADR-0007-gitops-defaults.md)). | — |
| **comin** (NixOS) | Each machine pulls its own configuration from Git by hostname; testing branches; commit signature verification; several remotes. | Pull model, signatures, multiple remotes ([ADR-0007](adr/ADR-0007-gitops-defaults.md)). | Pull is done by the fleet overlay or the group owner, not every node. |

---

## 4. Architecture overview

Three layers, each scaled by the deployment profile.

```
  ┌──────────────────────────────────────────────────────────────────────┐
  │ Fleet overlay (optional)          Git (optional, per scope)          │
  │  - inventory, desired state per   - export revisions as commits      │
  │    scope, rollout, monitoring     - import commits as revisions      │
  │  - never required at runtime      - direct or pull-request policy    │
  └───────────────▲──────────────────────────────────▲───────────────────┘
                  │ API tokens, revision sync        │ (overlay or group owner)
  ┌───────────────┴──────────────────────────────────┴───────────────────┐
  │ Storage group (1..n nodes)                                           │
  │  - topology: standalone | shared SAS | replicated                    │
  │  - owner node via quorum + epoch; fencing per topology               │
  │  - owner replicates group revisions to the other members             │
  └───────────────▲──────────────────────────────────────────────────────┘
                  │
  ┌───────────────┴──────────────────────────────────────────────────────┐
  │ Node (always complete on its own)                                    │
  │  - local PostgreSQL: revision log, applied state, outbox, holds      │
  │  - local GUI, local auth (break-glass admin, cached directory creds) │
  │  - local scheduler (snapshots, scrubs, SMART), local secrets key    │
  └──────────────────────────────────────────────────────────────────────┘
```

### Deployment profiles

| Profile | Nodes | Coordination | Typical user |
|---|---|---|---|
| Standalone | 1 | none | homelab, small office |
| Pair | 2 (+ witness) | Corosync `two_node` or QDevice; fencing per topology | homelab HA, SMB |
| Cluster | 3..~16 | Corosync votequorum per cluster; several storage groups | mid-size |
| Fleet | many clusters/standalone nodes | loose overlay; no fleet-wide consensus | storage fabric, multi-site |

A deployment moves between profiles through GUI actions (join, detach, create storage group), never by reinstalling.

---

## 5. Detailed design

### 5.1 Node-local configuration store ([ADR-0001](adr/ADR-0001-local-first-node-state.md), [ADR-0002](adr/ADR-0002-config-store-source-of-truth.md))

Every node runs its own PostgreSQL (`database.createLocally`, the default since v14.8.0 for non-HA). `dplaned` never depends on a database on another machine.

Desired configuration is kept as a **revision log**, not as mutable rows only:

```
config_revisions
  id               bigserial
  scope_type       node | group | cluster | fleet
  scope_id         text            -- node id, group id, ...
  resource_kind    text            -- dataset, share, user, ...
  resource_key     text            -- stable identity, e.g. "tank/media"
  payload          jsonb           -- desired spec; null = delete
  base_revision    bigint          -- revision this change was made against
  origin           gui | api | git | overlay | peer
  origin_node      text
  epoch            bigint          -- group epoch at write time (group scope)
  author, created_at

applied_state      (scope, resource_key, revision_id, status, applied_at, error)
outbox             (id, revision_id, target: peer|overlay|git, state, attempts, next_try)
holds              (scope, reason, revision_id or commit, since, set_by)
```

- The live tables the daemon uses today (shares, users, schedules, ...) remain the applied projection; the revision log is the history and the unit of sync.
- The GUI gets history, diff and one-click rollback per resource and per scope, on every profile including standalone.
- **The "two text files" promise of [PHILOSOPHY.md](../reference/PHILOSOPHY.md) is kept as an export guarantee:** the current revision of any scope can always be exported as `state.yaml` (and imported back), with or without Git. The change is that the text file is a view of the store, not a second source of truth that can disagree with it.

### 5.2 Scopes ([ADR-0003](adr/ADR-0003-scopes-and-storage-groups.md))

| Scope | Examples | Owner of changes |
|---|---|---|
| **node** | hostname, interfaces, BMC/fencing credentials, tuning (ARC, sysctl), SMART tasks per device, UPS | the node itself |
| **group** | pools, datasets, shares, NFS exports, snapshot/replication policies, stacks bound to the group's pools | the group's current owner (epoch holder) |
| **cluster** | users, groups, directory (LDAP/AD), certificates, alerting | the cluster's elected coordinator; replicated to all members |
| **fleet** | fleet-wide users, policies, defaults, update channels | the fleet overlay |

Effective configuration on a node = fleet ⟶ cluster ⟶ group ⟶ node, later scopes overriding earlier ones only where a key allows overrides. The GUI shows, for every effective setting, which scope it came from and whether it is a local override.

HA topology and fencing settings are node scope and are **not** managed through Git (misconfiguring them remotely is a split-brain risk).

### 5.3 Storage groups and topologies ([ADR-0003](adr/ADR-0003-scopes-and-storage-groups.md))

A **storage group** is the unit of ownership and failover: one or more pools plus the resources on them, a list of candidate nodes and a topology chosen in the GUI.

| | Standalone | Shared SAS / SAN / NVMe-oF | Replicated |
|---|---|---|---|
| Candidates | 1 | 2..n nodes seeing the same disks | 2..n nodes with own disks |
| Data protection | — | SCSI-3 PR reservation + watchdog self-fence (+ IPMI/PDU) | ZFS send/receive + watchdog + IPMI/PDU |
| RPO | — | zero | up to one replication interval (shown in the GUI) |
| Failover | none | automatic, reservation decides | automatic only if enabled and quorate (witness required) |
| Planned move | — | export/import with reservation hand-over | final incremental send, then **direction flips automatically** |
| Maps to today | standalone node | HA Path A' | HA Path B |

- **Owner and epoch.** The owner is chosen by the group's quorum ([5.4](#54-quorum-and-epochs-adr-0004)). Every promotion increments the group epoch. Every apply, every config write in group scope and every outgoing sync carries the epoch; a node whose epoch is stale must not write (fencing token).
- **Within a group, the owner replicates group-scope revisions to the other members** (TrueNAS model): each write is forwarded; members apply it only while they are not the owner; on failure the owner sends a full scope snapshot.
- **A reconcile failure never triggers a failover**, and a failover never triggers a revert. Configuration problems and node health are separate signals.
- **Losing the controller or the fleet overlay is never a failover trigger.** Only group-level signals (quorum, reservation, watchdog, witness) are.

### 5.4 Quorum and epochs ([ADR-0004](adr/ADR-0004-quorum-corosync.md))

- Each cluster runs **Corosync votequorum**. Storage groups derive ownership from it plus their topology-specific arbitration (reservations for shared SAS).
- Two-node clusters use `two_node` (implies `wait_for_all`) or a **QDevice** as third vote; the QDevice can run on a Raspberry Pi, a VM, another D-PlaneOS node or a small cloud instance.
- Larger clusters may use `auto_tie_breaker` or `last_man_standing` where appropriate; the GUI explains each option in plain language ("Wait for both nodes on first start", "Which node wins a tie").
- The epoch is stored in the group's revision log and, for shared SAS, also in the reservation key, so a node can check its authority locally without the network.
- The existing HTTP witnesses (`internal/ha/witness.go`) remain as an additional health signal ("am I isolated from the outside world"), not as the quorum.

### 5.5 Node states and independent operation ([ADR-0001](adr/ADR-0001-local-first-node-state.md))

| State | Entered | Behaviour |
|---|---|---|
| **Connected** | normal | Revisions sync with group, cluster and overlay. |
| **Isolated** | automatically when group peers, the cluster or the overlay are unreachable | Full local operation; local changes are queued in the outbox; GUI banner "Operating independently since 14:02 — 3 changes waiting to sync". |
| **Independent** | operator clicks **Detach** | Permanently standalone; configuration kept, cluster membership and peer trust removed. **Rejoin** later with a merge preview. |

What an isolated node may change:

| Scope | While isolated |
|---|---|
| node | always |
| group, standalone topology | always |
| group, shared SAS | only while it holds the disk reservation; storage arbitration decides, not network reachability |
| group, replicated | the source node keeps working; promoting a target copy during a partition is allowed only if the group is configured for it and has quorum through a witness (default: off for two-node groups) |
| cluster / fleet | as **local overrides**, marked in the GUI, merged on reconnect |

Working offline also requires: a local break-glass administrator; cached directory (LDAP/OIDC) credentials with a configurable lifetime; the group secrets key cached locally; timers running from the local applied state (replication pauses and catches up).

### 5.6 Merge engine and conflict policy ([ADR-0006](adr/ADR-0006-merge-engine.md))

One engine handles all three sources of conflicting changes: reconnect after a partition, GUI versus Git, and pull requests.

- Every revision carries its base revision. Changes to different resources merge automatically (three-way, per resource).
- Changes to the same resource go to a **review screen** that shows both sides; the operator picks one or edits a merged version.
- Default rules, configurable per scope:
  - node scope: the node wins;
  - group scope: the holder of the current epoch wins;
  - cluster/fleet scope: the coordinator or overlay wins, and the local change is kept as a proposal (never dropped).
- **Data is never merged.** If both copies of a replicated group were written during a partition, the GUI asks which side becomes canonical; the other side's state is preserved as snapshot and clone (`<dataset>@split-<time>`) before replication resumes.

### 5.7 Fleet overlay ([ADR-0005](adr/ADR-0005-fleet-overlay.md))

- An optional D-PlaneOS role (any node can enable it; dedicated hosts are an option) that manages remotes: standalone nodes and clusters registered with an API token.
- Holds fleet-scope configuration, distributes desired revisions per scope, shows inventory and health, orchestrates rollouts (canary, waves), and can own the Git integration for the fleet.
- No consensus across the fleet; remotes stay fully functional without it (Proxmox Datacenter Manager model). The overlay itself can be made highly available by running it on a cluster.

### 5.8 Git as an optional backend ([ADR-0007](adr/ADR-0007-gitops-defaults.md))

- Git is configured per scope. Without Git, nothing in this document changes except that there is no export/import of commits.
- **Outbox, not inline push.** A GUI change writes its revision and an outbox entry in one transaction; the scope's Git owner (the overlay, or the group owner without an overlay) drains the outbox. Nothing is lost on failover or crash; the local clone is disposable (fresh fetch and reset after promotion).
- **Conflict policy per repository**, chosen in the GUI:
  - *direct* (small deployments): commit to the branch; if the remote changed the same resource since the base revision, the GUI change is rejected (HTTP 409, "repository changed, reload") instead of being rebased blindly;
  - *pull request* (teams, fleets): GUI changes go to `scope/<id>` branches and open a PR; the resource shows "pending PR" until merged or closed.
- **Pull, verify, plan, apply:** the Git owner fetches on a schedule or webhook, verifies commit signatures (optional, per repository), computes the plan and applies according to the defaults below.
- **Defaults (Argo CD):** self-heal off, prune off, empty-state guard (a plan that would remove all resources of a kind is BLOCKED), retry with backoff, **no automatic revert**. A failed apply sets a **hold** on the affected scope: the scope stays on its last good revision, auto-apply pauses, the failure is reported (GUI, alert, commit status), and "Revert" is a one-click action.
- Several remotes per repository (comin) to avoid a single point of failure.

### 5.9 Secrets and credentials ([ADR-0008](adr/ADR-0008-secrets-and-credentials.md))

- **Group/cluster secrets key** established when a node joins (join token from the GUI) and stored on each member; replaces today's per-node `secrets.key` for shared data. Node-scope secrets keep a node key.
- **Secrets never go to Git.** Git holds references (`secret://ldap-bind`) resolved from the local store; optionally sops/age-encrypted files for teams that want secrets in the repository. Plain-text secret fields in `state.yaml` are rejected.
- **Git credentials only where Git I/O happens** (overlay or group owner), with separate read (deploy) and write keys, write access limited by branch protection to `scope/<id>/*`, and optional commit signing per node or overlay.

### 5.10 GUI requirements

| Area | Requirement |
|---|---|
| Setup | Wizards for standalone, pair, cluster and fleet; join a node with a join token; detach and rejoin with merge preview. |
| Storage groups | Create with topology choice; hardware detection (shared disks visible, SCSI-3 PR support, NIC links); plain-language consequences (RPO, fencing required, what happens when a node fails). |
| Safety checks | Test buttons for fencing (BMC/PDU, reservation), witness/QDevice reachability, replication. |
| Cluster map | Nodes, groups, owner, epoch, quorum, replication lag, holds, node state (connected/isolated/independent). |
| Operations | Planned move of a group; maintenance mode per node; manual failover. |
| Configuration | History, diff and rollback per resource and scope; source of each effective setting; local overrides marked. |
| Conflicts | Review screen with both sides; pending proposals; pending PRs. |
| Git | Repository setup, policy (direct / pull request), signature verification, sync status, holds with one-click resume or revert. |

### 5.11 Failure semantics

| Event | Storage | Configuration | GUI |
|---|---|---|---|
| Network glitch between group members | Owner keeps serving; standby does nothing unless quorum and topology rules allow promotion | Both sides keep their local stores; outbox queues | Banner on both nodes |
| Owner node fails | Standby promotes after fencing (shared SAS: takes reservation; replicated: only if configured) | New owner continues from the replicated revision log and outbox | Cluster map shows new owner and epoch |
| Overlay down | Unaffected | Group/node changes continue; fleet changes wait | Banner "fleet manager unreachable" |
| Git unreachable | Unaffected | Outbox queues; drift checks pause | Sync status |
| Apply fails | Unaffected | Scope on hold at last good revision | Hold banner with resume/revert |
| Split of a replicated group | Both copies preserved | — | Canonical-copy choice; loser kept as `@split-<time>` |

---

## 6. What changes for existing installations

- **Standalone:** no behavioural change except the new history/diff/rollback and the safer Git integration. `state.yaml` repositories keep working (imported as revisions).
- **HA Path A' (shared SAS):** becomes a two-node storage group with shared-SAS topology. The daemon moves from the shared Patroni database to the node-local database; Patroni, HAProxy and etcd are no longer needed for `dplaned`. Corosync with `two_node` or a QDevice replaces the etcd witness for quorum.
- **HA Path B (replicated):** becomes a two-node replicated storage group; existing replication jobs map to the group's replication policy.
- **Docs to correct now:** [GITOPS-DRIVEN-NAS.md](../admin/GITOPS-DRIVEN-NAS.md) (auto-apply is documented but not implemented), [HIGH-AVAILABILITY.md](../admin/HIGH-AVAILABILITY.md) and [ARCHITECTURE.md](../reference/ARCHITECTURE.md) (shared database model).

---

## 7. Security considerations

- Join tokens are single-use, short-lived and bound to the target cluster; joining requires an administrator on both sides.
- Epochs and reservations prevent a stale owner from writing storage or configuration after it was fenced.
- Group secrets keys never leave the group members; the overlay and Git never see them.
- Commit signature verification (optional) prevents an attacker with repository write access from changing configuration without a trusted key.
- Local break-glass accounts are required for independent operation and are audited like any other login.

---

## 8. Alternatives considered

| Alternative | Why not |
|---|---|
| Keep the shared Patroni database for `dplaned` | Incompatible with independent operation (section 2). |
| Proxmox-style replicated config that becomes read-only without quorum | Violates Goal 2. |
| One consensus ring across the whole fleet | Does not scale to hundreds of nodes; a fleet-wide partition would stop management everywhere. |
| etcd or embedded Raft for storage-group quorum | etcd needs three voters and has no two-node mode; embedded Raft means maintaining our own consensus code. Corosync is proven for exactly this role (Proxmox, Pacemaker). See [ADR-0004](adr/ADR-0004-quorum-corosync.md). |
| Git as the only source of truth | Excludes users who do not want Git; Git outages would block changes. |
| Automatic `git revert` on failed apply | Can flap across a fleet (one node's revert undoes another's success) and loop (revert of a revert). |
| CRDT-based configuration | Automatic convergence hides conflicts that an operator must see (Goal 5); per-resource three-way merge with review is easier to explain in a GUI. |

---

## 9. Implementation phases

Each phase ends with its tests passing in CI; later phases do not start on top of a red build.

| Phase | Content | Exit criteria |
|---|---|---|
| **0. Fixes (independent of this design)** | GitOps write-back and drift only on the active node; apply guard checks role, not only quorum; shared secrets key for existing pairs; abort a failed rebase and report push failures; real fetch in the drift detector; reject plain-text secrets in `state.yaml`; correct GITOPS-DRIVEN-NAS.md. | Unit tests; live-boot and HA VM tests green. |
| **1. Node-local store** | Revision log, applied state, outbox, holds; scopes; `state.yaml` import/export; GUI history, diff, rollback. | Standalone round-trip tests (import → revisions → export identical). |
| **2. Merge engine and node states** | Three-way merge per resource; review screen; connected/isolated/independent; detach and rejoin. | VM test: partition a pair (nftables), change both sides, reconnect, verify merge and review. |
| **3. Storage groups and quorum** | Corosync votequorum + QDevice; epochs; owner replication of group revisions; topologies mapped from Path A'/B; migration off the shared Patroni database. | VM tests per topology: failover, planned move, stale-owner rejection, split of a replicated group. |
| **4. GUI** | Wizards, cluster map, fencing and witness test buttons, conflict review, holds. | Browser tests of the wizards against a mock API; VM test of a full setup through the API. |
| **5. Git backend** | Outbox draining, direct/PR policies, pull + verify + plan + apply with defaults, multiple remotes. | Tests against a local Git server (Gitea/Forgejo) in CI. |
| **6. Fleet overlay** | Remote registration, fleet scope, distribution, rollouts. | Simulated fleet of 5–10 VMs in CI; overlay down → remotes unaffected. |
| **7. Hardening** | Partition, power-loss and slow-network tests for every topology; documentation rewrite. | Failure semantics table (5.11) covered by automated tests. |

---

## 10. Open questions

1. **Corosync as the quorum layer** ([ADR-0004](adr/ADR-0004-quorum-corosync.md)): accepted, or should the existing etcd witness stay for Path A'?
2. **Promotion of a replicated copy during a partition:** default off for two-node groups, allowed with witness quorum for larger groups — confirm.
3. **Fleet overlay as a role on any node or a separate product** ([ADR-0005](adr/ADR-0005-fleet-overlay.md)).
4. **Git providers for the pull-request policy:** GitHub, Gitea/Forgejo, GitLab — which first.
5. **Cluster-scope coordinator:** the owner of a designated "system" storage group, or a separately elected role.

---

## 11. References

- Proxmox: [pmxcfs](https://pve.proxmox.com/wiki/Proxmox_Cluster_File_System_(pmxcfs)), [High Availability](https://pve.proxmox.com/wiki/High_Availability), [Storage Replication](https://pve.proxmox.com/wiki/Storage_Replication), [Cluster Manager](https://pve.proxmox.com/wiki/Cluster_Manager), [Datacenter Manager](https://pdm.proxmox.com/docs/introduction.html)
- TrueNAS: [HA explained](https://www.truenas.com/blog/truenas-high-availability-ha-explained/), [Failover screen](https://www.truenas.com/docs/scale/24.10/scaleuireference/systemsettings/failoverscreen/), middleware source: [failover.py](https://github.com/truenas/middleware/blob/master/src/middlewared/middlewared/plugins/failover.py), [failover_/datastore.py](https://github.com/truenas/middleware/blob/master/src/middlewared/middlewared/plugins/failover_/datastore.py)
- [Corosync votequorum(5)](https://manpages.debian.org/bookworm/corosync/votequorum.5.en.html)
- [Argo CD automated sync](https://argo-cd.readthedocs.io/en/stable/user-guide/auto_sync/)
- [comin](https://github.com/nlewo/comin)
- DPlaneOS: [PHILOSOPHY.md](../reference/PHILOSOPHY.md), [ARCHITECTURE.md](../reference/ARCHITECTURE.md), [HIGH-AVAILABILITY.md](../admin/HIGH-AVAILABILITY.md), [GITOPS-DRIVEN-NAS.md](../admin/GITOPS-DRIVEN-NAS.md)
