# DPlaneOS GitOps Reference

GitOps is the mechanism that makes DPlaneOS declarative at the runtime level. This document covers how `state.yaml` is structured and how the reconciliation engine applies it. How the configuration is kept in a Git repository (files per scope, approvals, holds, pull requests) is in [GITOPS-DRIVEN-NAS.md](../admin/GITOPS-DRIVEN-NAS.md).

For the broader architectural picture, read [ARCHITECTURE.md](ARCHITECTURE.md) first.

---

## What GitOps Does

The GitOps engine closes the gap between a declared desired state (committed in `state.yaml`) and the actual live state of the system (ZFS datasets, SMB shares, NFS exports, Docker stacks, users, groups, network settings). It is the runtime counterpart to `nixos-rebuild switch`.

```
Git repository
     │
     ▼
 state.yaml ──► parser ──► DesiredState
                                │
                    ┌───────────┴───────────┐
                    ▼                       ▼
              LiveState              DiffEngine
           (ZFS, Docker,                   │
            Samba, NFS, DB)                ▼
                                      Plan
                                   ([]DiffItem)
                                       │
                                 SafeApply engine
                                       │
                             ┌─────────┴──────────┐
                             ▼                    ▼
                       DB sync (cache)     Physical execution
                    (users, shares,        (libzfs, docker compose,
                     NFS, LDAP)            exportfs, smbd)
                                                  │
                                          Convergence check
                                      (re-read live state, compare)
```

The apply engine holds a global lock while running so only one apply can execute at a time.

---

## state.yaml Format

`state.yaml` is stored in the configured Git repository and is the single source of truth for all runtime configuration. The parser is custom (no external YAML library) and validates strictly: unknown fields are rejected, and invalid values cause a parse failure rather than silent coercion.

### Top-Level Structure

```yaml
version: "6"
ignore_extraneous: false   # if true, reconciler ignores resources not in desired state

pools:        []  # ZFS pools
datasets:     []  # ZFS datasets
shares:       []  # SMB shares
nfs:          []  # NFS exports
stacks:       []  # Docker Compose stacks
system:           # hostname, DNS, NTP, firewall, networking, Samba, SSH
users:        []  # local user accounts
groups:       []  # local groups
replication:  []  # ZFS replication schedules
ldap:             # LDAP/AD directory integration
acme:             # ACME certificate automation
certificates: []  # manually provisioned TLS certificates
smart_tasks:  []  # SMART test schedules
fabrics:          # NVMe-oF targets (optional)
```

### Pools

```yaml
pools:
  - name: tank
    topology:
      data:
        - type: mirror
          disks:
            - /dev/disk/by-id/ata-WDC_WD40EFRX_00...
            - /dev/disk/by-id/ata-WDC_WD40EFRX_01...
      cache:
        - type: stripe
          disks:
            - /dev/disk/by-id/nvme-Samsung_SSD_...
      log:
        - type: mirror
          disks:
            - /dev/disk/by-id/...
            - /dev/disk/by-id/...
      spare:
        - type: stripe
          disks:
            - /dev/disk/by-id/...
    ashift: 12
    options:
      compression: lz4
      atime: "off"
    force_reshape: false
```

**Disk path rule:** Every disk entry must be a full `/dev/disk/by-id/` path. Paths like `/dev/sda` are rejected at parse time. Use the disk discovery API or Storage UI to get the correct by-id paths for this host.

**Supported vdev types:** `mirror`, `stripe`, `raidz`, `raidz1`, `raidz2`, `raidz3`, `draid`

**`force_reshape`:** When true, the reconciler allows purely additive topology changes (`zpool add` for new vdev groups, cache, log, spare). Removals are always surfaced as `MANUAL` regardless.

### Datasets

```yaml
datasets:
  - name: tank/media
    quota: 8T
    compression: lz4
    atime: "off"
    sync: standard           # standard | always | disabled
    recordsize: 1m           # 512|1k|2k|4k|8k|16k|32k|64k|128k|256k|512k|1m
    xattr: sa                # on | off | sa | dir
    secondarycache: all      # all | metadata | none
    mountpoint: /mnt/media
    encrypted: false
```

**Compression values:** `lz4`, `zstd`, `zstd-1` through `zstd-19`, `gzip`, `off`, `on`, `zle`, `lzjb`

### SMB Shares

```yaml
shares:
  - name: media
    path: /mnt/media
    read_only: false
    valid_users: "@storage alice"
    comment: "Media library"
    guest_ok: false
```

### NFS Exports

```yaml
nfs:
  - path: /mnt/media
    clients: "192.168.1.0/24"
    options: "rw,sync,no_subtree_check"
    enabled: true
```

### Docker Compose Stacks

```yaml
stacks:
  - name: jellyfin
    yaml: |
      services:
        jellyfin:
          image: jellyfin/jellyfin:latest
          ports:
            - "8096:8096"
          volumes:
            - /mnt/media:/media:ro
          restart: unless-stopped
```

Stack names must be lowercase alphanumeric, hyphens, or underscores (max 63 chars).

### System Settings

```yaml
system:
  hostname: nas-01
  timezone: Europe/Berlin
  dns_servers:
    - 1.1.1.1
    - 8.8.8.8
  ntp_servers:
    - 0.pool.ntp.org
  firewall:
    tcp: [22, 80, 443, 445, 2049]
    udp: [137, 138]
  networking:
    statics:
      eth0:
        cidr: 192.168.1.50/24
        gateway: 192.168.1.1
    bonds:
      bond0:
        mode: 802.3ad
        slaves: [eth0, eth1]
    vlans:
      eth0.100:
        parent: eth0
        vid: 100
  samba:
    workgroup: WORKGROUP
    server_string: "DPlaneOS NAS"
    time_machine: false
    allow_guest: false
    extra_global: |
      min protocol = SMB2
  ssh:
    port: 22
    password_auth: false
    permit_root_login: "no"
```

### Users and Groups

```yaml
users:
  - username: alice
    password_hash: "$2b$12$..."   # a bcrypt hash sets the password; exports write a secret reference instead
    email: alice@example.com
    role: operator
    active: true

groups:
  - name: storage
    description: "Storage team"
    gid: 1001
    members: [alice, bob]
```

### ZFS Replication

```yaml
replication:
  - name: offsite
    source_dataset: tank/data
    remote: backup-nas          # a peer from Replication > Peers, by name
    remote_pool: backup
    interval: daily             # hourly | daily | weekly | manual
    trigger_on_snapshot: true
    incremental: true
    compress: true
    rate_limit_mb: 100
    enabled: true
```

The peer (host, user, port, pinned host key, authorized replication key) is
set up under Replication › Peers; `state.yaml` refers to it by name, so the
same file works on every node. Applying fails with a clear message if no
peer of that name exists.

### LDAP

```yaml
ldap:
  enabled: true
  server: ldap.example.com
  port: 636
  use_tls: true
  bind_dn: "cn=dplaneos,ou=service,dc=example,dc=com"
  bind_password: "..."
  base_dn: "dc=example,dc=com"
  user_filter: "(objectClass=person)"
  user_id_attr: uid
  user_name_attr: cn
  user_email_attr: mail
  group_base_dn: "ou=groups,dc=example,dc=com"
  group_filter: "(objectClass=groupOfNames)"
  group_member_attr: member
  jit_provisioning: true
  default_role: viewer
  sync_interval: 3600
  timeout: 10
```

### ACME (Let's Encrypt)

```yaml
acme:
  enabled: true
  email: admin@example.com
  server: https://acme-v02.api.letsencrypt.org/directory
  domains:
    - nas.example.com
  resolver: http              # http | tls | dns provider name
  dns_config:
    CLOUDFLARE_API_TOKEN: "..."
```

### SMART Tasks

```yaml
smart_tasks:
  - device: /dev/disk/by-id/ata-WDC_WD40EFRX_...
    type: short               # short | long | conveyance
    schedule: "*-*-* 03:00:00"  # systemd calendar expression (not cron): "daily", "Sun *-*-* 02:00:00"
    enabled: true
```

Each task is a systemd timer. Schedules added on the Hardware page are
stored the same way and written back to the state file.

### NVMe-oF Fabrics (optional)

```yaml
fabrics:
  nvme:
    - subsystem_nqn: nqn.2024-01.io.dplaneos:tank-data
      zvol: tank/data-vol
      transport: tcp          # only tcp supported
      listen_addr: 0.0.0.0
      listen_port: 4420
      namespace_id: 1
      allow_any_host: false
      host_nqns:
        - nqn.2024-01.io.initiator:host1
```

---

## Reconciliation: How Apply Works

Every apply (a change from the Git repository, approved or automatic; a change from another node; a rollback; `dplaned -apply`) runs the following steps:

### 1. Parse and Validate

The engine fetches `state.yaml` from the configured Git repository checkout and parses it. Any parse or validation error aborts immediately - nothing is changed.

Validation rules include:
- `version` must be `"1"` or `"6"` (`"6"` is recommended for new configurations)
- All disk paths must begin with `/dev/disk/by-id/`
- Pool and dataset names must match `[a-zA-Z0-9][a-zA-Z0-9/_\-\.]*`
- Stack names must be lowercase alphanumeric + hyphens/underscores
- Compression values must be from the supported set
- `atime`, `sync`, `xattr`, `secondarycache` must be valid enum values

### 2. Read Live State

The engine reads the current state of all managed resources: `zfs list`, `zpool list`, Docker stacks, Samba shares (from DB), NFS exports, users, groups, LDAP config.

### 3. Compute the Diff Plan

Each resource in both desired and live state is compared and assigned a `DiffItem` with one of these kinds:

| Kind | Meaning |
|------|---------|
| `CREATE` | Resource exists in desired state but not in live state. Will be created. |
| `MODIFY` | Resource exists in both but with different properties. Will be updated. |
| `DELETE` | Resource exists in live state but not in desired state (and `ignore_extraneous` is false). Will be removed. |
| `NOP` | Resource matches exactly. No action needed. |
| `BLOCKED` | Change is potentially destructive (pool removal, deletion of a dataset that holds files, snapshots or children, removal of a share with connected clients). Requires explicit approval; a dataset or pool that holds anything is not destroyed even then. |
| `MANUAL` | Change cannot be automated (pool shrink, removing a log/cache device from a running pool). Shown in UI for operator action. |
| `AMBIGUOUS` | Reconciler cannot determine safe intent (e.g., same dataset name, different pool). Requires manual resolution. |

### 4. Check for BLOCKED Items

A `BLOCKED` item (for example removing a share with connected clients, or a dataset that holds data) is never executed, approved or not: the apply fails with the reason, and the Git page shows it on the scope's line. Resolve it by hand (disconnect the clients, destroy the dataset yourself if it is no longer needed) and apply again (**Resume**, or the next check). Approving a scope (`POST /api/gitops/approve {scope, sha}`) applies the repository's changes at that commit; it does not override a block.

**Data is never destroyed.** Git holds the shape of the storage, not its contents. A dataset is destroyed only when it is provably empty: a mounted filesystem with an empty directory, no snapshots and no child datasets (a volume never qualifies, its content cannot be inspected). ZFS's `used` cannot decide this: even an empty filesystem reports about 100 KiB of metadata. A dataset or pool that holds anything stays BLOCKED, and approving it does not change that: the apply fails with what the dataset holds. Destroy it by hand (`zfs destroy -r`) when it is no longer needed; the next apply converges.

### 5. Execute the Plan

Items execute in order: `CREATE` first, then `MODIFY`, then `DELETE`. If any step fails, execution halts immediately. Already-applied steps are not rolled back - ZFS operations are safe by design (creating a dataset that already exists is a no-op; setting a property is idempotent).

The engine runs two categories of changes in parallel:

**Stateless DB sync (cache):** Users, groups, shares, NFS exports, LDAP config, and replication schedules are synced from the desired state directly into the database. The database is treated as a read cache - it never overrides state.yaml.

**Physical execution:** ZFS datasets, pools, Docker stacks, Samba config (`smb.conf`), NFS exports (`/etc/exports`), and system networking are applied by the engine. ZFS operations (dataset create/modify/destroy, pool create/destroy, pool properties) go through `internal/libzfs` - either the native cgo libzfs path or the subprocess fallback depending on build tags. Docker, Samba, NFS, and network operations go through the exec allowlist.

### 6. Convergence Check

After execution, the engine re-reads the live state and verifies it matches the desired state. The result is reported as:

- `CONVERGED` - all desired items match live state
- `DEGRADED` - most items converged but some have minor deviations (e.g., a quota reported in a slightly different unit)
- `NOT_CONVERGED` - significant mismatch after apply (indicates a bug or race condition)
- `ERROR` - convergence check itself failed

---

## Changes Made Outside the Repository

A change made in the web interface, through the API or on the command line is recorded in the configuration history (right after a web UI change, and every 5 minutes for everything else) and written to the repository by the node that owns its scope. There is no separate drift state: what differs between a node and the repository is shown per scope on the Git page as *not yet in the repository*, *changed in the repository* or a conflict. With self-heal on, changes made outside the web interface are replaced by the repository's version instead.

`GET /api/config/export` returns the current configuration as one `state.yaml` at any time, with or without Git.

---

## API Reference

| Method | Path | Description |
|--------|------|-------------|
| `GET` | `/api/gitops/status` | Last check: newest commit, remotes, per scope what is in step, waiting, conflicting, held; open holds |
| `GET` | `/api/gitops/plan` | What waits, for the scopes this node writes |
| `POST` | `/api/gitops/sync` | Check now |
| `GET` / `PUT` | `/api/gitops/settings` | Repository settings (`webhook_secret`: `""` keep, `"generate"`, `"none"`) |
| `POST` | `/api/gitops/approve` | `{scope, sha}` apply the repository's changes of a scope at that commit |
| `POST` | `/api/gitops/take` | `{scope}` replace this node's changes with the repository's version |
| `POST` | `/api/gitops/holds/{scope}/resume` | Clear a hold and try again |
| `POST` | `/api/gitops/holds/{scope}/revert` | Write back the last good version and clear the hold |
| `GET` | `/api/gitops/state?scope=` | A scope's file in the newest commit |
| `PUT` | `/api/gitops/nixos-repo` | `{repo_id}` the tracked repository with the NixOS configuration |
| `POST` | `/api/gitops/webhook` | Push notification (HMAC signature or GitLab token; no session) |

---

## Common Errors

| Error | Cause | Resolution |
|-------|-------|------------|
| `disk must use /dev/disk/by-id/ path` | `state.yaml` contains an `/dev/sdX` path | Replace with the stable by-id path from the disk discovery UI |
| `unsupported state.yaml version` | `version` field is not `"1"` or `"6"` | Set `version: "1"` |
| `needs manual action` | A destructive change (dataset with data, share with open connections) | Do it by hand after checking, or change the commit |
| `global lock held` | Another apply is running | Wait for the current apply to complete |
| `data-not-ready` | A Docker stack requires a ZFS dataset that is not yet mounted | Ensure the dataset is created before the stack, or add it to the desired state |
| `validation errors` | `state.yaml` schema violation | Read the error message; it identifies the exact field and index |
