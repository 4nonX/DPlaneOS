# D-PlaneOS — Showstopper Mitigation Guide

**Updated:** 2026-03-09
**Purpose:** Honest assessment of what works, what has documented limits, and what is genuinely out of scope.

---

## Status Key

- **RESOLVED** — was a showstopper in an earlier version, fully addressed
- **PARTIAL** — real implementation exists with documented limitations
- **OPEN** — genuine limitation in the current version

---

## RESOLVED — Replication Was Simulated

### What the Old Guide Said

> `app/api/replication.php` line 121: `// Send snapshot (simulated)` — the GUI returns "success" but does nothing.

### Current State

`app/api/replication.php` does not exist. The PHP layer was replaced by the Go daemon.

The implementation lives in `daemon/internal/handlers/replication_remote.go` and performs:

- Full `zfs send | ssh zfs recv` pipe
- Incremental sends (`-i base_snapshot`)
- Compressed streams (`-c` flag)
- Resume tokens for interrupted transfers
- Rate limiting via `pv` (e.g. `rate_limit: "50M"` caps at 50 MB/s)
- Input validation on all shell-bound fields (snapshot name, remote host, user, port, SSH key path)

SSH connectivity can be verified before a transfer via `POST /api/replication/test`.

### What Still Requires Manual Setup

SSH key distribution is not handled by the GUI. Set up key-based authentication before using GUI replication:

```bash
# On the source NAS
sudo ssh-keygen -t ed25519 -f /root/.ssh/replication_key -N ""
sudo ssh-copy-id -i /root/.ssh/replication_key.pub root@target-nas
```

Then point the GUI's SSH key path field at `/root/.ssh/replication_key`.

---

## RESOLVED — PostgreSQL Dependency

### What the Old Guide Said

> Requires PostgreSQL (+100–200 MB RAM). Raspberry Pi users and low-RAM systems are affected.

### Current State

There is no PostgreSQL dependency. The database backend is SQLite with FTS5, compiled directly into the daemon binary (`-tags sqlite_fts5`). `install.sh` initialises the schema at `/var/lib/dplaneos/dplaneos.db`. No external database process runs.

Resource profile:

- Daemon idle RAM: ~50–80 MB
- No separate database service
- Compatible with Raspberry Pi 4 (4 GB) and similar hardware

---

## RESOLVED — No Upgrade Rollback

### What the Old Guide Said

> If a v4 upgrade fails the system can be "bricked". No rollback mechanism exists.

### Current State

`install.sh --upgrade` creates a timestamped backup before making any changes:

1. SQLite database, nginx config, and systemd unit backed up to `/var/lib/dplaneos/backups/pre-upgrade-YYYY-MM-DD-HH-MM/`
2. Installer verifies each phase, halting on first failure
3. On failure, the trap handler automatically restores the backup and restarts services
4. A `rollback.sh` is written to the backup directory for manual recovery

**To roll back manually:**
```bash
ls /var/lib/dplaneos/backups/
sudo bash /var/lib/dplaneos/backups/pre-upgrade-<timestamp>/rollback.sh
```

---

## PARTIAL — High Availability

### What the Old Guide Said

> No clustering, no failover, no redundancy. HA requires fundamental redesign.

### Current State

A real active/standby coordination layer exists in `daemon/internal/ha/cluster.go`.

**What it does:**

- Peer registration: `POST /api/ha/peers`
- Heartbeat loop: pings all registered peers via `GET /health` every 15 seconds
- State tracking: `healthy → degraded → unreachable` after 2 consecutive missed beats, persisted in SQLite
- Quorum calculation: reports whether a majority of registered nodes are reachable
- Role management: `active` / `standby` per node; `POST /api/ha/peers/{id}/role` promotes or demotes

**What it does not do:**

- **No automatic failover.** If the active node goes unreachable, the standby detects it and reports it — promotion is a deliberate manual step.
- **No shared storage.** Each node manages its own ZFS pools. Cross-node replication is via `zfs send`.
- **No split-brain fencing.** There is no STONITH. In a network partition, both nodes may believe they are active. Do not automate pool imports on standby without infrastructure-level fencing (e.g. IPMI power-off).
- **No VIP management.** DNS or load-balancer cutover is outside the system.

### Active/Standby Setup

**Step 1: Register the standby node on the active NAS:**
```bash
curl -s -X POST http://active-nas:9000/api/ha/peers \
  -H "Content-Type: application/json" \
  -d '{"id":"nas-b","name":"NAS-B Standby","address":"http://192.168.1.101:9000","role":"standby"}'
```

**Step 2: Confirm both nodes see each other:**
```bash
curl -s http://active-nas:9000/api/ha/status | python3 -m json.tool
```

**Step 3: Set up replication (daily incremental at 02:00):**
```bash
0 2 * * * curl -s -X POST http://active-nas:9000/api/replication/remote \
  -H "Content-Type: application/json" \
  -d '{"snapshot":"tank/data@daily","remote_host":"nas-b","remote_user":"root","remote_pool":"backup/data","incremental":true,"ssh_key_path":"/root/.ssh/replication_key","compressed":true}'
```

**Step 4: Manual failover procedure:**
```bash
# On standby node
sudo zpool import -f tank
sudo systemctl start dplaned nginx
# Update DNS or client config to point at standby
```

**RTO (manual): 5–10 minutes.** RPO: time since last successful replication.

### What HA Is Not Suitable For

| Requirement | Status | Alternative |
|---|---|---|
| Automatic failover < 60 s | Not supported | TrueNAS SCALE Enterprise / Proxmox HA |
| Split-brain safe auto-promotion | Not supported | Pacemaker + STONITH |
| Active/active shared storage | Not supported | Ceph, GlusterFS |
| 99.99% SLA | Not supported | Commercial SAN |

---

## OPEN — Binary-Trust Barrier

### The Issue

The Go daemon (`dplaned`) ships as a compiled binary. Users who require source-level auditability before trusting a privileged process must build it themselves.

**Who is affected:** security auditors, organisations with supply-chain review requirements.

**Who is not affected:** home users, homelabs, small offices.

### Mitigation: Build from Source

The full daemon source is included in the release tarball under `daemon/`.

```bash
sudo apt install golang-go gcc  # Go 1.22+ and gcc required

cd /path/to/dplaneos/daemon
CGO_ENABLED=1 go build -mod=vendor -tags sqlite_fts5 \
  -ldflags "-s -w -X main.Version=$(cat ../VERSION)" \
  -o dplaned-local ./cmd/dplaned/

# Compare with shipped binary
sha256sum dplaned-local /opt/dplaneos/daemon/dplaned

# Deploy your build
sudo systemctl stop dplaned
sudo install -m 755 dplaned-local /opt/dplaneos/daemon/dplaned
sudo systemctl start dplaned
```

If the SHA256 values match, the shipped binary is identical to what the source produces.

### Roadmap

Reproducible build verification (publishing expected hashes in the release alongside CI attestation) is planned. No specific date.

---

## Decision Matrix

| Use Case | Status | Notes |
|---|---|---|
| Home NAS | Ready | No caveats |
| Homelab / learning | Ready | Ideal use case |
| Small office (< 20 users) | Ready | Single-node; SQLite handles this load |
| Offsite backup / replication | Ready | GUI works; SSH keys needed upfront |
| Monitored active/standby | Usable | Manual failover only — plan your RTO |
| Security audit required | Usable | Build from source |
| Auto-failover / 99.99% SLA | Not this | Use TrueNAS Scale or Proxmox HA |
| Active/active shared storage | Not this | Requires Ceph or GlusterFS |

---

## Roadmap

| Item | Status |
|---|---|
| Replication (real implementation) | Done |
| SQLite-only (no PostgreSQL) | Done |
| Upgrade rollback | Done |
| Active/standby coordination layer | Done (manual failover) |
| Reproducible build verification | Planned |
| Automated failover with fencing | Not planned — outside single-node NAS scope |
