# D-PlaneOS v3.2.1 — Showstopper Mitigation Guide

**Updated:** 2026-02-22  
**Replaces:** 2026-02-07 edition (stale — referenced PHP layer that no longer exists)  
**Purpose:** Honest assessment of what works, what doesn't, and what to do about it

---

## How to Read This Guide

Each item is classified as one of:

- ✅ **RESOLVED** — was a showstopper in an earlier version, no longer is
- ⚠️ **PARTIAL** — real implementation exists but with documented limits
- 🔴 **OPEN** — still a genuine limitation in v3.2.1

---

## ✅ RESOLVED — Showstopper #2: Replication Was Simulated

### What the Old Guide Said

> `app/api/replication.php` line 121: `// Send snapshot (simulated)` — the GUI returns "success" but does nothing.

### What Is Actually True in v3.2.1

`app/api/replication.php` **does not exist**. It was removed when the PHP layer was replaced by the Go daemon in v3.0.0.

The real implementation lives in `daemon/internal/handlers/replication_remote.go`. It performs:

- Full `zfs send | ssh zfs recv` pipe
- Incremental sends (`-i base_snapshot`)
- Compressed streams (`-c` flag)
- Resume tokens for interrupted transfers — the send command checks for an existing token on the remote before starting, avoiding full re-sends after network drops
- Rate limiting via `pv` (e.g. `rate_limit: "50M"` for 50 MB/s)
- Input validation on all shell-bound fields (snapshot name, remote host, remote user, port, SSH key path) — shell metacharacters are rejected before any command is constructed

SSH connectivity can be verified independently before attempting a transfer via `POST /api/replication/test`.

**Both endpoints (`/api/replication/remote` and `/api/replication/test`) are exercised in CI** and pass on every commit.

### What Still Requires Manual Setup

SSH key distribution is not handled by the GUI. Before using GUI replication, set up key-based auth manually:

```bash
# On the source NAS
sudo ssh-keygen -t ed25519 -f /root/.ssh/replication_key -N ""
sudo ssh-copy-id -i /root/.ssh/replication_key.pub root@target-nas
```

Then point the GUI's SSH key path field at `/root/.ssh/replication_key`.

---

## ✅ RESOLVED — Showstopper #3: PostgreSQL Resource Hunger

### What the Old Guide Said

> v3.2.1 requires PostgreSQL (+100–200 MB RAM). Raspberry Pi users and low-RAM systems are affected.

### What Is Actually True in v3.2.1

**There is no PostgreSQL**. The database backend is SQLite with FTS5, built directly into the daemon binary at compile time (`-tags sqlite_fts5`). `install.sh` installs `sqlite3` and initialises the schema at `/var/lib/dplaneos/dplaneos.db`. No external database process runs.

The resource profile is:

- Daemon idle RAM: ~50–80 MB (Go runtime + SQLite in-process)
- No separate database service
- Compatible with Raspberry Pi 4 (4 GB) and similar single-board hardware

The old guide's RAM figures (400 MB idle, 650 MB active) came from a different version lineage and do not apply here.

---

## ✅ RESOLVED — Showstopper #4: No Upgrade Rollback

### What the Old Guide Said

> If a v4 upgrade fails the system can be "bricked". No rollback mechanism exists.

### What Is Actually True in v3.2.1

`scripts/upgrade-with-rollback.sh` ships in the release tarball. It:

1. Creates a timestamped backup at `/var/lib/dplaneos-backup/pre-upgrade-YYYY-MM-DD-HH-MM/` before touching anything — includes the SQLite database, nginx config, and system state
2. Runs the installer with verification at each step, halting on the first error
3. On failure, automatically restores the backup and restarts services
4. Leaves a `rollback.sh` in the backup directory for manual recovery if needed

**Usage — always use this instead of running `install.sh` directly when upgrading:**

```bash
sudo ./scripts/upgrade-with-rollback.sh
```

**Pre-upgrade checklist:**

```bash
# Database accessible
sqlite3 /var/lib/dplaneos/dplaneos.db ".tables"

# Web UI responding
curl -s http://localhost/health | grep ok

# Pools healthy
zpool status

# At least 500 MB free on the install partition
df -h /var/www/dplaneos
```

---

## ⚠️ PARTIAL — Showstopper #5: High Availability

### What the Old Guide Said

> No clustering, no failover, no redundancy. HA requires fundamental redesign.

### What Is Actually True in v3.2.1

A real active/standby coordination layer exists in `daemon/internal/ha/cluster.go`. It is not marketing — it runs in the daemon process and is tested in CI.

**What it does:**

- Peer registration: `POST /api/ha/peers` registers a standby node by address
- Heartbeat loop: the daemon pings all registered peers via `GET /health` every 15 seconds
- State tracking: peers move through `healthy → degraded → unreachable` after 2 consecutive missed beats, persisted in SQLite so the state survives restarts
- Quorum calculation: reports whether a majority of registered nodes are reachable
- Role management: `active` / `standby` roles are assignable per node; `POST /api/ha/peers/{id}/role` promotes or demotes a peer
- Inbound heartbeats: standby nodes can push heartbeats to the active node via `POST /api/ha/heartbeat`

**What it does not do:**

- **No automatic failover.** If the active node goes unreachable, the standby detects this and reports it, but does not automatically import ZFS pools or redirect traffic. Promotion is a deliberate manual step.
- **No shared storage / DRBD / Ceph.** Each node manages its own ZFS pools. Replication between them is via the `zfs send` pipeline described above.
- **No split-brain fencing.** There is no STONITH or equivalent. In a network partition, both nodes may believe they are active. Do not automate pool imports on standby without external fencing.
- **No VIP management.** DNS or load-balancer cutover is outside the system.

### Practical Active/Standby Setup

This setup gives you monitored standby with manual failover in under 5 minutes.

**Step 1: Register the standby node on the active NAS**

```bash
curl -s -X POST http://active-nas:9000/api/ha/peers \
  -H "Content-Type: application/json" \
  -d '{
    "id": "nas-b",
    "name": "NAS-B Standby",
    "address": "http://192.168.1.101:9000",
    "role": "standby"
  }'
```

**Step 2: Confirm both nodes see each other**

```bash
# On active node
curl -s http://active-nas:9000/api/ha/status | python3 -m json.tool
```

**Step 3: Set up replication (see Showstopper #2 above)**

```bash
# Daily incremental replication at 02:00
0 2 * * * curl -s -X POST http://active-nas:9000/api/replication/remote \
  -H "Content-Type: application/json" \
  -d '{"snapshot":"tank/data@daily","remote_host":"nas-b","remote_user":"root","remote_pool":"backup/data","incremental":true,"ssh_key_path":"/root/.ssh/replication_key","compressed":true}'
```

**Step 4: Manual failover procedure (when active fails)**

```bash
# On standby node — import the pool (use -f if active is unresponsive)
sudo zpool import -f tank

# Start services
sudo systemctl start dplaned nginx

# Promote this node to active role (optional — updates GUI display)
curl -s -X POST http://nas-b:9000/api/ha/peers/nas-a/role \
  -d '{"role":"standby"}'

# Update DNS or client config to point at nas-b
```

**RTO (manual): 5–10 minutes**  
**RPO: time since last successful replication run**

### What HA Is NOT Suitable for in v3.2.1

| Requirement | v3.2.1 | Alternative |
|---|---|---|
| Automatic failover < 60 s | ❌ | TrueNAS SCALE Enterprise / Proxmox HA |
| Split-brain safe auto-promotion | ❌ | Pacemaker + STONITH |
| Shared active/active storage | ❌ | Ceph, GlusterFS |
| 99.99% SLA | ❌ | Commercial SAN |

---

## 🔴 OPEN — Showstopper #1: Binary-Trust Barrier

### The Issue

The Go daemon (`dplaned`) ships as a compiled binary. Users who require source-level auditability before trusting a privileged process cannot verify the binary matches the source without building it themselves.

**Who is affected:** security auditors, organisations with supply-chain review requirements, open-source purists.

**Who is not affected:** home users, homelabs, small offices — the daemon source is published and the build is reproducible.

### Mitigation: Build from Source

The full daemon source is included in the release tarball under `daemon/`. The CI pipeline (GitHub Actions, public log) builds from that same source on every commit. To verify or replace the shipped binary:

```bash
# Install Go 1.21+
sudo apt install golang-go   # or use https://go.dev/dl/

cd /path/to/dplaneos-v3.2.1/daemon

# Build
CGO_ENABLED=1 go build -tags sqlite_fts5 -ldflags "-X main.Version=3.2.1" -o dplaned-local ./cmd/dplaned

# Compare
sha256sum dplaned-local release/dplaned-linux-amd64

# Deploy your build
sudo install -m 755 dplaned-local /usr/local/bin/dplaned
sudo systemctl restart dplaned
```

If the SHA256 values match, the shipped binary is identical to what the source produces. If they differ, use your own build.

### What Is Planned

No specific roadmap date. Reproducible build verification (publishing expected hashes in the release alongside CI attestation) is the next step, not eBPF monitoring. The eBPF reference in the previous guide was aspirational and has been removed.

---

## Decision Matrix

| Use Case | Verdict | Notes |
|---|---|---|
| Home NAS | ✅ Ready | No caveats |
| Homelab / learning | ✅ Ready | Ideal |
| Small office (< 20 users) | ✅ Ready | Single node, SQLite handles it |
| Offsite backup / replication | ✅ Ready | GUI works; SSH keys needed upfront |
| Monitored active/standby | ⚠️ Usable | Manual failover only; plan your RTO |
| Security audit required | ⚠️ Usable | Build from source |
| Auto-failover / 99.99% SLA | ❌ Not this | Use TrueNAS Scale or Proxmox HA |
| Active/active shared storage | ❌ Not this | Requires Ceph or GlusterFS |

---

## Roadmap

| Item | Status | Target |
|---|---|---|
| Replication GUI (real implementation) | ✅ Done as of v3.0.0 | — |
| SQLite-only (no PostgreSQL dependency) | ✅ Done | — |
| Upgrade rollback script | ✅ Done as of v3.2.1 | — |
| Active/standby coordination layer | ✅ Done (manual failover) | — |
| Reproducible build verification / hash attestation | Planned | TBD |
| Automated failover with fencing | Not planned | Scope exceeds single-node NAS design |

---

*Honest assessment: know the limits, work within them.*
