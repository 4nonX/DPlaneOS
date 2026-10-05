# Git-Driven NAS: Operating DPlaneOS via state.yaml

DPlaneOS can be operated entirely through Git commits. Every share, dataset, user, Docker stack, and network setting is expressible in `state.yaml`. When you push a change, DPlaneOS applies it. When you revert a commit, DPlaneOS rolls back to the previous state. When a setting drifts away from what Git says, DPlaneOS tells you.

This guide covers the complete workflow from first commit to production operations, including secret handling, auto-apply on push, rollback, and HA environments.

For the technical format spec (every field, validation rules, enum values), see [GITOPS-REFERENCE.md](../reference/GITOPS-REFERENCE.md).

---

## Why Git-Driven

The configuration of a NAS accretes over years: shares added, users removed, quotas adjusted, containers started and forgotten. Without a canonical record, the live system becomes the only source of truth, and that source can be destroyed.

Git-driven operation gives you:

- **Audit history.** Every share ever created, with who created it and why, is in the commit log.
- **Reproducibility.** A replacement machine can be brought to the same state by cloning the repository and running a single apply.
- **Safe changes.** The reconciler plans before executing. You see what will happen before it happens, and destructive changes require explicit approval.
- **Rollback.** `git revert` + apply undoes any change without manual bookkeeping.
- **Review.** Share configuration changes go through the same pull request process as application code.

---

## Concepts

### state.yaml

`state.yaml` is the single source of truth for all runtime configuration managed by DPlaneOS. It lives in a Git repository you control. The file is structured YAML with a strict schema: unknown fields are rejected rather than silently ignored.

### Reconciler

The reconciler compares `state.yaml` (desired state) against the live system (actual state) and produces a plan: which resources to create, modify, delete, or leave alone. You review the plan before it executes.

### Drift detection

Every five minutes, the reconciler runs a plan without executing it. If any differences are found between `state.yaml` and the live system, a drift banner appears in the UI. Drift is expected if you make changes via the web UI and have not yet updated `state.yaml` to match.

### Capture

Capture reads the live system and produces the corresponding `state.yaml` YAML for review. It never commits automatically. You use it to bootstrap the initial `state.yaml` from an already-configured system, or to generate YAML for a change you made interactively.

---

## Initial Setup

### Step 1: Create the state repository

Create a private Git repository. Any Git host works (GitHub, GitLab, Gitea, a bare repo on your backup server). The repository needs a single file at the root: `state.yaml`.

```
nas-state/
  state.yaml
  README.md   (optional)
```

### Step 2: Generate a deploy key

The DPlaneOS daemon needs read access to the repository. Generate a dedicated key:

```bash
ssh-keygen -t ed25519 -f /var/lib/dplaneos/gitops/deploy_key -N ""
cat /var/lib/dplaneos/gitops/deploy_key.pub
```

Add the public key to your repository as a deploy key with **read-only** access. If you want the daemon to be able to write back (for auto-commit of captures, or future features), grant read-write. For security, read-only is sufficient for the apply workflow.

### Step 3: Configure the repository in DPlaneOS

Navigate to **Settings - GitOps** and enter:

- **Repository URL:** `git@github.com:yourorg/nas-state.git`
- **Branch:** `main`
- **SSH key path:** `/var/lib/dplaneos/gitops/deploy_key`

Or via API:

```bash
curl -X POST http://nas/api/gitops/config \
  -H "X-Session-ID: $SESSION" \
  -H "Content-Type: application/json" \
  -d '{
    "repo_url": "git@github.com:yourorg/nas-state.git",
    "branch": "main",
    "ssh_key_path": "/var/lib/dplaneos/gitops/deploy_key"
  }'
```

The daemon clones the repository to `/var/lib/dplaneos/gitops/repo/`. This path is on `/persist` and survives reboots.

---

## Bootstrap: From Live System to First Commit

If you have already configured your NAS via the web UI and want to bring it under Git control, use Capture to generate the initial `state.yaml`.

### Capture everything

In the UI: **GitOps - Capture**. Select all categories and review the output.

Via API:

```bash
curl -X POST http://nas/api/gitops/capture \
  -H "X-Session-ID: $SESSION" \
  -H "Content-Type: application/json" \
  -d '{"categories": ["pools", "datasets", "shares", "nfs", "stacks", "users", "groups", "system"]}' \
  | jq -r '.yaml'
```

The output is a complete `state.yaml` reflecting the current live state. Paste it into your repository, review it carefully, and commit.

**What Capture exports and does not export:**

| Resource | Exported | Notes |
|----------|----------|-------|
| ZFS pools | Yes | Topology, ashift, properties |
| ZFS datasets | Yes | All managed properties |
| SMB shares | Yes | Path, permissions, comment |
| NFS exports | Yes | Path, clients, options |
| Docker stacks | Yes | Full Compose YAML |
| Users | Yes | Username, email, role, active |
| Groups | Yes | Name, GID, members |
| Passwords | **Never** | Hashes are captured, plaintexts are never stored anywhere |
| LDAP bind password | **Never** | Set this field manually after capture |
| SSH private keys | **Never** | Reference by path only |

After committing the captured state, run an apply immediately to verify that the reconciler computes an all-NOP plan (meaning the captured YAML matches reality exactly):

```bash
curl -X POST http://nas/api/gitops/apply \
  -H "X-Session-ID: $SESSION"
```

If you see any CREATE, MODIFY, or DELETE items, resolve them before considering the bootstrap complete.

---

## Day-to-Day Workflow

### Making a change

1. Edit `state.yaml` in your editor or via the GitHub/GitLab web UI
2. Commit with a meaningful message: `add media share for alice and bob`
3. Push to main
4. Pull and apply in DPlaneOS

The apply fetches the latest commit and runs the reconciler. For small changes (adding a share, changing a quota), the full cycle completes in seconds.

**Via UI:** GitOps page - Pull and Apply button.

**Via API:**
```bash
curl -X POST http://nas/api/gitops/apply \
  -H "X-Session-ID: $SESSION"
```

**Via CLI (from the NAS itself):**
```bash
curl -s -X POST -H "X-Session-ID: $(cat /tmp/session)" \
  http://localhost/api/gitops/apply
```

### Changes made in the web UI

A change made in the web UI takes effect immediately and is then written back to the repository as a snapshot commit of the live state ("feat: infrastructure state update via DPlaneOS"). Before committing, DPlaneOS fetches the repository:

- If the repository contains commits that are **not applied to this system yet**, the write-back is refused instead of committing over them (a snapshot of the live state would silently revert those commits). The UI change stays live; the GitOps page shows the refusal. Apply (or review) the pending commits, and the next write-back includes your change.
- Snapshot commits that were never pushed are regenerated from the live state, so the local clone is reset to the remote when the two have diverged.
- A failed push or fetch is shown on the GitOps page (`last_commit` in `GET /api/gitops/status`) and broadcast as a `gitops.commit_failed` event; it is never only logged.

The commit the live system corresponds to is reported as `synced_commit` in `GET /api/gitops/status`. It moves forward when this node pushes a snapshot or when an apply completes without deferred items.

### Reviewing the plan before applying

If you want to see what will happen before committing to it:

```bash
# Pull latest state.yaml without applying
curl -X POST http://nas/api/gitops/check \
  -H "X-Session-ID: $SESSION"

# Read the resulting plan
curl http://nas/api/gitops/plan \
  -H "X-Session-ID: $SESSION" | jq '.items'
```

The plan shows each resource with its kind (`CREATE`, `MODIFY`, `DELETE`, `NOP`, `BLOCKED`, `MANUAL`) and a human-readable description of what will change.

---

## Auto-Apply on Push

> **Not implemented yet.** Earlier versions of this guide described polling and webhook-based auto-apply (`/api/gitops/webhook`, an "Auto-apply on change" setting). Neither exists in the daemon. Automatic apply with safe defaults (no self-heal or prune by default, an empty-state guard, retry with backoff, and a hold instead of automatic revert on failure) is planned in [Design 0001](../design/0001-distributed-state-gitops-ha.md), Phase 5 ([ADR-0007](../design/adr/ADR-0007-gitops-defaults.md)).

What happens today:

- The drift detector fetches the repository every 5 minutes, fast-forwards the local clone and reports drift between the repository and the live system on the GitOps page.
- Apply is always explicit: the **Apply** button on the GitOps page or `POST /api/gitops/apply`.

---

## A Complete state.yaml Example

This is a fully annotated `state.yaml` for a typical home/small-business NAS with two users, a media share, a Docker stack, and NFS for Linux clients.

```yaml
version: "6"

# When false, resources that exist on the live system but are absent
# from state.yaml will appear as DELETE items. Set to true during
# migration if you want to manage only a subset of resources via Git.
ignore_extraneous: false

# ── Storage ────────────────────────────────────────────────────────────────

pools:
  - name: tank
    topology:
      data:
        - type: mirror
          disks:
            # Always use /dev/disk/by-id/ paths - never /dev/sda etc.
            # Find these via Storage - Disks in the web UI.
            - /dev/disk/by-id/ata-WDC_WD40EFRX_WD-WCC7K0ABCDEF
            - /dev/disk/by-id/ata-WDC_WD40EFRX_WD-WCC7K0FEDCBA
    ashift: 12
    options:
      compression: lz4
      atime: "off"

datasets:
  - name: tank/media
    quota: 8T
    compression: lz4
    atime: "off"
    recordsize: 1m        # Large records for sequential media reads
    mountpoint: /mnt/media

  - name: tank/backups
    quota: 2T
    compression: zstd
    atime: "off"
    recordsize: 128k

  - name: tank/docker
    quota: 500G
    compression: lz4
    atime: "off"
    mountpoint: /var/lib/docker

# ── Sharing ─────────────────────────────────────────────────────────────────

shares:
  # SMB share for Windows and macOS clients
  - name: media
    path: /mnt/media
    read_only: false
    valid_users: "@family"    # Only members of the family group
    comment: "Family media library"
    guest_ok: false

  - name: backups
    path: /mnt/backups
    read_only: false
    valid_users: "alice"
    comment: "Alice's backup target"
    guest_ok: false

nfs:
  # NFS export for Linux clients (e.g. a home server doing ZFS send/recv)
  - path: /mnt/media
    clients: "192.168.1.0/24"
    options: "rw,sync,no_subtree_check,no_root_squash"
    enabled: true

# ── Containers ──────────────────────────────────────────────────────────────

stacks:
  - name: jellyfin
    yaml: |
      services:
        jellyfin:
          image: jellyfin/jellyfin:10.9
          network_mode: host
          volumes:
            - /mnt/media:/media:ro
            - /var/lib/docker/jellyfin/config:/config
            - /var/lib/docker/jellyfin/cache:/cache
          environment:
            - JELLYFIN_PublishedServerUrl=http://nas.local:8096
          restart: unless-stopped

  - name: vaultwarden
    yaml: |
      services:
        vaultwarden:
          image: vaultwarden/server:latest
          ports:
            - "127.0.0.1:8080:80"
          volumes:
            - /var/lib/docker/vaultwarden:/data
          environment:
            - WEBSOCKET_ENABLED=true
            # ADMIN_TOKEN is intentionally absent here - set it in
            # /var/lib/docker/vaultwarden/.env or via Docker secrets.
            # Never commit credentials to state.yaml.
          restart: unless-stopped

# ── Users and Groups ─────────────────────────────────────────────────────────

users:
  - username: alice
    # Generate with: python3 -c "import bcrypt; print(bcrypt.hashpw(b'password', bcrypt.gensalt()).decode())"
    # Or use the web UI to create the user and then capture.
    password_hash: "$2b$12$Q9wLkJt0eXa7pVq3sR1mUuH8cN2bD5fG6hJ7kL8mN9pQ0rS1tU2vW"
    email: alice@example.com
    role: admin
    active: true

  - username: bob
    password_hash: "$2b$12$Z1yX2wV3uT4sR5qP6oN7mOeL8kJ9iH0gF1eD2cB3aZ4yX5wV6uT7s"
    email: bob@example.com
    role: operator
    active: true

groups:
  - name: family
    description: "Family members with media access"
    gid: 1001
    members: [alice, bob]

# ── System ───────────────────────────────────────────────────────────────────

system:
  hostname: nas-01
  timezone: Europe/Berlin
  dns_servers:
    - 1.1.1.1
    - 8.8.8.8
  ntp_servers:
    - 0.europe.pool.ntp.org
    - 1.europe.pool.ntp.org
  firewall:
    tcp: [22, 80, 443, 445, 2049, 8096]   # SSH, HTTP, HTTPS, SMB, NFS, Jellyfin
    udp: [137, 138]                         # NetBIOS for Samba discovery
  networking:
    statics:
      eth0:
        cidr: 192.168.1.50/24
        gateway: 192.168.1.1
  samba:
    workgroup: HOME
    server_string: "Home NAS"
    time_machine: false
    allow_guest: false
  ssh:
    port: 22
    password_auth: false
    permit_root_login: "no"
```

---

## Secret Handling

`state.yaml` is committed to Git. Do not commit secrets to it.

### What belongs in state.yaml

- Password **hashes** (bcrypt, `$2a$`/`$2b$`/`$2y$`, 60 characters) for local users. Validation rejects anything else in `password_hash`, which catches plain-text passwords pasted by mistake.
- Usernames, email addresses, roles
- Share paths, permissions, comments
- NFS client CIDRs
- Docker image names and non-sensitive environment variables
- Hostnames, IP addresses, DNS servers
- Firewall rules

### What does not belong in state.yaml

| Secret | Alternative |
|--------|-------------|
| LDAP bind password | Set in Directory settings in the web UI; stored encrypted in the database. `ldap.bind_password` in `state.yaml` is rejected by validation (earlier versions stored it unencrypted, which broke LDAP binds) |
| ACME DNS API tokens | Set in the DPlaneOS secrets store; referenced by name in state.yaml |
| Docker container passwords/tokens | Use Docker secrets, environment files outside `/var/lib/docker` (on `/persist`), or a secrets manager sidecar |
| SSH private keys | Reference by path only; key files live on `/persist` outside Git |
| Webhook secrets | Configured in Settings only |

### Handling Docker container credentials

The `yaml` field in a stack entry is pure Docker Compose YAML. Instead of embedding credentials:

```yaml
# Don't do this
stacks:
  - name: myapp
    yaml: |
      services:
        myapp:
          environment:
            - DATABASE_PASSWORD=hunter2   # This ends up in Git
```

Use an env_file that lives on `/persist` (which survives reboots but is not in Git):

```yaml
# Do this instead
stacks:
  - name: myapp
    yaml: |
      services:
        myapp:
          env_file:
            - /persist/secrets/myapp.env   # Created manually on the NAS
```

Create the secrets file on the NAS once:
```bash
cat > /persist/secrets/myapp.env <<EOF
DATABASE_PASSWORD=hunter2
API_KEY=secret
EOF
chmod 600 /persist/secrets/myapp.env
```

---

## Rollback

Rollback is a git operation followed by an apply.

### Revert a single commit

```bash
git revert abc1234 --no-edit
git push
```

Then trigger apply. The reconciler will compute the diff between the reverted state and the live system and return it to the previous configuration.

### Revert to an arbitrary point

```bash
# Find the commit you want to return to
git log --oneline

# Reset to that commit (creates a new commit, does not rewrite history)
git revert HEAD~3..HEAD --no-commit
git commit -m "revert to state from 3 commits ago"
git push
```

### Emergency revert without Git

If Git is inaccessible (network outage, repository issue), you can revert directly from the NAS:

```bash
cd /var/lib/dplaneos/gitops/repo
git log --oneline    # Find the previous good commit
git checkout abc1234 -- state.yaml    # Restore the file
curl -X POST http://localhost/api/gitops/apply \
  -H "X-Session-ID: $(cat /tmp/session)"
```

This applies the previous state without touching the remote repository. Once connectivity is restored, update the remote to match.

---

## Pull Request Workflow

For teams or when you want a review step before changes are applied:

### Setup

1. Set DPlaneOS to poll or listen on a non-main branch: `staging`
2. Protect `main` with required reviews in your repository settings
3. Apply from `main`; `staging` receives PRs for preview

### Workflow

```
feature/add-backup-share
         │
         │ pull request (human review)
         ▼
      staging ──── drift-check only ────► "Plan: CREATE shares/backups"
         │
         │ merge after approval
         ▼
        main ──────────── apply ─────────► share created on NAS
```

**Drift check against a branch without applying** is useful for showing the plan in a PR comment. From a CI action in your state repository:

```yaml
# .github/workflows/plan.yml
on: [pull_request]
jobs:
  plan:
    runs-on: ubuntu-latest
    steps:
      - name: Trigger plan
        run: |
          curl -X POST ${{ secrets.NAS_URL }}/api/gitops/check \
            -H "X-Session-ID: ${{ secrets.NAS_SESSION }}"
          curl ${{ secrets.NAS_URL }}/api/gitops/plan \
            -H "X-Session-ID: ${{ secrets.NAS_SESSION }}" | jq '.items'
```

---

## HA Environments

In a two-node HA cluster, both nodes run the GitOps daemon but only the primary node holds active ZFS pools and Docker stacks.

### How apply works in HA

Only the **GitOps writer** acts on the repository: the active node, with quorum, not in subordinate mode and not fencing. On any other node:

- `POST /api/gitops/apply` is refused with HTTP 409 and the reason (for example "standby node");
- web UI changes are not written back to Git (a standby has no pools imported, so its live state would be pushed as a deletion);
- the drift detector reports "drift checks run on the GitOps writer" instead of false drift.

Both nodes currently share one PostgreSQL database through Patroni, so the standby needs no separate configuration sync. Trigger apply through the VIP (managed by Keepalived), which always routes to the active node.

Both nodes must also use the **same secrets key** (`services.dplaneos.secrets.keyFile`), or Git credentials stored by one node cannot be used by the other after a failover. See [HIGH-AVAILABILITY.md](HIGH-AVAILABILITY.md#shared-secrets-key).

### Quorum-gated pool operations

When HA is active, pool ownership operations - `CREATE`, `IMPORT`, `RESHAPE`, and `DESTROY` in the plan - are gated on cluster quorum. An isolated node (one that has lost contact with its peer and all witnesses) will not act on these plan items, regardless of what `state.yaml` says. The operations are **deferred**, not failed: they appear in the plan result as `[DEFERRED no-quorum]` and are retried automatically on the next reconcile cycle once quorum is restored.

This is the sole software-level write guard for replicated-topology clusters (Path B). On shared-storage clusters (Path A'), the SCSI-3 PR hardware reservation is a physical backstop, but the quorum gate fires first in software.

**Operations that are NOT quorum-gated:**

| Operation | Gated on quorum? | Reason |
|---|---|---|
| Pool `CREATE` / `IMPORT` / `RESHAPE` / `DESTROY` | Yes | Dual-writer corruption risk |
| Dataset `CREATE` / `DELETE` / `MODIFY` | No | Dataset lives on already-imported pool |
| SMB share `CREATE` / `DELETE` | No | Config-only, no block-level risk |
| NFS export changes | No | Config-only |
| Docker stack changes | No | No ownership question |
| User / group changes | No | DB-only |
| System / network changes | No | No pool ownership question |

If a pool operation is consistently deferred, check cluster quorum status on the HA page before diagnosing the GitOps config.

### Rolling configuration updates

For changes that do not affect storage (adding a user, changing a share comment):

1. Commit and push
2. Trigger apply via the VIP (the shared database makes the change visible on both nodes)

For changes that affect ZFS (new dataset, new pool):

1. Commit and push
2. Trigger apply via the VIP on the primary
3. ZFS replication (if configured) propagates the dataset to the standby on the next replication cycle

For changes that affect the NixOS layer (not state.yaml - actual module changes):

Follow the HA rolling upgrade procedure in [HIGH-AVAILABILITY.md](HIGH-AVAILABILITY.md) instead.

---

## Common Patterns

### Adding a dataset and share together

```yaml
datasets:
  - name: tank/projects
    quota: 500G
    compression: zstd
    atime: "off"
    mountpoint: /mnt/projects

shares:
  - name: projects
    path: /mnt/projects
    read_only: false
    valid_users: "@engineering"
    comment: "Engineering project files"
    guest_ok: false
```

The reconciler creates resources in dependency order: datasets before shares.

### Adjusting a quota

Change the `quota` field and apply. The reconciler calls `zfs set quota=...` on the dataset. No data is moved or at risk.

```yaml
datasets:
  - name: tank/media
    quota: 12T    # was 8T
```

### Adding a user mid-cycle

Add the user to `users:` and add them to any relevant groups in `groups:`. Apply creates the DPlaneOS DB user and the POSIX account. The user can log in to the web UI immediately after apply completes.

```yaml
users:
  - username: carol
    password_hash: "$2b$12$..."
    email: carol@example.com
    role: viewer
    active: true

groups:
  - name: family
    members: [alice, bob, carol]   # added carol
```

### Disabling a user without deleting

Set `active: false`. The account is suspended but the data and history are retained. Use `active: true` to re-enable.

### Running a one-off container

Use a Docker stack with `restart: "no"`. Apply starts the container once. Capture will export it; you can then delete it from `state.yaml` and apply again to remove it.

### Stopped containers are restarted by apply

A stack in `state.yaml` is desired to be running. If a container is stopped (manually via `docker stop` or by the container itself exiting), the diff engine sees `status: stopped → running` and the next apply will restart it.

This is intentional: GitOps owns the desired state, and the desired state for a declared stack is "running." If you want a stack to be stopped, remove it from `state.yaml`. If you need it stopped temporarily without triggering a DELETE plan item, set `ignore_extraneous: true` and stop the container manually - the reconciler will not notice the extraneous stopped stack. Remember to set `ignore_extraneous: false` again once the operation is complete.

### Decommissioning a share

Remove it from `shares:` and apply. The reconciler computes a DELETE plan item. If the share has no open sessions, it is removed immediately. If sessions are active, they are disconnected first.

**The underlying dataset is not touched.** Removing a share entry only removes the SMB share definition. The data remains. To delete the dataset, also remove it from `datasets:`, which generates a `BLOCKED` item requiring explicit approval.

---

## Troubleshooting

### Plan shows unexpected DELETE items

You have resources on the live system that are not in `state.yaml`. Either:

1. Run Capture for those resource types and add them to `state.yaml`, or
2. Set `ignore_extraneous: true` at the top of `state.yaml` to suppress DELETE items for unmanaged resources

### Apply halts with BLOCKED items

A BLOCKED item is a potentially destructive change that requires explicit confirmation. Read the description in the plan - it explains what data is at risk. To approve:

```bash
curl -X POST http://nas/api/gitops/approve \
  -H "X-Session-ID: $SESSION" \
  -H "Content-Type: application/json" \
  -d '{"item_id": "datasets/tank/old-data", "reason": "dataset is empty, confirmed"}'
```

Then run apply again.

### Drift detected but nothing changed in state.yaml

The live system was modified outside of state.yaml (via UI, CLI, or another process). Options:

1. Run apply to bring the system back into alignment with state.yaml
2. Run Capture and update state.yaml to reflect the change you made interactively

### Apply fails with "validation errors"

The error message names the exact field and index. Common causes:

- Disk path is `/dev/sda` instead of `/dev/disk/by-id/...`
- `version` field is missing or not `"1"`
- An enum value is misspelled (e.g., `compress: lz4` instead of `compression: lz4`)
- Unknown field added (state.yaml uses strict parsing - no extra fields allowed)

### Repository clone fails

Check:
1. The deploy key is added to the repository with the correct permissions
2. The key file at `ssh_key_path` is readable by the `dplaneos` user
3. The repository URL uses the SSH form (`git@github.com:...`), not HTTPS
4. The NAS can reach the Git host: `ssh -i /var/lib/dplaneos/gitops/deploy_key -T git@github.com`

### Apply makes unexpected changes

Set `ignore_extraneous: false` only when `state.yaml` is complete and trusted. During initial migration, keep it true to prevent the reconciler from deleting resources you have not yet captured. Review the plan output before every apply.

### Web UI change was not committed to Git

`GET /api/gitops/status` shows the reason in `last_commit.error`:

- "the Git repository has changes that are not applied to this system yet": apply or review the pending commits; the next write-back includes the UI change.
- "this node is not the GitOps writer": the change was made on an HA standby; make changes through the VIP.
- push or fetch errors: check the repository credentials and connectivity.
