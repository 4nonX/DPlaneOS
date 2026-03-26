# D-PlaneOS - Showstopper Mitigation Guide

**Updated:** 2026-03-25
**Purpose:** Honest assessment of what works, what has documented limits, and what is genuinely out of scope.

---

## Status Key

- **RESOLVED** - was a showstopper in an earlier version, fully addressed
- **PARTIAL** - real implementation exists with documented limitations
- **OPEN** - genuine limitation in the current version

---

## RESOLVED - Replication Was Simulated

### What the Old Guide Said

> `app/api/replication.php` line 121: `// Send snapshot (simulated)` - the GUI returns "success" but does nothing.

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

## RESOLVED - Scalable Database Infrastructure

### What the Old Guide Said

> Requires PostgreSQL (+100–200 MB RAM). Raspberry Pi users and low-RAM systems are affected.

### Current State

The system uses PostgreSQL for all metadata and configuration. While this adds a small RAM footprint (~150 MB for PostgreSQL/Patroni), it provides the concurrency and reliability required for enterprise-grade HA. SQLite is no longer supported for production deployments.

Resource profile:

- Daemon idle RAM: ~80–120 MB
- Database (PostgreSQL/Patroni): ~150 MB
- Compatible with systems with 4 GB+ RAM.

---

## RESOLVED - No Upgrade Rollback

### What the Old Guide Said

> If a v4 upgrade fails the system can be "bricked". No rollback mechanism exists.

### Current State

`install.sh --upgrade` creates a timestamped backup before making any changes:

1. PostgreSQL database (logical dump), nginx config, and systemd unit backed up to `/var/lib/dplaneos/backups/pre-upgrade-YYYY-MM-DD-HH-MM/`
2. Installer verifies each phase, halting on first failure
3. On failure, the trap handler automatically restores the backup and restarts services
4. A `rollback.sh` is written to the backup directory for manual recovery

**To roll back manually:**
```bash
ls /var/lib/dplaneos/backups/
sudo bash /var/lib/dplaneos/backups/pre-upgrade-<timestamp>/rollback.sh
```

---

## RESOLVED - High Availability

### What the Old Guide Said

> No clustering, no failover, no redundancy. HA requires fundamental redesign.

### Current State

Full enterprise HA is implemented across three layers:

**Layer 1 — Database (Patroni/etcd/HAProxy)**
- Automatic PostgreSQL leader election via Patroni and etcd quorum
- HAProxy routes all application connections to the current primary transparently
- Witness node (Raspberry Pi, small VPS) provides quorum for two-node clusters

**Layer 2 — Network and Storage (Keepalived + ZFS Replication)**
- Floating virtual IP migrates automatically between nodes on failover
- Continuous ZFS snapshot replication from primary to standby
- Real-time replication telemetry (percentage, throughput, ETA)

**Layer 3 — Fencing (STONITH/IPMI)**
- Automatic IPMI-based fencing via BMC when primary exceeds 45-second heartbeat threshold
- 60-second chassis power confirmation before promotion proceeds
- `fencingInProgress` mutex prevents concurrent fencing sequences
- Standby-only guard — only a standby node can initiate fencing
- Full HMAC audit trail on every fencing event
- Maintenance mode (`POST /api/ha/maintenance`, 0–3600 s) suppresses fencing during planned operations

**Split-brain protection:** On startup, daemon queries Patroni `/health`. If replica role is confirmed, automatic ZFS pool import is blocked.

**RTO: ~90 seconds** from failure detection to fully serving. No human intervention required.

---

## OPEN - Binary-Trust Barrier

### The Issue

The Go daemon (`dplaned`) ships as a compiled binary. Users who require source-level auditability before trusting a privileged process must build it themselves.

**Who is affected:** security auditors, organisations with supply-chain review requirements.

**Who is not affected:** home users, homelabs, small offices.

### Mitigation: Build from Source

The full daemon source is included in the release tarball under `daemon/`.

```bash
CGO_ENABLED=0 go build \
  -ldflags "-s -w -X main.Version=$(cat ../VERSION)" \
  -o dplaned-local ./cmd/dplaned/

# Compare with shipped binary
sha256sum dplaned-local /opt/dplaneos/daemon/dplaned

# Deploy your build
sudo systemctl stop dplaned
sudo install -m 755 dplaned-local /opt/dplaneos/daemon/dplaned
sudo systemctl start dplaned
```

### Reproducible Builds

Guaranteed by NixOS. Every node builds from a pinned flake with locked inputs (`flake.lock`). Bit-for-bit identical system closures across all nodes. Build the ISO or system closure yourself with `nix build .#iso` — the result is deterministic.

---

## Decision Matrix

| Use Case | Status | Notes |
|---|---|---|
| Home NAS | Ready | No caveats |
| Homelab / learning | Ready | Ideal use case |
| Small office (< 20 users) | Ready | PostgreSQL handles high concurrency with ease |
| Offsite backup / replication | Ready | GUI works; SSH keys needed upfront |
| Monitored active/standby | Ready | Full automatic failover with STONITH fencing, RTO ~90 seconds |
| Security audit required | Usable | Build from source; NixOS flake guarantees reproducibility |
| Auto-failover / 99.99% SLA | Ready | Fully implemented in v7.3.0 |
| Active/active shared storage | Out of scope by design | D-PlaneOS uses active/passive HA with automatic STONITH failover |

---

## Roadmap

| Item | Status |
|---|---|
| Replication (real implementation) | Done |
| Native PostgreSQL HA | Done |
| Upgrade rollback | Done |
| Active/standby coordination layer | Done (automated failover) |
| Reproducible build verification | Done (NixOS Flake, pinned inputs) |
| Automated failover with fencing | Done |
| Active Directory domain join | Done (v7.3.0) |
| Offline installer ISO | Done (v7.2.0) |
