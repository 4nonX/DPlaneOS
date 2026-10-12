# DPlaneOS Backup and Replication

What protects what, and how to set each of it up. Everything here is in the web interface under **Data Protection**.

---

## What needs a backup

| | Where it is | Protected by |
|---|---|---|
| Your files | ZFS pools on the data disks | snapshots (against mistakes), replication or cloud sync (against losing the machine) |
| The configuration: users, shares, schedules, settings | the system's database, on the system disk | a copy of the database ([Recovery, section 6](RECOVERY.md#6-the-database-and-the-configuration)), or `state.yaml` in Git ([Git-Driven NAS](GITOPS-DRIVEN-NAS.md)) |
| Container data | wherever the stack's volumes point; put them on a pool | as your files |
| The system itself | the system disk | nothing needed: it is reinstalled from the installation medium |

A snapshot is not a backup: it lives in the same pool as the data. A pool that is lost takes its snapshots with it.

---

## Snapshots

A snapshot is the state of a dataset at one moment. It costs nothing when taken and grows only with what changes afterwards.

**Data Protection › Snapshot Scheduler**: per dataset, how often (hourly, daily, weekly, monthly), and how many to keep, by number or by age. The scheduler removes the snapshots it made when they fall out of the retention; snapshots made by hand are never removed by it. *Run now* takes one at once.

**Getting files back:**

- **Data Protection › Previous Versions** browses a dataset's snapshots and restores single files or folders.
- From a client: on SMB shares with the option *Previous Versions* switched on, Windows shows them in the folder's properties; on the system and over NFS they are in the hidden folder `.zfs/snapshot/<name>/` of the dataset.
- **Rollback** (Datasets page) returns the whole dataset to a snapshot. Everything written since is lost, and so are the snapshots after it.

---

## Replication to another system

Replication is configured through two screens under **Data Protection → Replication** in the nav:

**Step 1 - Add a Peer** (Replication: Peers: Add Peer)

Enter a name, host, SSH user and port. For another DPlaneOS system the user is an administrator of that system (root does not sign in over SSH there; the administrator runs `zfs` through `sudo`); for other ZFS systems, a user that may run `zfs receive`. Then click **Authorize** and enter the host's SSH password once. The daemon installs its ed25519 replication key via the Go SSH client. The password exists only in the request buffer and is never stored. After authorization, click **Test** to confirm key-based access and ZFS readiness.

For air-gapped or high-security hosts where password auth is disabled: copy the **Sovereign Target Key** shown in the Peers panel to the target's `authorized_keys` manually, then click **Test** to verify and pin the fingerprint. No password is required.

**If the remote host's SSH host key changes** (hardware replacement, OS reinstall, or deliberate key rotation): click **Reset Trust** on the peer row. This clears the stored fingerprint without removing authorization - the replication key stays installed. Replication jobs will continue in TOFU mode (accepting any host key) until you run **Test** again to pin the new fingerprint. Run Test immediately after Reset Trust to close the TOFU window.

**Step 2 - Create a Schedule** (Replication: Schedules: Add Schedule)

| Field | Notes |
|---|---|
| Source dataset | The local ZFS dataset to replicate |
| Peer | Select from authorized peers |
| Remote pool | Destination pool/dataset on the remote (default: mirrors source name) |
| Interval | Hourly, Daily, Weekly, or Manual only |
| Trigger after snapshot | Run immediately after each automatic snapshot in addition to the interval |
| Incremental | Send only changes since the last replicated snapshot (tracked automatically) |
| Resume | Check for an interrupted transfer token before sending |
| Recursive | Include child datasets in the send stream (`-R`). Checked by default. Uncheck to send only the exact dataset. |
| Compress | Enable lz4 stream compression |
| Rate limit MB/s | Bandwidth cap via `pv` (0 = unlimited; requires `pv` installed on the source) |

The background monitor fires due schedules every 5 minutes. Click **Run Now** on any schedule to trigger an immediate run regardless of interval.

This is also declarable in `state.yaml` under `replication:`:
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

`trigger_on_snapshot: true` means replication runs immediately after each automatic snapshot, rather than only on the configured interval.

**Reliability features (v14.0.0):** Transient failures trigger automatic retry with exponential backoff (up to 3 attempts: immediate, +30 seconds, +120 seconds). Two schedules targeting the same source dataset and remote destination cannot run concurrently - the second is skipped with a "conflict" status to prevent ZFS stream corruption. Before each incremental send, the daemon verifies the base snapshot exists on the remote; if the chain is broken, it falls back to a full send with a log message. After every `zfs recv`, the daemon verifies the expected snapshot exists on the destination and fails the job explicitly if absent.

---

## Snapshot Retention Policies

DPlaneOS automatically prunes old local snapshots based on per-bucket retention counts. Only snapshots matching the configured prefix are pruned; manually created snapshots are never touched. Retention runs after each replication cycle so local pruning happens after remote copies are confirmed current.

**Configure via UI:** Data Protection -> Replication -> Retention tab -> Add Policy

| Field | Description |
|-------|-------------|
| Dataset | Dataset to prune (e.g. `tank/data`) |
| Prefix | Snapshot name prefix to match (e.g. `auto-`) |
| Keep Hourly | Keep N most recent hourly snapshots (0 = unlimited) |
| Keep Daily | Keep N most recent daily snapshots |
| Keep Weekly | Keep N most recent weekly snapshots |
| Keep Monthly | Keep N most recent monthly snapshots |
| Keep Yearly | Keep N most recent yearly snapshots |

Snapshots are sorted by creation time and assigned to time buckets. The N newest in each configured bucket are retained; the rest are destroyed. A snapshot matching multiple buckets is retained if any bucket's limit has not been reached.

**Via API:**
```
GET  /api/replication/retention          # list policies
POST /api/replication/retention          # create/update/delete
```

---

## Restore from Remote (Disaster Recovery)

If the local dataset is lost or corrupted, restore from a remote copy created by scheduled replication.

**Prerequisites:**
- The remote peer must be authorized (SSH key installed)
- The remote dataset must exist and contain valid snapshots
- Specify the full remote snapshot path (e.g. `backuppool/data@snap-2025-01-15`)

**Via UI:** Data Protection -> Replication -> Restore tab

1. Select the source peer
2. Enter the remote snapshot path exactly as it appears on the remote (e.g. `backuppool/data@snap-2025-01-15`)
3. Enter the local destination dataset (must not exist, or use Force)
4. Check Force only if you want to overwrite an existing local dataset (destroys conflicting local snapshots)
5. Click Start Restore

The restore runs asynchronously with real-time progress and ETA. After receive completes, the daemon verifies the snapshot exists on the local dataset. A resume token warning is shown if a prior interrupted receive is detected.

**Via API:**
```
POST /api/replication/restore
{
  "remote_id": "<peer-id>",
  "remote_snapshot": "backuppool/data@snap-2025-01-15",
  "local_dataset": "tank/recovered-data",
  "force": false
}
```

---

---

## Cloud Sync

Copies a folder to cloud storage, or back: S3 and compatible services, Backblaze B2, Azure Blob, SFTP, FTP, WebDAV. The transfers are made by rclone.

**1. A remote** (Data Protection › Cloud Sync › Remotes): the provider and its credentials. *Test* checks that they work.

**2. A task** (tab Tasks):

| Field | |
|---|---|
| Direction | Upload (this system to the remote) or Download |
| Folder on this system | a folder in a pool |
| Bucket / folder in the remote | where in the remote. Always required: a task never works on a remote as a whole |
| Copy or Mirror | **Copy** (the default) adds and updates files at the destination and never deletes there. **Mirror** makes the destination identical to the source: what is only at the destination is deleted |
| Runs | by hand, every hour, daily or weekly at an hour you choose |

*Dry run* shows what a run would transfer and delete, without doing it: use it before the first run of a mirror. A run that was missed while the system was off is made up for once after the start. A task that is still running is not started a second time. A failed run keeps its error message on the task and raises the alert `backup.failed`.

Cloud sync copies files, not ZFS snapshots: for a consistent copy of data that changes while it is copied, replicate to a second ZFS system instead, or sync from a snapshot folder (`.zfs/snapshot/<name>/...`).

## A remote as a folder (Cold Tier)

**Data Protection › Cloud Sync › Cold Tier** mounts a folder of a remote under `/mnt/cold/<name>`: files there are read from and written to the remote as they are used. For data that may live elsewhere and is rarely needed. It is not a backup, and nothing is moved there by itself. Mounts are restored when the system starts.

---

## Rsync

For targets that are neither ZFS nor cloud storage: another NAS, a server reached over SSH, a USB disk.

**Data Protection › Backup**: source, destination (a local folder, or `user@host:/path` over SSH), and rsync options; *Run now*, or a schedule (hourly, daily, weekly, monthly). Runs take as long as they need. A failed scheduled run raises the alert `backup.failed`, and its job shows what rsync said.

A destination over SSH is reached with this system's own SSH key (shown under Replication › Peers): add it to `authorized_keys` of the user at the destination. The destination's host key is accepted at the first connection and must stay the same afterwards.

---

## Which one

| You have | Use |
|---|---|
| A second DPlaneOS or other ZFS system | Replication: block-level, incremental, keeps snapshots, verifiable |
| Cloud storage | Cloud Sync, Copy mode, daily |
| Any other machine with SSH | Rsync |
| Two DPlaneOS nodes that should take over from each other | a replicated storage group ([High Availability](HIGH-AVAILABILITY.md)) |

Whichever you use: set up an alert channel (System › Alerts), and now and then get a file back from the copy.

## When something is lost

- A file or folder: Previous Versions.
- A dataset: Replication › Restore from the copy on the other system, or copy back from cloud storage with a Download task.
- A disk: replace it on the pool's page; the pool rebuilds itself.
- The system disk, or the whole machine: [Recovery, section 7](RECOVERY.md#7-the-system-disk-is-gone).
