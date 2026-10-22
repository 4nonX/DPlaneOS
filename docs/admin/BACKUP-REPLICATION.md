# DPlaneOS Backup and Replication

DPlaneOS provides several complementary backup strategies. Understanding which to use requires knowing what data lives where and what each strategy protects.

For the persistence model (what survives reboots vs. what is ephemeral), see [ARCHITECTURE.md](../reference/ARCHITECTURE.md#the-persistence-model).

---

## What Needs Backing Up

| Data | Location | Backup strategy |
|------|----------|-----------------|
| User files | ZFS pools on data disks | ZFS snapshots + ZFS Send/Receive |
| NAS configuration (runtime) | `state.yaml` in Git | Git push to remote |
| NAS configuration (OS-level) | `configuration.nix` / `flake.nix` | Git push to remote |
| NAS configuration (database and settings files) | `/var/lib/dplaneos`, `/etc/dplaneos`, `/etc/target` | Configuration backup (Settings → Maintenance); a daily copy is written to the first pool |
| Docker container state | `/var/lib/docker/` | ZFS snapshot (if on a ZFS dataset) or container volume backup |
| TLS certificates | `/etc/dplaneos/ssl/` (persisted) | Included in ZFS snapshot of `/persist` |

**What you do NOT need to back up:** The NixOS system closure itself. If the boot disk fails, you reinstall from the ISO and import your ZFS pools. The ISO always contains the correct system closure for the pinned nixpkgs revision.

---

## ZFS Snapshots

Snapshots are the primary backup mechanism for user data. They are instantaneous, space-efficient (copy-on-write), and can be replicated to a remote system.

### Manual Snapshots

```bash
# Take a snapshot
zfs snapshot tank/data@backup-$(date +%Y%m%d)

# List snapshots
zfs list -t snapshot tank/data

# Roll back to a snapshot (destroys later data)
zfs rollback tank/data@backup-20260509

# Restore a single file from a snapshot (snapshots appear in .zfs/snapshot/)
ls /mnt/data/.zfs/snapshot/backup-20260509/myfile.txt
cp /mnt/data/.zfs/snapshot/backup-20260509/myfile.txt /mnt/data/myfile.txt
```

### Scheduled Snapshots via the UI

Data Protection → Snapshot Scheduler. Set a per-dataset schedule (every 15 minutes, hourly, daily, weekly or monthly), whether to include child datasets (one atomic `zfs snapshot -r` of the whole tree), and retention: a count, and optionally a maximum age in days. After each snapshot the schedule prunes its own older snapshots beyond the count or older than the age; the newest one and replication bases always stay.

Each frequency gets its own prefix by default (`auto-frequent-`, `auto-hourly-`, `auto-daily-`, ...), and two schedules of one dataset may not share a prefix: otherwise one schedule's pruning would remove the other's snapshots. Snapshots with the default prefixes appear in Windows Explorer as Previous Versions on shares with that option enabled. "Run now" takes a snapshot with the schedule's prefix.

A schedule can also start a replication schedule after each snapshot ("Trigger Replication after each snapshot").

### Automatic Snapshots via NixOS (optional, not set up by DPlaneOS)

NixOS's own `services.zfs.autoSnapshot` (zfs-auto-snapshot) also works, but DPlaneOS neither enables it nor sets the `com.sun:auto-snapshot` property it relies on. Its snapshots (`zfs-auto-snap_<freq>-<timestamp>`) are not pruned by DPlaneOS retention unless a policy names that prefix, and do not appear as Previous Versions. The UI scheduler above is the supported way.

These schedules are stored in `/etc/dplaneos/snapshot-schedules.json` and survive reboots (the `/etc/dplaneos/` directory is bind-mounted from `/persist`). SMART task schedules are separate and can be declared in `state.yaml` under `smart_tasks`.

---

## ZFS Send/Receive (Replication)

ZFS Send/Receive transmits a stream of ZFS data from one pool to another, either locally or over SSH. It is the most efficient way to replicate large datasets because it is block-level and incremental.

### Local Clone

```bash
# Initial full send
zfs send tank/data@backup-20260509 | zfs recv backup/data

# Incremental send
zfs send -i tank/data@backup-20260501 tank/data@backup-20260509 | zfs recv backup/data
```

### Remote Replication via SSH

```bash
# Initial send to remote
zfs send -R tank/data@backup-20260509 | \
  ssh backup@backup-server.example.com "zfs recv -Fu backup/data"

# Incremental
zfs send -i tank/data@backup-20260501 tank/data@backup-20260509 | \
  ssh -C backup@backup-server.example.com "zfs recv -F backup/data"
```

**`-R` flag:** Sends the dataset and all child datasets recursively. Use for first full replication.

**`-C` flag on SSH:** Enables SSH compression. Useful on slow links but adds CPU overhead on fast links.

### Scheduled Remote Replication via UI

Replication is configured through two screens under **Data Protection → Replication** in the nav:

**Step 1 - Add a Peer** (Replication: Peers: Add Peer)

Enter a name, host, SSH user (default: root), and port. Then click **Authorize** and enter the host's SSH password once. The daemon installs its ed25519 replication key via the Go SSH client. The password exists only in the request buffer and is never stored. After authorization, click **Test** to confirm key-based access and ZFS readiness.

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
| Resume | Check for an interrupted transfer token before sending |
| Recursive | Include child datasets in the send stream (`-R`). Checked by default. Uncheck to send only the exact dataset. |
| Compress | Enable lz4 stream compression (encrypted datasets are always sent raw instead) |
| Rate limit MB/s | Bandwidth cap via `pv` (0 = unlimited; requires `pv` installed on the source) |
| Replicate from scratch | Off by default. When the peer's copy shares no snapshot with the source, destroy it and send everything again. Off, such a run fails with an explanation and leaves the peer's copy alone. |

**What a run sends.** Each run compares the snapshots of the source with those of the copy on the peer, by GUID (a snapshot of the same name from another dataset never counts), and sends everything from the newest snapshot both sides share up to the newest local one, intermediate snapshots included (`zfs send -I`). A peer that does not have the dataset yet gets a full copy.

**How the copy is kept.** The peer receives with `zfs recv -s -u -x mountpoint -o readonly=on`: resumable, never mounted over a path of the backup host (it inherits a mountpoint below its parent there), and read-only, so nothing changes it between runs. There is no `-F`: the copy keeps the snapshots the source has since pruned, so the backup can hold a longer history than the source. Encrypted datasets are sent raw (`-w`): the peer stores the ciphertext and never needs the key. A restore (Replication › Restore) receives with `-x readonly -x mountpoint`, so the restored dataset is writable and mounts where it lands.

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

**Reliability features:** Transient failures trigger automatic retry with exponential backoff (up to 3 attempts: immediate, +30 seconds, +120 seconds); a retry continues an interrupted stream from its resume token. Two schedules targeting the same source dataset and remote destination cannot run concurrently - the second is skipped with a "conflict" status to prevent ZFS stream corruption. Without a common snapshot a run stops with an explanation instead of falling back to a full send (see "Replicate from scratch"). A failing `zfs send` is reported with its own error message. After every `zfs recv`, the daemon verifies the expected snapshot exists on the destination and fails the job explicitly if absent. A run the daemon was in the middle of when it stopped is marked "interrupted" at the next start, and the schedule runs again normally.

---

## Snapshot Retention Policies

DPlaneOS prunes old local snapshots by time bucket. Only snapshots matching the configured prefix are pruned (the prefix is required), so manually created snapshots are never touched; nor is the snapshot a replication schedule uses as its incremental base. Policies are applied every 5 minutes, after the replication monitor. The copies on replication peers keep their snapshots (see above).

**Configure via UI:** Data Protection -> Replication -> Retention tab -> Add Policy

| Field | Description |
|-------|-------------|
| Dataset | Dataset to prune (e.g. `tank/data`); its child datasets are covered too, each on its own |
| Prefix | Snapshot name prefix to match (e.g. `auto-` or `auto-hourly-`); required |
| Keep Hourly | Keep the newest snapshot of each of the last N hours that have one (0 = off) |
| Keep Daily | Keep the newest snapshot of each of the last N days |
| Keep Weekly | Keep the newest snapshot of each of the last N ISO weeks |
| Keep Monthly | Keep the newest snapshot of each of the last N months |
| Keep Yearly | Keep the newest snapshot of each of the last N years |

The tiers add up, as in sanoid and TrueNAS: with Keep Hourly 24 and Keep Daily 7, the newest snapshot of each of the last 24 hours stays, and so does the newest snapshot of each of the last 7 days. A snapshot kept by any tier stays. With every count at 0 the policy keeps everything.

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

## Cloud Sync (rclone)

Cloud Sync uses rclone to synchronize datasets to object storage providers: S3-compatible stores (AWS, Backblaze B2, Wasabi, MinIO), Azure Blob, SFTP, FTP, and WebDAV.

### Supported Providers

| Provider | Backend identifier |
|----------|--------------------|
| Amazon S3 | `s3` |
| Backblaze B2 | `b2` |
| Wasabi | `s3` (endpoint override) |
| MinIO (local) | `s3` (endpoint override) |
| Azure Blob Storage | `azureblob` |
| SFTP | `sftp` |
| FTP / FTPS | `ftp` |
| WebDAV / Nextcloud | `webdav` |
| Google Cloud Storage | `gcs` |

### Setup via UI

Data Protection → Cloud Sync → Add Provider.

1. Select provider type
2. Enter credentials (access key, secret key, region, endpoint for S3-compatible)
3. Enter the destination bucket/container/path
4. Set schedule and options:
   - **Direction:** Upload, Download, or Sync (bidirectional)
   - **Delete:** whether to delete files at the destination that no longer exist at the source
   - **Bandwidth limit**

### Setup via rclone config directly

The rclone config is stored at `/etc/dplaneos/rclone.conf` (persisted). Direct edits are supported:

```bash
# Test connectivity
rclone ls backblaze:mybucket

# Manual sync
rclone sync /mnt/tank/data backblaze:mybucket/nas-backup --progress
```

### Schedule expression

Cloud Sync tasks use standard 5-field cron expressions:
```
* * * * *
│ │ │ │ └── day of week (0-7, Sunday = 0 or 7)
│ │ │ └──── month (1-12)
│ │ └────── day of month (1-31)
│ └──────── hour (0-23)
└────────── minute (0-59)
```

---

## Cold Tier (FUSE Mounts)

Cold Tier exposes remote object storage as a local FUSE mount under `/mnt/cold/`. Files appear as regular filesystem paths; reads and writes are transparently forwarded to the remote store via rclone.

Cold Tier is intended for archival data that is accessed infrequently. It is not a substitute for ZFS replication for primary data.

### Enable via UI

Settings: Cold Tier: Add Remote. Select provider, enter credentials, choose a local mount name.

The mount appears at `/mnt/cold/<name>`. Docker containers can use this path as a volume mount.

### Enable via NixOS module

```nix
services.dplaneos.coldTier = {
  enable = true;
  mountPath = "/mnt/cold";
};
```

The daemon manages rclone FUSE mount lifecycle (start, stop, health monitoring). On boot, all configured cold tier mounts are restored automatically.

---

## Rsync

For irregular or one-off backups where ZFS Send/Receive is not available (e.g., backing up to a non-ZFS remote), use rsync.

```bash
# Backup to remote
rsync -avz --delete /mnt/tank/data/ backup@backup-server.example.com:/backup/data/

# Backup with SSH and compression
rsync -avz --compress-level=6 -e "ssh -p 22" /mnt/tank/data/ backup@server:/backup/

# Exclude specific paths
rsync -avz --exclude='.zfs/' --exclude='*.tmp' /mnt/tank/data/ backup@server:/backup/
```

**Rsync vs. ZFS Send/Receive:** Rsync is file-level and must stat every file to detect changes. On large datasets, ZFS Send/Receive is significantly faster for incremental replication because it works at the block level. Use Rsync for interoperability with non-ZFS systems or when the remote does not support `zfs receive`.

---

## Configuration Backup

One file holds the whole configuration: the database (users, groups, shares, alerts, audit log, everything stored there) and the settings kept in files: snapshot, replication and rsync schedules, firewall rules, certificates and the ACME account, Docker stack files, iSCSI and NVMe-oF exports, NixOS settings, replication SSH keys, the audit log key, the secrets key (optional) and the network configuration. Data on the pools is not part of it.

| Where | How |
|---|---|
| Settings → Maintenance → Download Backup | `.tar.gz`; "Include the secrets key" lets another machine open the stored passwords (keep that file as safe as the passwords) |
| Daily, automatic | `<first pool>/.dplaneos-config/dplaneos-config-<host>-<date>.tar.gz`, newest 14, mode 0600 (with the secrets key): survives the loss of the boot disk |
| API | `GET /api/system/config/backup?secrets=1`, `GET /api/system/config/backups` |

### Restore

Settings → Maintenance → Configuration Restore (upload a file or pick a stored backup), or on the console:

```bash
sudo dplaned -config-dir /etc/dplaneos -recover-restore <backup.tar.gz>   # add -recover-network on the same hardware
sudo systemctl restart dplaned
```

What happens:

1. The archive is checked: format, checksum of the database dump, `pg_restore --list`, only known paths, not from a newer DPlaneOS than this one. Nothing is changed if a check fails.
2. The daemon restarts. Before it uses the database it writes the current configuration to `/var/lib/dplaneos/before-restore.tar.gz` (downloadable and restorable: the undo), then replaces the database in one transaction (a failure leaves it unchanged) and writes the files.
3. Migrations bring a backup from an older DPlaneOS up to date; startup recreates accounts, `smb.conf`, NFS exports, firewall, iSCSI and timers from the restored state. Everyone logs in again (sessions are cleared).
4. An alert reports the result. NixOS settings from the backup show as pending under Settings → NixOS until applied. The network settings are only restored when asked for (interface names differ on other hardware). A node in a cluster must leave it before a restore; a backup taken on a cluster member restores without the membership.

### Point-in-time recovery of the database (WAL)

For point-in-time recovery, configure WAL archiving via the NixOS module. Do not edit `postgresql.conf` directly - NixOS regenerates it on every `nixos-rebuild switch`.

**Standalone (no HA):** add to `/etc/nixos/local.nix`:
```nix
services.postgresql.settings = {
  archive_mode    = "on";
  archive_command = "cp %p /mnt/backup/wal/%f";
  wal_level       = "replica";
};
```
Then rebuild (`sudo nixos-rebuild switch --flake /etc/nixos#dplaneos --impure`).

**Clusters:** every node has its own database; set up archiving on each node you want to recover.

---

## NixOS Configuration Backup

An installed system is built from `/etc/nixos/flake.nix` (the DPlaneOS release) plus `hardware-configuration.nix` and an optional `local.nix`; settings made in the web UI are part of the configuration backup. Keep `local.nix` in Git if you have one (Settings → NixOS → Back up config to Git pushes `/etc/nixos`).

---

## Recovery Procedure

### Boot disk failure (no data loss)

1. Boot from DPlaneOS ISO and install to replacement boot disk
2. After first boot, import data pools: `zpool import -a`
3. Settings → Maintenance → Stored backups: restore the newest backup from the pool (or upload a downloaded one)
4. If you keep the configuration in Git (GitOps): System › Git › Configuration repository, then **Apply** the changes the first check lists

ZFS pools on the data disks are completely independent of the boot disk. A boot disk failure does not affect pool data as long as the pool was cleanly exported (or the system crashed cleanly - ZFS's transaction group model tolerates sudden power loss).

### Pool data loss (disk failure)

ZFS RAID-Z provides redundancy within a pool. With RAID-Z1, one disk failure is tolerated:

```bash
# Check pool status
zpool status tank

# Replace a failed disk (hot-swap)
zpool replace tank /dev/disk/by-id/old-disk /dev/disk/by-id/new-disk

# Monitor resilver progress
zpool status -v tank
```

For catastrophic failure (multiple simultaneous disk failures exceeding RAID level), restore from the most recent ZFS send/receive or cloud sync backup.

### Snapshot rollback

```bash
# List available snapshots
zfs list -t snapshot tank/data

# Roll back (destroys any data written after the snapshot)
zfs rollback -r tank/data@backup-20260509
```

### Point-in-time recovery from .zfs/snapshot

```bash
# Access snapshot files without rolling back
ls /mnt/data/.zfs/snapshot/
ls /mnt/data/.zfs/snapshot/backup-20260509/path/to/file

# Restore a single file
cp /mnt/data/.zfs/snapshot/backup-20260509/important.doc /mnt/data/important.doc
```

The `.zfs/snapshot` directory is read-only and hidden from `ls` by default but always accessible by its full path.
