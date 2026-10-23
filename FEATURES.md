# DPlaneOS Features

A NixOS-based NAS operating system. Every setting is declarative, every change is version-controlled, and every upgrade is reversible.

---

## Storage

- **ZFS pools:** mirror, RAIDZ1/2/3, dRAID, cache (L2ARC), intent log (SLOG), spare, hot-add without downtime
- **Datasets:** compression, encryption, quotas, recordsize, atime, sync, xattr, secondarycache
- **Block volumes:** ZVols for iSCSI and NVMe-oF backing
- **ACL manager:** POSIX access control per dataset or path (OpenZFS on Linux has no native NFSv4 ACLs; the NFSv4 ACL API needs nfs4-acl-tools and applies to NFS client mounts)
- **ZFS delegation:** fine-grained per-user/per-dataset permission grants (`zfs allow`)
- **Snapshot scheduler:** independent tiers every 15 min, hourly, daily, weekly, monthly, each with its own prefix; optionally recursive (child datasets in one atomic snapshot); retention by count and by age; replication bases are never pruned
- **Retention policies:** keep the newest snapshot of each of the last N hours, days, weeks, months and years, per dataset; only prefixed snapshots, never manual ones
- **Snapshot rollback:** picker for any historical restore point, destructive-action warning
- **Pool health:** real-time status, degraded/faulted vdev banner, one-click scrub
- **ZED events:** scrub, resilver, trim, data loss, checksum and I/O errors dispatched live to the UI
- **Sandbox:** isolated scope for experimental ZFS operations

---

## Sharing

- **Accounts:** every local user and group is also a system account and group (UID/GID from 3000) with a Samba password, so users log in to SMB with their DPlaneOS password and NFS sees the same owners; recreated at startup (OTA-safe)
- **SMB / Samba:** per-share user restrictions, folder access for the share's users in one step (inherited POSIX ACL), guest shares, Time Machine support, ZFS snapshots as Windows Previous Versions (also child datasets), Windows network discovery (WS-Discovery), active-session viewer and disconnect
- **NFS:** NFSv4.0–4.2 (NFSv3 optional), per-export client rules checked against the exports(5) options, child datasets visible (`crossmnt`), dynamic `exportfs` reload; the exports are rewritten at startup, so they survive OTA updates
- **iSCSI:** kernel LIO targets via `targetcli`; the configuration is saved under `/etc/target` (persisted) and restored at boot
- **NVMe-oF:** NVMe over TCP (kernel `nvmet` via configfs) with per-initiator host NQN allowlist; exports restored at boot
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

- **ZFS replication:** incremental send/receive over SSH from the newest snapshot both sides share (matched by GUID), configurable schedule and rate limit, resumable transfers; encrypted datasets are sent raw (the peer never needs the key); the copy is received read-only and unmounted and keeps the snapshots the source has pruned; without a common snapshot a run stops instead of overwriting the peer's copy, unless "replicate from scratch" is enabled
- **Backup:** rsync-based backup tasks
- **Cloud sync:** S3, Backblaze B2, Google Drive, Azure, Dropbox, and any rclone-supported target
- **Cold tier:** FUSE-mounted object storage for archive datasets

---

## Containers

- **Docker Compose:** deploy, update, start, stop, and remove stacks from the UI; edits checked with `docker compose config` before they replace the running file; one operation per stack at a time
- **Updates with rollback:** pull, recreate, wait until every service is up, otherwise back to the previous images; the stack's ZFS datasets are snapshotted first (optional data rollback)
- **Update detection and auto-update:** registry digests checked every 6 hours without pulling; optional per-stack auto-update that skips an image that failed before
- **Git-backed stacks:** deploy from a repository on new commits (polling or signed GitHub/Gitea/GitLab webhooks), each as its own compose project
- **Sandboxes:** ZFS clones of a dataset for experiments, removed with their base snapshot
- **Live log streaming:** real-time container logs via SSE with level filter and line limit
- **GPU passthrough:** automatic device injection for GPU workloads

---

## GitOps

- **`state.yaml` as source of truth:** declare pools, datasets, shares, users, stacks, and system settings in Git
- **Plan before apply:** every reconcile produces a typed plan (CREATE / MODIFY / DELETE / BLOCKED / MANUAL) before any change is made
- **Drift detection:** background 5-minute loop; UI banner on any deviation from declared state
- **Capture:** generate `state.yaml` from the live system; never commits automatically
- **BLOCKED approval:** risky operations (such as removing a share with connected clients) require explicit per-item sign-off with a written reason; a dataset or pool that holds anything (files, snapshots, child datasets) is never destroyed, approved or not: Git holds the shape of the storage, never its data
- **Auto-apply:** webhook (HMAC-verified) or polling trigger on Git push
- **Rollback:** `git revert` + apply restores any previous state

---

## Networking

- **Interfaces:** DHCP or static address and gateway per interface, static routes, global DNS servers and search domains (systemd-networkd and resolved, applied without a rebuild; fresh installs use DHCP on every Ethernet port)
- **Safe changes:** every network and firewall change applies at once and is reverted after 90 seconds unless confirmed, also across a daemon restart or reboot
- **Bonding:** all kernel modes (LACP, active-backup, …) with link monitoring; a bond takes over its member's address
- **VLANs:** tagged sub-interfaces on interfaces or bonds; the parent keeps its address
- **Firewall:** ordered allow/deny rules by port, protocol and source, applied at once on top of the NixOS firewall (deny also restricts ports the system opens); the ports the configuration opens are shown
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
- **libzfs cgo:** pool and dataset management goes through libzfs in the production binary; send/receive and some listings still run the allowlisted `zfs` command
- **Rate limiting:** 100 requests / minute / IP, in-process after session validation and before the handler; the chunks of an authenticated file upload are not counted
- **Systemd hardening:** `NoNewPrivileges`, a bounded capability set, `MemoryMax=1G` for the daemon and its jobs (`services.dplaneos.memoryMax`; `nixos-rebuild` runs in its own unit). No `ProtectSystem`: a mount namespace would hide the ZFS mounts the daemon makes from Samba, NFS and the host
- **SSE ticket auth:** one-time 30-second tokens for `EventSource` connections; session IDs never appear in any URL

---

## High Availability

- **Local-first nodes:** every node has its own database; paired nodes exchange configuration (with secrets, encrypted for the pair) and merge per resource with conflict review
- **Corosync cluster:** quorum as in Proxmox; two nodes work with manual takeover, a third vote enables automatic failover
- **Third vote from anything:** QDevice on a Raspberry Pi/VM/cloud instance (one command), a corosync-only voter, another DPlaneOS system, or a third DPlaneOS node
- **Storage groups:** pools with their shares, NVMe-oF exports, Docker stacks and a floating address move between nodes; epochs fence stale owners
- **Shared or replicated:** shared SAS/SATA shelves or SANs (no data loss), or ZFS replication between independent boxes (bounded loss)
- **Protection layers:** watchdog self-fencing, ZFS multihost, SCSI-3 persistent reservations, optional IPMI/Redfish or PDU power fencing; missing layers are explained, not required
- **Planned moves:** apps stop, the address moves, pools are exported and imported (or a final replication runs) with rollback if the target cannot take over
- **Monitoring:** Prometheus metrics and alert rules for quorum, self-fencing and every storage group

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

- **NixOS 26.05:** Linux 6.12 LTS kernel, OpenZFS 2.3 or later (as packaged by nixpkgs)
- **Declarative OS:** entire system described in `flake.nix`; `nixos-rebuild switch` converges to spec
- **Impermanence:** ephemeral root filesystem; all runtime state on a dedicated `/persist` partition
- **Offline installer ISO:** full NAS closure baked into squashfs; zero internet required at install time
- **A/B boot slots:** disko-managed partition layout; safe to reinstall without touching data disks
- **Daemon builds:** production uses a glibc build linked against libzfs (cgo); a static, CGO-free build serves CI and non-NixOS installs
- **AGPL-3.0:** free to use, modify, and distribute
