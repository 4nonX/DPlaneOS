# DPlaneOS GitOps Reference

GitOps is the mechanism that makes DPlaneOS declarative at the runtime level. This document covers how `state.yaml` is structured, how the reconciliation engine applies it, how drift is detected, and how to use the Capture workflow to generate state from a live system.

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
version: "1"
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
    password_hash: "$2b$12$..."   # bcrypt hash; capture never exports plaintext
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
  - name: offsite-backup
    source_dataset: tank/data
    remote_host: backup.example.com
    remote_user: replication
    remote_port: 22
    remote_pool: backup
    ssh_key_path: /var/lib/dplaneos/keys/replication_ed25519
    interval: "0 2 * * *"      # cron expression
    trigger_on_snapshot: true
    compress: true
    rate_limit_mb: 100
    enabled: true
```

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
    type: short               # short | long | offline
    schedule: "0 3 * * *"
    enabled: true
```

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

Triggering an apply (`POST /api/gitops/apply`) runs the following steps:

### 1. Parse and Validate

The engine fetches `state.yaml` from the configured Git repository checkout and parses it. Any parse or validation error aborts immediately - nothing is changed.

Validation rules include:
- `version` must be `"1"` or `"6"`
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
| `BLOCKED` | Change is potentially destructive (pool topology removal, dataset deletion with data). Requires explicit approval. |
| `MANUAL` | Change cannot be automated (pool shrink, removing a log/cache device from a running pool). Shown in UI for operator action. |
| `AMBIGUOUS` | Reconciler cannot determine safe intent (e.g., same dataset name, different pool). Requires manual resolution. |

### 4. Check for BLOCKED Items

If any item is `BLOCKED` and has not been explicitly approved via `POST /api/gitops/approve`, the plan halts with an error. The UI shows which items need approval and what the consequences are.

To approve a single blocked item:
```
POST /api/gitops/approve
{"item_id": "datasets/tank/old-data", "reason": "confirmed empty dataset"}
```

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

## Drift Detection

A background goroutine runs every five minutes and computes the same diff plan without applying it. If any `CREATE`, `MODIFY`, or `DELETE` items are found, a drift event is broadcast to all connected WebSocket clients and the UI displays a drift banner.

Operators can also trigger an immediate drift check:
```
POST /api/gitops/check
```

**When drift is expected:** If you make a change via the UI or API (e.g., add an NFS export) but have not updated `state.yaml`, the drift detector will report drift on the next cycle. This is normal. Either update `state.yaml` manually, or use the Capture workflow (below) to generate the updated YAML.

**Drift in HA mode:** Each node runs the drift detector independently. In practice only the primary should be reporting drift, since the standby does not serve ZFS pools or Docker stacks. The drift detector consults Patroni before running to avoid false positives on the standby.

---

## Capture: Live State to YAML

The Capture workflow reads the live system state and generates the corresponding `state.yaml` sections. Use it when you have made UI-driven changes and want to commit them to Git.

```
POST /api/gitops/capture
{"categories": ["pools", "datasets", "shares", "nfs", "stacks", "users", "groups"]}
```

**What Capture does and does not export:**

| Resource | Exported | Notes |
|----------|----------|-------|
| ZFS pools | Yes | Topology, ashift, properties |
| ZFS datasets | Yes | All properties |
| SMB shares | Yes | Path, permissions, comment |
| NFS exports | Yes | Path, clients, options |
| Docker stacks | Yes | Full compose YAML |
| Users | Yes | Username, email, role, active - NOT passwords |
| Groups | Yes | Name, GID, members |
| Passwords | Never | Capture never exports password hashes |

The captured YAML is returned in the API response for review. It is never automatically committed - the operator reviews, edits if needed, and commits manually.

**Workflow:**
1. Make changes via the UI or API
2. `POST /api/gitops/capture` with the relevant categories
3. Review the output
4. Commit the updated `state.yaml` to Git
5. Run `POST /api/gitops/apply` or let the drift detector confirm convergence

---

## Git Repository Setup

The GitOps engine expects a Git repository with `state.yaml` at the repository root.

Configure the repository in Settings: GitOps, or via `state.yaml` itself under `system:`:

```
POST /api/gitops/config
{
  "repo_url": "git@github.com:yourorg/nas-state.git",
  "branch": "main",
  "ssh_key_path": "/var/lib/dplaneos/gitops/deploy_key"
}
```

For private repositories, generate a deploy key and add it to the repository:
```bash
ssh-keygen -t ed25519 -f /var/lib/dplaneos/gitops/deploy_key -N ""
cat /var/lib/dplaneos/gitops/deploy_key.pub
# Add this public key as a deploy key on GitHub/GitLab with read access
```

The daemon clones and fetches the repository to `/var/lib/dplaneos/gitops/repo/`. This path is on `/persist` and survives reboots.

---

## API Reference

| Method | Path | Description |
|--------|------|-------------|
| `GET` | `/api/gitops/status` | Current sync status, last apply result, drift state |
| `GET` | `/api/gitops/config` | Repository URL, branch, key path |
| `POST` | `/api/gitops/config` | Update repository configuration |
| `POST` | `/api/gitops/apply` | Pull latest state.yaml and apply |
| `POST` | `/api/gitops/check` | Compute drift plan without applying |
| `GET` | `/api/gitops/plan` | Last computed plan (items, kinds, descriptions) |
| `POST` | `/api/gitops/approve` | Approve a BLOCKED item |
| `POST` | `/api/gitops/capture` | Generate state.yaml from live system |
| `GET` | `/api/gitops/history` | Apply history log |

---

## Common Errors

| Error | Cause | Resolution |
|-------|-------|------------|
| `disk must use /dev/disk/by-id/ path` | `state.yaml` contains an `/dev/sdX` path | Replace with the stable by-id path from the disk discovery UI |
| `unsupported state.yaml version` | `version` field is not `"1"` or `"6"` | Set `version: "1"` |
| `plan contains BLOCKED items` | A destructive change needs approval | Review the blocked items in the UI and approve explicitly |
| `global lock held` | Another apply is running | Wait for the current apply to complete |
| `data-not-ready` | A Docker stack requires a ZFS dataset that is not yet mounted | Ensure the dataset is created before the stack, or add it to the desired state |
| `validation errors` | `state.yaml` schema violation | Read the error message; it identifies the exact field and index |
