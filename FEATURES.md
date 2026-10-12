# DPlaneOS Features

A NAS operating system built on NixOS and ZFS. It is installed as an image, configured in the web interface (or from a Git repository), and updated as a whole, with a way back.

---

## Storage

- **ZFS pools:** mirror, RAIDZ1/2/3, dRAID, cache (L2ARC), intent log (SLOG), spare, hot-add without downtime
- **Datasets:** compression, encryption, quotas, recordsize, atime, sync, xattr, secondarycache
- **Block volumes:** ZVols for iSCSI and NVMe-oF backing
- **ACL manager:** POSIX access control per dataset or path (OpenZFS on Linux has no native NFSv4 ACLs; the NFSv4 ACL API needs nfs4-acl-tools and applies to NFS client mounts)
- **ZFS delegation:** fine-grained per-user/per-dataset permission grants (`zfs allow`)
- **Snapshot scheduler:** per dataset: hourly, daily, weekly or monthly, kept by number or by age
- **Snapshot rollback:** picker for any historical restore point, destructive-action warning
- **Pool health:** real-time status, degraded/faulted vdev banner, one-click scrub
- **ZED events:** scrub, resilver, trim, data loss, checksum and I/O errors dispatched live to the UI
- **Sandbox:** isolated scope for experimental ZFS operations

---

## Sharing

- **SMB / Samba:** per-share user restrictions, Time Machine support, active-session viewer and disconnect
- **NFS:** per-export client rules, dynamic `exportfs` reload
- **iSCSI:** targets on ZVols (Linux LIO), per-initiator access lists, CHAP; a LUN keeps its identity and the initiators' persistent reservations across restarts; targets follow their storage group
- **NVMe-oF:** NVMe over TCP, for named hosts or any host; a namespace keeps its identity across restarts; exports follow their storage group
- **FTP / FTPS:** vsftpd, with the users and passwords of the web interface, each confined to the configured folder
- **File share links:** time-limited browser-downloadable links, no authentication required
- **S3 object storage:** a folder served over the S3 protocol (Versity Gateway), with one access key

---

## File Management

- **Web file explorer:** navigate, upload, download, rename, move, delete
- **Text editor:** in-browser editing restricted to recognised text file extensions
- **ACL inspector:** inspect and set permissions directly from the file context menu

---

## Data Protection

- **ZFS replication:** incremental send/receive over SSH, configurable schedule and rate limit
- **Backup:** rsync to a local folder or over SSH, by hand or on a schedule; a failed run raises an alert
- **Cloud sync:** tasks that copy (or, if chosen, mirror) a folder to or from S3-compatible storage, Backblaze B2, Azure Blob, SFTP, FTP or WebDAV, by hand or on a schedule, with a dry run
- **A remote as a folder:** a folder of a cloud remote mounted into the file tree

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
- **Automatic apply (optional):** new commits found by the five-minute check, or announced by a signed webhook from the Git server, are applied by themselves when their plan only creates and changes things; anything that deletes, needs approval or is high risk waits for a person
- **Rollback:** `git revert` + apply restores any previous state

---

## Networking

- **Static IP:** per-interface CIDR, gateway, and DNS configuration
- **Bonding:** LACP (802.3ad) and active-backup modes
- **VLANs:** tagged sub-interfaces with configurable VID
- **Firewall:** open ports, ports open to one network only, and blocked ports, managed from the UI and in effect at once
- **mDNS:** the system and its SMB shares are announced on the local network (`<hostname>.local`)

---

## Identity & Access

- **Local users and groups:** every user also is a Unix and SMB/FTP account with the same password; roles, and additional roles that can end on a date
- **LDAP / Active Directory:** sign-in with directory accounts (accounts created at first sign-in or by a scheduled sync, groups mapped to roles); membership in an Active Directory domain, so that domain users use the SMB shares
- **OIDC SSO:** Authorization Code + PKCE; accounts linked by verified e-mail address or created at first sign-in; roles follow the provider's groups; a handoff code keeps the session token out of the URL
- **TOTP two-factor auth:** per-user, with backup codes; the setup code is drawn in the browser and sent nowhere
- **SSH:** administrators sign in with their account; public keys, port, password and root sign-in are settings
- **RBAC:** four roles (viewer, user, operator, admin), 31 discrete permissions, enforced at handler level

---

## Security

- **Audit chain:** every state change appended to an HMAC-SHA256 linked log; key stored separately from the database; chain verifiable on demand
- **Requests from other sites:** the session token travels in a header, not a cookie, and changing requests need a CSRF token bound to the session; the web server sends a Content-Security-Policy that allows scripts from the system only
- **TLS certificates:** a self-signed certificate from the first start; ACME / Let's Encrypt, uploaded certificates, or one made on the system
- **Commands, not shell lines:** programs run with their arguments as a list; where arguments come from a request, an allowlist fixes the program and the shape of each argument
- **Rate limiting:** 100 requests per minute and client address; failed sign-ins make that address wait longer each time
- **What limits the daemon:** it runs as root (it creates pools, mounts datasets and manages accounts), so the limits are in the daemon: sessions and permissions on every route, the command allowlist, path checks. `NoNewPrivileges` is set; a mount or capability sandbox is not, because it broke what a NAS daemon has to do
- **SSE ticket auth:** one-time 30-second tokens for `EventSource` connections; session IDs never appear in any URL

---

## High Availability

- **Local-first nodes:** every node has its own database; paired nodes exchange configuration (with secrets, encrypted for the pair) and merge per resource with conflict review
- **Corosync cluster:** quorum as in Proxmox; two nodes work with manual takeover, a third vote enables automatic failover
- **Third vote from anything:** QDevice on a Raspberry Pi/VM/cloud instance (one command), a corosync-only voter, another DPlaneOS system, or a third DPlaneOS node
- **Storage groups:** pools with their shares, iSCSI targets, NVMe-oF exports, Docker stacks and a floating address move between nodes; an initiator keeps its disk across a move; epochs fence stale owners
- **Shared or replicated:** shared SAS/SATA shelves or SANs (no data loss), or ZFS replication between independent boxes (bounded loss)
- **Protection layers:** watchdog self-fencing, ZFS multihost, disk reservations (SCSI-3: the disks of a shared group refuse writes from every node but its owner), optional IPMI/Redfish or PDU power fencing; missing layers are explained, not required
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
- **Alerts:** e-mail, webhooks (each with its own choice of events) and Telegram, for a documented list of events: pool and disk problems, capacity, replication, UPS, takeovers, updates, failed sign-ins, the domain membership, automatic applies

---

## System & Operations

- **System updates:** signed update files, from the release server or uploaded; the new version is installed next to the running one, started once on trial, kept if the system is healthy and removed again by itself if not; one click back to the previous version
- **Web terminal:** full PTY via xterm.js, direct shell access from the browser
- **API Explorer:** live endpoint browser with request/response for every API route
- **Power:** restart and shut down from the UI; spin-down of idle disks
- **Removable media:** mount, unmount, and eject USB/optical devices
- **Support page:** diagnostics bundle, links to recovery

---

## Platform

- **NixOS and OpenZFS:** the kernel and ZFS versions are those of the pinned NixOS release (`flake.lock`); every release is built from the same inputs
- **An image, not a package set:** the system is one unit that is installed and updated as a whole; settings made in the web interface take effect at once and are kept on a partition of their own (`/persist`), separate from the system
- **Offline installer ISO:** everything needed is on the medium; no network at install time. UEFI systems
- **Disk layout:** boot, system and persistent state partitions on the system disk; data disks are never touched by the installer or an update
- **Live image:** the system running from the installation medium, to try it or to rescue one
- **Daemon:** one Go program, statically linked; ZFS through the `zfs` and `zpool` commands, checked against an allowlist
- **AGPL-3.0:** free to use, modify, and distribute
