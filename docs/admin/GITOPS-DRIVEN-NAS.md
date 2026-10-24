# Git-Driven NAS: Keeping the Configuration in Git

DPlaneOS can keep its whole configuration in a Git repository you control: shares, datasets, users, groups, stacks, replication jobs and system settings, as `state.yaml` files. A change made in the web interface becomes a commit (or a pull request); a change made in the repository comes back to the system, after your approval or automatically, with safety rules that never let a bad commit destroy data. Git is optional: without it, change history, rollback and the `state.yaml` export work as they are.

Everything here is set up on the **Git** page (System › Git › Configuration repository). For the file format (every field, validation rules), see [GITOPS-REFERENCE.md](../reference/GITOPS-REFERENCE.md). The design is [Design 0001](../design/0001-distributed-state-gitops-ha.md), section 5.8, and [ADR-0007](../design/adr/ADR-0007-gitops-defaults.md).

---

## How it works

**One file per scope.** On a cluster the repository holds one file per scope, and exactly one node writes each:

| File | Holds | Written by |
|---|---|---|
| `cluster/state.yaml` | users, groups, directory (LDAP), certificates, ACME, shared settings (time zone, DNS, NTP, firewall, Samba, SSH) | the cluster coordinator (the DPlaneOS member with the lowest cluster node id in the quorate part); between paired nodes without a cluster, the paired node with the lowest node id |
| `groups/<group>/state.yaml` | the pools of a storage group and what lives on them (datasets, shares, NFS and NVMe-oF exports, stacks using them) | the group's current owner |
| `nodes/<node>/state.yaml` | the node's own settings (hostname, interfaces, SMART tasks) and its pools outside any storage group | the node itself |

A standalone node can instead use **one file** (`state.yaml`, or a path you choose), the format earlier versions wrote. A paired node needs one file per scope: otherwise two nodes would write the same file.

**The repository is one more peer.** For every resource the node compares three versions: the one in the commit it last reconciled with, its own current one, and the one in the newest commit.

- Changed only on this node: committed (or proposed in a pull request).
- Changed only in the repository: applied on this node, after your approval or automatically (see below), then passed on to the other nodes like any change.
- Changed on both: a conflict, decided on the **Configuration Sync** page with both versions side by side, like a conflict between two nodes. Git conflicts are never settled by a rule.

**Nothing waits in an outbox.** What a node still has to write is exactly what differs between its configuration and the reconciled commit. After a crash, a restart or a failover the new writer computes the same difference from its own configuration (which holds the same changes), so nothing is lost. The local clone (`/var/lib/dplaneos/git/clone`) is disposable: a broken clone is simply created again.

**Secrets never go to the repository.** A user's password, the LDAP bind password, ACME DNS credentials and certificate keys appear as references (`password_hash: "secret://user/alice/password"`). A node reading a reference keeps the secret it holds. Changing a password is therefore never a change for the repository. Paired nodes exchange secrets among themselves, encrypted (see [HIGH-AVAILABILITY.md](HIGH-AVAILABILITY.md)). A bcrypt hash written into `password_hash` by hand still sets that password.

---

## Setting it up

1. **Create an empty repository** on your Git server (GitHub, Gitea, Forgejo, GitLab or anything reachable over HTTPS or SSH).
2. **Add credentials** (Git › Credentials): a token, with an optional user name (GitHub accepts any; Gitea, Forgejo and GitLab may want the account name), or an SSH private key. Ideally a read-only deploy key to fetch and a separate credential to push.
3. **Configure the repository** (Git › Configuration repository):
   - **Remotes:** one or more URLs, each with its fetch and push credential. The node fetches from all of them and works from the newest history; it pushes to all. If remotes disagree on history, nothing is done until you bring them together.
   - **Branch** (default `main`) and **files** (one per scope, or one file for a standalone node).
   - **What the repository manages:** storage, sharing, apps, identity, protection, system.
   - **How changes made here reach the repository:** committed to the branch, or as a pull request (needs a provider: GitHub, Gitea/Forgejo or GitLab, the API URL, `owner/name` and a token credential).
   - **Changes from the repository**, **signatures** and **push notifications**: below.
4. **Save.** The settings, with the credentials they name, are sent to the paired nodes (encrypted with the pair's key), so whichever node becomes a scope's writer can work. The newest saved version wins on every node.

The first cycle writes the current configuration to the repository. From then on, the page shows per scope: whether it is in step, what is not yet in the repository, what changed in the repository and waits, open pull requests, conflicts and holds.

---

## Day to day

**Changing something in the web interface.** It takes effect immediately. Within seconds the writer of its scope commits it (*"cluster: 1 change(s) from nas1"*, listing each resource, who changed it and when). If the branch moved on meanwhile, the push is refused, the node fetches and tries again; if someone changed the same resource in the repository, it becomes a conflict to decide.

**With pull requests,** the change is committed to `dplaneos/<scope>` (for example `dplaneos/cluster`) and a pull request is opened or updated. It counts once it is merged. If the pull request is closed without merging, the same change is not proposed again; the page offers **Take the repository's version**, which replaces this node's change with what the repository has.

**Changing something in the repository.** Commit and push as usual. The writer of the scope notices on its next check (every 5 minutes by default) or at once with a push notification. With automatic apply off (the default) the change appears under the scope with **Apply from <commit>**; with it on, it is applied. Either way the commit gets a status (`dplaneos: success` or the reason it failed) on the provider, if one is configured.

**Viewing a file** as it is in the newest commit: **View file** on the scope.

---

## Safety rules (the defaults)

The defaults follow Argo CD's: nothing destructive happens unless you asked for it.

| Setting | Default | Meaning |
|---|---|---|
| Apply automatically | off | Changes from the repository wait for approval. On: they are applied when fetched. |
| Also delete what the repository deletes (prune) | off | A resource removed from the repository waits for approval, even with automatic apply. |
| Undo changes made outside the web interface (self-heal) | off | Off: a change made in a shell or by another tool is committed like a web UI change. On: it is replaced by the repository's version. |
| Empty-state guard | always | A commit that would remove every resource of a kind this node has (all users, all shares, …) is held, never applied. |
| Automatic revert | never | A failed apply holds the scope; reverting is your decision. |

The engine's own rules apply to everything from the repository: deleting a dataset that holds data or a share with open connections is never executed automatically; it waits for manual action.

---

## Holds

A scope is put **on hold** when a commit cannot be taken: its apply failed, a commit is not signed by an allowed key, the file cannot be read, or the empty-state guard stopped it. While on hold:

- the scope stays on its last good commit; nothing more from the repository is applied to it;
- an alert is raised (Alerts), and the commit gets a failure status on the provider;
- this node's own changes are still written (not for signature holds: nothing is written on top of a history that failed verification).

The scope's line offers **Resume** (try again, after you fixed the cause, for example by pushing a corrected commit) and **Revert**, which writes the file back as it was in the last good commit, as a new commit (or pull request). History is never rewritten.

---

## Signatures

With **Take only commits signed by an allowed key**, every commit after the last reconciled one must be signed by one of the listed SSH keys (allowed-signers format, or bare public keys) or GPG keys. Otherwise the scope is held. Write access to the repository alone can then not change the configuration.

With **Sign this node's commits**, each node signs its commits with its own SSH key (created on first use; its public key is shown on the Git page). Add every node's key to the allowed keys.

---

## Push notifications

Save with **a new webhook secret**; the secret is shown once. In the repository's webhook settings enter `https://<node>/api/gitops/webhook`, content type JSON, and the secret. GitHub, Gitea and Forgejo sign the body with it; GitLab sends it as a token. A valid notification starts a check at once; anything else is refused. Each node that may write a scope can be a webhook target; the others ignore what they do not write.

---

## Conflicts between the web interface and the repository

Configuration Sync shows them with both versions: keep this node's, take the repository's, or edit a combined one. The result is written to the repository by the next cycle. See [HIGH-AVAILABILITY.md](HIGH-AVAILABILITY.md) for the conflict rules between nodes; between a node and the repository the operator always decides.

---

## High availability

Only the writer of a scope talks to Git for it: the group owner for a group, the coordinator for the cluster. After a failover the new owner continues from its own configuration and the repository; nothing is pushed twice, nothing is lost. A node cut off from the cluster keeps working; its changes reach the repository once its scope's writer has them. Changes from the repository enter through the writer and reach the other nodes through configuration sync.

HA settings (cluster, votes, fencing, storage groups) are deliberately not in the repository: a remote change there could split the cluster.

---

## A Complete state.yaml Example

This is a fully annotated `state.yaml` for a typical home/small-business NAS with two users, a media share, a Docker stack, and NFS for Linux clients.

```yaml
version: "6"

# When false, resources that exist on the live system but are absent
# from state.yaml will appear as DELETE items (and undeclared users,
# groups, shares and NFS exports are removed from the database on apply).
# Set to true during migration if you want to manage only a subset of
# resources via Git: undeclared resources are then left alone.
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
    # Or create the user in the web interface: it is committed with a secret reference.
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

- Secret references (`secret://…`), which DPlaneOS writes itself: the node keeps the secret.
- Password **hashes** (bcrypt, `$2a$`/`$2b$`/`$2y$`, 60 characters) for local users, if you want the repository to set a password. Validation rejects anything else in `password_hash`, which catches plain-text passwords pasted by mistake.
- Usernames, email addresses, roles
- Share paths, permissions, comments
- NFS client CIDRs
- Docker image names and non-sensitive environment variables
- Hostnames, IP addresses, DNS servers
- Firewall rules

### What does not belong in state.yaml

| Secret | Alternative |
|--------|-------------|
| LDAP bind password | Set in Directory settings in the web UI; stored encrypted. In `state.yaml` it appears as a reference; anything else is rejected by validation |
| ACME DNS API tokens | Set in the web UI; they appear as references in `state.yaml` |
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

Change the `quota` field and commit; approve it on the Git page (or let automatic apply take it). The reconciler calls `zfs set quota=...` on the dataset. No data is moved or at risk.

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

Use a Docker stack with `restart: "no"`. Applying it starts the container once; remove it from `state.yaml` afterwards to remove it.

### Stopped containers are restarted by apply

A stack in `state.yaml` is desired to be running. If a container is stopped (manually via `docker stop` or by the container itself exiting), the diff engine sees `status: stopped → running` and the next apply will restart it.

This is intentional: GitOps owns the desired state, and the desired state for a declared stack is "running." If you want a stack to be stopped, remove it from `state.yaml`. If you need it stopped temporarily without triggering a DELETE plan item, set `ignore_extraneous: true` and stop the container manually - the reconciler will not notice the extraneous stopped stack. Remember to set `ignore_extraneous: false` again once the operation is complete.

### Decommissioning a share

Remove it from `shares:` and apply. The reconciler computes a DELETE plan item. If the share has no open sessions, it is removed immediately. If clients are connected, the item is `BLOCKED` until they disconnect or you approve it.

**The underlying dataset is not touched.** Removing a share entry only removes the SMB share definition. The data remains. Removing the dataset from `datasets:` deletes it only if it is empty; a dataset that holds files, snapshots or children stays `BLOCKED` even when approved. GitOps never destroys data: run `zfs destroy -r` by hand once you are sure.

---

## Troubleshooting

### "No remote of the repository can be reached"

The page lists each remote with its error. Check the URL, the credential (token scopes: read and write repository contents; for pull requests also pull requests and commit statuses), and for SSH that the host key was accepted (`/var/lib/dplaneos/git/known_hosts`). The node retries with growing intervals (30 s doubling to an hour) and at once on **Sync now**.

### "The remotes of the repository diverged"

Two remotes have different histories. Bring them to the same commit (push one to the other); the node does not choose.

### "This node is paired: the single-file layout cannot say which node writes what"

Switch to one file per scope. The first cycle writes the scope files; delete the old single file in the repository afterwards.

### A scope is on hold

The reason is on the scope's line. Fix the commit in the repository and **Resume**, or **Revert** to the last good commit.

### "state.yaml cannot hold these exactly as they are; not written"

A resource has a setting the file format cannot express. The node writes nothing rather than something wrong. Please report it with the resource named in the message.

### A change I made in the repository is not applied

Automatic apply is off (the default): approve it on the scope's line. Deletions also need approval unless prune is on. If the resource was also changed on the node, it is a conflict on the Configuration Sync page.

### "Written by another node"

That scope belongs to another node (its own settings, a storage group it owns, or the cluster scope when it is the coordinator). Look on that node.
