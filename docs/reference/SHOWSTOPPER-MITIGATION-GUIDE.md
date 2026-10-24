# DPlaneOS - Showstopper Mitigation Guide

**Updated:** 2026-10-24
**Purpose:** Honest assessment of what works, what has documented limits, and what is genuinely out of scope.

---

## Status Key

- **OPEN** - genuine limitation in the current version; everything else below is in place, with its limits listed

---

## Replication

The replication engine lives in `daemon/internal/handlers/` and performs:

- `zfs send | ssh zfs recv` as separate processes linked by pipes - no shell, no string interpolation, discrete argv
- Incremental sends from the newest snapshot both sides share (matched by GUID); without one, a run stops instead of overwriting the peer's copy, unless "replicate from scratch" is enabled
- Raw sends for encrypted datasets (the peer never needs the key); compressed streams otherwise
- Resume tokens for interrupted transfers (checked before every send when enabled)
- Rate limiting via `pv`
- Input validation on all fields that reach a command (snapshot name, dataset, host, user, port, resume token)
- SSH host key pinning: the fingerprint is captured on the first authorize/test (TOFU) and enforced on every ZFS send

SSH connectivity and ZFS readiness can be verified at any time via the Test button in the Peers tab.

### Setup - Fully Automated via Peers Tab

SSH key distribution, host key pinning, and all connection management are handled by the GUI as of the current version. No manual shell steps are required for normal targets:

1. **Replication > Peers > Add Peer** - name, host, SSH user, port.
2. **Authorize** - enter the root password once. The daemon installs the replication key via the Go SSH client (no `sshpass`, no shell). The password exists only in the request buffer and is discarded immediately; it never touches disk, database, or logs.
3. The host's SSH fingerprint is pinned (TOFU) and enforced on all future connections including the ZFS send pipeline.
4. Click **Test** to verify key-based access and ZFS readiness. Replication runs unattended from this point.

For air-gapped or high-security hosts where password authentication is disabled, copy the **Sovereign Target Key** from the Peers tab to the target's `authorized_keys` manually, then use Test to verify and pin the fingerprint. No password prompt is shown for already-authorized peers; click **Re-auth** only to rotate the key after a `ssh-keygen` regeneration.

---

## Database

Every node has its own PostgreSQL database for all metadata and configuration; paired nodes exchange configuration, they do not share a database.

Resource profile:

- Daemon idle RAM: ~80–120 MB
- Database (PostgreSQL): ~150 MB
- Compatible with systems with 4 GB+ RAM.

---

## Upgrade Rollback

An update builds the new release as a new NixOS generation and reboots into it. After the boot, a check (up to five minutes) verifies:

1. The daemon answers on its socket (`/api/system/health`)
2. nginx runs, and PostgreSQL if the database is local
3. Every pool imported before the update is imported again
4. If SMB shares are enabled, Samba runs

If all checks pass, the update is kept. Any failure makes the previous generation the boot default again, restores the flake and reboots - no human intervention required. See [Updates](../admin/OTA-UPDATES.md).

**To roll back manually** (if a regression is discovered after the health check already passed):
```bash
sudo nixos-rebuild switch --rollback   # then set /etc/nixos/flake.nix back (OTA-UPDATES.md)
```

If the system will not boot at all: select the previous NixOS generation from the systemd-boot menu at startup, press `d` to set it as default, then `Enter` to boot.

---

## High Availability

High availability is built from node-local databases with configuration exchange, a Corosync cluster with a third vote, storage groups (shared storage or ZFS replication) and protection layers (watchdog self-fencing, ZFS multihost, SCSI-3 persistent reservations, optional IPMI/PDU power fencing). The earlier Patroni/etcd/HAProxy/keepalived stack has been removed. See [HIGH-AVAILABILITY.md](../admin/HIGH-AVAILABILITY.md) and [THIRD-VOTE.md](../admin/THIRD-VOTE.md).

**Limits that matter:**
- Two nodes without a third vote never fail over automatically: failover is *Take over* by hand. That is deliberate (a split could leave both sides in charge).
- Replicated groups lose up to one replication interval on failover, so automatic failover is off for them by default.
- Without a hardware watchdog the kernel's softdog is used; it does not reset a node whose kernel hangs. Power fencing covers that case.
- Tested in VM CI (quorum, groups, failover, replicated groups, voters); not yet under real production load.

**RTO:** the watchdog timeout plus 15 seconds before the takeover starts, then the import (about 30 seconds for shared pools because of the multihost activity check).

---

## OPEN - Binary-Trust Barrier

### The Issue

The Go daemon (`dplaned`) ships as a compiled binary. Users who require source-level auditability before trusting a privileged process must build it themselves.

**Who is affected:** security auditors, organisations with supply-chain review requirements.

**Who is not affected:** home users, homelabs, small offices.

### Mitigation: Build from Source

The full source is in the repository and in the release tarball. The system is built from the flake, so the daemon on an installed system is a Nix store path that can be rebuilt from the same release and compared:

```bash
git clone https://github.com/4nonX/DPlaneOS && cd DPlaneOS && git checkout v16.0.0
nix build .#dplaneos-daemon-cgo --print-out-paths       # the daemon the appliance runs
systemctl cat dplaned | grep -o '/nix/store/[^/]*-dplaneos-daemon-cgo-[^/]*'   # on the NAS
```

The same release gives the same store path (Nix hashes every input). `nix build .#iso` builds the installer ISO, and `nix store verify` checks a store path's contents.

### Reproducible Builds

Every build comes from the pinned flake with locked inputs (`flake.lock`): all nodes of a release run the same store paths. Nix makes the inputs reproducible; the outputs are not checked bit-for-bit.

---

## Decision Matrix

| Use Case | Status | Notes |
|---|---|---|
| Home NAS | Ready | No caveats |
| Homelab / learning | Ready | Ideal use case |
| Small office (< 20 users) | Ready | |
| Offsite backup / replication | Ready | Zero-touch via Peers tab; one-time password authorization, unattended thereafter |
| Monitored active/standby | Ready | Storage groups with automatic failover (third vote + fencing) |
| Security audit required | Usable | Build from source; NixOS flake guarantees reproducibility |
| Auto-failover | Ready | Needs a third vote and a fencing method (watchdog or power fencing) |
| Active/active shared storage | Out of scope by design | DPlaneOS moves storage groups between nodes; one owner per group |

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
| Configuration exchange between node-local databases | Done |
| Upgrade rollback | Done |
| Active/standby coordination layer | Done (automated failover) |
| Build verification | Done (NixOS flake, pinned inputs, comparable store paths) |
| Automated failover with fencing | Done |
| Active Directory domain join | Done |
| Offline installer ISO | Done |
