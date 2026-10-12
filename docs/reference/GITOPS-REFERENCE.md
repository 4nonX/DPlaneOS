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
                    (users, shares,        (zfs, zpool, docker compose,
                     NFS, LDAP)            exportfs, smbd)
                                                  │
                                          Convergence check
                                      (re-read live state, compare)
```

The apply engine holds a global lock while running so only one apply can execute at a time.

---

## state.yaml Format

`state.yaml` is stored in the configured Git repository and is the single source of truth for all runtime configuration. The parser is custom (no external YAML library) and validates strictly: a section or setting it does not know is an error that names the place (`datasets[1].qouta`), and invalid values cause a parse failure rather than silent coercion. Nothing is applied from a file that does not validate.

Not part of `state.yaml`: certificates and their keys (Security › Certificates; a private key does not belong in a repository), the directory bind password, user passwords in clear text, iSCSI targets, the UPS, SSH keys.

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

**`options`:** properties of the pool (`autoexpand`, `autoreplace`, `autotrim`, `failmode`, `listsnapshots`, `comment`) and of its root dataset, which its datasets inherit (`compression`, `atime`, `recordsize`, `sync`, `xattr`, `acltype`, `secondarycache`, `copies`, `dedup`). Each is set where ZFS keeps it and corrected when it differs. `relatime`, `dnodesize`, `normalization`, `casesensitivity` and `utf8only` are used when the pool is created and not compared afterwards. Any other key is refused.

**Disks and partitions:** name the disk. ZFS partitions a disk it is given whole and reports the first partition (`…-part1`); DPlaneOS compares the disks, so the pool matches its declaration. A partition you gave ZFS on purpose is named as that partition.

**What DPlaneOS writes:** with *Storage* switched on under GitOps › Settings, the file it commits records each pool as it is built (mirrors, raidz groups, log, cache, spare, special), with its GUID. While a disk is being replaced or a spare has stepped in, it does not commit at all and says why on the GitOps page, because the layout of that moment is not the one to keep.

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
    # Optional. One that is left out stays as it is on the system.
    shadow_copy: true            # snapshots as "Previous Versions"
    recycle_bin: true
    time_machine: false
    time_machine_quota: "500G"
    hosts_allow: "192.168.1.0/24"
    hosts_deny: ""
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

How the section is applied:

- `hostname`, `timezone`, `dns_servers` and `ntp_servers` are changed only when given. DNS servers are used in the order listed.
- `firewall` lists the ports that are open to everyone, completely: a port that is not listed is closed. Rules made on the Firewall page that allow a port for one source only, or deny it, are not part of `state.yaml` and stay as they are.
- `networking` describes static addresses, bonds and VLANs completely once it names any of them: an interface whose static address is no longer listed goes back to DHCP, a bond or VLAN that is no longer listed is removed. A section without `networking` leaves the interfaces alone. Network changes are applied at once and without the trial period the Network page uses, so the plan marks them as high risk: check the address of the interface you reach the system through before you apply.
- The UPS, the web certificate and SSH keys are not part of `state.yaml`.

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

The bind password is not part of the file (`bind_password` is refused): set it under Identity › Directory, where it is stored encrypted. An apply never changes it, and a setting the file leaves out keeps the value it has.

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

Triggering an apply (`POST /api/gitops/apply`) runs the following steps:

### 1. Parse and Validate

The engine reads `state.yaml` from the local clone of the repository and parses it. The clone is brought up to date by the drift check (every five minutes, or `POST /api/gitops/check`, or the Git server's webhook); an apply itself does not fetch. Any parse or validation error aborts immediately - nothing is changed.

Validation rules include:
- every section and setting must be one this page describes
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

**Physical execution:** ZFS datasets, pools, Docker stacks, Samba config (`smb.conf`), NFS exports (`/etc/exports`), and system networking are applied by the engine. ZFS operations (dataset create/modify/destroy, pool create/destroy, pool properties) are run as `zfs` and `zpool` commands through the exec allowlist. Docker, Samba, NFS, and network operations go through the exec allowlist.

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

**Drift in a cluster:** every node keeps its clone current, but only the GitOps writer (the node that serves every storage group, while the cluster has quorum) compares and applies. See [Git-Driven NAS, Clusters](../admin/GITOPS-DRIVEN-NAS.md#clusters).

---

## Capture: Live State to YAML

Capture replaces sections of the local `state.yaml` with what the system has now. Sections you do not name are left as they are. Use it to bring an already configured system under Git, one category at a time, under GitOps › Settings.

```
POST /api/gitops/capture
{"categories": ["smb", "nfs", "users", "groups", "stacks", "replication", "system"]}
```

| Category | What is written |
|----------|-----------------|
| `smb` | shares with their options |
| `nfs` | exports |
| `users` | name, e-mail, role, active. Never a password or its hash: a user without `password_hash` in the file keeps the password it has |
| `groups` | name, description, GID, members |
| `stacks` | the Compose file of each stack |
| `replication` | schedules (the peer by name) |
| `system` | host name, time zone, DNS and time servers, firewall ports, network, Samba and SSH settings |

Pools and datasets have no capture: they are recorded by the write-back below when *Storage* is switched on.

The answer lists what was captured and how many resources of each category are now in the file; a drift check follows at once.

**Write-back.** With a repository linked, DPlaneOS itself commits and pushes a snapshot of the live state after every change made in the web interface, and on *Sync Now*: all categories that are switched on under GitOps › Settings, generated from the system. The file in the repository is therefore rewritten by the system; comments in it are not kept.

---

## Git Repository Setup

The engine expects `state.yaml` at the root of the repository's branch.

1. **GitOps › Credentials**: add what the Git server accepts for this repository: an access token (HTTPS), or an SSH private key whose public key is a deploy key there. It needs write access if the system is to commit its own changes (write-back), read access otherwise. Stored encrypted.
2. **GitOps › Repositories › Link Repository** (or the wizard on the Settings tab): the URL, the branch, the credential, and the name and e-mail for the system's commits.
3. **GitOps › Settings**: switch GitOps on, choose the repository as *System State*, and choose the categories the system writes back.

The clone lives in `/var/lib/dplaneos/gitops/` and survives restarts and updates.

The same through the API:

```
POST /api/git-sync/credentials   {"name": "state-repo", "host": "github.com", "auth_type": "token", "token": "..."}
                                 {"name": "state-repo", "host": "github.com", "auth_type": "ssh", "ssh_key": "-----BEGIN OPENSSH PRIVATE KEY-----..."}
POST /api/git-sync/repos         {"name": "nas-state", "repo_url": "https://github.com/yourorg/nas-state.git", "branch": "main",
                                  "credential_id": 1, "commit_name": "nas-01", "commit_email": "nas-01@example.com"}
PUT  /api/gitops/settings        {"enabled": true, "repo_id": 1, "sync_storage": true, "sync_access": true, "sync_app": true,
                                  "sync_identity": true, "sync_protection": true, "sync_system": true, "auto_apply": false}
```

---

## API Reference

| Method | Path | Description |
|--------|------|-------------|
| `GET` | `/api/gitops/status` | Whether this node is the writer, drift, last check, last commit of the system, the commit the system corresponds to |
| `POST` | `/api/gitops/check` | Fetch the repository and compare it with the system now |
| `GET` | `/api/gitops/plan` | The plan: `changes` (what would be done), `plan` (all items), `plan_summary` |
| `POST` | `/api/gitops/apply` | Apply the `state.yaml` of the local clone |
| `POST` | `/api/gitops/approve` | Approve a BLOCKED item: `{"kind": "dataset", "name": "tank/old", "reason": "..."}` |
| `GET` / `PUT` | `/api/gitops/state` | Read or replace the local `state.yaml` |
| `GET` / `PUT` | `/api/gitops/settings` | Repository, categories, automatic apply, webhook secret |
| `POST` | `/api/gitops/sync` | Fetch, then commit and push a snapshot of the live state (*Sync Now*) |
| `POST` | `/api/gitops/capture` | Replace sections of `state.yaml` with the live state |
| `GET` | `/api/gitops/managed-summary` | Per category: resources in the file and on the system |
| `POST` | `/api/gitops/webhook` | Called by the Git server on a push (signed) |
| | `/api/git-sync/credentials`, `/api/git-sync/repos` | Credentials and repositories (above) |

---

## Common Errors

| Error | Cause | Resolution |
|-------|-------|------------|
| `disk must use /dev/disk/by-id/ path` | `state.yaml` contains an `/dev/sdX` path | Replace with the stable by-id path from the disk discovery UI |
| `unsupported state.yaml version` | `version` field is not `"1"` or `"6"` | Set `version: "6"` |
| `unknown settings` | A section or key the format does not have (misspelt, or from another version) | The message names each place; correct or remove it |
| `plan contains BLOCKED items` | A destructive change needs approval | Review the blocked items in the UI and approve explicitly |
| `global lock held` | Another apply is running | Wait for the current apply to complete |
| `data-not-ready` | A Docker stack requires a ZFS dataset that is not yet mounted | Ensure the dataset is created before the stack, or add it to the desired state |
| `validation errors` | `state.yaml` schema violation | Read the error message; it identifies the exact field and index |
