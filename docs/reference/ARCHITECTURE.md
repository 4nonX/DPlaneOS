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
│  zfs/zpool, docker,       systemd, kernel modules,             │
│  samba, nfs, nvmet         Nix store population                 │
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

The daemon executes changes by calling system tools through a strict allowlist (`internal/security/whitelist.go`). Only predefined, validated command structures are permitted. There is no shell; every `exec.Command` call is a named binary with validated arguments. This means a compromised API request cannot execute arbitrary commands.

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
      ├── ZFS (kernel module, exec.Command via allowlist)
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

High Availability adds a second DPlaneOS node and a witness node. The witness is a minimal machine (Raspberry Pi or small VM) that only runs etcd - it does not serve NAS traffic.

```
           ┌─────────────────────────────────────┐
           │         Client Network              │
           └──────┬──────────────────────────────┘
                  │ VIP (Keepalived, e.g. 10.0.0.10)
         ┌────────┴────────┐
         ▼                 ▼
   [Node A - PRIMARY]   [Node B - STANDBY]
   nginx, dplaned        nginx, dplaned
   Patroni (primary)     Patroni (replica)
   etcd member           etcd member
   Keepalived MASTER     Keepalived BACKUP
   ZFS pools mounted     ZFS datasets held
         │                     │
         └──────────┬──────────┘
                    │ etcd quorum
                    ▼
            [Witness Node]
            etcd member only
            (no NAS traffic)
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

ZFS pools are on shared or replicated storage. Only one node may write to a pool at a time. DPlaneOS enforces this by gating ZFS pool import on Patroni role:

- Primary node: pools are imported and mounted normally
- Standby node: ZFS import units are disabled; datasets remain unmounted until promotion

This prevents the standby from serving stale or conflicting data while the primary is still alive.

### STONITH Fencing

Split-brain scenarios (both nodes believe they are primary) are the most dangerous failure mode. DPlaneOS supports two fencing mechanisms:

**IPMI/Redfish fencing**: On detecting a split, the surviving node powers off the other node via its BMC. Requires IPMI credentials for the peer node.

**SBD (STONITH Block Device) fencing**: A ZFS dataset property is used as a lease. Each node periodically renews its lease. If a node's lease expires, the surviving node treats it as fenced. No BMC required, but requires a ZFS dataset accessible to both nodes.

### Virtual IP (Keepalived)

A floating IP (VIP) moves between nodes with Keepalived. Clients connect to the VIP; it always resolves to the current primary. Keepalived checks the daemon API every two seconds and demotes if the daemon is unresponsive.

### Witness Node

The witness runs only etcd. It provides the third vote in leader election, preventing a split-brain where each of the two main nodes believes the other is down and both attempt to promote.

Without a witness, a two-node cluster cannot achieve quorum when one node fails (1-of-2 is not a majority). With a witness, the surviving main node plus the witness (2-of-3) form quorum and promotion proceeds.

The witness runs no NAS workloads and requires minimal resources (512 MB RAM, any storage).

### HA vs Single-Node: Feature Differences

| Feature | Single Node | HA Cluster |
|---------|-------------|------------|
| PostgreSQL | Local instance | Patroni + streaming replication |
| Failover | None (manual restart) | Automatic (Patroni, ~10-30s RTO) |
| ZFS pools | Always mounted | Only on primary; standby holds |
| Virtual IP | Not applicable | Keepalived VIP on primary |
| Fencing | Not applicable | IPMI or SBD (strongly recommended) |
| Witness | Not applicable | Required for safe quorum |
| OTA updates | One node, one reboot | Rolling: update standby, failover, update old primary |
| dplaned DB | `postgres://localhost/dplaneos` | `postgres://localhost:5000/dplaneos` (via HAProxy) |

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
| iSCSI, FTP, NVMe-oF, MinIO | [Optional Protocols](../admin/OPTIONAL-PROTOCOLS.md) |
| SMTP, webhook, and Telegram alerts; TOTP/2FA | [Alerts and Authentication](../admin/ALERTS.md) |
| Why NixOS exclusively | [NixOS Rationale](NIXOS-RATIONALE.md) |
| Impermanence: what persists and what does not | [NixOS Rationale - Impermanence](NIXOS-RATIONALE.md#impermanence) |
