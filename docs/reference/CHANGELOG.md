# DPlaneOS Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/), and this project adheres to [Semantic Versioning](https://semver.org/).


## Unreleased

Found by the new handler tests (the API handlers now run against a real
PostgreSQL schema and a command fake in CI). CI also checks every request of the web
interface against the fields its handler reads
(`scripts/check-api-contract.py`).

### Upgrade notes

- **API token scopes are enforced.** A `read` token can only read, a
  `write` token cannot perform admin actions. Before, every token could do
  everything its user could. Re-create automation tokens with the scope
  they need.
- **API tokens expire.** Expiry dates were never applied; tokens past
  their date stop working after the upgrade.
- New NFS exports default to `root_squash` (was `no_root_squash`).
- **Webhooks created in the web interface were stored disabled** and never
  fired (the Test button worked, real alerts were not sent), and their
  custom headers were dropped. New and re-saved webhooks work; existing
  ones must be re-created or enabled (`enabled = 1` in `webhook_configs`).

### Security

- Rsync backups: paths only had to start with /mnt/ ("/mnt/../etc/shadow"
  as source, /etc as destination by way of a symlink). Source and
  destination are resolved against the pool and media mounts before
  every run; the remote shell is fixed.
- Certificates: the Let's Encrypt request used an unchecked certificate
  name as a file path (files could be written anywhere as root), and
  activation wrote an unchecked name into the nginx configuration.
- The CSRF check was skipped when the session lookup failed (database
  slow or down); such requests are refused now.
- Public file links: a link could be created for any file on the system
  (/etc/shadow, the daemon's secrets key) and downloaded without a login.
  Only files on pool and media mounts can be linked, and the path is
  checked again at download.
- Trash: a "/mnt/../etc/..." path moved system files into the trash;
  restoring trusted the item name (path traversal) and an original
  location read from a file on the share, so a share user could have the
  daemon move a file anywhere as root. Paths are resolved and checked, and
  trash folders must be owned by the daemon.
- POSIX and NFSv4 ACLs, cloud sync folders: "/mnt/../etc" passed the prefix
  check and symlinks were followed; the real path is checked now.
- SMB shares and NFS exports accepted any folder (/, /etc); with
  no_root_squash an export of / is root on the machine. Shares must be
  folders on pool or media mounts, given by their real path, and are
  checked again whenever the Samba and exports files are written. Updating
  a share did not check the path at all.
- File manager (runs as root; the built-in "user" role may use it): listing
  and file properties had no path check at all, the other operations
  checked paths lexically only (a symlink created on a share reached the
  whole system), the allowed roots included /home, /tmp and the daemon's
  data directory (secrets key, SSH keys), and setuid/setgid modes were
  accepted. It is now limited to pool and removable-media mounts, every
  path is checked after resolving symlinks, deleting or renaming a symlink
  acts on the link itself, writes never follow a symlink, and
  setuid/setgid modes are refused.
- First-run setup: /api/system/setup-admin replaced the admin password
  even when the installer had set one, so anyone on the network could take
  the account over until setup was finished; /api/system/setup-complete
  needed no login at all (and set the hostname). The admin password is
  never overwritten now, and finishing setup needs the admin session.

- Password resets and account creation ignored the role hierarchy: a
  non-admin user manager could reset an admin's password or create an
  admin. Nobody can raise their own role.
- The 2FA step had no attempt limit (5 failures in 15 minutes now lock it
  per user); backup codes could be used twice concurrently; TOTP codes
  could be replayed within their window.
- Dataset names starting with `-` were accepted and read as options by
  zfs (a "dataset" named `-a` unloaded every encryption key).
- iSCSI targets accepted any device (the system disk could be exported);
  only zvols are accepted. Targets whose authentication setup failed were
  left running; CHAP failures left unauthenticated ACLs. Both roll back.
- Remote replication passed base_snapshot, rate limit, key path, host and
  user to zfs/pv/ssh with only shell characters filtered: a value starting
  with "-" became an option (ssh -oProxyCommand runs a command as root).
  All are validated now, for peers too.
- The login lockout overflowed after about 35 failures and stopped
  throttling.
- Malformed token allowlists are refused instead of treated as
  unrestricted.
- SSH keys: a key label with a line break added arbitrary
  authorized_keys entries; key lines were not really parsed (options,
  extra lines, bad key data); and a user could point ~/.ssh at another
  account's directory to have the daemon (root) write that account's
  authorized_keys. The file is now written without following symlinks.

### Fixed

- Rsync backups: one-off backups ignored the options entered; pools
  mounted outside /mnt were refused; `--exclude=` patterns, shown as an
  example, were refused; a remote backup could hang on a password prompt
  (ssh now runs in batch mode).
- The disk latency check called every healthy disk "slow" (it timed a
  fixed three-second read test) and found no disks in pools imported by
  id. It reports the measured read speed now and tests the disks at the
  same time. Pool I/O statistics lacked write throughput and wait times.
  Both are on the Pools page.
- Directory services: additional Active Directory domains (register,
  join, leave, check) are in the UI; overlapping ID ranges and duplicate
  names are refused.
- Let's Encrypt certificates were saved as <name>.pem while the list,
  activation and deletion only knew <name>.crt: a certificate obtained in
  the UI never appeared and could not be activated. Nothing renewed them
  either: renewal ran only when its API was called, looked for the wrong
  files and ended its job after the first certificate. Certificates are
  now checked twice a day and renewed 30 days before they expire (the web
  server reloads them); "Renew due certificates" runs the check at once.
- Git repositories with auto sync: the last sync time was read back in
  the database's time zone but treated as UTC, so east of UTC a
  repository was not synced again for hours. The same mislabelling made
  API token, user and group timestamps wrong by the zone offset.
- Removed the single-repository git sync endpoints (/api/git-sync/config,
  pull, status, stacks, deploy, export, push) and its table; repositories
  are managed under /api/git-sync/repos.
- NixOS apply: the "confirm or roll back" banner lived only in the open
  page (a reload lost it and the system rolled back unconfirmed), counted
  60 s while the server waited 120 s, and its "Rollback" button only hid
  it. The banner now follows the server's timer.
- Settings: restoring a database backup from the UI always failed (no
  CSRF token was sent). Memory tuning (ZFS ARC limit, swappiness) is in
  the UI; it reported "8 GB" when nothing had been set and carried six
  settings that did nothing. Stored-secrets check, NixOS config backup to
  Git and audit log retention are in the UI; the audit chain no longer
  shows "Valid" when the check could not run.
- Removed /api/smb/vfs, which only returned text to paste into smb.conf
  (shares have Time Machine, shadow copy and recycle bin options).
- Scheduled SMART tests never worked from the UI: the disk name was sent
  without /dev/ and refused, a schedule was only written to the GitOps
  state file (an error without one, and nothing ran until a manual
  apply), the list dropped every row, and schedules were cron
  expressions handed to systemd timers. Schedules are stored, turned into
  timers at once and written back to the GitOps state; they are systemd
  calendar expressions. Disks named by /dev/disk/by-id work, and a test
  can be started right away ("Run test now").
- Hardware page: drive enclosures (bays, the disk in each, fault state)
  with locate LEDs. Pools page: fill level and the emergency reserve
  (reserve 2%, release when the pool is full).
- Trash: everything moved to the trash was copied to one folder on the
  system disk (/mnt/.dplaneos-trash). Each filesystem now has its own
  trash folder, so trashing is a rename and uses no extra space.
- Pools mounted at /<pool> (created or imported without a mountpoint):
  the file manager, ACLs and shares only knew /mnt, /tank, /data and
  /media. Every mounted ZFS filesystem outside the system directories is
  available now; creating folders under /media was refused.
- Files page: Move and Properties added; copy, rename, delete, trash and
  permission changes reported success when the server had refused them.
- Docker: updating a container pulled the new image but only restarted
  the old container, which kept running the old image while the update
  reported success. It now recreates the container with the same
  configuration and restores the previous one if the new one does not
  come up healthy; compose containers are updated with their stack.
- Docker: system prune also removed every stopped container and then the
  volumes they left behind (a stopped app lost its data). It now removes
  dangling images; stopped containers and unused volumes are separate
  opt-ins. Removing an image from the API always failed (the ID was read
  from the wrong place).
- Docker page: images (list, remove), live container stats, pre-flight
  checks, container updates and "docker run" import are now in the UI.
- Files: the Upload button sent the file's path as the target folder, so
  uploads failed (drag and drop worked). Custom app icons get an upload in
  the Docker page; icon files are served with a sandboxing CSP.
- Replicating a dataset without naming a snapshot could pick the newest
  snapshot of a child dataset and replicate that instead.
- Changing an encryption key failed on unlocked datasets and never checked
  the old passphrase.
- Shrinking a zvol (which destroys data) needs `allow_shrink`.
- NFS changes said "applied" when exportfs failed; iSCSI said "created"
  when saving the configuration failed.
- The iSCSI page could not create or update targets (field mismatch) and
  crashed once a zvol existed.
- Concurrent storage operations on one target could both start
  (migration 00024 enforces one pending operation per target).
- A double-submitted 2FA login returned a session id that did not exist.
- The setup wizard could not list disks or create a pool (both need a
  session it never had). It logs in after creating the admin account, or
  asks for the password chosen during installation.
- User and group management in the web interface did not work: the API
  requires the signed-in admin's current password for these changes and
  the Users page never sent it; groups were also sent by name with member
  names, which the API did not accept. The page asks for the password,
  and the API accepts names. Renaming a group failed on its memberships
  (migration 00025).
- Web pages that sent fields their API does not read (found by comparing
  every request of the web interface with its handler):
  - Pools: unlocking/locking encrypted datasets, clearing errors and
    onlining disks failed.
  - iSCSI: ACLs could not be added, removed or listed; the target and ACL
    lists were always empty (targetcli output was misparsed).
  - Removable media: mount, unmount and eject failed.
  - UPS: the page edited NUT connection settings the API does not have and
    saved defaults instead of the chosen shutdown level. It now edits the
    shutdown policy (action, battery level, grace period).
  - GitOps: repository pull/push/deploy/delete, credentials (save, delete,
    test) and the setup wizard failed; saving a repository disabled it.
  - Directory: the LDAP form did not load or save the server and TLS
    settings; group mappings could not be added or removed.
- SMTP alerts could not be saved from the web interface (the page had no
  recipients field and the API requires one), the saved settings did not
  load, and the "enabled" switch did nothing. NFS exports created switched
  off were exported anyway.
- **LDAP group mappings never applied and LDAP users got no permissions:**
  sync wrote only the cosmetic users.role. Mappings now name RBAC roles,
  sync assigns them (and revokes them when a user leaves the group; roles
  an admin granted are kept). Migration 00026 converts existing mappings.
- Webhook body templates inserted values unescaped: a message with a quote
  produced invalid JSON and the alert was rejected. Webhooks cannot
  target link-local addresses (cloud metadata).

## v15.0.0 (2026-10-10) - "Local First"

[Design 0001](../design/0001-distributed-state-gitops-ha.md), phases 0 to 3: every node keeps its own database and keeps working on its own; paired nodes exchange configuration (with secrets) and merge it per resource; high availability is rebuilt on Corosync quorum, a third vote from anything, and storage groups that move between nodes with their shares, apps and a floating address.

### Upgrade notes (breaking)

- **Patroni-based HA is removed.** `services.dplaneos.ha` (Patroni, etcd, HAProxy, keepalived), the etcd witness and the installer's "Install Witness Node" entry are gone. Remove `services.dplaneos.ha.*` from your NixOS configuration if you set it there. HA is set up anew from the web interface: pair the nodes (System › Configuration Sync), form the cluster and add a third vote (System › High Availability), create storage groups. See [HIGH-AVAILABILITY.md](../admin/HIGH-AVAILABILITY.md).
- **Removed API endpoints:** the legacy `/api/ha/*` routes for peers, promote, switchover, fence, maintenance, witness, network witness, SBD, cluster secret, replication, timing, toggle, heartbeat, standby and ALUA standby. Kept: `/api/ha/protection`, `/api/ha/watchdog/configure`, `/api/ha/fencing/configure`, `/api/ha/pdu/configure`, `/api/ha/hardware/detect`, `/api/ha/scsi/*`, `/api/ha/ctdb/*`.
- **Removed daemon flags:** `--ha-local-id`, `--ha-local-addr`, `--ha-cluster-secret`.
- **CTDB** takes its node list from the new `services.dplaneos.ctdb.nodes`.
- **Prometheus:** the HA metrics changed (`dplaneos_ha_*`, `dplaneos_group_*`); update dashboards and use the new `prometheus/ha-alerts.yml`.
- The database schema migrates automatically on first start (migrations up to 00023).

Phase 0: fixes in GitOps and HA that do not depend on the new architecture.

### Fixed

- **GitOps write-back could revert other people's commits.** A web UI change snapshots the live state and commits it. It now fetches first and refuses to commit while the repository holds commits not yet applied to this system (it would have silently reverted them); the UI change stays live and is committed with the next write-back. The commit the live system corresponds to is tracked (`synced_commit`) and advances on push and on fully successful applies.
- **Failed rebases and pushes were only logged.** A failed `git pull --rebase` is aborted and returned instead of being ignored; a missing remote branch is told apart from a network or authentication error; write-back failures appear on the GitOps page (`last_commit`) and as a `gitops.commit_failed` notification.
- **Authenticated Git never worked from the daemon.** `RunInDirWithEnv` replaced the whole environment with the credential variables, so git ran without `PATH` and `HOME`: the token askpass helper could not run `cat` and `GIT_SSH_COMMAND` could not find `ssh`. The credentials now extend the environment.
- **The drift detector never fetched.** It compared against whatever the local clone held. It now fetches and fast-forwards the clone every 5 minutes on every node; local snapshot commits that diverged are reset to the remote (they are regenerated from live state). A second, duplicate drift detector started in `main` is removed.
- **Secrets key handling.** New `services.dplaneos.secrets.keyFile` / `fallbackKeyFile` (`-secrets-key`, `-secrets-key-fallback`): every sealed value is checked at startup, values only the fallback key opens are re-sealed under the active key, and undecryptable values are reported (`GET /api/system/secrets/status`, log). Rotation now preserves unknown fields in the SMTP settings.
- **Plain-text secrets in `state.yaml`.** `ldap.bind_password` is rejected (apply stored it unencrypted in a column read as encrypted, breaking LDAP binds); `password_hash` must be a bcrypt hash.
- **GitOps page status card always showed "Synchronized"**: it read fields the API does not return. It now shows drift, check time, the synced commit, drift-check errors and failed write-backs.

### Added (Design 0001, Phase 1)

- **Change History** (System → Change History, `/api/config/*`): every change to datasets, shares, NFS exports, users, groups, stacks, replication, SMART tasks, LDAP/ACME/certificate settings and system settings is recorded as a revision, right after a web UI change and every 5 minutes for changes made elsewhere. Each entry shows the settings before and after; secrets are stored only as fingerprints. A change can be undone ("Undo this change": back to the previous version of that resource) through the GitOps engine (BLOCKED safety rules apply), and the current configuration can be exported as `state.yaml` without a Git repository. Revisions are stored in the new `config_revisions` table.

### Added (Design 0001, Phase 2)

- **Configuration Sync between nodes** (System → Configuration Sync, `/api/config/sync/*`, `/api/config/peers/*`, `/api/config/conflicts`): nodes with their own databases are paired with a single-use join code after a merge preview, then exchange revisions of datasets, shares, NFS exports, users, groups, replication jobs and shared settings (pull every 30 s and on change; TLS certificate pinned at pairing). Changes are merged per resource with a three-way merge over revision ancestry: changes to different resources merge automatically and are applied through the GitOps engine (BLOCKED rules unchanged); the same resource changed on two nodes becomes a conflict shown with both versions (keep, take the other's, or edit a combined version), never resolved silently. Node states: standalone, connected, isolated (banner on every page with the number of changes waiting) and independent (after **Detach**). Received changes for a pool that is not imported wait for it.
- The change history no longer records datasets of an exported pool as deleted.
- The change history splits the system section into **node settings** (hostname, network interfaces) and **shared settings** (time zone, DNS, NTP, firewall ports, Samba globals, SSH); shared settings are exchanged between paired nodes and can be rolled back. Docker stacks whose volumes are on a pool, and NVMe-oF exports, are recorded with their pool instead of the node (recorded, not yet exchanged: they must run only on the pool owner). After upgrading, the first capture records this regrouping once.
- CI: new job "Config Sync & Merge (two nodes)": two daemons with separate databases, paired, partitioned with iptables, changed on both sides, reconnected; verifies merge, conflict, resolution and detach. Releases are gated on it.

### Added (Design 0001, Phase 3a)

- **Cluster quorum with Corosync and a third vote** (System › High Availability › Cluster quorum, `/api/quorum/*`): form a cluster with a paired node (addresses suggested), see members, votes and quorum. **Add a third vote** without SSH: one pasted command on any Linux machine (`witness-setup.sh` installs and enrolls `corosync-qnetd` with a one-time code; apt, dnf, yum, zypper; opens TCP 5403 in ufw/firewalld), or *Serve as third vote* on another DPlaneOS system. With a cluster configured, its quorum replaces the heartbeat majority in the HA engine (watchdog reset only while quorate); automatic failover is refused with only two votes or outside the quorate partition, and the GUI says why. NixOS module `modules/cluster.nix` (units started only once configured; state in `/var/lib/dplaneos/corosync`; UDP 5405, TCP 5403).
- CI: "Cluster Quorum (3 VMs)": two DPlaneOS nodes and a plain Linux witness enrolled with the one-line command, including a network partition. Releases are gated on it.

### Removed (Design 0001, Phase 3e)

- **Patroni, etcd, HAProxy, keepalived and the legacy HA engine.** High availability is now built only from node-local databases with configuration exchange, Corosync quorum with a third vote, storage groups, watchdog self-fencing and optional power fencing. Removed: `services.dplaneos.ha` (`nixos/ha.nix`), the etcd witness (`patroni-witness.nix`, the witness installer ISO and the installer's "Install Witness Node" entry), the daemon's heartbeat/promotion engine (peers, promotion, switchover, STONITH sequencing, SBD leases, HTTP and network witnesses, cluster secret, legacy replication, maintenance mode, the HA on/off toggle) with their API endpoints and the `--ha-local-id`, `--ha-local-addr` and `--ha-cluster-secret` flags, and the related CI jobs and VM tests. The Patroni setup could not have served the daemon (it never created the `dplaneos` role or database and refused the replica node's connection), so no working installation depends on it. Kept: watchdog and power-fencing settings (now in their own migration), the SCSI-3 probe, hardware detection, CTDB (which now takes its node list from `services.dplaneos.ctdb.nodes`). Tables of the legacy engine stay unused in existing databases. Prometheus HA metrics now describe the cluster, the watchdog and storage groups; the HA alert rules referred to metrics that were never exported and are rewritten.

### Added (Design 0001, Phase 3e)

- **DPlaneOS Witness images.** A ready-to-flash third-vote appliance: a Raspberry Pi 3/4 SD card image and x86_64 images (raw for USB/SSD, qcow2 for VMs), attached to releases. It runs as QDevice or voter; it joins headlessly from `dplaneos-witness.txt` on the boot partition (node address, one-time code, role), from a console wizard, or over SSH (`dplaneos-witness join`). New VM test of the headless join and a reboot of the witness.
- **The third vote from anything.** Besides a QDevice (one command on any Linux machine, or another DPlaneOS system), a **voter**: a Raspberry Pi, mini PC or VM that runs only corosync and joins as a full member with the same one-time code (`witness-setup.sh --voter`); it pins the node's TLS key and pulls its configuration every minute, so added or removed members reach it. Clusters can grow beyond two DPlaneOS nodes (*Add a vote or node*, `POST /api/quorum/nodes`); members and voters can be removed. The HA page compares the four paths; [THIRD-VOTE.md](../admin/THIRD-VOTE.md) documents each with its strengths and limits.
- **Secrets between paired nodes.** User passwords (with SCRAM verifiers and TOTP), the LDAP bind password, ACME DNS credentials and certificate keys now reach the other nodes. Revisions hold plaintext fingerprints; the material is fetched from the node that changed it, encrypted for the pair, checked against the fingerprint and sealed under the receiving node's own key. LDAP, ACME and certificates join the exchanged kinds.
- **New HA documentation:** [HIGH-AVAILABILITY.md](../admin/HIGH-AVAILABILITY.md) and [HA-FAILURE-MODES.md](../admin/HA-FAILURE-MODES.md) rewritten for the new model (topologies with their trade-offs, setup, failover timeline, operations, every failure scenario with what to do).
- **Floating address per storage group.** Held by the owner while it serves the group, announced with gratuitous ARP, removed before a planned move releases the pools; replaces the keepalived VIP.
- **Shared pools are imported by the owner, not at boot.** Pools of shared groups are kept out of the boot-time import (`cachefile=none`); the owner imports them once the other nodes confirm they do not use them, or after a silent node left the cluster and the fencing delay passed. **Import here** on the owner imports on request. Manual imports (pool page, disk hot-plug) of a shared group's pool are refused on nodes that do not own it.

### Fixed (Design 0001, Phase 3e)

- **Watchdog without a hardware driver.** With the HA module gone nothing loaded `softdog`; the daemon now loads it when the watchdog is enabled and no `/dev/watchdog` exists (it was silently off otherwise).
- **Apps restarted on the old owner during a planned move.** The activation loop could start a group's stack again between stopping it and exporting the pools; groups being moved or released are now left alone, and a failed pool check (for example a `zpool list` timeout) no longer drops the floating address.
- **Go 1.26.9 and golang.org/x/net v0.60.0** (GO-2026-6611 to GO-2026-6617, net/http).

### Added (Design 0001, Phase 3d)

- **Apps and NVMe-oF exports follow their storage group.** Docker stacks with volumes on a group's pools and NVMe-oF exports of its zvols run only on the group's owner. The owner publishes their definitions to the other nodes of the group (`group_resources`); a planned move stops them before the pools are released (and is refused if they cannot be stopped), and the new owner starts them after a move, failover or takeover. The NVMe-oF target on each node leaves out the exports of groups it does not own and is reconfigured only when its list changes.

### Fixed (Design 0001, Phase 3d)

- **Docker commands bypassed the command whitelist.** All daemon calls to `docker` ran without argument checks (and logged a security warning). They now go through `docker_compose` (projects under the allowed directories, a fixed set of subcommands and flags) and `docker_cli` (read-only `ps`, `stats`, `inspect`); container names taken from requests are validated.

### Added (Design 0001, Phase 3c: replicated groups)

- **Replicated storage groups**: the owner snapshots its pools recursively on the group's interval and streams `zfs send -R` (incremental from the newest common snapshot) to the other candidates over the paired-node channel (no SSH setup); copies stay read-only; old replication snapshots are pruned except those a lagging node still needs. Receivers accept streams only from the owner at the current epoch and refuse to roll back changes made to their own copy (split); the GUI shows it with "Discard the changes here". Planned move (final replication, direction flips), manual takeover, and optional automatic failover per group. "Replicate now", last replication per node in the GUI. nginx passes replication streams without size limit or buffering; the receive endpoint lifts the daemon's 30 s body read deadline.
- CI: VM test for replicated groups (replicate, move, replicate back, split refused until discarded).

### Added (Design 0001, Phase 3c)

- **Automatic failover per storage group** with the ADR-0009 baseline (third vote + watchdog self-fencing; power fencing optional): when the owner leaves the quorate partition, the first other candidate waits the fencing delay (watchdog timeout + 15 s), fences by IPMI/PDU if configured, and takes over (epoch + 1, import past multihost). Shared-storage groups; replicated groups follow.
- **Watchdog follows Corosync quorum** (Proxmox model): a node that owns a storage group and loses quorum stops resetting its watchdog; nodes without storage, and nodes in a deliberate corosync restart, keep resetting it. The node-level failover engine steps aside when storage groups manage failover.
- **Manual "Take over"** for groups whose owner is unreachable when automatic failover is off (two votes, no fencing); confirmed by the operator, still protected by multihost.
- **Protection summary** (`/api/ha/protection`, HA page): third vote, watchdog (hardware or softdog, with its limitation), multihost per shared pool, SCSI-3 reservations, power fencing, each with what it adds; automatic failover on/off with the reason and the expected delay.
- The cluster reconfiguration window ends as soon as the node is quorate with the configured votes (adding a third vote no longer holds automatic failover back for the full 60 s).
- CI: VM test with three VMs (shared disk, softdog, third vote): owner cut off stops resetting its watchdog, b takes over after the fencing delay, the old owner follows the new epoch after reboot; the two-node test covers manual takeover.

### Added (Design 0001, Phase 3b)

- **Storage groups and epochs** (System › High Availability › Storage groups, `/api/groups`): pools grouped with their candidate nodes and topology (standalone, shared storage, replicated); one owner at a time; the epoch increases with every change of owner. Group definitions are pushed to all members and pulled every 10 s, so a node that missed a change (partition, restart) adopts it on reconnect and releases shared pools it no longer owns. **Planned move** of shared-storage groups (export, epoch + 1, import on the target; pools taken back if the target cannot import). Group-scope configuration revisions carry the epoch; revisions from a former owner are refused by the other nodes (shown as blocked).
- CI: the VM job also runs a shared-disk test: two nodes attached to one image, multihost pool, group creation, move there and back, multihost refusing a second import, a stale node correcting itself by pulling.

### Fixed (found during Phase 3a)

- **HA promotion reported success while steps failed.** `ExecutePromotion` only logged errors: a failed pool import was followed by service restarts and "Promotion sequence complete", and the manual promote job always ended as succeeded. Each step is now reported (pools, datasets made writable, CTDB, SMB, NetBIOS, NFS, Docker, Patroni); a failed import stops the promotion (failed), other failures mark it degraded. The manual promote job fails with the list of problems; the last automatic promotion is shown on the HA page, and the post-promotion stack apply is skipped when the pools were not imported. The Patroni call now checks the HTTP answer.
- **SMB and NetBIOS were never restarted on failover**: the calls used `restart smbd`/`restart nmbd`, which the command whitelist refused (it allowed only `reload-or-restart`), and NixOS names the units `samba-smbd`/`samba-nmbd`. **CTDB was never started on failover**: its command was not in the whitelist. Both fixed; CTDB is skipped (and reported as skipped) when it is not enabled.
- `modules/samba.nix`: the shares-config stub was ordered before `smbd.service`, which does not exist on NixOS, so systemd ignored the ordering; now `samba-smbd.service`.

### Fixed (found during Phase 1)

- **GitOps apply deleted resources that `state.yaml` did not declare, whatever `ignore_extraneous` said.** The database sync before every apply deleted all groups and group memberships, all NFS exports and undeclared shares, and deleted and re-created users declared in Git (losing their row and password hash). With `ignore_extraneous: true` it now only adds and updates declared resources; undeclared ones are removed only with `ignore_extraneous: false`, as documented. Declared users are updated in place.

### Documentation

- GITOPS-DRIVEN-NAS.md: "Auto-Apply on Push" (polling, webhook) was documented but never implemented; marked as planned (Design 0001, Phase 5). Web UI write-back behaviour, HA writer rules and secrets rules documented; example hashes made valid.
- HIGH-AVAILABILITY.md: GitOps writer rules and a shared-secrets-key procedure for new and existing pairs.

## v14.8.0 (2026-10-05) - "Live Boot Release"

Live boot support enables running D-PlaneOS directly from USB without installation. Full daemon and UI run in RAM, existing ZFS pools are auto-discovered and imported, and persistence is optional via USB drive.

### Live Boot Features (NEW)

- **Bootable ISO**: Same release ISO can be used as installer (traditional) or live boot (no installation needed). Dual-purpose build with branching logic at boot time.

- **Ephemeral by default**: System runs entirely from RAM using impermanence module. All state (configs, logs, container data) is lost on shutdown unless persistent USB attached. Zero disk writes to boot USB (read-only squashfs).

- **Auto-import ZFS pools**: On boot, systemd service auto-discovers and imports any ZFS pools found on attached drives. Pools appear in UI immediately without manual intervention. Existing pool data preserved and accessible.

- **Full daemon capability**: Everything works the same as installed version: SMB/NFS/iSCSI sharing, Docker containers, dataset management, snapshots, replication, UI accessible at `http://<ip>/` (nginx, port 80).

- **Optional persistence**: Attach USB drive labeled `dplane-persist` to preserve daemon state across reboots. Without persistence, state resets on shutdown. Useful for trial/rescue scenarios or persistent operation after first boot.

- **VM test suite**: nixos/tests/live-boot.nix validates boot, daemon startup, ZFS auto-import, Docker engine, ephemeral root, and clean shutdown. Runs on every CI build.

### Live Boot CI Fix

The live-boot VM test had failed since v14.7.0 with `dplaned.service` in state "failed":

- **PostgreSQL over the Unix socket**: NixOS PostgreSQL does not listen on TCP unless `enableTCPIP` is set, so the `pg_isready -h localhost` pre-start probe could never succeed, and the default TCP DSN had no password for NixOS's default `pg_hba.conf`. The live config now connects via `host=/run/postgresql`, creates the role and database with `ensureUsers`/`ensureDatabases` and maps `root` to `dplaneos` via peer auth. The pre-start probe follows the DSN. Installed systems keep their existing DSN default.
- **Startup ordering**: `dplaned` is ordered after `postgresql-setup.service` (which creates roles) and waits up to 60s for the database before running migrations instead of exiting.
- **Logging**: daemon output goes to the journal again (`journalctl -u dplaned`), replacing the `kmsg` experiment. The VM test dumps unit status and journals on failure and checks the UI on port 80, not 9000.

### Guided Storage UX (NEW)

- **Automatic pool layout**: Create Pool picks disks by layout, disk size group (optionally treating the size as a minimum), width and vdev count, with a live preview of vdevs, usable capacity and fault tolerance. Warnings for stripes, RAID-Z1 on 8 TB+ disks, RAID-Z wider than 12 and leftover disks. Manual topology remains available.
- **Pool import dialog**: `GET /api/zfs/pool/importable` and `POST /api/zfs/pool/import` import exported or foreign pools by GUID, with optional rename and explicit forced import for pools last used by another host. Refused on an HA standby.
- **Dataset presets**: Generic, SMB share (case-insensitive), Apps/containers and Media. `POST /api/zfs/datasets` accepts `acltype` (posix/off; OpenZFS on Linux has no NFSv4 ACLs) and `casesensitivity`, which is passed to `zfs create -o` through a new whitelisted command.
- **Rollback safety levels**: `POST /api/zfs/snapshots/rollback` takes `mode` = `safe` | `destroy_newer` | `destroy_clones` (no flag / `-r` / `-R`). The dialog lists the snapshots a destructive level would delete. ZFS refusals return 409 with ZFS's message.
- **Per-share SMB options**: Time Machine (with optional size cap), Previous Versions (shadow copies), recycle bin and hosts allow/deny are set per share (migration 00012; existing shares inherit the previous global toggles). Share purposes in the UI: Default, Time Machine, Windows with file history, Public read-only. New optional share keys in `state.yaml`; omitted keys are left unmanaged.

### Fixes

- **Disk pickers**: Create Pool, RAID-Z expand, wipe and cache/log/special/spare dialogs read a `path` field the disk API never returns, so all disks shared one undefined id (ticking one ticked all). They now use `by_id_path`/`dev_path` and list only unused disks.
- **Rollback**: both rollback dialogs always sent `force` (`zfs rollback -r`), silently destroying newer snapshots; the Pools page copy posted the wrong field and always failed; failures were reported as success.
- **Hot-swap auto-import**: ran `zpool import ... -g <guid>`; `zpool import` has no `-g` flag, so automatic imports always failed. The GUID is now positional, via the whitelisted command.
- **Samba on NixOS**: the daemon wrote its own `[global]` (security, workgroup, map to guest) into the shares include, overriding `modules/samba.nix` (including AD `security = ads`). It now writes share stanzas only and re-opens `[global]` at the end of the include.
- **GitOps shares**: GitOps used its own reduced smb.conf writer that dropped browsable, masks and VFS options on every apply; it now shares the renderer with the UI (`internal/smbconf`). `SyncDB` upserts instead of deleting and re-inserting all shares, and writes `read_only`/`guest_ok` as integers. The generated `state.yaml` keeps share comments.
- **Share editing**: the edit form never sent the share id, so every edit failed with "Share ID required".
- **Time Machine discovery**: the Bonjour `_adisk` record advertised a non-existent share "DPlaneOS"; it now lists the actual Time Machine shares.
- **Dataset creation**: ZFS failures (200 `{success:false}`) were shown as success.

### NixOS Runtime Fixes (full-codebase audit)

- **Database on installed systems**: non-HA installs had no PostgreSQL. `services.dplaneos.database.createLocally` (default without HA) runs it with data in `dbPath`, socket DSN and peer auth, and refuses to start an empty cluster next to existing data.
- **Daemon tools on PATH**: zfs, zpool, lsblk, smartctl, exportfs, git, ssh, rsync, samba, avahi and the other ~80 tools the daemon executes were not on the service PATH; storage, replication and GitOps operations failed with "executable not found".
- **SMB shares reach Samba**: `-smb-conf` is now passed, so UI shares land in the file `samba.nix` includes (they went to the read-only `/etc/samba/smb.conf`); the file is rewritten at startup.
- **Schedules run, also after reboots**: snapshot, scrub, rsync and SMART timers are runtime units on NixOS with absolute `ExecStart`, and all platforms re-install them at daemon start (they embed the per-boot token, so they failed after every reboot).
- **Settings persist through rebuilds**: hostname, timezone, NTP servers, ARC limit, swappiness, the web UI TLS certificate and the UPS policy go through the JSON bridge into `dplane-generated.nix`.
- **FTP and S3**: vsftpd (`services.dplaneos.ftp.enable`, default on, with PAM) and MinIO (`services.dplaneos.s3.enable`) run as daemon-managed runtime units with config in `/var/lib/dplaneos`; FTPS gets a self-signed certificate.
- **UPS**: new `services.dplaneos.ups` (driver, port) configures NUT; the UPS page sets low-battery level, final delay and action.
- **Replication key** in `/var/lib/dplaneos/ssh` (ProtectHome hid `/root/.ssh`). **Time Machine** Bonjour record names the real shares.
- **Hardening phases** (resource watcher, health checks, crash-recovery journal, event log, rollback manager) now start with the daemon; their migrations (feature_flags, operation_journal, audit_events) are applied, and the Settings Features tab works.
- **OTA**: the inactive slot is resolved by partition label to exactly one device.
- **UI**: Expand Pool adds a data vdev; the Network page gets per-interface data (it was blank and its VLAN/bond tabs crashed); actions whose failures came back as 200 `{success:false}` now show the error.
- **Web UI layout (regression since v14.6.0)**: `#root` was a CSS grid while the shell positions the sidebar and top bar as fixed elements, so `<main>` landed in the 260px sidebar column and its margin left it 0px wide: every page body rendered blank. Tooltip wrappers no longer take 100% width in flex rows (squeezed the top bar search and the sidebar user row).
- **Datasets created from the UI were not mounted on the host**: dplaned ran in a private mount namespace (ProtectSystem/ProtectHome/PrivateTmp/ReadWritePaths), so its ZFS mounts stayed inside the service until a reboot, invisible to Samba and NFS, and mountpoints outside the allow-list could not be created. The namespace is removed in the NixOS module, the Samba and fencing modules (they appended ReadWritePaths) and the Debian unit, and a module assertion keeps it out; NoNewPrivileges and the capability bounding set remain.
- **No more silently dropped settings** (inputs that were skipped or failed while the API reported success):
  - *Dataset create*: compression, quota, atime, sync, recordsize, xattr, secondarycache, acltype and dedup are validated up front (400, nothing created) and applied atomically with `zfs create -o`.
  - *Bond / VLAN create*: slaves must exist, IPs must be CIDR; every step is checked and a half-built interface is removed on failure. Skipped slaves were previously still saved for boot.
  - *Interface configure*: the Network page posted to the netplan endpoint with the wrong body; it now uses `PUT /api/system/network`, supports DHCP, and the promised auto-revert is real (the networkd file is restored unless confirmed within the countdown; boot state is saved on confirm). The MTU field, which was never applied, is removed. DNS: invalid entries are rejected instead of filtered, and the form sent the wrong field.
  - *Netplan endpoint*: answers 501 where netplan is missing (NixOS) and restores the old file when `netplan apply` fails.
  - *CTDB, NTP, nginx reload, network persistence, networkctl reload*: failures are returned instead of logged.
  - *SMB shares*: a failed smb.conf write or Samba reload is reported (the share is saved, Samba not updated) instead of success.
  - *Time Machine discovery*: `avahi-daemon --reload` always failed ("Operation not permitted": avahi has dropped to its own user and dplaned has no CAP_KILL), so the Bonjour record never refreshed. Avahi is now reloaded through systemd, and a failure is shown as a warning on the share.
  - *Schedules (snapshot, scrub, rsync, SMART)*: hook timers run `bash -c "curl ..."`, but bash was not on dplaned's PATH, so ExecStart stayed relative and systemd could not run it on NixOS; install failures were only logged. bash is on the PATH, an unresolvable program is now an install error, and the schedule pages report it.
  - *Scheduled jobs never ran*: timer hooks reach the daemon over its Unix socket, where the request address is "@", but the hook check only accepted 127.0.0.1/::1, so every snapshot, rsync and SMART timer call was rejected as unauthenticated. Socket callers with the per-boot token are now accepted (token compared in constant time).
  - *Tools missing from script PATHs* (bare names in units without a matching `path`, errors hidden): the ZFS boot gate never found `zpool` and always reported "no pools" (it never waited for pools); the ZED hook was named `dplaneos-notify.sh`, which ZED never runs (now `all-dplaneos-notify.sh`, so pool faults reach the daemon); the OTA wrapper had no `awk`, so the inactive-slot lookup failed and the degraded-pool check always passed; the USB persistence link never saw the stick (`mountpoint`); the SBD lease dataset was never created (`zfs`); the HA failover hook and the persist check lost their log lines (`logger`). All use absolute paths or a unit `path`, and the kernel-matched `config.boot.zfs.package`.
  - *Quotas page*: dataset quotas were posted to the dataset-create endpoint and user/group quotas sent the wrong fields; unparseable sizes became 0 (= no quota).
- **Dataset mountpoints**: an invalid mountpoint (only `/mnt/` and `/media/` are allowed) was dropped silently after the create, which still reported success; it is now rejected with 400 before anything is created. The New Dataset dialog no longer sends one and inherits `<pool mountpoint>/<name>`.
- **First-run setup failed on fresh installs**: the startup seed created `admin` with the default `user` role, so the setup wizard tried to insert a second `admin` and returned "Creation failed". The seed now sets the role, setup updates the seeded account, and the RBAC admin role is granted.
- **Audit log writes**: both write paths bound a Go bool to the INTEGER `success` column, so no audit rows were stored; retention rotation compared the epoch `timestamp` with a date string and never deleted anything.
- **Health checker**: no longer panics without a hardware profile, and no longer deadlocks on its own lock on the first check cycle.
- NFSv4 ACL endpoints answer 501 with an explanation (OpenZFS on Linux has POSIX ACLs only; nfs4-acl-tools is not in nixpkgs).

### Use Cases

- **Trial before install**: Boot live, manage existing pools, run workloads, decide if you want to commit to disk installation
- **Rescue mode**: Non-destructive access to existing ZFS pools if main NAS becomes unavailable  
- **Evaluation**: Full feature set accessible without modifying hardware
- **Temporary workload**: Run D-PlaneOS for a specific task with automatic cleanup on shutdown

### Documentation Changes

- **Added**: docs/admin/LIVE-BOOT-QUICKSTART.md - Getting started guide with troubleshooting
- **Updated**: README.md - Links to live boot option, feature maturity table updated
- **Updated**: CHANGELOG.md - This entry

### CI/CD Changes

- **New jobs**: build-live-iso (x86_64 + aarch64 parallel), test-live-boot (VM integration test)
- **Updated release job**: Downloads, signs, and publishes live ISOs alongside installer ISOs
- **Release artifacts**: Both installer and live ISOs available in same GitHub release

### No Breaking Changes

Live boot is an optional feature. Existing installer ISO unchanged. All installations created before v14.8.0 continue to work exactly as before. OTA updates disabled in live mode (but normal installed systems are unaffected).

---

## v14.7.0 (2026-07-16) - "Vigilant II"

High Availability stabilization: Failover logic and split-brain guards move to beta status (stable, validated, field-ready). Load-testing framework, CTDB clustering, operator runbooks, monitoring, and hardware matrix are experimental additions; core HA is no longer demo-ware. PostgreSQL failover validated under sustained I/O workload, CTDB clustering enables SMB client persistence through failover, comprehensive runbooks document recovery for all 8 failure scenarios, Prometheus alerting and Grafana monitoring provide real-time cluster visibility, SCSI-3 PR compatibility matrix preempts fencing failure surprises.

### High Availability Enhancements

- **PostgreSQL HA Load Testing**: Automated load-test framework (nixos/tests/ha-failover-load-test.nix) validates Patroni failover under sustained workload (pgbench 100 concurrent connections). Injects network partition mid-run and validates failover <60 seconds, cluster converges to single primary (no split-brain), database consistency (row count validated post-failover). EXPERIMENTAL: infrastructure ready, production validation pending.

- **CTDB Clustering for SMB HA** (NEW): Enables Samba clustering so SMB clients survive HA failover without disconnecting or losing byte-range locks. Configuration via Settings → High Availability → CTDB Clustering. Requires: HA enabled, Samba enabled, ZFS dataset for lock database. Automatic failover of public IPs (Keepalived + CTDB coordination). Frontend UI for configuration, real-time cluster status monitoring. Documentation: HA-CTDB-SETUP.md. EXPERIMENTAL: tested in lab, production validation in progress.

- **Prometheus HA Alerting** (NEW): 20+ production-grade alert rules for cluster health monitoring:
  - Failover readiness: quorum lost, peer unreachable, fencing disabled, witness unreachable
  - Replication health: lag high, replication stalled, Patroni no leader, WAL lag high
  - Safety nets: watchdog disabled, watchdog stuck, split-brain detection
  - System health: daemon unresponsive, etcd unreachable, SCSI-3 PR fencing failures
  Each alert includes detection criteria and remediation guidance. Integration: prometheus/ha-alerts.yml.

- **Grafana HA Cluster Dashboard** (NEW): Real-time monitoring dashboard (grafana/dashboards/ha-cluster.json) with:
  - Cluster quorum status, node roles, recent failover timeline
  - Peer heartbeat tracking (missed beats per node)
  - Replication lag graphs with color-coded thresholds
  - Node availability heatmap over time
  - Failover suppression flags (maintenance, hysteresis, subordinate mode)
  - CTDB cluster status and public IP ownership
  - Automated impact measurement (how long did failover take?)

- **HA Operator Runbooks** (NEW): Comprehensive guide (docs/admin/HA-FAILURE-MODES.md, 4000+ lines) covering 8 real failure scenarios:
  1. Node unreachable (network partition detection and recovery)
  2. Failover stalled (witness/maintenance/hysteresis blocking)
  3. Split-brain detected (emergency data reconciliation)
  4. Witness down (quorum witness loss)
  5. Replication lag high (bottleneck identification)
  6. Patroni no leader (etcd quorum restoration)
  7. SCSI-3 fencing failed (PR support validation)
  8. Subordinate mode (stale data catch-up)
  Each scenario includes: root causes, detection symptoms, step-by-step recovery commands, validation checklist, escalation procedures. Post-failover validation checklist included.

- **SCSI-3 PR Hardware Matrix** (NEW): Documented hardware compatibility (docs/reference/HA-HARDWARE-MATRIX.md):
  - Tested working hardware (enterprise SANs, JBODs, SAS controllers with SCSI-3 PR support)
  - Known issues and firmware workarounds
  - NVMe-oF support (enterprise targets yes, consumer targets no)
  - Testing procedures and fallback strategies
  - Community contribution process for undocumented hardware

### HA Status Changes

- **Failover Logic**: Beta (stable, battle-tested)
- **Split-Brain Guards**: Beta (all 6 guards validated by CI)
- **Load-Testing Framework**: Experimental (infrastructure complete, field validation pending)
- **CTDB Clustering**: Experimental (optional module for SMB failover support)
- **Monitoring**: Beta (20+ alerts, real-time dashboard)
- **Operator Runbooks**: Beta (comprehensive recovery procedures for all failure scenarios)

### Database Changes

- **New Tables** (CTDB configuration):
  - `ctdb_config` - CTDB settings (enable, pool, dataset, timeouts, log level)
  - `ctdb_public_addresses` - VIPs managed by CTDB across cluster nodes

### API Changes

- **New Endpoints** (CTDB):
  - `GET /api/ha/ctdb/configure` - Get CTDB configuration
  - `POST /api/ha/ctdb/configure` - Save CTDB configuration (requires system:admin)
  - `GET /api/ha/ctdb/status` - Real-time CTDB cluster status
  - `GET /api/ha/ctdb/databases` - CTDB database replication status

### UI Changes

- **Settings → High Availability → CTDB Clustering** (NEW): Configuration panel for:
  - Enable/disable CTDB
  - Configure data pool and dataset
  - Configure node timeout and recovery timeout
  - Manage public IP addresses (VIPs)
  - Monitor real-time cluster status and IP ownership

### Known Limitations & Future Work

- PostgreSQL HA: Load-testing framework in place; field validation pending before v15.0.0 GA
- CTDB: SMB failover now works; requires broader field validation before v15.0.0 GA
- Hardware Matrix: Community-contributed (submit test results for your hardware)
- Monitoring: Alerts available for configuration; integration with PagerDuty/OpsGenie recommended for production

### Documentation Changes

- Added: docs/admin/HA-FAILURE-MODES.md (operator runbooks, all 8 scenarios)
- Added: docs/admin/HA-CTDB-SETUP.md (CTDB installation and operational guide)
- Added: docs/reference/HA-HARDWARE-MATRIX.md (SCSI-3 PR compatibility)
- Updated: docs/admin/HIGH-AVAILABILITY.md (linked to new failure modes guide)
- Added: prometheus/ha-alerts.yml (20+ alert rules)
- Added: grafana/dashboards/ha-cluster.json (cluster monitoring dashboard)

### No Breaking Changes

All changes are backward-compatible. HA remains off-by-default. Existing v14.6.0 deployments can upgrade without changes. CTDB is optional (enabled by setting services.dplaneos.ctdb.enable = true). New Prometheus alerts and Grafana dashboard are optional integrations.

---

## v14.6.0 (2026-06-20) - "Vigilant"

Production-grade resilience system enabling graceful degradation, automatic recovery, and zero-data-loss operations across any hardware configuration. System now monitors itself, recovers from crashes, adapts to hardware changes, rejects operations when resources are critical, and maintains immutable audit trail of all changes. No breaking API changes. No breaking configuration changes.

### Infrastructure & Resilience

- **Hardware-Agnostic Adaptation** (Phase 2): System automatically detects available hardware (BMC, iLO, Redfish, IPMI, NVMe controllers, SES enclosures, network bonding) at startup and disables features requiring unsupported hardware. Features can be manually managed via Settings → Features tab. No configuration changes required. Enables single image deployment across heterogeneous hardware.

- **Resource Exhaustion Gates** (Phase 1): Daemon monitors disk space, memory, and file descriptor utilization with configurable thresholds (DEGRADED at 85% disk, 80% memory, 80% FD; CRITICAL at 95% disk, 90% memory, 95% FD). When critical thresholds reached, new operations are rejected with HTTP 507 (Insufficient Storage) or 429 (Too Many Requests) to prevent catastrophic failures. Running operations continue uninterrupted. Operators notified via health status.

- **Crash Recovery & State Persistence** (Phase 3.1): All long-running operations (pool creation, dataset migration, replication, snapshots) persist state to operation_journal before execution. If daemon crashes or loses power mid-operation, startup recovery automatically resumes from last checkpoint. Zero data loss. No manual intervention required. Enables safe unattended operation.

- **Health Check Aggregation** (Phase 3.3): System health continuously monitored and visible via GET /api/health endpoint with per-subsystem status (ZFS, Docker, Postgres, network, resources, external services, BMC, SES enclosures). Hardware-aware: BMC and enclosure checks only run if hardware detected. Kubernetes-compatible liveness and readiness probes included. Enables integration with monitoring stacks.

- **Immutable Audit Trail** (Phase 4): All significant events logged with HMAC chain integrity: operations (start, complete, fail), feature state changes, resource warnings, hardware detection, circuit breaker transitions, rollbacks. Audit entries form cryptographic HMAC chain where each entry links to previous via hash. Chain integrity verifiable, tampering detectable. Compliance-ready for regulated environments.

- **Automatic Rollback & Recovery** (Phase 5): On operation failure, system automatically rolls back configuration via git revert, disables incompatible features, and logs recovery event. Resource exhaustion or circuit breaker failures trigger graceful degradation instead of crashes. Incomplete operations resume at startup. Operators notified via audit trail and health status. Enables safe automation and unattended operation.

### User Interface

- **Settings → Features Tab** (NEW): Users can enable/disable optional features based on system needs and hardware capabilities. Each feature displays: name, description, current state (enabled/disabled/beta/deprecated), and hardware requirements if applicable. Hardware-required features show grayed out with explanatory tooltip if not present. Feature state changes logged to audit trail.

### API Changes

- **New Endpoints**:
  - `GET /api/system/features` - List all features with state and requirements
  - `POST /api/system/features/:id/enable` - Enable a feature (requires system:write permission)
  - `POST /api/system/features/:id/disable` - Disable a feature (requires system:write permission)

- **Enhanced Endpoints**:
  - `GET /api/health` - Now includes per-subsystem status with hardware-aware checks
  - `GET /api/health?subsystem=X` - Query specific subsystem health
  - `GET /api/health/live` - Kubernetes liveness probe (200 if alive, 503 if critical subsystems down)
  - `GET /api/health/ready` - Kubernetes readiness probe (200 if all subsystems OK, 503 otherwise)

### Database Changes

- **New Tables** (3 migrations, no breaking changes):
  - `feature_flags` - Feature state persistence with enable/disable timestamps
  - `operation_journal` - Operation state for crash recovery with resumption support
  - `audit_events` - Immutable event log with HMAC chain integrity

### No Breaking Changes

All changes are backward-compatible. No API breaking changes (new endpoints only), no configuration file changes required, no database schema breaking changes (new tables only), no binary format changes. Existing operations continue to work exactly as before. v14.5.0 deployments can upgrade to v14.6.0 without operational changes.

---

## v14.5.0 (2026-06-15) - "Adaptive"

Responsive design overhaul: DPlaneOS interface now adapts seamlessly across all screen sizes, from 320px mobile devices to 4K displays. Desktop-first design maintained with full feature parity across all breakpoints. No breaking API changes. No breaking configuration changes.

### User Interface

- **Responsive Design Foundation**: Interface adapts gracefully across full spectrum of screen sizes (320px mobile to 3840px 4K) with three primary breakpoints (768px mobile, 1024px tablet, 1440px desktop) plus ultra-wide adjustments.
  - **Mobile** (<768px): Hamburger menu overlay, stacked form columns, icon-only buttons, horizontal table scroll
  - **Tablet** (768px-1024px): Compressed spacing, toggleable sidebar for more content space
  - **Desktop** (1024px+): Full sidebar visible, all controls displayed, optimal reading width (1440px max-width)
  - **Ultra-wide** (2560px+): Increased padding and spacing for visual balance on QHD/4K monitors

- **Mobile Navigation**: Hamburger menu button appears at <768px viewport width with full-screen overlay pattern. Click backdrop or navigate to dismiss. Menu automatically closes when resizing to desktop or changing routes. Semi-transparent dark overlay (0.4 opacity) provides affordance for dismissal.

- **Touch Target Compliance**: All interactive elements maintain 44px minimum height/width on any screen size per WCAG AA accessibility guidelines. Buttons, form inputs, tab controls, and navigation items scale appropriately while staying within touch-target requirements.

- **TopBar Responsive Scaling**: Header adapts padding (16px mobile, 32px desktop), search button collapses to icon-only on mobile (<768px) to preserve space while maintaining Ctrl+K keyboard shortcut functionality. Hamburger menu button positioned for easy reach on mobile devices.

- **Form Layout Adaptation**: Single-column form layouts on mobile (<768px) automatically stack for readability. Form field padding scales from 10px desktop to 12px mobile for easier touch interaction. Select dropdowns and text inputs expand to 100% width on mobile with proper spacing.

- **Data Table Responsiveness**: Tables use horizontal scroll on mobile (<768px) instead of forcing columns to squeeze. Sticky headers ensure column context remains visible during scroll. Padding and font sizing scale appropriately for readability at all viewport widths.

- **Modal Responsiveness**: Modal dialogs adapt to viewport constraints. On mobile (<768px), modals take full screen with rounded corners removed and slide-up animation from bottom. Desktop modals maintain optimal 420px-680px width with centered positioning. Footer buttons stack vertically on mobile for adequate touch spacing.

- **Typography Scaling**: Page titles and headings scale from desktop sizes down to mobile-optimized sizes. All text maintains readability and visual hierarchy across breakpoints without requiring manual resizing.

- **Reduced Motion Support**: Respects `prefers-reduced-motion: reduce` media query across all animations and transitions. Users who prefer reduced motion experience instant state changes without animations while fully-featured interface remains available.

### Technical Changes

- **CSS Media Queries**: Comprehensive responsive breakpoints in `src/index.css` with desktop-first approach. Safeguard rules prevent horizontal overflow at any screen size. Flexible flex children with `min-width: 0` allow proper shrinking. Word-break rules prevent text overflow in constrained spaces.

- **Responsive Component Props**: Sidebar and TopBar accept `isMobile` and `mobileMenuOpen` props for state-driven responsive behavior. AppShell manages mobile state with window resize listener and route-change effects for seamless adaptation.

- **Body Class Toggle**: Mobile menu state drives CSS-based sidebar styling via `mobile-menu-open` class on document.body. Separates state management from inline styles for cleaner responsive patterns.

- **Ultra-wide Adjustments**: Main content area padding increases from `32px 40px` desktop to `40px 80px` QHD (2560px+) and `48px 120px` 4K (3840px+) for appropriate spacing on ultra-wide displays.

### Accessibility

- **WCAG AA Compliance**: All responsive changes maintain WCAG AA accessibility standards. 44px touch targets on all interactive elements. Focus management preserved across all breakpoints. Keyboard navigation fully functional on mobile with proper focus-visible outlines.

- **Aria Labels**: Hamburger menu button includes `aria-label="Toggle navigation menu"` and `aria-expanded` attribute reflecting current state. Semantic HTML maintained throughout responsive adaptations.

### Fixed

- **Sidebar Responsiveness**: Previously occupying 71% of mobile viewport (260px on 375px screen), sidebar now uses overlay pattern on mobile devices, preventing content from becoming unusable. Desktop users unaffected; sidebar remains fixed and always visible.

## v14.4.0 (2026-06-13) - "Complete Roadmap"

Upgrade from: v14.3.0 - Schema migration required (migration 00011 adds `enterprise_license` and `enterprise_audit_usage` tables for licensing and audit tracking; applied automatically at startup). No breaking API changes. No breaking configuration changes.

This release completes the full DPlaneOS v14.4.0 roadmap with all eight planned priorities shipped: enterprise licensing with Ed25519 verification, multi-forest Active Directory with automatic Samba security mode switching, comprehensive ZED event integration with 2-second progress polling, SES enclosure support (LED locate + full element status via direct SCSI ioctls), transactional storage operations, libzfs cgo bindings with thread-local handles, NFSv4 ACL layer with RFC 5661 translation, GitOps/CRUD race protection via RWMutex, and dplane-fenced SCSI-3 persistent reservations. The community edition remains free and requires no license. Enterprise customers receive signed license keys that activate the optional Compliance Engine sidecar.

### Storage & ZFS Operations

- **ZED Event Integration (`daemon/internal/zfs/zed_listener.go`, 309 lines)**: Kernel ZED events delivered via Unix socket at `/run/dplaneos/dplaneos.sock` with support for 14+ event types (scrub/resilver/TRIM lifecycle, pool state changes, VDEV health, error conditions). Real-time WebSocket broadcasts to UI with 2-second progress polling for long operations and 30-second fallback polling for missed events. Integrated alert routing to SMTP/Webhook/Telegram with severity levels.

- **libzfs cgo Bindings (`daemon/internal/libzfs/zfs_cgo.go`, 615 lines)**: Direct C library bindings for all core ZFS operations (pool create/import/export/destroy, dataset create/set/delete, VDEV attach/detach/online/offline/remove/clear). Thread-local handle pattern ensures safe concurrent access from Go. Automatic CLI fallback if cgo unavailable.

- **Storage Operations Transactional State** (`migrations/00003_storage_operations.sql`): Database-level guarantees prevent CRUD operations from racing with GitOps sync. Pre-flight validation, atomic state transitions (pending → committed → success/failed), per-operation error tracking.

- **Hot-Swap Disk Detection**: Automatically detects new disks, imports available pools, suggests disk replacement for faulted vdevs. Integrated with hardware monitoring and WebSocket event streaming.

### Networking & Directory Services

- **Multi-Forest Active Directory Support**: Register and join multiple AD domains simultaneously with per-domain UID/GID mapping. Each forest can use different IDMAP backend (rid, ad, autorid, tdb) with independent ranges. Automatic Samba security mode switching on join/leave. Kerberos ticket renewal every 15 minutes with machine account keytab fallback.

- **Declarative AD Configuration** (`nixos/modules/samba.nix`, `dplane-state.json`): Domain settings, IDMAP ranges, and Samba security mode flow from database → JSON bridge → NixOS configuration → smb.conf at rebuild time. No imperative config files.

- **Winbind NSS Resolution**: Automatic daemon activation when any domain is joined. Transparent user/group resolution via standard NSS interfaces. Configurable per-domain UID/GID ranges prevent collisions across forests.

### Hardware & Enclosure Support

- **SES Enclosure LED Control**: List all SES enclosures from sysfs. Control identify (locate) LED on individual drive slots. Full path traversal protection on sysfs writes. GET/PUT API endpoints for automation and manual locate workflows.

- **SES Enclosure Element Status** (`daemon/internal/hardware/ses_ioctl.go`, 356 lines): Query enclosure element status via direct SCSI ioctls (SG_IO). Parse configuration, status, and descriptor pages. Support 32+ element types (device slots, power supplies, cooling, temperature sensors, controllers, etc). Type-specific detail extraction (flags for slots, Celsius for temperature sensors). Zero external CLI tool dependencies; more reliable and faster than subprocess approach.

### Data Integrity & Concurrency

- **GitOps/CRUD Race Protection** (`daemon/internal/gitops/lock.go`): RWMutex-based locking prevents concurrent API mutations during GitOps syncs. Read locks for parallel CRUD, exclusive lock for Git operations. No split-brain risk; all configuration changes are atomic.

- **NFSv4 ACL Layer** (`daemon/internal/acl/acl.go`, 163 lines): Full RFC 5661 POSIX mode<→NFSv4 ACE translation. Support for ALLOW/DENY/AUDIT/ALARM ACE types with all standard permission masks. API endpoints for getting/setting dataset ACLs with atomic application guarantees.

- **HA Failover with SCSI-3** (`daemon/cmd/dplane-fenced/main.go`, 386 lines): Systemd-isolated binary manages persistent reservations on shared SAS enclosures. Registers disks on startup, releases on shutdown. Prevents split-brain by blocking peer access during failover. Hardware-gated to shared SAS environments.

### Database & Migrations

- **Goose v3 Migration Framework**: 11 numbered migrations (00001-00011) with automatic version tracking. Applied migrations are automatically skipped on subsequent startups. No manual intervention required.

### Fixed

- **CRITICAL - Multi-forest IDMAP missing Samba security mode switch (`daemon/internal/handlers/ldap.go`, commit b1a2abf)**: When joining an Active Directory domain, the handler synced IDMAP configuration but did not switch Samba from "user" mode to "ads" mode. Result: winbindd daemon would not activate on the next nixos-rebuild, preventing IDMAP resolution. Fixed: `JoinDomain` now calls `SetSambaGlobals(SecurityMode: "ads", Realm: realm)` after successful join; `LeaveDomain` reverts to `SecurityMode: "user"` if the last joined domain is unjoined. This completes the end-to-end chain: domain join → IDMAP sync → security mode switch → NixOS configuration update → winbind activation on rebuild.

### Added

- **Enterprise Licensing System (`daemon/internal/handlers/license_handler.go`, migration 00011)**:
  - `POST /api/system/license/activate`: Accepts Ed25519-signed license key (format: `base64(signature).base64(payload)`)
  - `GET /api/system/license/status`: Returns license status (active/expired), customer name, audit limits, CE installation/running status
  - Offline signature verification using embedded Ed25519 public key (no external API calls required)
  - Hourly license expiration checker: automatically stops CE daemon and disables routes on expiration
  - CE daemon lifecycle management: downloads from private repo, installs to `/opt/dplane-compliance/`, starts on port 9001
  - Environment variable-based credential passing: CE access token via `CE_API_TOKEN` (not command-line arguments)

- **License Management CLI Tools**:
  - `dplane-gen-keypair`: Generates Ed25519 keypair for license signing (stores keys with 0600 permissions)
  - `dplane-license-gen`: Creates signed customer license keys with configurable customer name, audit limits, expiration date, CE repo URL, version, and GitHub PAT

- **Compliance Engine Integration**:
  - `CompliancePage` (`app-react/src/pages/CompliancePage.tsx`): Embeds CE sidecar as iframe on port 9001
  - License-gated sidebar: "Compliance" menu item only appears when licensed (real-time status polling)
  - `LicenseTab` in `SettingsPage`: License input form, status display, three states (not activated, active with customer info, expired with renewal option)
  - License status query with 30s `staleTime`: filters enterprise features based on license validity

- **Database**:
  - `enterprise_license` table: Stores customer name, audits limit/consumed, expiration date, CE repo URL/version, activation timestamp
  - `enterprise_audit_usage` table: Audit trail of report generations for compliance tracking

- **Documentation**:
  - `ENTERPRISE_LICENSE_SETUP.md`: Complete integration workflow (keypair generation, license creation, customer activation, expiration handling)
  - Feature maturity table update: Enterprise Licensing marked as Stable

### Security

- **Offline Ed25519 Verification**: No external API calls for license validation; works completely offline
- **Public Key Embedding**: Verification key embedded in daemon binary via environment variable `DPLANE_LICENSE_PUBLIC_KEY`
- **Credential Isolation**: CE access token from license payload, passed via environment variable (not process arguments visible in `ps`)
- **File Permissions**: License files 0600 (owner-only read/write), keypair files 0600
- **Zero Hardcoding**: All URLs, credentials, and customer information from signed license payload
- **Immutable License Format**: `base64(signature).base64(json_payload)` prevents tampering while maintaining human-readability for debugging

### Technical Details

- **License Payload**: Customer name, audit limits (1-N or -1 for unlimited), expiration date (RFC3339 or "never"), CE repo URL, CE version, GitHub PAT, issued timestamp
- **Signature Algorithm**: Ed25519 (FIPS 186-5 compliant, widely supported)
- **Verification**: Atomic signature + payload validation; no separate checks that could be exploited
- **Daemon Integration**: Hourly background goroutine checks expiration; stops daemon gracefully, disables routes on expiration
- **UI Updates**: License status polled every 30 seconds; sidebar menu updates reflect current license state instantly

### Testing

✓ Go tests pass with race detector  
✓ TypeScript: tsc --noEmit = 0 errors  
✓ ESLint: npm run lint = clean  
✓ App bundle: npm run build successful, committed  
✓ CLI tools: dplane-gen-keypair and dplane-license-gen compile and run end-to-end  
✓ Security: No hardcoded URLs, credentials, or customer information  

---

## v14.3.0 (2026-06-08) - "Quorum"

Upgrade from: v14.2.0 - No schema migration required. No breaking API changes. No breaking configuration changes.

This release closes the NixOS module gap in the Path A' two-node setup, ships a persistent job taskbar with xterm log streaming, a rewritten command palette, stable-layout polling across four pages, and inline list filters. A codebase-wide lint audit eliminated all ESLint warnings and the remaining `any` types across the frontend. Three correctness fixes from a post-release audit of the v14.2.0 HA watchdog are also included.

### Added

- **Job taskbar (`app-react/src/components/ui/JobTaskbar.tsx`, `AppShell.tsx`)**: Persistent 40px strip at the bottom of the shell that becomes visible when a background job is active. Auto-expands when a new job starts. The expanded panel is drag-resizable (min 150px, max 80 % of viewport height) and streams live xterm output via WebSocket. Expanded height is persisted to `localStorage`. Dismissing the taskbar hides it without cancelling the running job. The main content area gains `paddingBottom: 40px` while the taskbar is active so nothing is obscured.

- **`colocatedWitness` NixOS option (`nixos/ha.nix`)**: When `colocatedWitness = true`, the ha.nix module starts a second etcd process on this node (port 2381 client / 2382 raft peer) alongside the data-path etcd (port 2379). The `initialCluster` for both etcd processes and the Patroni `etcd3.hosts` list are wired automatically. No `witnessAddress` required; no separate witness machine needed. Ports 2381 and 2382 are added to the firewall automatically. This makes the "no third node required" statement in the Path A' docs accurate: the previous ha.nix generated all three etcd members on port 2380, so pointing `witnessAddress` at Node A's IP would create an address collision.

- **Command palette rewrite (`app-react/src/components/ui/GlobalSearch.tsx`)**: Cmd/Ctrl+K now shows an empty state with recent pages (last 5 navigations, stored in `localStorage`) and quick-action items before any query is typed. Active queries produce scored results grouped by category with section headers. Scoring ranks: exact match > word-prefix > acronym > substring > subsequence. Search sources: nav items (instant), pools/datasets/containers/shares (API), HA cluster nodes (React Query cache, zero extra requests). Updated ARIA labels and keyboard help description to reflect the richer behaviour.

- **`useFrozenLayout` hook (`app-react/src/hooks/useFrozenLayout.ts`)**: Separates structural layout from live metrics for polling pages. Maintains a `snapshot` (which items exist and in what order - updated only when items are added or removed) and a `liveById` map (always current). Renders iterate over `snapshot` for stable key/position assignments and read values from `liveById`, eliminating the layout shifts and scroll-position resets that occurred when polling returned updated data with the same item set. Applied to pools on `PoolsPage` and `DashboardPage`, and to HA cluster nodes on `HAPage` with a `nodeRefreshKey` that forces a structural update after add/remove/promote mutations.

- **`listFilter` utility (`app-react/src/lib/listFilter.ts`)**: Opacity-based inline filtering. Items that do not match the query are rendered at 0.15 opacity and made non-interactive rather than removed from the DOM. This keeps items in their original positions so operators can see what they searched past. Exports `itemOpacity`, `itemMatches`, and `matchCount`.

- **Inline container filter (`app-react/src/pages/DockerPage.tsx`)**: A search input appears above the container/stack list when there are more than three containers. Filters by container name or image using `itemOpacity`; matching items stay full-opacity and interactive, non-matching items fade to 0.15.

- **Inline share filter (`app-react/src/pages/SharesPage.tsx`)**: Same pattern as the container filter - appears when more than three shares are present, filters by name or path.

- **Doc-freshness gate (`scripts/release-check.sh`, Check 7)**: Hard error if HA code (`daemon/internal/ha/`, `nixos/ha.nix`) changed since the previous tag but `docs/admin/HIGH-AVAILABILITY.md` did not. Hard error if GitOps code (`daemon/internal/gitops/`) changed but `docs/admin/GITOPS-DRIVEN-NAS.md` did not (upgraded from warning). Hard error if no file under `docs/` changed at all since the previous tag.

- **Quorum-gated pool operations documented (`docs/admin/GITOPS-DRIVEN-NAS.md`)**: New "Quorum-gated pool operations" section under "HA Environments" explains that pool `CREATE`, `IMPORT`, `RESHAPE`, and `DESTROY` plan items are deferred (not failed) when HA is active and the node has no quorum. Includes a table of which operations are and are not quorum-gated.

### Fixed

- **CRITICAL - `StateUnknown` peers counted as quorum-reachable (`daemon/internal/ha/cluster.go`)**: The quorum calculation used `n.State != StateUnreachable` to count reachable peers. A peer that has never responded starts in `StateUnknown`, not `StateHealthy`, so it was being counted as reachable before the first heartbeat was received. The hardware watchdog was therefore petting before any peer was verified alive - defeating the startup safety invariant. Fixed to `n.State == StateHealthy`. Only peers with a confirmed live heartbeat count toward quorum.

- **MEDIUM - Watchdog pet interval validation too permissive (`daemon/internal/handlers/ha_handler.go`)**: `POST /api/ha/watchdog/configure` accepted any `pet_interval_sec < timeout_secs`. The correct invariant is `pet_interval_sec * 2 <= timeout_secs`: the node needs at least two chances to pet before the timeout fires, otherwise a single transient missed write can trigger an unexpected reset. Existing valid configurations are unaffected.

- **LOW - SCSI-3 PR cleanup unregister failure silently discarded (`daemon/internal/scsipr/scsipr_linux.go`)**: The PROUT REGISTER cleanup step at the end of `SupportsReservations()` was ignoring errors with `_, _ = sgIOIoctl(...)`. A cleanup failure means the test key (`0xdeadbeefcafe0001`) may remain registered on the device. The failure is now logged as a warning with the device path and error text.

### Changed

- **`witnessAddress` is now optional in ha.nix**: Changed from `lib.types.str` (required) to `lib.types.nullOr lib.types.str` (default null). Set it when using a separate witness machine (Path B). Leave it unset when `colocatedWitness = true` (Path A'). An assertion fires at `nixos-rebuild` time if neither is provided.

- **`PoolsPage` and `DashboardPage` use frozen layout for pool lists**: Pool cards no longer jump or re-order on the 60-second poll tick; health and capacity values update in place. A structural update fires only when pools are added or removed.

- **`HAPage` node list uses frozen layout**: Node cards no longer re-order on the 15-second heartbeat poll. `nodeRefreshKey` increments after add-peer, remove-peer, and promote mutations to force a structural update immediately after the API call.

- **`HIGH-AVAILABILITY.md` Path A' install example** now uses `colocatedWitness = true` instead of the manual `witnessAddress`/`etcdEndpoints` port-2381 entries.

- **Frontend lint audit - zero warnings (`app-react/src/`)**: All 105 ESLint warnings and the one blocking error present since v14.2.0 are eliminated. Fixes span 25 files:
  - `any` → proper types across `api.ts`, `Toast.tsx`, `AppShell.tsx`, `PendingChangesSidebar.tsx`, `GlobalSearch.tsx`, `plugins.ts`, `stores/ws.ts`, `stores/notifications.ts`, `FirewallPage.tsx`, `GitOpsPage.tsx`, `AlertsPage.tsx`, `CertificatesPage.tsx`, `DatasetsPage.tsx`, `DockerPage.tsx`, `FTPPage.tsx`, `HardwarePage.tsx`, `NetworkPage.tsx`, `PoolsPage.tsx`, `ReplicationPage.tsx`, `SettingsPage.tsx`
  - `react-hooks/set-state-in-effect` resolved across `Sidebar.tsx`, `GlobalSearch.tsx`, `AlertsPage.tsx`, `DockerPage.tsx`, `FTPPage.tsx`, `HAPage.tsx`, `HardwarePage.tsx`, `LoginPage.tsx`, `NetworkPage.tsx`, `ReplicationPage.tsx`, `S3Page.tsx`, `SettingsPage.tsx` by replacing seeding effects with dirty-tracking or event-driven state patterns
  - `react-hooks/exhaustive-deps` resolved in `DatasetsPage.tsx`, `PoolsPage.tsx`, `HardwarePage.tsx` by wrapping logical expressions in their own `useMemo`
  - `react-hooks/purity` resolved in `GitOpsPage.tsx` by replacing `useRef(Math.random())` with `useState(() => ...)` lazy initialiser
  - `react-hooks/refs` resolved in `DockerPage.tsx` by extracting the SSE log area from a render-time JSX variable into a proper `SSETerminalArea` component that owns its own scroll ref

## v14.2.0 (2026-06-07) - "Arbiter"

Upgrade from: v14.1.0 - Schema migration required (migration adds `ha_watchdog_config` table for hardware watchdog self-fence; applied automatically at startup). No breaking API changes. No breaking configuration changes.

This release closes the hardware-agnostic HA gap: homelab hardware without enterprise SAS or IPMI BMC now has a complete, safe HA path. The disk controller as write arbiter (SCSI-3 PR) and the hardware watchdog as self-fence floor are first-class citizens in both the backend and the UI.

### Added

- **Hardware watchdog self-fence (`daemon/internal/ha/watchdog.go`, `watchdog_linux.go`, `watchdog_other.go`, migration adds `ha_watchdog_config`)**: The daemon opens `/dev/watchdog` and pets it on every heartbeat tick while the cluster has quorum. When quorum is lost and all configured witnesses are unreachable, petting stops and the kernel hard-resets the node after the timeout. This removes the BMC/PDU network-reachability assumption from fencing: the loser self-fences via local hardware, the survivor waits the guaranteed fence interval and promotes - no outbound network reach to the peer required. Isolation detection is correct for two-node clusters: self-fence only fires when at least one witness is configured and all witnesses are unreachable. Without a witness, or when a witness is reachable, the watchdog keeps petting (IPMI/PDU remain the fencing path). On NixOS, `ha.watchdog.enable = true` loads `softdog` as a fallback driver on hardware without a native watchdog device.
  - `GET /api/ha/watchdog/configure` returns current config (defaults if unconfigured)
  - `POST /api/ha/watchdog/configure` (AAL2) saves config and applies enable/disable to the running daemon immediately
  - `nixos/ha.nix`: `ha.watchdog.{enable,device,timeoutSecs}` NixOS options; `systemd.watchdog.runtimeTime` set automatically when enabled

- **Quorum-aware GitOps reconciler (`daemon/internal/gitops/apply.go`, `daemon/internal/handlers/gitops_handler.go`)**: `ApplyContext.OwnershipGuard func() bool` gates pool create, reshape, and destroy operations when HA is active and this node has no quorum. On shared-SAS topologies the SCSI-3 PR is the hardware backstop; on replicated topologies this software gate is the only protection between an isolated node and acting on Git-desired state that says "create/import this pool". Ownership operations are deferred (not failed) so the next reconcile cycle retries once quorum is restored. Wired to `clusterMgr.Status().Quorum` in main.go when `ha.enable = true`.

- **SCSI-3 PR write probe - `SupportsReservations()` (`daemon/internal/scsipr/scsipr_linux.go`, `scsipr_stub.go`)**: A full PROUT round-trip check: register a test key, read back to verify it appears, unregister. This is the only reliable test. PRIN READ KEYS (the previous approach) is a read-only probe that passes on drives which reject PROUT REGISTER - the false positive that arms the cluster with silent broken fencing. The test key (`0xdeadbeefcafe0001`) is distinct from any operational key and is cleaned up immediately, making it safe to run against a live cluster.

- **SCSI-3 PR probe and status API endpoints (`daemon/internal/handlers/ha_handler.go`)**:
  - `GET /api/ha/scsi/status`: queries dplane-fenced via Unix socket; returns running state, reservation key, and list of currently reserved `/dev/sgN` devices
  - `POST /api/ha/scsi/probe`: runs `SupportsReservations()` on specified devices or auto-enumerates pool disks via `zpool status -P` + sysfs. Returns per-device pass/fail with exact firmware error text for any rejected device

- **Hardware detection endpoint (`daemon/internal/handlers/ha_handler.go`, `GET /api/ha/hardware/detect`)**: Non-destructive scan returning watchdog availability (`/dev/watchdog`), dplane-fenced reservation status, pool SG device count, and a provisional HA path recommendation. No PROUT writes on this path. Path A' (shared_storage) is recommended when SG devices are present or dplane-fenced is running; Path B (replicated) is recommended otherwise. The frontend uses this for the dashboard topology badge and wizard auto-detection.

- **Path-aware HA setup wizard (`app-react/src/pages/HAPage.tsx`)**: The wizard now detects hardware on Step 1 and presents two distinct paths with different step content:
  - Path A' (Shared Storage): Step 5 = SCSI-3 PR PROUT probe with false-positive warning; Step 6 = Watchdog floor (recommended) + IPMI/PDU optional; Step 7 = Witness recommended; Step 8 = clean dissolution summary (zero RPO, no migration)
  - Path B (Replicated ZFS): Step 5 = ZFS replication config + RPO explanation; Step 6 = Watchdog critical warning + IPMI/PDU important; Step 7 = Witness required for watchdog isolation detection; Step 8 = non-zero RPO dissolution warning with TXG comparison note
  - Operator can override the auto-detected path via the two-card chooser in Step 1

- **`SCSIFencingCard` UI component**: Shows dplane-fenced running state, reservation key, fenced device list with green lock badges, and a "Probe PR Support" button that calls `POST /api/ha/scsi/probe`. Per-device results include firmware error text for any device that fails the PROUT round-trip. Wired into the fencing config section on the main HA dashboard.

- **`WatchdogConfigForm` UI component**: Enable toggle, device path, timeout, pet-interval fields. Active state shows a warning with the exact timeout value and the self-fence semantics. Explains `softdog` fallback for hardware without native watchdog. Notes that device/timeout changes require daemon restart; enable/disable takes effect immediately.

- **`TimingConfigForm` UI component**: Three-field form for `failover_after_seconds`, `heartbeat_interval_seconds`, and `hysteresis_window_minutes`. Client-side enforcement of the `failover >= heartbeat * 3` invariant (error shown inline before save). Notes which values require daemon restart vs take effect immediately.

- **`PathBadge` topology indicator**: Small badge on the main HA dashboard showing current detected topology (Path A' green / Path B blue / unknown grey) with reason text. Warns when the PROUT probe is still needed to confirm shared-storage capability.

- **RPO-aware HA dissolution**: `handleDisableHA()` replaces the direct `toggleHA.mutate({ enable: false })` calls. For replicated clusters (`effectivePath === 'replicated'`) it shows a confirmation dialog describing the replication lag risk before proceeding. Force-disable bypasses the RPO dialog.

### Changed

- **`dplane-fenced` enumeration logic (`daemon/internal/handlers/ha_handler.go`)**: `enumPoolSGDevices()` and `blockDevToSG()` helpers in the handler mirror the `dplane-fenced` enumeration so the setup probe and runtime fencing operate on the same device set. Keeps the two code paths consistent without introducing a shared library dependency.

- **HA timing config note surfaced in UI**: `TimingConfigForm` displays the `note` field from `GET /api/ha/timing` response explaining the restart requirement, rather than repeating it in static text.

### Security

- **SCSI-3 PR false-positive detection**: Closes the class of misconfigurations where a shared-storage HA cluster appeared healthy but had non-functional fencing because the setup probe passed a read-only capability check on drives that reject write commands. Any deployment using shared-storage fencing should run `POST /api/ha/scsi/probe` to confirm their specific hardware. The probe is safe to run against a live cluster.

## v14.1.0 (2026-06-05) - "Beacon"

Upgrade from: v14.0.0 - Schema migration required (migration 00008 adds `ha_cluster_config` table for runtime-configurable HA timing; applied automatically at startup). No breaking API changes. No breaking configuration changes.

This release addresses the operational maturity gaps identified, the monitoring island (no scrape endpoint), hardcoded HA timing constants, opaque technical error messages, and the absence of an honest integration guide. The foundation is unchanged; this release closes the last-mile-of-utility gap.

### Added

- **Prometheus/OpenMetrics exporter extension (`daemon/internal/handlers/prometheus.go`)**: `/metrics` now exposes ZFS dataset usage and quota per dataset, scrub/resilver progress per pool (with -1 when no scan is active), pool health at four levels (ONLINE=1, DEGRADED=0.75, FAULTED=0.5, UNAVAIL=0), per-peer HA health with role labels, replication schedule status (enabled, last run timestamp, last success), and build info. The HA manager is wired in at startup via `SetPrometheusHAManager`. Scrape with any Prometheus-compatible collector; no authentication required so existing scrape configs work without token management.

- **HA timing parameters configurable at runtime (`daemon/internal/ha/cluster.go`, migration 00008)**: `failover_after_seconds`, `hysteresis_window_minutes`, and `heartbeat_interval_seconds` moved from compile-time constants to the `ha_cluster_config` database table. `GetClusterTimingConfig` reads from the DB at each heartbeat cycle with fallback to historical defaults (45s/60m/15s). Constraint enforced: `failover_after_seconds >= heartbeat_interval_seconds * 3`. `GET /api/ha/timing` returns current values; `POST /api/ha/timing` updates them (requires AAL2). Changes to failover and heartbeat thresholds take effect after daemon restart; hysteresis takes effect immediately. This unblocks WAN-linked and high-latency deployments that previously required a recompile to tune.

- **Guided error responses (`daemon/internal/handlers/error_guide.go`)**: A `guidedError` type enriches error responses with a `guide` field (plain-English next step), a `code` field (machine-readable for UI routing), and an `action` field (UI hint). Applied to the five highest-volume error states: AAL2 gate, session expired, must-change-password, quota below current usage, and pool destroy blocked by active dependencies. Example: the pool destroy 409 now says which specific services are blocking and gives step-by-step resolution rather than "stop all shares and services first."

- **Persist-health-check console output**: When the `/persist` partition is not mounted at boot, the health check now writes the failure reason and recovery URL to `/dev/console` (in addition to syslog) before halting. Operators with IPMI SOL or serial console access see the reason immediately rather than a silent black screen.

- **`INTEGRATION-GUIDE.md` (`docs/admin/`)**: Honest documentation covering Prometheus scrape config and the full metrics table, SMB reconnect-on-failover behavior, NVMe-oF client setup, iSCSI ALUA HA configuration, GitOps/CI automation, the HA timing API, Docker hardware video transcoding (VA-API setup, device passthrough for Jellyfin/Plex, GPU passthrough via VFIO), and the offline install note for Intel GPU packages. See "Known behaviour" below for the transcoding and offline install detail.

### Changed

- **Pool health metric**: `dplaneos_zfs_pool_healthy` now returns 0.75 for DEGRADED and 0.5 for FAULTED instead of collapsing all non-ONLINE states to 0. Allows alerting rules to distinguish a degraded-but-functional pool from a completely unavailable one.

- **HA `heartbeatLoop`**: Reads `HeartbeatInterval` from the database on startup rather than hardcoding 15 seconds. Default unchanged.

- **HA `checkFailover`**: Reads `FailoverAfter` and `HysteresisWindow` from the database on each evaluation rather than hardcoding constants. Default values unchanged; behavior is identical on existing deployments.

- **`ha.Manager.DB()` accessor added**: Returns the database handle. Used by API handlers that need to persist HA config without direct access to the internal `db` field.

- **AAL2 and session-expired middleware errors**: Now include `guide`, `code`, and `action` fields alongside `error`. Clients that only read the `error` field are unaffected.

### Added (continued)

- **Active Directory / Winbind NSS integration (closes v14.0.0 known gap)**:
  - `nixos/modules/samba.nix`: adds `system.nssModules` and `system.nssDatabases` entries when `securityMode = "ads"`, so `ls -l`, `id`, `stat`, and SMB ACL display resolve AD user/group names instead of showing numeric UIDs. This was the only missing piece between "Samba joined to AD" and "AD users usable on the filesystem."
  - `daemon/internal/handlers/ldap.go` `JoinDomain`: adds DNS SRV pre-validation (`_kerberos._tcp.REALM`, `_ldap._tcp.REALM`) before attempting `kinit`. Failed DC reachability now surfaces a clear error ("check that the NAS is using the AD DNS server") rather than a cryptic 30-second kinit timeout.
  - `GET /api/ldap/domains/{name}/status`: new diagnostic endpoint returning winbind ping, machine account trust check (`wbinfo -t`), DC online status (`wbinfo --online-status`), and sample AD user/group lists. Used to verify the join is healthy and NSS resolution is working without SSH access.
  - `daemon/internal/handlers/ad_join.go`: legacy `/api/directory/join` endpoint now calls `syncIDMAPToNixwriter` after successful join, so both the legacy and multi-domain join paths activate winbind.
  - `daemon/internal/security/whitelist.go`: added `wbinfo_users`, `wbinfo_groups`, `wbinfo_online`, and `wbinfo_ping` whitelist entries.
  - `INTEGRATION-GUIDE.md`: rewritten AD section reflects the full working implementation. CTDB remains the only noted limitation (SMB reconnect-on-failover, no byte-range lock continuity across HA failover - same as TrueNAS active-passive).

- **Out-of-band hardware management: iLO, iDRAC, IPMI (`daemon/internal/bmc/`, migration 00009)**:
  Unified BMC abstraction layer auto-detects the available protocol (Redfish for iLO 5+/iDRAC 9+, iLO 4 legacy REST, or generic IPMI via ipmitool) and dispatches through the best available path. TLS certificate pinning (TOFU) is used for all Redfish connections instead of `InsecureSkipVerify`: on first enrollment the BMC's SHA-256 certificate fingerprint is captured and stored; subsequent connections verify the fingerprint matches, detecting both MITM attacks and BMC firmware updates that regenerate the certificate.

  New endpoints (all use `ha_fencing_config` credentials - same BMC as STONITH fencing):
  - `POST /api/bmc/enroll` (AAL2) - TOFU enrollment: capture and store BMC TLS fingerprint
  - `GET  /api/bmc/info` - detected BMC type, vendor, model, firmware version
  - `GET  /api/bmc/health` - temperatures, fan speeds, power consumption, voltages
  - `GET  /api/bmc/events?limit=N` - BMC system event log (Redfish or IPMI SEL)
  - `GET  /api/bmc/power` - current chassis power state
  - `POST /api/bmc/power` (AAL2) - power management (off, graceful_off, on, reset, graceful_reset)
  - `POST /api/bmc/reset-cert` (AAL2) - clear pinned fingerprint for re-enrollment after BMC cert regeneration

  BMC sensor metrics added to `/metrics`: `dplaneos_bmc_sensor_value{sensor,category}`, `dplaneos_bmc_sensor_ok{sensor,category}`, `dplaneos_bmc_chassis_power`, `dplaneos_bmc_scrape_ok`.

  `nixos/module.nix`: added `pkgs.dmidecode` (hardware identity) and `pkgs.lm_sensors` (local sensor fallback). Migration 00009 adds `bmc_tls_fingerprint`, `bmc_tls_pinned_at`, and `bmc_protocol` columns to `ha_fencing_config`.

### Security fixes

- **BMC TLS pinning bypass closed (`daemon/internal/bmc/client.go`, `daemon/internal/bmc/detect.go`)**: The initial BMC Redfish implementation fell through from a failed pinned connection to an unpinned probe that sent BasicAuth credentials over an unverified TLS channel. Fixed: if a certificate fingerprint is enrolled, a failed pinned connection returns an error immediately - no retry on an unverified channel. Detection probes (`probeRedfishUnauthenticated`, `probeILO4Unauthenticated`) no longer send credentials; the Redfish and iLO 4 service roots return metadata without authentication per spec.

### Known behaviour

- **Intel hardware video transcoding on offline installs**: The installer ISO excludes `intel-media-driver` (~100 MB, VA-API) and `intel-compute-runtime` (~300 MB, OpenCL) to keep the ISO within a manageable size. These packages were present in ISOs through v12.5.1 but grew significantly in nixpkgs 26.05. Systems installed from ISO without internet access will not have VA-API hardware transcoding until `nixos-rebuild` is run once the system is online. Systems deployed via GitOps or installed with internet access are unaffected. GPU passthrough (VFIO to VMs/containers) is not affected - it does not use these host-side packages. See `INTEGRATION-GUIDE.md` for setup instructions.

### CI / build fixes

- **Installer kernel pinned to 6.12 LTS** (`nixos/installer.nix`): The installer environment was still using `linuxPackages_6_6` after the appliance was bumped to 6.12. The mismatch caused both kernel versions to appear in the squashfs simultaneously, adding ~600 MB to the ISO. Fixed by syncing the installer kernel with `applianceConfig`.

- **`dplaneos-for-iso` target introduced** (`flake.nix`): The ISO now embeds a `dplaneos-for-iso` closure identical to `dplaneos` except `hardware.graphics.extraPackages` is stripped. This keeps the x86_64 ISO under the GitHub Actions runner disk limit while the running system retains the full Intel GPU stack. CPU microcode updates (security mitigations) are intentionally not stripped.

- **`flake.lock` auto-generated in CI** (`.github/workflows/ci.yml`): The `ha-vm-cluster` job now runs `nix flake lock`, commits the result back to main with `[skip ci]`, and uploads it as an artifact. Both ISO jobs download the artifact before building, ensuring all jobs in a run use the same nixpkgs revision. This closes the known gap documented since v12.5.1 where each CI job independently resolved nixpkgs HEAD and could silently pull in different package versions.

- **Third-party GitHub Actions pinned to SHA** (`.github/workflows/ci.yml`): `jlumbroso/free-disk-space`, `DeterminateSystems/magic-nix-cache-action`, and `wimpysworld/nothing-but-nix` pinned to specific commit SHAs. Mutable `@main` and `@v8` refs replaced.

### Release-integrity improvements

A pipeline review identified that the tarball binary (tested by all CI jobs) and the ISO binary (what users boot) were compiled by different paths: `CGO_ENABLED=0` subprocess-ZFS for the tarball, `CGO_ENABLED=1 -tags libzfs` for the Nix closure. Every integration test was exercising the wrong code path. The following changes close that gap and add the provenance and scanning the ISO was missing:

- **CGO build parity**: `prepare` now installs `libzfs-dev` and builds a third binary `dplaned-linux-amd64-cgo` (`CGO_ENABLED=1 -tags libzfs`). All four test stages (`validate`, `convergence`, `storage-failure`, `integration`) run this binary, matching the code path that ships on the ISO. The CGO_ENABLED=0 binary remains for the distribution tarballs (portable, no libzfs runtime requirement).

- **ARM64 behavioral coverage** (`arm64-api` job, `ubuntu-22.04-arm` runner): ARM64 was previously only sanity-checked with `--version` under QEMU. A new native arm64 job builds the daemon on the arm64 runner and runs the full REST API test suite. ZFS kernel-module tests are skipped via `SKIP_ZFS_TESTS=1` (module not available on the GitHub-hosted arm64 runner); auth, session, SCRAM, HA config, directory, and BMC endpoints are exercised natively.

- **ISO signing and SBOM**: Both `iso` (amd64) and `iso-arm` (arm64) jobs now run `cosign sign-blob` and `syft scan` after packaging. The `.bundle` (cosign signature) and `.sbom.json` (SPDX) are attached to the GitHub Release. The ISO now has the same provenance chain as the release tarballs. A checksum alone is integrity; the signature is provenance.

- **Vulnerability scanning** (`govulncheck`): Added to `prepare` after `go test`. Consumes the full dependency tree already enumerated in `go.sum`. Fails the build on known CVEs in direct or transitive Go dependencies. The project generates SBOMs and now also acts on them.

- **Frontend lint** (`eslint`): Added `eslint@10` with `typescript-eslint` and `eslint-plugin-react-hooks@7`. `npm run lint` runs in `prepare` after `tsc --noEmit`. Eight real bugs fixed in the process: ternary expressions used as statements in toggle-set handlers (expressions whose return values were silently discarded), empty `catch {}` blocks, `Math.random()` called during render (moved to `useMemo`), a useless null initialisation in the WebSocket store, a `useState` declaration appearing after the `useEffect` that uses its setter, and a ternary used as a statement in an `onSuccess` callback. The safety UX (STONITH confirmation gate, FAILOVER confirmation, IQN deletion gate) is now covered by a lint rule that will catch regressions.

### HA: network quorum witness and bulletproof enable/disable

- **Network quorum witness** (`daemon/internal/ha/network_witness.go`, migration 00010): A neutral IP or URL (rented VPS, cloud metadata endpoint, DNS nameserver) that both HA nodes independently probe to detect network isolation. No software needed on the target - it only needs to respond to TCP connections or HTTP requests. Configured under Settings → HA → Network Quorum Witness with live Test Connectivity. Guard 4b in `checkFailover()`: if the node cannot reach the witness it may be the isolated side - promotion suppressed. Additive-only: never enables failover alone. API: `GET/POST /api/ha/network-witness`, `POST /api/ha/network-witness/probe`.

- **HA enable/disable bulletproofed** (`daemon/internal/handlers/ha_handler.go`):
  - Enable pre-flight: blocks if STONITH fencing is in progress. Blocks with guided panel if no fencing method is configured (`code: "no_fencing"`).
  - Disable sequence (in order): (1) Patroni graceful demotion via `POST localhost:8008/demote`, polls `/primary` until 503 (max 20s) so the peer becomes PostgreSQL primary before services stop. Blocked unless `force:true` if demotion times out. (2) etcd graceful leave via `/v3/cluster/member/remove` so remaining members maintain quorum cleanly. (3) `nixos-rebuild switch` stops HA services via systemd. ZFS pools are NOT exported on disable - this node stays operational as a standalone. (4) On rebuild failure, the `ha_enable` JSON flag is reverted so `ha_enabled` in the UI reflects reality.
  - `POST /api/ha/switchover` (new, AAL2): graceful Patroni primary handoff to the standby node. Returns a job ID for frontend progress tracking. Used by the guided UI workflow before disable.
  - `Manager.IsFencingInProgress()` exposed as a public method.

- **HA on/off toggle** (`app-react/src/pages/HAPage.tsx`): HA is off by default after installation. The Settings → HA page shows a clear "Enable High Availability" button in the disabled state. The main dashboard header has a "Disable HA" button. HA can be enabled and disabled at any time; all NAS features (ZFS, SMB, NFS, iSCSI, Docker) remain fully operational in both states. The setup wizard is for HA configuration, not required for initial system use.

### Guided error UX and systematic error sanitization

- **`error_sanitize.go`** (`daemon/internal/handlers/error_sanitize.go`): Systematic error sanitization framework eliminating raw Go errors, CLI commands, system paths, and library internals from API responses. Provides `SanitizeSSH()`, `SanitizeZFS()`, `SanitizeDomainJoin()`, `SanitizeDB()`, `SanitizeServiceControl()`, `SanitizeHTTP()` functions that map technical errors to user-readable messages by category (auth failed, connection refused, no space, host key changed, etc.). Every sanitizer logs the raw error at WARNING level while returning only a safe message to the client. `userErr(code, message, guide)` and `respondUserErr()` helpers enforce the structured format.

- **Guided UX for blocked HA operations**: When an operation is blocked, the API returns a structured `{error, code, guide, action}` response (200 OK with `success:false`) instead of a raw error string. The UI renders a guided panel - not a toast - with a context-appropriate action button:
  - `patroni_primary` (disable while primary): panel shows "Hand off the primary role to the standby node first" + **Switch Primary to Standby** button → calls `POST /api/ha/switchover` → job progress tracker. After switchover, retry disable.
  - `no_fencing` (enable without fencing): panel shows why fencing matters + **Configure Fencing** button → scrolls directly to the fencing section on the same page.
  - Force Disable available as explicit fallback after seeing the `patroni_primary` guided panel.

- **Applied to highest-risk handlers**: `ad_join.go` (domain join, NTP pre-check, wbinfo verification - no more raw Kerberos/Samba output in responses), `replication_remotes.go` (SSH errors via `SanitizeSSH()` - no more cipher negotiation details, host key specifics, or authentication method lists), `zfs.go` line 99 (ALL ZFS commands now return structured errors with `code` and `guide` fields), `ftp.go`, `minio.go`, `nfs_handler.go` (service start/stop/restart and database errors).

- **HA guard test coverage** (`daemon/internal/ha/failover_integration_test.go`): `promotionWouldFire()` helper now mirrors all 6 guards in `checkFailover()` including Guard 4b. Two new tests: `TestFailover_NetworkWitnessUnreachable_DoesNotPromote` and `TestFailover_NetworkWitnessReachable_ClearsGuard`. CI gate expanded from 7 to 9 required guard tests.

### Dependency security

- **16 CVEs resolved** by bumping Go from 1.25.7 to 1.26.3 (stdlib CVEs in `crypto/tls`, `crypto/x509`, `net`, `net/http`, `net/url`, `os`, `net/textproto`) and modules: `golang.org/x/crypto` v0.50.0 → v0.52.0 (4 SSH CVEs: infinite loop, FIDO bypass, RSA/DSA DoS, server deadlock), `golang.org/x/net` v0.53.0 → v0.55.0 (Punycode label bypass in idna), `github.com/go-jose/go-jose/v4` v4.1.3 → v4.1.4 (JWE decryption panic). Two stdlib CVEs (GO-2026-5039, GO-2026-5037) remain tracked pending nixpkgs updating to go1.26.4.

### Known gaps

- `flake.lock` is committed automatically by CI on each main-branch push. A committed `flake.lock` will be present in the repository from this release onward.
- ARM64 ZFS integration tests (degraded-pool, interrupted-send, promotion mechanics) require a self-hosted arm64 runner with the ZFS kernel module loaded. The GitHub-hosted `ubuntu-22.04-arm` runner does not support it. The amd64 CGO path covers ZFS integration fully; the arm64 gap is infrastructure, not code.
- Two stdlib CVEs (GO-2026-5039: `net/textproto`, GO-2026-5037: `crypto/x509`) require go1.26.4 which nixpkgs 26.05 does not yet ship. govulncheck reports them; the CI gate is set to allow exactly 2 known tracked CVEs and fails on any additional.

---

## v14.0.0 (2026-06-05) - "Vanguard"

Upgrade from: v13.0.0 - Schema migrations required (00006 adds SCRAM-SHA-512 credential columns to `users` and `disk_fault_tolerance_pct` to `ha_fencing_config`; 00007 adds `aal` to `sessions`; both applied automatically at startup). No breaking API changes. No breaking configuration changes.

**Known gap - not closed in this release**: Clustered SMB with Active Directory (winbind/NSS/CTDB). TrueNAS SCALE ships Samba with winbind for NSS-level user/group resolution and CTDB for distributed Samba state across HA nodes. DPlane has LDAP-backed authentication and a TTL directory cache but no winbind daemon, no nsswitch integration, and no CTDB. Closing this gap requires shipping a Samba + winbind NixOS module, an NSS overlay that resolves AD identities at the filesystem level, and CTDB coordination with the HA cluster. This is not a minor addition; it is a separate subsystem. It is documented here as an open gap rather than omitted.

This release closes the remaining architectural gaps between DPlaneOS and TrueNAS SCALE, adds several capabilities that exceed TrueNAS, and fixes three mechanical failure modes introduced by translating Python/Debian patterns into Go/NixOS: a sync-before-reboot bug that caused D-state deadlock during failover, configfs writes that outran NVMe AEN propagation, and non-atomic state file writes racing with ZFS pool export on NixOS. The code quality pass removed all dead code and migrated 137 files from `interface{}` to `any`.

### Added

- **SCRAM-SHA-512 authentication (`daemon/internal/scram/`, migration 00006)**: Full RFC 5802 SCRAM-SHA-512 implementation with PBKDF2 (100k iterations, SHA-512). Two-round REST challenge/response via `POST /api/auth/scram/challenge` and `POST /api/auth/scram/verify`. On every password-set path the daemon now derives and stores both a bcrypt hash (backward-compatible web login over HTTPS) and SCRAM keys (StoredKey + ServerKey). Applied to all six callsites: user self-service `ChangePassword`, admin `CreateUser`, admin `UpdateUser`, admin `ResetUserPassword`, and both branches of the bootstrap `HandleSetup` (INSERT and UPDATE). The SCRAM verify path correctly gates on TOTP and must-change-password, mirroring the bcrypt path exactly. 9 unit tests. Exceeds TrueNAS, which applies SCRAM only to API keys; DPlane applies it to all authentication paths.

- **must_change_password session restriction (`daemon/internal/security/session.go`, `daemon/internal/middleware/rbac.go`)**: Sessions created after a forced password reset or admin password set now carry `status='must_change_password'` instead of `active`. `ValidateSessionAndGetUser` reads this status; the RBAC middleware rejects all requests except `POST /api/auth/change-password` with a 403 and `action: "change_password"` until the user complies. Applies to both bcrypt and SCRAM login paths.

- **Fencing disk fault tolerance (`daemon/internal/ha/fence.go`, `daemon/cmd/dplane-fenced/main.go`, migration 00006)**: `DiskFaultTolerancePct` field on `FencingConfig` (0-50%, default 10%, capped at 50%). `dplane-fenced` now counts SCSI-3 PR reservation failures per disk; if the failure fraction exceeds the threshold it calls `log.Fatalf` (forcing systemd restart and requiring admin review) rather than continuing with reduced fencing coverage. `SaveFencingConfig` writes the tolerance to `/var/lib/dplaneos/fence-tolerance` atomically (temp + rename) for `dplane-fenced` which has no DB access. `dplane-fenced` caches the last-known-good value in memory so pool export cannot silently revert the threshold. Matches TrueNAS 10% default. UI field added to `FencingConfigForm`.

- **Directory cache persistence (`daemon/internal/ldap/cache.go`)**: `NewDirectoryCacheWithPersistence(ttl, path)` loads the last-known-good JSON snapshot on daemon startup so authentication works immediately before the first LDAP contact after a restart. Successful refreshes write an atomic snapshot (temp + rename). Concurrent pool export races are handled: if the persistent path is on a ZFS dataset being exported, the rename fails cleanly and the in-memory cache continues serving.

- **Job credentials (`daemon/internal/jobs/jobs.go`)**: `UserID`, `Username`, and `Role` fields added to `Job` and `JobSnapshot`. `StartWithCreds(jobType, userID, username, role, fn)` variant allows job goroutines to inspect caller identity without threading a request context through call sites. `Start` delegates to `StartWithCreds` with zero values.

- **NVMe-oF ANA groups (`daemon/internal/nvmet/spec.go`, `daemon/internal/nvmet/apply_linux.go`)**: `ANAState` type and `ANAGroup` struct added. `Export` gains `ANAEnabled` and `ANAGroups` fields. `applyANAGroupState` writes the ANA group directory and `ana_state` file in kernel configfs, reads it back to confirm the kernel accepted the write (empty read-back is an error; string normalisation by the kernel, e.g. "standby" to "14", is logged but not an error), then waits `ANAPropagationDelay` (200ms by default, 0 in tests) as a timing heuristic for AEN propagation to connected hosts. This wait does not verify AEN receipt; it reduces the probability of a race between the state write and VIP release. ANA toggle added to NVMe target creation form. Note: TrueNAS SCALE 25.10 also ships NVMe-oF with ANA support. DPlane's contribution is the explicit failover-sequencing discipline - write ANA state, wait, then yield the VIP - not the existence of ANA itself.

- **iSCSI ALUA (`daemon/internal/handlers/iscsi.go`)**: `ALUAState` type (Active/Optimized=0, Active/Non-Optimized=1, Standby=2, Unavailable=3). `ISCSICreateRequest` gains `alua_enabled` and `alua_state` fields. `configureALUA` sets `alua_support=1` and `alua_access_state` on the LIO TPG via targetcli. `POST /api/iscsi/targets/alua` allows runtime state flip during HA failover without tearing down the target. `POST /api/ha/alua-standby` sets all ALUA-enabled targets to Standby in one call; the Keepalived `notify_backup` script calls this before `POST /api/ha/standby` to ensure initiators see a clean path state change before pool export begins. ALUA toggle in iSCSI creation form; ALUA state-change modal in the target list row.

- **ZFS pool destroy dependency check (`daemon/internal/handlers/zfs_pool_maintenance.go`)**: `HandlePoolDestroy` converted from standalone function to `ZFSHandler` method. `poolDependencyCheck` queries `/proc/mounts` for active dataset mountpoints and the `nfs_exports` and `smb_shares` DB tables. With `force=false` (default), any active dependency returns HTTP 409 with a dependency list. With `force=true`, the dependency check is bypassed: this is a recovery mechanism for cases where a share entry is corrupt and cannot be removed through normal means. The `confirmRoute` wrapper requires typing the pool name before the request arrives. Note: pool export with `force=true` behaves differently - it still checks and refuses if dependencies are found, because a forced export with open file handles causes client-side data corruption, whereas a forced destroy is terminal regardless.

- **ZFS force export dependency check (`daemon/internal/handlers/zfs_operations.go`)**: `ExportPool` runs `poolDependencyCheck` before honoring `force=true`. Force export is refused if any dataset in the pool is actively mounted or referenced by a share, preventing silent data corruption on connected NFS/SMB clients.

- **ZFS quota-below-current-usage guard (`daemon/internal/handlers/zfs_operations.go`)**: `SetDatasetQuota` queries `zfs get -H -p referenced` (raw bytes via `-p`) before applying `refquota`. If the proposed quota is below current referenced usage the request returns HTTP 400 with a human-readable message. `parseRawBytes` and `humanToBytes` helpers added.

- **ZFS snapshot clone detection (`daemon/internal/handlers/zfs_snapshots.go`)**: `DestroySnapshot` scans `zfs list -t filesystem,volume -o name,origin` before attempting deletion. If any clone's origin matches the target snapshot, returns a specific error naming the dependent clone instead of the opaque ZFS kernel rejection.

- **Snapshot retention policies (`daemon/internal/handlers/replication_retention.go`, new)**: `SnapshotRetentionPolicy` struct with per-bucket keep counts (hourly, daily, weekly, monthly, yearly). Time-bucket algorithm retains the N most recent snapshots per bucket; snapshots beyond the limit are pruned. Only snapshots matching the configured prefix are touched; manual snapshots are never pruned. `RunRetentionPolicies` is called from the replication monitor after every tick so local pruning happens after remote copies are confirmed current. `GET/POST /api/replication/retention` CRUD endpoint. RetentionTab UI with create/edit/delete modal in ReplicationPage.

- **Replication: remote snapshot chain validation (`daemon/internal/handlers/replication_schedule.go`)**: Before every incremental send, the schedule runner verifies the base snapshot exists on the remote via `ssh remote zfs list -H -t snapshot -o name <remote_base>`. If the remote chain is broken (remote rollback, manual pruning), falls back to a full send with a clear log message rather than failing mid-stream and leaving the remote dataset inconsistent.

- **Replication: restore from remote (`daemon/internal/handlers/replication_remote.go`)**: `POST /api/replication/restore` - disaster recovery path. `execPipedRestore` implements the reverse pipeline: `ssh remote zfs send | local zfs recv`. Verifies the snapshot exists on the remote before starting, supports `-F` force, pipes stderr through the existing progress parser for real-time ETA. RestoreForm UI tab in ReplicationPage.

- **Replication: retry with exponential backoff (`daemon/internal/handlers/replication_schedule.go`)**: The send pipeline is wrapped in a 3-attempt retry loop with backoff (30s then 120s) for transient network failures. All attempts failing produces a single error message including the attempt count.

- **Replication: concurrent schedule conflict lock (`daemon/internal/handlers/replication_schedule.go`)**: `replActiveSet` map guarded by `replActiveMu`. Two schedules targeting the same `sourceDataset|remoteID` pair cannot run concurrently. The second is rejected with `"skipped"` status and a clear message rather than corrupting the remote ZFS stream.

- **HA: D-state safe self-reboot (`daemon/internal/ha/standby.go`, `daemon/internal/ha/standby_reboot_linux.go`)**: `ForceSelfReboot` now calls `syscall.Reboot(LINUX_REBOOT_CMD_RESTART)` via `golang.org/x/sys/unix` as its primary path - a single kernel syscall with no fork, no exec, no filesystem I/O. `runtime.LockOSThread()` is called first to ensure the syscall runs on a dedicated OS thread that cannot be migrated even if other goroutines are blocked in cgo D-state calls. The `exec.Command("reboot", "-f")` remains as fallback only. See fix section for why `sync()` was removed.

- **Text-input confirmation for highest-risk HA operations**: Both STONITH fence operations in `HAPage.tsx` require typing `STONITH` before firing. Local failover requires typing `FAILOVER`. User deletion in `UsersPage.tsx` requires typing the exact username. iSCSI target deletion requires typing the IQN suffix.

- **Authentication Assurance Level (AAL) enforcement (`daemon/internal/middleware/rbac.go`, migration 00007)**: Sessions now carry an `aal` column (1 = password only, 2 = password + TOTP verified). `POST /api/auth/totp/verify` upgrades the session to AAL2 on success. `RequireAAL2` middleware rejects requests with AAL < 2 with HTTP 403 and `action: "enable_totp"`. Applied to pool destroy, password reset, fencing configuration, and ALUA state flip - operations where an account compromise under password-only auth would cause irreversible damage.

- **`POST /api/ha/alua-standby`**: Sets all ALUA-enabled iSCSI targets to Standby access state in one call. Called by the Keepalived `notify_backup` script before pool export so initiators see a clean path-state transition rather than an abrupt loss when the VIP moves.

- **Replication: post-receive integrity check**: After every `zfs recv` (both scheduled replication and restore-from-remote), the daemon verifies the expected snapshot exists on the destination with `zfs list -H -t snapshot`. If the snapshot is absent, the job fails with a specific message rather than reporting success on a partial transfer. A resume token check is also run and surfaced as a warning if present.

- **Confirmations added to 10 previously unprotected destructive operations**: DirectoryPage (LDAP group mapping remove, LDAP cache reset), FirewallPage (delete rule), NetworkPage (delete VLAN, delete bond), QuotasPage (dataset quota, project quota), NFSPage (delete export), SharesPage (delete SMB share). NFSPage and SharesPage migrated from fragile `[confirming]` local state pattern to the `useConfirm` hook.

### Changed

- **`interface{}` replaced with `any` across 137 first-party Go files**: Go 1.18+ idiomatic alias. Zero semantic change; vendor directory excluded.

- **`HandlePoolDestroy` is now a `ZFSHandler` method**: Required for database access to run `poolDependencyCheck`. Route registration updated from `handlers.HandlePoolDestroy` to `zfsHandler.HandlePoolDestroy`.

- **`ForceSelfReboot` no longer calls `sync()`**: See Fixed section. The reboot path now follows the correct sequence: lock OS thread, attempt direct kernel reboot syscall, fall back to `reboot -f` (which already skips sync via the `-f` flag). The `logger` call is now non-blocking (goroutine).

- **Replication schedule failure alerts use `"warning"` level**: `updateScheduleStatus` previously dispatched `"info"` on all status changes including failures. Failures now use `"warning"` which routes to SMTP and webhook alert channels per the alert dispatch rules.

- **Replication monitor runs retention after each tick**: `StartReplicationMonitor` calls `RunRetentionPolicies` after `runDueReplicationSchedules` on every 5-minute tick. Retention is intentionally run after replication, not before, so local pruning only happens after remote copies are confirmed current.

- **`applyANAGroupState` blocks for AEN propagation**: After writing `ana_state` to configfs, the function sleeps `ANAPropagationDelay` (200ms) before returning. Callers cannot proceed to VIP handoff until this returns, ensuring NVMe hosts have processed the state-change notification.

- **`replication_schedule.go` imports `sync` for conflict lock**: No behavioral change to existing schedules that do not share a source+remote pair.

### Fixed

- **HA: `sync()` before `reboot -f` caused D-state deadlock on pool export timeout**: When ZFS pool export blocked in D-state (hung bus, stuck SCSI device), `ForceSelfReboot` called `exec.Command("sync").Run()` before `reboot -f`. `sync(2)` flushes all dirty pages including those from the D-state hung pool - it joins the same D-state queue and the reboot function blocks indefinitely. The `-f` flag on `reboot` explicitly means "skip sync and reboot immediately"; the `sync` call directly defeated it. Fixed by removing `sync` entirely and using `syscall.Reboot` (which never syncs) as the primary path.

- **SCRAM auth: TOTP bypass** - `SCRAMVerify` was creating `active` sessions even when `totpEnabled == 1`. Fixed to create `pending_totp` sessions with a 5-minute TTL, mirroring the bcrypt login path exactly.

- **SCRAM auth: `must_change_password` session bypass** - Both the bcrypt login path and the new `SCRAMVerify` handler were creating `active` sessions when `mustChange == 1`. Fixed in both paths: sessions now carry `status='must_change_password'` and the RBAC middleware enforces the restriction.

- **SCRAM keys missing from admin password paths** - `CreateUser`, `UpdateUser` (admin password change), `ResetUserPassword`, and bootstrap `HandleSetup` called `bcrypt.GenerateFromPassword` without deriving SCRAM keys. All four now atomically store bcrypt hash and SCRAM keys (salt, iterations, StoredKey, ServerKey) in a single UPDATE/INSERT.

- **`cancelEdit` in `ISCSIPage.tsx` did not reset `aluaEnabled`**: After canceling an edit that had ALUA enabled, creating a new target would incorrectly default to ALUA on. Fixed.

- **`cancelEdit` and `startEdit` in `NVMeOFPage.tsx` ignored `anaEnabled`**: `cancelEdit` did not reset `anaEnabled` to false; `startEdit` did not load `ana_enabled` from the existing export. Both fixed. `NVMeExport` interface extended with `ana_enabled?: boolean`.

- **`nvmet/spec.go` helper functions appeared unused on non-Linux builds**: `slug`, `subsysDirName`, `portDirName`, `hostDirName`, and `nvmetRoot` are used only in `apply_linux.go` (linux build tag). staticcheck flagged them as unused on Windows. Fixed by moving them to `apply_linux.go` and removing unused imports from `spec.go`.

### Removed

- **Dead code**: `saveRsyncSchedules`, `executeCommandAsync`, `getPoolUsagePercent`, `executeBackgroundCommandWithTimeout`, `isInZFSPool`, `detectDiskType`, `saveFileShares`, `runGitGlobal`, `saveMinioConfig`, `saveRemotes`, `saveReplicationSchedules`, `persistDHCP`, `persistRemoveInterface`, `persistFirewallPorts`, `saveSSHKeys` (11 unused exported functions across 10 handler files). `scheduleFile_deprecated` const, `netRollbackContent` and `netRollbackPath` vars in `zfs_operations.go`, `clearanceCooldown` const in `monitoring/background.go`. Two unused `steps` variable assignments in `docker_enhanced.go`. Removed unused `os/exec` and `errors` imports from `command_utils.go` after function removal.

---

## v13.0.0 (2026-06-04) - "Aegis"

Upgrade from: v12.5.1 - Schema migration required (migration 00005 adds `allowed_resources` column to `api_tokens`; applied automatically at startup). No breaking API changes. No breaking configuration changes.

The version jump reflects a fundamental architectural advance: the safety and operational patterns documented in TrueNAS SCALE's source code have been studied and applied to DPlaneOS. The result is a system that knows why it cannot fail over, refuses to corrupt pools by rebooting itself rather than yielding dirty, refuses to delete datasets that have active service attachments, and gives API consumers fine-grained access boundaries that cannot be exceeded regardless of the token holder's role.

### Added

- **HA: Force-reboot on pool export timeout (`daemon/internal/ha/standby.go`)**: When this node needs to yield (planned failover, Keepalived BACKUP event), it now exports all ZFS pools within a 4-second deadline. If export does not complete in time, the node calls `reboot -f` unconditionally. A node that cannot cleanly yield its pools must be considered unsafe - rebooting eliminates the split-brain risk without requiring the peer to fence it. Mirrors TrueNAS `ZPOOL_EXPORT_TIMEOUT = 4s` from `plugins/failover_/event.py`. The Keepalived `notify_backup` script now calls `POST /api/ha/standby` to trigger this path on VIP demotion.

- **HA: Granular disabled reasons (`daemon/internal/ha/disabled_reasons.go`)**: `GET /api/ha/status` now returns a `disabled_reasons` array with typed reason codes and human-readable descriptions. Codes: `NO_FENCING_CONFIGURED`, `NO_WITNESS_REACHABLE`, `HYSTERESIS_ACTIVE`, `SUBORDINATE_MODE`, `MAINTENANCE_MODE`, `FENCING_IN_PROGRESS`, `NO_PEERS`, `ALL_PEERS_HEALTHY`, `VERSION_MISMATCH`, `CLUSTER_SECRET_MISMATCH`. Mirrors TrueNAS `DisabledReasonsEnum` from `plugins/failover_/enums.py`. The UI triage panel can now show "daemon versions differ between nodes" instead of a generic "HA is broken" banner.

- **GitOps: Dataset attachment graph (`daemon/internal/gitops/diff.go`)**: `blockedCheckDataset` now accepts a `DiffContext` carrying the database handle and queries for active service attachments before allowing dataset deletion: SMB shares pointing to the dataset's mountpoint, NFS exports at the same path, iSCSI targets with ZVols under the dataset, and Docker stacks with volume bind-mounts to the path. A dataset with any active attachment is `BLOCKED` with a message naming the specific services. This closes the gap with TrueNAS `pool_/dataset_attachments.py`, which performs the same graph traversal before any destructive operation. All `ComputeDiff` callers in the GitOps handler and convergence check now pass `DiffContext{DB: h.db}`.

- **LDAP: Directory cache with TTL and resilience (`daemon/internal/ldap/cache.go`)**: New `DirectoryCache` with configurable TTL (default 5 minutes), background refresh at TTL/2, and stale-data fallback when the directory server is temporarily unreachable. `CachedClient` wraps `Client` and starts the refresh goroutine automatically. On login the cache entry for the authenticated user is updated immediately. `Config` gains `CacheTTL` and `SyncInterval` fields. Mirrors TrueNAS `DSCacheFill` pattern from `plugins/directoryservices_/cache.py`.

- **API tokens: Resource-level scoping (`daemon/internal/handlers/api_tokens.go`, migration 00005)**: API tokens can now carry an `allowed_resources` JSON array of `{method, resource}` rules using fnmatch-style wildcards (e.g. `{"method":"GET","resource":"/api/zfs/*"}`). The session middleware enforces these rules before every request - a token with a resource allowlist cannot access endpoints outside its declared pattern regardless of the holder's role. An empty `allowed_resources` array preserves the previous coarse-scope behaviour. Mirrors TrueNAS `utils/allowlist.py` (Allowlist class with exact + pattern matching). Migration 00005 adds `allowed_resources TEXT NOT NULL DEFAULT '[]'` to `api_tokens`; existing tokens are unaffected.

### Changed

- **HA `BecomeStandby` API endpoint (`POST /api/ha/standby`)**: New endpoint that initiates graceful pool export. Returns 202 immediately; the export and potential self-reboot happen asynchronously so the calling Keepalived notify script does not block.

- **HA status response extended**: `GET /api/ha/status` now includes `disabled_reasons` alongside the existing `cluster` and `witness` objects.

- **`SessionUser` extended with `AllowedResources`**: The security package's `SessionUser` struct now carries the token's `allowed_resources` JSON for middleware enforcement. This is an internal type change with no API surface impact.

---

## v12.5.1 (2026-06-04) - "Conduit"

Upgrade from: v12.5.0 - No schema migration required. No breaking API changes. No breaking configuration changes. Drop-in upgrade.

### Fixed

- **Daemon switched to Unix domain socket (`/run/dplaneos/dplaned.sock`)**: The daemon previously listened on `127.0.0.1:9000` for nginx-to-daemon communication. This consumed a TCP port in the same address space as MinIO's standard API port, causing MinIO to fail to bind on a fresh install with default settings. The internal pipe is now a Unix socket - nginx proxies `/api/` and `/ws/` to `http://unix:/run/dplaneos/dplaned.sock:/`. No TCP port is consumed for DPlaneOS internal plumbing. This matches the pattern used by TrueNAS SCALE, OpenMediaVault, and other professional NAS operating systems. MinIO retains its standard 9000/9001 ports with no conflict. The `listenAddress` and `listenPort` NixOS module options are removed; `socketPath` replaces them (read-only, `/run/dplaneos/dplaned.sock`).

- **All internal daemon references updated to Unix socket**: The socket change in the daemon itself was complete, but every other component that communicated with the daemon via `http://127.0.0.1:9000` also needed updating. Fixed in this release:
  - Cron hook shell commands written by the backup scheduler, snapshot scheduler, and SMART task handler (`backup_schedule.go`, `system_extended.go`, `smart.go`) - these produce systemd timer `ExecStart` lines; all now use `curl --unix-socket /run/dplaneos/dplaned.sock`.
  - Disk hot-plug notification scripts (`install/scripts/notify-disk-added.sh`, `notify-disk-removed.sh`) - previously read a port from config, now use the socket directly.
  - OTA health check (`nixos/ota-update.sh`) - post-reboot daemon liveness check updated.
  - Post-install validation (`install/scripts/post-install-validation.sh`) - health endpoint probe updated.
  - System audit (`install/scripts/system-audit.sh`) - port 9000 TCP checks replaced with socket existence and connectivity checks.
  - Integration test (`install/scripts/integration-test.sh`) - `API` base replaced with socket-based curl; port 9000 exposure check replaced with socket presence check.
  - `install.sh` (non-NixOS path) - nginx `proxy_pass` directives and daemon `--listen` argument updated.
  - `nixos_guard.go` - port 9000 was hardcoded as an "immune" system port exempt from firewall drift detection; removed (9000 is now available for user services such as MinIO).
  - `gitops/verify.go` - post-apply service probe was checking TCP port 9000; now probes nginx on port 80, which is the actual public entry point.

- **Installer ISO: python3.11 HTML doc build no longer blocks the ISO**: nixpkgs 26.05 ships Sphinx 9.1 + docutils 0.22.4, which produce a `TypeError` when building Python 3.11 HTML documentation. `documentation.doc.enable = false` was already set in `applianceConfig` (preventing the target system from pulling the broken derivation), but the installer ISO has its own separate NixOS evaluation that did not inherit this setting. Added `documentation.doc.enable = false` and `environment.extraOutputsToInstall = lib.mkForce []` to both `installer.nix` and `witness-installer.nix`.

- **CI: docs-only pushes no longer trigger the full build pipeline**: The `paths-ignore` filter used `*.md` which only matches Markdown files at the repository root. Files such as `nixos/README.md` fell through and triggered CI. Changed to `**/*.md` to cover Markdown files in all subdirectories.

---

## v12.5.0 (2026-06-03) - "Operator"

Upgrade from: v12.4.0 - No schema migration required. No breaking API changes. No breaking configuration changes. Pure frontend, NixOS, and design system changes.

### Added

- **SSE one-time ticket authentication (`POST /api/sse/ticket`, `daemon/internal/handlers/sse_ticket.go`)**: Replaces session-ID-in-URL pattern for Server-Sent Events. `EventSource` cannot send custom headers; the previous approach embedded the full session token in the URL where it appeared in access logs, browser history, and `Referer` headers. The new flow: the frontend calls `POST /api/sse/ticket` (authenticated via normal headers) to receive a 32-byte random hex token valid for 30 seconds and a single connection. The session middleware checks `?ticket=` only on the two SSE paths (`/api/system/logs/stream`, `/api/docker/stacks/logs/stream`), consumes the token atomically, and sets the user context. Session IDs never appear in any URL. `Referrer-Policy: no-referrer` added to the log stream handler.

- **`usePersistedState` hook (`app-react/src/hooks/usePersistedState.ts`)**: Drop-in replacement for `useState` backed by `localStorage`. Lazy-initialises from the stored value on first render, writes back on every update, handles both string and number types. Applied to 14 preferences across 12 pages: tab selections on Pools, Network, Replication, Users, Quotas, GitOps, iSCSI, Snapshot Scheduler, Settings, Security, Docker (outer tab), and LogsPage level filter and history limit. View mode preference (grid/list/stacks) on DockerPage was already fixed; Docker outer tab now also persists.

- **`@/lib/fmt.ts` - locale-aware date utilities**: `fmtDate(raw)` and `fmtDateTime(raw)` using `undefined` locale to respect the user's browser/OS setting. Replaces hardcoded `'de-DE'` locale in seven pages: FilesPage, UsersPage, GitOpsPage, CloudSyncPage, SupportPage, DirectoryPage, SecurityPage.

- **`--surface-2` CSS token**: Added to both `:root` (dark: `hsla(hue, 20%, 22%, 0.5)`) and `[data-theme="light"]` (light: `hsl(hue, 15%, 87%)`). Was referenced in 20 places across DockerPage and NVMeOFPage but never defined, causing all panel headers, code blocks, and toolbar backgrounds to silently render transparent.

- **NixOS: `tun` kernel module (`nixos/module.nix`)**: Added `"tun"` to `boot.kernelModules`. Required for OpenVPN and Tailscale Docker containers to create `/dev/net/tun`. Previously absent, causing OpenVPN and Tailscale containers to fail at startup with "cannot open /dev/net/tun: No such file or directory".

### Fixed

- **Frontend-backend wiring: 7 broken mutations fixed**:
  - PoolsPage encryption unlock/lock: sent `name`/`passphrase` but handler reads `dataset`/`key`.
  - QuotasPage dataset quota: called `POST /api/zfs/datasets` (creates a dataset) instead of `POST /api/zfs/dataset/quota`; quota was never set.
  - NetworkPage DNS save: sent field `dns` but handler reads `nameservers`; DNS could never be saved.
  - RemovableMediaPage mount/unmount/eject: sent `path` field but handler reads `device`; all three operations returned 400.
  - SharesPage SMB sessions: `GET /api/shares/smb/sessions` and `POST /api/shares/smb/sessions/disconnect` were not registered; both returned 404. Added `ListSMBSessions` and `DisconnectSMBSession` handlers in `shares_crud.go` parsing `smbstatus` output.
  - LogsPage SSE stream: session was passed as `?session=<id>` but the session middleware only reads the `X-Session-ID` header; the log stream always returned 401. Fixed via the new SSE ticket system.
  - DirectoryPage LDAP mapping delete: sent `id` in the JSON body but handler reads `r.URL.Query().Get("id")`.

- **NixOS: Samba and NFS were silently disabled despite being core NAS features (`nixos/modules/samba.nix`, `nixos/modules/nfs.nix`)**: Both modules used `lib.mkEnableOption` (default false) and neither was ever set to `true` anywhere in the codebase. A NAS OS that ships with SMB and NFS disabled is broken by definition. Fixed: `enable` option changed from `mkEnableOption` to `lib.mkOption { type = lib.types.bool; default = true; }` in both modules. Both modules are now imported directly from `nixos/module.nix` (alongside `ha.nix`) so every DPlaneOS installation gets them without any user configuration. NFS was also missing from the `flake.nix` production configs entirely; now comes transitively via `nixosModules.dplaneos`.

- **Dead route aliases removed (`daemon/cmd/dplaned/main.go`)**: Three routes that were never called by any frontend code deleted:
  - `/api/settings/telegram` (alias for `/api/alerts/telegram`)
  - `/api/system/acl` (alias for `/api/acl/get`+`/api/acl/set`)
  - `/api/status` (alias for `/api/system/status`)

- **VPN handler removed (`daemon/internal/handlers/system.go`)**: The `if action == "vpn" || action == "add_vpn" || action == "remove_vpn"` branch in `ApplyNetworkWithRollback` returned 501 with a message directing operators to Docker. The frontend never sent these action strings. The handler was a lie: it advertised VPN as a known-but-unimplemented network action. Unknown actions now fall through to the existing 400 response.

- **ApiExplorerPage stale endpoint entries (`app-react/src/pages/ApiExplorerPage.tsx`)**: Five endpoint entries pointed to non-existent paths: `/api/shares/settings` (correct: `/api/smb/settings`), `/api/hardware` (correct: `/api/zfs/smart` + `/api/system/disks`), `/api/network/interfaces` (correct: `/api/system/network`), `/api/system/info` (correct: `/api/system/status`), `/api/alerts` (replaced with three real alert endpoints). All corrected to working paths.

- **TerminalPage UI chrome hex colors**: Non-xterm hex values in the title bar (`#a78bfa`, `#e2e8f0`, `#f87171`, `rgba(167,139,250,0.15)`, etc.) and `StatusDot` component replaced with design system tokens (`var(--primary)`, `var(--text)`, `var(--error)`, `var(--warning)`, `var(--success)`, `var(--text-secondary)`, `var(--text-tertiary)`). xterm.js `ITheme` hex values unchanged (library API requires raw hex).

- **PoolsPage "All Healthy" storage summary badge hardcoded**: The badge always showed "All Healthy" with a `check_circle` icon regardless of actual pool health. Now derives from `pools.every(p => p.health === 'ONLINE')` and shows a `warning` icon with "N Pool(s) Degraded" when any pool is not ONLINE.

- **Snapshot rollback had no snapshot picker (`app-react/src/pages/DatasetsPage.tsx`)**: The rollback modal blindly targeted the most recent snapshot with no way to select a specific restore point. Now fetches available snapshots via `GET /api/zfs/snapshots?dataset=`, renders them sorted newest-first, requires selection, and posts `{ snapshot: "dataset@name", force: true }`. A destructive-action warning is shown.

- **QuotasPage dataset input required exact free-text entry**: The dataset name input required operators to type `tank/data` exactly. Replaced with a `<select>` populated from `GET /api/zfs/datasets`.

- **NetworkPage apply warning shown after change fires**: The 30-second revert window warning appeared only after the network change was already applied. Moved inside the `ConfigureIfaceModal` as a pre-action info banner so operators see it before clicking Apply.

- **Delegation page lost state on reload**: The ZFS delegation UI tracked additions in client state only. After reload, only the raw `zfs allow` text was shown. Added `parseZfsAllow()` parser that extracts structured entries (type, principal, permissions) from the raw output into a table. Raw output preserved in a collapsed `<details>` block.

- **FirewallPage had no rule deletion**: Rules could be added but not removed from the UI. Delete button added to each rule row calling the existing `POST /api/firewall/rule { action: "delete", rule_num: N }` endpoint.

- **Replication job failures had no log link**: Failed schedule rows showed status but no way to see why. "View logs" button added that opens a `JobConsole` modal when `last_status === 'failed'` and `last_job_id` is present.

- **Create Dataset dedup inconsistency**: The DatasetsPage create modal had a deduplication selector; PoolsPage version did not. Dedup field added to PoolsPage `CreateDatasetModal`.

- **FilesPage "Edit" opened binary files**: The context menu "Edit" action appeared for all file types. Renamed "Edit Text" and restricted to recognized text file extensions via an `isTextFile()` helper checking against a set of known text extensions.

- **Username creation had no client-side validation**: Invalid usernames (e.g., starting with a digit, containing spaces) failed silently at the server with a generic error. Added `/^[a-z_][a-z0-9_-]*$/` validation and 2-32 character length check before the POST, plus inline hint text on the username field.

- **AuditPage dev artifact and broken icon**: Page title had a leftover "RESTORED: " prefix. `Icon name="Search"` used PascalCase (Material Symbols names are snake_case, so this rendered nothing). Both fixed.

- **ACLPage bare form when accessed without a path param**: Direct sidebar navigation to `/acl` showed only an empty path input with no guidance. Added a centered guidance card explaining to navigate from File Explorer or Datasets.

- **SharesPage had an NFS reload button**: The SMB Shares page contained a "Reload NFS" button that belongs on the NFS page. Removed.

- **"Destroy Pool" button sat next to "Expand Pool"**: Catastrophic irreversible action had identical visual weight to constructive actions. Moved to a clearly separated danger zone at the bottom of each pool card with a red top-border separator.

- **FTPPage showed a full config form when vsftpd was not installed**: The form silently failed to save. Now shows a clear `alert-warning` banner with NixOS installation instructions and disables the save/start buttons when `installed: false`.

- **SetupWizard "Finish Setup" button had no loading state**: The button remained enabled and unlabeled during the POST, enabling duplicate requests. Now disabled with "Finalising…" label during completion.

- **SetupWizard hostname had no format validation**: Hostname with spaces or uppercase letters would silently fail at the daemon. Validation added before proceeding.

- **LoginPage boot spinner had no text**: A full-page spinner with no explanation appeared while the daemon status was being checked. "Connecting to D-PlaneOS..." label added.

- **IPMIPage had no manual refresh button**: No way to force a refresh other than navigating away and back. Refresh button added.

- **Dashboard UPS status showed raw NUT codes**: "OL", "OB", "LB" etc. displayed directly. Mapped to human-readable labels ("Online", "On Battery", "Low Battery", etc.).

- **Dashboard disk temperature alert did not auto-clear**: The alert persisted after the disk cooled down until manually dismissed. Now auto-clears after 60 seconds.

- **S3Page had no MinIO console link**: `console_port` was fetched in the status response but no link was rendered. "Open MinIO Console" button added, shown when MinIO is running.

### Changed

- **Navigation completely restructured**: The 19-item Storage sidebar group is replaced with focused groups. Storage (7 items: core ZFS), Sharing (7 items: all file/block protocols), File Explorer (standalone leaf), Data Protection (4 items: snapshots/replication/backup/cloud-sync), Containers (formerly "Compute"; Docker + GitOps Engine). Removable Media moved from Network to System. Inotify Watches moved to sit after Reporting (both are observability tools). "Shares" renamed "SMB Shares" to clarify scope.

- **"Compute" nav group renamed "Containers"**: The group containing Docker and GitOps Engine was named "Compute" which implied server/VM workloads. Renamed to "Containers" which accurately describes its contents.

- **Docker Compose Templates page and backend deleted**: `ModulesPage.tsx`, `docker_templates.go`, and all `/api/docker/templates/*` routes removed. The Docker page's Compose Stacks tab already provides full compose deployment; the templates page was entirely redundant.

- **NixOS modules now imported centrally (`nixos/module.nix`)**: `nixos/modules/samba.nix`, `nixos/modules/nfs.nix`, and `nixos/modules/fenced.nix` are now imported in `module.nix` alongside the existing `ha.nix` import. Users and flake configurations no longer need to import these separately. The explicit `./nixos/modules/samba.nix` imports removed from `flake.nix` `dplaneos` and `dplaneos-arm` configurations.

- **SetupWizard done screen shows next-steps checklist**: The previous "go to dashboard" message gave no guidance. Replaced with a 4-item next-steps checklist (create dataset, create share, add users, configure static IP) with clickable links to each page.

- **SupportPage pre-upgrade snapshot list links to NixOS generations rollback**: A note added pointing operators to Settings > NixOS > Generations for snapshot rollback.

- **MonitoringPage title made consistent**: Page title changed from "System Monitoring" to "Inotify Watches" to match the sidebar label. Subtitle updated to explain what inotify watches are and why hitting the limit causes silent failures in file-watching applications.

- **SettingsPage license key field has explanatory hint**: Previously showed a bare input with no explanation. Hint added: activates optional add-ons, leave blank for the standard open-source build.

- **SSHKeysPage port change shows clear reconnection warning**: A `toast.warning` is shown after a successful port change, naming the new port and noting that connections on the old port will be refused after the next `nixos-rebuild switch`.

- **UPSPage shutdown_level field has unit hint**: Field now explains the value is a battery percentage (e.g. 20 for 20%).

- **NixOS base upgraded to 26.05**: `nixpkgs.url` bumped from `nixos-25.11` to `nixos-26.05`. Six evaluation errors exposed by the upgrade fixed:
  - `services.nfs.server.extraNfsdConfig` removed in 26.05 - replaced with `services.nfs.settings.nfsd` (INI attrset).
  - `environment.etc."idmapd.conf"` conflict - nixpkgs 26.05's NFS module now manages that file; use `services.nfs.idmapd.settings` instead.
  - `pkgs.nfs4-acl-tools` removed from nixpkgs after 25.05 - reference guarded with `lib.optionals (pkgs ? nfs4-acl-tools)`.
  - `services.winbind` is not a NixOS module in 26.05 - reference removed; winbind activates automatically via smbd when `security = ads`.
  - `lib.mkIf` used as inline values inside `services.samba.settings.global` - `settings` expects plain `str` values; replaced with `lib.optionalAttrs` at the attrset level.
  - `python3.11-3.11.15-doc` fails to build in nixpkgs 26.05 (Sphinx 9.1 + docutils 0.22.4 incompatibility). `documentation.doc.enable = false` only unlinks doc files, it does not prevent the derivation from being built. Fixed with a nixpkgs overlay that replaces `pkgs.python311.doc` with an empty stub, eliminating the broken build from the closure entirely.

- **`boot.zfs.forceImportRoot` warning silenced**: new deprecation warning in 26.05. Set explicitly to `false` in both `module.nix` and `installer.nix`.

- **CI: deleted `/api/docker/templates*` routes removed from integration test**: both endpoints 404'd after the Compose Templates page was removed in this release.

- **CI: HA NixOS modules test updated for default-enabled Samba and NFS**: ha-failover test VMs now explicitly set `samba.enable = false; nfs.enable = false;` since those services are not needed for the failover test but now default to enabled via `module.nix`.

- **CI: integration test log sections are expandable**: added `::group::` / `::endgroup::` annotations; moved `set +e` after the setup phase so the full failure list always prints.

---

## v12.4.0 (2026-06-03) - "Meridian"

Upgrade from: v12.3.0 - No schema migration required. No breaking API changes. No breaking configuration changes. Pure frontend and design system changes.

### Added

- **`.stitch/DESIGN.md` — design system source of truth (`app-react/.stitch/DESIGN.md`)**: Machine-readable specification synthesized from `index.css` and the component library. Documents every color token with semantic role, the full typography scale, the 4px spacing grid, border radius ladder, shadow system, z-index layers with their constraints, component patterns (card, button, alert, modal, badge, empty state, data table), glass morphism rules, chart color constants, the backdrop z-index pattern, and an explicit anti-pattern list. Reference this file before generating or editing any screen.

- **UI workflows for three operational scenarios (`app-react/src/pages/GitOpsPage.tsx`, `HAPage.tsx`, `PoolsPage.tsx`)**: Three operator-facing workflows for situations the system cannot handle automatically. (1) BLOCKED destructive approval: BLOCKED plan items now show a per-item Approve button; clicking opens a modal requiring a written reason (logged to the HMAC audit chain) before the deletion is permitted to proceed on Deploy. (2) HA physical triage panel: automatically surfaces when any peer enters the unreachable state, listing four physical checks (SAS cable, HBA, management network, BMC console) with specific diagnostic guidance and inline Fence & Promote / Enter Maintenance actions. (3) Scrub error banner: appears on each pool card when a completed scrub reports errors, showing the error count and linking to Hardware for disk identification and replacement.

- **GitOps plan API extended (`daemon/internal/handlers/gitops_handler.go`)**: `GET /api/gitops/plan` now includes `kind`, `name`, and `approved` on each change item so the frontend can call `POST /api/gitops/approve` directly without parsing the combined `resource` field.

### Fixed

- **Design system consistency audit — raw hex colors eliminated (15 files)**: All hardcoded hex colors (`#3b82f6`, `#080808`, `#ff4b2b`, `#a78bfa`, `#52525b`, etc.) and non-exempt specific-color `rgba()` values replaced with CSS variable equivalents across AppShell, TopBar, JobConsole, HardwarePage, NetworkPage, and ReportingPage. xterm.js `ITheme` hex values are intentionally unchanged (library API requires raw hex).

- **Design system consistency audit — raw z-index numbers eliminated (15 files)**: All raw `zIndex: N` inline values replaced with the CSS variable token set. Two new tokens added to `index.css`: `--z-topbar: 40` (fixed topbar and sidebar nav layer) and `--z-supreme: 9999` (force-password-change wall that must appear above everything including toasts). Affected: AppShell, PendingChangesSidebar, TopBar, Sidebar, GlobalSearch, KeyboardHelpModal, JobConsole, DashboardPage, DatasetsPage, DockerPage, FilesPage, FirewallPage, GitOpsPage.

- **ReportingPage chart colors tokenised**: `#8b5cf6`, `#06b6d4`, `#f59e0b` (used as SVG fill props across six call sites) extracted to named constants `C_ARC`, `C_LOAD`, `C_IOWAIT` using hsl values that match the design token palette, so chart colors are semantically tied to the design system even where CSS variables are not supported.

- **BlockedApprovalModal uses `<Modal>` component**: Replaced a hand-rolled `position:fixed` overlay (no focus trap, no Escape key, raw `zIndex: 1000`) with the shared `<Modal>` component (portal to `#modal-root`, focus trap, Tab cycling, Escape key, ARIA `role="dialog"`).

- **PoolsPage `<a href>` for internal navigation replaced**: `<a href="/hardware?pool=...">` replaced with `router.navigate({ to: '/hardware' })` via `useRouter` per design system navigation rules.

- **HAPage triage panel `rgba(0,0,0,0.15)` replaced**: Raw rgba replaced with `var(--surface)` per design token rules.

- **DashboardPage off-grid spacing corrected**: MetricCard padding `20px 22px` → `20px 24px`; MetricCard icon-row margin `14px` → `12px`; SectionCard header padding `18px 24px` → `16px 24px`; PoolRow item padding `10px 12px` → `12px`. All values now land on the 4px grid documented in `.stitch/DESIGN.md`.

- **DashboardPage empty states upgraded**: Three bare text placeholders ("No ZFS pools configured", "No running containers", "No disk data available") upgraded to icon+text structure following the design system empty state pattern.

---

## v12.3.0 (2026-06-03) - "Ironclad"

Upgrade from: v12.2.0 - No schema migration required. No breaking API changes. No breaking configuration changes. HA replication behaviour is unchanged; the SSH transport is now native Go rather than a subprocess.

### Fixed

- **HA replication: shell-on-remote command injection vector eliminated (`daemon/internal/ha/replication.go`, `daemon/internal/ha/reconcile.go`, `daemon/internal/ha/ssh_client.go`)**: All SSH operations in the HA replication and zombie-boot reconciliation paths previously used `exec.Command("ssh", ...)` which assembled a remote command string executed by the remote shell. This is the classic sanitize-then-interpolate pattern: even with validated inputs, the execution boundary is the remote shell's parser, not the Go validator. Two structural problems: (1) piped remote commands (`zfs list ... | tail -n 1`) required the remote shell to be active and interpret a pipeline operator, (2) `fmt.Sprintf` was used to build command strings from DB-sourced values. Both are now fully eliminated.

- **HA replication: native SSH client with TOFU host key verification (`daemon/internal/ha/ssh_client.go` new)**: All SSH operations now use `golang.org/x/crypto/ssh` directly. `openSSHClient` loads the private key file, verifies host keys against `/var/lib/dplaneos/ha_known_hosts` with accept-new semantics (first connection stores the fingerprint; subsequent connections verify it; fingerprint mismatch returns an explicit error naming both fingerprints), and connects via the SSH protocol exec channel without spawning any subprocess. The `knownhosts` sub-package is not required; host key verification is implemented using only the vendored `golang.org/x/crypto/ssh` primitives (`ssh.ParseAuthorizedKey`, `ssh.MarshalAuthorizedKey`, `ssh.FingerprintSHA256`). A package-level mutex serialises first-connection key writes to prevent races when two goroutines connect to the same new host simultaneously.

- **HA replication: piped remote commands replaced with Go string processing**: `zfs list -t snapshot ... | tail -n 1` is gone from all remote execution paths. The full snapshot list is retrieved via the SSH exec channel and the last non-empty line is extracted by `lastNonEmptyLine` in Go. No shell pipeline operator appears in any remote command.

- **HA replication: each argument single-quote-wrapped before remote execution**: `shellQuoteArgs` wraps every argument in single quotes before joining into the command string sent to the remote shell via the SSH exec channel. Since all arguments are pre-validated to contain only `[a-zA-Z0-9_\-\./@:]`, no single-quote character can appear inside a quoted token. The remote shell receives e.g. `'zfs' 'list' '-t' 'snapshot' '-r' 'tank'` rather than `zfs list -t snapshot -r tank`, providing a structural guarantee that word splitting and glob expansion cannot affect argument boundaries.

- **HA replication: remote snapshot names validated before subsequent exec calls**: Snapshot names returned from `zfs list` on the remote peer are now validated through `security.ValidateSnapshotName` before being used in any subsequent `exec.Command` or SSH session command. This closes the path where a compromised peer could return a malformed snapshot name that survives the initial validator but causes injection at the point of use.

- **CI: cron-hook integration test updated for per-boot token rotation (`.github/scripts/api-integration-test.sh`)**: The test previously sent the hardcoded literal `dplaneos-internal-reconciliation-secret-v1` and asserted it was accepted. Since v12.2.0 that token is correctly rejected (per-boot `crypto/rand` token). The test now asserts the hardcoded token is rejected (proving the rotation is active) and separately asserts the cron-hook endpoint is reachable via an authenticated admin session (proving the functionality works).

---

## v12.2.0 (2026-06-03) - "Bastion"

Upgrade from: v12.1.0 - Schema migration required (1 new table: `ha_cluster_secret`, created automatically at daemon startup via `ensureSchema`). No breaking API changes. No breaking configuration changes.

### Added

- **HA peer authentication (`GET|POST /api/ha/cluster-secret/configure`, `daemon/internal/ha/cluster.go`, `daemon/internal/handlers/ha_handler.go`)**: Pre-shared cluster secret that peer daemons must include in every heartbeat payload. When configured, `HandleHeartbeat` rejects any node that does not carry the correct secret, preventing any host on the management network from injecting itself as a cluster peer and influencing HA decisions. The secret is persisted in a new `ha_cluster_secret` table, loaded at startup (CLI flag `--ha-cluster-secret` takes precedence over the DB value), and applied to the running manager in-place on save with no restart required. The secret is write-only: `GET /api/ha/cluster-secret/configure` returns only `{ configured: bool }`. A startup warning is logged when no secret is set and the daemon is operating in unauthenticated peer mode.

- **HA cluster secret GUI (`app-react/src/pages/HAPage.tsx`)**: New `ClusterSecretForm` component in the HA Configuration section with current status indicator (Active/Not Set), masked input with show/hide toggle, one-click browser-side generation via `crypto.getRandomValues` (32 bytes encoded as 64-char hex), and save. Warning banner on the main dashboard when peers are registered but no secret is configured. New wizard Step 4 "Peer Authentication" inserted between peer registration (Step 3) and storage replication (now Step 5), with contextual explanation of the risk of skipping it. Wizard `TOTAL_STEPS` updated from 6 to 7.

- **`ValidateHostname`, `ValidateUnixUsername`, `ValidateAbsolutePath` (`daemon/internal/security/whitelist.go`)**: Three new input validators covering HA replication config fields. `ValidateHostname` accepts RFC 1123 hostname labels and IPv4/IPv6 addresses, rejecting shell metacharacters. `ValidateUnixUsername` enforces POSIX portable username format (lowercase, no shell metacharacters). `ValidateAbsolutePath` validates SSH key file paths for safe use as exec arguments.

- **HA replication config validation at save time (`daemon/internal/handlers/ha_handler.go`)**: `ConfigureHAReplication` now calls `ValidateReplicationConfig` before persisting to the database. Invalid pool names, hostnames, usernames, port numbers, or key paths are rejected at the API level with HTTP 400 rather than silently saved and discovered as a runtime error when replication runs.

- **SBD config validation at save time (`daemon/internal/handlers/ha_handler.go`)**: `ConfigureSBD` now validates pool and dataset names against `ValidatePoolName` and `ValidateDatasetName` before saving, preventing a bad name from being stored and silently failing every lease renewal cycle.

- **Replication config format hints (`app-react/src/pages/HAPage.tsx`)**: Helper text below the SSH replication fields documents the pool name character set, lowercase-only username requirement, and absolute-path constraint for the identity file.

### Fixed

- **HA: spurious STONITH fence against a live peer (`daemon/internal/ha/cluster.go`)**: `checkFailover` read a `deadPeer` snapshot under a read lock, dropped the lock, ran five guards, then re-acquired a write lock. A delayed heartbeat arriving between the unlock and re-lock could restore the peer to `StateHealthy` while the stale snapshot continued driving the fencing sequence, causing a spurious fence against an alive primary. Fixed: after acquiring the write lock, re-reads `m.nodes[deadPeer.ID]` and aborts if the peer is no longer `StateUnreachable`.

- **Audit HMAC chain race condition (`daemon/internal/audit/buffered_logger.go`)**: `Flush` (batch path) and `writeDirect` (security-event path) both read `prevHash` in independent transactions with no serialization. Under concurrent load they could read the same tail hash and produce a broken chain link, causing the verify-chain endpoint to flag all subsequent rows as invalid. Fixed: new `chainMu sync.Mutex` serializes the prevHash read-compute-insert-commit sequence across both callers.

- **Audit HMAC chain permanently broken on batch insert error (`daemon/internal/audit/buffered_logger.go`)**: On insert failure inside `Flush`, the code did `continue` and advanced `prevHash = rowHash`. All subsequent rows in the batch received a `prev_hash` pointing to a hash that was never committed, permanently breaking the chain for that batch with no error surfaced. Fixed: on insert error, return immediately so the deferred `tx.Rollback()` fires and `prevHash` is never advanced past a committed row.

- **YAML parser stack overflow on deeply nested `state.yaml` (`daemon/internal/gitops/state.go`)**: `parseMapping` and `parseSequence` are mutually recursive with no depth limit. A deeply nested or adversarial `state.yaml` would stack-overflow the daemon process. Fixed: `const maxYAMLDepth = 50`; both functions accept a `depth int` parameter and return a parse error when the limit is exceeded.

- **SBD `renewLease` pool/dataset injection (`daemon/internal/ha/sbd.go`)**: `cfg.Pool` and `cfg.Dataset` were concatenated and passed directly to `exec.Command("zfs", "set", ...)` without validation. A compromised database row could inject arbitrary arguments. Fixed: both fields now pass through `security.ValidatePoolName` and `security.ValidateDatasetName` before the exec call.

- **HA replication and reconcile exec calls bypassing the security allowlist (`daemon/internal/ha/replication.go`, `daemon/internal/ha/reconcile.go`)**: All `exec.Command` calls in the HA package passed DB-sourced pool names, hostnames, usernames, and SSH key paths directly without allowlist validation, inconsistent with the rest of the daemon. Fixed: `ValidateReplicationConfig` is called at the start of `syncZFS`, `executeSendRecv`, `catchUpFromPeer`, and `StartupReconciliation`. `localPoolTXG` validates pool names before use.

- **Hardcoded internal cron hook token exposed in systemd unit files (`daemon/cmd/dplaned/main.go`, `daemon/internal/handlers/system_extended.go`, `daemon/internal/handlers/backup_schedule.go`, `daemon/internal/hardware/smart.go`)**: The literal string `dplaneos-internal-reconciliation-secret-v1` was hardcoded in three timer-generator functions and written verbatim into systemd unit files on disk, allowing any local user who could read those files to call cron-hook endpoints without a session. Fixed: a 32-byte `crypto/rand` token is generated at each daemon startup, distributed to all three packages via `SetCronToken`, embedded into unit files only at timer-generation time, and validated in `sessionMiddleware` against the runtime value. The token rotates on every restart; existing unit files are invalidated when schedules are next saved.

- **Bond slave restoration errors silently discarded (`daemon/internal/reconciler/reconciler.go`)**: `LinkSetDown` and `LinkSetMaster` errors in `restoreBond` were discarded with `_`. A bond could come up missing a member with no log evidence, passing traffic through fewer links than configured. Fixed: errors are now logged with bond name and slave interface.

- **Default route restoration fire-and-forget (`daemon/internal/reconciler/reconciler.go`)**: `RouteReplace` for the default gateway was discarded with `_`. A node with a misconfigured gateway would have its static IP restored but no default route, appearing online to local traffic while being unreachable from outside its subnet. Fixed: error is now logged.

---

## v12.1.0 (2026-06-01) - "Integrity"

Upgrade from: v12.0.0 - No schema migration required. No breaking API changes.

### Added

- **Secrets key rotation (`POST /api/system/secrets/rotate`, `daemon/internal/handlers/secrets_rotation.go`, `daemon/internal/secrets/secrets.go`)**: New endpoint re-encrypts every stored secret under a freshly-generated AES-256-GCM key in a single atomic DB transaction before atomically replacing the key file at `/var/lib/dplaneos/secrets.key`. Surfaces rotated: `telegram_config.bot_token`, `ldap_config.bind_password`, `ad_domains.bind_password` (all rows), `git_credentials.token` and `git_credentials.ssh_key` (all rows), `oidc_config.client_secret`, `totp_secrets.secret` (all users), and `settings.smtp_config.password` (JSON field). The new key is generated in memory first; `secrets.PrepareRotation` returns `openOld`/`sealNew` closures and a `commit` func. `commit` is called only after the transaction commits - if the DB update rolls back, the old key stays active and no secrets are stranded. Use via Settings > Security > Rotate Encryption Key.

- **Control-plane database backup and restore (`GET /api/system/db/backup`, `POST /api/system/db/restore`, `daemon/internal/handlers/system_backup.go`)**: Backup streams a `pg_dump --format=custom` of the control-plane database directly as an HTTP attachment (`dplaneos-backup-YYYYMMDD-HHMMSS.dump`). Restore accepts a multipart upload in field `backup`, writes to a temp file, and runs `pg_restore --clean --if-exists`. ZFS pool state is not included; it is managed by `state.yaml`. Use via Settings > Maintenance.

- **HA promotion epoch check (`daemon/cmd/dplaned/main.go`)**: `checkPromotionStateEpoch` runs at the start of every post-promotion stacks apply. It queries `git_sync_repos` to identify the repo whose `local_path` contains `state.yaml`, reads the `last_commit` recorded by the last successful auto-sync, and compares it against the actual local `git rev-parse HEAD`. If they differ, a `HA PROMOTION WARNING` is logged and a `stale_state_warning` audit event is written with both commit hashes and the state file path. Promotion and stacks apply proceed either way (fail-open design documented in `docs/admin/HIGH-AVAILABILITY.md`).

- **Release artifact signing and SBOM (`.github/workflows/ci.yml`)**: The release job now installs `cosign` and `syft` after building the tarballs. Each tarball is signed with `cosign sign-blob --yes` (keyless Sigstore/Fulcio via GitHub Actions OIDC token); the resulting `.bundle` files contain the transparency log entry and signature. Each tarball also receives an SPDX JSON SBOM generated by `syft`. Both `.bundle` and `.sbom.json` files are attached to every GitHub release alongside the existing tarballs and SHA256 checksums.

- **Settings > Security tab (`app-react/src/pages/SettingsPage.tsx`)**: New tab in System Settings with a card explaining the encryption key location and rotation behavior, plus a two-step confirm flow (click Rotate, confirm, done) that calls `POST /api/system/secrets/rotate` and toasts the count of re-encrypted secrets on success.

- **Settings > Maintenance tab (`app-react/src/pages/SettingsPage.tsx`)**: New tab in System Settings with a "Download Backup" button (triggers `GET /api/system/db/backup` as a file download) and a "Restore from File" file picker that posts the selected `.dump` to `POST /api/system/db/restore`.

### Fixed

- **`POST /api/alerts/smtp/test` always failed (`daemon/internal/handlers/alerting_smtp.go`)**: The handler was a standalone function with no database access. It decoded the request body into an `SMTPConfig`, but the frontend sent an empty `{}`, so `Host`, `Port`, `From`, and `To` were all zero-valued and every test attempt failed with a connection error. Fixed: `TestSMTP` is now a method on `AlertingHandler`, loads the saved SMTP config from the database, decrypts the stored password, and sends the test email using the stored configuration. The frontend continues to send `{}`.

## v12.0.0 (2026-06-01) - "Sovereign"

Upgrade from: v11.6.5 - Schema migration required (4 new tables). No breaking configuration changes. OIDC is disabled by default; existing local and LDAP authentication is unaffected.

### Added

- **OIDC SSO: full Authorization Code + PKCE login flow (`daemon/internal/oidc/`, `daemon/internal/handlers/oidc.go`, `daemon/internal/database/migrations/00004_oidc.sql`, `daemon/cmd/dplaned/main.go`)**: DPlaneOS can now act as an OIDC relying party against any standards-compliant provider: Keycloak, Authentik, Dex, Microsoft Entra ID, Google Workspace, and others. Full implementation including: cryptographic token verification package (`internal/oidc`) with configurable allowed algorithms, strict OIDC Core 3.1.3.7 claims validation (issuer, audience, `azp`, expiry, `iat`, nonce, subject), and a JWKS cache with a 15-minute TTL and flood-protection on unknown-kid refreshes. The provider discovery document is cached for 1 hour and re-fetched only when the issuer URL changes. Six HTTP endpoints: `GET /api/auth/oidc/info` (public, login page SSO button), `GET /api/auth/oidc/start` (public, PKCE challenge generation and IdP redirect), `GET /api/auth/oidc/callback` (public, code exchange and session mint), `POST /api/auth/oidc/exchange` (public, SPA session pickup), `GET /api/auth/oidc/config` (protected system:admin), `POST /api/auth/oidc/config` (protected system:admin). Four new DB tables: `oidc_config` (singleton, mirrors `ldap_config` pattern), `oidc_identities` (stable `issuer+subject` to `user_id` mapping), `oidc_state` (10-minute PKCE/nonce rows, `DELETE...RETURNING` replay prevention), `oidc_handoff` (2-minute one-time codes for session pickup, same atomic pattern). All four endpoints in the login flow are allowlisted in `sessionMiddleware`. Hourly background goroutine purges expired `oidc_state` and `oidc_handoff` rows.

- **OIDC SSO: user provisioning and group-to-role mapping**: Three-step user resolution at login: (1) exact `(issuer, subject)` identity match, (2) email-based link to an existing local account (creates an `oidc_identities` row so future logins are stable), (3) auto-provision a new account with `source='oidc'` when `auto_provision` is enabled. Auto-provisioned usernames are derived from `preferred_username`, then email local-part, then subject; unsafe characters replaced with `_`; numeric suffix on collision. IdP group names (configurable claim, default `groups`) are matched against `roles.name` and assigned via `INSERT INTO user_roles ... ON CONFLICT DO NOTHING` with `granted_by='oidc-provider'`. The `default_role_id` option assigns a baseline role to all newly provisioned accounts.

- **OIDC SSO: handoff code session pickup pattern**: The session token cannot safely travel in the IdP redirect URL (browser history and access-log exposure). Instead, the `Callback` handler stores the minted `session_id` in a `oidc_handoff` row keyed by a 64-hex-character one-time code, then redirects to `/login?oidc_handoff=<code>`. The SPA `LoginPage` detects the parameter on mount, immediately removes it from the URL via `window.history.replaceState`, calls `POST /api/auth/oidc/exchange`, and stores the returned session. The `DELETE...RETURNING` exchange is atomic; a second use within the 2-minute window returns 401.

- **OIDC SSO: login page integration (`app-react/src/pages/LoginPage.tsx`)**: Login page fetches `/api/auth/oidc/info` on mount and conditionally renders an SSO button with the configured label below the Sign In button, separated by an "or" divider. Button is hidden when OIDC is disabled or not configured. Handles `?oidc_handoff=` URL parameter for session pickup (see above). Handles `?error=` parameters from the callback redirect and maps eight OIDC error codes to human-readable messages.

- **OIDC SSO: admin config panel (`app-react/src/pages/SettingsPage.tsx`)**: New "SSO / OIDC" tab in System Settings with a complete configuration form: issuer URL (with discovery hint), client ID, client secret (masked input, empty value preserves existing secret to prevent accidental clearing), button label, enable toggle; advanced section (collapsible) for scopes, allowed algorithms, and group claim; provisioning card with auto-provision toggle, default role selector (populated from `/api/rbac/roles`), and explanatory hint text.

- **HA: promotion callback and post-promotion stacks apply (`daemon/internal/ha/cluster.go`, `daemon/cmd/dplaned/main.go`)**: `Manager.SetPromotionCallback(fn)` registers a function invoked on the failover goroutine immediately after a successful STONITH promotion. At startup, `main.go` registers `runPostPromotionStacksApply`, which reads `state.yaml`, computes a GitOps diff, filters it to stack-only `Create`/`Modify`/`NOP` items (destructive and blocked items are excluded), acquires the reconcile lock, and applies the plan with up to 3 retries at 15-second intervals. The `data-not-ready` halt reason (ZFS datasets not yet mounted on the newly-promoted node) triggers a retry rather than failure. This closes the gap where container stacks running on the failed primary did not restart automatically on the promoted standby.

- **Secrets at rest: AES-256-GCM envelope encryption for all service credentials (`daemon/internal/secrets/secrets.go`)**: New `internal/secrets` package provides `Init(keyPath)`, `Seal(plaintext)`, and `Open(ciphertext)` backed by AES-256-GCM. A 32-byte key is auto-generated at `/var/lib/dplaneos/secrets.key` on first startup (mode 0600, root-only), following the same pattern as the existing audit HMAC key. All six service-credential fields that were previously stored as plaintext are now encrypted at rest: `ldap_config.bind_password`, `ad_domains.bind_password`, `telegram_config.bot_token`, `git_credentials.token`, `git_credentials.ssh_key`, `oidc_config.client_secret`, `totp_secrets.secret` (TOTP shared secret), and `settings.smtp_config.password` (stored inside the JSON blob). Each `Seal` call uses a fresh random 12-byte nonce; the output is `base64(nonce || ciphertext || GCM-tag)`. Empty strings pass through as empty (no-op) to handle optional fields. Decryption happens transparently in each handler and in `gitops.BuildPushEnvForCred` before credentials are written to temp files for git operations. Startup is fatal if the key file is unreadable or has the wrong length.

### Fixed

- **OIDC: Callback handler did not check `enabled` flag after consuming state row (`daemon/internal/handlers/oidc.go`)**: If OIDC was disabled between a user initiating the flow (`/api/auth/oidc/start`) and the IdP redirect arriving at `/api/auth/oidc/callback`, the callback continued to mint a session using the stale `oidc_state` row. The `Start` handler correctly rejected unauthenticated requests when OIDC was disabled, but `Callback` did not re-check the flag after loading config. Fixed: after `loadConfig`, `Callback` now checks `cfg.Enabled` and redirects to `/login?error=oidc_internal` if OIDC has been disabled since the flow was initiated.

### Changed

- **`GET /api/alerts/telegram` (and `GET /api/settings/telegram`) response shape**: The `bot_token` field is no longer returned. The response now contains `has_token: bool` (true if a bot token is stored) alongside `chat_id` and `enabled`. Frontend forms should show a placeholder indicating the token is saved and accept a new value only when the user explicitly types one; an empty `bot_token` on save preserves the existing token rather than clearing it.

- **`POST /api/alerts/telegram/test` behavior**: If `bot_token` is omitted or empty in the request body the handler now loads and decrypts the stored token automatically, eliminating the need for the frontend to round-trip the plaintext token through the browser.

- **`POST /api/alerts/smtp` behavior**: If `password` is empty or the sentinel `"***"` the existing stored password is preserved rather than overwritten. This fixes a pre-existing silent bug where opening the SMTP panel and saving without re-entering the password would replace the stored password with `"***"`.

- **Dependency count: 8 direct Go dependencies → 9**: `github.com/go-jose/go-jose/v4` promoted from indirect to direct. All dependencies remain vendored.

- **`DEPENDENCIES.md`**: All vendored Go packages now listed (previously omitted: `go-jose/v4`, `go-acme/lego`, `pressly/goose`, `creack/pty`, `miekg/dns`, `cenkalti/backoff`, `sethvargo/go-retry`, `go.uber.org/multierr`, `golang.org/x/net`, `golang.org/x/sys`, `golang.org/x/text`, and transitive deps).

- **Documentation**: `ADMIN-GUIDE.md` - new SSO (OIDC) section with setup steps, IdP registration requirements, group mapping table, provisioning priority order, login flow walkthrough, API reference, and troubleshooting table. `ARCHITECTURE.md` - OIDC flow and handoff code pattern added to Authentication and Session Model section. `THREAT-MODEL.md` - new T10a (OIDC Client Secret Exposure), updated T2 to cover OIDC public endpoint allowlist and handoff replay prevention, OIDC client secret added to Assets table, attack surface summary updated. `CODEBASE-DIAGRAM.md` - OIDC SSO sequence diagram added alongside existing local/LDAP flow. `README.md` - Identity feature row updated, auth row updated, dependency count updated, go-jose added to Acknowledgements.

---

## v11.6.5 (2026-05-26) - "Docker Compose Editor Fix"

Upgrade from: v11.6.4 - Drop-in. No schema changes. No configuration changes.

### Fixed

- **Docker: Compose create flow missing `.env` editor and not sending env on deploy (`app-react/src/pages/DockerPage.tsx`)**: The `ComposeManager` new-stack panel (`isCreating` state) rendered only a `docker-compose.yml` textarea with no `.env` editor, and the `deployNew` mutation sent `{ name, yaml }` to `POST /api/docker/stacks/deploy` without the `env` field. The backend `DeployStack` handler has always accepted and written a `.env` file when `env` is provided - the omission was purely in the frontend. Users creating a new stack had no way to supply environment variables at deploy time. Fixed: added a `.env` textarea to the create flow (matching the layout of the existing stack edit panel), and updated `deployNew` to include `env` in the request body.

- **Docker: Compose editor blank immediately after deploying a new stack (`app-react/src/pages/DockerPage.tsx`)**: After `deployNew` succeeded, `selectStack(name)` was called immediately after `qc.invalidateQueries({ queryKey: ['docker', 'stacks'] })`. `invalidateQueries` marks the query stale and triggers a background refetch but does not await it. At the moment `selectStack` ran, the stacks list still held pre-deploy data, `stacks.find(s => s.name === selected)` returned `undefined`, the `selected && !isCreating && selectedInfo` render condition evaluated false, and the editor panel did not render - leaving the user on the empty "Select a stack to edit" state until the refetch completed on its own. Fixed: replaced `invalidateQueries` with `qc.refetchQueries(...).then(() => selectStack(name))` so `selectStack` is only called after the stacks list has been updated and `selectedInfo` is guaranteed to resolve.

- **NixOS: `dplaneos-patroni-init` fails to write config because it runs as `postgres` (`nixos/ha.nix`)**: The oneshot service ran as `User = "postgres"` but writes its output to `/etc/dplaneos/`, which is owned by root. The write always failed with a permission error and Patroni never received a valid configuration file, preventing HA cluster bring-up. Fixed: the service now runs as root; after writing the config file it immediately `chown postgres:postgres` the file so Patroni (which runs as `postgres`) can read it.

- **NixOS: ZFS event notifications never reach the daemon because `nc` is not in the system closure (`nixos/module.nix`)**: The ZED (ZFS Event Daemon) notify hook used a bare `nc` invocation to forward pool events to the local daemon socket. `nc` is not included in the NixOS system closure by default, so the hook process exited with "command not found" on every event and no pool health notifications were delivered. Fixed: replaced `nc` with `${pkgs.socat}/bin/socat -t2`, using the full Nix store path so the binary is always present regardless of PATH.

- **NixOS: Dead `PGPASSFILE` environment variable in dplaned service (`nixos/module.nix`)**: `PGPASSFILE` was set in the `dplaned` systemd service environment pointing to a `.pgpass` file. Patroni bootstrap uses trust authentication on localhost, so the file is never created and the environment variable was dead weight - a stale artifact from an earlier authentication scheme. Removed.

- **NixOS: Dead inline ISO builder in flake and stale standalone configuration removed (`flake.nix`, `nixos/configuration.nix`)**: `flake.nix` contained an inline ISO builder expression that set `services.dplaneos.*` options without importing `nixos/module.nix`, meaning those options were undefined and the expression never compiled correctly. `nixos/configuration.nix` was a stale v5.3.2 standalone config that referenced a nonexistent `dplaneos-recovery` package. Both were dead code with no callers; removed.

- **NixOS: `frontendPackage` module option has no default; HA failover test now supplies an explicit derivation (`nixos/module.nix`, `nixos/tests/ha-failover.nix`)**: `frontendPackage` is a required option - it must be set to the built React app for production deployments. A temporary default (`pkgs.runCommand "dplaneos-frontend-empty" {} "mkdir $out"`) was added then immediately reverted because a silent empty-directory default masks misconfigured deployments. The option is now explicitly required with no default. The `ha-failover.nix` NixOS test (which does not need a real frontend) was updated to supply its own `pkgs.runCommand "dplaneos-frontend-test" {} "mkdir $out"` derivation explicitly.

## v11.6.4 (2026-05-24) - "OTA Health Check Hardening"

Upgrade from: v11.6.3 - Drop-in. No schema changes. No configuration changes.

### Fixed

- **OTA health check commits update when ZFS command fails (`nixos/ota-update.sh`)**: The ZFS pool health check used `zpool list ... 2>/dev/null | awk ... || echo ""`. If `zpool list` failed for any reason (module not loaded, driver issue, command not found), the pipeline produced an empty string, `degraded_pools=""` evaluated as "no degraded pools", and the health check passed. A catastrophically broken ZFS subsystem after an OTA slot swap would be silently committed rather than triggering a revert to the previous slot. Fixed: `zpool list` exit code is now captured explicitly; a non-zero exit is logged as a FAIL and increments `checks_failed`, guaranteeing auto-revert.
- **OTA health check skips smbd gate when database is unreachable (`nixos/ota-update.sh`)**: The Samba service check queried PostgreSQL to count active shares, with `|| echo "0"` as the fallback. If `psql` could not reach the database, `share_count` became "0" and the smbd check was silently skipped. A broken PostgreSQL after an OTA (detectable state) would cause this check to report "SKIP: no active shares" and count as neither pass nor fail - potentially allowing the OTA to be committed with a dead database. Fixed: psql failure or empty result now increments `checks_failed` directly.

---

## v11.6.3 (2026-05-24) - "Security & Error Propagation"

Upgrade from: v11.6.2 - Drop-in. No schema changes. No configuration changes.

### Fixed

- **Security: RBAC hierarchy enforcement silently bypassed on DB read failure (`users_groups.go`)**: `ManageUser` fetched the target user's role and ID via a bare `QueryRow().Scan()` with no error check. If the scan failed (e.g., transient DB error, row not found), `targetRole` and `targetID` stayed at their zero values (`""` and `0`). The admin-protection check (`targetRole == "admin"`) then evaluated false, and the hierarchy check used `roleRank[""] = 0`, which is never larger than any valid rank, so the block was also skipped. A non-admin user sending a valid request during a transient DB error could modify or delete an admin account. Fixed: added error check with HTTP 500 response and early return before any authorization logic runs on the fetched values.
- **Security: Last-admin deactivation guard silently bypassed on DB read failure (`users_groups.go`)**: The guard that prevents deactivating the final active admin account used two consecutive bare `QueryRow().Scan()` calls - one for the target user's role and one for the admin count. If either scan failed, the role check (`currentRole == "admin"`) evaluated false and the guard was bypassed, allowing all admin accounts to be deactivated and locking every operator out. Fixed: both scans now check errors and return HTTP 500 on failure.
- **First-boot setup gate silent failure (`system_status.go`)**: The endpoint that configures initial admin credentials used a bare `tx.QueryRow().Scan()` to check whether setup was already complete. Scan failure left `setupDone = 0`, so the gate evaluated "not complete" and allowed re-running setup. A second bare scan for admin count used the same pattern, with the same silent-zero-value problem. Fixed: both scans now return HTTP 500 on failure before any credential changes are attempted.
- **NixOS backup-config crashes with empty git identity on scan failure (`nixos_guard.go`)**: `BackupConfig` fetched the repo's URL and branch via a bare scan; failure left both as empty strings, causing `gitops.EnsureRepoRootDir` to fail with an opaque "failed to initialize Git" message rather than a clear DB error. A second bare scan for commit name and email silently produced anonymous commits. Fixed: repo URL/branch scan now returns HTTP 500 on failure; identity scan logs the error and continues (empty identity is valid for some git configurations).
- **Audit log silent on pre-delete query failure (`api_tokens.go`, `git_sync_repos.go`, `nfs_handler.go`)**: Three delete handlers read a display name or path for the audit log using bare `QueryRow().Scan()` calls before performing the actual delete. Scan failure left the variable at `""`, so the audit entry showed an empty name/path with no indication of the lookup failure. Fixed: added `log.Printf(WARN)` on scan error so the failure is visible in the daemon log without blocking the delete.

---

## v11.6.2 (2026-05-24) - "Error Handling & Reliability"

Upgrade from: v11.6.1 - Drop-in. No schema changes. No configuration changes.

### Fixed

- **Silent database write failures across all handler files**: Every `db.Exec` and `tx.Exec` call that previously discarded its error return is now checked. Affected paths: `system_status.go` (DDL provisioning, setup_complete INSERT, hostname and timezone inserts, settings upsert loop, advisory lock), `auth.go` (session revocation on password change), `kerberos.go` (domain renewal status update), `users_groups.go` (ResetUserPassword session revocation), `api_tokens.go`, `ldap.go` (LeaveDomain goroutine), `shares_crud.go` (UpdateSMBSettings settings loop now returns HTTP 500 on failure; previously silently succeeded), `git_sync.go` (SaveConfig, Pull, Push, AutoSync status updates), `git_sync_repos.go` (SaveCredential UPDATE paths, DeleteCredential, DeleteRepo, SaveRepo, PullRepo, PushRepo, autoSyncOne status updates), `gitops_handler.go` (approval persist). Previously, failures in these calls were silently discarded and the client received `{"success": true}` regardless.
- **Missing `rows.Err()` checks after every iteration loop**: All `for rows.Next()` loops now check `rows.Err()` after iteration to detect mid-stream database errors that cut a result set short. Previously, a DB error mid-iteration would silently return a truncated list with no indication of failure. Affected files: `alerting_webhook.go` (list and dispatch loops), `audit_verify.go` (chain verification - now returns HTTP 500 on incomplete read rather than a potentially false "chain intact"), `cold_tier.go` (background remount loop), `enterprise_hardening.go` (audit log list), `hardware_smart.go` (SMART schedules list), `git_sync_repos.go` (auto-sync loop), `ldap.go` (group mappings list, domain list, IDMAP sync helper), `nixos_guard.go` (pre-upgrade snapshots list), `shares_crud.go` (SMB config regeneration - aborts config write on rows error to prevent writing a partial smb.conf), `support_bundle.go` (audit log bundle collection), `nfs_handler.go` (exports list).
- **Unchecked `rows.Scan` in iteration loops**: All scan calls inside `for rows.Next()` loops now check their error return and log or skip on failure. Previously, a failed scan silently left variables at zero values, which in `regenerateSMBConf` would write corrupted share sections into smb.conf.
- **Security: session revocation failures now logged on password change and admin password reset**: If the `DELETE FROM sessions` call fails after a password change (`auth.go`) or admin-forced password reset (`users_groups.go`), the failure is now logged. Previously a silent failure left the old sessions active.
- **Frontend: NixOS reconciliation errors silently dropped in Pending Changes sidebar**: `PendingChangesSidebar.tsx` called `reconcileM.mutate()` with no `onError` handler. If a `nixos-rebuild switch` failed, the sidebar closed and showed no error. Now surfaces the daemon error message via `toast.error`.
- **Frontend: Network diagnostics auth broken for "remember me" sessions**: `NetworkPage.tsx` read `sessionStorage.getItem('session_id')` directly instead of using the `getSessionId()` helper. Sessions stored in `localStorage` (the "remember me" path) were invisible to this call, sending an empty `X-Session-ID` header and receiving a 401 with no user-visible feedback. Fixed to use `getSessionId()` and `getUsername()`. Also removed a redundant live fetch to `/api/csrf` - the CSRF token is already initialised at app boot via `getCsrfToken()`.
- **NixOS: `dplaneos-sbd-init` silently ignores `zfs create` failures (`nixos/ha.nix`)**: The `dplaneos-sbd-init` oneshot script had no `set -eu`. If `zfs create` failed (pool offline, permission denied, dataset name conflict), the script exited 0, `RemainAfterExit` marked the service active, and `dplaned.service` started normally. The SBD lease dataset - required for ZFS-property fencing and split-brain protection - was never actually created, but all downstream services believed it was. Fixed: added `set -eu` as the first line of the script body so any ZFS error propagates as a service failure and halts the boot sequence.
- **NixOS: PostgreSQL readiness gate is a no-op (`install/scripts/init-database-with-lock.sh`)**: The DB connectivity check in `init_database()` tested whether `pg_isready` exists on `$PATH` but never called it. Every run printed "PostgreSQL check enabled" and proceeded regardless of whether PostgreSQL was reachable. Fixed: the check now calls `pg_isready "$DB_DSN" --timeout=10` and exits non-zero if the probe fails.
- **Install: admin password containing a single quote silently breaks bcrypt hash (`nixos/install.sh`)**: The `progress_step` command used `<<< '$ADMIN_PASS'` to feed the password to Python via stdin. When `$ADMIN_PASS` contained a `'` character, the shell heredoc terminated early inside the surrounding `bash -c "..."` double-quoted string, producing a broken or empty hash written to `.first-boot-password`. First login then failed with a credentials error and no way to recover without re-running the installer. Fixed: the password is written to a `chmod 600` temp file before `progress_step` runs; Python reads from the file path via `sys.argv[1]` so no shell quoting is involved; the temp file is removed after the step completes.

---

## v11.6.1 (2026-05-23) - "UI Polish"

Upgrade from: v11.6.0 - Drop-in. No schema changes. No configuration changes.

### Fixed
- **Undefined CSS token `var(--text-primary)` in five page components**: The token was never defined in the design system. References in `GitOpsPage.tsx` (branch dropdown), `HAPage.tsx` (connectivity test URL display), `NVMeOFPage.tsx` (two section headings), and `PoolsPage.tsx` (disk wipe confirmation label) all replaced with the correct `var(--text)`.
- **Missing root tokens `--text-3xs` and `--bg-hover`**: Both were referenced in component styles but absent from the `:root` block. Added: `--text-3xs: 9px` and `--bg-hover: var(--surface-hover)`.
- **Broken `.modal-close:hover` token references**: Used `var(--bg-hover)` (undefined) and `var(--text-primary)` (undefined); fixed to `var(--surface)` and `var(--text)` respectively.
- **Password visibility toggle used emoji instead of icon**: Replaced literal unicode checkmark with `<Icon name="visibility" / "visibility_off">` from the Material Symbols set for consistent rendering.

### Changed
- **Primary color saturation reduced below 80%**: Dark theme: 100% -> 60%; light theme: 80% -> 55%. Prevents oversaturation across accent-colored interactive elements.
- **Outer glows removed from interactive elements**: Removed `box-shadow` neon glows from `.btn-primary`, `.btn-primary:hover`, `.btn-danger:hover`, sidebar logo, active nav icons, and notification dot. Replaced with neutral drop shadows or no shadow where none is needed.
- **`text-shadow` glow removed from active tab underline**: `.tab-underline.active` no longer uses `text-shadow: 0 0 12px var(--primary-glow)`.
- **`background-attachment: fixed` removed from root gradient**: Caused rendering artifacts on iOS Safari (full-viewport jumps on scroll). Root background now uses standard attachment.
- **Inter removed from font fallback stack**: `'Outfit', 'Inter', system-ui` -> `'Outfit', system-ui`. Inter is banned per the design system.
- **Glow pulse animation opacity softened**: `glow-pulse-green` max opacity 0.6 -> 0.4; `glow-pulse-blue` 0.6 -> 0.3.
- **TopBar user chip simplified**: Replaced full pill (avatar, username, role badge) with avatar-only circle wrapped in a tooltip showing username and role. Reduces header clutter.
- **PoolMonitor moved to left side of TopBar**: Previously centered with wide margins; now positioned immediately after the breadcrumb with a vertical separator, consistent with left-to-right information hierarchy.
- **ZFS events sidebar widget**: Raw event strings replaced with parsed icon + label rows. Eight event types recognized (scrub complete/started, resilver done/progress, pool import/export, fault detected, TRIM event) with appropriate Material Symbol icons and semantic colors.
- **Page icon border desaturated**: TopBar page icon border changed from a saturated primary-hue HSL value to `var(--border)`.

---

## v11.6.0 (2026-05-22) - "Infrastructure"

Upgrade from: v11.5.0 - Drop-in. No schema changes. No configuration changes.

### Fixed
- **CGO daemon build compatibility with OpenZFS 2.3.7 (`daemon/internal/libzfs/zfs_cgo.go`)**: Fixed five breaking API changes between the original C bindings and OpenZFS 2.3.7 as shipped in nixpkgs 25.11. `nvlist_lookup_string` output pointer changed to `const char **`; `zpool_clear` gained a third (rewind policy nvlist) argument; `zfs_hold` and `zfs_release` each gained a snapshot-name argument. The bulk pool import path (`PoolImportAll`) was rewritten to use `zpool import -a -f -d /dev/disk/by-id` via the subprocess allowlist, replacing C API calls removed from the public `libzfs.h` in 2.3.7 (`importargs_t`, `zpool_search_import`, `zpool_find_import`). Header discovery was also fixed: added `#cgo pkg-config: libzfs` directive and explicit `PKG_CONFIG_PATH` in the Nix preBuild hook, since CGO bypasses the GCC wrapper and does not see `NIX_CFLAGS_COMPILE`.
- **OTA auto-revert after every successful update (`nixos/ota-update.sh`)**: The post-boot health check was hitting `/api/system/info`, which is not a registered route. curl's `-f` flag treats 404 as failure, so the daemon check always failed, always triggering an auto-revert. Fixed: endpoint changed to `/api/system/health`, which is registered and explicitly whitelisted as public in `sessionMiddleware`.
- **HA ZFS import guard infinite loop (`nixos/ha.nix`)**: The `preStart` scripts for `zfs-import-cache` and `zfs-import-scan` used a `while true` loop that only exited on Patroni HTTP 200 (primary) or 503 (standby). Any other response (connection refused, timeout, 5xx) looped forever with no escape. If Patroni was slow to start or returned an unexpected status, ZFS pools never mounted and the node was unrecoverable without manual intervention. Fixed: added a 120-second deadline with fail-safe exit on timeout; also added `--max-time 3` to each curl poll.
- **HA cluster firewall ports not opened (`nixos/ha.nix`)**: The HA module bound etcd on `0.0.0.0:2379` (client) and `0.0.0.0:2380` (raft peer) and relied on Patroni REST API port 8008 for HAProxy health checks, but never added these ports to the firewall. The base `module.nix` only opens 80 and 443. Without these rules, etcd cannot elect a leader, Patroni cannot manage failover, and HAProxy routes to the wrong node. Fixed: added `allowedTCPPorts = [2379 2380 5432 8008]` in the HA module.
- **Network config lost after OTA slot swap (`nixos/impermanence.nix`)**: `networkdwriter` writes `50-dplane-*.{network,netdev}` to `/etc/systemd/network/`. The root ext4 partition is replaced on every OTA update, so these files were lost after each OTA and network reverted to NixOS defaults on the next reboot. Fixed: added `/etc/systemd/network` to the impermanence persistence list.
- **etcd cluster state lost after OTA slot swap (`nixos/impermanence.nix`)**: etcd's WAL and snap data in `/var/lib/etcd` were on the ephemeral root. After an OTA update on any node, etcd started with an empty data directory. With `initialClusterState = "new"`, the node attempted to bootstrap a new cluster while its peers still ran the existing one. Fixed: added `/var/lib/etcd` (mode 0700, user/group etcd) to the impermanence persistence list.
- **OTA signing key never injectable (`nixos/ota-module.nix`)**: `OTA_PUBLIC_KEY` defaulted to a placeholder literal with no mechanism to configure a real key. Every `dplaneos-ota-update` invocation failed signature verification. Added `services.dplaneos.ota.signingKey` option (type: str, default: ""); when non-empty, injected via `--set OTA_PUBLIC_KEY` in `wrapProgram` so the installed binary carries the correct key at build time.
- **Patroni config never provisioned (`nixos/ha.nix`)**: The Patroni service referenced `/etc/patroni/patroni.yml` with no automated provisioning. Enabling HA via the UI started Patroni which immediately failed with a missing config file. Added `dplaneos-patroni-init` oneshot that generates `/etc/dplaneos/patroni.yaml` on first boot with random replication and superuser passwords. Config lives in `/etc/dplaneos/` (persisted via impermanence) and survives OTA slot swaps. Patroni `ExecStart` updated to the new path; `requires` gates on the init service completing.

### Changed
- **Production daemon now uses libzfs CGO path (`flake.nix`)**: Both NAS `nixosConfigurations` (`dplaneos` and `dplaneos-arm`) now use `mkDaemonCGO` (CGO_ENABLED=1, build tag `libzfs`) instead of `mkDaemon` (static musl). The `zfs_cgo.go` bindings now compile and link against the system libzfs, replacing the subprocess fallback (`zfs_fallback.go`) in production. The ISO installer retains the static musl build for portability.

---

## v11.5.0 (2026-05-21) - "Auth"

Upgrade from: v11.4.0 - Drop-in. No schema changes. No configuration changes.

### Added
- **Admin reset-password endpoint (`POST /api/users/{id}/reset-password`)**: Requires `users:write` permission. Sets a temporary password (full strength validation enforced), sets `must_change_password = 1`, and revokes all sessions for the target user. Rejects LDAP accounts with a clear error. Frontend: reset-password button (lock icon, warning color) added to each user row in UsersPage, opens `ResetPasswordModal` with inline advisory note about session revocation.
- **"Remember me" on login**: Checkbox on the credentials form. When checked, session token is stored in `localStorage` instead of `sessionStorage`, so the session persists across browser restarts within the 24-hour server-side TTL. `getSessionId()` and `getUsername()` check `localStorage` first so new tabs also pick up the persistent session.

### Fixed
- **Server-side `must_change_password` enforcement**: The session middleware now queries `must_change_password` for every session-authenticated request. When the flag is set, only `/api/auth/change-password`, `/api/auth/logout`, and `/api/auth/session` are permitted; all other routes return HTTP 403 with `{"success":false,"error":"Password change required"}`. Previously the blocking was client-side only (AppShell overlay) and could be bypassed with raw API calls.
- **Other sessions not revoked on password change**: `ChangePassword` handler now runs `DELETE FROM sessions WHERE username = $1 AND session_id != $2` after updating the password hash, revoking all sessions except the one that performed the change. Previously all sessions remained valid after a password change.
- **LDAP users had no feedback on password tab**: `PasswordTab` in `SecurityPage.tsx` now reads `user.source` from the session store. When `source === 'ldap'`, a notice banner explains that passwords are managed by the directory server and the form is visually disabled (`opacity: 0.4; pointer-events: none`).
- **Session endpoint did not expose `source` field**: `GET /api/auth/session` now returns `source` ('local' or 'ldap') alongside the existing user fields. `SessionUser` interface and mock data updated.
- **Setup wizard partial-state recovery**: `StepAdmin` (Step 1) in `SetupWizardPage.tsx` now handles "already exists" / "conflict" errors from `POST /api/system/setup-admin` by automatically advancing to the disk selection step. This recovers gracefully when the admin account was created in a previous wizard attempt before the browser was closed.

### Changed
- **Password-change info note updated**: The note at the bottom of `SecurityPage` PasswordTab now reads "All other sessions are revoked on password change" instead of the previously incorrect "Other sessions are not invalidated".

---

## v11.4.0 (2026-05-21) - "Hardened"

Upgrade from: v11.3.0 - Drop-in. No schema changes. No configuration changes.

### Added
- **API confirmation tokens for destructive operations (`internal/security/confirm.go`)**: `POST /api/confirm/issue` issues a 48-hex-char single-use token scoped to `operation + target + userID`, valid for 60 seconds. Consuming the token atomically on first use prevents replay. The `confirmRoute` middleware enforces token presence on six destructive routes: pool destroy (`pool_destroy`), pool export (`pool_export`), docker container remove (`docker_remove`), docker prune (`docker_prune`), docker image remove (`docker_rmi`), and zvol destroy (`zvol_destroy`). Returns structured `confirm_required` / `confirm_invalid` error codes.
- **libzfs snapshot operations**: `SnapshotCreate`, `SnapshotDestroy`, and `SnapshotClone` added to `internal/libzfs` for both the cgo path (direct `zfs_snapshot`, `zfs_destroy`, `zfs_clone` C API calls) and the subprocess fallback. Two new security whitelist entries: `zfs_destroy_snapshot` and `zfs_clone`, both with strict regex patterns enforcing the `pool/dataset@snapname` format.
- **4 missing admin-level permissions seeded (`schema.go`)**: `storage:admin`, `shares:admin`, `docker:admin`, and `users:admin` were absent from the seed list; added so role editors can assign them without manual DB intervention.

### Fixed
- **ZED event name alignment**: `zedFastProgressPoll` was broadcasting `zfs.scrub.progress` and `zfs.resilver.progress` while the frontend switch expected `scrub_progress` and `resilver_progress`. Event names unified to underscore convention throughout (`scrub_progress`, `resilver_progress`, `trim_progress`).
- **Missing ZED typed event handlers**: Added `scrub_aborted`, `trim_started`, `trim_completed`, `trim_aborted`, `trim_progress`, and `zfs_alert` (covering `zfs.data_loss`, `zfs.deadman`, `zfs.io_error`) to the frontend WebSocket event map and PoolsPage subscriptions. Scrub abort now shows a warning toast; ZFS alert events show a human-readable error toast.

### Changed
- **RBAC gap closed**: 21 read-only endpoints that were session-authenticated only (ZFS health, scrub status, metrics, iSCSI/NVMe-oF status, firewall status, certificate list, HA status, job polling, and others) now carry explicit `permRoute()` RBAC checks. No authenticated user below the required role can reach any operational endpoint.
- **libzfs snapshot/clone/destroy caller migration**: `zfs_snapshots.go` (`CreateSnapshot`, `DestroySnapshot`, `CloneSnapshot`), `docker_enhanced.go` (pre-update ZFS safety snapshot), and `zfs_sandbox.go` (snapshot create/destroy, clone, dataset destroy, mountpoint and origin queries) all migrated from direct `executeCommand("zfs", ...)` calls to `libzfs.SnapshotCreate`, `SnapshotDestroy`, `SnapshotClone`, `DatasetDestroy`, and `DatasetGet`. All ZFS mutation paths now go through libzfs; only read-only list queries retain subprocess calls with whitelist validation.
- **Docker prune requires confirmation**: The prune button previously fired immediately. It now opens a confirmation modal, and the mutation issues a server-side confirmation token before executing.
- **Pool destroy and Docker remove use confirmation tokens**: `DestroyPoolModal` and both container delete mutations (grid and list view) in `DockerPage` issue a confirmation token via `issueConfirmToken()` and attach it as `X-Confirm-Token` on the destructive request.
- **`apiFetch` extended with custom headers**: The `opts` object now accepts a `headers` field so callers can inject `X-Confirm-Token` without bypassing the standard session/CSRF injection.

---

## v11.3.0 (2026-05-20) - "Integrity"

Upgrade from: v11.2.0 - Drop-in. No schema changes. No configuration changes.

### Added
- **GitOps branch URL parser**: Pasting a GitHub, GitLab, Gitea, or Bitbucket branch URL (e.g. `https://github.com/org/repo/tree/v2-stable`) into the wizard URL field now auto-extracts the repo base URL and populates the branch field. The branch input remains editable for manual override.
- **Inline branch switcher on repo cards**: Each tracked repository on the Repositories tab now shows the current branch as a pill badge (monospace, primary color) with a dropdown. Clicking the badge fetches all remote branches and allows switching with one click; the daemon triggers an immediate pull on switch.
- **HA failover logic job in main CI gate**: Three HA test jobs (`ha-failover-logic`, `ha-promotion-zfs`, `ha-vm-cluster`) are now inlined directly into `ci.yml` and listed as hard dependencies for the `release`, `iso`, and `iso-arm` jobs. No release artifact can publish until all HA guards pass.
- **HA guard-coverage gate**: CI step verifies that all seven named split-brain guard tests pass by name (`TestFailover_NoFencingConfigured_DoesNotPromote`, `TestFailover_WitnessUnreachable_DoesNotPromote`, `TestFailover_WitnessReachable_ClearsWitnessGate`, `TestFailover_MaintenanceMode_SuppressesPromotion`, `TestFailover_HysteresisWindow_SuppressesFlapping`, `TestFailover_SubordinateMode_SuppressesPromotion`, `TestFailover_GuardPriority_SubordinateBeatsHysteresis`). Deleting a guard test is a visible CI failure.
- **HA promotion ZFS job**: `ha-promotion-zfs` exercises `ExecutePromotion` against real loopback ZFS pools in CI (readonly elevation, clone promotion, pool export/import, PoolImportAll hardcoded-search-path regression guard).

### Changed
- **`docs/admin/HIGH-AVAILABILITY.md`**: Restructured to lead with Path A (shared-SAS, SCSI-3 PR fencing, co-located etcd witness on port 2381 of node A - true 2-machine HA with no separate witness machine). Path B (replicated disks, separate witness) is now second. Explicit topology boundary section at the top. Fencing reference order: SCSI-3 PR first, IPMI second, SBD third (with 2-node SBD limitation noted).
- **`docs/reference/ARCHITECTURE.md`**: Multi-Node HA section updated. Removed "witness node required" framing; replaced with topology-dependent explanation. Two ASCII diagrams: shared-SAS (co-located witness) and replicated (separate witness). "Witness Node" section replaced with "Quorum and the Witness" explaining when co-location works vs. when a separate machine is required.
- **`nixos/tests/ha-failover.nix`**: Fixed NixOS `networking.hostId` assertion by deriving a unique 8-char hex value per node from its IP address (`builtins.substring 0 8 (builtins.hashString "md5" localIP)`) directly in `mkDataNode`, bypassing priority-ordering issues with `lib.mkDefault`.
- **`ci.yml`**: `FORCE_JAVASCRIPT_ACTIONS_TO_NODE24: true` now applies to all jobs (was only reaching workflow-called jobs inconsistently). Node.js 20 deprecation warnings eliminated.

---

## v11.2.0 (2026-05-20) - "Toolkit"

Upgrade from: v11.1.0 - Drop-in. No schema changes. No configuration changes.

### Added
- **ZFS block volume (zvol) management**: New `zfs_volumes.go` handler with `ListZvols`, `CreateZvol`, `ResizeZvol`, and `DestroyZvol`. Supports blocksize, sparse provisioning, compression, and volmode (dev/geom/none/default). Routes: `GET/POST/DELETE /api/zfs/volumes`, `POST /api/zfs/volumes/resize`. New `VolumesPage.tsx` provides a full management UI with create, resize, and delete modals. Block Volumes added to sidebar navigation under Storage.
- **Pool TRIM management**: New `zfs_trim.go` handler with `StartTrim` (optional rate limit), `StopTrim`, and `GetTrimStatus` (parses `zpool status -t` per-pool). Routes: `POST /api/zfs/trim/start`, `POST /api/zfs/trim/stop`, `GET /api/zfs/trim/status`. PoolsPage gains a dedicated TRIM tab with pool selector, status display, rate limit input, and start/stop controls.
- **Pool maintenance suite**: New `zfs_pool_maintenance.go` handler with `GetCheckpointStatus`, `CreateCheckpoint`, `DiscardCheckpoint`, `UpgradePool`, `GetPoolFeatures`, `SetMultihost`, and `GetDDTStats`. Routes under `/api/zfs/checkpoint`, `/api/zfs/pool/upgrade`, `/api/zfs/pool/features`, `/api/zfs/pool/multihost`, `/api/zfs/ddt/stats`. PoolsPage gains a Maintenance tab with five sections: checkpoint management (create/discard with confirmation), pool upgrade (all or targeted, with on-version warning), ZFS feature flag inspection (active/enabled/disabled badges), multihost toggle (fencing advisory), and DDT statistics.
- **Project quota management**: New `zfs_project_quotas.go` handler with `GetProjectQuotas` (single-pass `zfs get -H all` parsing both `projectquota@` and `projectused@` prefixes), `SetProjectQuota`, and `RemoveProjectQuota`. Routes: `GET/POST/DELETE /api/zfs/quota/project`. QuotasPage gains a Project tab alongside the existing dataset and user/group tabs.
- **Dedup property for dataset creation**: `CreateDataset` handler extended with `dedup` field support. `DatasetsPage.tsx` create modal gains a deduplication selector (off/on/verify/sha512).
- **Pool capacity tracking on dashboard**: `PoolRow` in `DashboardPage.tsx` now renders a color-coded capacity progress bar (primary under 70%, warning under 85%, error at 85%+) with percentage inline.
- **SMART pre-failure attribute detection on dashboard**: `DiskHealthRow` checks `ata_smart_attributes.table` against a set of pre-failure attribute IDs (5, 10, 184, 187, 188, 196, 197, 198, 199, 201) and shows a "Pre-failure" warning label when any attribute value breaches its threshold.
- **Proactive SMART predictive failure analysis on HardwarePage**: All disk predictions fetched in parallel on page load via `useQueries`. Risk badge always visible inline per disk (critical/warning/ok) with no interaction required. Disks sorted by risk severity (critical first, then warning, then degraded, then healthy). Summary banner above the disk list when any disk is at elevated or critical risk. Toast notification on page load naming all critical-risk disks. Detail panel expands on badge click for warning and critical disks.

### Changed
- **`daemon/cmd/dplaned/main.go`**: Registered all new routes for zvols, TRIM, pool maintenance, and project quotas with appropriate RBAC permission levels (read/write/admin).
- **`app-react/src/routes/index.tsx`**: Added `/volumes` route bound to `VolumesPage`.
- **`app-react/src/components/layout/navConfig.ts`**: Added Block Volumes leaf in the Storage navigation group.

---

## v11.1.0 (2026-05-18) - "Groundwork"

Upgrade from: v11.0.0 - Drop-in. No schema changes. No configuration changes.

### Added
- **Complete ZED typed event dispatch (`internal/zfs/zed_listener.go`)**: Nine previously unhandled ZED subclasses now emit structured WebSocket events and trigger pool health refresh. `scrub_abort` emits `scrub_aborted` (warning). TRIM lifecycle: `trim_start` emits `trim_started` and spawns a progress poll goroutine; `trim_finish` emits `trim_completed`; `trim_abort` emits `trim_aborted`. `vdev_clear` emits `vdev_errors_cleared`. `vdev_online` emits `vdev_recovered`. `pool_import` emits `pool_imported`. `data_loss` emits `zfs.data_loss` with pool and state fields (error severity). `deadman` emits `zfs.deadman` with pool and state fields (error severity).
- **TRIM progress poll goroutine (`zedTrimProgressPoll`)**: Polls `zpool status` every 2 seconds while a TRIM is in flight, broadcasting `zfs.trim.progress` events with `percent_done`, `eta`, and `bytes_done` fields. Exits when TRIM finishes (the `trim_finish` event broadcasts completion). Maximum runtime 12 hours as a safety bound.
- **`TrimInfo` struct and TRIM status helpers (`internal/zfs/status.go`)**: `GetPoolTrimLine` scans `zpool status` output for the TRIM section. `ParseTrimLine` parses "trim in progress" and "trim completed" lines into a `TrimInfo` struct with `InProgress`, `PercentDone`, `ETA`, `BytesDone`, and `RawTrimLine` fields.
- **Shared libzfs subprocess functions (`internal/libzfs/zfs.go`)**: Functions that require complex nvlist construction and are not simpler through the native C API are implemented once as subprocess calls and used by both cgo and fallback build paths: `VdevAdd`, `VdevAttach`, `VdevReplace`, `VdevRemove`, `PoolSplit`, `PoolCreate`, `PoolDestroy`, `DatasetRename`, `SnapshotListHolds` (parses tab-delimited hold output into `[]HoldEntry`), and `DatasetCreateWithProps` (creates dataset then sets each property).
- **Native cgo libzfs implementations (`internal/libzfs/zfs_cgo.go`)**: `PoolClear` calls `zpool_clear(zhp, NULL)`. `PoolSetProperty` calls `zpool_set_prop`. `DatasetDestroy` calls `zfs_destroy` for non-recursive; falls back to subprocess for recursive. `SnapshotHold` calls `zfs_hold`. `SnapshotRelease` calls `zfs_release`.
- **Security whitelist additions (`internal/security/whitelist.go`)**: `zpool_add` entry with `validateZpoolAdd` (validates pool name, optional vdev-type keyword, and device paths). `zpool_set_property` entry with `validateZpoolSetProperty` (validates `set key=value pool` against an allowedKeys map). `validateZpoolCreate` rewritten to handle the full `-f`/`-o`/`-O` flag grammar used by `ZpoolCreateFullArgs`. `validateZfsSetProperty` extended with `refreservation`, `reservation`, and user/group quota properties (`userquota@name=size` format).

### Changed
- **`handlers/zfs_operations.go` - complete libzfs migration**: All remaining subprocess calls replaced. `AddVdevToPool` uses `libzfs.VdevAdd`. `RemoveCacheOrLog` uses `libzfs.VdevRemove`. `ReplaceDisk` and `AttachDisk` job closures use `libzfs.VdevReplace` and `libzfs.VdevAttach`. `SetDatasetQuota` (refquota and refreservation) uses `libzfs.DatasetSet`. `HoldSnapshot` and `ReleaseSnapshot` use `libzfs.SnapshotHold` and `libzfs.SnapshotRelease`. `ListHolds` uses `libzfs.SnapshotListHolds`. `SplitPool` uses `libzfs.PoolSplit`. `PoolOperations` (clear and online subcommands) uses `libzfs.PoolClear` and `libzfs.VdevOnline`. `RenameDataset` uses `libzfs.DatasetRename`. `PromoteDataset` uses `libzfs.DatasetPromote`. `OfflineDisk` uses `libzfs.VdevOffline`. `ExportPool` uses `libzfs.PoolExport`.
- **`gitops/apply.go` - complete libzfs migration**: `destroyPool` uses `libzfs.PoolDestroy`. `createDataset` uses `libzfs.DatasetCreateWithProps` with a property map built from `DesiredDataset` fields. `modifyDataset` uses `libzfs.DatasetSet`. `deleteDataset` uses `libzfs.DatasetDestroy`. Nil-dataset guard added to the create log line.
- **`zfs_fallback.go` whitelist key fix**: `PoolIsMember` corrected to use `zpool_status` whitelist key (was incorrectly using the raw binary name `zpool`).

---

## v11.0.0 (2026-05-17) - "Bedrock"

Upgrade from: v10.5.0 - Drop-in. One schema migration runs automatically on first start (`storage_operations` table). New modules are opt-in via NixOS options.

### Added
- **Transactional storage operation safeguards (`internal/storageops`)**: New `storageops` package wraps every destructive ZFS operation in a pre-flight gate and lifecycle record. `Begin` inserts a `pending` row and blocks if a previous operation on the same target is already in progress. `Commit` and `Fail` transition the row to `committed` or `failed` with timestamp. Operations pending beyond 5 minutes are flagged as possibly stuck. Wired into `PoolCreate`, `VdevAdd`, `Replace`, `Attach`, `WipeDisk`, and `DatasetCreate`.
- **`storage_operations` schema migration (`00003_storage_operations.sql`)**: Goose migration adding `storage_operations(id, operation_type, target, state, error, started_at, completed_at)` with indexes on `(target, state)` and `started_at DESC`.
- **Storage operation audit API**: `GET /api/storage/operations` returns the 50 most recent operations (up to 200 via `?limit=N`). `DELETE /api/storage/operations/{id}` clears a stuck pending operation so a new one can start on the same target. Both routes require `storage:admin` permission.
- **SES enclosure management (`internal/hardware`)**: New `hardware` package enumerates SAS enclosures from `/sys/class/enclosure`, reads slot status (`status`, `locate`, `fault`, `type`) from sysfs attributes, and resolves the enclosure's `/dev/sgN` device via the `scsi_generic` sysfs child. Path traversal prevention on slot names. `SESElement` type carries element type, descriptor, status, and details fields.
- **SES locate LED control (`PUT /api/enclosure/{id}/slot/{index}/locate`)**: Writes `1` or `0` to `/sys/class/enclosure/{id}/{slot}/locate` directly via sysfs. No subprocess, no library dependency. Enclosure ID and slot index are validated before any write. Requires `system:admin` permission.
- **SES topology and status API**: `GET /api/enclosure` returns all enclosures and their slots. `GET /api/enclosure/{id}/ses-status` reads element status from the resolved `/dev/sgN` device via direct SG_IO RECEIVE DIAGNOSTIC RESULTS ioctls (no subprocess). Device Slot elements report IDENT/FAULT/OFF flags; Temperature Sensors report degrees C. `/dev/sgN` path is validated against a strict regex. Requires `system:read` permission.
- **NFSv4 ACL layer (`internal/acl`)**: New `acl` package with `NFSv4ACE` and `ACLResult` types, `ParseNFSv4ACL`, `FormatACESpec`, `ValidateACE`, and `POSIXModeToNFSv4` (RFC 5661 canonical mapping for `OWNER@`, `GROUP@`, `EVERYONE@`). ACE validation enforces type (`A/D/U/L`), flags (`gdfpniGSF`), safe principal regex, and permission character set (`rwaxdDtTnNcCoy`).
- **NFSv4 ACL API endpoints**: `GET /api/nfs4acl?path=...` returns a parsed ACE list via `nfs4_getfacl`. `PUT /api/nfs4acl` sets the full ACL via `nfs4_setfacl -s`. Both enforce `security.IsValidPath`, validate each ACE before any subprocess, and audit every execution. Require `storage:read` and `storage:write` respectively.
- **`nixos/modules/nfs.nix`**: Declarative NFSv4.2 server module. Options: `enable`, `nfs4Domain` (default `localdomain`), `minVersion` (enum `4`/`4.1`/`4.2`, default `4.2`), `openFirewall`. Configures `nfsd`, writes `/etc/idmapd.conf`, sets `fs.nfs.nfs4_disable_idmapping=0` sysctl, adds `nfs4-acl-tools` to the daemon PATH, and opens TCP/UDP 2049 and 111 when `openFirewall` is true. Follows the same two-layer split pattern as `samba.nix`.
- **Security whitelist entries for `nfs4_getfacl` and `nfs4_setfacl`**: Path restricted to `/(mnt|tank|data|home|srv|opt)/...`; ACE spec argument validated against the full NFSv4 grammar before any subprocess is invoked.
- **`internal/libzfs` package with build-tag isolation**: Two compile paths behind Go build tags. `zfs_cgo.go` (`//go:build linux && cgo`) calls `libzfs.h` directly via cgo, using `runtime.LockOSThread` and a per-call `libzfs_init`/`libzfs_fini` handle. `zfs_fallback.go` (`//go:build !linux || !cgo`) provides identical signatures via `cmdutil` subprocess calls, keeping the static musl production build unaffected. Exported: `PoolIsMember`, `PoolImportAll`, `PoolExport`, `DatasetCreate`, `DatasetGet`, `DatasetSet`, `DatasetPromote`, `VdevDetach`, `VdevOnline`, `VdevOffline`.
- **`nix build .#dplaneos-daemon-cgo`**: New `mkDaemonCGO` flake builder with `CGO_ENABLED=1`, `gcc`, `pkg-config`, and `pkgs.zfs` in build inputs. Produces a glibc-linked binary that exercises the cgo libzfs path at runtime.
- **`internal/scsipr` package**: SCSI-3 Persistent Reservation primitives for shared-SAS HA. `scsipr_linux.go` (`//go:build linux`) issues raw `SG_IO` ioctls (`PERSISTENT RESERVE IN/OUT`) via `golang.org/x/sys/unix.Syscall`. `scsipr_stub.go` returns "unsupported" on non-Linux. `DeriveKey` produces a stable 8-byte key from SHA-256 of `/etc/machine-id`. `Register` uses PROUT REGISTER with APTPL=1. `Reserve` uses WRITE EXCLUSIVE REGISTRANTS ONLY (type `0x05`). `Release`, `Preempt`, and `ReadKeys` (PRIN READ KEYS + READ RESERVATION) complete the primitive set.
- **`cmd/dplane-fenced` standalone binary**: Manages SCSI-3 persistent reservations as an independent systemd service that survives `dplaned` restarts. Startup: derives key, enumerates ZFS pool member disks via `zpool status -P`, resolves each block device to `/dev/sgN` through sysfs (`/sys/class/block/<dev>/device/generic`), registers and reserves all disks. Refreshes every 30 seconds to fence newly added disks. Listens on `/run/dplaneos/fenced.sock` for `STATUS`, `RELEASE`, `FENCE`, and `UNFENCE` commands (newline-delimited JSON). On `SIGTERM`: releases all reservations cleanly before exit.
- **`internal/ha/fenced_client.go`**: Client for `dplaned` to communicate with `dplane-fenced`. `FencedRelease()` signals graceful reservation release before pool export during failover. `FencedStatus()` returns current disk count and key. `FencedPreempt(device)` fences an individual disk on demand.
- **`nixos/modules/fenced.nix`**: NixOS module for `dplane-fenced`. Runs in `dplaneos-fenced.slice` isolated from `dplaneos.slice`, so it survives `dplaned` restarts and slice resets. Uses `Wants` + `After` for ordering, never `BindsTo`. `CAP_SYS_RAWIO` only; `ProtectSystem=strict`, read-only `/sys/class/block` and `/etc/machine-id`. 60-second watchdog; APTPL=1 preserves reservations in disk controller NVRAM across fenced restarts.

### Changed
- **SES status endpoint: `sg_ses` subprocess replaced with direct SG_IO ioctls**: `GET /api/enclosure/{id}/ses-status` now reads SES pages directly via `RECEIVE DIAGNOSTIC RESULTS` (opcode `0x1C`) without spawning any external process, eliminating the `sg_ses` runtime dependency. `ses_ioctl.go` (`//go:build linux`) implements the three-page parse (0x01 Configuration, 0x02 Status, 0x07 Element Descriptor). `ses_stub.go` (`//go:build !linux`) returns unsupported. `ParseSGSesOutput` removed.
- **`handlers/disk_operations.go` `WipeDisk`**: Pool membership check migrated from `zpool status` string parsing to `libzfs.PoolIsMember` (nvlist tree walk). Returns the specific pool name in the safety error message. `storageops.Begin/Fail/Commit` now wraps the entire wipe sequence.
- **`handlers/zfs.go` `CreateDataset`**: Dataset creation migrated from `executeCommand("zfs", ["create", ...])` to `libzfs.DatasetCreate`. `storageops` tracking added.
- **`handlers/zfs_operations.go`**: `DetachDisk` migrated to `libzfs.VdevDetach`. `storageops.Begin/Fail/Commit` added to `VdevAdd`, `Replace`, and `Attach` operations.
- **`handlers/disk_discovery.go` `HandlePoolCreate`**: `storageops.Begin/Fail/Commit` added around `zpool create` execution. Returns HTTP 409 if a pool creation on the same name is already in progress.
- **`ha/promote.go` failover sequence**: Pool import migrated to `libzfs.PoolImportAll`. Dataset `readonly` and `origin` property reads migrated to `libzfs.DatasetGet`; property writes to `libzfs.DatasetSet`; clone promotion to `libzfs.DatasetPromote`.
- **`flake.nix`**: `subPackages` updated to `["cmd/dplaned" "cmd/dplane-fenced"]` in all three builders. `nixosConfigurations.dplaneos` and `dplaneos-arm` wire `services.dplaneos.fenced.package` from the same derivation as `daemonPackage`. ISO builder updated to match.
- **`nixos/configuration-standalone.nix`**: Added `./modules/nfs.nix` and `./modules/fenced.nix` to imports.
- **`nixos/configuration.nix`**: NFSv4.2 enabled via `services.dplaneos.nfs` with `nfs4Domain = "localdomain"` and `minVersion = "4.2"`. Port 2049/111 firewall comment updated to reflect module ownership.

---

## v10.5.0 (2026-05-16) - "Ironclad"

Upgrade from: v10.4.0 - Drop-in. No breaking changes.

### Fixed
- **TOCTOU races in replication peer handlers**: `HandleCreateRemote`, `HandleUpdateRemote`, `HandleDeleteRemote`, `HandleResetFingerprint`, `updateRemoteTestStatus`, `persistTestSuccess`, and `resetAllRemotesKeyState` all used the classic load-then-save pattern with a gap between lock acquisitions. Added `atomicModifyRemotes` helper that holds the write lock across the full load-modify-save cycle. `HandleAuthorizeRemote` was split into a two-step pattern: `loadRemotes` for the SSH connection read phase, then `atomicModifyRemotes` targeting only the specific peer's fields after the SSH session completes.
- **TOCTOU race in rsync "Run Now" status write**: `RunRsyncScheduleNow` recorded the job ID and "running" status by mutating the snapshot loaded before job start, then calling `saveRsyncSchedules` with that stale slice. Any concurrent CRUD request could interleave and be silently overwritten. Replaced with `atomicModifyRsyncSchedules` targeting only the specific schedule's status fields.
- **TOCTOU races in file share handlers**: `CreateFileShare`, `DeleteFileShare`, and `DownloadFileShare` all read the shares list, modified it in memory, then saved back - racing with any concurrent operation. Added `atomicModifyFileShares` helper. The download counter increment now uses a separate atomic update after the file is opened, so the stale snapshot from the validation phase is never written back.
- **TOCTOU races in SSH key handlers**: `AddSSHKey` and `DeleteSSHKey` loaded the key store, mutated it, and saved it in separate lock acquisitions. Added `atomicModifySSHKeys` helper. The `importExistingKeys` scan and duplicate check now happen inside the callback so they see the definitive on-disk state. `writeAuthorizedKeys` is called after the lock releases, as required.
- **TOCTOU race in MinIO config update**: `UpdateMinioConfig` loaded the current config, merged non-empty request fields in memory, validated, then saved - leaving a window where two concurrent updates would each read the same base config and one would win. Added `atomicModifyMinioConfig` that holds the write lock across load-merge-validate-save. Validation errors are signaled via a sentinel and mapped to HTTP 400; I/O errors map to HTTP 500.
- **TOCTOU races in NVMe-oF target handlers**: `CreateNVMeTarget`, `UpdateNVMeTarget`, and `DeleteNVMeTarget` called `nvmet.LoadExports` and `nvmet.SaveExports` without any serialization - the `nvmet` package has no internal mutex. Added `nvmeMu sync.Mutex` and `atomicModifyNVMeTargets` that covers the load-modify-save, with `nvmet.Apply` called after the lock releases so the kernel configfs write does not block concurrent reads.



## v10.4.0 (2026-05-16) - "Bulwark"

Upgrade from: v10.3.0 - Drop-in. No breaking changes.

### Fixed
- **TOCTOU races in all CRUD handlers eliminated**: Replication schedule Create/Update/Delete and Rsync backup schedule Create/Update/Delete all used a separate load-then-save pattern, leaving a race window where concurrent HTTP requests could overwrite each other's writes. All six handlers now use a new `atomicModifySchedules` / `atomicModifyRsyncSchedules` helper that holds the write lock across the entire load-modify-save cycle.
- **`updateScheduleStatus` raced with CRUD handlers**: The function called `loadReplicationSchedules` (read lock) then `saveReplicationSchedules` (write lock) in two separate acquisitions, allowing a concurrent PUT/DELETE to interleave and overwrite the status write. Converted to use `atomicModifySchedules` so status updates are fully serialized with CRUD writes.
- **`HandleResetFingerprint` erroneously cleared `KeyInstalled`**: After clicking Reset Trust, the peer appeared as "Needs authorization" in the UI, the replication key was considered absent, and all replication jobs for that peer failed immediately at the authorization check. The route now only clears `Fingerprint`, `HostKey`, and `TestOK`; `KeyInstalled` is preserved because the client key remains in the remote's `authorized_keys`.
- **Deadlock on "Run Now" for rsync schedules**: `RunRsyncScheduleNow` held `rsyncSchedMu` then called `saveRsyncSchedules`, which also acquires the same lock. Go mutexes are not reentrant; every "Run Now" click deadlocked the handler goroutine. Outer lock removed; `saveRsyncSchedules` handles its own locking.
- **Network rollback timer called non-existent binary**: `ApplyNetworkWithRollback` scheduled `cmdutil.Run(..., "network_apply", "apply")` as the rollback command. The binary `network_apply` does not exist; the rollback would silently fail. Changed to `executeCommandWithTimeout(TimeoutMedium, "netplan", []string{"apply"})`.
- **Network rollback globals unprotected against data races**: The rollback path and content vars were read by the timer goroutine and written by HTTP handlers without synchronization. Added `netRollbackMu sync.Mutex`; all accesses in timer setup, rollback callback, and `ConfirmNetwork` are now protected.
- **`SetNTPServers` wrote nothing to `timesyncd.conf`**: Code called `executeCommandWithTimeout(..., "tee", ...)` without piping the NTP config string to stdin. `tee` received EOF and wrote an empty file. Replaced with `os.WriteFile` writing the config directly.
- **`GetZFSDelegation` masked errors with success response**: On a `zfs allow` execution failure the handler returned `{"success": true}` with HTTP 200. Fixed to return `respondErrorSimple` with HTTP 500.
- **POSIX name validation allowed names starting with a digit**: `isValidPosixName` checked only that chars were `[a-zA-Z0-9_]` but did not enforce the POSIX rule that the first character must be a letter or underscore. Numeric-prefixed names such as `1bad` were accepted. First-character check added.



## v10.3.0 (2026-05-16) - "Harness"

Upgrade from: v10.2.0 - Drop-in. No breaking changes.

### Added
- **Recursive flag for replication**: Both one-shot Replicate and Schedules now expose a "Recursive (include child datasets)" checkbox. When unchecked, `-R` is omitted from `zfs send` so only the exact dataset is sent, not its children. Default is recursive (previous behavior preserved).
- **Reset Trust button on Peers**: Each peer with a pinned host fingerprint now shows a "Reset Trust" action. Clicking it clears the stored TOFU fingerprint so the next connection re-pins the host key. Useful when a peer's SSH host key changes intentionally (hardware replacement, reinstall).
- **POST /api/replication/remotes/{id}/reset-fingerprint**: Backend route backing the Reset Trust action. Clears `Fingerprint`, `HostKey`, and `TestOK` for the peer; `KeyInstalled` is preserved because the replication key remains in the remote's `authorized_keys`. Commits state to GitOps.
- **Manual+TriggerOnSnapshot helper note**: The schedule modal now shows an explanatory note when interval is "Manual" and "Trigger after each snapshot" is enabled, clarifying that replication fires after every auto-snapshot but never on a fixed timer.

### Fixed
- **`buildKnownHostsArgs` silently fell back to `accept-new` on temp file failure**: If the OS failed to create the temp known_hosts file (e.g. disk full), the function returned `StrictHostKeyChecking=accept-new` without logging or failing. Any host would be accepted silently. Fixed to return an error; the replication job now fails explicitly rather than running with weakened host verification.
- **`getResumeToken` used shell string interpolation**: The resume token check passed a single interpolated string to SSH (`"zfs get ... dataset"`), meaning the remote shell parsed it. Replaced with discrete argv matching the rest of the pipeline.
- **Incremental base snapshot deleted causes hard failure**: If a snapshot recorded in `last_replicated_snapshot` was pruned before the next replication run, `zfs send -i` would fail with an obscure error. The schedule runner now pre-checks that the base snapshot exists before building the send args. If it has been pruned, it logs a warning and falls back to a full send automatically.
- **`TriggerPostSnapshotReplication` stacked concurrent jobs**: If a replication job for a schedule was already running when a new snapshot fired the trigger, a second job was launched unconditionally. Schedules with `last_status == "running"` are now skipped in the trigger path to prevent concurrent overlapping sends to the same remote.
- **Rate limit silently ignored with no warning at job start**: When `rate_limit_mb` was set but `pv` was not installed, the warning appeared only after the send pipeline started. Both the schedule and one-shot paths now check for `pv` at job start and log a visible warning before any ZFS data flows.

### Removed
- `"Manual only"` label in the interval selector replaced with `"Manual"`.



## v10.2.0 (2026-05-15) - "Keyring"

Upgrade from: v10.1.0 - Drop-in. No breaking changes.

### Added
- **Zero-touch ZFS replication via Peers model**: SSH key distribution, host key pinning, and all connection management are now handled by the GUI. Add a peer (name, host, user, port), click Authorize, enter the SSH password once - the daemon installs the replication ed25519 key via the Go SSH client. The password exists only in the request buffer and is never written to disk, database, or logs. Subsequent replication runs use key-based auth only; no passwords, no shell helpers, no `sshpass`.
- **TOFU host key pinning enforced in ZFS send pipeline**: The SSH host fingerprint captured during Authorize/Test is written to a per-transfer temp known_hosts file. The `ssh` binary is invoked with `StrictHostKeyChecking=yes` against that file, preventing MITM attacks during long-running ZFS send streams. For air-gapped hosts where password auth is disabled, the Sovereign Target Key panel provides the daemon's public key for manual `authorized_keys` installation.
- **Peers tab - full CRUD**: Add, edit, delete peers. Authorize (one-time password), Re-auth (after keypair rotation), Test (verifies SSH key-based access and ZFS readiness on the remote). All peer connection details (name, host, user, port, fingerprint, host key, authorization state) are persisted in `replication-remotes.json` and committed to GitOps state.
- **Schedules tab - full CRUD with incremental and resume**: Replication schedules support incremental sends (tracks `last_replicated_snapshot` across runs as the `-i` base), resume tokens (checks remote for an interrupted transfer token before sending), bandwidth throttling via `pv` (graceful degraded-mode fallback if `pv` is not installed), and post-snapshot triggers. The background monitor fires due schedules every 5 minutes.
- **Replicate tab - one-shot send**: Dataset picker, peer picker (authorized peers only), snapshot picker for incremental base (loaded from `/api/zfs/snapshots`), rate limit, resume, and compress options. Job progress streamed in real time with bytes sent, rate, and ETA.

### Fixed
- **IPv6 host:port formatting**: Five instances of `fmt.Sprintf("%s:%d", host, port)` passed to `net.Dial`-family calls replaced with `net.JoinHostPort`. Affected `alerting_smtp.go` (SMTP delivery) and three replication files (TCP reachability check, SSH dial). IPv6 peer addresses now work correctly.
- **pv missing caused hard replication failure**: The rate-limit path called `pv` unconditionally. Added `exec.LookPath("pv")` check; if absent, logs a warning and falls through to direct pipe. Replication proceeds at unlimited bandwidth; throttling is degraded-mode only.
- **Incremental replication was broken end-to-end**: The schedule runner never used `LastReplicatedSnapshot` as the `-i` base and the success path did not persist the snapshot name. The frontend `ReplicateForm` sent `incremental: true` but no `base_snapshot`. Both are now fixed.
- **Resume token not checked in schedule path**: The resume token check existed in the one-shot handler but was never wired into the schedule runner. `launchReplicationJob` now checks for a remote resume token before sending when `Resume` is enabled.

### Removed
- Legacy replication handlers (`ZFSSend`, `ZFSSendIncremental`, `ZFSReceive`, `TestRemoteConnection`, `CopyReplicationKey`) and their routes removed. These were dead code replaced by the Peers model.



## v10.1.0 (2026-05-13) - "Sentinel"

Upgrade from: v10.0.0 - Drop-in. No breaking changes.

### Added
- **Witness node installer ISO**: The combined installer ISO now presents a role-selection menu on boot: "Install DPlaneOS" (existing NAS path) or "Install Witness Node" (new). Selecting witness launches a `gum`-based TUI wizard that collects the three cluster IP addresses, an SSH public key, and the target disk, then installs a minimal NixOS system running only etcd for Patroni quorum. No ZFS, no DPlaneOS daemon.
- **arm64 installer ISOs**: The combined installer ISO is now built for both `x86_64` (amd64) and `aarch64` (arm64) on native GitHub ARM64 runners. arm64 ISOs bake the `dplaneos-arm` NixOS closure for fully offline installation on ARM hardware.
- **`nixosModules.dplaneos-witness`**: Flake export of `nixos/patroni-witness.nix` for users deploying via `git clone` + `nixos-rebuild switch`. Declare the module in any NixOS configuration and set `services.dplaneos.ha.witness.{enable, localAddress, nodeAAddress, nodeBAddress}`.
- **Comprehensive documentation suite**: Architecture (three-layer model, HA data flow), GitOps Reference (state.yaml format, reconciliation engine), Design Philosophy, High Availability, Backup and Replication, OTA Updates, Optional Protocols, Alerts, Troubleshooting, Recovery, Hardware Compatibility, Non-ECC Warning, Showstopper Mitigation Guide, Threat Model, NixOS Rationale, Porting Guide, Error Reference, Dependencies, and Codebase Diagram.

### Fixed
- **HA guide witness setup (Step 2)**: The previous inline NixOS snippet used etcd node names (`"witness"`, `"node-a"`, `"node-b"`) incompatible with the `ha.nix` module (which uses `"etcd-witness"` and `"etcd-<IP>"`). Following the old guide would cause etcd cluster formation to fail. Step 2 now documents the correct ISO and Flake paths using `patroni-witness.nix`.
- **arm64 ISO closure mismatch**: The `eachSystem` flake block always embedded the `dplaneos` (x86_64) closure as the install target regardless of the build system. arm64 ISO builds now correctly embed `dplaneos-arm`.



## v10.0.0 (2026-05-12) - "Polaris"

Upgrade from: v9.1.1 - Drop-in. No breaking changes.

### Added
- **Global search palette** (Ctrl/Cmd+K): ARIA combobox command palette searches nav items instantly and queries live API sources (pools, datasets, containers, shares) at 2+ characters. Full keyboard navigation with ArrowUp/Down, Enter, Escape.
- **Reporting chart hover tooltips**: Interactive SVG crosshair on all history charts with timestamp+value callout tooltip. Accessible via `aria-live` region and an expandable table fallback (`View as table`).
- **ZFS dataset encryption management**: Encrypt, lock, and unlock actions on the Datasets page action menu. Contextual menu items based on current `encryption` and `keystatus` properties. Lock icon badge in the tree row. Uses `/api/zfs/encryption/create|lock|unlock`.
- **Light/dark theme toggle**: Full `[data-theme="light"]` CSS token block with WCAG AA contrast ratios. ThemeToggle button in TopBar persists to `localStorage`. Flash-free via `initTheme()` called before first React paint.
- **SMB active sessions viewer**: Tabbed layout on the Shares page (Shares / Active Sessions). Live poll every 15 seconds with per-session disconnect action. Full ARIA data table.
- **Storage treemap on Datasets page**: Pure-SVG squarified treemap above the dataset tree, color-coded by pool. Click any cell to filter the tree to that dataset. ResizeObserver-responsive width, accessible table fallback.
- **API Explorer page** (`/api-explorer`): Browse and test all daemon endpoints grouped by resource. Live request/response with status badge and timing. Added to System nav group.
- **Dashboard widget pinning**: "Customize" popover on the dashboard lets users toggle any of 10 widgets on/off. State persisted in `localStorage`, reset-to-defaults button included.
- **Global keyboard shortcuts**: `?` opens a keyboard shortcuts help modal. `g` + letter navigation shortcuts jump to key pages (g+h=Dashboard, g+p=Pools, g+d=Datasets, g+s=Settings, g+n=Network, g+l=Logs, g+c=Docker, g+f=Files, g+r=Reporting, g+u=Updates). `?` button added to TopBar for discoverability.
- **Docker: in-browser container terminal**: xterm.js terminal panel on container rows. Opens a WebSocket-backed shell session directly in the UI without needing SSH. Includes FitAddon for responsive sizing.
- **Docker: container card grid view**: ZimaOS-style card layout alternative to the default table. Each card shows container icon, name, status dot, primary port, and quick start/stop/restart actions.
- **Docker: container edit modal**: Tabbed inspect panel (General / Ports / Volumes / Env) backed by `/api/docker/containers/{name}/inspect`. Allows reconfiguring the container via `/api/docker/containers/{name}/reconfigure`.
- **Docker: enhanced Compose Stacks**: `StackInfo` type extended with `services`, `file_size`, `created_at`, `updated_at`, `labels`. Stack status now shows `running/partial/stopped` with distinct colors.
- **GitOps: category capture**: "Capture" action per sync category lets users snapshot live state (SMB/NFS shares, Docker stacks, users/groups, replication, system config) into `state.yaml`. Storage category intentionally excluded (disk paths are machine-specific). Capture keys and descriptive hints added to `CATEGORY_META`.

### Changed
- **MonitoringPage** nav label renamed from "Monitoring" to "Inotify Watches" with `notifications` icon; page title updated to match. The page covers inotify watch limit monitoring only, not broad system monitoring.
- **GitOpsPage**: RESHAPE change color switched from hardcoded `#f59e0b` to `var(--warning)` for theme consistency.
- **RemovableMediaPage**: Page header and empty state migrated to standard `page-header`/`page-title`/`page-subtitle`/`empty-state` CSS classes.
- **DirectoryPage**: Sync log `pre` element color changed from hardcoded `#aab2c0` to `var(--text-secondary)` for light-mode correctness.
- All inline `rgba()` color values across 15+ page files replaced with CSS design tokens (`var(--warning-bg)`, `var(--success-border)`, `var(--warning)`, `hsla(var(--hue-primary),...)`) for correct light-mode rendering. Covers AlertsPage, CertificatesPage, DelegationPage, FilesPage, FirewallPage, HardwarePage, HAPage, IPMIPage, NetworkPage, PowerPage, SettingsPage, TopBar, UpdatesPage.



## v9.1.1 (2026-05-11)

Upgrade from: v9.1.0 - Drop-in. No breaking changes.

### Added
- **NixOS: Cold Tier FUSE support** (`module.nix`, `configuration.nix`): `pkgs.fuse3` added to `environment.systemPackages` (provides `fusermount3` required by rclone); `fuse` kernel module added to `boot.kernelModules`; new `services.dplaneos.coldTier.rootPath` option (default `/mnt/cold`) in the module path declares the FUSE mount root, adds it to dplaned `ReadWritePaths` (required under `ProtectSystem=strict`), and creates it via `systemd.tmpfiles` at boot. The standalone `configuration.nix` template receives the same three additions directly.
- **NixOS: OpenZFS 2.2+ build-time assertion** (`module.nix`): `nixos-rebuild` fails at evaluation time with a clear message if the configured ZFS package is older than 2.2.0, preventing silent RAID-Z expansion misfire where `zpool attach` on raidz would create a mirror instead.
- **NixOS: SBD lease dataset init** (`ha.nix`): New `services.dplaneos.ha.sbd.{pool,dataset}` options. When `pool` is non-empty, a one-shot systemd service `dplaneos-sbd-init` creates the ZFS dataset at first boot before dplaned starts. Fully opt-in: empty `pool` (the default) skips the service entirely with zero overhead.



## v9.1.0 (2026-05-11) - "Elastic VDEV"

Upgrade from: v9.0.0 - Drop-in. No breaking changes.

### Added
- **RAID-Z Parity Expansion**: Pools with RAID-Z vdevs now support online disk addition via `zpool attach` (OpenZFS 2022+ expansion semantics). A new "Expand VDEV" button appears on raidz-type vdevs in the topology view. Selecting a new disk triggers `POST /api/zfs/pool/raidz-expand`, which validates the anchor disk is in a RAID-Z vdev (preventing accidental mirror creation), starts the attach, then polls progress every 30 seconds broadcasting `raidz_expand_started`, `raidz_expand_progress`, and `raidz_expand_completed` WebSocket events. The UI renders a live progress card (matching the resilver card pattern) with percent-done, ETA, and completion state. The pool remains fully accessible during the multi-hour redistribution process.

### Changed
- Pool topology view now shows an "add disk" icon button on raidz vdevs in addition to the existing replace button on degraded disk vdevs.



## v9.0.0 (2026-05-11) - "Zero-Touch Integrity"

Upgrade from: v8.3.0 - Drop-in. No breaking changes. All HA features are opt-in.

### Added
- **NixOS SSH Daemon Settings** (Pillar 1): Authorized-key management page gains a second tab "Daemon Settings" allowing port, password authentication, and PermitRootLogin to be configured and applied via nixos-rebuild. Settings are stored in the nixwriter JSON bridge and rendered into NixOS configuration at build time with `lib.mkIf` guards so unset fields have no effect. GitOps engine picks up SSH fields in state.yaml and emits `ssh-set:*` change items.
- **Cold Tier (rclone FUSE mounts)** (Pillar 2): Cloud Sync page gains a "Cold Tier" tab for mounting rclone remotes as FUSE filesystems under `/mnt/cold/`. Supports VFS cache modes (off/minimal/writes/full), per-mount live status indicator, usage reporting, mount/unmount/delete actions. Mounts are persisted in the database and re-established at daemon startup via `ReMountAll()`. Remote names are validated against the local rclone config before creation.
- **Declarative ZFS Topology Reshape** (Pillar 3): state.yaml pool entries support `force_reshape: true` which converts purely additive topology changes (new vdev groups) into `RESHAPE` plan actions that execute `zpool add` automatically. Partial vdev group changes and destructive operations remain `MANUAL` with an explanatory hint. GitOps plan view shows RESHAPE items with amber color and an inline `zpool add` command preview. Pre-existing bug fixed: plan view no longer always shows "Zero drift" after GitOps handler was updated to emit a flat `changes` array alongside the nested plan.
- **SBD Lease Fencing** (Pillar 4): New opt-in ZFS-property-based self-fencing mechanism. When configured, the daemon writes a unix timestamp property (`dplaneos:sbd_lease`) to a designated dataset every `LeaseTTLSecs/3` seconds. If the node loses ZFS access it cannot renew, and `ExecuteSBDFence` triggers `reboot -f` as a last-resort self-termination. Exposed as `GET/POST /api/ha/sbd/configure`. HA page gains an SBD card with pool/dataset/TTL inputs and a live lease-health badge. Completely opt-in: leaving Pool empty is a no-op with zero runtime overhead on single-node deployments.
- **TryFence** (Pillar 4): New `ha.TryFence(nodeID, bmcCfg, sbdCfg)` function provides a priority-ordered fencing chain: BMC/IPMI if armed, then SBD if configured, then a warning log that returns nil without error so single-node systems are never blocked.

### Changed
- GitOps `diffPool` rewrites fine-grained per-group topology analysis replacing the previous single "topology-drift" fallback.
- HA setup wizard step 6 label updated to include SBD in the fencing overview.

## v8.3.0 (2026-05-11) - "Barrier-Free"

Upgrade from: v8.2.0 - Drop-in.

### Changed
- **WCAG 2.2 accessibility pass**: All interactive elements across the UI now meet WCAG 2.2 AA. Clickable divs replaced with semantic buttons, modal dialogs use `role="dialog"` with `aria-modal` and a keyboard focus trap (Tab/Shift+Tab wrap, Escape closes), tooltips use `role="tooltip"` with `aria-describedby` injection that merges with existing descriptions, sidebar navigation buttons carry `aria-expanded`/`aria-controls` for group toggles, the Users page tab strip follows the WAI-ARIA Manual Activation tab pattern (arrow keys move focus, Enter/Space activates), role cards use `aria-expanded`/`aria-controls`, and the password change strength indicator uses an always-mounted `aria-live="polite"` region so screen readers receive strength announcements before the first keystroke.



## v8.2.0 (2026-05-09) - "Storage Depth"

Upgrade from: v8.1.0 - Drop-in.

### Added
- **ZFS Scrub Schedules**: A dedicated Scrub Schedules tab in the ZFS Storage page surfaces the existing scrub scheduler backend. Per-pool daily, weekly, and monthly scrub scheduling with time-of-day selection. Backed by systemd timers installed by the daemon.
- **SMB Protocol Options**: The Shares page now has a Protocol Options card with live toggles for three global SMB features: Time Machine (macOS), Shadow Copies (Windows Previous Versions), and Recycle Bin. Enabling Time Machine also writes an Avahi mDNS service advertisement file so macOS discovers the server automatically via Bonjour.
- **Shadow Copies (Previous Versions)**: ZFS snapshots are now surfaced as Windows Previous Versions via the Samba shadow_copy2 VFS module when the Shadow Copies toggle is enabled. The shadow format is tuned to match D-PlaneOS snapshot naming (auto-YYYYMMDD-HHMM).
- **MinIO S3 Object Storage**: Managed MinIO service providing local S3-compatible object storage. The new S3 Object Storage page supports configuration (root user, secret key, data volume path, API port, console port), service start/stop/restart, and writes the MinIO environment file and systemd unit. Listed under Storage in the nav.

### Changed
- ZFS Storage page gains a third tab (Scrub Schedules) alongside Pools and Encryption.
- Shares page gains a Protocol Options card above the share list.
- Shadow copy Samba config uses `shadow:snapprefix` to strip the frequency component (`auto-daily-`, `auto-weekly-`, etc.) so Previous Versions works correctly with the default Snapshot Scheduler naming.



## v8.1.0 (2026-05-09) - "Protocol Suite"

Upgrade from: v8.0.4 - Drop-in.

### Added
- **Scheduled rsync backups**: Recurring rsync jobs with hourly, daily, weekly, and monthly scheduling. Each schedule installs a systemd timer that calls back into the daemon via a localhost-only cron hook. Per-schedule run history tracks last execution time, status, and job ID. Full CRUD from the Backup page (new Schedules tab alongside Run Now).
- **FTP/FTPS server**: Full management of vsftpd including enable/disable, FTP vs. explicit TLS (FTPS) mode, port, passive port range, max clients, anonymous access, chroot, and TLS certificate paths. Allowed-user selection from the system user list controls who may connect. Service start/stop/restart with live status from systemd.
- **Shareable file download links**: Authenticated users create time-limited, optionally password-protected, optionally download-count-capped download tokens for individual files. Public download endpoint (`/api/s/{token}/download`) requires no session - the token is the credential. Links can be created via right-click in the File Explorer or from the dedicated File Share Links management page, which shows active and expired links with copy-to-clipboard and revoke actions.
- **SSH authorized key management**: Per-user SSH public key management writing directly to `~/.ssh/authorized_keys`. Supports all current key types (ed25519, RSA, ECDSA, sk-* hardware-backed variants). On first mutation for a user, any pre-existing keys in their authorized_keys file are auto-imported so no manually-added keys are lost. SSH daemon settings remain under NixOS configuration management; the page shows live daemon status and port as read-only.
- **Audit Log page routing**: The Audit Log page was implemented but not reachable via navigation. Now correctly routed at `/audit` with a nav entry under Security.
- **Backup page frontend**: The ad-hoc rsync backend existed but had no dedicated UI. BackupPage now provides source, destination, and options inputs, live job progress tracking, and a deletable task history table.

### Changed
- Backup page reorganised into Run Now and Schedules tabs to accommodate the new scheduling feature.
- Storage nav group expanded with FTP/FTPS and File Share Links entries. Identity nav group expanded with SSH Keys.



## v8.0.4 (2026-05-09)

Upgrade from: v8.0.3 - Drop-in.

### Added
- **GitOps MANUAL action type**: New `MANUAL` action in the reconciliation diff engine for pool changes that require manual `zpool` commands (topology modifications, health-flagged changes, GUID mismatches). These changes are surfaced in the plan result with the exact commands needed, without halting reconciliation of automatable changes. The GitOps page renders MANUAL items in warning color.

### Fixed
- **Pool topology drift silently dropped**: Pools with topology drift, health issues, or GUID mismatches were previously recorded as no-ops after applying automatable property changes. The reconciler now emits a MANUAL action so operators see exactly what requires intervention instead of the system appearing to be in sync.
- **Install script dead password generation**: Removed admin password generation code that was computed and printed during install but never written to the database. First-time setup now correctly directs users to the browser setup wizard.
- **Login page startup race**: On first boot while systemd services are still starting, the login page would render immediately against an unreachable daemon. The page now polls the daemon status every 3 seconds and shows "System is starting" during startup instead of exposing a broken form.
- **Setup wizard re-entry guard**: The setup wizard now checks at mount time whether setup has already been completed and redirects to login immediately, preventing accidental re-entry on already-configured systems.



## v8.0.3 (2026-04-10)

Upgrade from: v8.0.2 - Drop-in.

### Fixed
- **Session validation deadlock**: Database operations in session validation had no timeout - when ZFS pool state changed, the database could hang indefinitely causing API calls to fail. Added 5-second context timeout to all session validation queries.
- **Shares CRUD timeout**: Added 5-second timeout to shares_crud database queries to prevent hangs during storage issues.
- **Degraded mode**: When database is unavailable during storage failures, system now enters degraded mode - allowing READ operations but blocking mutations. This keeps the system operational instead of failing completely.

### Changed
- **Health endpoint public**: `/api/system/health` is now a public endpoint that doesn't require session validation. This ensures monitoring works even when authentication is degraded.
- **RBAC degraded handling**: When user.ID=0 (degraded mode), READ/list operations are allowed, mutations are blocked with clear error message.

### Security
- During degraded mode (database unavailable), only read operations are permitted. Write operations require confirmed session. This is acceptable for a NAS behind a firewall where availability trumps strict authentication during transient storage faults.



## v8.0.2 (2026-04-10)

Upgrade from: v8.0.1 - Drop-in.

### Fixed
- **Health endpoint timeout handling**: After pool offline/online operations, `zpool list` can hang because the pool is in a transitional state. The health endpoint now gracefully handles this timeout instead of failing the entire health check.
- **Session validation defensive**: Added database ping before session queries to detect connection issues. Session validation now returns "unauthorized" instead of failing hard when the database becomes temporarily unavailable during ZFS operations.

### Changed
- **Storage failure test**: Expanded test coverage to include more real-world scenarios that were previously causing false failures.



## v8.0.1 (2026-04-06)

Upgrade from: v8.0.0 - Drop-in.

### Fixed
- **Audit chain verification**: Added `DEFAULT (strftime('%s', 'now'))` to audit_logs table timestamp column, fixing CI verification failures.
- **TOTP handler error handling**: Fixed 11 locations in `daemon/internal/handlers/totp.go` where database operations were silently ignoring errors.
- **Storage failure test script**: Fixed incorrect API endpoint references (`/api/zfs/health` → `/api/system/health`), added missing directory creation, and simplified pool validation to prevent false failures.


## v8.0.0 (2026-04-04) - "GPU Passthrough"

Upgrade from: v7.5.3 - Drop-in for the daemon and UI; enable NVIDIA Container Toolkit in NixOS only if you deploy NVIDIA compose stacks.

### Added
- **NVMe-oF target (nvmet)**: Export ZFS zvols over **NVMe/TCP** via kernel configfs - alternative block data plane to ZFS send/recv. Persisted at `/var/lib/dplaneos/nvmet-targets.json`, applied atomically to nvmet. **API**: `GET/POST/PUT/DELETE /api/nvmet/targets`, `GET /api/nvmet/status`, `GET /api/nvmet/zvols`. **GitOps**: optional `fabrics.nvme` in `state.yaml` (omit `fabrics:` if you only use the UI). Host NQN allow-list or `allow_any_host`. **UI**: Storage → **NVMe-oF**, plus cross-link from Replication.
- **NixOS**: `nvmet` / `nvmet-tcp` kernel modules; `dplaned` may write `/sys/kernel/config` (module + standalone `configuration.nix`).
- **GET /api/docker/gpu**: Host GPU passthrough report - PCI display-class devices (`lspci -nn`), `/dev/dri` nodes, `nvidia-smi` when available, Docker `Runtimes` (including `nvidia`), compose hints, NixOS module option name, and copy-paste compose YAML examples for NVIDIA reservations and DRI bind-mounts.
- **Compose GPU preflight**: Before stack deploy, YAML update, GitOps stack create, and template-driven `compose up`, the daemon validates NVIDIA and DRI requirements from the compose text against the live host (Linux only). Failures return explicit errors; compose files are not silently rewritten.
- **NixOS `services.dplaneos.docker.enableNvidia`**: When true, sets `virtualisation.docker.enableNvidia` for the NVIDIA Container Toolkit. Host driver installation remains operator-owned.
- **Docker UI**: **GPU / hardware** tab showing the passthrough report; **Deploy Compose Stack** now calls `POST /api/docker/stacks/deploy` and handles the synchronous response (fixes the broken `/api/docker/compose/deploy` + job flow).

### Changed
- **GitOps `docker compose`**: Stack up/down uses `--project-directory` for consistent context with the stacks API.

### Notes
- **`pciutils`** is added to the daemon systemd `path` in `module.nix` and to the template `configuration.nix` so `lspci` is available for discovery.


## v7.5.3 (2026-04-04) - "Operational Depth"

Upgrade from: v7.5.2 - Drop-in. `sudo bash install.sh --upgrade`

### Added
- **GET /api/system/disks**: Disk discovery for the UI and operators-stable `/dev/disk/by-id/` paths and metadata for replacement workflows and GitOps authoring.
- **Persist guard (Linux)**: Background monitor for `/persist` usage; at high utilization runs `journalctl` vacuum and rotates largest log files under D-PlaneOS and Samba log dirs to reduce risk of etcd/journal/DB filling the partition.
- **`zfs send -P` progress**: Parser and wiring for replication and HA send paths so job progress and WebSocket clients receive percent, throughput, and ETA from `zfs send -P` stderr.
- **GitOps `zpool create` argument builder**: Shared topology-to-argv helper for consistent, validated `zpool create` construction.
- **NixOS console network wizard**: Optional `dplaneos-console-net` (gum TUI) for emergency static IPv4 when DHCP fails; module wiring and boot hint option.
- **Regression test**: `TestParseStateYAML_EmptyStarter` ensures the empty first-run GitOps `state.yaml` template (with comments) still parses.

### Changed
- **GitOps desired state**: Safer default `state.yaml` template and schema documentation no invented disk paths; operators use real by-id values from this system.
- **HA package documentation**: Package comment describes the implemented Patroni startup and import guards (no backlog wording).
- **Pools UI**: Disk replacement flow improvements; “Wipe disk” modal uses correct title and boolean state (no string sentinel).
- **ZFS property allowlist**: `zfs set` validation allows `xattr` and `secondarycache` where appropriate.

### Fixed
- **Job progress persistence**: `LatestProgress` is stored on the job, included in snapshots for `GET /api/jobs/{id}`, and cleared on completion or failure so stale progress does not leak across jobs.

### Docs
- Threat model and showstopper guide: maintenance wording and capability section title aligned with shipped behavior.


## v7.5.2 (2026-04-02) - "Ironclad HA"

Upgrade from: v7.5.1 - Drop-in. `sudo bash install.sh --upgrade`

### Added
- **STONITH Jitter (Mutual-Destruction Prevention)**: Both nodes in an HA pair previously had no coordination mechanism to prevent simultaneous STONITH - each could fence the other at exactly the same moment, killing both. A cryptographically random pre-fire delay (`crypto/rand`, 0–3000 ms by default, configurable up to 30 s) is now applied before the BMC power-off command is issued. When both nodes hit the `FailoverAfter` threshold simultaneously, they each draw independent random delays; the node with the shorter delay fires first, and the fenced node dies before its own delay elapses. The jitter window is configurable via `jitter_max_ms` on the fencing config.
- **Witness Array with N-of-M Quorum**: The single witness URL is replaced by a configurable array of `WitnessEntry` objects. Each entry supports optional strict TLS enforcement, expected HTTP status code matching, and body regex validation. The `required_healthy` field sets the quorum threshold - e.g. `required_healthy: 2` with three witnesses requires at least two to pass before auto-failover proceeds. All witnesses are probed concurrently. The configuration is validated at save time: invalid regexes are rejected with a descriptive error before they can silently disable failover at probe time.
- **Assertive Probing**: Witness probes now go beyond basic TCP reachability. Per-entry options: `strict_tls: true` enforces certificate verification; `expected_status` validates the HTTP response code; `expected_body_regex` reads the first 1 KB of the response body and matches against a compiled regex. A probe only passes when all configured checks succeed.
- **PDU Out-of-Band STONITH**: A networked PDU (Digital Loggers, iBoot, Raritan, etc.) can now be registered as a secondary fencing method. When IPMI fencing is enabled but fails, the daemon automatically falls back to an HTTP GET or POST to the configured PDU outlet-off URL, with optional HTTP Basic Auth via a password file and an exact-status-code response check. Neither promotion nor split-brain exposure occurs unless at least one fencing method confirms the peer is dark.
- **Zombie Reconciliation (TXG Boot Check)**: On daemon start, before the heartbeat loop is initialised, the local ZFS pool TXG (Transaction Group ID) is compared against the active peer's TXG via the peer's `/api/ha/sync/status` endpoint. If the local pool is stale (lower TXG), the node enters `SubordinateMode`: the local pool is set `readonly=on` and a full ZFS catch-up is initiated over SSH from the active peer. Once the catch-up completes, `readonly` is lifted and normal HA operation resumes. This prevents a rebooted node with stale data from winning a promotion race.
- **Flapping Defense (Hysteresis)**: A 60-minute suppression window is enforced after any automated failover. During the hysteresis window, `checkFailover()` will not trigger another promotion, preventing ping-pong flapping caused by an unstable peer that repeatedly crosses and recovers from the `FailoverAfter` threshold. The window start time is persisted to `ha_cluster_state` so it survives daemon restarts. Operators can clear the window early via `POST /api/ha/clear_fault`.
- **New API Endpoints**:
  - `GET /api/ha/pdu/configure` - read PDU fencing configuration
  - `POST /api/ha/pdu/configure` - save PDU fencing configuration
  - `GET /api/ha/sync/status` - return local ZFS pool TXG values (public endpoint, used by peer reconciliation)
  - `POST /api/ha/clear_fault` - clear hysteresis window and/or subordinate mode to re-enable automated failover
- **Full UI Exposure**: All new HA features are accessible through the web interface. The HA page adds a Subordinate Mode operational banner (amber, with confirm-gated "Clear Fault" button), a Hysteresis Active banner, a "Last Failover" stat card, a Witness Array configuration form with per-entry TLS/status/regex controls and live quorum-test output, a PDU fencing configuration form, and a `jitter_max_ms` slider on the fencing card. The setup wizard is extended with a dedicated Quorum Witness step.

### Fixed
- **`loadPersistedNodes` zero-time LastSeen**: `lastSeenUnix` was scanned from the DB but never assigned to `n.LastSeen`. `time.Since(time.Time{})` ≈ 56 years, which instantly exceeds `FailoverAfter = 45 s`, causing STONITH against all persisted peers every time the daemon restarted. Fixed by assigning `n.LastSeen = time.Unix(lastSeenUnix, 0)` after the scan.
- **`ReplicationConfig` JSON tag mismatch**: The `IntervalSecs` field was tagged `json:"interval_seconds"` while the database column and all API payloads used `interval_secs`. Silent decode failures caused `IntervalSecs` to always deserialise as 0, defaulting the replication loop to 30 s regardless of the configured value. Fixed by aligning the tag to `json:"interval_secs"`.
- **Missing scan error check in `loadPersistedNodes`**: `rows.Scan` errors were not checked, silently storing zero-valued peer entries. Also added `rows.Err()` check after the iteration loop.

### Changed
- **Fencing config `jitter_max_ms`**: New field on `FencingConfig`. Default 3000 ms (3 s window). Clamped to [0, 30000] at save time.
- **Witness config schema**: `ha_witness_config.witnesses_json` replaces the former single `url TEXT` column, storing a JSON array of `WitnessEntry` objects.


## v7.5.1 (2026-03-30) - "Zero-Touch HA"

Upgrade from: v7.5.0 - Drop-in. `sudo bash install.sh --upgrade`

### Added
- **Quorum Witness for Zero-Touch HA**: Automated failover now requires a reachable quorum witness before executing STONITH and promotion. When a peer exceeds the 45-second FailoverAfter threshold and fencing is enabled, the daemon probes a configurable HTTP witness endpoint. If the witness is unreachable the failover is suspended - protecting against false-positive promotion during network partitions where this node cannot distinguish "peer is dead" from "I am isolated". Any HTTP response (any status code) from the witness counts as reachable; a connection error or timeout counts as isolated.
- **Witness API**: Three new endpoints under admin RBAC:
  - `GET /api/ha/witness/configure` - read current witness configuration
  - `POST /api/ha/witness/configure` - save witness `{ "enable": true, "url": "http://...", "timeout_secs": 5 }`
  - `POST /api/ha/witness/test` - probe the configured URL (or an ad-hoc URL in the request body) and return `{ "reachable": true/false }`
- **Witness status in HA status**: `GET /api/ha/status` now includes a `witness` key with the current witness configuration alongside the cluster status.
- **`ha_witness_config` schema**: New single-row DB table storing witness parameters, created automatically on daemon start.

### Behavior
- If witness is **disabled** (default): `checkFailover()` behavior is identical to v7.5.0 - fencing + promotion fires when fencing is enabled and peer breaches FailoverAfter. No change.
- If witness is **enabled**: The witness gate is evaluated between the fencing-enabled check and the maintenance-mode check. An unreachable witness suspends auto-failover; a reachable witness allows it to proceed.
- Witness probe logs at the 3rd missed beat to avoid log spam on subsequent 15s ticks.


## v7.5.0 (2026-03-31) - "Runtime Integrity"

Upgrade from: v7.4.6 - Drop-in. `sudo bash install.sh --upgrade`

### Fixed
- **HA Peer Health Check Panic**: `pingPeer()` dereferenced `resp` before checking the HTTP error, causing a nil-pointer panic when a peer was unreachable. The condition is now split so `resp.StatusCode` is only accessed after confirming `err == nil`. Response body is now closed in all non-error branches, including non-200 responses.
- **Silent GitOps Credential Failure**: Three `QueryRow.Scan()` calls in `gitops/commit.go`, `git_util.go`, and `drift.go` dropped their errors, causing git operations to proceed with zero-value repo URLs or credential IDs. Errors are now propagated and logged, aborting the affected operation cleanly.
- **ZFS Restore Double-Close**: The `io.Pipe` write-end in the dataset restore path was closed both on the early-return error branch and by the background goroutine that waits for `sendCmd`. On the `recvCmd.Start()` failure path, `sendCmd` was left running as an orphaned process. The goroutine now uses `sync.Once` to guarantee a single close, `sync.WaitGroup` to ensure it exits before the caller returns, and `sendCmd.Process.Kill()` + `Wait()` to reap the orphan.
- **Group Member Cleanup Error Discarded**: `deleteGroup` used `_, _ = db.Exec(...)` to clear members, explicitly swallowing the error. Failures are now logged before the group delete continues.

### Security
- **Trusted Proxy Enforcement**: `RealIP()` previously trusted `X-Forwarded-For` and `X-Real-IP` headers from any non-loopback address, enabling IP spoofing on multi-NIC or VLAN-segmented deployments. A CIDR-based allow-list (RFC 1918 + loopback + IPv6 ULA) is now enforced - headers are only honoured when the direct connection originates from a trusted proxy range.

### Reliability
- **Clean Goroutine Shutdown**: `ha.Manager`, `gitops.DriftDetector`, and `monitoring.BackgroundMonitor` now track their background goroutines with `sync.WaitGroup`. `Stop()` on each component blocks until the goroutine has fully exited, eliminating use-after-free and map-write-after-close races during daemon teardown.
- **Background Monitor Deadlock Prevention**: `BackgroundMonitor.stopChan` was unbuffered; if the `run()` goroutine was between ticks when `Stop()` was called, the send blocked indefinitely. The channel is now buffered (`make(chan bool, 1)`).
- **ZED Listener Context Support**: The ZFS Event Daemon Unix-socket Accept loop ran forever with no cancellation mechanism. It now accepts a `context.Context` and polls with a 1-second deadline, exiting cleanly when the daemon context is cancelled at shutdown. `daemonCtx` is created in `main.go` and cancelled as the first action in the shutdown sequence.
- **DB Query Timeouts in Drift Detector**: Both `QueryRow` calls in `DriftDetector.loop()` and `runCheck()` are replaced with `QueryRowContext` using a 5-second deadline, preventing a hung database connection from blocking the drift-check goroutine indefinitely.
- **Async DB Write Observability**: The fire-and-forget `go db.Exec()` for `last_used` token updates now logs errors. Silent `db.Exec()` calls in `Logout()` for session deletion and in `Login()` for `last_login` updates also now log on failure.

### Changed
- **HA Manual Promote - Split-Brain Prevention**: `POST /api/ha/promote` previously called `ExecutePromotion` directly with a documented warning that split-brain would occur if the primary was still alive. The handler now sequences STONITH fencing before promotion when fencing is configured: the leader node's BMC receives a chassis power-off command and the chassis state is polled until confirmed dark before promotion begins. If fencing is not configured, a warning is logged to the job stream and promotion continues, leaving split-brain avoidance to the operator. The operation is wrapped in the jobs system for real-time progress streaming.
- **VPN Network Action Response**: The generic `501 Not Implemented` for `add_*`/`remove_*` network actions is replaced with a targeted check for `vpn`, `add_vpn`, and `remove_vpn` that returns a descriptive message directing operators to deploy containerised VPN solutions (wg-easy, Tailscale, OpenVPN) via the Docker interface. Unrecognised actions now fall through to the existing `400 Bad Request` path.
- **Dead Code Removed**: The `checkDone` channel in the ACME proxy-verification handler was allocated and written to but never received from. Removed.

---

## v7.4.6 (2026-03-29) - "Security Guard Hardening"

Upgrade from: v7.4.5 - Drop-in. `sudo bash install.sh --upgrade`

### Security
- **Hybrid System User Protection (#17)**: Implemented multi-tier protection in `deleteUser` handler using UID < 1000 check (POSIX), explicit hardcoded blocks for `root/admin/dplaneos`, and static fallback list for common services.
- **RBAC Migration Phase 2 (#38)**: Expanded mission-critical route coverage with `permRoute` wrappers for 40+ endpoints, including ZFS replication streams (elevated to `storage:admin`) and network confirmation.
- **User/Group Management Regression (#39)**: Restored explicit RBAC protection by splitting user and group creation endpoints into dedicated POST registrations.
- **SMB Write-Time Sanitization (#30)**: Hardened share creation/update logic with mandatory input sanitization before database persistence, eliminating the PostgreSQL state as a potential injection vector.

---

## v7.4.5 (2026-03-29) - "Reconciliation Hardening"

Upgrade from: v7.4.4 - Drop-in. `sudo bash install.sh --upgrade`

### Added
- **WebSocket Streaming Console (Industrial Polish)**: 
    - Introduced a real-time, high-performance terminal console using `xterm.js` for all reconciliation and system update tasks.
    - **History Replay**: Implemented a 1,000-line log ring buffer in the daemon, allowing the UI to instantly "replay" console history upon connection or refresh mid-job.
    - **Global Job Indicator**: Added a persistent status indicator in the `TopBar` that monitors active background tasks system-wide, allowing users to minimize the console and multi-task without losing observability.
- **Zombie Lock Protection**: Implemented a 10-minute hard-stop timeout (`TimeoutExtreme`) for all critical system reconfiguration paths (`nixos-rebuild switch`), ensuring the global `ReconcileLock` is always released even in the event of hung external processes.
- **Regression-Aware Service Probing**: Expanded the post-apply verification engine to perform exhaustive TCP liveness checks against all configured services (SMB, NFS, API) plus mandatory management ports (SSH/22, API/9000), acting as a digital nervous system to detect functional regressions immediately.

---

## v7.4.4 (2026-03-28) - "Authorization Coverage"

Upgrade from: v7.4.3 - Drop-in. `sudo bash install.sh --upgrade`

### Security
- **RBAC Coverage for Storage Operations**: Applied proper role/action permission checks to previously session-only endpoints: trash management (list/move/restore/empty), power management (disk status/spindown), ACL get/set, snapshot schedules, and replication schedule management. Any authenticated user could previously invoke these operations regardless of their assigned role.
- **Duplicate Route Removed**: Eliminated a duplicate registration of `/api/zfs/snapshots/cron-hook` that created an ambiguous handler binding. The canonical registration in the snapshot scheduler block now correctly handles this route.

---

## v7.4.3 (2026-03-28) - "Physical Truth"

Upgrade from: v7.4.2 - Drop-in. `sudo bash install.sh --upgrade`

### Added
- **Forensic Compliance Engine (Physical Truth)**: 
    - Introduced a kernel-level forensic probe that uses `nft -j` to extract the live firewall state directly from the Linux kernel.
    - **Divergence Detection**: The system now automatically detects and flags "Shadow Ports" (manually opened via CLI/SSH) that deviate from the declarative D-PlaneOS intent.
    - **Integrity Monitor (Pro/Compliance)**: A real-time audit dashboard in the Compliance Engine that warns administrators of physical state drift before generating official SOC2 reports.
    - **Certified Evidence**: Forensic probe results are now embedded directly into the "Persistence Proof" section of PDF compliance reports.
- **Security Whitelisting**: Safely integrated the forensic probe into the `cmdutil` whitelist, ensuring zero-bypass security for high-privilege kernel operations.

---

## v7.4.2 (2026-03-27) - "Core Structural Integrity"

Upgrade from: v7.4.1 - Drop-in. `sudo bash install.sh --upgrade`

### Security
- **Strict Role-Based Access Control Boundaries**: Applied proper `permRoute` RBAC wrappers to critical HTTP streaming endpoints (`/ws/terminal`, `/ws/monitor`, `/api/system/logs/stream`), closing permission escalation loopholes that allowed users without administrative privileges to access sensitive system data or shells.
- **Fail-Closed Dataset Management**: Hardened the ZFS GitOps continuous reconciler. A failure to read dataset usage stats no longer returns 0 bytes; it correctly propagates the system fault to abort any scheduled deletion actions, eliminating a potential zero-byte data loss vector.
- **Resilient ZFS Heartbeats**: Fixed a defect in the storage heartbeat loop where catastrophic ZFS pool loss (un-importable/destroyed pools returning non-zero exit codes) failed to trigger `CRITICAL` system alerts and automatic Docker service suspension.

---

## v7.4.1 (2026-03-27) - "Security Polish & Determinism"

Upgrade from: v7.4.0 - Drop-in. `sudo bash install.sh --upgrade`

### Added
- **Detailed Firewall Diffs**: GitOps now provides granular "add/remove" descriptions for firewall port changes.
- **Config Determinism**: Automated sorting of DNS, NTP, and firewall port lists to ensure consistent state and minimize unnecessary system changes.
- **System User Protection & Hierarchy**: 
    - Implemented a hierarchical RBAC model for user management.
    - Mandatory "Current Password" verification for all sensitive user and group mutations.
    - Lockout prevention: Added a guard against deactivating or deleting the last remaining active admin.
    - Protected system service accounts (`root`, `dplaneos`) from deletion.

### Fixed
- **SMART Cron-hook Conflict**: Resolved a race/auth conflict between session bypass and RBAC middleware for internal systemd timers.
- **Dynamic Pool Protection**: Hardened the ZFS pool root deletion guard to automatically discover and protect all mounted pools, including nested datasets.

---

## v7.4.0 (2026-03-27) - "Security Hardening Patch"

Upgrade from: v7.3.0 - Drop-in. `sudo bash install.sh --upgrade`

### Added
- **Automated CSRF Protection**: Implemented session-linked CSRF token validation for all mutating API requests.
- **Unified Command Execution**: Hardened the `cmdutil` layer with a zero-bypass, whitelist-by-default architecture for all privileged operations.
- **File System Safeguards**: Added recursive deletion guards for ZFS pool roots and hardened file operations with middleware-backed user context.
- Stabilized CI/CD pipeline and command whitelist validation.
- **Local Hook Authentication**: Bypassed session middleware for local loopback requests to enable scheduled snapshots and SMART monitoring without external auth headers.

### Fixed
- **Input Sanitization**: Resolved potential injection vulnerabilities in SMB share configurations and snapshot prefixes.
- **Metrics Response Injection**: Hardened the metrics history endpoint against JSON response injection.
- **Setup Race Condition**: Eliminated concurrent administrative initialization risks via database advisory locks.

---

## v7.3.0 (2026-03-25) - "Enterprise Directory Services"

Upgrade from: v7.2.0 - Drop-in. `sudo bash install.sh --upgrade`

### Added
- **Multi-Provider Directory Engine**
    - **Active Directory (Windows)**: Full domain member support with `security = ads`, `winbind`, and Kerberos SSO.
    - **OpenLDAP (Linux)**: Advanced LDAP integration with standard schema support.
    - **Open Directory (MacOS)**: Specialized support for Apple's directory service, including Mac-specific attribute mapping.
- **Enterprise-Grade Identity Mapping**
    - Deterministic `IDMAP` configuration for consistent UID/GID mapping across SMB, NFS, and local shell sessions.
    - Support for both `rid` and `ad` backends.
- **Seamless UI Integration**
    - Redesigned "Directory Service" page with Provider Presets and guided AD Join workflow.
    - Real-time "Join Status" tracking and NTP synchronization verification.
- **Transparent SMB Authentication**
    - Bridged Samba into the Active Directory domain for transparent Kerberos-based file share access on Windows clients.
- **Native Audit Transparency (Enterprise Polish)**
    - Restored local audit log visibility with paginated table and filtering, protected by RBAC and stealth licensing logic.
- **Unified Sharing & ZFS Explorer**
    - Integrated SMB and NFS share management directly into the ZFS dataset tree.
    - Added native UI support for ZFS snapshots, rollbacks, and recursive child creation.
- **CI/CD & ISO Release**
    - Unified the build cycle to produce a v7.3.0 "Golden Image" ISO featuring all new features out-of-the-box.

---

## v7.2.0 (2026-03-24) - "Hermetic Firewall"

Upgrade from: v7.1.0 - Drop-in. `sudo bash install.sh --upgrade`

### Added
- **Nix-Native Firewall Infrastructure**
    - Engineered a native NixOS firewall bridge using `NixWriter` to bypass the removal of `ufw` in NixOS 25.11/24.11.
    - **Firewall UI Parity**: Implemented a `ufw status` reporter for NixOS to maintain a consistent user experience across all supported distributions.
    - **Declarative Persistence**: Firewall rules are now updated in `dplane-state.json` and applied automatically via the NixOS module.
- **Hardened ISO Build Architecture**
    - **De-recursive Flake**: Resolved infinite evaluation loops in `flake.nix` by decoupling the ISO build from system configurations.
    - **Explicit Image Targeting**: Transitioned to a `mkIso` pattern that passes the `targetSystem` as a concrete derivation for stable generation.
- **Offline-First Installer**
    - Removed network-dependent `nix run` calls from `nixos/install.sh`.
    - All required partitioning and TUI tools are now pre-baked into the ISO for reliable air-gapped installations.

### Fixed
- **NixOS 25.11 Compatibility**: General cleanup and removal of deprecated package references to ensure full compatibility with the latest Nixpkgs channel.
- **Evaluation Resilience**: Fixed a critical redundancy issue in `flake.nix` where system configurations were being evaluated multiple times during the ISO build.
- **Build Infrastructure Resilience**:
    - **Go Build Stabilization**: Resolved `runtime/cgo` and `go.mod` pathing errors by disabling CGO and correctly scoping the daemon build to the `daemon/` directory.
    - **First-Boot Authentication Bridge**: Implemented auto-seeding of the administrator account from the installer's TUI password, ensuring a seamless offline "First Boot" experience.
    - **Self-Contained Vendoring**: Transitioned to a fully-committed `vendor/` directory to eliminate CI dependencies on external Go proxies and avoid ephemeral `vendorHash` mismatches.
    - **Metadata Restoration**: Recovered the `VERSION` file and ensured consistent version injection across all build artifacts.

---

## v7.1.0 (2026-03-23) - "High Availability Nexus"

Upgrade from: v7.0.0 - Drop-in. `sudo bash install.sh --upgrade`
    - **Patroni & etcd Orchestration**: Native NixOS module for automated PostgreSQL consensus and failover.
    - **HAProxy Service Mesh**: Transparent traffic routing to the active cluster leader.
    - **Keepalived Virtual IP**: Automated floating IP migration for zero-downtime client access.
- **Guided HA Setup Wizard**
    - Interactive 5-step UI process for safe cluster arming and configuration.
    - Automated background NixOS reconfiguration during setup.
    - Pre-flight prerequisite verification for networking and quorum.
- **Intelligent Fencing (STONITH)**
    - Secure out-of-band power management via IPMI Redfish/IPMIspec.
    - Hardened execution whitelist with regex-based argument validation.
    - Zero-leak password handling via environment variable injection.
- **Continuous Storage Replication**
    - High-performance ZFS snapshot shipping for asynchronous Active-to-Standby data sync.
    - Real-time replication telemetry and bottleneck detection.
- **Security & Robustness Hardening**
    - **Mandatory Command Whitelisting**: Integrated structural security validation into the `cmdutil` execution layer.
    - **Failover State Protection**: Added a `fencingInProgress` flag to ensure atomic STONITH/Promotion sequences.
    - **Startup Split-Brain Guard**: Implemented Patroni health checks to block automatic ZFS imports on replica nodes.
    - **Job-Based Setup Wizard**: Updated the toggle HA process to leverage the jobs system for real-time progress feedback.

### Changed
- **NixOS Bridge**: Integrated HA enablement status into the declarative `dplane-state.json` fragment.
- **Cluster Monitoring**: Real-time visual topology tracking for quorum, node health, and Patroni roles.

---

## v7.0.0 (2026-03-22) - "PostgreSQL Ascension"

Upgrade from: v6.2.0 - **BREAKING CHANGE**. This release replaces SQLite with PostgreSQL. 
Manual database migration is required or start with a fresh install.

### Added
- **Architectural Shift: PostgreSQL Core**
    - Completely replaced SQLite with PostgreSQL 15+ as the primary metadata engine.
    - Improved concurrency and scalability for large-scale storage environments.
    - Standardized on `pgx` for high-performance PostgreSQL driver support with robust connection pooling.
- **CI/CD Pipeline v2**
    - Integrated PostgreSQL service containers into all automated test stages (`prepare`, `validate`, `convergence`, `integration`).
    - Implemented **Multi-Database Isolation** for fleet integration tests, enabling clean parallel node simulations on a single PG instance.
    - Unified release process with automated checksum generation and multi-architecture packaging.
    - Refined GHA metadata handling to use explicit outputs, resolving previous context access lint warnings.
- **Installer Enhancement**
    - Added `--db-dsn` support to `install.sh` for external PostgreSQL connectivity.
    - Automated systemd environment injection for secure database credential management.
    - Transitioned to native `postgresql-client` for database bootstrapping and maintenance.

### Changed
- **Database Layer**: Migrated all 40+ handlers to PostgreSQL-compatible SQL syntax ($1 placeholders, RETURNING clauses, etc.).
- **Global Search**: Ported SQLite FTS5 file search to a more robust PostgreSQL implementation.
- **Audit Logging**: Hardened audit trail persistence with PostgreSQL's strong consistency guarantees.

---

## v6.2.0 (2026-03-22) - "Cryptographic Sovereignty"

Upgrade from: v6.1.2 - Drop-in. `sudo bash install.sh --upgrade`

### Added
- **Automated Certificate Management (ACME)**
    - Integrated `go-acme/lego` for automated Let's Encrypt certificate issuance via HTTP-01 challenges.
    - **Hardened Background Job**: Moved ACME acquisition to a non-blocking background job with real-time progress tracking (Registering, Validating, Obtaining).
    - **Account Key Persistence**: ACME keys are now persisted to `/etc/dplaneos/acme_account.key`, ensuring identity reuse and avoiding Let's Encrypt rate limits.
    - **Pre-flight Proxy Verification**: Added a "Verify Proxy" diagnostic tool to ensure port 80/8080 proxying is correctly configured before starting the challenge.
    - **Automated NixOS Proxy**: The NixOS module now automatically configures Nginx to proxy `/.well-known/acme-challenge/` to the daemon.
    - Added support for manual certificate and private key imports via the new `ImportModal`.
    - Restored and hardened the self-signed certificate generation with SAN (Subject Alternative Name) support.
- **Real-time Replication Telemetry**
    - Implemented asynchronous progress tracking for ZFS replication jobs.
    - Backend now parses `zfs send -P` stderr in real-time to broadcast percentage, throughput, and ETA.
    - Updated `JobStatusBanner` in the Replication page with a live, high-fidelity progress bar.
- **Advanced ZFS Operations**
    - Implemented snapshot holds (`zfs hold`, `zfs release`) to protect critical snapshots from accidental deletion.
    - Added mirrored pool split (`zpool split`) support, allowing users to safely split mirrors into independent pools.
    - Added dedicated API endpoints and security whitelisting for all advanced ZFS management.
- **Production Hardening**
    - **ACME Auto-Renewal**: Added background expiry checking and automated renewal (30 days before expiration) via systemd timers.
    - **Nginx Challenge Automation**: Automated challenge proxy configuration for non-NixOS systems (idempotent injection and validation).
    - **NixOS Compatibility**: Secured installer path guards and fixed internal references for seamless NixOS coexistence.
    - **GitOps Consistency**: Standardized state persistence across all handlers using asynchronous Git hooks.
- **Session Control & Security**
    - Introduced a dedicated "Sessions" management tab in the Users & Groups page.
    - Users can now view all active web sessions, including IP addresses, device types (User-Agent), and last activity timestamps.
    - Added granular session revocation, allowing users to forcefully log out other devices.
    - Hardened the `sessionMiddleware` to support immediate invalidation of revoked sessions across all API endpoints.

### Fixed
- **NixOS Path Resilience**: Fixed a hardcoded `/usr/bin/curl` path in the system handlers, ensuring binary resolution follows the system `PATH` for NixOS compatibility.
- **API Route Registration**: Properly registered all new certificate and session management endpoints in the main daemon router.

## v6.1.2 (2026-03-22) - "NixOS Path Convergence"

Upgrade from: v6.1.1 - Drop-in. `sudo bash install.sh --upgrade`

### Added
- **Exhaustive NixOS Compatibility**
    - Completed the project-wide removal of hardcoded absolute paths (`/usr/bin/*`, `/bin/*`, `/sbin/*`) across all shell scripts, systemd unit templates, and internal handlers.
    - All system commands now rely on the system `PATH` for resolution, ensuring 100% compatibility with NixOS store paths while maintaining standard Linux support.
- **CI/CD Build Integrity**
    - Fixed the release pipeline trigger to ensure automated publishing of release tarballs on version tags.
    - Ensured consistent versioning across all build artifacts.

---

## v6.1.1 (2026-03-21) - "Real-time Monitoring Overhaul"

Upgrade from: v6.1.0 - Drop-in. `sudo bash install.sh --upgrade`

### Added
- **Systemic WebSocket Architecture**
    - Integrated real-time push notifications into the central `DispatchAlert` hub, enabling immediate UI toasts for Capacity Guardian and S.M.A.R.T. failures.
    - Added standardized `job.completed` and `job.failed` WebSocket broadcasts to the core `jobs` system, providing rich metadata (`job_id`, `job_type`, `success`, `message`).
- **ZFS Progress Overhaul**
    - Refactored ZFS resilver and scrub status parsing into a reusable `zfs` package, ensuring consistent telemetry across all callers.
    - Eliminated client-side polling for ZFS operations by pushing `zfs.resilver.progress` and `zfs.scrub.progress` events directly from the backend.
    - Upgraded the `BackgroundMonitor` to periodically scrape and broadcast live ZFS status.
- **NixOS Management Hardening**
    - Refactored the NixOS rebuild logic (`ApplyWithWatchdog`) into a non-blocking background job, preventing dashboard timeouts and providing step-by-step progress through the `jobs` API.
    - Integrated the watchdog lifecycle directly into the background job for reliable auto-rollback.
- **Monitoring & Replication Gaps**
    - Added real-time status broadcasts for replication schedule transitions (`replication.schedule_updated`).
    - Ensured all systemic alerts are non-blocking to prevent system delays during notification bursts.

---

## v6.1.0 (2026-03-21) - "VDEV Sentinel"

Upgrade from: v6.0.6 - Drop-in. `sudo bash install.sh --upgrade`

### Added
- **Extended VDEV Operations**: Full backend and UI support for `zpool attach` (mirroring), `zpool detach`, and `zpool replace`.
- **Hardware Topology Viewer**: Interactive indented tree view of ZFS pool structure (Mirrors, RAIDZ, Special VDEVs).
- **VDEV-Aware Pool Repair**: Updated the Pool Fixer Wizard to correctly identify and guide replacement of failed disks within any VDEV sub-group.
- **NixOS Native Scheduling**: Migrated ZFS scrubbing and snapshotting from legacy cron to native systemd timers for enterprise-grade NixOS compatibility.
- **Dynamic Timer Management**: Internal generator for transient systemd units that survive reboots and provide granular state tracking.

### Security
- **Whitelist Hardening**: Expanded execution whitelist to include `wipefs`, `labelclear`, and specific `zpool` subcommand variants.
- **Path Validation Enforcement**: Replaced all legacy string-based validation with unified `security.ValidateDevicePath` for `by-id` path safety.
- **Disk Wiping Safety**: Implemented real-time pool membership checks in the `WipeDisk` handler to prevent data loss on active storage members.

---

## v6.0.6 (2026-03-21) - "Hardened Core"

Upgrade from: v6.0.5 - Drop-in. `sudo bash install.sh --upgrade`

### Security
- **Execution Whitelist Hardening**: Improved command pattern validation for `zpool`, `zfs`, `ufw`, `ip route`, and `openssl`.
- **ZFS Property Safety**: Implemented strict allowlists for `zfs set` properties and validated `mountpoint`/`quota` values.
- **Path Traversal Defenses**: Enhanced `IsValidPath` with mandatory `filepath.Clean` normalization and explicit rejection of dot-slash patterns.
- **Binary Path Normalization**: Removed all hardcoded absolute paths (`/usr/bin/*`, `/bin/*`, `/usr/sbin/*`) from the entire `daemon/` codebase, ensuring 100% compatibility with NixOS and non-standard Linux distributions.
- **Whitelist Synchronization**: Aligned `chown`/`chmod` security patterns with file handler base paths to ensure UI consistency across all managed storage.

### Fixed
- **Storage Persistence**: Fixed a critical bug in `writeFileContent` (handlers/zfs_operations.go) where binary content was being ignored during network and system configuration writes.
- **Switch Optimizations**: Optimized state handling switches in `capacity_guardian.go` and `system_extended.go` for better performance and readability.

---

## v6.0.5 (2026-03-20) - "NixOS & GitOps Hardening"

Upgrade from: v6.0.4 - Drop-in. `sudo bash install.sh --upgrade`

### Added
- **Hardened GitOps Engine**: Implemented `git pull --rebase` before pushing to prevent sync conflicts and enforced Git identity (`user.name`/`user.email`) for all commits.
- **NixOS Path Normalization**: Removed all hardcoded absolute paths (`/usr/bin/*`, `/sbin/*`) across the system, enabling full compatibility with NixOS and non-standard distributions.
- **Resilience Guards**: Added automated existence checks for critical binaries (ZFS, Docker, Samba) with descriptive error reporting.
- **Audit Log Refactoring**: Migrated audit log rotation to the internal Go SQL driver, removing the external `sqlite3` dependency.
- **Asynchronous Persistence**: Refactored background commits to be non-blocking, ensuring the UI remains responsive during slow Git operations.

---

## v6.0.4 (2026-03-20) - "System-Wide CRUD Consistency"

Upgrade from: v6.0.3 - Drop-in. `sudo bash install.sh --upgrade`

### Added
- **System-Wide CRUD Enhancement**: Implemented/Hardened Create, Read, Update, and Delete (CRUD) operations across the entire platform.
  - **Storage**: Added "Edit" and "Delete" (with safety confirmation) for ZFS Datasets and Snapshot Schedules.
  - **Networking**: Added "Edit" for Firewall Rules and "Remove" for VLANs and Bonds.
  - **Services**: Implemented "Edit" for iSCSI Targets, Cloud Sync remotes, and Replication Schedules.
  - **Security**: Added full CRUD for RBAC Roles and confirmed Users/Groups consistency.
- **Unified GitOps**: Consolidated GitOps and Git Sync into a single hub with full CRUD for Repositories and Credentials.

---

## v6.0.3 (2026-03-20) - "System Sync Core"

Upgrade from: v6.0.2 - Drop-in. `sudo bash install.sh --upgrade`

### Fixed
- **Gap 7: System Sync Toggle**: Resolved a major GitOps gap where the `sync_system` toggle was being ignored. System settings (Hostname, Timezone, DNS, NTP, Firewall, Networking, and Samba) are now correctly filtered and committed to Git based on the User Interface selection.
- **State Serialization**: Implemented the missing `system:` block in the live-to-Git state generation engine.

---

## v6.0.2 (2026-03-20) - "Deterministic Integrity"

Upgrade from: v6.0.1 - Drop-in. `sudo bash install.sh --upgrade`

### Added
- **Hardened Deterministic Bootstrap**: Introduced the `-apply` flag for `dplaned`, enabling one-off GitOps reconciliation during initial system setup.
- **Compliance Tooling (`dplane`)**: Added a dedicated CLI symlink with `-test-serialization` and `-test-idempotency` flags for mathematical verification of system state.
- **Data Readiness Enforcement**: Stacks and workloads are now blocked from starting until dependent ZFS datasets are verified as mounted and ready.
- **Audit Chain Integrity API**: New endpoint `/api/system/audit/verify-chain` for real-time cryptographic verification of the audit log chain.
- **CI/CD Alignment**: Hardened the validation pipeline with automated enforcement of v6 invariants on every push.
- **Convergence Engine**: Introduced post-apply state verification that re-reads live system status to confirm the desired `state.yaml` configuration was successfully reached.

### Fixed
- **Gap 1: Pool Import Safety**: Switched from name-based to GUID-based `zpool import` to prevent accidental mis-imports on systems with overlapping pool names or renamed pools.
- **Gap 2: Ambiguous State Detection**: The GitOps engine now detects and blocks reconciliation if multiple pools or datasets with the same name are found, requiring manual intervention for safety.
- **Gap 3 & 6: Strict Mountpoint Verification**: Enhanced the data-readiness gate to verify not just mount status, but exact mountpath accuracy, preventing accidental data writes to the root partition if ZFS drifts.
- **Gap 4: Share-Dataset Cross-Validation**: Declarative SMB shares and NFS exports are now cross-referenced against managed ZFS datasets to ensure every share has a valid, managed backing mountpoint.
- **API Handler Robustness**: Added `HasAmbiguous` guards to the apply handler to prevent degenerate states from causing generic errors.
- **Convergence Visibility**: Enriched API responses with structured convergence metadata (`CONVERGED`, `DEGRADED`, etc.).
- **Plan Summaries**: Included `ambiguous_count` and `has_ambiguous` in the plan summary for enhanced operator visibility.
- **Schema Correction**: Fixed a mapping bug in pool GUID parsing from `state.yaml`.
- **Hardened Testing**: Added automated unit tests for GUID parsing and ambiguity detection, and implemented a comprehensive **Fleet & Install CI** pipeline in a single, gated `ci.yml` covering fresh installs, idempotency, and multi-node fleet simulations.
### Quality Gaps Addressed (v6.0.2 Polish)
- [x] **Environment Resiliency**: Added `.gitattributes` and automatic CRLF-to-LF conversion in `install.sh` to ensure script execution stability across all checkout environments.
- [x] **CI Consolidation**: Successfully merged `validate.yml`, `fleet-install.yml`, and `release.yml` into a single, comprehensive deployment pipeline.
- [x] **Diff Engine Halt**: Implemented early return in `ComputeDiff` for ambiguous states.
- [x] **Convergence Correctness**: Updated `ConvergenceCheck` to correctly include `ActionDelete` in drift calculations.
- [x] **CLI Automation**: Added `-diff` and `-convergence-check` flags to `dplaned` for CI/CD integration.
- **Audit Key Initialization**: Resolved an issue where the audit signing key was not correctly generated on fresh installs.
- **YAML Key Ordering**: Ensured deterministic serialization of the GitOps state to prevent false diffs.

---

## v6.0.1 (2026-03-19) : "Enforcement Mode"

(Skipped or superseded by v6.0.2)

---

## v6.0.0 (2026-03-17) : "Declarative Freedom"

Upgrade from: v5.3.5 - Drop-in. `sudo bash install.sh --upgrade`

### Added
- **Optional & Granular GitOps**
  - **Global Toggle**: GitOps functionality can now be entirely enabled or disabled via the UI, making it a non-essential control plane.
  - **Granular Sync Matrix**: Introduced selective synchronization for six key resource categories: Storage (ZFS), Data Access (SMB/NFS), Applications (Docker), Identity (Users/Groups), Protection (Replication), and System settings.
  - **GitHub Connect Wizard**: A premium 3-step onboarding flow for linking repositories and managing Personal Access Tokens (PAT) directly within the GitOps settings.
  - **Manual Sync Fallback**: Added a "Sync Now" button for instant on-demand reconciliation.
  - **Authenticated Git Operations**: Robust support for GitHub PAT and SSH keys using secure `GIT_ASKPASS` and `GIT_SSH_COMMAND`.
- **Audit Log Automation & Maintenance**
  - **Auto-Rotation**: Weekly background log purging based on user-defined retention settings.
  - **Space Reclamation**: Integrated `VACUUM` into both manual and scheduled rotations to reclaim SQLite disk space.
- **Enterprise Integration Hooks**
  - **License Management**: New "Enterprise License Key" field in System Settings as the gateway for premium features.
  - **Plugin Injection System**: Added hooks for dynamic injection of navigation, routes, and settings, enabling a true "Zero-Pollution" open-core architecture.

### Changed
- **"Zero-Pollution" Open-Core**: Migrated the Audit Log UI, routes, and navigation from the Community Edition to the Enterprise Compliance Engine (PRO). The core repository is now free of all proprietary UI traces.

---

## v5.3.5 (2026-03-16) : "API Auth Patch"

Upgrade from: v5.3.4 - Drop-in. `sudo bash install.sh --upgrade`

### Security

- **API Authentication**
  - Patched `sessionMiddleware` to support `Authorization: Bearer` tokens.
  - Enables secure programmatic access for sidecars and automation using long-lived API tokens.
  - Backported `ValidateAPITokenAndGetUser` to the security core.

---

## v5.3.4 (2026-03-16) : "Hardened Enterprise Deployment"

Upgrade from: v5.3.3 - Drop-in. `sudo bash install.sh --upgrade`

### Added

- **Enterprise Security Hardening**
  - **Environment-Based Auth**: Modified the installation process to store sensitive API tokens in a dedicated `EnvironmentFile` (`/etc/dplaneos/daemon.env`) with `0600` permissions.
  - **Systemd Integration**: Updated service definitions to use `EnvironmentFile` instead of command-line flags, preventing token leakage in process lists (`ps aux`).
- **Unified Versioning**: Synchronized version identifiers across CE and Enterprise suites for cleaner release tracking.

---

## v5.3.3 (2026-03-16) - "ZED Integration"

### Added

- **ZED Hook Integration**
  - Integrated ZFS Event Daemon (ZED) real-time events into the D-PlaneOS daemon via a Unix domain socket at `/run/dplaneos/dplaneos.sock`.
  - Bypasses the 30-second polling limitation of the daemon by feeding critical pool and VDEV events immediately to the UI and alert channels.
  - Replaced the standalone JSON file writing and Telegram alerting in the ZED hook script with a streamlined notification forwarder.
  - Automatically installed by `install.sh` on Debian/Ubuntu systems and fully declared in `nixos/module.nix` for NixOS systems.

---

## v5.3.2 (2026-03-15) - "Build Integrity & Maintenance"

Upgrade from: v5.3.1 - Drop-in. `sudo bash install.sh --upgrade`

### Added

- **Smarter UI Build Management**
  - Migrated static assets (`manifest.json`, `modules/`) to the source directory (`app-react/public/`). This enabled the use of Vite's `emptyOutDir` to automatically purge orphaned hashed files (like the stale `index-nABdU3XR.js`) during the build process.

### Fixed

- **Pool Operations Integrity**
  - Re-verified that `PoolOperations` (`zpool_clear`, `zpool_online`) and their corresponding whitelisted commands are fully present in the release. Addresses reports of accidental feature removal.

---

## v5.3.1 (2026-03-15) - "CI & Panic Resilience"

Upgrade from: v5.3.0 - Drop-in. `sudo bash install.sh --upgrade`

### Fixed

- **Critical - GetDiskStatus runtime panic**
  - Resolved `slice bounds out of range` panic in `GetDiskStatus` caused by unsafe parsing of `lsblk` output on loopback devices (commonly seen in CI environments). Implemented field-count safety checks.
- **ACL Management**
  - **Route Alignment**: Fixed a mismatch where the daemon expected `/api/acl/get` while tests/frontend might use `/api/system/acl`. Both are now supported via aliasing.
  - **Diagnostic Visibility**: Added `ACL:` log prefixes to important operations in `GetACL` and `SetACL` to ensure system-level failures (like missing `getfacl`) are visible in daemon logs.
  - **Bulk API**: Refactored `SetACL` to support the multi-line full ACL format sent by the frontend, ensuring "Apply" works predictably while maintaining compatibility with single-entry CI tests.

### CI/CD

- **Environment Hardening**: Added `acl` and `ipmitool` to standard CI dependencies.
- **ZFS Integration**: Explicitly enabled `acltype=posixacl` on the test pool to match production-grade ZFS configurations.

---

## v5.3.0 (2026-03-15) - "Storage & Security Integrity"

Upgrade from: v5.2.3 - Drop-in. `sudo bash install.sh --upgrade`

### Added

- **Storage Management Core**
  - **StorageSummary component**: Real-time unified capacity telemetry (Total/Used/Free) across all ZFS pools.
  - **Integrated Pool Lifecycle**: Full support for creating, expanding, and destroying pools directly from `PoolsPage.tsx`.
  - **Data Safety**: Destructive operations (pool destruction) now require explicit "type-to-confirm" validation.
- **Enhanced Modal UX**
  - **Universal Close Mechanisms**: Added high-visibility "X" buttons to modal headers and standardized "Cancel" buttons in all footers.
  - **Determinism Hardening**: Aligned the Enterprise sidecar with the new v6.0.2 "Deterministic Integrity" invariants, ensuring all compliance reports reflect accurate convergence state.
- **CI Gating**: Integrated the consolidated `D-PlaneOS` CI pipeline as a mandatory release gate for all production builds.
  - **Portal-based Rendering**: Migrated the entire modal architecture to React Portals, rendering into `#modal-root` to ensure modals are always top-level and immune to parent stacking context glitches.

### Fixed

- **Critical Security - Path Traversal Vulnerabilities**
  - **IsValidPath**: Fixed a bypass where `./` could be used to traverse directories. Added explicit blocking for dot-slash patterns.
  - **IsSafeFilename**: Corrected a logical error (converted `&&` to `||`) that allowed filenames with path separators if they didn't contain both `/` and `\` simultaneously.
- **UI/UX Refinement**
  - Standardized modal centering and backdrop-blur filters for a premium "glass" aesthetic.
  - Resolved layout issues in `PoolsPage.tsx` where long disk lists could clip modal footers.

### Refactored

- **API Integration**: Replaced legacy mock data structures in the storage layer with real API contracts, preparing for full ZFS integration.

---

## v5.2.3 (2026-03-15) - "More UI Polish"

### Added

- Global Design Tokens (src/index.css)Rich HSL Color Palette: Refactored the base colors (--primary, --success, --warning, --error) from flat hex codes to a deeply saturated and highly controllable HSL spectrum.
Deep Mesh Background: Upgraded the root background from a basic 2-color radial gradient to a multi-layered, dramatic pseudo-mesh gradient that reacts beautifully underneath glassmorphic elements.
Shadow and Depth Overhaul: Implemented a new multi-layered robust shadow system (--shadow-sm, --shadow-md, --shadow-lg, and an all-new --shadow-glow variable).
- The Glassmorphism Aesthetic
Introduced the --blur-glass: blur(20px) variable.
Applied backdrop-filter alongside semi-transparent deep-dark backgrounds (hsla(var(--hue-bg), 18%, 10%, 0.5)) to:
Top navigation bar (TopBar.tsx) Left navigation menu (Sidebar.tsx)
Dashboard component containers (.card, .alert)
Input fields, dropdown menus, tooltips, and popovers.

### Fixed

- Codebase-Wide Component Normalization
Audited the entire src/pages directory (over 40+ route components).
Discovered extensive usage of hardcoded inline styles (style={{ background: 'var(--bg-card)', border: '1px solid var(--border)' }}) that bypassed the new glass tokens.
- Deployed a series of automated AST-like regex replacements across thousands of lines of TypeScript to dynamically strip the static borders/backgrounds and inject the global className="card". This ensures every single view in D-PlaneOS inherits the interactive glassmorphism updates uniformly.
- Micro-Animations & Dynamic Feedback
Buttons (.btn): Added inner light-catching borders (box-shadow: inset 0 1px 0 hsla(0,0%,100%,0.3)), scale compression on active clicks (transform: scale(0.97)), and heavy glowing hover shadows.
Inputs & Tabs: Introduced a var(--transition-bounce) for spring-like fluidity when moving between tab states or focusing on input bars.
- Dashboard Interactive Cards (.card.interactive): Upgraded dashboard metrics and section cards to smoothly translate Y: -2px with a heightened drop-shadow.

## v5.2.2 (2026-03-14) - "UI Polish"

Upgrade from: v5.2.1 - Drop-in. `sudo bash install.sh --upgrade`

### Added

- **Custom Tooltip component** - new styled floating tooltip component. Supports positioning (top/bottom/left/right), delay options, and custom styling.
- **Popover component** - hover cards for showing contextual info.
- **Enhanced ConfirmDialog** - supports context info display and typed confirmation for destructive operations. Auto-focuses input, blocks confirm until text matches.

### UI Improvements

- Modal entrance/exit animations - smoother transitions using scale and fade effects
- Consistent tooltip/popover styling matching design system
- All button `title=` attributes replaced with custom Tooltip component

---

## v5.2.1 (2026-03-13) - "Complete Consistency"

Upgrade from: v5.1.2 - Drop-in. `sudo bash install.sh --upgrade`

### Fixed

**Critical - silent database errors on user/group operations**

- **User update/delete silently failed** - `users_groups.go` was ignoring DB errors on several operations. Fixed: all DB errors are now checked and returned to the UI properly.

**Reliability - database connection pooling**

- **SMTP alerting opened new DB connection per request** - `alerting_smtp.go` was calling `sql.Open()` on every HTTP request. Fixed: refactored to use a shared pooled `*sql.DB` via `AlertingHandler` struct.

### Refactored

- **HTTP error response consistency** - replaced all ~195 `http.Error` calls with `respondError`/`respondErrorSimple` for consistent JSON error format throughout the API.
- **Config package** - centralized `/var/lib/dplaneos/*` paths in `internal/config/paths.go`. Migrated enterprise_hardening.go, audit_verify.go, docker_icons.go, docker_stacks.go, system_extended.go.

### Tests Added

- **validateRepoURL** - tests for blocking dangerous URL schemes (ext://, file://, fd://)
- **Path traversal prevention** - IsValidPath and IsSafeFilename functions with tests
- **Auth handlers** - respondError JSON format tests

---

## v5.2.0 (2026-03-13) - "Reliability & Consistency"

Upgrade from: v5.1.2 - Drop-in. `sudo bash install.sh --upgrade`

### Fixed

**Critical - silent database errors on user/group operations**

- **User update/delete silently failed** - `users_groups.go` was ignoring DB errors on several operations. Fixed: all DB errors are now checked and returned to the UI properly. Previously, operations could fail without the user knowing.

**Reliability - database connection pooling**

- **SMTP alerting opened new DB connection per request** - `alerting_smtp.go` was calling `sql.Open()` on every HTTP request, creating connection overhead. Fixed: refactored to use a shared pooled `*sql.DB` via `AlertingHandler` struct, consistent with all other handlers.

### Refactored

- **HTTP error response consistency** - replaced ~160 `http.Error` calls with `respondError`/`respondErrorSimple` for consistent JSON error format throughout the API. Now returns `{"success":false,"error":"message"}` on all error paths instead of plain text.
- **Config package** - added centralized path constants in `internal/config/paths.go` for `/var/lib/dplaneos/*` paths.

---

## v5.1.3 (2026-03-13) - "Reliability Fixes"

Upgrade from: v5.1.2 - Drop-in. `sudo bash install.sh --upgrade`

### Fixed

**Critical - silent database errors on user/group operations**

- **User update/delete silently failed** - `users_groups.go` was ignoring DB errors on several operations. Fixed: all DB errors are now checked and returned to the UI properly. Previously, operations could fail without the user knowing.

**Reliability - database connection pooling**

- **SMTP alerting opened new DB connection per request** - `alerting_smtp.go` was calling `sql.Open()` on every HTTP request, creating connection overhead. Fixed: refactored to use a shared pooled `*sql.DB` via `AlertingHandler` struct, consistent with all other handlers.

## v5.1.2 (2026-03-13) - "Auth Integrity"

Upgrade from: v5.1.1 - Drop-in. `sudo bash install.sh --upgrade`

### Added

- **Docker template library** - one-click deployment of pre-configured application stacks (Home Assistant, Plex, Nextcloud, etc.) via `GET /api/docker/templates` and `POST /api/docker/templates/deploy`. Templates can be git repos or built-in. Deployed as independent Compose stacks with atomic rollback on failure.

### Fixed

**Critical - LDAP-sourced users could not log in**

- **LDAP users were permanently locked out of the UI** - the login handler only performed bcrypt comparison against `password_hash`. LDAP-synced accounts have an intentionally empty `password_hash` (they authenticate via directory bind). Any login attempt by an LDAP account failed silently with "Invalid credentials". Fixed: `Login()` now reads the `source` column; when `source='ldap'` it calls `ldapAuthenticate()` which loads LDAP config from the database and performs a real-time bind to verify credentials. Local accounts and user ID 1 remain on bcrypt regardless of LDAP state.
- **LDAP circuit breaker** - when the directory server is unreachable, each login attempt would block for the full TCP timeout. Added circuit breaker: after 3 consecutive connection failures, LDAP authentication fails immediately for 30 seconds rather than waiting for TCP timeouts. Connection-level errors (vs. credential errors) are tracked separately from authentication failures.

**UI - eliminated all browser-native dialogs**

- **`window.confirm()` used in 13 places across 9 pages** - browser-native confirm dialogs are visually inconsistent and cannot be styled. Replaced with `useConfirm()` hook that renders inline modal dialogs using the existing design system. Each dialog has a context-appropriate title, descriptive message, and correct danger/warning variant. Pages updated: AlertsPage, CloudSyncPage, FilesPage, GitSyncPage, HAPage, ISCSIPage, SecurityPage, SettingsPage, UsersPage.

**Code quality**

- **Syncthing conflict files in vendor** - 1,260 `.sync-conflict-*` files in `daemon/vendor/` caused duplicate symbol build failures locally. Removed.
- **Duplicate inline style definitions** - `btnPrimary`, `btnGhost`, `btnDanger`, `inputStyle`, `cardStyle` were independently defined across 15–20 page files. Pages now use global CSS classes from `index.css`.

### Documentation

- **ADMIN-GUIDE.md** - replaced incorrect "JIT provisioning on first login" with accurate model: sync populates local DB, login performs real-time LDAP bind for directory accounts, local accounts always use bcrypt. Added note that D-PlaneOS uses LDAP for web UI login (unlike TrueNAS Scale / Unraid which use it for SMB auth only).
- **README.md** - corrected Identity and Auth architecture descriptions.

---

## v5.1.1 (2026-03-10) - "System Audit"

Upgrade from: v5.1.0 - Drop-in. `sudo bash install.sh --upgrade`

### Fixed

**Critical - broken on first interaction**

- **Docker page: all containers showed blank names, images, and state** - `containerToMap` emitted PascalCase Docker SDK keys (`Id`, `Image`, `State`). Frontend expected lowercase. Fixed: added lowercase aliases. Ports now also include `host_port`/`container_port`/`protocol`.
- **ZFS pool capacity bars always 0%** - `ListPools` fetched only `name,size,alloc,free,health`, never `cap`. Fixed: added `cap,health` columns. Also removed invalid `type` property (not a `zpool list` column - caused `exit status 2` and `success:false` on every `GET /api/zfs/pools` call, confirmed in CI).
- **ZFS dataset quota column always empty** - `ListDatasets` fetched `refer` instead of `quota`. Fixed.
- **Firewall rules page crashed on load** - `GetStatus` returned `rules` as raw `ufw status numbered` text. Frontend called `.map()` on a string - runtime crash. Fixed: new `parseUFWRules()` returns structured `[]map`.
- **Setup wizard failed on fresh DB** - `HandleStatus` and `HandleSetupAdmin` both `SELECT` from `system_config` before creating the table. Fixed: `CREATE TABLE IF NOT EXISTS` before any query.
- **`must_change_password` never acted on** - backend sets this flag on the auto-generated admin account; login page never checked it and never redirected. Fixed: redirects to `/security` after login when flag is set.

**High - specific features silently broken**

- **SMB share create/edit always used defaults** - `Share` interface used `readonly`/`guestok`/`browseable`; backend returns/expects `read_only`/`guest_ok`/`browsable`. Every create ignored the user's checkbox selections. Fixed: aligned field names throughout `SharesPage.tsx`.
- **File manager writes blocked by systemd `ProtectSystem=strict`** - `ReadWritePaths` didn't include `/mnt`, `/tank`, `/data`, `/media`, `/etc/samba`, `/etc/exports`, `/etc/iscsi`, `/etc/ssh`, `/tmp`, `/home`. Every file write returned permission denied. Fixed in `dplaned.service` and `install.sh` inline unit.
- **Log streams, WebSocket, and large downloads cut at 30s** - `WriteTimeout: 30s` on the HTTP server. SSE, both WebSocket endpoints, and file downloads were terminated. Fixed: `WriteTimeout: 0`.
- **`chmod` always 400** - frontend sends `{ mode }`, backend expected `{ permissions }`. Fixed: both accepted.
- **File rename always 400** - frontend sends `{ new_name }` (filename only), backend expected `{ new_path }` (full path). Fixed.
- **Chunked upload wrote empty files** - frontend sends field `chunk`, backend read `chunkIndex`. Every chunk 0 truncated with no data. Fixed.
- **Setup wizard, HA heartbeat, udev disk events, and Prometheus blocked by session middleware** - no exemptions for these legitimately public routes. Fixed: all five paths added to bypass list.

**Medium**

- **Install phase numbering collision** - two phases both labelled `8`. Renumbered to clean 0–13 sequence.
- **`/etc/exports` not in `ReadWritePaths`** - NFS export writes would fail. Added.
- **File manager: four missing features added** - inline text editor (Ctrl+S, dirty tracking, 2 MB guard), download button per row, drag-and-drop upload with chunked upload, multi-select with checkbox column, quick-access bookmark sidebar.
- **Login `must_change_password` redirect** - added to `LoginPage.tsx`.

### Stats

| What | Before | After |
|------|--------|-------|
| Docker page container display | All blank | Correct |
| ZFS pool capacity bar | Always 0% | Correct |
| Firewall rules table | JS crash | Structured table |
| SMB share settings honoured | Never | Always |
| File manager features | 4 missing | Complete |
| CI: ZFS pools test | ✗ (exit 2) | ✓ |

---

## v5.1.0 (2026-03-10) - "Template Library"

Upgrade from: v5.0.0 - Drop-in. `sudo bash install.sh --upgrade`

### Added

**Multi-Stack Template System**

Templates are Git repositories where each sub-directory containing a `docker-compose.yml` is an independently deployed stack. Templates may also include:
- `template.json` - name, description, icon, ordered stack list, user-configurable variables
- `dplane-requirements.json` - ZFS datasets to create and firewall ports to open before deployment

**Backend (`daemon/internal/handlers/docker_templates.go`):**
- `GET /api/docker/templates` - built-in template catalogue (3 templates shipped: *arr Media Suite, Monitoring Suite, Home Automation)
- `GET /api/docker/templates/installed` - all deployed stacks grouped by `template_id`; standalone stacks (no template) grouped under `__standalone__`
- `POST /api/docker/templates/deploy` - clones template Git repo, processes `dplane-requirements.json` (creates ZFS datasets, logs required firewall ports), creates shared Docker network if specified, deploys each sub-stack, substitutes `${VAR}` placeholders in compose/env files, writes `.dplane-template` JSON marker in each stack directory
- Variable substitution: `${KEY}` placeholders in `docker-compose.yml` and `.env` are replaced with user-supplied values from the deploy request
- ZFS-aware: `dplane-requirements.json` datasets created with `zfs create -p` before stacks start; quota and mountpoint supported

**`daemon/internal/handlers/docker_stacks.go`:**
- `StackInfo` gains `template_id` and `template_name` fields
- `ListStacks` reads `.dplane-template` marker from each stack directory

**`daemon/cmd/dplaned/main.go`:**
- Stack CRUD routes registered (were missing - `DeployStack`, `GetStackYAML`, `UpdateStackYAML`, `DeleteStack`, `StackAction`, `ConvertDockerRun`)
- 3 template routes registered

**`daemon/internal/jobs/jobs.go`:**
- `Job` and `JobSnapshot` gain `Logs []string` field
- `Job.Log(line string)` method: appends a progress line under mutex; visible to any caller polling `GET /api/jobs/{id}`. Long-running jobs (template deploy, ZFS send, apt upgrade) now surface step-by-step progress.

**Frontend (`app-react/src/pages/ModulesPage.tsx` - rewrite):**
- **Installed tab**: template groups shown as collapsible cards with aggregate `N/M running` badge and template icon. Each group expands to compact per-stack cards. Standalone stacks shown as full cards below.
- **Template Catalogue tab**: grid of available templates with icon, description, stack list, and tags. "Deploy" opens a variable-input modal.
- **TemplateDeployModal**: renders each `TemplateVariable` as a labelled input (type=password for `secret: true`). Required fields validated before submit.
- **StackCard**: compact mode (inside group) and full mode (standalone). Shows per-service status dots. Restart/Stop/Start actions with job progress inline.
- **TemplateGroupCard**: collapsible. Running count badge colour: green (all running), amber (partial), grey (stopped).
- All existing `ContainerIcon`, `dplaneos.icon` label resolution, icon map, and port link behaviour fully preserved.

### Stats

| What | Before | After |
|------|--------|-------|
| Template deployment | Manual git clone + per-stack deploy | One-click with variable prompts |
| Stack grouping in UI | Flat list | Grouped by template with aggregate status |
| Job progress visibility | Status only (running/done/failed) | Step-by-step log lines |
| ZFS dataset provisioning | Manual | Automatic from `dplane-requirements.json` |
| Stack routes registered | Missing from main.go | Fully registered |

---

## v5.0.0 (2026-03-10) - "Solid State"

Upgrade from: v4.3.2 - **Breaking change for NixOS users only** (run `setup-nixos.sh` once after upgrade).

### Architectural Pivot: JSON-to-Nix Bridge

Previous versions used "The Surgeon": the Go daemon built raw Nix syntax strings in Go templates and wrote them directly to `dplane-generated.nix`. Any special character in a user-supplied value (apostrophe in a hostname, backslash in an `extraGlobalConfig`, multiline string) could produce a `.nix` file that fails `nix-instantiate --parse`, silently breaking `nixos-rebuild`.

v5.0 replaces this with the **JSON-to-Nix Bridge**:

- Daemon writes one file: `/var/lib/dplaneos/dplane-state.json` (pure JSON via `encoding/json`).
- `nixos/dplane-generated.nix` is now **static** - installed once by `setup-nixos.sh`, never modified by the daemon. It reads the JSON at eval time via `builtins.fromJSON` and maps keys to NixOS module options with `s.key or default` guards.
- Zero dynamic Nix syntax generated anywhere. Zero Surgeon. Zero injection risk.

### Changed

- **`daemon/internal/nixwriter/writer.go`**: Complete rewrite. Removed all string-template stanza builders. New `DPlaneState` struct + atomic JSON write. Same `Set*()` caller API - no handler changes required. `validateIP` now uses `net.ParseIP` (was a lax character-range check).
- **`nixos/dplane-generated.nix`**: New static bridge file. Reads `/var/lib/dplaneos/dplane-state.json` at eval time. `builtins.pathExists` guard for first-boot safety. Nix helper functions map JSON maps to correct `systemd.network` attrset shapes.
- **`nixos/setup-nixos.sh`**: Installs bridge file, seeds empty `{}` state file, auto-adds import to `configuration.nix`.
- **`nixos/configuration.nix`**: Added `imports = [ ./dplane-generated.nix ./modules/samba.nix ]`.
- **`nixos/flake.nix`**: Added bridge and samba module to both x86_64 and aarch64 module lists.
- **`nixos/impermanence.nix`**: Added `dplane-state.json` to persisted files - without this, all UI settings revert on every reboot on the appliance build.

### Migration (NixOS)

```bash
git pull
sudo bash nixos/setup-nixos.sh   # installs bridge, seeds state file
sudo nixos-rebuild switch --flake nixos#dplaneos
```

Re-apply any network/samba settings via the web UI after upgrading - the daemon writes them to the JSON file and the next rebuild picks them up.

---

## v4.3.2 (2026-03-10) - "WebSocket & API Wiring"

Upgrade from: v4.3.1 - Drop-in upgrade via `sudo bash install.sh --upgrade`

### Fixed

- **Pool health WS events never reached the UI:** `broadcastPoolHealthChanged` in
  `disk_event_handler.go` broadcast the event as `"poolHealthChanged"` (camelCase)
  but `ws.ts` switch handled `'pool_health_change'` (snake_case). Every hot-swap
  and pool recovery event was silently dropped. Event name corrected to
  `"pool_health_change"` on the daemon side. `PoolsPage`, `DashboardPage`, and
  `HardwarePage` now receive live pool health push as intended.
  (`daemon/internal/handlers/disk_event_handler.go:407`)

- **`diskAdded` / `diskRemoved` events broadcast but never routed in frontend:**
  The daemon correctly broadcasts `"diskAdded"` and `"diskRemoved"` on hot-swap.
  `ws.ts`'s `EventMap` declared `diskAdded` and `diskRemoved` subscribers, but the
  `onmessage` switch had no `case` for either string - both events were silently
  dropped. Cases added. Additionally, each event now also emits `hardwareEvent`
  with the action embedded, so `HardwarePage`'s existing `wsOn('hardwareEvent', ...)`
  subscription fires correctly without any page changes.
  (`app-react/src/stores/ws.ts`)

- **Scrub and resilver WS events never broadcast by daemon:** `ws.ts` handled
  `scrub_started`, `scrub_completed`, `resilver_started`, `resilver_progress`, and
  `resilver_completed` - but the daemon never called `Broadcast` for any of these.
  `StartScrub` now broadcasts `scrub_started`; `StopScrub` broadcasts `scrub_completed`.
  `ReplaceDisk` job broadcasts `resilver_started` at job start and `resilver_completed`
  (with success/failure) at job end. `PoolsPage` live-refresh subscriptions now work.
  (`daemon/internal/handlers/zfs_operations.go`)

- **`gitops.drift` event broadcast by daemon but unhandled in frontend:** The GitOps
  drift detector broadcasts `"gitops.drift"` whenever declarative state diverges from
  runtime. `ws.ts` had no `case` for it and no `EventMap` entry. Added `gitopsDrift`
  to `EventMap` and the switch. (`app-react/src/stores/ws.ts`)

- **`mount_health_<pool>` events unreachable in frontend:** The background mount monitor
  broadcasts per-pool events like `"mount_health_tank"`. `ws.ts` declared `mountError`
  in `EventMap` but had no switch handler. Added a `default` branch that matches any
  `msg.type.startsWith('mount_health_')` and emits to `mountError` subscribers.
  (`app-react/src/stores/ws.ts`)

- **`DELETE /api/shares` unregistered - share deletion always returned 405:**
  `SharesPage` calls `DELETE /api/shares` with `{ name }` in the body. The route was
  only registered for `GET` and `POST`. Added `DELETE` registration in `main.go` and a
  new `deleteShareByName` method on `ShareCRUDHandler` that looks up the share by name,
  deletes it, and regenerates `smb.conf`. The existing `deleteShare` (used by POST
  action-dispatch with an `id`) is unchanged.
  (`daemon/cmd/dplaned/main.go:587`, `daemon/internal/handlers/shares_crud.go`)

- **`GET/POST /api/system/tuning` handler implemented but route never registered:**
  `HandleSystemSettings` (ARC limit, swappiness, inotify/memory/iowait thresholds) was
  fully implemented in `system_settings.go` and documented in the CHANGELOG since v4.1.2
  but the route was never added to `main.go`. Registered at
  `GET /api/system/tuning` and `POST /api/system/tuning`.
  (`daemon/cmd/dplaned/main.go`)

- **Rate limiter used `r.RemoteAddr` - all traffic from a reverse proxy shared one bucket:**
  When the daemon runs behind nginx (standard production setup), every request arrives
  with `RemoteAddr = 127.0.0.1`. All users shared a single rate-limit bucket, so a
  single active user could exhaust the 100 req/min limit for everyone. The limiter
  now uses a new `realIP()` helper: for direct connections it trusts `RemoteAddr`; for
  loopback connections it falls back to `X-Real-IP` then `X-Forwarded-For` (safe because
  only a trusted local proxy can set these headers on loopback).
  (`daemon/cmd/dplaned/main.go`)

### Stats

| What | Before | After |
|------|--------|-------|
| Pool health WS push | Silently dropped | Live |
| Hot-swap disk WS push | Silently dropped | Live |
| Scrub WS events | Never emitted | Emitted on start/stop |
| Resilver WS events | Never emitted | Emitted on replace job start/end |
| `gitops.drift` WS event | Unhandled | Routed to `gitopsDrift` subscribers |
| `mountError` WS event | Unhandled | Routed via `mount_health_` prefix match |
| Share deletion (DELETE) | 405 Method Not Allowed | Works |
| `/api/system/tuning` | 404 | Registered |
| Rate limiter (behind proxy) | 1 bucket for all users | Per-client IP |

---

## v4.3.1 (2026-03-09) - "Icon System Fixes"

Upgrade from: v4.3.0 - Drop-in upgrade via `sudo bash install.sh --upgrade`

### Fixed

- **`Stack` interface fields never populated (`running_containers`, `total_containers`, `total_ports` always `undefined`):**
  `groupContainersByStack` in `docker.go` emitted only `name`, `containers`, and `count`. The frontend
  `ContainersTab` reads `stack.running_containers` and `stack.total_containers` to render the "N/M running"
  badge in every stack header - these were always `undefined`, rendering as `undefined/undefined running`.
  `groupContainersByStack` now iterates the original `dockerclient.Container` slice to compute all three
  fields before serialising. (`daemon/internal/handlers/docker.go`)

- **`dplaneos.icon` label silently ignored for all stack cards in ModulesPage:**
  `StackCard` passed `image={stack.name}` to `ContainerIcon` but never passed the `labels` prop, so the
  `dplaneos.icon` resolution path (priority 1) was permanently skipped for every module card even when the
  label was set. `ComposeStack` type gains an optional `labels` field; `StackCard` now forwards it.
  (`app-react/src/pages/ModulesPage.tsx`)

- **`IconMapEntry` type duplicated in three files - structural divergence risk:**
  `ContainerIcon.tsx`, `DockerPage.tsx`, and `ModulesPage.tsx` each declared their own `interface IconMapEntry`
  and `interface IconMapResponse`. These are now centralised in `app-react/src/lib/iconTypes.ts` and imported
  everywhere. (`app-react/src/lib/iconTypes.ts`, all three consumers)

- **Dead-code redundancy in `resolveIcon` image matching:**
  `ContainerIcon.tsx` called `nameLower(namePart).includes(entry.match) || nameLower(imageLower).includes(entry.match)`.
  `namePart` is a substring of `imageLower`, so the first condition is always a strict subset of the second -
  it could never be true when the second was false. The `nameLower()` wrapper was also a no-op (both inputs
  were already lowercased). Both simplified to a single `imageLower.includes(entry.match)` check.
  (`app-react/src/components/ui/ContainerIcon.tsx`)

- **`.jpg`, `.jpeg`, `.gif` accepted by frontend but missing from daemon MIME fallback:**
  `ContainerIcon.tsx`'s `IMAGE_EXTS` array includes `.jpg`, `.jpeg`, and `.gif`, so users can set
  `dplaneos.icon: mylogo.jpg`. The daemon's `HandleCustomIconFile` MIME fallback `switch` only covered
  `.svg`, `.png`, `.webp` - on minimal Linux systems without `/etc/mime.types` the file would be served
  as `application/octet-stream`, preventing browser rendering. Added `case ".jpg", ".jpeg": "image/jpeg"`
  and `case ".gif": "image/gif"` to the fallback switch.
  (`daemon/internal/handlers/docker_icons.go`)

- **Route ordering dependency in `main.go` undocumented:**
  `GET /api/assets/custom-icons/list` must be registered before the `PathPrefix` catch-all or gorilla/mux
  would route list requests to the file handler (returning 404). The ordering dependency is now documented
  with an explicit comment. (`daemon/cmd/dplaned/main.go`)

- **`custom_icons/` directory behind `chmod 700` parent - files inaccessible to non-root:**
  `install.sh` set `chmod 700 /var/lib/dplaneos` but never set explicit permissions on the
  `custom_icons/` subdirectory. Because the parent had `700`, no non-root process could traverse
  into it even if the subdirectory itself had permissive permissions. `custom_icons/` is now
  explicitly set to `root:root 755` so nginx (if configured as a static server) and other
  authorised processes can read icon files. (`install.sh`)

### Added

- **`app-react/src/lib/iconTypes.ts`:** New shared module exporting `IconMapEntry` and `IconMapResponse`.
  Single source of truth for icon map types across all frontend consumers.

- **`dplaneos.icon` label help tooltip in Docker containers table:**
  An `ⓘ` icon now appears next to the "Container" column header in the containers table. Hovering it
  shows a tooltip explaining the three supported `dplaneos.icon` label value formats (Material Symbol
  name, local icon filename, remote URL) and the custom icons directory path.
  (`app-react/src/pages/DockerPage.tsx`)

### Stats

| What | Before | After |
|------|--------|-------|
| `running_containers` in stack header | always `undefined` | correct live count |
| `dplaneos.icon` label honoured in ModulesPage | never | always |
| `IconMapEntry` declaration sites | 3 | 1 (shared) |
| MIME types with reliable fallback | 3 (svg/png/webp) | 6 (+jpg/jpeg/gif) |
| User-facing `dplaneos.icon` documentation | none | tooltip in Docker page |

---

## v4.3.0 (2026-03-09) - "Automation"

Upgrade from: v4.2.0 - Drop-in upgrade via `sudo bash install.sh --upgrade`

### Fixed

- **DEGRADED pools were never alerted on:** `pool_heartbeat.go` only caught
  SUSPENDED/UNAVAIL. Now detects DEGRADED via `zpool list -H -o name,health`
  every 30 seconds and fires a WARNING-level alert through all channels.
  Per-pool per-event de-duplication prevents spam; clears when pool recovers.

- **All alert channels were dead code:** `SendWebhookAlert`, `SendSMTPAlert`,
  and Telegram were defined but had zero callers for pool/disk/capacity events.
  New `alert_dispatch.go` provides `DispatchAlert(level, event, resource, msg)`
  as a single call site. All subsystems now route through it.

- **Webhook body templates were ignored:** UI allowed template variables but
  backend sent fixed JSON regardless. Now rendered via `strings.NewReplacer`.
  Custom `Content-Type` header also honoured.

- **`ReplaceDisk` never returned a `job_id`:** Now runs async via job queue;
  `job_id` returned immediately so UI can poll progress.

- **Resilver progress was unparsed raw text:** New `HandleResilverStatus`
  (`GET /api/zfs/resilver/status`) parses `percent_done`, `bytes_done`,
  `eta`, `errors`, `completed`. PoolsPage shows live progress bar with ETA.

- **Snapshot cron written to wrong directory:** Fixed from `ConfigDir/cron-snapshots`
  to `/etc/cron.d/dplaneos-snapshots`.

- **SMART prediction logic was dead code:** `TranslateSMARTAttribute()` now
  called by `GET /api/zfs/smart/predict` and a 6-hour background monitor
  that fires `DispatchAlert` on warning/critical predictions.

### Added

- **Central alert dispatch** (`alert_dispatch.go`): single `DispatchAlert(level,
  event, resource, msg)` routes to webhook + SMTP + Telegram. All subsystems use it.

- **Capacity alerts wired:** WARNING (≥80%), CRITICAL (≥90%), EMERGENCY (≥95%)
  with per-pool de-duplication.

- **Automatic disk replacement suggestion:** On hot-swap disk arrival, daemon
  cross-references faulted vdevs. Broadcasts `diskReplacementAvailable` WS event.
  HardwarePage auto-opens Replace modal with suggestion pre-populated.

- **Scrub schedule UI in PoolsPage:** Per-pool schedule modal (daily/weekly/monthly).

- **Replication schedules** (`GET/POST/DELETE /api/replication/schedules`):
  hourly/daily/weekly/manual intervals plus `trigger_on_snapshot` mode.
  ReplicationPage gains a Schedules tab.

- **Post-snapshot replication hook** (`POST /api/zfs/snapshots/cron-hook`):
  Cron jobs call this endpoint enabling Go-side hooks - snapshot, retain, replicate.

- **Time-based snapshot retention:** `retention_days` field on schedules.

- **Dataset search** (`GET /api/zfs/datasets/search?q=<query>`): PoolsPage
  live filter bar with match count and `pool:` prefix support.

- **`GET /api/zfs/resilver/status`**: Parsed resilver progress.

### Stats

| Metric | Before | After |
|--------|--------|-------|
| Alert event constants with zero callers | 8 | 0 |
| DEGRADED pool detection | None | 30 s heartbeat |
| SMART prediction calls | 0 | Background every 6 h |
| Replication triggers | Manual only | Scheduled + post-snapshot |
| Dataset search/filter | None | Live filter + API |
| Resilver progress | Raw string | Parsed %, ETA, bytes |

---

## v4.2.0 (2026-03-09) - "Disk Lifecycle"

Upgrade from: v4.1.2 - Drop-in upgrade via `sudo bash install.sh --upgrade`

### Architecture

This release implements the four pillars of disk lifecycle management -
the foundation required for serious NAS infrastructure:

**1. Disk Discovery (enriched)**
`GET /api/system/disks` now returns stable identifiers for every disk:
`by_id_path` (`/dev/disk/by-id/wwn-0x…`), `by_path_path`, `wwn`, `size_bytes`,
`rpm`, `pool_name`, `health`, `temp_c`. Type detection extended to SAS and USB.
Pool membership and per-vdev health resolved from a single `zpool status -P -v`
pass at discovery time.

**2. Device Renaming / Stable Identifiers (enforced)**
Pool creation via the UI now enforces `/dev/disk/by-id/` paths - matching
the GitOps engine which has always enforced this. Short `/dev/sdX` names
submitted to `POST /api/system/pool/create` are auto-promoted to their by-id
path via sysfs; if promotion fails the request is rejected with a clear error.
Suggestions from the setup wizard use by-id paths.

A new SQLite table `disk_registry` (migration 010) persists serial, WWN,
by-id path, model, pool membership, and last-seen timestamp for every disk
the system has ever encountered. This is the source of truth for identity
across reboots and physical replacements.

**3. Hot-Swap Detection (end-to-end)**
- New `udev/99-dplaneos-hotswap.rules`: covers SATA, SAS, NVMe add/remove
  events for internal pool disks (USB excluded to avoid double-firing with
  the existing removable media rules).
- New `scripts/notify-disk-added.sh` / `notify-disk-removed.sh`: send HTTP
  POST to `http://127.0.0.1:9000/api/internal/disk-event` via curl - replacing
  the broken `nc -U` Unix socket approach.
- New `POST /api/internal/disk-event` (localhost-only): updates disk registry,
  broadcasts `diskAdded`/`diskRemoved`/`poolHealthChanged` WebSocket events.

**4. Pool Import Recovery (automatic)**
On a `diskAdded` event the daemon now:
1. Waits 2 seconds for the kernel to settle the device tree.
2. Runs `zpool import -d /dev/disk/by-id` to enumerate importable pools.
3. Cross-references against the pool registry for any previously-known pool
   whose vdevs match the arriving disk's serial or WWN.
4. If a match is found: runs `zpool import -d /dev/disk/by-id <poolname>`
   automatically and logs the result to the audit chain.
5. Broadcasts `poolHealthChanged` so connected UI clients update instantly.

### Added

- `disk_registry` SQLite table (migration 010): persists full disk identity
  history including `removed_at` timestamp for disks that have been pulled.
- `GET /api/system/disks` enriched fields: `by_id_path`, `by_path_path`,
  `wwn`, `size_bytes`, `rpm`, `pool_name`, `health`, `temp_c`, `dev_path`.
- `POST /api/internal/disk-event`: internal hot-swap notification endpoint.
- `udev/99-dplaneos-hotswap.rules`: hot-swap rules for pool disks.
- `scripts/notify-disk-added.sh`, `scripts/notify-disk-removed.sh`.
- **HardwarePage**: WWN, by-id path, SAS/USB type badges, pool membership
  badge, disk replacement workflow (modal → `POST /api/zfs/pool/replace`).
- **Dashboard**: "Disk Health" section shows SMART failures and high-temp
  warnings across all disks with link to Hardware page.
- **Background monitor**: `CheckMountStatus()` implemented - write-tests each
  pool's mountpoint every 60 seconds, broadcasts `mountError` on failure.
- **Disk temperature monitoring**: reads `/sys/class/hwmon/` sensors every
  5 minutes, falls back to `smartctl`, broadcasts `diskTempWarning` at 45°C
  warning / 55°C critical thresholds.

### Fixed

- `diskTempWarning` WebSocket event was subscribed in frontend but never
  broadcast by daemon - now implemented end-to-end.
- `CheckMountStatus` was an empty stub - now performs real write-test.
- Pool creation accepted raw `/dev/sdX` paths that become invalid after
  reboot - now auto-promotes to by-id or rejects with actionable error.
- Disk type detection did not distinguish SAS from HDD, or USB from SATA -
  now uses vendor string and subsystem symlink for accurate classification.

### Stats

| Metric | Before | After |
|--------|--------|-------|
| Disk identity fields returned | 6 | 14 |
| Hot-swap detection (internal disks) | None | SATA + SAS + NVMe |
| Automatic pool re-import | Manual only | Automatic on disk add |
| Pool creation using stable paths | GitOps only | UI + GitOps |
| Disk registry (persistent identity) | None | SQLite, full history |
| `diskTempWarning` WS events | Dead code | Live, hwmon + smartctl |

---

## v4.1.2 (2026-03-09) - "Completeness"

Upgrade from: v4.1.1 - Drop-in upgrade via `sudo bash install.sh --upgrade`

### Fixed

- **`/api/system/metrics` shape mismatch:** `SupportPage` was showing dashes for
  CPU model, CPU %, uptime, OS, kernel. `HandleSystemMetrics` now returns all
  fields the frontend expects: `cpu_model`, `cpu_percent`, `memory_total`,
  `memory_used`, `uptime`, `os`, `kernel`, `load_avg`. CPU % is sampled over
  200 ms for accuracy.

- **`HandleSystemSettings` was dead code:** The system tuning handler
  (ARC limit, swappiness, inotify thresholds) was never registered and returned
  hardcoded stubs. Now registered at `GET/POST /api/system/tuning`, persists to
  `ConfigDir/system-settings.json`, and applies immediately to the running
  system: ARC limit → `/sys/module/zfs/parameters/zfs_arc_max` +
  `/etc/modprobe.d/zfs.conf`; swappiness → `/proc/sys/vm/swappiness` +
  `/etc/sysctl.d/99-dplaneos.conf`.

- **`install.sh` fatal-die on systems without Go:** If no pre-built binary is
  found and Go is not installed, `install.sh` now auto-downloads the release
  tarball from GitHub Releases, extracts the binary, and continues. Falls back
  to a clear actionable error message if the download also fails. GitHub URL
  corrected throughout (`4nonX/D-PlaneOS`).

- **inotify file watching was a stub:** `HybridIndexer.addRealtimeWatch` stored
  `nil` in the watch map. Now opens a real `inotify_init1` fd per watched path,
  registers `IN_CREATE | IN_DELETE | IN_MODIFY | IN_MOVED_FROM | IN_MOVED_TO |
  IN_CLOSE_WRITE`, and drains events in a per-path goroutine. `RemoveWatch`
  properly closes the fd.

- **Rsync backup task history was always empty:** `GET /api/backup/rsync`
  returned an empty list on every load. Tasks are now persisted to
  `ConfigDir/backup-tasks.json`. Each task record captures ID, source,
  destination, status, start/finish times, exit code, and job ID. Last 50 tasks
  returned newest-first. New `DELETE /api/backup/rsync/{id}` clears a record.

- **Cloud sync had no job tracking:** `listJobs` always returned empty.
  Sync and copy actions now create in-memory `CloudSyncJob` records (ID,
  provider, action, source, destination, status, timing). `GET
  /api/cloud-sync/jobs` returns the last 20 jobs newest-first.

### Added

- **OS package updates (Debian/Ubuntu):** New `UpdatesPage` tab "OS Packages"
  surfaces four new endpoints:
  - `GET /api/system/updates/check` - runs `apt-get update` + `apt list
    --upgradable`, returns structured package list with security flag, non-blocking via job queue
  - `POST /api/system/updates/apply` - runs `apt-get upgrade -y`, non-blocking
  - `POST /api/system/updates/apply-security` - security-only upgrade via
    `unattended-upgrades`, non-blocking
  - `GET /api/system/updates/daemon-version` - checks GitHub Releases API,
    returns current vs latest version with update-available flag

- **ZFS Sandbox page** (`/sandbox`): UI for the existing sandbox backend
  (ephemeral ZFS clones backed by Docker). Create named sandboxes from any
  dataset, destroy to revert all changes, clean orphaned volumes.

- **ZFS Delegation page** (`/delegation`): UI for `zfs allow`. Add and revoke
  fine-grained ZFS permissions per user/group per dataset. Full permission
  checkbox grid (create, destroy, mount, snapshot, rollback, clone, send,
  receive, quota, reservation, hold, release).

- **SMART test trigger in Hardware page:** Per-disk "Short Test" and "Long
  Test" buttons trigger `POST /api/zfs/smart/test`. Results viewable in a modal
  via `GET /api/zfs/smart/results?device=X`.

### Stats

| Metric | Before | After |
|--------|--------|-------|
| Stub/dead handler functions | 3 | 0 |
| Frontend pages with blank metric fields | 1 | 0 |
| Missing frontend pages (backend existed) | 2 | 0 |
| install.sh fatal on no-Go systems | yes | no |
| Real inotify file watching | no | yes |

---

## v4.1.1 (2026-03-09) - "Design System"

### Changed

- **Design system adoption (all pages):** 27 pages previously defined
  per-file `const btnPrimary / btnGhost / btnDanger / inputStyle` objects,
  producing inconsistent padding, missing hover states, and divergent font
  weights across the UI. All removed and replaced with the CSS design system:
  `.btn .btn-primary`, `.btn .btn-ghost`, `.btn .btn-danger`, `.btn-sm`,
  `.input`, `.data-table`.

- **New `.tabs-line` CSS variant:** The existing `.tabs` is a pill/segment
  control. Pages use an underline tab pattern - this is now a first-class
  design system member (`.tabs-line` wrapper + `.tab` / `.tab-active`),
  replacing 13 pages of duplicated inline `borderBottom` logic.

- **Hover / focus states restored uniformly:** Inline style buttons had no
  `:hover` or `:focus-visible` states. All buttons now inherit the glow and
  colour transitions from `index.css`.

- **Unused `import type React` removed** from 14 pages (was only needed
  for `React.CSSProperties` on the deleted style objects).
  Bundle size: −18 KB minified.

### Fixed

- **AppShell:** `ForcePasswordChange` overlay gates the full UI when
  `must_change_password` is set. Strength bar mirrors daemon
  `validatePasswordStrength` rules exactly (8 chars, upper+lower+digit+special).

- **SecurityPage:** New **Password** tab (first tab, default) exposes
  `POST /api/auth/change-password` to all logged-in users at any time.
  Session remains valid after change.

- **LoginPage:** `pending_token` for TOTP verification now sent in JSON body
  as daemon `HandleTOTPVerify` expects, not as a request header.

### Stats

| Metric | Before | After |
|--------|--------|-------|
| Pages with inline style objects | 25 | 0 |
| Pages using CSS design system | 2 | 38 |
| Unused React imports | 14 | 0 |
| JS bundle (minified) | 951 KB | 933 KB |

---

## v4.1.0 (2026-03-08) - "Terminal"

### Feature: Embedded PTY Terminal

- **New `/ws/terminal` WebSocket endpoint (daemon):** Spawns a `bash --login` PTY via `creack/pty` and pipes stdin/stdout over WebSocket. Authenticated by the global `sessionMiddleware` - same session validation as all other endpoints. Each connection gets its own isolated PTY; connections are torn down cleanly when the WebSocket closes.
- **Terminal resize support:** Client sends `{"type":"resize","cols":N,"rows":N}` messages; daemon calls `pty.Setsize()` so shell-aware programs (vim, htop, man) render correctly at any window size.
- **New `TerminalPage` (frontend):** Full xterm.js terminal (`@xterm/xterm` v5) with `FitAddon` (auto-resize) and `WebLinksAddon` (clickable URLs). Colour scheme matches the D-PlaneOS dark theme. Reconnect and Clear buttons in the title bar. Connection status indicator (green/amber/red dot).
- **Sidebar:** Terminal added to the System group (`terminal` icon).
- **Font regression fixed:** `index.html` was loading fonts from `fonts.googleapis.com`. All three fonts (Outfit, JetBrains Mono, Material Symbols Rounded) now load exclusively from `/assets/fonts/` - zero external requests at runtime, fully airgap-safe.

### Added
- `daemon/internal/handlers/terminal_handler.go` - PTY handler
- `daemon/vendor/github.com/creack/pty` v1.1.24
- `app-react/src/pages/TerminalPage.tsx`
- `@xterm/xterm`, `@xterm/addon-fit`, `@xterm/addon-web-links` npm dependencies

### Fixed
- `index.html` CDN font references removed; replaced with `/assets/fonts/fonts.css`
- `fonts.css` updated to use absolute paths (`/assets/fonts/...`)
- Dead `react-vendor` Rollup manual chunk removed from `vite.config.ts`

---


---


## v4.0.0 (2026-03-08) - **"React SPA"**

Upgrade from: v3.3.3 - Drop-in upgrade via `sudo ./scripts/upgrade-with-rollback.sh`

### ⚡ Architecture: Full React SPA Migration

The entire frontend has been rewritten from scratch. 41 standalone vanilla HTML/JS pages replaced by a single-page application built on React 19 + TypeScript + Vite + TanStack Query. The daemon is unchanged - this is a pure frontend replacement.

**Stack:**
- React 19 + TypeScript (0 type errors at build)
- TanStack Router (type-safe navigation - TS error on unregistered routes)
- TanStack Query (data fetching, caching, background refresh)
- Zustand (auth state, WebSocket hub)
- Vite build (tree-shaken, code-split by route)

**37 pages implemented across 10 phases:**

| Phase | Pages |
|-------|-------|
| 0 - Scaffold | AppShell, Sidebar, TopBar, auth/session infrastructure |
| 1 - Core Read-Only | Dashboard, Reporting, Hardware, Logs, Monitoring |
| 2 - Storage | Pools, Shares, NFS, Snapshot Scheduler, Replication |
| 3 - Docker | Docker (containers + compose tabs), Modules |
| 4 - Files | Files, ACL, Removable Media |
| 5 - Users & Security | Users (users/groups/roles tabs), Security, Directory (LDAP) |
| 6 - Network & System | Network, Settings, Alerts, Firewall, Certificates, UPS, Power, IPMI, HA |
| 7 - DevOps | Git Sync, GitOps, Cloud Sync |
| 8 - Admin | Audit, Support, Updates |
| 9 - Wizards | Setup Wizard |
| 10 - WebSocket | Real-time push for Docker state, pool health, disk temps |

### 🐛 Bug Fix: NFS Routes Not Registered (Daemon)

`nfs_handler.go` existed but its routes were never registered in `main.go`. NFS CRUD (`/api/nfs/exports`, `/api/nfs/status`, `/api/nfs/reload`) were silently unreachable in all previous v3.x releases. Routes are now registered.

### 🏗️ Infrastructure: Fully Offline Fonts

All three fonts are bundled in `app/assets/fonts/` - zero external requests at runtime:

| Font | Format | Purpose |
|------|--------|---------|
| `MaterialSymbolsRounded.woff2` | Variable | All icons |
| `outfit.woff2` | Variable (100–900) | UI chrome |
| `jetbrains-mono.woff2` | Variable (100–900) | Code / data display |

### 🏗️ Infrastructure: NixOS Deployment (Corrected)

NixOS configuration updated to reflect accurate current-state facts:
- `system.stateVersion` and `nixpkgs.url` corrected to `25.11` (current stable)
- Default kernel for NixOS 25.11 is `6.12`; our explicit pin to `6.6 LTS` is documented as intentional
- OpenZFS LTS branch is `2.3.x` (not 2.2); ZFS assertion updated to `>= 2.3`
- `lib.fakeHash` → `nixpkgs.lib.fakeHash` (was not in scope in `eachSystem` block - would have caused eval error)

### ✅ Compatibility

Drop-in replacement for v3.3.2. Daemon is unchanged. No schema changes, no migrations, no configuration changes required. The new frontend serves from the same `/opt/dplaneos/app` path.


---

## v3.3.3 (2026-03-07) - **"Async & Governance"**

Upgrade from: v3.3.2, v3.3.1, v3.3.0, or any v3.x - Drop-in upgrade via `sudo ./scripts/upgrade-with-rollback.sh`

### ⚖️ Governance: License Changed to AGPLv3

- **License changed from PolyForm Shield 1.0.0 to GNU Affero General Public License v3.0 (AGPLv3):** D-PlaneOS is now licensed under an OSI-approved open-source license. The AGPLv3 permits free use, modification, and distribution. Modified versions run as a network service must make their source available to users of that service. SPDX identifier: `AGPL-3.0-only`.

- **NixOS users - remove `allowUnfreePredicate`:** Under PolyForm Shield the Nix `meta.license` was set to `licenses.unfree`, requiring `allowUnfreePredicate` or `allowUnfree = true`. AGPLv3 is a free software license. Remove any `allowUnfreePredicate` blocks referencing `dplaneos-daemon` - they are now dead code. The flake's `meta.license` is updated to `licenses.agpl3Only`.

- **Contributor License Agreement introduced:** `CLA-INDIVIDUAL.md` and `CLA-ENTITY.md` added to the repository root. The CLA grants the maintainer the right to re-license commercially in the future; contributors retain full ownership. Signing is handled via CLA Assistant bot on pull requests.

### ⚡ Feature: Async Job Store (Daemon)

- **New `daemon/internal/jobs/jobs.go` package:** In-process, in-memory job store for long-running operations. Each job has a UUID, status (`running` → `done` / `failed`), result payload, and error string. Concurrent-safe. State is ephemeral - does not survive daemon restarts, acceptable because all jobs are short-lived.

- **New `GET /api/jobs/{id}` route (`jobs_handler.go`):** Poll for job status. Returns `{"status":"running"}` while in progress, `{"status":"done","result":{...}}` on success, or `{"status":"failed","error":"..."}` on failure.

- **8 blocking endpoints converted to async (HTTP 202):**

  | Endpoint | Typical duration |
  |---|---|
  | `POST /api/replication/send` | Seconds – hours |
  | `POST /api/replication/send-incremental` | Seconds – hours |
  | `POST /api/replication/receive` | Seconds – hours |
  | `POST /api/backup/rsync` | Minutes – hours |
  | `POST /api/docker/pull` | 30s – 10 min |
  | `POST /api/docker/update` | 1 – 5 min |
  | `POST /api/docker/compose/up` | 10s – 5 min |
  | `POST /api/docker/compose/down` | 5 – 60s |

  **Breaking change:** These endpoints now return `{"job_id":"<uuid>"}` immediately. API consumers that expect a result in the response body must update to the poll pattern.

### ⚡ Feature: Frontend Async Polling - `ui.pollJob()`

- **New `DPlaneUI.pollJob()` in `ui-components.js`:** Single consistent polling loop for all async operations. Shows loading overlay immediately, polls `GET /api/jobs/{id}` every 2 seconds, retries on transient network errors, enforces 30-minute hard timeout, hides overlay in all exit paths.

- **`docker.html` - 4 operations updated:** `composeUp`, `composeDown`, `pullImage`, `updateContainer` now dispatch via `ui.pollJob()`.

- **`replication.html` - 2 operations updated:** `runTask` and `startReplication` dispatch via `ui.pollJob()`. Replication start button now correctly restores its icon on job completion.

### 🐛 Bug: Navigation Stub Redirects and Missing NFS Entry

- **5 nav stub redirects replaced with direct links** in `nav-shared.js`: Interfaces → `network.html#interfaces`, DNS → `network.html#dns`, Routing → `network.html#routing`, System Settings → `settings.html`, File Upload → `files.html`. Stubs retained for existing bookmarks.

- **`data-page` mismatch fixed:** File Upload nav entry had `data-page="files-enhanced"`, breaking the active-page highlight on `files.html`. Fixed to `data-page="files"`.

- **NFS Exports added to nav:** `nfs.html` has been a complete, functional NFS management page since v3.x but had no navigation entry - unreachable without a direct URL. Now listed under Storage → NFS Exports (between Shares and Replication).

### 🏗️ NixOS: `ota-module.nix` Options

- **`options.services.dplaneos.ota` namespace added:** Two new tunable options: `ota.enable` (default: `true`) to disable the health-check timer independently of the daemon, and `ota.healthCheckDelay` (default: `"90s"`) to tune the post-boot wait. Module is gated on `lib.mkIf (cfg.enable && cfg.ota.enable)`.

### ✅ Compatibility

Drop-in replacement for v3.3.2 with one exception: the 8 async endpoints now return `{"job_id":"..."}` with HTTP 202 instead of blocking. All other API surface, schema, and configuration unchanged.

**NixOS users only:** Remove any `allowUnfreePredicate` or `allowUnfree` blocks referencing `dplaneos-daemon`.

---

## v3.3.2 (2026-03-01) - **"Runtime fixes"**

Upgrade from: v3.3.1, v3.3.0, or any v3.x - Drop-in upgrade via `sudo ./scripts/upgrade-with-rollback.sh`

### 🔒 Security: Eliminated `bash -c` Shell Construction in Replication

- **`replication_remote.go` - shell injection vector removed:** Both the normal and resume-token replication paths previously built a complete shell pipeline string via `fmt.Sprintf` and executed it with `executeCommand("/bin/bash", []string{"-c", fullCmd})`. Despite upstream input validation, string-formatted shell commands are an inherently fragile security boundary. The entire replication pipeline (`zfs send` → optional `pv` → `ssh recv`) is now implemented as three discrete `exec.Command` processes connected via Go `io.Pipe` in a new `execPipedZFSSend()` helper. No shell is invoked at any point.

- **Resume token validation added:** ZFS resume tokens are now validated with `isValidResumeToken()` (alphanumeric + base64 characters only, max 4096 bytes) before being used as a command argument. Previously the token was passed directly from the SSH remote into `fmt.Sprintf`.

- **Error responses no longer leak command strings:** The `"command": fullCmd` field previously included in replication failure responses exposed the full constructed shell command to API callers. This field has been removed.

### 🔒 Security: iSCSI Authentication Default Made Explicit

- **`iscsi.go` - `authentication=0` is now an explicit opt-out, not a silent default:** Every new iSCSI target previously had CHAP authentication disabled silently. A new `require_chap` boolean field has been added to `ISCSICreateRequest`. When `require_chap: true`, the TPG is created with `authentication=1`. When `require_chap: false` (the current default for backward compatibility), `authentication=0` is still set but a `SECURITY NOTICE` log line is emitted, making the decision auditable. This is a **non-breaking change** - existing API callers that do not include `require_chap` behave identically to before.

### 🐛 Bug: LDAP `TriggerSync` - Full Implementation

- **`ldap.go` + `ldap/client.go` - sync now actually syncs:** `POST /api/ldap/sync` previously connected to the LDAP server, bound the service account, and immediately returned `{"success": true}` with 0 users found/created/updated. No directory data was read or written. This has been replaced with a full implementation:
  - New `SyncAll()` method on the LDAP client performs a wildcard search against the configured `BaseDN` using the configured `UserFilter`, fetches all matching entries, and retrieves group memberships for each
  - `TriggerSync` upserts each user into the `users` table (`source='ldap'`, empty `password_hash`) applying group→role mapping via the existing `GroupMappings` config
  - Response now returns real counts: `users_found`, `users_created`, `users_updated`, `users_skipped`, and `errors` per user

### 🐛 Bug: Version String Never Embedded in Binary

- **`daemon/cmd/dplaned/main.go` - `Version` changed from `const` to `var`:** The `Version` identifier was declared as a `const`, but Go's `-ldflags "-X main.Version=..."` mechanism only works with package-level `var` declarations. As a result, all previous release builds reported `version: "dev"` at `/health` and in startup logs regardless of the version tag. Changed to `var` - version is now correctly embedded at build time and visible in the health endpoint.

- **README:** Removed "No other NAS OS does this" from the container update description (snapshot+rollback is standard practice in the NAS space). Removed unsupported "100× faster" benchmark claim from replication description. Changed "injection-hardened" to "allowlist-based input validation" (more accurate). Added explicit HA limitations section. Fixed LDAP feature list to reflect actual implementation.
- **INSTALLATION-GUIDE:** Removed "enterprise NAS" language.
- **SECURITY.md:** Updated command execution description to reflect the `bash -c` removal. Added HA and LDAP known limitations to the Known Limitations section.
- **THREAT-MODEL.md:** Updated T1 (Command Injection) to document the replication fix. Added T13 (HA Split-Brain) as a new threat entry with HIGH residual risk rating and mitigation guidance.
- **ADMIN-GUIDE:** Updated LDAP sync documentation to accurately describe the full-directory sync behavior.
- **HA `cluster.go`:** Package comment expanded with explicit NO-STONITH, NO-automatic-failover, NO-split-brain-protection, and NO-quorum warnings.

### ✅ Compatibility

Drop-in replacement for v3.3.1. No schema changes, no migrations, no configuration changes required. The `require_chap` field in iSCSI create requests defaults to `false` - existing API integrations are unaffected.

---

## v3.3.1 (2026-02-25) - **"Universal Compatibility"**

Upgrade from: v3.3.0, v3.2.1, or any v3.x - Drop-in upgrade via `sudo ./scripts/upgrade-with-rollback.sh`

### 🐛 Bug Fixes

- **Ubuntu readonly variable crash (Phase 0):** `install.sh`, `get.sh`, `scripts/pre-flight.sh`, and `scripts/system-audit.sh` previously sourced `/etc/os-release` directly, which caused Ubuntu to abort with `/etc/os-release: line 4: VERSION: readonly variable` and fail at Phase 0 before any installation occurred. All four files now use safe `grep`-based extraction into scoped variables - the OS-managed `VERSION` variable is never touched.

- **`TERM environment variable not set` warning:** `install.sh` called `clear` unconditionally at startup and on the completion screen. In non-interactive contexts (serial console, VM without TTY, piped install) this emitted a `TERM` warning that polluted output and confused users expecting a clean install log. Both `clear` calls are now guarded with `[ -n "$TERM" ]`.

### 🐧 NixOS Compatibility

- **NixOS no longer causes install termination:** `install.sh` Phase 0 OS detection previously treated any unrecognised `$ID` as a fatal error. NixOS is now detected as a named case, emits an informational warning directing users to `nixos/NIXOS-INSTALL-GUIDE.md`, and continues rather than aborting. All NixOS-specific files remain untouched under `nixos/`.

### 🚀 Phase 12: Dynamic IP Notification

- **Access URL displayed at completion:** Phase 12 now calculates the primary IPv4 address via `hostname -I` and displays it in a clearly bordered completion box - `http://<PRIMARY_IP>` - along with a notice that the VM screen may remain black after install. Eliminates the most common post-install support question.

### 📋 CI / Release Pipeline

- **Syncthing conflict file guard:** Both `validate.yml` and `release.yml` now fail immediately at checkout with a clear error listing any `.sync-conflict-*.go` files present in the tree, preventing the duplicate symbol build failures caused by Syncthing writing conflict copies into the working directory.
- **`*.sync-conflict-*` excluded from release tarballs:** `rsync` in the package step explicitly excludes all Syncthing conflict files regardless of the guard.
- **`build/` directory pre-created before daemon build:** `go build` does not create parent directories; the `mkdir -p ../build` step was missing, which would cause the release build to fail if `build/` did not already exist in the workspace.
- **`sha256sum` step simplified:** Removed fragile `cd /tmp && basename` pattern; checksum is now generated directly from the full `$TARBALL` path.
- **Double-trigger removed from `release.yml`:** The `on: release: types: [created]` trigger was firing the release job a second time when GitHub auto-created the release after a tag push. Now triggers only on `push: tags: v*`.
- **bcrypt CI password injection fixed:** `validate.yml` was embedding `CI_PASS` directly into a Python string literal via shell substitution. Password is now passed via `env:` and read with `os.environ` inside Python, making it safe for passwords containing `!`, `$`, or `'`.
- **ZFS cleanup order fixed:** `validate.yml` cleanup was calling `losetup -d` on all loop devices in a single command before `zpool destroy`, which hangs ZFS when its backing devices are detached first. Now destroys the pool first, then detaches each loop device individually.
- **Release notes regex hardened:** `extract-release-notes.py` now matches CHANGELOG headings with or without the `v` prefix (`## v3.3.1` or `## 3.3.1`).
- **Wrong install command in release notes:** The generated installation instructions used `sudo make install` (no `Makefile` exists); corrected to `sudo bash install.sh`.

### 📄 Licensing & Documentation

- **Corrected "open-source" misidentification:** D-PlaneOS is licensed under GNU Affero General Public License v3.0 (AGPLv3), which is **source-available**, not OSI-approved open-source. Corrected in `README.md`, `SHOWSTOPPER-MITIGATION-GUIDE.md`, `docs/SHOWSTOPPER-MITIGATION-GUIDE.md`, and `nixos/NIXOS-README.md`. Third-party dependency references (ZFS, Samba, Docker, nginx) correctly retain their "open-source" descriptions as those packages use OSI-approved licenses.

### ✅ Compatibility

Drop-in replacement for v3.3.0. No schema changes, no migrations, no config changes required.

---

## v3.3.0 (2026-02-22) - **"UX / Security Hardening"**

Upgrade from: v3.2.1, v3.2.0, or v3.1.x - Drop-in upgrade via `sudo ./scripts/upgrade-with-rollback.sh`

### ⚡ Architecture: Boot-Order Hardening (`dplaneos-init-db`)

- New `dplaneos-init-db.service` acts as mandatory gatekeeper before API and Event daemons start
- Executes `init-database-with-lock.sh` to eliminate schema-creation race conditions
- Runs `validate-db-schema.sh` to verify SQLite FTS5 integrity
- All core daemons strictly `Require` this service to complete before startup

### 🔒 Security: HMAC Audit Chain & Zero-Trust API

- **Tamper-proof audit log:** every administrative action hashed and chained with HMAC-SHA256; WebUI flags integrity violations immediately
- **Strict parameter validation:** all API inputs (ZFS pool names, Docker IDs, filesystem paths) pass through whitelist-only regex engine - prevents shell injection and malformed parameter attacks
- **RBAC foundation:** SQLite schema extended with dedicated Role-Based Access Control tables; groundwork for multi-user / enterprise deployments

### 🔌 Storage: Real-Time udev Reactivity

- New udev rules trigger immediate WebUI updates on hardware state changes
- Detects insertion/removal of USB storage devices, optical media (CD/DVD/Blu-ray)
- WebUI can issue physical eject commands to compatible drives
- Eject synchronized with ZFS unmount workflows - prevents data loss during media removal

### 🔐 Password UX - Unified & Predictable

**Backend (Go)**
- Password validation centralized via `validatePasswordStrength()` - eliminates rule drift between handlers
- All password inputs normalized with `strings.TrimSpace()` - prevents invisible copy/paste whitespace failures

**Frontend**
- Real-time strength checklist (mirrors backend rules), show/hide toggle, live confirm-match indicator
- Client-side pre-validation reduces failed API calls
- Affected pages: `login.html`, `users.html`, `setup-wizard.html`

### 🔔 Notifications & UX Hardening

- All toast notifications now fully dismissible (×), unified top-right positioning, hover pauses auto-dismiss
- **Unsaved Changes Guard:** Material Design 3 warning banner + browser `beforeunload` safeguard; applied to `network.html`, `settings.html`
- **Double-submit protection:** apply/save buttons disabled during API calls, safe re-enable via `finally` logic - prevents duplicate operations and race conditions

### 🎨 UI: Material Design 3 Proportions

- Migrated to 8px grid system with `rem` units for resolution-independent sizing
- Sidebar adapts to bottom navigation rail on mobile
- Material Symbols Rounded integrated as variable font (programmatic weight/theme adjustments)

### New Components

- `dplaneos-init-db.service`
- `password-strength.js`
- `unsaved-changes.js`

### ✅ Compatibility

Drop-in replacement for v3.2.1. No schema changes, no migrations required (optional FTS5 optimization available).

---

## v3.2.1 (2026-02-21) - **"XSS Sanitisation"**

### 🔒 Security: Frontend XSS sanitisation (T5 closure)
- Added `esc()` / `escapeHtml()` sanitiser to all frontend pages and the alert system
- Server-sourced values (`alert.title`, `alert.message`, `alert.alert_id`, log fields, UPS hardware strings, dataset names, error messages) are now escaped before `innerHTML` insertion
- Affected files: `alert-system.js`, `audit.html`, `docker.html`, `iscsi.html`, `pools.html`, `ups.html`, `reporting.html`, `system-updates.html`
- T5 residual risk downgraded from MEDIUM to LOW in `THREAT-MODEL.md`

### 📄 Documentation
- All version references bumped to v3.2.1 across all docs, scripts, and NixOS modules
- `THREAT-MODEL.md` updated to reflect v3.2.1 security posture (T5 mitigated, Known Gaps updated)

### ✅ Compatibility
- Drop-in replacement for v3.2.0. No schema changes, no daemon flag changes, no config changes.
- Binary rebuilt with `-X main.Version=3.2.1`

---

## v3.2.0 (2026-02-21) - **"networkd Persistence"**

### ⚡ Architecture: systemd-networkd file writer (networkdwriter)
- New package `internal/networkdwriter`: writes `/etc/systemd/network/50-dplane-*.{network,netdev}`
- All network changes now survive reboots AND `nixos-rebuild switch` - no extra steps required
- `networkctl reload` used for zero-downtime live reload (< 1 second)
- Works on every systemd distro: NixOS, Debian, Ubuntu, Arch
- nixwriter scope reduced to NixOS-only settings (firewall ports, Samba globals)
- hostname/timezone/NTP already persistent via OS-level tool calls - no nixwriter needed

### ✅ Completeness
- All 12 nixwriter methods fully wired; all 9 stanzas covered
- New `/api/firewall/sync` endpoint for explicit NixOS firewall port sync
- DNS now has POST handler (`action: set_dns`) + `SetGlobalDNS` via resolved dropin
- `HandleSettings` runtime POST wires hostname + timezone persist calls
- `/etc/systemd/network` added to `ReadWritePaths` in `module.nix`

---

## v3.1.0 (2026-02-21) - **"NixOS Architecture Hardening"**

### ⚡ Architecture: Static musl binary + nixwriter + boot reconciler
- Static musl binary via `pkgsStatic`: glibc-independent, survives NixOS upgrades
- `internal/nixwriter`: writes `dplane-generated.nix` fragments for persistent NixOS config
- Boot reconciler: re-applies VLANs/bonds/static IPs from SQLite DB on non-NixOS systems
- Samba persistence: declarative NixOS ownership + imperative share management via include bridge
- `/etc/systemd/network` naming convention: NixOS owns `10-`/`20-` prefix, D-PlaneOS owns `50-dplane-`

### 🔒 Security & Stability
- SSH hardening: `PasswordAuthentication=false`, `PermitRootLogin=no`; new `sshKeys` NixOS module option
- Support bundle: `POST /api/system/support-bundle` - streams diagnostic `.tar.gz` (ZFS, SMART, journal, audit tail)
- Pre-upgrade ZFS snapshots: automatic `@pre-upgrade-<timestamp>` on all pools before every `nixos-rebuild switch`; `GET /api/nixos/pre-upgrade-snapshots`
- Webhook alerting: generic HTTP webhooks for all system events; `GET/POST/DELETE /api/alerts/webhooks`, test endpoint
- Audit HMAC chain: tamper-evident audit log with HMAC-SHA256; `GET /api/system/audit/verify-chain`; key at `/var/lib/dplaneos/audit.key`

### 📊 Monitoring & Real-Time Alerting
- Background monitor: debounced alerting (5 min cooldown, 30 s hysteresis) for inotify, ZFS health, capacity
- WebSocket hub: real-time events at `WS /api/ws/monitor`
- ZFS pool heartbeat: active I/O test every 30 s; auto-stops Docker on pool failure
- Capacity guardian: configurable thresholds, emergency reserve dataset, auto-release at 95%+
- Deep ZFS health: per-disk risk scoring, SMART JSON integration; `GET /api/zfs/health`
- SMTP alerting: configurable SMTP for system alerts

### 🔁 GitOps
- Declarative `state.yaml`: schema for pools, datasets, shares with stdlib YAML parser
- By-ID enforcement: `/dev/disk/by-id/` required; bare `/dev/sdX` rejected at parse time
- Diff engine: CREATE / MODIFY / DELETE / BLOCKED / NOP classification with risk levels
- Safety contract: pool destroy always BLOCKED; dataset destroy blocked if used > 0 bytes; share remove blocked if active SMB connections
- Transactional apply: halts on unapproved BLOCKED items; idempotent operations
- Drift detection: background worker every 5 min; broadcasts `gitops.drift` WebSocket event
- API: `GET /api/gitops/plan`, `POST /api/gitops/apply`, `POST /api/gitops/approve`, `GET/PUT /api/gitops/state`

### 🏗️ Appliance Hardening (NixOS)
- A/B partition layout (`disko.nix`): EFI + system-a (8 G) + system-b (8 G) + persist (remaining)
- OTA update flow (`ota-update.sh`): Ed25519 signature verification, A/B slot switch, 90 s auto-revert health check
- NixOS OTA module (`ota-module.nix`): systemd health check timer, daemon integration
- Version pinning (`flake.nix`): kernel 6.6 LTS + OpenZFS 2.2, eval-time assertions
- Impermanence layer (`impermanence.nix`): ephemeral root, all state persisted to `/persist`

### New API routes
```
POST   /api/system/support-bundle
GET    /api/nixos/pre-upgrade-snapshots
GET    /api/system/audit/verify-chain
GET    /api/alerts/webhooks
POST   /api/alerts/webhooks
DELETE /api/alerts/webhooks/{id}
POST   /api/alerts/webhooks/{id}/test
GET    /api/gitops/status
GET    /api/gitops/plan
POST   /api/gitops/apply
POST   /api/gitops/approve
POST   /api/gitops/check
GET    /api/gitops/state
PUT    /api/gitops/state
WS     /api/ws/monitor
GET    /api/zfs/health
GET    /api/zfs/iostat
GET    /api/zfs/events
GET    /api/zfs/capacity
POST   /api/zfs/capacity/reserve
POST   /api/zfs/capacity/release
```

---

## v3.0.0 (2026-02-18) - **"Native Docker API"**

### ⚡ Major: Docker exec.Command → stdlib REST client

All container lifecycle operations now use the Docker Engine REST API directly over `/var/run/docker.sock` via a thin stdlib `net/http` client - zero new dependencies, no CGO, no shell involved.

**New package: `internal/dockerclient`** (pure stdlib, no imports outside std library)

| Method | Replaces |
|---|---|
| `ListAll(ctx)` | `docker ps -a --format {{json .}}` |
| `Inspect(ctx, id)` | `docker inspect --format ... NAME` |
| `Start(ctx, id)` | `docker start NAME` |
| `Stop(ctx, id, t)` | `docker stop -t T NAME` |
| `Restart(ctx, id, t)` | `docker restart NAME` |
| `Pause(ctx, id)` | `docker pause NAME` |
| `Unpause(ctx, id)` | `docker unpause NAME` |
| `Remove(ctx, id, force, vol)` | `docker rm [-f] [-v] NAME` |
| `PullImage(ctx, image)` | `docker pull IMAGE` |
| `Logs(ctx, id, opts)` | `docker logs --tail N NAME` |
| `WaitForHealthy(ctx, id, timeout)` | `docker inspect` polling loop |
| `IsAvailable(ctx)` | `docker info` / `which docker` |

### ⚡ Major: Linux netlink (`ip link/addr/route`) → stdlib syscall client

New package: `internal/netlinkx` - rtnetlink via raw `syscall.Socket(AF_NETLINK, ...)`, no external dependencies, no CGO. Replaces ~15 `ip(8)` exec calls across `system.go` and `network_advanced.go`.

### 🔒 Security fix: Git repository URL RCE via `ext::` transport

**Severity: Critical** - `ext::` transport executes arbitrary subprocesses as root daemon user. Fix: `validateRepoURL()` enforces allowlist of permitted schemes (`https://`, `http://`, `git://`, `ssh://`, `git@host:path`). Blocks `ext::`, `file://`, `fd::`, and custom transports. Applied at `TestConnectivity` and `SaveRepo`.

### 🎨 UI Consolidation
- Shared navigation injected via `nav-shared.js` - eliminates 8KB nav HTML duplicated across 20 pages
- `dplaneos-ui-complete.css` now includes global reset
- NixOS configuration files added (`nixos/flake.nix`, `nixos/module.nix`, `nixos/configuration-standalone.nix`, `nixos/setup-nixos.sh`)

---

## v2.2.1 (2026-02-18) - **"Security & Reliability Audit Fixes"**

### 🔴 Critical: Runtime ZFS Pool Loss → Docker Still Running
- New `pool_heartbeat.go` - `maybeStopDocker()`: calls `systemctl stop docker` on `SUSPENDED/UNAVAIL` or write-probe failure
- Guard fires only once per failure window, resets on pool recovery

### 🔴 Critical: Path Traversal in Git Sync compose_path
- `validateComposePath()`: rejects absolute paths/null bytes, `filepath.Clean()` + prefix check
- Applied in 4 places: `SaveRepo`, `DeployRepo`, `ExportToRepo`, `PushRepo`

### 🟡 Medium: Audit Buffer - Security Events Lost on SIGKILL
- 10 security-critical action types bypass buffer, write directly to SQLite: `login`, `login_failed`, `logout`, `auth_failed`, `permission_denied`, `user_created/deleted`, `password_changed`, `token_created/revoked`

### 🟡 Medium: Health Check - False Positives for Slow Apps
- `waitForHealthy()` polls every 2s with Docker `HEALTHCHECK` awareness; default raised from 5s to 30s; `unhealthy` fails immediately

### 🟢 Low: ECC Detection Unreliable in VMs
- VM detection via `/sys/class/dmi/id/product_name` and `/proc/cpuinfo` hypervisor bit; three states: Physical+ECC / Physical+no ECC / VM

---

## v2.2.0 (2026-02-17) - **"Git Sync: Bidirectional Multi-Repo"**

### ✨ New Feature: Bidirectional Git Sync

Full GitHub/Gitea integration for Docker Compose stacks - no external tool required.

| Direction | Trigger | Effect |
|---|---|---|
| Pull ← Git | Manual / Auto | Clone or pull repo, update local compose file |
| Deploy ← Git | Manual | `docker compose up -d` from repo compose file |
| Export → Git | Manual | Snapshot running containers as `docker-compose.yml` |
| Push → Git | Manual | `git commit + push` compose file to remote |

- Multi-repo syncs with per-sync credential references, auto-sync intervals, commit author identity
- Credential store (`git_credentials` table): PAT via `GIT_ASKPASS`, SSH key via `GIT_SSH_COMMAND`
- New backend: `git_sync_repos.go` - full CRUD + pull/push/deploy/export endpoints
- New frontend: `git-sync.html` (956 lines) - three-tab layout, per-sync cards, PAT setup wizard
- Legacy single-repo config fully preserved on "Legacy Config" tab

---

## v2.1.1 (2026-02-17) - **"Security, Stability & Architecture"**

### 🔴 Showstopper Fix: ZFS-Docker Boot Race (Critical)
- Hard systemd gate (`dplaneos-zfs-mount-wait.service`): polls until every configured pool is `ONLINE`, mounted, and writable
- `dplaned.service` and `docker.service` both `Require=` this gate - cannot start without it
- 5-minute timeout with 30-second progress logging

### 🟡 Notification Debouncing (Flooding Fix)
- `monitoring/background.go` rewritten: 30s hysteresis, 5 min cooldown, clear event on resolution, 2 min clearance cooldown

### 🟡 SQLite Durability (Power-Loss Safety)
- All 5 DB connections upgraded to `_synchronous=FULL`; consistent across all connections

### 🔒 Security Fixes (XSS)
- All `onclick="func('${serverData}')"` patterns replaced with `JSON.stringify()` across 9 pages
- `settings.html`: NixOS version display switched from `innerHTML` to `textContent`

### 🐛 Bug Fixes
- URL fixes (`&param=` → `?param=`): 5 locations
- `disk_discovery.go`: recursive `hasMountPoint()`, regex `diskNameInZpoolStatus()`
- `zfs_encryption.go`: `change-key -l` flag; key validation
- New: `daemon/internal/handlers/disk_discovery_test.go`

---

## v2.1.0 (2026-02-15) - **"ZFS-Docker Integration"**

### ⚡ Safe Container Updates (Killer Feature)

`POST /api/docker/update` - atomic container updates with ZFS data protection:
1. Creates ZFS snapshot of container volume
2. Pulls new image
3. Stops and restarts container
4. Runs health check (5s)
5. On failure: returns snapshot name for instant rollback

### Added
- ZFS Snapshot CRUD, Time Machine (file-level restore), Sandbox (ZFS clone environments)
- ZFS Remote Replication (`zfs send | ssh zfs recv`)
- ZFS Health Predictor (per-disk risk scoring, SMART integration)
- NixOS Config Guard (dry-activate validation, generation management)
- Docker Compose, Container Stats, Pool Capacity Guardian
- Routes: 105 → 171 (+66 new endpoints)

---

## v2.0.0 (2026-02-12) - **"Ground-Up Rewrite"**

Complete rewrite: PHP/Apache stack replaced by single Go binary (`dplaned`, 8 MB). Not an in-place upgrade from v1.x.

| Component | v1.x (PHP) | v2.0.0 (Go) |
|-----------|-----------|-------------|
| Backend | PHP-FPM + Apache | Single Go binary (`dplaned`, 8 MB) |
| Database | SQLite via PHP PDO | SQLite/WAL, 64 MB cache, FULL sync |
| Auth | PHP sessions | Session tokens + RBAC middleware |
| Frontend | PHP-rendered SPA | Static HTML + vanilla JS |
| Install | `install.sh` (50+ steps) | `make install` (one command) |

- 38 Go source files, 85 API routes, 41 HTML pages
- Full LDAP/AD integration, RBAC engine, buffered audit logging, WebSocket live updates
- ZFS encryption, removable media, replication, snapshot scheduler, firewall, TLS management
- Docker management, file browser, ACL/quota management, UPS monitoring, IPMI
- Input validation on all `exec.Command` calls, command whitelist, rate limiting

### ⚠️ Breaking Changes
Fresh install required. ZFS pools, datasets, shares, and Docker containers preserved on disk.

---

## v1.14.0-OMEGA (2026-02-01) - **"OMEGA Edition"**

First fully production-ready PHP release. Fixes 7 critical infrastructure bugs.

1. **www-data sudo permissions missing** (CRITICAL)
2. **SQLite write permissions** (CRITICAL)
3. **Login loop on cold start** (HIGH)
4. **API timeout handling** (HIGH)
5. **Silent session expiry** (MEDIUM)
6. **No loading feedback** (LOW)
7. **Style flash on load** (LOW)

---

## v1.12.0 (2026-01-31) - **"The Big Fix"**

45 vulnerabilities from comprehensive penetration test - 10 Critical, 7 High fixed.

---

## v1.11.0 (2026-01-31) - **"Vibecoded Security Theater Fix"**

- `execCommand()` checked if string `"escapeshellarg"` appeared in command, not whether arguments were actually escaped - 108 vulnerable call sites. Complete rewrite with strict command whitelisting.

---

## v1.10.0 (2026-01-31) - **"Smart State Polling & One-Click Updates"**

- ETag-based smart polling (95% bandwidth reduction, 88% CPU reduction)
- ZFS snapshot-based update system with automatic rollback
- License: MIT → GNU Affero General Public License v3.0 (AGPLv3)

---

## v1.9.0 (2026-01-30) - **"RBAC & Security Fixes"**

- Role-Based Access Control: Admin, User, Readonly roles
- 7 critical security fixes including session fixation, wildcard sudoers, Docker Compose YAML injection

---

## v1.8.0 (2026-01-28) - **"Power User Release"**

- File browser, ZFS native encryption, system service control, real-time monitoring - all 14 tabs functional

---

## v1.7.0 (2026-01-28) - **"The Paranoia Update"**

- UPS/USV management (NUT), automatic snapshot scheduling, system log viewer

---

## v1.6.0 (2026-01-28) - **"Disk Health & Notifications"**

- SMART monitoring, disk replacement tracking, notification center

---

## v1.2.0 - **"Initial Public Release"**

- ZFS management, Docker integration, system monitoring, session auth, audit logging, CSRF protection

---

## Upgrade Path

### v3.2.1 → v3.3.0
```bash
sudo ./scripts/upgrade-with-rollback.sh
```

### v1.14.0-OMEGA → v2.0.0
Fresh install required. ZFS pools and Docker containers preserved on disk.
```bash
tar xzf dplaneos-v2.0.0-production-vendored.tar.gz
cd dplaneos && sudo make install
```

---

## Support

**Security issues:** GitHub issues with `security` label. Response: Critical 24h, High 72h, Medium/Low 1 week.
**Bug reports:** GitHub issue with version, steps to reproduce, and logs.
**Feature requests:** GitHub issue with `enhancement` label.


