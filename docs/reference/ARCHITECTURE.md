# DPlaneOS System Architecture

This document describes how DPlaneOS is structured at a system level: how state flows from declaration to reality, how the layers interact, and how the architecture changes between a single-node appliance and a multi-node HA cluster.

If you want to understand the design reasoning before the mechanics, read [PHILOSOPHY.md](PHILOSOPHY.md) first. This document covers the structure; PHILOSOPHY.md covers the why.

---

## The Three-Layer Model

DPlaneOS is built around a strict separation between declaration, reconciliation, and execution.

```
┌─────────────────────────────────────────────────────────────────┐
│  DECLARATION LAYER                                              │
│  What the system should look like                               │
│                                                                 │
│  state.yaml          configuration.nix / flake.nix             │
│  (runtime config)    (OS-level config)                          │
└───────────────────────────┬─────────────────────────────────────┘
                            │ reconcile
┌───────────────────────────▼─────────────────────────────────────┐
│  RECONCILIATION LAYER                                           │
│  Closes the gap between declared and actual state               │
│                                                                 │
│  GitOps apply engine      nixos-rebuild switch                  │
│  (runtime drift check)    (OS drift check + activation)         │
└───────────────────────────┬─────────────────────────────────────┘
                            │ execute
┌───────────────────────────▼─────────────────────────────────────┐
│  EXECUTION LAYER                                                │
│  Makes changes happen                                           │
│                                                                 │
│  dplaned daemon           NixOS activation scripts             │
│  libzfs (cgo) / exec      systemd, kernel modules,             │
│  allowlist, docker,       Nix store population                  │
│  samba, nfs, nvmet                                              │
└─────────────────────────────────────────────────────────────────┘
```

### Declaration Layer

There are two declaration surfaces:

**`state.yaml`** - Runtime configuration managed by the daemon and the GitOps engine. Covers everything that changes during normal NAS operation: ZFS datasets and their properties, SMB/NFS/iSCSI shares, Docker stacks, users and groups, replication schedules, network config, LDAP settings, ACME certificates. This file lives in a Git repository so every change is version-controlled and auditable.

**`configuration.nix` / `flake.nix`** - OS-level configuration managed by NixOS. Covers everything that is fixed for the lifetime of the appliance: kernel version, ZFS package pin, systemd units, firewall baseline, package set, impermanence rules, HA cluster membership. Changes here require a NixOS rebuild (either OTA or manual `nixos-rebuild switch`).

The division of responsibility is: NixOS owns the platform, the daemon owns the workload.

### Reconciliation Layer

**GitOps apply engine** runs inside the daemon. On every `POST /api/gitops/apply`, it:

1. Reads `state.yaml` from the configured Git repository
2. Reads the current live system state (ZFS, Docker, Samba, NFS, DB)
3. Computes a diff plan: which items to CREATE, MODIFY, DELETE, or block
4. Executes the plan transactionally (halt on first failure; already-applied items are safe by design)
5. Verifies convergence by re-reading live state after apply

A background detector runs every five minutes and broadcasts drift events to connected UI clients. Operators can also trigger an immediate check or apply via the UI or API.

**`nixos-rebuild switch`** is the OS-layer reconciler. It reads the flake, computes which store paths are needed, builds or fetches them, and atomically activates the new system configuration. OTA updates wrap this in an A/B slot mechanism with automatic health-check rollback.

### Execution Layer

The daemon executes changes through two paths, depending on the operation:

**`internal/libzfs` (cgo or subprocess fallback)**: All destructive ZFS operations (dataset create/modify/destroy, pool create/destroy, vdev add/attach/replace/remove/detach/online/offline, snapshot hold/release, pool property set, pool clear) go through the `libzfs` package. When built with `//go:build linux && cgo && libzfs`, calls invoke the native `libzfs.h` C API directly - no subprocess, no argument marshalling. When built without CGO (static musl build, CI), the same package interfaces fall back to validated subprocess calls. This means most ZFS operations do not spawn external processes at all in the production binary.

**Exec allowlist (`internal/security/whitelist.go`)**: Operations that have no clean native libzfs equivalent (Docker Compose, Samba reload, NFS exportfs, SSH, network tools, the small set of complex zpool operations) still call external binaries through the allowlist. Only predefined, validated command structures are permitted. There is no shell; every `exec.Command` call is a named binary with a validated argument slice - no shell expansion. A compromised API request cannot execute arbitrary commands.

---

## Security Architecture

Security in DPlaneOS is layered. Each layer independently limits what a compromised request or compromised component can do.

### Trust Boundary

The daemon listens on `127.0.0.1:9000` only. It is not reachable from the network without a reverse proxy (nginx, Caddy, or Pangolin). Everything behind the proxy is trusted; everything in front is not.

```
UNTRUSTED
  Browser ──── Network ──── Reverse Proxy (TLS terminated)
                                     │ localhost only
TRUSTED
  dplaned (Go, 127.0.0.1:9000)
    ├── PostgreSQL / Patroni
    ├── exec allowlist → zfs / zpool / docker / exportfs / smbcontrol
    ├── networkdwriter → /etc/systemd/network/
    └── /dev/disk/by-id/* │ /mnt/* │ /var/lib/dplaneos/
```

### The Exec Allowlist

For operations that still use subprocess calls (Docker, Samba, NFS, networking tools, and complex ZFS topology operations), every call goes through `internal/security/whitelist.go`, which validates:

1. The binary name (only known tools are permitted)
2. The subcommand (e.g., `zpool add` is validated with a dedicated argument grammar)
3. Each argument individually against predefined regex patterns

Arguments are passed to `exec.Command` as a string slice, never as a shell string. There is no `bash -c`, no shell expansion. A request that passes all API-level validation but asks for a command not in the allowlist is rejected before any process is spawned.

Input is validated before it reaches the allowlist. Pool names, dataset names, device paths, and share names each pass through dedicated validators (`ValidatePoolName`, `ValidateDatasetName`, `ValidateDevicePath`) that reject shell metacharacters with HTTP 400 before any command is constructed. Device paths must be stable `/dev/disk/by-id/` paths; short `/dev/sdX` paths are rejected at `state.yaml` parse time, long before execution.

For ZFS operations that go through the libzfs package, the same input validators run before the cgo call - so validated strings are passed as C arguments, not shell-expanded. The combination of libzfs + allowlist means arbitrary command execution is structurally unavailable from any code path.

### The libzfs Package

`internal/libzfs` provides two compile paths behind Go build tags:

- **`zfs_cgo.go`** (`//go:build linux && cgo && libzfs`): calls `libzfs.h` directly via cgo. Uses `runtime.LockOSThread` and a per-call `libzfs_init/libzfs_fini` handle. No subprocess is spawned for pool/dataset operations.
- **`zfs_fallback.go`** (`//go:build !linux || !cgo || !libzfs`): provides identical function signatures via `cmdutil` subprocess calls routed through the exec allowlist. Active for the static musl production build and CI.
- **`zfs.go`** (no build tag): shared functions that use subprocess in all variants - complex topology operations (VdevAdd, VdevAttach, VdevReplace, PoolCreate, PoolSplit) where native libzfs would require C nvlist construction but the subprocess call is already safe and validated.

The calling code never knows which path is active. From `handlers/` and `gitops/`, a call to `libzfs.DatasetCreate("tank/media")` either calls `zfs_create()` directly in C or spawns `zfs create tank/media` through the allowlist, depending on build tags.

### Daemon Hardening

The daemon runs as root but constrains itself at the systemd unit level:

- `ProtectSystem=strict`: the NixOS system closure is read-only from the daemon's perspective
- `NoNewPrivileges=true`: the daemon cannot escalate beyond its initial capabilities
- `MemoryMax=1G`: bounded memory prevents OOM-triggered system instability

### Authentication and Session Model

Session tokens are 32-byte random values stored hashed in PostgreSQL. They travel in the `X-Session-ID` request header, not a cookie. Browsers cannot auto-submit arbitrary headers cross-origin, which makes the session design inherently CSRF-resistant. HMAC-SHA256 double-submit CSRF tokens are layered on top for defence in depth.

TOTP two-factor authentication is available per user. When enabled, login issues a `pending_totp` session that can only call `/api/auth/totp/verify`; all other routes reject it until the second factor is verified.

### RBAC

Four roles (viewer, user, operator, admin) with 34 discrete permissions enforced at the handler level via `permRoute()` middleware. System roles are immutable in the database (`is_system = 1`). Role assignments support expiry dates.

### Audit Chain

Every state-changing operation appends to an audit log stored in PostgreSQL. Each row includes an HMAC-SHA256 hash computed over its content plus the previous row's hash, keyed by a 32-byte key at `/var/lib/dplaneos/audit.key`. The key is stored separately from the database. An attacker with database write access alone cannot forge the chain without also possessing the key. Chain integrity is verifiable at any time via `GET /api/system/audit/verify-chain`.

### Rate Limiting

100 requests per minute per source IP, enforced in-process before any handler runs. Returns HTTP 429 and logs a security event.

---

## The Persistence Model

DPlaneOS uses NixOS impermanence: the root filesystem (`/`) is ephemeral and is replaced on every OTA update. All state that must survive reboots lives on a dedicated `/persist` partition.

```
Boot disk layout (disko.nix):
├── ESP                 /boot       (EFI, systemd-boot, slot A/B entries)
├── Slot A              /           (ephemeral ext4, active NixOS closure)
├── Slot B              /           (ephemeral ext4, standby NixOS closure)
├── swap
└── /persist            /persist    (durable ext4, all runtime state)

ZFS data disks (separate from boot disk):
└── tank/               (user data pool, survives any OS reinstall)
```

The `/persist` partition is bind-mounted into the live filesystem at boot. From the daemon's perspective, it writes to `/var/lib/dplaneos`, `/etc/dplaneos`, `/var/lib/docker`, and similar paths - but these are bind-mounts from `/persist` and always land on the durable partition.

Key items on `/persist`:

| Path | Contents |
|------|----------|
| `/persist/dplaneos/` | PostgreSQL data, daemon DB, audit HMAC key, gitops repo checkout |
| `/persist/docker/` (bind to `/var/lib/docker`) | Container state, images, overlay2 volumes |
| `/persist/samba/` (bind to `/var/lib/samba`) | Samba passdb.tdb, secrets, user accounts |
| `/persist/ssh/` | NixOS SSH host keys (prevents "host key changed" warnings after OTA) |
| `/persist/ota/` | A/B slot markers, pending-revert marker, OTA log |
| `/persist/nixos/` | NixOS UID/GID allocation map (stable ownership across rebuilds) |

ZFS data pools on separate disks are fully independent of this mechanism. A complete reinstall of the boot disk does not touch data pools. After reinstall, run `zpool import -a`.

---

## Single-Node Architecture

On a single node, the full stack runs on one machine:

```
Internet / LAN
      │
      ▼
  [nginx :80/:443]
      │ proxy /api/ /ws/
      ▼
  [dplaned :9000]    ←── WebSocket hub (real-time UI updates)
      │
      ├── PostgreSQL (local socket, /var/lib/dplaneos/pgsql/)
      ├── ZFS via libzfs (cgo) or exec allowlist fallback
      ├── ZED listener (/run/dplaneos/dplaneos.sock, typed dispatch)
      ├── Docker (unix socket)
      ├── Samba (smbd, controlled via systemctl + smbcontrol)
      ├── NFS (kernel nfsd, controlled via exportfs)
      ├── iSCSI (kernel nvmet via configfs)
      └── rclone (cloud sync, cold tier FUSE mounts)
```

**Database**: PostgreSQL runs embedded (single instance, no replication). The daemon connects via `postgres://dplaneos@localhost/dplaneos`. The data directory lives on `/persist`.

**Failure mode**: If the node goes down, the NAS is unavailable. Data on ZFS pools is safe as long as the pool was cleanly exported. On restart, dplaned reconnects to PostgreSQL, re-imports ZFS pools (via the ZFS gate service), and resumes all operations.

**Upgrades**: OTA updates write to the inactive boot slot. A reboot activates the new system. If the health check fails, the system automatically reverts.

---

## Multi-Node HA Architecture

High Availability adds a second DPlaneOS node. Whether a third witness machine is required depends on the storage topology.

**Shared-SAS / shared-block (recommended for two-box deployments):** Both nodes connect to the same physical disk shelf. SCSI-3 Persistent Reservations enforce storage exclusion at the disk-controller level. No separate witness machine is required; the third etcd member for Patroni quorum runs co-located on one of the two data nodes as a lightweight process.

**Replicated / stretched-ZFS:** Nodes do not share physical storage; ZFS state is replicated over the network. A separate witness node provides the quorum tiebreaker. IPMI or SBD fencing ensures the non-surviving node is powered off before promotion.

```
  Shared-SAS topology (no separate witness machine):

           ┌─────────────────────────────────────┐
           │         Client Network              │
           └──────┬──────────────────────────────┘
                  │ VIP (Keepalived)
         ┌────────┴────────┐
         ▼                 ▼
   [Node A - PRIMARY]   [Node B - STANDBY]
   nginx, dplaned        nginx, dplaned
   dplane-fenced         dplane-fenced
   Patroni (primary)     Patroni (replica)
   etcd member A         etcd member B
   etcd witness          Keepalived BACKUP
     (co-located)
   Keepalived MASTER
   ZFS pools mounted     ZFS pools held
   SCSI-3 PR owner       SCSI-3 PR standby
         │                     │
         └──────────┬──────────┘
                    │ shared SAS / block
              [Disk Shelf]
              SCSI-3 PR enforced
              at controller level


  Replicated topology (separate witness required):

   [Node A - PRIMARY]   [Node B - STANDBY]
         │                     │
         └──────────┬──────────┘
                    │ etcd quorum
                    ▼
            [Witness Node]
            etcd member only
            (512 MB RAM, no NAS traffic)
```

### PostgreSQL HA (Patroni + etcd)

PostgreSQL is replicated from primary to standby in streaming replication mode. Patroni manages leader election via etcd. If the primary fails:

1. etcd detects the member is down
2. Patroni on the standby wins the election
3. Standby is promoted to primary
4. HAProxy (`:5000`) begins routing to the new primary
5. The daemon reconnects within seconds

HAProxy runs on each node and listens on `127.0.0.1:5000`. It health-checks Patroni (`GET /primary` returns 200 on primary, 503 on standby) and routes only to the current primary. The daemon always connects to `localhost:5000`, never directly to PostgreSQL.

### ZFS Split-Brain Protection

ZFS pools are on shared or replicated storage. Only one node may write to a pool at a time. DPlaneOS enforces this through two complementary mechanisms:

**Patroni-gated pool import**: Pool import is gated on Patroni role. The primary node imports and mounts pools normally; the standby keeps pools unexported until promotion. Pool import during failover uses `libzfs.PoolImportAll` (native cgo call, no subprocess) for reliability.

**SCSI-3 Persistent Reservations (`dplane-fenced`)**: A dedicated `dplane-fenced` binary registers SCSI-3 Write Exclusive Registrants Only (WERO) persistent reservations on all ZFS pool member disks at startup. Reservations survive system reboots (APTPL=1 stores them in disk controller NVRAM). On graceful failover, `dplaned` calls `FencedRelease()` via `/run/dplaneos/fenced.sock` before exporting pools; on unclean failover, the surviving node preempts the dead node's reservation via `FencedPreempt(device)`. This means a split-brain node literally cannot write to the disks - the SAS/SCSI layer rejects I/O from the non-reservation-holder.

`dplane-fenced` runs in its own systemd slice (`dplaneos-fenced.slice`) isolated from `dplaneos.slice`, so it survives `dplaned` restarts and slice resets.

### STONITH Fencing

Split-brain scenarios (both nodes believe they are primary) are the most dangerous failure mode. DPlaneOS supports three fencing mechanisms:

**SCSI-3 Persistent Reservations (recommended for shared SAS)**: Managed by `dplane-fenced`. Each node registers an 8-byte key derived from `/etc/machine-id` at boot. The primary holds a WERO reservation. On failover, the surviving node preempts the faulted node's registration, making its I/O requests fail at the disk controller. No BMC or shared block device required - the disks themselves enforce exclusion.

**IPMI/Redfish fencing**: On detecting a split, the surviving node powers off the other node via its BMC. Requires IPMI credentials for the peer node.

**SBD (STONITH Block Device) fencing**: A shared block device is used as a "poison pill." Each node periodically renews a lease timestamp. If a node's lease expires, the surviving node treats it as fenced. No BMC required, but requires a ZFS dataset or block device accessible to both nodes.

### Virtual IP (Keepalived)

A floating IP (VIP) moves between nodes with Keepalived. Clients connect to the VIP; it always resolves to the current primary. Keepalived checks the daemon API every two seconds and demotes if the daemon is unresponsive.

### Quorum and the Witness

Patroni uses etcd for leader election and requires an odd number of etcd members to reach quorum under partition. In a two-node cluster, 1-of-2 is not a majority; a third etcd member is needed so the surviving side (2-of-3) can elect a primary without risking both sides promoting simultaneously.

**Shared-SAS deployments** satisfy this requirement by running the third etcd member as a co-located lightweight process on one of the two data nodes. No separate machine is required. SCSI-3 PR independently guarantees storage exclusion at the hardware level, so even if the co-located etcd member's vote were unreliable, the disk controller would still reject writes from the non-reservation-holder.

**Replicated deployments** require a separate witness node because the storage is not shared: SCSI-3 PR is not available to mediate the split-brain question, so quorum must come from the software layer. The witness runs only etcd, carries no NAS traffic, and requires minimal resources (512 MB RAM, any storage). A Raspberry Pi 4 or spare VM qualifies.

### HA vs Single-Node: Feature Differences

| Feature | Single Node | HA Cluster |
|---------|-------------|------------|
| PostgreSQL | Local instance | Patroni + streaming replication |
| Failover | None (manual restart) | Automatic (Patroni, ~10-30s RTO) |
| ZFS pools | Always mounted | Only on primary; standby holds |
| Virtual IP | Not applicable | Keepalived VIP on primary |
| Fencing | Not applicable | SCSI-3 PR via dplane-fenced (shared-SAS) or IPMI/SBD (replicated) |
| Witness | Not applicable | Co-located etcd member on node A (shared-SAS) or separate machine (replicated) |
| OTA updates | One node, one reboot | Rolling: update standby, failover, update old primary |
| dplaned DB | `postgres://localhost/dplaneos` | `postgres://localhost:5000/dplaneos` (via HAProxy) |

---

## ZED Event System

The ZFS Event Daemon (ZED) feeds real-time pool and device events into the daemon through a Unix socket at `/run/dplaneos/dplaneos.sock`. The daemon's `zed_listener.go` goroutine reads these events and routes them through `zedTypedDispatch`.

### Event ingestion

ZED executes `/etc/zfs/zed.d/dplaneos-notify.sh` on each kernel event. The hook formats the event as `zfs_event:<severity>:<pool>:<subclass>:<state>` and writes it to the daemon socket with a 2-second non-blocking timeout.

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
6. GitOps drift detector (next 5-min tick) compares live state to state.yaml
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
| OTA update procedure and rollback | [OTA Updates](../admin/OTA-UPDATES.md) |
| Security threats, mitigations, and residual risks | [Threat Model](THREAT-MODEL.md) |
| iSCSI, FTP, NVMe-oF, MinIO | [Optional Protocols](../admin/OPTIONAL-PROTOCOLS.md) |
| SMTP, webhook, and Telegram alerts; TOTP/2FA | [Alerts and Authentication](../admin/ALERTS.md) |
| Why NixOS exclusively | [NixOS Rationale](NIXOS-RATIONALE.md) |
| Impermanence: what persists and what does not | [NixOS Rationale - Impermanence](NIXOS-RATIONALE.md#impermanence) |
