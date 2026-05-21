# DPlaneOS - Showstopper Mitigation Guide

**Updated:** 2026-05-21
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

The implementation lives in `daemon/internal/handlers/` and performs:

- Full `zfs send | ssh zfs recv` pipe - no shell, no string interpolation, discrete argv
- Incremental sends (`-i base_snapshot`) with last-replicated-snapshot tracking across runs
- Compressed streams (`-c` flag)
- Resume tokens for interrupted transfers (checked before every send when enabled)
- Rate limiting via `pv` with graceful degraded-mode fallback when `pv` is not installed
- Input validation on all shell-bound fields (snapshot name, dataset, host, user, port, resume token)
- SSH host key pinning: TOFU fingerprint captured on first authorize/test, written to a per-transfer temp known_hosts file so the `ssh` binary enforces it on every ZFS send

SSH connectivity and ZFS readiness can be verified at any time via the Test button in the Peers tab.

### Setup - Fully Automated via Peers Tab

SSH key distribution, host key pinning, and all connection management are handled by the GUI as of the current version. No manual shell steps are required for normal targets:

1. **Replication > Peers > Add Peer** - name, host, SSH user, port.
2. **Authorize** - enter the root password once. The daemon installs the replication key via the Go SSH client (no `sshpass`, no shell). The password exists only in the request buffer and is discarded immediately; it never touches disk, database, or logs.
3. The host's SSH fingerprint is pinned (TOFU) and enforced on all future connections including the ZFS send pipeline.
4. Click **Test** to verify key-based access and ZFS readiness. Replication runs unattended from this point.

For air-gapped or high-security hosts where password authentication is disabled, copy the **Sovereign Target Key** from the Peers tab to the target's `authorized_keys` manually, then use Test to verify and pin the fingerprint. No password prompt is shown for already-authorized peers; click **Re-auth** only to rotate the key after a `ssh-keygen` regeneration.

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

Full enterprise HA is implemented across three layers. Two deployment topologies are supported: shared-SAS (two data nodes, SCSI-3 PR fencing, co-located etcd witness on node A - no third machine) and replicated-ZFS (two data nodes plus a lightweight witness node for quorum).

**Layer 1 - Database (Patroni/etcd/HAProxy)**
- Automatic PostgreSQL leader election via Patroni and etcd quorum
- HAProxy routes all application connections to the current primary transparently
- Three-member etcd cluster: two full members on the data nodes plus a third member that is either co-located on node A (shared-SAS) or a separate witness machine (replicated-ZFS)

**Layer 2 - Network and Storage (Keepalived + ZFS Replication)**
- Floating virtual IP migrates automatically between nodes on failover
- Continuous ZFS snapshot replication from primary to standby
- Real-time replication telemetry (percentage, throughput, ETA)

**Layer 3 - Fencing (STONITH)**
- SCSI-3 Persistent Reservations (`dplane-fenced`): disk controller enforces write exclusion at the hardware level; no BMC required (shared-SAS topology)
- IPMI/Redfish: automatic BMC-based fencing when primary exceeds 45-second heartbeat threshold (replicated topology)
- SBD: ZFS dataset lease mechanism as an alternative to IPMI (replicated topology; requires witness node)
- `fencingInProgress` mutex prevents concurrent fencing sequences
- Standby-only guard - only a standby node can initiate fencing
- Full HMAC audit trail on every fencing event
- Maintenance mode (`POST /api/ha/maintenance`, 0-3600 s) suppresses fencing during scheduled maintenance

**Split-brain protection:** On startup, daemon queries Patroni `/health`. If replica role is confirmed, automatic ZFS pool import is blocked.

**RTO:** ~10-30 seconds for shared-SAS deployments (SCSI-3 PR fencing is near-instant once the preempt command completes). ~90 seconds for replicated deployments (45-second heartbeat timeout + IPMI power-off confirmation + startup). No human intervention required in either case.

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

Guaranteed by NixOS. Every node builds from a pinned flake with locked inputs (`flake.lock`). Bit-for-bit identical system closures across all nodes. Build the ISO or system closure yourself with `nix build .#iso` - the result is deterministic.

---

## Decision Matrix

| Use Case | Status | Notes |
|---|---|---|
| Home NAS | Ready | No caveats |
| Homelab / learning | Ready | Ideal use case |
| Small office (< 20 users) | Ready | PostgreSQL handles high concurrency with ease |
| Offsite backup / replication | Ready | Zero-touch via Peers tab; one-time password authorization, unattended thereafter |
| Monitored active/standby | Ready | Full automatic failover with STONITH fencing, RTO ~90 seconds |
| Security audit required | Usable | Build from source; NixOS flake guarantees reproducibility |
| Auto-failover / 99.99% SLA | Ready | Fully implemented in v7.3.0 |
| Active/active shared storage | Out of scope by design | DPlaneOS uses active/passive HA with automatic STONITH failover |

---

## Capability status

Shipped items verified in product and docs:

| Item | Status |
|---|---|
| Replication (real implementation) | Done |
| Zero-touch SSH key distribution (Peers model) | Done |
| SSH host key pinning - TOFU, enforced in send pipeline | Done |
| Incremental replication with tracked base snapshot | Done |
| Resume tokens for interrupted transfers | Done |
| Bandwidth throttling via pv (graceful degraded-mode fallback) | Done |
| Full CRUD for Peers and Schedules | Done |
| Native PostgreSQL HA | Done |
| Upgrade rollback | Done |
| Active/standby coordination layer | Done (automated failover) |
| Reproducible build verification | Done (NixOS Flake, pinned inputs) |
| Automated failover with fencing | Done |
| Active Directory domain join | Done (v7.3.0) |
| Offline installer ISO | Done (v7.2.0) |
