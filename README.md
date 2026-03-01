# D-PlaneOS — ZFS-first NAS Operating System

ZFS-first NAS operating system for advanced homelab operators.
Designed around immutability, safe container lifecycle management, and reproducible infrastructure.

## Quick Start

### Ubuntu / Debian
```bash
tar xzf dplaneos-*.tar.gz
cd dplaneos
sudo bash install.sh
```

Access the web UI at `http://your-server` immediately after install.

**Default login:** `admin` / `admin` — change immediately after first login.

> **Rebuilding from source?** You need Go 1.22+ and gcc: `make build` compiles fresh.

### NixOS
```bash
cd nixos
sudo bash setup-nixos.sh
sudo nixos-rebuild switch --flake .#dplaneos
```

See [nixos/README.md](nixos/README.md) for the complete NixOS guide.

### Off-Pool Database Backup (recommended for large pools)

Edit `/etc/systemd/system/dplaned.service` and add `-backup-path`:
```
ExecStart=/opt/dplaneos/daemon/dplaned -db /var/lib/dplaneos/dplaneos.db -backup-path /mnt/usb/dplaneos.db.backup
```
Creates a `VACUUM INTO` backup on startup and every 24 hours.

---

## Features

- **Storage:** ZFS pools, datasets, snapshots, replication, encryption, quotas, SMART monitoring, file explorer with chunked uploads
- **Sharing:** SMB / AFP / Time Machine, NFS exports, iSCSI block targets — all managed via the UI
- **Compute:** Docker container management, Compose stacks, ephemeral sandbox clones, safe rollback-aware updates
- **Network:** Interface config, bonding, VLANs, routing, DNS
- **Identity:** Users, groups, LDAP / Active Directory, 2FA, API tokens
- **Security:** RBAC (4 roles, 34 permissions), audit log with HMAC integrity chain, firewall, TLS certificates
- **System:** Settings, logs, UPS management, IPMI / sensors, hardware detection, cloud sync (rclone), HA node monitoring
- **GitOps:** Git-sync repositories, state reconciliation
- **UI:** Material Design 3, dark theme, responsive, keyboard shortcuts, realtime metrics WebSocket

### Safe Container Updates
`POST /api/docker/update` — ZFS snapshot → pull → restart → health check. On failure: instant rollback. Updates are atomic: either the new image runs cleanly, or the previous state is restored automatically.

### ZFS Time Machine
Browse any snapshot like a folder. Find a deleted file from yesterday, restore just that one file — no full rollback needed.

### Ephemeral Docker Sandbox
Test any container on a ZFS clone (zero disk cost). Stop the container, the clone disappears. No residue.

### ZFS Health Predictor
Deep pool health monitoring: per-disk error tracking, checksum error detection, risk levels (low / medium / high / critical), S.M.A.R.T. integration. Warns you before a disk dies, not after.

### NixOS Config Guard
On NixOS systems: validate `configuration.nix` before applying, list / rollback generations, dry-activate checks. Cannot brick your system.

### ZFS Replication (Remote)
Native `zfs send | ssh remote zfs recv` — block-level replication that transfers only changed blocks and preserves all snapshots on the remote. Substantially faster than rsync for large datasets because only deltas are transferred after the initial seed.

---

## HA Node Monitoring

D-PlaneOS includes lightweight active/standby node monitoring: standby nodes heartbeat the active node and alert if it becomes unreachable. Manual promotion is available via API.

**What this is:** A coordination layer to prevent accidental pool imports on standby nodes, with a clean manual failover workflow.

**What this is not:** A replacement for Pacemaker/Corosync. There is no STONITH, no automatic failover, and no split-brain protection. Do not use for production HA without independent fencing at the infrastructure level.

---

Install separately; auto-detected and fully managed by D-PlaneOS once present.

| Protocol | Install command | Notes |
|---|---|---|
| SMB / Windows shares | `sudo apt install samba` | D-PlaneOS writes and manages `smb.conf` |
| AFP / Time Machine (macOS) | included with Samba | Uses Samba's `fruit` module — no extra package |
| NFS exports | `sudo apt install nfs-kernel-server` | D-PlaneOS writes `/etc/exports` and runs `exportfs -ra` |
| iSCSI block targets | `sudo apt install targetcli-fb` | D-PlaneOS manages LIO targets via `targetcli` |
| UPS monitoring | `sudo apt install nut` | D-PlaneOS reads status via `upsc` |

If a protocol package is not installed, the UI shows a clear message with the install command rather than returning an error.

---

## LDAP / Active Directory

Navigate to **Identity → Directory Service** to configure. Supports:

- Active Directory, OpenLDAP, FreeIPA (one-click presets)
- Group → Role mapping (auto-assign permissions)
- Full directory sync: fetches all matching users, upserts into local accounts with correct roles
- Just-In-Time user provisioning on first login
- Background sync with audit trail
- TLS 1.2+ enforced

---

## Architecture

- **Frontend:** HTML5 + Material Design 3, flyout navigation, no framework dependencies
- **Backend:** Go daemon (`dplaned`, ~8 MB) on port 9000, 256 API routes
- **Database:** SQLite with WAL mode, `synchronous=FULL`, daily `.backup` (WAL-safe)
- **Web Server:** nginx reverse proxy (TLS termination)
- **Storage:** ZFS (native kernel module) + ZED hook for real-time disk failure alerts
- **Security:** Allowlist-based input validation on all exec.Command calls, RBAC (4 roles, 34 permissions), HMAC audit chain, OOM-protected (1 GB limit)
- **NixOS:** Full support via Flake — entire NAS defined in a single `configuration.nix`

---

## Documentation

- [`CHANGELOG.md`](CHANGELOG.md) — Full version history
- [`INSTALLATION-GUIDE.md`](INSTALLATION-GUIDE.md) — Detailed installation steps
- [`ADMIN-GUIDE.md`](ADMIN-GUIDE.md) — Full administration guide
- [`TROUBLESHOOTING.md`](TROUBLESHOOTING.md) — Build issues, ZED setup, common fixes
- [`ERROR-REFERENCE.md`](ERROR-REFERENCE.md) — API error codes and diagnostics
- [`SECURITY.md`](SECURITY.md) — Security policy and architecture
- [`CONTRIBUTING.md`](CONTRIBUTING.md) — How to contribute
- [`nixos/README.md`](nixos/README.md) — NixOS installation and configuration

---

## License

Source-available under [PolyForm Shield 1.0.0](LICENSE). See LICENSE file.
