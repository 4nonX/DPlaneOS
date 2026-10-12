# DPlaneOS System Architecture

This document describes how DPlaneOS is structured at a system level: how state flows from declaration to reality, what runs where, and how a cluster differs from a single node.

If you want to understand the design reasoning before the mechanics, read [PHILOSOPHY.md](PHILOSOPHY.md) first. This document covers the structure; PHILOSOPHY.md covers the why.

---

## Two descriptions

A running DPlaneOS system is the product of two descriptions.

```
 THE RELEASE                                  THE CONFIGURATION
 one image, built from a tagged commit        what this NAS is set up to do
 ─────────────────────────────────────        ─────────────────────────────
 kernel, OpenZFS, Samba, NFS, PostgreSQL,     pools, datasets, shares, users,
 nginx, Docker, the daemon, the web UI,       schedules, stacks, network and
 and how they are wired together              system settings

 changes: by a system update                  changes: in the web interface,
 (the whole image is replaced)                or by applying state.yaml

 lives in: the Nix store on the               lives in: the database on the
 system partition                             persist partition (and, if you
                                              want, as state.yaml in Git)
```

The data itself is in ZFS pools on disks of their own, and belongs to neither.

**The release** is the same for every installation. An installed system has no configuration file of the operating system to edit: what differs between machines (host name, network, firewall, time, SSH) is configuration, applied by the daemon at runtime and again at every start.

**The configuration** has one home, the database, and two ways in. The web interface changes it directly, with immediate effect. `state.yaml` is the same configuration written down: the daemon compares the file with the running system, shows the difference as a plan, and applies it; *Capture* writes the file from the running system. The drift check runs in the background and shows when the file and the system have moved apart. See the [GitOps Reference](GITOPS-REFERENCE.md).

## How a change happens

Whether a change comes from the web interface or from a plan, the daemon makes it the same way:

1. The request is authenticated, and the route's permission is checked.
2. Its input is validated (names, paths, addresses).
3. The configuration is saved, and the system is changed: by running a program from the allowlist (`zfs`, `zpool`, `docker`, `exportfs`, ...), by writing a service's configuration and reloading it, or by writing to the kernel's configuration (block targets, network).
4. Where the system refuses, what was saved is put back, and the error says why.
5. The change is recorded in the audit log.

Applying a plan from `state.yaml` adds: the plan is computed first and shown; steps that would destroy data are blocked until approved; steps run in order (create, modify, delete) and stop at the first failure; afterwards the system is read again to verify that it matches.

### ZFS

`internal/libzfs` is where the daemon's ZFS operations are; despite its name, the installed daemon runs them through the `zfs` and `zpool` commands, through the exec allowlist. That is the path every test of the system exercises.

`zfs_cgo.go` (`//go:build linux && cgo && libzfs`) calls `libzfs.h` directly for some of the operations. It is experimental and **not installed by any image**: the commands do more than the library calls they end in (unmounting before an export or a destroy, mounting after a create or a clone), and the bindings do not reproduce all of that.

---

## Security

The [Threat Model](THREAT-MODEL.md) is the full account. In short:

- **The boundary is the API.** nginx serves the interface and passes API requests to the daemon over a Unix socket; the daemon has no network port of its own. Every route needs a session (or an API token) and carries the permission it requires.
- **No shell.** A command is a program and a list of arguments; where they come from a request, the allowlist (`internal/security/whitelist.go`) fixes their shape.
- **The daemon is root**, with `NoNewPrivileges` and nothing else from systemd: it imports pools, mounts datasets and configures the network, so a sandbox around it would have to be full of holes. What limits it is inside it.
- **Sessions** are random tokens in a request header, with a CSRF token for changing requests; TOTP as a second factor; sign-in attempts throttled per client address.
- **The audit log** is chained with an HMAC whose key is kept outside the database.
- **Secrets of integrations** are stored encrypted with a key file on the persist partition.

---

## Where things are

```
System disk (written by the installer, nixos/disko.nix):
├── ESP        /boot      boot loader (systemd-boot), kernels of the installed versions
├── system     /          the Nix store: the running version and the previous one
└── persist    /persist   everything the system remembers

Data disks:
└── ZFS pools, mounted under /mnt/<pool>
```

Directories on `persist` are bound into place at start (`nixos/impermanence.nix`), so the daemon and the services write to their usual paths:

| Path | Contents |
|------|----------|
| `/var/lib/dplaneos` | the database (`pgsql/`), keys (`secrets.key`, `audit.key`), certificates (`tls/`), the daemon's SSH key (`ssh/`), Samba and network configuration, saved block targets, the checkout of the configuration repository |
| `/etc/dplaneos` | cloud remotes, schedules |
| `/var/lib/docker` | images, containers, volumes that are not on a pool |
| `/var/lib/samba`, `/var/lib/nfs`, `/etc/target` | state of Samba, NFS and the iSCSI target |
| `/var/log/dplaneos`, `/var/log/samba` | logs |

A system update replaces what is on `system` and leaves `persist` alone.

---

## One node

```
 clients
    │
    ▼
 nginx :80/:443 ── static files (the web interface)
    │ /api/, /ws/
    ▼
 dplaned (Unix socket) ── WebSocket hub (live updates in the interface)
    ├── PostgreSQL (local socket, /var/lib/dplaneos/pgsql)
    ├── ZFS: the zfs and zpool commands; events from ZED
    ├── Docker (its socket)
    ├── Samba, NFS (their configuration, reload)
    ├── iSCSI (kernel LIO), NVMe-oF (kernel nvmet), FTP, S3
    ├── network and firewall (systemd-networkd, the kernel's packet filter)
    └── rclone (cloud sync, remotes mounted as folders), rsync
```

At start, pools are imported (`dplaneos-zfs-import`), the saved system settings and network are applied, the database comes up, then the daemon; the daemon restores what the kernel forgets at a restart (block targets, schedules' timers, floating addresses).

**Updates**: the new version is installed next to the running one, started once on trial, and kept if the system is healthy; see [System Updates](../admin/OTA-UPDATES.md).

---

## Multi-Node HA Architecture

High availability adds nodes without making any node depend on another to work. The user guide is [HIGH-AVAILABILITY.md](../admin/HIGH-AVAILABILITY.md); the design is [Design 0001](../design/0001-distributed-state-gitops-ha.md).

```
        clients ── floating address of storage group "data" (held by the owner)
                                   │
        ┌──────────────────────────┴──────────────────────────┐
   node A (owner of "data")                          node B (candidate)
   PostgreSQL (own)  ◄── configuration sync ──►      PostgreSQL (own)
   corosync          ◄──── UDP 5405 (knet) ────►     corosync
   pools of "data" imported                         pools not imported (shared)
   apps, exports, address of "data"                 / read-only copy (replicated)
   watchdog: reset unless quorate                   watchdog: reset unless quorate
        │                                                     │
        └──────── corosync-qdevice ──► third vote ◄───────────┘
                  (QDevice on a Pi/VM/DPlaneOS, or a voter, or a third node)
```

**Node-local state.** Every node has its own PostgreSQL. Paired nodes exchange configuration revisions (pull every 30 s and on change) and merge them per resource with a three-way merge over revision ancestry; the same resource changed on two nodes becomes a conflict for the operator. Secrets are exchanged as material encrypted for the pair and sealed under each node's own key ([configstore](../../daemon/internal/configstore)).

**Quorum.** Corosync votequorum decides membership. Two nodes run in `two_node` mode (no automatic failover); a QDevice (`corosync-qnetd`), a voter (a corosync-only member) or a third DPlaneOS node provides the deciding vote. The daemon renders `corosync.conf`, distributes it to DPlaneOS members over the paired-node channel, and voters pull it with a token ([quorum](../../daemon/internal/quorum)).

**Storage groups.** A group (pools, their shares/exports, apps with pool volumes, iSCSI targets and NVMe-oF exports of its volumes, a floating address) has exactly one owner and an epoch that rises with every change of owner and fences stale writers. Topologies: shared storage (one pool, imported by the owner, ZFS multihost) and replicated (one pool per node, `zfs send` streams from the owner, read-only copies) ([groups](../../daemon/internal/groups)).

**Protection layers** ([ADR-0009](../design/adr/ADR-0009-fencing-layers.md)): the watchdog (a storage owner that loses quorum stops resetting it and is restarted; the takeover waits the watchdog timeout plus a margin), ZFS multihost, SCSI-3 persistent reservations where the disks support them, and optional power fencing (IPMI/Redfish, PDU). Missing layers are reported, not required; automatic failover needs a third vote and a fencing method.

### HA vs Single-Node

| Feature | Single node | Cluster |
|---------|-------------|---------|
| PostgreSQL | local | local on every node; configuration exchanged |
| Failover | none | per storage group: automatic with a third vote, otherwise *Take over* |
| ZFS pools | imported at boot | shared groups: imported by the owner's daemon; replicated: read-only copies elsewhere |
| Client address | the node's | the group's floating address |
| Fencing | none | watchdog, multihost, SCSI-3 PR, optional power fencing |
| Updates | one node, one reboot | move groups away, update, move back |

---

## ZED Event System

The ZFS Event Daemon (ZED) feeds real-time pool and device events into the daemon through a Unix socket at `/run/dplaneos/dplaneos.sock`. The daemon's `zed_listener.go` goroutine reads these events and routes them through `zedTypedDispatch`.

### Event ingestion

ZED executes `/etc/zfs/zed.d/all-dplaneos-notify.sh` on each kernel event. The hook formats the event as `zfs_event:<severity>:<pool>:<subclass>:<state>` and writes it to the daemon socket with a 2-second non-blocking timeout.

### Typed dispatch

`zedTypedDispatch` handles over 20 specific subclasses with structured responses:

| Subclass | WebSocket event | Severity | Side effects |
|----------|----------------|----------|--------------|
| `scrub_start` | `scrub_started` | info | spawns `zedFastProgressPoll` (2s) |
| `scrub_finish` | `scrub_completed` | info | pool health refresh |
| `scrub_abort` | `scrub_aborted` | warning | |
| `resilver_start` | `resilver_started` | info | spawns `zedFastProgressPoll` (2s) |
| `resilver_finish` | `resilver_completed` | info | pool health refresh |
| `trim_start` | `trim_started` | info | spawns `zedTrimProgressPoll` (2s) |
| `trim_finish` | `trim_completed` | info | pool health refresh |
| `trim_abort` | `trim_aborted` | warning | |
| `vdev_clear` | `vdev_errors_cleared` | info | pool health refresh |
| `vdev_online` | `vdev_recovered` | info | pool health refresh |
| `pool_import` | `pool_imported` | info | pool health refresh |
| `data_loss` | `zfs.data_loss` | error | pool health refresh |
| `deadman` | `zfs.deadman` | error | |
| `statechange` | `zfs.event.statechange` | info | pool health refresh |
| `checksum` | `zfs.checksum_errors` | warning | alert issued |
| `io` | `zfs.io_errors` | error | alert issued |
| `pool_destroy`, `vdev_remove`, `device_removal` | (none) | - | pool health refresh |

Unrecognized subclasses fall through to a generic `zfs.event.<subclass>` broadcast.

### Progress polling

Two goroutines provide ongoing progress during long ZFS operations:

- **`zedFastProgressPoll`**: runs during scrub and resilver. Polls `zpool status` every 2 seconds, broadcasts `zfs.scrub.progress` or `zfs.resilver.progress` with scan percentage, ETA, and error counts. Exits when the finish event arrives.
- **`zedTrimProgressPoll`**: runs during TRIM operations. Parses the TRIM status line from `zpool status`, broadcasts `zfs.trim.progress` with percent done, ETA, and bytes trimmed. Exits when TRIM completes (max runtime 12 hours).

### Alert routing

Severity-mapped events with severity `warning` or higher are also forwarded to the alert subsystem, which routes them to all configured delivery channels (SMTP, webhook, Telegram). See [ALERTS.md](../admin/ALERTS.md) for the full event taxonomy.

---

## Data Flow: User Action to System State

A concrete example - user adds an NFS export via the UI:

```
1. Browser POST /api/nfs/exports
         │
2. dplaned validates request, writes to PostgreSQL
         │
3. dplaned writes /etc/exports (via nfs_handler.go)
         │
4. dplaned calls exportfs -ra (via exec allowlist)
         │
5. NFS kernel exports new share immediately
         │
6. The drift check (in the background, if a state.yaml is configured) compares live state to it
         │
7. If state.yaml has not been updated, drift is reported in UI
         │
8. Operator updates state.yaml (manually or via Capture) and commits to Git
         │
9. GitOps apply engine pulls new state.yaml, sees NFS entry matches live, no action needed
         │
10. Drift resolved. System is converged.
```

This illustrates the reconciliatory loop: the daemon can make immediate changes, but long-term truth lives in state.yaml. Drift between the two is surfaced continuously.

---

## Where to Go Next

| Topic | Document |
|-------|----------|
| GitOps state.yaml format and reconciliation | [GitOps Reference](GITOPS-REFERENCE.md) |
| Backup: ZFS replication, Rsync, Cloud Sync, Cold Tier | [Backup and Replication](../admin/BACKUP-REPLICATION.md) |
| High Availability setup and day-2 operations | [High Availability Guide](../admin/HIGH-AVAILABILITY.md) |
| System updates and the return to the previous version | [System Updates](../admin/OTA-UPDATES.md) |
| Security threats, mitigations, and residual risks | [Threat Model](THREAT-MODEL.md) |
| iSCSI, FTP, NVMe-oF, S3 | [Optional Protocols](../admin/OPTIONAL-PROTOCOLS.md) |
| SMTP, webhook, and Telegram alerts; TOTP/2FA | [Alerts and Authentication](../admin/ALERTS.md) |
| Why NixOS, and what is kept across updates | [NixOS Rationale](NIXOS-RATIONALE.md) |
