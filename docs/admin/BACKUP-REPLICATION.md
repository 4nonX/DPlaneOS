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
| Database (users, roles, audit) | `/var/lib/dplaneos/pgsql/` | `pg_dump` or ZFS snapshot of the dataset |
| Docker container state | `/var/lib/docker/` | ZFS snapshot (if on a ZFS dataset) or container volume backup |
| TLS certificates | `/etc/dplaneos/` (persisted) | Included in ZFS snapshot of `/persist` or `pg_dump` |

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

### Automatic Snapshots via NixOS

```nix
# In configuration.nix or nixos/module.nix
services.zfs.autoSnapshot = {
  enable = true;
  frequent = 4;   # every 15 minutes, keep 4
  hourly   = 24;  # keep 24 hourly
  daily    = 7;   # keep 7 daily
  weekly   = 4;   # keep 4 weekly
  monthly  = 12;  # keep 12 monthly
};
```

Snapshots created by `autoSnapshot` follow the naming convention `zfs-auto-snap_<freq>-<timestamp>` and are taken for all datasets with the `com.sun:auto-snapshot` property set to `true` (set by default on pool creation).

Opt a dataset out of automatic snapshots:
```bash
zfs set com.sun:auto-snapshot=false tank/scratch
```

### Scheduled Snapshots via the UI

Storage UI: Snapshots tab on any dataset. Set a cron schedule, retention count, and whether to include child datasets.

These schedules are stored in the database and survive reboots. They can also be declared in `state.yaml` under `smart_tasks` (for SMART) - snapshot schedules are managed via the UI/API separately.

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

Settings: Replication: Add Replication Task.

Required fields:
- Source dataset
- Remote host and port
- Remote user (needs `zfs allow` on the remote pool - see below)
- Remote pool path
- SSH key path (generate with `ssh-keygen -t ed25519 -f /var/lib/dplaneos/keys/replication_ed25519`)
- Schedule (cron expression)
- Rate limit (MB/s, 0 for unlimited)

On the remote host, grant the replication user ZFS permissions without full root access:
```bash
# On the remote server
sudo zfs allow replication-user compression,create,destroy,hold,mount,receive,rollback,send,snapshot backup
```

This is also declarable in `state.yaml` under `replication:`:
```yaml
replication:
  - name: offsite
    source_dataset: tank/data
    remote_host: backup.example.com
    remote_user: replication
    remote_port: 22
    remote_pool: backup
    ssh_key_path: /var/lib/dplaneos/keys/replication_ed25519
    interval: "0 2 * * *"
    trigger_on_snapshot: true
    compress: true
    rate_limit_mb: 100
    enabled: true
```

`trigger_on_snapshot: true` means replication runs immediately after each automatic snapshot, rather than only on the cron interval.

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

Settings: Cloud Sync: Add Provider.

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

## Database Backup

The DPlaneOS PostgreSQL database contains users, roles, audit logs, container metadata, and monitoring history. It is separate from ZFS pool data.

### One-time backup

```bash
sudo -u postgres pg_dump dplaneos > /mnt/backup/dplaneos-$(date +%Y%m%d).sql

# Compressed
sudo -u postgres pg_dump dplaneos | gzip > /mnt/backup/dplaneos-$(date +%Y%m%d).sql.gz
```

### Continuous archiving (WAL)

For point-in-time recovery, configure WAL archiving in PostgreSQL:
```bash
# In /var/lib/dplaneos/pgsql/postgresql.conf (append):
archive_mode = on
archive_command = 'cp %p /mnt/backup/wal/%f'
wal_level = replica
```

Then run `systemctl restart postgresql`.

### Restore

```bash
sudo systemctl stop dplaned
sudo -u postgres psql -c "DROP DATABASE dplaneos;"
sudo -u postgres psql -c "CREATE DATABASE dplaneos OWNER dplaneos;"
sudo -u postgres psql dplaneos < /mnt/backup/dplaneos-20260509.sql
sudo systemctl start dplaned
```

---

## NixOS Configuration Backup

The OS-level configuration (`configuration.nix`, `flake.nix`, `flake.lock`, all `nixos/*.nix` files) should be kept in a separate Git repository.

```bash
# In the DPlaneOS project directory
git push origin main   # or whichever remote holds your fork/customizations
```

For a custom installation (operator-managed flake), back up at minimum:
- `flake.nix` and `flake.lock`
- `nixos/configuration-standalone.nix` (or your custom configuration)
- Any site-local patches or overrides

If you use the stock DPlaneOS flake without customization, you only need to know the DPlaneOS version you were running - any future install from the same release tag produces an identical system.

---

## Recovery Procedure

### Boot disk failure (no data loss)

1. Boot from DPlaneOS ISO and install to replacement boot disk
2. After first boot, import data pools: `zpool import -a`
3. Restore `state.yaml` from Git: Settings: GitOps: configure repository
4. Apply: `POST /api/gitops/apply`
5. Restore the PostgreSQL database from backup if needed

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
