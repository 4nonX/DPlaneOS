# DPlaneOS Features

A NixOS-based NAS operating system. Every setting is declarative, every change is version-controlled, and every upgrade is reversible.

---

## Storage

- **ZFS pools:** mirror, RAIDZ1/2/3, dRAID, cache (L2ARC), intent log (SLOG), spare, hot-add without downtime
- **Datasets:** compression, encryption, quotas, recordsize, atime, sync, xattr, secondarycache
- **Block volumes:** ZVols for iSCSI and NVMe-oF backing
- **ACL manager:** POSIX access control per dataset or path (OpenZFS on Linux has no native NFSv4 ACLs; the NFSv4 ACL API needs nfs4-acl-tools and applies to NFS client mounts)
- **ZFS delegation:** fine-grained per-user/per-dataset permission grants (`zfs allow`)
- **Snapshot scheduler:** independent retention tiers: 15 min, hourly, daily, weekly, monthly
- **Snapshot rollback:** picker for any historical restore point, destructive-action warning
- **Pool health:** real-time status, degraded/faulted vdev banner, one-click scrub
- **ZED events:** scrub, resilver, trim, data loss, checksum and I/O errors dispatched live to the UI
- **Sandbox:** isolated scope for experimental ZFS operations

---

## Sharing

- **SMB / Samba:** per-share user restrictions, Time Machine support, active-session viewer and disconnect
- **NFS:** per-export client rules, dynamic `exportfs` reload
- **iSCSI:** kernel `nvmet` targets via configfs
- **NVMe-oF:** NVMe over TCP with per-initiator host NQN allowlist
- **FTP / FTPS:** vsftpd with installed-state detection; form locked when not installed
- **File share links:** time-limited browser-downloadable links, no authentication required
- **S3 object storage:** embedded MinIO with one-click console access

---

## File Management

- **Web file explorer:** navigate, upload, download, rename, move, delete
- **Text editor:** in-browser editing restricted to recognised text file extensions
- **ACL inspector:** inspect and set permissions directly from the file context menu

---

## Data Protection

- **ZFS replication:** incremental send/receive over SSH, configurable schedule and rate limit
- **Backup:** rsync-based backup tasks
- **Cloud sync:** S3, Backblaze B2, Google Drive, Azure, Dropbox, and any rclone-supported target
- **Cold tier:** FUSE-mounted object storage for archive datasets

---

## Containers

- **Docker Compose:** deploy, update, start, stop, and remove stacks from the UI
- **Live log streaming:** real-time container logs via SSE with level filter and line limit
- **GPU passthrough:** automatic device injection for GPU workloads

---

## GitOps

- **`state.yaml` as source of truth:** declare pools, datasets, shares, users, stacks, and system settings in Git
- **Plan before apply:** every reconcile produces a typed plan (CREATE / MODIFY / DELETE / BLOCKED / MANUAL) before any change is made
- **Drift detection:** background 5-minute loop; UI banner on any deviation from declared state
- **Capture:** generate `state.yaml` from the live system; never commits automatically
- **BLOCKED approval:** destructive operations require explicit per-item sign-off with a written reason
- **Auto-apply:** webhook (HMAC-verified) or polling trigger on Git push
- **Rollback:** `git revert` + apply restores any previous state

---

## Networking

- **Static IP:** per-interface CIDR, gateway, and DNS configuration
- **Bonding:** LACP (802.3ad) and active-backup modes
- **VLANs:** tagged sub-interfaces with configurable VID
- **Firewall:** nftables-backed TCP/UDP allowlist managed from the UI
- **mDNS:** auto-discovery as `dplaneos.local`

---

## Identity & Access

- **Local users and groups:** POSIX account provisioning, role assignment, and expiry dates
- **LDAP / Active Directory:** bind, search, JIT provisioning, group-to-role mapping, configurable sync interval
- **OIDC SSO:** Authorization Code + PKCE; handoff code keeps session token out of the URL and browser history
- **TOTP two-factor auth:** per-user, with backup codes
- **SSH keys:** per-user public key management with post-change reconnection warning
- **RBAC:** four roles (viewer, user, operator, admin), 31 discrete permissions, enforced at handler level

---

## Security

- **Audit chain:** every state change appended to an HMAC-SHA256 linked log; key stored separately from the database; chain verifiable on demand
- **CSRF protection:** custom header + double-submit token on all mutations
- **TLS certificates:** ACME / Let's Encrypt automation or manual PEM upload
- **Exec allowlist:** every subprocess validated against predefined binary + argument grammars; no shell string, no expansion
- **libzfs cgo:** ZFS operations bypass the subprocess layer entirely in the production binary
- **Rate limiting:** 100 requests / minute / IP, in-process before any handler runs
- **Systemd hardening:** `ProtectSystem=strict`, `NoNewPrivileges`, `MemoryMax=1G` on the daemon
- **SSE ticket auth:** one-time 30-second tokens for `EventSource` connections; session IDs never appear in any URL

---

## High Availability

- **Two topologies:** shared-SAS (no witness machine needed) or replicated ZFS (separate witness)
- **Patroni + etcd:** PostgreSQL leader election, streaming replication, automatic promotion
- **Keepalived VIP:** floating IP always on the current primary; daemon-health-checked every 2 seconds
- **HAProxy:** each node routes `localhost:5000` to the current PostgreSQL primary
- **SCSI-3 Persistent Reservations:** hardware I/O exclusion at the disk controller; survives reboots (APTPL=1)
- **IPMI / BMC fencing:** remote power-off with cryptographic jitter to prevent mutual-destruction races
- **SBD fencing:** lease-based STONITH on a shared block device; no BMC required
- **Witness probe:** HTTP-based network-partition guard before any automated failover fires
- **Rolling OTA upgrades:** update standby, failover, update old primary; no service interruption
- **HA triage panel:** surfaces automatically when a peer enters unreachable state, with inline fence and promote actions

---

## Monitoring & Alerting

- **Dashboard:** CPU, memory, network I/O, ZFS ARC hit rate, pool health, running containers
- **S.M.A.R.T.:** scheduled short, long, and offline tests per disk; results surfaced in hardware view
- **UPS:** NUT integration, battery status, human-readable labels, configurable shutdown threshold
- **IPMI:** sensor readings: temperatures, fan speeds, power consumption
- **ECC RAM:** detected at startup; advisory notice on dashboard if non-ECC is found
- **Inotify watches:** current count vs. kernel limit; alert before silent failures in file-watching apps
- **Log streaming:** SSE-based system log viewer with level filter and history limit
- **Alerts:** SMTP, JSON webhook, and Telegram delivery for health events and threshold breaches

---

## System & Operations

- **OTA updates:** A/B slot system; inactive slot updated, health-checked on reboot, auto-reverted on failure
- **Web terminal:** full PTY via xterm.js, direct shell access from the browser
- **API Explorer:** live endpoint browser with request/response for every API route
- **Power management:** shutdown, reboot, and suspend from the UI
- **Removable media:** mount, unmount, and eject USB/optical devices
- **Support page:** pre-upgrade snapshot checklist, NixOS generation rollback link, diagnostics bundle

---

## Platform

- **NixOS 26.05:** Linux 6.6 LTS kernel, OpenZFS 2.3 LTS
- **Declarative OS:** entire system described in `flake.nix`; `nixos-rebuild switch` converges to spec
- **Impermanence:** ephemeral root filesystem; all runtime state on a dedicated `/persist` partition
- **Offline installer ISO:** full NAS closure baked into squashfs; zero internet required at install time
- **A/B boot slots:** disko-managed partition layout; safe to reinstall without touching data disks
- **Static binary:** Go daemon built with musl libc; no glibc runtime dependencies
- **Plugin system:** third-party navigation and handler extensions via the plugin API
- **AGPL-3.0:** free to use, modify, and distribute
