# D-PlaneOS Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/), and this project adheres to [Semantic Versioning](https://semver.org/).



## v7.5.1 (2026-03-30) - "Zero-Touch HA"

Upgrade from: v7.5.0 - Drop-in. `sudo bash install.sh --upgrade`

### Added
- **Quorum Witness for Zero-Touch HA**: Automated failover now requires a reachable quorum witness before executing STONITH and promotion. When a peer exceeds the 45-second FailoverAfter threshold and fencing is enabled, the daemon probes a configurable HTTP witness endpoint. If the witness is unreachable the failover is suspended — protecting against false-positive promotion during network partitions where this node cannot distinguish "peer is dead" from "I am isolated". Any HTTP response (any status code) from the witness counts as reachable; a connection error or timeout counts as isolated.
- **Witness API**: Three new endpoints under admin RBAC:
  - `GET /api/ha/witness/configure` — read current witness configuration
  - `POST /api/ha/witness/configure` — save witness `{ "enable": true, "url": "http://...", "timeout_secs": 5 }`
  - `POST /api/ha/witness/test` — probe the configured URL (or an ad-hoc URL in the request body) and return `{ "reachable": true/false }`
- **Witness status in HA status**: `GET /api/ha/status` now includes a `witness` key with the current witness configuration alongside the cluster status.
- **`ha_witness_config` schema**: New single-row DB table storing witness parameters, created automatically on daemon start.

### Behavior
- If witness is **disabled** (default): `checkFailover()` behavior is identical to v7.5.0 — fencing + promotion fires when fencing is enabled and peer breaches FailoverAfter. No change.
- If witness is **enabled**: The witness gate is evaluated between the fencing-enabled check and the maintenance-mode check. An unreachable witness suspends auto-failover; a reachable witness allows it to proceed.
- Witness probe logs at the 3rd missed beat to avoid log spam on subsequent 15s ticks.


## v7.5.0 (2026-03-31) - "Runtime Integrity"

Upgrade from: v7.4.6 - Drop-in. `sudo bash install.sh --upgrade`

### Fixed
- **HA Peer Health Check Panic**: `pingPeer()` dereferenced `resp` before checking the HTTP error, causing a nil-pointer panic when a peer was unreachable. The condition is now split so `resp.StatusCode` is only accessed after confirming `err == nil`. Response body is now closed in all non-error branches, including non-200 responses.
- **Silent GitOps Credential Failure**: Three `QueryRow.Scan()` calls in `gitops/commit.go`, `git_util.go`, and `drift.go` dropped their errors, causing git operations to proceed with zero-value repo URLs or credential IDs. Errors are now propagated and logged, aborting the affected operation cleanly.
- **ZFS Restore Double-Close**: The `io.Pipe` write-end in the dataset restore path was closed both on the early-return error branch and by the background goroutine that waits for `sendCmd`. On the `recvCmd.Start()` failure path, `sendCmd` was left running as an orphaned process. The goroutine now uses `sync.Once` to guarantee a single close, `sync.WaitGroup` to ensure it exits before the caller returns, and `sendCmd.Process.Kill()` + `Wait()` to reap the orphan.
- **Group Member Cleanup Error Discarded**: `deleteGroup` used `_, _ = db.Exec(...)` to clear members, explicitly swallowing the error. Failures are now logged before the group delete continues.

### Security
- **Trusted Proxy Enforcement**: `RealIP()` previously trusted `X-Forwarded-For` and `X-Real-IP` headers from any non-loopback address, enabling IP spoofing on multi-NIC or VLAN-segmented deployments. A CIDR-based allow-list (RFC 1918 + loopback + IPv6 ULA) is now enforced — headers are only honoured when the direct connection originates from a trusted proxy range.

### Reliability
- **Clean Goroutine Shutdown**: `ha.Manager`, `gitops.DriftDetector`, and `monitoring.BackgroundMonitor` now track their background goroutines with `sync.WaitGroup`. `Stop()` on each component blocks until the goroutine has fully exited, eliminating use-after-free and map-write-after-close races during daemon teardown.
- **Background Monitor Deadlock Prevention**: `BackgroundMonitor.stopChan` was unbuffered; if the `run()` goroutine was between ticks when `Stop()` was called, the send blocked indefinitely. The channel is now buffered (`make(chan bool, 1)`).
- **ZED Listener Context Support**: The ZFS Event Daemon Unix-socket Accept loop ran forever with no cancellation mechanism. It now accepts a `context.Context` and polls with a 1-second deadline, exiting cleanly when the daemon context is cancelled at shutdown. `daemonCtx` is created in `main.go` and cancelled as the first action in the shutdown sequence.
- **DB Query Timeouts in Drift Detector**: Both `QueryRow` calls in `DriftDetector.loop()` and `runCheck()` are replaced with `QueryRowContext` using a 5-second deadline, preventing a hung database connection from blocking the drift-check goroutine indefinitely.
- **Async DB Write Observability**: The fire-and-forget `go db.Exec()` for `last_used` token updates now logs errors. Silent `db.Exec()` calls in `Logout()` for session deletion and in `Login()` for `last_login` updates also now log on failure.

### Changed
- **HA Manual Promote — Split-Brain Prevention**: `POST /api/ha/promote` previously called `ExecutePromotion` directly with a documented warning that split-brain would occur if the primary was still alive. The handler now sequences STONITH fencing before promotion when fencing is configured: the leader node's BMC receives a chassis power-off command and the chassis state is polled until confirmed dark before promotion begins. If fencing is not configured, a warning is logged to the job stream and promotion continues, leaving split-brain avoidance to the operator. The operation is wrapped in the jobs system for real-time progress streaming.
- **VPN Network Action Response**: The generic `501 Not Implemented` for `add_*`/`remove_*` network actions is replaced with a targeted check for `vpn`, `add_vpn`, and `remove_vpn` that returns a descriptive message directing operators to deploy containerised VPN solutions (wg-easy, Tailscale, OpenVPN) via the Docker interface. Unrecognised actions now fall through to the existing `400 Bad Request` path.
- **Dead Code Removed**: The `checkDone` channel in the ACME proxy-verification handler was allocated and written to but never received from. Removed.

---

## v7.4.6 (2026-03-29) - "Security Guard Hardening"

Upgrade from: v7.4.5 - Drop-in. `sudo bash install.sh --upgrade`

### Security
- **Hybrid System User Protection (#17)**: Implemented multi-tier protection in `deleteUser` handler using UID < 1000 check (POSIX), explicit hardcoded blocks for `root/admin/dplaneos`, and static fallback list for common services.
- **RBAC Migration Phase 2 (#38)**: Expanded mission-critical route coverage with `permRoute` wrappers for 40+ endpoints, including ZFS replication streams (elevated to `storage:admin`) and network confirmation.
- **User/Group Management Regression (#39)**: Restored explicit RBAC protection by splitting user and group creation endpoints into dedicated POST registrations.
- **SMB Write-Time Sanitization (#30)**: Hardened share creation/update logic with mandatory input sanitization before database persistence, eliminating the PostgreSQL state as a potential injection vector.

---

## v7.4.5 (2026-03-29) - "Reconciliation Hardening"

Upgrade from: v7.4.4 - Drop-in. `sudo bash install.sh --upgrade`

### Added
- **WebSocket Streaming Console (Industrial Polish)**: 
    - Introduced a real-time, high-performance terminal console using `xterm.js` for all reconciliation and system update tasks.
    - **History Replay**: Implemented a 1,000-line log ring buffer in the daemon, allowing the UI to instantly "replay" console history upon connection or refresh mid-job.
    - **Global Job Indicator**: Added a persistent status indicator in the `TopBar` that monitors active background tasks system-wide, allowing users to minimize the console and multi-task without losing observability.
- **Zombie Lock Protection**: Implemented a 10-minute hard-stop timeout (`TimeoutExtreme`) for all critical system reconfiguration paths (`nixos-rebuild switch`), ensuring the global `ReconcileLock` is always released even in the event of hung external processes.
- **Regression-Aware Service Probing**: Expanded the post-apply verification engine to perform exhaustive TCP liveness checks against all configured services (SMB, NFS, API) plus mandatory management ports (SSH/22, API/9000), acting as a digital nervous system to detect functional regressions immediately.

---

## v7.4.4 (2026-03-28) - "Authorization Coverage"

Upgrade from: v7.4.3 - Drop-in. `sudo bash install.sh --upgrade`

### Security
- **RBAC Coverage for Storage Operations**: Applied proper role/action permission checks to previously session-only endpoints: trash management (list/move/restore/empty), power management (disk status/spindown), ACL get/set, snapshot schedules, and replication schedule management. Any authenticated user could previously invoke these operations regardless of their assigned role.
- **Duplicate Route Removed**: Eliminated a duplicate registration of `/api/zfs/snapshots/cron-hook` that created an ambiguous handler binding. The canonical registration in the snapshot scheduler block now correctly handles this route.

---

## v7.4.3 (2026-03-28) - "Physical Truth"

Upgrade from: v7.4.2 - Drop-in. `sudo bash install.sh --upgrade`

### Added
- **Forensic Compliance Engine (Physical Truth)**: 
    - Introduced a kernel-level forensic probe that uses `nft -j` to extract the live firewall state directly from the Linux kernel.
    - **Divergence Detection**: The system now automatically detects and flags "Shadow Ports" (manually opened via CLI/SSH) that deviate from the declarative D-PlaneOS intent.
    - **Integrity Monitor (Pro/Compliance)**: A real-time audit dashboard in the Compliance Engine that warns administrators of physical state drift before generating official SOC2 reports.
    - **Certified Evidence**: Forensic probe results are now embedded directly into the "Persistence Proof" section of PDF compliance reports.
- **Security Whitelisting**: Safely integrated the forensic probe into the `cmdutil` whitelist, ensuring zero-bypass security for high-privilege kernel operations.

---

## v7.4.2 (2026-03-27) - "Core Structural Integrity"

Upgrade from: v7.4.1 - Drop-in. `sudo bash install.sh --upgrade`

### Security
- **Strict Role-Based Access Control Boundaries**: Applied proper `permRoute` RBAC wrappers to critical HTTP streaming endpoints (`/ws/terminal`, `/ws/monitor`, `/api/system/logs/stream`), closing permission escalation loopholes that allowed users without administrative privileges to access sensitive system data or shells.
- **Fail-Closed Dataset Management**: Hardened the ZFS GitOps continuous reconciler. A failure to read dataset usage stats no longer returns 0 bytes; it correctly propagates the system fault to abort any scheduled deletion actions, eliminating a potential zero-byte data loss vector.
- **Resilient ZFS Heartbeats**: Fixed a defect in the storage heartbeat loop where catastrophic ZFS pool loss (un-importable/destroyed pools returning non-zero exit codes) failed to trigger `CRITICAL` system alerts and automatic Docker service suspension.

---

## v7.4.1 (2026-03-27) - "Security Polish & Determinism"

Upgrade from: v7.4.0 - Drop-in. `sudo bash install.sh --upgrade`

### Added
- **Detailed Firewall Diffs**: GitOps now provides granular "add/remove" descriptions for firewall port changes.
- **Config Determinism**: Automated sorting of DNS, NTP, and firewall port lists to ensure consistent state and minimize unnecessary system changes.
- **System User Protection & Hierarchy**: 
    - Implemented a hierarchical RBAC model for user management.
    - Mandatory "Current Password" verification for all sensitive user and group mutations.
    - Lockout prevention: Added a guard against deactivating or deleting the last remaining active admin.
    - Protected system service accounts (`root`, `dplaneos`) from deletion.

### Fixed
- **SMART Cron-hook Conflict**: Resolved a race/auth conflict between session bypass and RBAC middleware for internal systemd timers.
- **Dynamic Pool Protection**: Hardened the ZFS pool root deletion guard to automatically discover and protect all mounted pools, including nested datasets.

---

## v7.4.0 (2026-03-27) - "Security Hardening Patch"

Upgrade from: v7.3.0 - Drop-in. `sudo bash install.sh --upgrade`

### Added
- **Automated CSRF Protection**: Implemented session-linked CSRF token validation for all mutating API requests.
- **Unified Command Execution**: Hardened the `cmdutil` layer with a zero-bypass, whitelist-by-default architecture for all privileged operations.
- **File System Safeguards**: Added recursive deletion guards for ZFS pool roots and hardened file operations with middleware-backed user context.
- Stabilized CI/CD pipeline and command whitelist validation.
- **Local Hook Authentication**: Bypassed session middleware for local loopback requests to enable scheduled snapshots and SMART monitoring without external auth headers.

### Fixed
- **Input Sanitization**: Resolved potential injection vulnerabilities in SMB share configurations and snapshot prefixes.
- **Metrics Response Injection**: Hardened the metrics history endpoint against JSON response injection.
- **Setup Race Condition**: Eliminated concurrent administrative initialization risks via database advisory locks.

---

## v7.3.0 (2026-03-25) - "Enterprise Directory Services"

Upgrade from: v7.2.0 - Drop-in. `sudo bash install.sh --upgrade`

### Added
- **Multi-Provider Directory Engine**
    - **Active Directory (Windows)**: Full domain member support with `security = ads`, `winbind`, and Kerberos SSO.
    - **OpenLDAP (Linux)**: Advanced LDAP integration with standard schema support.
    - **Open Directory (MacOS)**: Specialized support for Apple's directory service, including Mac-specific attribute mapping.
- **Enterprise-Grade Identity Mapping**
    - Deterministic `IDMAP` configuration for consistent UID/GID mapping across SMB, NFS, and local shell sessions.
    - Support for both `rid` and `ad` backends.
- **Seamless UI Integration**
    - Redesigned "Directory Service" page with Provider Presets and guided AD Join workflow.
    - Real-time "Join Status" tracking and NTP synchronization verification.
- **Transparent SMB Authentication**
    - Bridged Samba into the Active Directory domain for transparent Kerberos-based file share access on Windows clients.
- **Native Audit Transparency (Enterprise Polish)**
    - Restored local audit log visibility with paginated table and filtering, protected by RBAC and stealth licensing logic.
- **Unified Sharing & ZFS Explorer**
    - Integrated SMB and NFS share management directly into the ZFS dataset tree.
    - Added native UI support for ZFS snapshots, rollbacks, and recursive child creation.
- **CI/CD & ISO Release**
    - Unified the build cycle to produce a v7.3.0 "Golden Image" ISO featuring all new features out-of-the-box.

---

## v7.2.0 (2026-03-24) - "Hermetic Firewall"

Upgrade from: v7.1.0 - Drop-in. `sudo bash install.sh --upgrade`

### Added
- **Nix-Native Firewall Infrastructure**
    - Engineered a native NixOS firewall bridge using `NixWriter` to bypass the removal of `ufw` in NixOS 25.11/24.11.
    - **Firewall UI Parity**: Implemented a `ufw status` reporter for NixOS to maintain a consistent user experience across all supported distributions.
    - **Declarative Persistence**: Firewall rules are now updated in `dplane-state.json` and applied automatically via the NixOS module.
- **Hardened ISO Build Architecture**
    - **De-recursive Flake**: Resolved infinite evaluation loops in `flake.nix` by decoupling the ISO build from system configurations.
    - **Explicit Image Targeting**: Transitioned to a `mkIso` pattern that passes the `targetSystem` as a concrete derivation for stable generation.
- **Offline-First Installer**
    - Removed network-dependent `nix run` calls from `nixos/install.sh`.
    - All required partitioning and TUI tools are now pre-baked into the ISO for reliable air-gapped installations.

### Fixed
- **NixOS 25.11 Compatibility**: General cleanup and removal of deprecated package references to ensure full compatibility with the latest Nixpkgs channel.
- **Evaluation Resilience**: Fixed a critical redundancy issue in `flake.nix` where system configurations were being evaluated multiple times during the ISO build.
- **Build Infrastructure Resilience**:
    - **Go Build Stabilization**: Resolved `runtime/cgo` and `go.mod` pathing errors by disabling CGO and correctly scoping the daemon build to the `daemon/` directory.
    - **First-Boot Authentication Bridge**: Implemented auto-seeding of the administrator account from the installer's TUI password, ensuring a seamless offline "First Boot" experience.
    - **Self-Contained Vendoring**: Transitioned to a fully-committed `vendor/` directory to eliminate CI dependencies on external Go proxies and avoid ephemeral `vendorHash` mismatches.
    - **Metadata Restoration**: Recovered the `VERSION` file and ensured consistent version injection across all build artifacts.

---

## v7.1.0 (2026-03-23) - "High Availability Nexus"

Upgrade from: v7.0.0 - Drop-in. `sudo bash install.sh --upgrade`
    - **Patroni & etcd Orchestration**: Native NixOS module for automated PostgreSQL consensus and failover.
    - **HAProxy Service Mesh**: Transparent traffic routing to the active cluster leader.
    - **Keepalived Virtual IP**: Automated floating IP migration for zero-downtime client access.
- **Guided HA Setup Wizard**
    - Interactive 5-step UI process for safe cluster arming and configuration.
    - Automated background NixOS reconfiguration during setup.
    - Pre-flight prerequisite verification for networking and quorum.
- **Intelligent Fencing (STONITH)**
    - Secure out-of-band power management via IPMI Redfish/IPMIspec.
    - Hardened execution whitelist with regex-based argument validation.
    - Zero-leak password handling via environment variable injection.
- **Continuous Storage Replication**
    - High-performance ZFS snapshot shipping for asynchronous Active-to-Standby data sync.
    - Real-time replication telemetry and bottleneck detection.
- **Security & Robustness Hardening**
    - **Mandatory Command Whitelisting**: Integrated structural security validation into the `cmdutil` execution layer.
    - **Failover State Protection**: Added a `fencingInProgress` flag to ensure atomic STONITH/Promotion sequences.
    - **Startup Split-Brain Guard**: Implemented Patroni health checks to block automatic ZFS imports on replica nodes.
    - **Job-Based Setup Wizard**: Updated the toggle HA process to leverage the jobs system for real-time progress feedback.

### Changed
- **NixOS Bridge**: Integrated HA enablement status into the declarative `dplane-state.json` fragment.
- **Cluster Monitoring**: Real-time visual topology tracking for quorum, node health, and Patroni roles.

---

## v7.0.0 (2026-03-22) - "PostgreSQL Ascension"

Upgrade from: v6.2.0 - **BREAKING CHANGE**. This release replaces SQLite with PostgreSQL. 
Manual database migration is required or start with a fresh install.

### Added
- **Architectural Shift: PostgreSQL Core**
    - Completely replaced SQLite with PostgreSQL 15+ as the primary metadata engine.
    - Improved concurrency and scalability for large-scale storage environments.
    - Standardized on `pgx` for high-performance PostgreSQL driver support with robust connection pooling.
- **CI/CD Pipeline v2**
    - Integrated PostgreSQL service containers into all automated test stages (`prepare`, `validate`, `convergence`, `integration`).
    - Implemented **Multi-Database Isolation** for fleet integration tests, enabling clean parallel node simulations on a single PG instance.
    - Unified release process with automated checksum generation and multi-architecture packaging.
    - Refined GHA metadata handling to use explicit outputs, resolving previous context access lint warnings.
- **Installer Enhancement**
    - Added `--db-dsn` support to `install.sh` for external PostgreSQL connectivity.
    - Automated systemd environment injection for secure database credential management.
    - Transitioned to native `postgresql-client` for database bootstrapping and maintenance.

### Changed
- **Database Layer**: Migrated all 40+ handlers to PostgreSQL-compatible SQL syntax ($1 placeholders, RETURNING clauses, etc.).
- **Global Search**: Ported SQLite FTS5 file search to a more robust PostgreSQL implementation.
- **Audit Logging**: Hardened audit trail persistence with PostgreSQL's strong consistency guarantees.

---

## v6.2.0 (2026-03-22) - "Cryptographic Sovereignty"

Upgrade from: v6.1.2 - Drop-in. `sudo bash install.sh --upgrade`

### Added
- **Automated Certificate Management (ACME)**
    - Integrated `go-acme/lego` for automated Let's Encrypt certificate issuance via HTTP-01 challenges.
    - **Hardened Background Job**: Moved ACME acquisition to a non-blocking background job with real-time progress tracking (Registering, Validating, Obtaining).
    - **Account Key Persistence**: ACME keys are now persisted to `/etc/dplaneos/acme_account.key`, ensuring identity reuse and avoiding Let's Encrypt rate limits.
    - **Pre-flight Proxy Verification**: Added a "Verify Proxy" diagnostic tool to ensure port 80/8080 proxying is correctly configured before starting the challenge.
    - **Automated NixOS Proxy**: The NixOS module now automatically configures Nginx to proxy `/.well-known/acme-challenge/` to the daemon.
    - Added support for manual certificate and private key imports via the new `ImportModal`.
    - Restored and hardened the self-signed certificate generation with SAN (Subject Alternative Name) support.
- **Real-time Replication Telemetry**
    - Implemented asynchronous progress tracking for ZFS replication jobs.
    - Backend now parses `zfs send -P` stderr in real-time to broadcast percentage, throughput, and ETA.
    - Updated `JobStatusBanner` in the Replication page with a live, high-fidelity progress bar.
- **Advanced ZFS Operations**
    - Implemented snapshot holds (`zfs hold`, `zfs release`) to protect critical snapshots from accidental deletion.
    - Added mirrored pool split (`zpool split`) support, allowing users to safely split mirrors into independent pools.
    - Added dedicated API endpoints and security whitelisting for all advanced ZFS management.
- **Production Hardening**
    - **ACME Auto-Renewal**: Added background expiry checking and automated renewal (30 days before expiration) via systemd timers.
    - **Nginx Challenge Automation**: Automated challenge proxy configuration for non-NixOS systems (idempotent injection and validation).
    - **NixOS Compatibility**: Secured installer path guards and fixed internal references for seamless NixOS coexistence.
    - **GitOps Consistency**: Standardized state persistence across all handlers using asynchronous Git hooks.
- **Session Control & Security**
    - Introduced a dedicated "Sessions" management tab in the Users & Groups page.
    - Users can now view all active web sessions, including IP addresses, device types (User-Agent), and last activity timestamps.
    - Added granular session revocation, allowing users to forcefully log out other devices.
    - Hardened the `sessionMiddleware` to support immediate invalidation of revoked sessions across all API endpoints.

### Fixed
- **NixOS Path Resilience**: Fixed a hardcoded `/usr/bin/curl` path in the system handlers, ensuring binary resolution follows the system `PATH` for NixOS compatibility.
- **API Route Registration**: Properly registered all new certificate and session management endpoints in the main daemon router.

## v6.1.2 (2026-03-22) - "NixOS Path Convergence"

Upgrade from: v6.1.1 - Drop-in. `sudo bash install.sh --upgrade`

### Added
- **Exhaustive NixOS Compatibility**
    - Completed the project-wide removal of hardcoded absolute paths (`/usr/bin/*`, `/bin/*`, `/sbin/*`) across all shell scripts, systemd unit templates, and internal handlers.
    - All system commands now rely on the system `PATH` for resolution, ensuring 100% compatibility with NixOS store paths while maintaining standard Linux support.
- **CI/CD Build Integrity**
    - Fixed the release pipeline trigger to ensure automated publishing of release tarballs on version tags.
    - Ensured consistent versioning across all build artifacts.

---

## v6.1.1 (2026-03-21) - "Real-time Monitoring Overhaul"

Upgrade from: v6.1.0 - Drop-in. `sudo bash install.sh --upgrade`

### Added
- **Systemic WebSocket Architecture**
    - Integrated real-time push notifications into the central `DispatchAlert` hub, enabling immediate UI toasts for Capacity Guardian and S.M.A.R.T. failures.
    - Added standardized `job.completed` and `job.failed` WebSocket broadcasts to the core `jobs` system, providing rich metadata (`job_id`, `job_type`, `success`, `message`).
- **ZFS Progress Overhaul**
    - Refactored ZFS resilver and scrub status parsing into a reusable `zfs` package, ensuring consistent telemetry across all callers.
    - Eliminated client-side polling for ZFS operations by pushing `zfs.resilver.progress` and `zfs.scrub.progress` events directly from the backend.
    - Upgraded the `BackgroundMonitor` to periodically scrape and broadcast live ZFS status.
- **NixOS Management Hardening**
    - Refactored the NixOS rebuild logic (`ApplyWithWatchdog`) into a non-blocking background job, preventing dashboard timeouts and providing step-by-step progress through the `jobs` API.
    - Integrated the watchdog lifecycle directly into the background job for reliable auto-rollback.
- **Monitoring & Replication Gaps**
    - Added real-time status broadcasts for replication schedule transitions (`replication.schedule_updated`).
    - Ensured all systemic alerts are non-blocking to prevent system delays during notification bursts.

---

## v6.1.0 (2026-03-21) - "VDEV Sentinel"

Upgrade from: v6.0.6 - Drop-in. `sudo bash install.sh --upgrade`

### Added
- **Extended VDEV Operations**: Full backend and UI support for `zpool attach` (mirroring), `zpool detach`, and `zpool replace`.
- **Hardware Topology Viewer**: Interactive indented tree view of ZFS pool structure (Mirrors, RAIDZ, Special VDEVs).
- **VDEV-Aware Pool Repair**: Updated the Pool Fixer Wizard to correctly identify and guide replacement of failed disks within any VDEV sub-group.
- **NixOS Native Scheduling**: Migrated ZFS scrubbing and snapshotting from legacy cron to native systemd timers for enterprise-grade NixOS compatibility.
- **Dynamic Timer Management**: Internal generator for transient systemd units that survive reboots and provide granular state tracking.

### Security
- **Whitelist Hardening**: Expanded execution whitelist to include `wipefs`, `labelclear`, and specific `zpool` subcommand variants.
- **Path Validation Enforcement**: Replaced all legacy string-based validation with unified `security.ValidateDevicePath` for `by-id` path safety.
- **Disk Wiping Safety**: Implemented real-time pool membership checks in the `WipeDisk` handler to prevent data loss on active storage members.

---

## v6.0.6 (2026-03-21) - "Hardened Core"

Upgrade from: v6.0.5 - Drop-in. `sudo bash install.sh --upgrade`

### Security
- **Execution Whitelist Hardening**: Improved command pattern validation for `zpool`, `zfs`, `ufw`, `ip route`, and `openssl`.
- **ZFS Property Safety**: Implemented strict allowlists for `zfs set` properties and validated `mountpoint`/`quota` values.
- **Path Traversal Defenses**: Enhanced `IsValidPath` with mandatory `filepath.Clean` normalization and explicit rejection of dot-slash patterns.
- **Binary Path Normalization**: Removed all hardcoded absolute paths (`/usr/bin/*`, `/bin/*`, `/usr/sbin/*`) from the entire `daemon/` codebase, ensuring 100% compatibility with NixOS and non-standard Linux distributions.
- **Whitelist Synchronization**: Aligned `chown`/`chmod` security patterns with file handler base paths to ensure UI consistency across all managed storage.

### Fixed
- **Storage Persistence**: Fixed a critical bug in `writeFileContent` (handlers/zfs_operations.go) where binary content was being ignored during network and system configuration writes.
- **Switch Optimizations**: Optimized state handling switches in `capacity_guardian.go` and `system_extended.go` for better performance and readability.

---

## v6.0.5 (2026-03-20) - "NixOS & GitOps Hardening"

Upgrade from: v6.0.4 - Drop-in. `sudo bash install.sh --upgrade`

### Added
- **Hardened GitOps Engine**: Implemented `git pull --rebase` before pushing to prevent sync conflicts and enforced Git identity (`user.name`/`user.email`) for all commits.
- **NixOS Path Normalization**: Removed all hardcoded absolute paths (`/usr/bin/*`, `/sbin/*`) across the system, enabling full compatibility with NixOS and non-standard distributions.
- **Resilience Guards**: Added automated existence checks for critical binaries (ZFS, Docker, Samba) with descriptive error reporting.
- **Audit Log Refactoring**: Migrated audit log rotation to the internal Go SQL driver, removing the external `sqlite3` dependency.
- **Asynchronous Persistence**: Refactored background commits to be non-blocking, ensuring the UI remains responsive during slow Git operations.

---

## v6.0.4 (2026-03-20) - "System-Wide CRUD Consistency"

Upgrade from: v6.0.3 - Drop-in. `sudo bash install.sh --upgrade`

### Added
- **System-Wide CRUD Enhancement**: Implemented/Hardened Create, Read, Update, and Delete (CRUD) operations across the entire platform.
  - **Storage**: Added "Edit" and "Delete" (with safety confirmation) for ZFS Datasets and Snapshot Schedules.
  - **Networking**: Added "Edit" for Firewall Rules and "Remove" for VLANs and Bonds.
  - **Services**: Implemented "Edit" for iSCSI Targets, Cloud Sync remotes, and Replication Schedules.
  - **Security**: Added full CRUD for RBAC Roles and confirmed Users/Groups consistency.
- **Unified GitOps**: Consolidated GitOps and Git Sync into a single hub with full CRUD for Repositories and Credentials.

---

## v6.0.3 (2026-03-20) - "System Sync Core"

Upgrade from: v6.0.2 - Drop-in. `sudo bash install.sh --upgrade`

### Fixed
- **Gap 7: System Sync Toggle**: Resolved a major GitOps gap where the `sync_system` toggle was being ignored. System settings (Hostname, Timezone, DNS, NTP, Firewall, Networking, and Samba) are now correctly filtered and committed to Git based on the User Interface selection.
- **State Serialization**: Implemented the missing `system:` block in the live-to-Git state generation engine.

---

## v6.0.2 (2026-03-20) - "Deterministic Integrity"

Upgrade from: v6.0.1 - Drop-in. `sudo bash install.sh --upgrade`

### Added
- **Hardened Deterministic Bootstrap**: Introduced the `-apply` flag for `dplaned`, enabling one-off GitOps reconciliation during initial system setup.
- **Compliance Tooling (`dplane`)**: Added a dedicated CLI symlink with `-test-serialization` and `-test-idempotency` flags for mathematical verification of system state.
- **Data Readiness Enforcement**: Stacks and workloads are now blocked from starting until dependent ZFS datasets are verified as mounted and ready.
- **Audit Chain Integrity API**: New endpoint `/api/system/audit/verify-chain` for real-time cryptographic verification of the audit log chain.
- **CI/CD Alignment**: Hardened the validation pipeline with automated enforcement of v6 invariants on every push.
- **Convergence Engine**: Introduced post-apply state verification that re-reads live system status to confirm the desired `state.yaml` configuration was successfully reached.

### Fixed
- **Gap 1: Pool Import Safety**: Switched from name-based to GUID-based `zpool import` to prevent accidental mis-imports on systems with overlapping pool names or renamed pools.
- **Gap 2: Ambiguous State Detection**: The GitOps engine now detects and blocks reconciliation if multiple pools or datasets with the same name are found, requiring manual intervention for safety.
- **Gap 3 & 6: Strict Mountpoint Verification**: Enhanced the data-readiness gate to verify not just mount status, but exact mountpath accuracy, preventing accidental data writes to the root partition if ZFS drifts.
- **Gap 4: Share-Dataset Cross-Validation**: Declarative SMB shares and NFS exports are now cross-referenced against managed ZFS datasets to ensure every share has a valid, managed backing mountpoint.
- **API Handler Robustness**: Added `HasAmbiguous` guards to the apply handler to prevent degenerate states from causing generic errors.
- **Convergence Visibility**: Enriched API responses with structured convergence metadata (`CONVERGED`, `DEGRADED`, etc.).
- **Plan Summaries**: Included `ambiguous_count` and `has_ambiguous` in the plan summary for enhanced operator visibility.
- **Schema Correction**: Fixed a mapping bug in pool GUID parsing from `state.yaml`.
- **Hardened Testing**: Added automated unit tests for GUID parsing and ambiguity detection, and implemented a comprehensive **Fleet & Install CI** pipeline in a single, gated `ci.yml` covering fresh installs, idempotency, and multi-node fleet simulations.
### Quality Gaps Addressed (v6.0.2 Polish)
- [x] **Environment Resiliency**: Added `.gitattributes` and automatic CRLF-to-LF conversion in `install.sh` to ensure script execution stability across all checkout environments.
- [x] **CI Consolidation**: Successfully merged `validate.yml`, `fleet-install.yml`, and `release.yml` into a single, comprehensive deployment pipeline.
- [x] **Diff Engine Halt**: Implemented early return in `ComputeDiff` for ambiguous states.
- [x] **Convergence Correctness**: Updated `ConvergenceCheck` to correctly include `ActionDelete` in drift calculations.
- [x] **CLI Automation**: Added `-diff` and `-convergence-check` flags to `dplaned` for CI/CD integration.
- **Audit Key Initialization**: Resolved an issue where the audit signing key was not correctly generated on fresh installs.
- **YAML Key Ordering**: Ensured deterministic serialization of the GitOps state to prevent false diffs.

---

## v6.0.1 (2026-03-19) : "Enforcement Mode"

(Skipped or superseded by v6.0.2)

---

## v6.0.0 (2026-03-17) : "Declarative Freedom"

Upgrade from: v5.3.5 - Drop-in. `sudo bash install.sh --upgrade`

### Added
- **Optional & Granular GitOps**
  - **Global Toggle**: GitOps functionality can now be entirely enabled or disabled via the UI, making it a non-essential control plane.
  - **Granular Sync Matrix**: Introduced selective synchronization for six key resource categories: Storage (ZFS), Data Access (SMB/NFS), Applications (Docker), Identity (Users/Groups), Protection (Replication), and System settings.
  - **GitHub Connect Wizard**: A premium 3-step onboarding flow for linking repositories and managing Personal Access Tokens (PAT) directly within the GitOps settings.
  - **Manual Sync Fallback**: Added a "Sync Now" button for instant on-demand reconciliation.
  - **Authenticated Git Operations**: Robust support for GitHub PAT and SSH keys using secure `GIT_ASKPASS` and `GIT_SSH_COMMAND`.
- **Audit Log Automation & Maintenance**
  - **Auto-Rotation**: Weekly background log purging based on user-defined retention settings.
  - **Space Reclamation**: Integrated `VACUUM` into both manual and scheduled rotations to reclaim SQLite disk space.
- **Enterprise Integration Hooks**
  - **License Management**: New "Enterprise License Key" field in System Settings as the gateway for premium features.
  - **Plugin Injection System**: Added hooks for dynamic injection of navigation, routes, and settings, enabling a true "Zero-Pollution" open-core architecture.

### Changed
- **"Zero-Pollution" Open-Core**: Migrated the Audit Log UI, routes, and navigation from the Community Edition to the Enterprise Compliance Engine (PRO). The core repository is now free of all proprietary UI traces.

---

## v5.3.5 (2026-03-16) : "API Auth Patch"

Upgrade from: v5.3.4 - Drop-in. `sudo bash install.sh --upgrade`

### Security

- **API Authentication**
  - Patched `sessionMiddleware` to support `Authorization: Bearer` tokens.
  - Enables secure programmatic access for sidecars and automation using long-lived API tokens.
  - Backported `ValidateAPITokenAndGetUser` to the security core.

---

## v5.3.4 (2026-03-16) : "Hardened Enterprise Deployment"

Upgrade from: v5.3.3 - Drop-in. `sudo bash install.sh --upgrade`

### Added

- **Enterprise Security Hardening**
  - **Environment-Based Auth**: Modified the installation process to store sensitive API tokens in a dedicated `EnvironmentFile` (`/etc/dplaneos/daemon.env`) with `0600` permissions.
  - **Systemd Integration**: Updated service definitions to use `EnvironmentFile` instead of command-line flags, preventing token leakage in process lists (`ps aux`).
- **Unified Versioning**: Synchronized version identifiers across CE and Enterprise suites for cleaner release tracking.

---

## v5.3.3 (2026-03-16) - "ZED Integration"

### Added

- **ZED Hook Integration**
  - Integrated ZFS Event Daemon (ZED) real-time events into the D-PlaneOS daemon via a Unix domain socket at `/run/dplaneos/dplaneos.sock`.
  - Bypasses the 30-second polling limitation of the daemon by feeding critical pool and VDEV events immediately to the UI and alert channels.
  - Replaced the standalone JSON file writing and Telegram alerting in the ZED hook script with a streamlined notification forwarder.
  - Automatically installed by `install.sh` on Debian/Ubuntu systems and fully declared in `nixos/module.nix` for NixOS systems.

---

## v5.3.2 (2026-03-15) - "Build Integrity & Maintenance"

Upgrade from: v5.3.1 - Drop-in. `sudo bash install.sh --upgrade`

### Added

- **Smarter UI Build Management**
  - Migrated static assets (`manifest.json`, `modules/`) to the source directory (`app-react/public/`). This enabled the use of Vite's `emptyOutDir` to automatically purge orphaned hashed files (like the stale `index-nABdU3XR.js`) during the build process.

### Fixed

- **Pool Operations Integrity**
  - Re-verified that `PoolOperations` (`zpool_clear`, `zpool_online`) and their corresponding whitelisted commands are fully present in the release. Addresses reports of accidental feature removal.

---

## v5.3.1 (2026-03-15) - "CI & Panic Resilience"

Upgrade from: v5.3.0 - Drop-in. `sudo bash install.sh --upgrade`

### Fixed

- **Critical - GetDiskStatus runtime panic**
  - Resolved `slice bounds out of range` panic in `GetDiskStatus` caused by unsafe parsing of `lsblk` output on loopback devices (commonly seen in CI environments). Implemented field-count safety checks.
- **ACL Management**
  - **Route Alignment**: Fixed a mismatch where the daemon expected `/api/acl/get` while tests/frontend might use `/api/system/acl`. Both are now supported via aliasing.
  - **Diagnostic Visibility**: Added `ACL:` log prefixes to important operations in `GetACL` and `SetACL` to ensure system-level failures (like missing `getfacl`) are visible in daemon logs.
  - **Bulk API**: Refactored `SetACL` to support the multi-line full ACL format sent by the frontend, ensuring "Apply" works predictably while maintaining compatibility with single-entry CI tests.

### CI/CD

- **Environment Hardening**: Added `acl` and `ipmitool` to standard CI dependencies.
- **ZFS Integration**: Explicitly enabled `acltype=posixacl` on the test pool to match production-grade ZFS configurations.

---

## v5.3.0 (2026-03-15) - "Storage & Security Integrity"

Upgrade from: v5.2.3 - Drop-in. `sudo bash install.sh --upgrade`

### Added

- **Storage Management Core**
  - **StorageSummary component**: Real-time unified capacity telemetry (Total/Used/Free) across all ZFS pools.
  - **Integrated Pool Lifecycle**: Full support for creating, expanding, and destroying pools directly from `PoolsPage.tsx`.
  - **Data Safety**: Destructive operations (pool destruction) now require explicit "type-to-confirm" validation.
- **Enhanced Modal UX**
  - **Universal Close Mechanisms**: Added high-visibility "X" buttons to modal headers and standardized "Cancel" buttons in all footers.
  - **Determinism Hardening**: Aligned the Enterprise sidecar with the new v6.0.2 "Deterministic Integrity" invariants, ensuring all compliance reports reflect accurate convergence state.
- **CI Gating**: Integrated the consolidated `D-PlaneOS` CI pipeline as a mandatory release gate for all production builds.
  - **Portal-based Rendering**: Migrated the entire modal architecture to React Portals, rendering into `#modal-root` to ensure modals are always top-level and immune to parent stacking context glitches.

### Fixed

- **Critical Security - Path Traversal Vulnerabilities**
  - **IsValidPath**: Fixed a bypass where `./` could be used to traverse directories. Added explicit blocking for dot-slash patterns.
  - **IsSafeFilename**: Corrected a logical error (converted `&&` to `||`) that allowed filenames with path separators if they didn't contain both `/` and `\` simultaneously.
- **UI/UX Refinement**
  - Standardized modal centering and backdrop-blur filters for a premium "glass" aesthetic.
  - Resolved layout issues in `PoolsPage.tsx` where long disk lists could clip modal footers.

### Refactored

- **API Integration**: Replaced legacy mock data structures in the storage layer with real API contracts, preparing for full ZFS integration.

---

## v5.2.3 (2026-03-15) - "More UI Polish"

### Added

- Global Design Tokens (src/index.css)Rich HSL Color Palette: Refactored the base colors (--primary, --success, --warning, --error) from flat hex codes to a deeply saturated and highly controllable HSL spectrum.
Deep Mesh Background: Upgraded the root background from a basic 2-color radial gradient to a multi-layered, dramatic pseudo-mesh gradient that reacts beautifully underneath glassmorphic elements.
Shadow and Depth Overhaul: Implemented a new multi-layered robust shadow system (--shadow-sm, --shadow-md, --shadow-lg, and an all-new --shadow-glow variable).
- The Glassmorphism Aesthetic
Introduced the --blur-glass: blur(20px) variable.
Applied backdrop-filter alongside semi-transparent deep-dark backgrounds (hsla(var(--hue-bg), 18%, 10%, 0.5)) to:
Top navigation bar (TopBar.tsx) Left navigation menu (Sidebar.tsx)
Dashboard component containers (.card, .alert)
Input fields, dropdown menus, tooltips, and popovers.

### Fixed

- Codebase-Wide Component Normalization
Audited the entire src/pages directory (over 40+ route components).
Discovered extensive usage of hardcoded inline styles (style={{ background: 'var(--bg-card)', border: '1px solid var(--border)' }}) that bypassed the new glass tokens.
- Deployed a series of automated AST-like regex replacements across thousands of lines of TypeScript to dynamically strip the static borders/backgrounds and inject the global className="card". This ensures every single view in D-PlaneOS inherits the interactive glassmorphism updates uniformly.
- Micro-Animations & Dynamic Feedback
Buttons (.btn): Added inner light-catching borders (box-shadow: inset 0 1px 0 hsla(0,0%,100%,0.3)), scale compression on active clicks (transform: scale(0.97)), and heavy glowing hover shadows.
Inputs & Tabs: Introduced a var(--transition-bounce) for spring-like fluidity when moving between tab states or focusing on input bars.
- Dashboard Interactive Cards (.card.interactive): Upgraded dashboard metrics and section cards to smoothly translate Y: -2px with a heightened drop-shadow.

## v5.2.2 (2026-03-14) - "UI Polish"

Upgrade from: v5.2.1 - Drop-in. `sudo bash install.sh --upgrade`

### Added

- **Custom Tooltip component** - new styled floating tooltip component. Supports positioning (top/bottom/left/right), delay options, and custom styling.
- **Popover component** - hover cards for showing contextual info.
- **Enhanced ConfirmDialog** - supports context info display and typed confirmation for destructive operations. Auto-focuses input, blocks confirm until text matches.

### UI Improvements

- Modal entrance/exit animations - smoother transitions using scale and fade effects
- Consistent tooltip/popover styling matching design system
- All button `title=` attributes replaced with custom Tooltip component

---

## v5.2.1 (2026-03-13) - "Complete Consistency"

Upgrade from: v5.1.2 - Drop-in. `sudo bash install.sh --upgrade`

### Fixed

**Critical - silent database errors on user/group operations**

- **User update/delete silently failed** - `users_groups.go` was ignoring DB errors on several operations. Fixed: all DB errors are now checked and returned to the UI properly.

**Reliability - database connection pooling**

- **SMTP alerting opened new DB connection per request** - `alerting_smtp.go` was calling `sql.Open()` on every HTTP request. Fixed: refactored to use a shared pooled `*sql.DB` via `AlertingHandler` struct.

### Refactored

- **HTTP error response consistency** - replaced all ~195 `http.Error` calls with `respondError`/`respondErrorSimple` for consistent JSON error format throughout the API.
- **Config package** - centralized `/var/lib/dplaneos/*` paths in `internal/config/paths.go`. Migrated enterprise_hardening.go, audit_verify.go, docker_icons.go, docker_stacks.go, system_extended.go.

### Tests Added

- **validateRepoURL** - tests for blocking dangerous URL schemes (ext://, file://, fd://)
- **Path traversal prevention** - IsValidPath and IsSafeFilename functions with tests
- **Auth handlers** - respondError JSON format tests

---

## v5.2.0 (2026-03-13) - "Reliability & Consistency"

Upgrade from: v5.1.2 - Drop-in. `sudo bash install.sh --upgrade`

### Fixed

**Critical - silent database errors on user/group operations**

- **User update/delete silently failed** - `users_groups.go` was ignoring DB errors on several operations. Fixed: all DB errors are now checked and returned to the UI properly. Previously, operations could fail without the user knowing.

**Reliability - database connection pooling**

- **SMTP alerting opened new DB connection per request** - `alerting_smtp.go` was calling `sql.Open()` on every HTTP request, creating connection overhead. Fixed: refactored to use a shared pooled `*sql.DB` via `AlertingHandler` struct, consistent with all other handlers.

### Refactored

- **HTTP error response consistency** - replaced ~160 `http.Error` calls with `respondError`/`respondErrorSimple` for consistent JSON error format throughout the API. Now returns `{"success":false,"error":"message"}` on all error paths instead of plain text.
- **Config package** - added centralized path constants in `internal/config/paths.go` for `/var/lib/dplaneos/*` paths.

---

## v5.1.3 (2026-03-13) - "Reliability Fixes"

Upgrade from: v5.1.2 - Drop-in. `sudo bash install.sh --upgrade`

### Fixed

**Critical - silent database errors on user/group operations**

- **User update/delete silently failed** - `users_groups.go` was ignoring DB errors on several operations. Fixed: all DB errors are now checked and returned to the UI properly. Previously, operations could fail without the user knowing.

**Reliability - database connection pooling**

- **SMTP alerting opened new DB connection per request** - `alerting_smtp.go` was calling `sql.Open()` on every HTTP request, creating connection overhead. Fixed: refactored to use a shared pooled `*sql.DB` via `AlertingHandler` struct, consistent with all other handlers.

## v5.1.2 (2026-03-13) - "Auth Integrity"

Upgrade from: v5.1.1 - Drop-in. `sudo bash install.sh --upgrade`

### Added

- **Docker template library** - one-click deployment of pre-configured application stacks (Home Assistant, Plex, Nextcloud, etc.) via `GET /api/docker/templates` and `POST /api/docker/templates/deploy`. Templates can be git repos or built-in. Deployed as independent Compose stacks with atomic rollback on failure.

### Fixed

**Critical - LDAP-sourced users could not log in**

- **LDAP users were permanently locked out of the UI** - the login handler only performed bcrypt comparison against `password_hash`. LDAP-synced accounts have an intentionally empty `password_hash` (they authenticate via directory bind). Any login attempt by an LDAP account failed silently with "Invalid credentials". Fixed: `Login()` now reads the `source` column; when `source='ldap'` it calls `ldapAuthenticate()` which loads LDAP config from the database and performs a real-time bind to verify credentials. Local accounts and user ID 1 remain on bcrypt regardless of LDAP state.
- **LDAP circuit breaker** - when the directory server is unreachable, each login attempt would block for the full TCP timeout. Added circuit breaker: after 3 consecutive connection failures, LDAP authentication fails immediately for 30 seconds rather than waiting for TCP timeouts. Connection-level errors (vs. credential errors) are tracked separately from authentication failures.

**UI - eliminated all browser-native dialogs**

- **`window.confirm()` used in 13 places across 9 pages** - browser-native confirm dialogs are visually inconsistent and cannot be styled. Replaced with `useConfirm()` hook that renders inline modal dialogs using the existing design system. Each dialog has a context-appropriate title, descriptive message, and correct danger/warning variant. Pages updated: AlertsPage, CloudSyncPage, FilesPage, GitSyncPage, HAPage, ISCSIPage, SecurityPage, SettingsPage, UsersPage.

**Code quality**

- **Syncthing conflict files in vendor** - 1,260 `.sync-conflict-*` files in `daemon/vendor/` caused duplicate symbol build failures locally. Removed.
- **Duplicate inline style definitions** - `btnPrimary`, `btnGhost`, `btnDanger`, `inputStyle`, `cardStyle` were independently defined across 15–20 page files. Pages now use global CSS classes from `index.css`.

### Documentation

- **ADMIN-GUIDE.md** - replaced incorrect "JIT provisioning on first login" with accurate model: sync populates local DB, login performs real-time LDAP bind for directory accounts, local accounts always use bcrypt. Added note that D-PlaneOS uses LDAP for web UI login (unlike TrueNAS Scale / Unraid which use it for SMB auth only).
- **README.md** - corrected Identity and Auth architecture descriptions.

---

## v5.1.1 (2026-03-10) - "System Audit"

Upgrade from: v5.1.0 - Drop-in. `sudo bash install.sh --upgrade`

### Fixed

**Critical - broken on first interaction**

- **Docker page: all containers showed blank names, images, and state** - `containerToMap` emitted PascalCase Docker SDK keys (`Id`, `Image`, `State`). Frontend expected lowercase. Fixed: added lowercase aliases. Ports now also include `host_port`/`container_port`/`protocol`.
- **ZFS pool capacity bars always 0%** - `ListPools` fetched only `name,size,alloc,free,health`, never `cap`. Fixed: added `cap,health` columns. Also removed invalid `type` property (not a `zpool list` column - caused `exit status 2` and `success:false` on every `GET /api/zfs/pools` call, confirmed in CI).
- **ZFS dataset quota column always empty** - `ListDatasets` fetched `refer` instead of `quota`. Fixed.
- **Firewall rules page crashed on load** - `GetStatus` returned `rules` as raw `ufw status numbered` text. Frontend called `.map()` on a string - runtime crash. Fixed: new `parseUFWRules()` returns structured `[]map`.
- **Setup wizard failed on fresh DB** - `HandleStatus` and `HandleSetupAdmin` both `SELECT` from `system_config` before creating the table. Fixed: `CREATE TABLE IF NOT EXISTS` before any query.
- **`must_change_password` never acted on** - backend sets this flag on the auto-generated admin account; login page never checked it and never redirected. Fixed: redirects to `/security` after login when flag is set.

**High - specific features silently broken**

- **SMB share create/edit always used defaults** - `Share` interface used `readonly`/`guestok`/`browseable`; backend returns/expects `read_only`/`guest_ok`/`browsable`. Every create ignored the user's checkbox selections. Fixed: aligned field names throughout `SharesPage.tsx`.
- **File manager writes blocked by systemd `ProtectSystem=strict`** - `ReadWritePaths` didn't include `/mnt`, `/tank`, `/data`, `/media`, `/etc/samba`, `/etc/exports`, `/etc/iscsi`, `/etc/ssh`, `/tmp`, `/home`. Every file write returned permission denied. Fixed in `dplaned.service` and `install.sh` inline unit.
- **Log streams, WebSocket, and large downloads cut at 30s** - `WriteTimeout: 30s` on the HTTP server. SSE, both WebSocket endpoints, and file downloads were terminated. Fixed: `WriteTimeout: 0`.
- **`chmod` always 400** - frontend sends `{ mode }`, backend expected `{ permissions }`. Fixed: both accepted.
- **File rename always 400** - frontend sends `{ new_name }` (filename only), backend expected `{ new_path }` (full path). Fixed.
- **Chunked upload wrote empty files** - frontend sends field `chunk`, backend read `chunkIndex`. Every chunk 0 truncated with no data. Fixed.
- **Setup wizard, HA heartbeat, udev disk events, and Prometheus blocked by session middleware** - no exemptions for these legitimately public routes. Fixed: all five paths added to bypass list.

**Medium**

- **Install phase numbering collision** - two phases both labelled `8`. Renumbered to clean 0–13 sequence.
- **`/etc/exports` not in `ReadWritePaths`** - NFS export writes would fail. Added.
- **File manager: four missing features added** - inline text editor (Ctrl+S, dirty tracking, 2 MB guard), download button per row, drag-and-drop upload with chunked upload, multi-select with checkbox column, quick-access bookmark sidebar.
- **Login `must_change_password` redirect** - added to `LoginPage.tsx`.

### Stats

| What | Before | After |
|------|--------|-------|
| Docker page container display | All blank | Correct |
| ZFS pool capacity bar | Always 0% | Correct |
| Firewall rules table | JS crash | Structured table |
| SMB share settings honoured | Never | Always |
| File manager features | 4 missing | Complete |
| CI: ZFS pools test | ✗ (exit 2) | ✓ |

---

## v5.1.0 (2026-03-10) - "Template Library"

Upgrade from: v5.0.0 - Drop-in. `sudo bash install.sh --upgrade`

### Added

**Multi-Stack Template System**

Templates are Git repositories where each sub-directory containing a `docker-compose.yml` is an independently deployed stack. Templates may also include:
- `template.json` - name, description, icon, ordered stack list, user-configurable variables
- `dplane-requirements.json` - ZFS datasets to create and firewall ports to open before deployment

**Backend (`daemon/internal/handlers/docker_templates.go`):**
- `GET /api/docker/templates` - built-in template catalogue (3 templates shipped: *arr Media Suite, Monitoring Suite, Home Automation)
- `GET /api/docker/templates/installed` - all deployed stacks grouped by `template_id`; standalone stacks (no template) grouped under `__standalone__`
- `POST /api/docker/templates/deploy` - clones template Git repo, processes `dplane-requirements.json` (creates ZFS datasets, logs required firewall ports), creates shared Docker network if specified, deploys each sub-stack, substitutes `${VAR}` placeholders in compose/env files, writes `.dplane-template` JSON marker in each stack directory
- Variable substitution: `${KEY}` placeholders in `docker-compose.yml` and `.env` are replaced with user-supplied values from the deploy request
- ZFS-aware: `dplane-requirements.json` datasets created with `zfs create -p` before stacks start; quota and mountpoint supported

**`daemon/internal/handlers/docker_stacks.go`:**
- `StackInfo` gains `template_id` and `template_name` fields
- `ListStacks` reads `.dplane-template` marker from each stack directory

**`daemon/cmd/dplaned/main.go`:**
- Stack CRUD routes registered (were missing - `DeployStack`, `GetStackYAML`, `UpdateStackYAML`, `DeleteStack`, `StackAction`, `ConvertDockerRun`)
- 3 template routes registered

**`daemon/internal/jobs/jobs.go`:**
- `Job` and `JobSnapshot` gain `Logs []string` field
- `Job.Log(line string)` method: appends a progress line under mutex; visible to any caller polling `GET /api/jobs/{id}`. Long-running jobs (template deploy, ZFS send, apt upgrade) now surface step-by-step progress.

**Frontend (`app-react/src/pages/ModulesPage.tsx` - rewrite):**
- **Installed tab**: template groups shown as collapsible cards with aggregate `N/M running` badge and template icon. Each group expands to compact per-stack cards. Standalone stacks shown as full cards below.
- **Template Catalogue tab**: grid of available templates with icon, description, stack list, and tags. "Deploy" opens a variable-input modal.
- **TemplateDeployModal**: renders each `TemplateVariable` as a labelled input (type=password for `secret: true`). Required fields validated before submit.
- **StackCard**: compact mode (inside group) and full mode (standalone). Shows per-service status dots. Restart/Stop/Start actions with job progress inline.
- **TemplateGroupCard**: collapsible. Running count badge colour: green (all running), amber (partial), grey (stopped).
- All existing `ContainerIcon`, `dplaneos.icon` label resolution, icon map, and port link behaviour fully preserved.

### Stats

| What | Before | After |
|------|--------|-------|
| Template deployment | Manual git clone + per-stack deploy | One-click with variable prompts |
| Stack grouping in UI | Flat list | Grouped by template with aggregate status |
| Job progress visibility | Status only (running/done/failed) | Step-by-step log lines |
| ZFS dataset provisioning | Manual | Automatic from `dplane-requirements.json` |
| Stack routes registered | Missing from main.go | Fully registered |

---

## v5.0.0 (2026-03-10) - "Solid State"

Upgrade from: v4.3.2 - **Breaking change for NixOS users only** (run `setup-nixos.sh` once after upgrade).

### Architectural Pivot: JSON-to-Nix Bridge

Previous versions used "The Surgeon": the Go daemon built raw Nix syntax strings in Go templates and wrote them directly to `dplane-generated.nix`. Any special character in a user-supplied value (apostrophe in a hostname, backslash in an `extraGlobalConfig`, multiline string) could produce a `.nix` file that fails `nix-instantiate --parse`, silently breaking `nixos-rebuild`.

v5.0 replaces this with the **JSON-to-Nix Bridge**:

- Daemon writes one file: `/var/lib/dplaneos/dplane-state.json` (pure JSON via `encoding/json`).
- `nixos/dplane-generated.nix` is now **static** - installed once by `setup-nixos.sh`, never modified by the daemon. It reads the JSON at eval time via `builtins.fromJSON` and maps keys to NixOS module options with `s.key or default` guards.
- Zero dynamic Nix syntax generated anywhere. Zero Surgeon. Zero injection risk.

### Changed

- **`daemon/internal/nixwriter/writer.go`**: Complete rewrite. Removed all string-template stanza builders. New `DPlaneState` struct + atomic JSON write. Same `Set*()` caller API - no handler changes required. `validateIP` now uses `net.ParseIP` (was a lax character-range check).
- **`nixos/dplane-generated.nix`**: New static bridge file. Reads `/var/lib/dplaneos/dplane-state.json` at eval time. `builtins.pathExists` guard for first-boot safety. Nix helper functions map JSON maps to correct `systemd.network` attrset shapes.
- **`nixos/setup-nixos.sh`**: Installs bridge file, seeds empty `{}` state file, auto-adds import to `configuration.nix`.
- **`nixos/configuration.nix`**: Added `imports = [ ./dplane-generated.nix ./modules/samba.nix ]`.
- **`nixos/flake.nix`**: Added bridge and samba module to both x86_64 and aarch64 module lists.
- **`nixos/impermanence.nix`**: Added `dplane-state.json` to persisted files - without this, all UI settings revert on every reboot on the appliance build.

### Migration (NixOS)

```bash
git pull
sudo bash nixos/setup-nixos.sh   # installs bridge, seeds state file
sudo nixos-rebuild switch --flake nixos#dplaneos
```

Re-apply any network/samba settings via the web UI after upgrading - the daemon writes them to the JSON file and the next rebuild picks them up.

---

## v4.3.2 (2026-03-10) - "WebSocket & API Wiring"

Upgrade from: v4.3.1 - Drop-in upgrade via `sudo bash install.sh --upgrade`

### Fixed

- **Pool health WS events never reached the UI:** `broadcastPoolHealthChanged` in
  `disk_event_handler.go` broadcast the event as `"poolHealthChanged"` (camelCase)
  but `ws.ts` switch handled `'pool_health_change'` (snake_case). Every hot-swap
  and pool recovery event was silently dropped. Event name corrected to
  `"pool_health_change"` on the daemon side. `PoolsPage`, `DashboardPage`, and
  `HardwarePage` now receive live pool health push as intended.
  (`daemon/internal/handlers/disk_event_handler.go:407`)

- **`diskAdded` / `diskRemoved` events broadcast but never routed in frontend:**
  The daemon correctly broadcasts `"diskAdded"` and `"diskRemoved"` on hot-swap.
  `ws.ts`'s `EventMap` declared `diskAdded` and `diskRemoved` subscribers, but the
  `onmessage` switch had no `case` for either string - both events were silently
  dropped. Cases added. Additionally, each event now also emits `hardwareEvent`
  with the action embedded, so `HardwarePage`'s existing `wsOn('hardwareEvent', ...)`
  subscription fires correctly without any page changes.
  (`app-react/src/stores/ws.ts`)

- **Scrub and resilver WS events never broadcast by daemon:** `ws.ts` handled
  `scrub_started`, `scrub_completed`, `resilver_started`, `resilver_progress`, and
  `resilver_completed` - but the daemon never called `Broadcast` for any of these.
  `StartScrub` now broadcasts `scrub_started`; `StopScrub` broadcasts `scrub_completed`.
  `ReplaceDisk` job broadcasts `resilver_started` at job start and `resilver_completed`
  (with success/failure) at job end. `PoolsPage` live-refresh subscriptions now work.
  (`daemon/internal/handlers/zfs_operations.go`)

- **`gitops.drift` event broadcast by daemon but unhandled in frontend:** The GitOps
  drift detector broadcasts `"gitops.drift"` whenever declarative state diverges from
  runtime. `ws.ts` had no `case` for it and no `EventMap` entry. Added `gitopsDrift`
  to `EventMap` and the switch. (`app-react/src/stores/ws.ts`)

- **`mount_health_<pool>` events unreachable in frontend:** The background mount monitor
  broadcasts per-pool events like `"mount_health_tank"`. `ws.ts` declared `mountError`
  in `EventMap` but had no switch handler. Added a `default` branch that matches any
  `msg.type.startsWith('mount_health_')` and emits to `mountError` subscribers.
  (`app-react/src/stores/ws.ts`)

- **`DELETE /api/shares` unregistered - share deletion always returned 405:**
  `SharesPage` calls `DELETE /api/shares` with `{ name }` in the body. The route was
  only registered for `GET` and `POST`. Added `DELETE` registration in `main.go` and a
  new `deleteShareByName` method on `ShareCRUDHandler` that looks up the share by name,
  deletes it, and regenerates `smb.conf`. The existing `deleteShare` (used by POST
  action-dispatch with an `id`) is unchanged.
  (`daemon/cmd/dplaned/main.go:587`, `daemon/internal/handlers/shares_crud.go`)

- **`GET/POST /api/system/tuning` handler implemented but route never registered:**
  `HandleSystemSettings` (ARC limit, swappiness, inotify/memory/iowait thresholds) was
  fully implemented in `system_settings.go` and documented in the CHANGELOG since v4.1.2
  but the route was never added to `main.go`. Registered at
  `GET /api/system/tuning` and `POST /api/system/tuning`.
  (`daemon/cmd/dplaned/main.go`)

- **Rate limiter used `r.RemoteAddr` - all traffic from a reverse proxy shared one bucket:**
  When the daemon runs behind nginx (standard production setup), every request arrives
  with `RemoteAddr = 127.0.0.1`. All users shared a single rate-limit bucket, so a
  single active user could exhaust the 100 req/min limit for everyone. The limiter
  now uses a new `realIP()` helper: for direct connections it trusts `RemoteAddr`; for
  loopback connections it falls back to `X-Real-IP` then `X-Forwarded-For` (safe because
  only a trusted local proxy can set these headers on loopback).
  (`daemon/cmd/dplaned/main.go`)

### Stats

| What | Before | After |
|------|--------|-------|
| Pool health WS push | Silently dropped | Live |
| Hot-swap disk WS push | Silently dropped | Live |
| Scrub WS events | Never emitted | Emitted on start/stop |
| Resilver WS events | Never emitted | Emitted on replace job start/end |
| `gitops.drift` WS event | Unhandled | Routed to `gitopsDrift` subscribers |
| `mountError` WS event | Unhandled | Routed via `mount_health_` prefix match |
| Share deletion (DELETE) | 405 Method Not Allowed | Works |
| `/api/system/tuning` | 404 | Registered |
| Rate limiter (behind proxy) | 1 bucket for all users | Per-client IP |

---

## v4.3.1 (2026-03-09) - "Icon System Fixes"

Upgrade from: v4.3.0 - Drop-in upgrade via `sudo bash install.sh --upgrade`

### Fixed

- **`Stack` interface fields never populated (`running_containers`, `total_containers`, `total_ports` always `undefined`):**
  `groupContainersByStack` in `docker.go` emitted only `name`, `containers`, and `count`. The frontend
  `ContainersTab` reads `stack.running_containers` and `stack.total_containers` to render the "N/M running"
  badge in every stack header - these were always `undefined`, rendering as `undefined/undefined running`.
  `groupContainersByStack` now iterates the original `dockerclient.Container` slice to compute all three
  fields before serialising. (`daemon/internal/handlers/docker.go`)

- **`dplaneos.icon` label silently ignored for all stack cards in ModulesPage:**
  `StackCard` passed `image={stack.name}` to `ContainerIcon` but never passed the `labels` prop, so the
  `dplaneos.icon` resolution path (priority 1) was permanently skipped for every module card even when the
  label was set. `ComposeStack` type gains an optional `labels` field; `StackCard` now forwards it.
  (`app-react/src/pages/ModulesPage.tsx`)

- **`IconMapEntry` type duplicated in three files - structural divergence risk:**
  `ContainerIcon.tsx`, `DockerPage.tsx`, and `ModulesPage.tsx` each declared their own `interface IconMapEntry`
  and `interface IconMapResponse`. These are now centralised in `app-react/src/lib/iconTypes.ts` and imported
  everywhere. (`app-react/src/lib/iconTypes.ts`, all three consumers)

- **Dead-code redundancy in `resolveIcon` image matching:**
  `ContainerIcon.tsx` called `nameLower(namePart).includes(entry.match) || nameLower(imageLower).includes(entry.match)`.
  `namePart` is a substring of `imageLower`, so the first condition is always a strict subset of the second -
  it could never be true when the second was false. The `nameLower()` wrapper was also a no-op (both inputs
  were already lowercased). Both simplified to a single `imageLower.includes(entry.match)` check.
  (`app-react/src/components/ui/ContainerIcon.tsx`)

- **`.jpg`, `.jpeg`, `.gif` accepted by frontend but missing from daemon MIME fallback:**
  `ContainerIcon.tsx`'s `IMAGE_EXTS` array includes `.jpg`, `.jpeg`, and `.gif`, so users can set
  `dplaneos.icon: mylogo.jpg`. The daemon's `HandleCustomIconFile` MIME fallback `switch` only covered
  `.svg`, `.png`, `.webp` - on minimal Linux systems without `/etc/mime.types` the file would be served
  as `application/octet-stream`, preventing browser rendering. Added `case ".jpg", ".jpeg": "image/jpeg"`
  and `case ".gif": "image/gif"` to the fallback switch.
  (`daemon/internal/handlers/docker_icons.go`)

- **Route ordering dependency in `main.go` undocumented:**
  `GET /api/assets/custom-icons/list` must be registered before the `PathPrefix` catch-all or gorilla/mux
  would route list requests to the file handler (returning 404). The ordering dependency is now documented
  with an explicit comment. (`daemon/cmd/dplaned/main.go`)

- **`custom_icons/` directory behind `chmod 700` parent - files inaccessible to non-root:**
  `install.sh` set `chmod 700 /var/lib/dplaneos` but never set explicit permissions on the
  `custom_icons/` subdirectory. Because the parent had `700`, no non-root process could traverse
  into it even if the subdirectory itself had permissive permissions. `custom_icons/` is now
  explicitly set to `root:root 755` so nginx (if configured as a static server) and other
  authorised processes can read icon files. (`install.sh`)

### Added

- **`app-react/src/lib/iconTypes.ts`:** New shared module exporting `IconMapEntry` and `IconMapResponse`.
  Single source of truth for icon map types across all frontend consumers.

- **`dplaneos.icon` label help tooltip in Docker containers table:**
  An `ⓘ` icon now appears next to the "Container" column header in the containers table. Hovering it
  shows a tooltip explaining the three supported `dplaneos.icon` label value formats (Material Symbol
  name, local icon filename, remote URL) and the custom icons directory path.
  (`app-react/src/pages/DockerPage.tsx`)

### Stats

| What | Before | After |
|------|--------|-------|
| `running_containers` in stack header | always `undefined` | correct live count |
| `dplaneos.icon` label honoured in ModulesPage | never | always |
| `IconMapEntry` declaration sites | 3 | 1 (shared) |
| MIME types with reliable fallback | 3 (svg/png/webp) | 6 (+jpg/jpeg/gif) |
| User-facing `dplaneos.icon` documentation | none | tooltip in Docker page |

---

## v4.3.0 (2026-03-09) - "Automation"

Upgrade from: v4.2.0 - Drop-in upgrade via `sudo bash install.sh --upgrade`

### Fixed

- **DEGRADED pools were never alerted on:** `pool_heartbeat.go` only caught
  SUSPENDED/UNAVAIL. Now detects DEGRADED via `zpool list -H -o name,health`
  every 30 seconds and fires a WARNING-level alert through all channels.
  Per-pool per-event de-duplication prevents spam; clears when pool recovers.

- **All alert channels were dead code:** `SendWebhookAlert`, `SendSMTPAlert`,
  and Telegram were defined but had zero callers for pool/disk/capacity events.
  New `alert_dispatch.go` provides `DispatchAlert(level, event, resource, msg)`
  as a single call site. All subsystems now route through it.

- **Webhook body templates were ignored:** UI allowed template variables but
  backend sent fixed JSON regardless. Now rendered via `strings.NewReplacer`.
  Custom `Content-Type` header also honoured.

- **`ReplaceDisk` never returned a `job_id`:** Now runs async via job queue;
  `job_id` returned immediately so UI can poll progress.

- **Resilver progress was unparsed raw text:** New `HandleResilverStatus`
  (`GET /api/zfs/resilver/status`) parses `percent_done`, `bytes_done`,
  `eta`, `errors`, `completed`. PoolsPage shows live progress bar with ETA.

- **Snapshot cron written to wrong directory:** Fixed from `ConfigDir/cron-snapshots`
  to `/etc/cron.d/dplaneos-snapshots`.

- **SMART prediction logic was dead code:** `TranslateSMARTAttribute()` now
  called by `GET /api/zfs/smart/predict` and a 6-hour background monitor
  that fires `DispatchAlert` on warning/critical predictions.

### Added

- **Central alert dispatch** (`alert_dispatch.go`): single `DispatchAlert(level,
  event, resource, msg)` routes to webhook + SMTP + Telegram. All subsystems use it.

- **Capacity alerts wired:** WARNING (≥80%), CRITICAL (≥90%), EMERGENCY (≥95%)
  with per-pool de-duplication.

- **Automatic disk replacement suggestion:** On hot-swap disk arrival, daemon
  cross-references faulted vdevs. Broadcasts `diskReplacementAvailable` WS event.
  HardwarePage auto-opens Replace modal with suggestion pre-populated.

- **Scrub schedule UI in PoolsPage:** Per-pool schedule modal (daily/weekly/monthly).

- **Replication schedules** (`GET/POST/DELETE /api/replication/schedules`):
  hourly/daily/weekly/manual intervals plus `trigger_on_snapshot` mode.
  ReplicationPage gains a Schedules tab.

- **Post-snapshot replication hook** (`POST /api/zfs/snapshots/cron-hook`):
  Cron jobs call this endpoint enabling Go-side hooks - snapshot, retain, replicate.

- **Time-based snapshot retention:** `retention_days` field on schedules.

- **Dataset search** (`GET /api/zfs/datasets/search?q=<query>`): PoolsPage
  live filter bar with match count and `pool:` prefix support.

- **`GET /api/zfs/resilver/status`**: Parsed resilver progress.

### Stats

| Metric | Before | After |
|--------|--------|-------|
| Alert event constants with zero callers | 8 | 0 |
| DEGRADED pool detection | None | 30 s heartbeat |
| SMART prediction calls | 0 | Background every 6 h |
| Replication triggers | Manual only | Scheduled + post-snapshot |
| Dataset search/filter | None | Live filter + API |
| Resilver progress | Raw string | Parsed %, ETA, bytes |

---

## v4.2.0 (2026-03-09) - "Disk Lifecycle"

Upgrade from: v4.1.2 - Drop-in upgrade via `sudo bash install.sh --upgrade`

### Architecture

This release implements the four pillars of disk lifecycle management -
the foundation required for serious NAS infrastructure:

**1. Disk Discovery (enriched)**
`GET /api/system/disks` now returns stable identifiers for every disk:
`by_id_path` (`/dev/disk/by-id/wwn-0x…`), `by_path_path`, `wwn`, `size_bytes`,
`rpm`, `pool_name`, `health`, `temp_c`. Type detection extended to SAS and USB.
Pool membership and per-vdev health resolved from a single `zpool status -P -v`
pass at discovery time.

**2. Device Renaming / Stable Identifiers (enforced)**
Pool creation via the UI now enforces `/dev/disk/by-id/` paths - matching
the GitOps engine which has always enforced this. Short `/dev/sdX` names
submitted to `POST /api/system/pool/create` are auto-promoted to their by-id
path via sysfs; if promotion fails the request is rejected with a clear error.
Suggestions from the setup wizard use by-id paths.

A new SQLite table `disk_registry` (migration 010) persists serial, WWN,
by-id path, model, pool membership, and last-seen timestamp for every disk
the system has ever encountered. This is the source of truth for identity
across reboots and physical replacements.

**3. Hot-Swap Detection (end-to-end)**
- New `udev/99-dplaneos-hotswap.rules`: covers SATA, SAS, NVMe add/remove
  events for internal pool disks (USB excluded to avoid double-firing with
  the existing removable media rules).
- New `scripts/notify-disk-added.sh` / `notify-disk-removed.sh`: send HTTP
  POST to `http://127.0.0.1:9000/api/internal/disk-event` via curl - replacing
  the broken `nc -U` Unix socket approach.
- New `POST /api/internal/disk-event` (localhost-only): updates disk registry,
  broadcasts `diskAdded`/`diskRemoved`/`poolHealthChanged` WebSocket events.

**4. Pool Import Recovery (automatic)**
On a `diskAdded` event the daemon now:
1. Waits 2 seconds for the kernel to settle the device tree.
2. Runs `zpool import -d /dev/disk/by-id` to enumerate importable pools.
3. Cross-references against the pool registry for any previously-known pool
   whose vdevs match the arriving disk's serial or WWN.
4. If a match is found: runs `zpool import -d /dev/disk/by-id <poolname>`
   automatically and logs the result to the audit chain.
5. Broadcasts `poolHealthChanged` so connected UI clients update instantly.

### Added

- `disk_registry` SQLite table (migration 010): persists full disk identity
  history including `removed_at` timestamp for disks that have been pulled.
- `GET /api/system/disks` enriched fields: `by_id_path`, `by_path_path`,
  `wwn`, `size_bytes`, `rpm`, `pool_name`, `health`, `temp_c`, `dev_path`.
- `POST /api/internal/disk-event`: internal hot-swap notification endpoint.
- `udev/99-dplaneos-hotswap.rules`: hot-swap rules for pool disks.
- `scripts/notify-disk-added.sh`, `scripts/notify-disk-removed.sh`.
- **HardwarePage**: WWN, by-id path, SAS/USB type badges, pool membership
  badge, disk replacement workflow (modal → `POST /api/zfs/pool/replace`).
- **Dashboard**: "Disk Health" section shows SMART failures and high-temp
  warnings across all disks with link to Hardware page.
- **Background monitor**: `CheckMountStatus()` implemented - write-tests each
  pool's mountpoint every 60 seconds, broadcasts `mountError` on failure.
- **Disk temperature monitoring**: reads `/sys/class/hwmon/` sensors every
  5 minutes, falls back to `smartctl`, broadcasts `diskTempWarning` at 45°C
  warning / 55°C critical thresholds.

### Fixed

- `diskTempWarning` WebSocket event was subscribed in frontend but never
  broadcast by daemon - now implemented end-to-end.
- `CheckMountStatus` was an empty stub - now performs real write-test.
- Pool creation accepted raw `/dev/sdX` paths that become invalid after
  reboot - now auto-promotes to by-id or rejects with actionable error.
- Disk type detection did not distinguish SAS from HDD, or USB from SATA -
  now uses vendor string and subsystem symlink for accurate classification.

### Stats

| Metric | Before | After |
|--------|--------|-------|
| Disk identity fields returned | 6 | 14 |
| Hot-swap detection (internal disks) | None | SATA + SAS + NVMe |
| Automatic pool re-import | Manual only | Automatic on disk add |
| Pool creation using stable paths | GitOps only | UI + GitOps |
| Disk registry (persistent identity) | None | SQLite, full history |
| `diskTempWarning` WS events | Dead code | Live, hwmon + smartctl |

---

## v4.1.2 (2026-03-09) - "Completeness"

Upgrade from: v4.1.1 - Drop-in upgrade via `sudo bash install.sh --upgrade`

### Fixed

- **`/api/system/metrics` shape mismatch:** `SupportPage` was showing dashes for
  CPU model, CPU %, uptime, OS, kernel. `HandleSystemMetrics` now returns all
  fields the frontend expects: `cpu_model`, `cpu_percent`, `memory_total`,
  `memory_used`, `uptime`, `os`, `kernel`, `load_avg`. CPU % is sampled over
  200 ms for accuracy.

- **`HandleSystemSettings` was dead code:** The system tuning handler
  (ARC limit, swappiness, inotify thresholds) was never registered and returned
  hardcoded stubs. Now registered at `GET/POST /api/system/tuning`, persists to
  `ConfigDir/system-settings.json`, and applies immediately to the running
  system: ARC limit → `/sys/module/zfs/parameters/zfs_arc_max` +
  `/etc/modprobe.d/zfs.conf`; swappiness → `/proc/sys/vm/swappiness` +
  `/etc/sysctl.d/99-dplaneos.conf`.

- **`install.sh` fatal-die on systems without Go:** If no pre-built binary is
  found and Go is not installed, `install.sh` now auto-downloads the release
  tarball from GitHub Releases, extracts the binary, and continues. Falls back
  to a clear actionable error message if the download also fails. GitHub URL
  corrected throughout (`4nonX/D-PlaneOS`).

- **inotify file watching was a stub:** `HybridIndexer.addRealtimeWatch` stored
  `nil` in the watch map. Now opens a real `inotify_init1` fd per watched path,
  registers `IN_CREATE | IN_DELETE | IN_MODIFY | IN_MOVED_FROM | IN_MOVED_TO |
  IN_CLOSE_WRITE`, and drains events in a per-path goroutine. `RemoveWatch`
  properly closes the fd.

- **Rsync backup task history was always empty:** `GET /api/backup/rsync`
  returned an empty list on every load. Tasks are now persisted to
  `ConfigDir/backup-tasks.json`. Each task record captures ID, source,
  destination, status, start/finish times, exit code, and job ID. Last 50 tasks
  returned newest-first. New `DELETE /api/backup/rsync/{id}` clears a record.

- **Cloud sync had no job tracking:** `listJobs` always returned empty.
  Sync and copy actions now create in-memory `CloudSyncJob` records (ID,
  provider, action, source, destination, status, timing). `GET
  /api/cloud-sync/jobs` returns the last 20 jobs newest-first.

### Added

- **OS package updates (Debian/Ubuntu):** New `UpdatesPage` tab "OS Packages"
  surfaces four new endpoints:
  - `GET /api/system/updates/check` - runs `apt-get update` + `apt list
    --upgradable`, returns structured package list with security flag, non-blocking via job queue
  - `POST /api/system/updates/apply` - runs `apt-get upgrade -y`, non-blocking
  - `POST /api/system/updates/apply-security` - security-only upgrade via
    `unattended-upgrades`, non-blocking
  - `GET /api/system/updates/daemon-version` - checks GitHub Releases API,
    returns current vs latest version with update-available flag

- **ZFS Sandbox page** (`/sandbox`): UI for the existing sandbox backend
  (ephemeral ZFS clones backed by Docker). Create named sandboxes from any
  dataset, destroy to revert all changes, clean orphaned volumes.

- **ZFS Delegation page** (`/delegation`): UI for `zfs allow`. Add and revoke
  fine-grained ZFS permissions per user/group per dataset. Full permission
  checkbox grid (create, destroy, mount, snapshot, rollback, clone, send,
  receive, quota, reservation, hold, release).

- **SMART test trigger in Hardware page:** Per-disk "Short Test" and "Long
  Test" buttons trigger `POST /api/zfs/smart/test`. Results viewable in a modal
  via `GET /api/zfs/smart/results?device=X`.

### Stats

| Metric | Before | After |
|--------|--------|-------|
| Stub/dead handler functions | 3 | 0 |
| Frontend pages with blank metric fields | 1 | 0 |
| Missing frontend pages (backend existed) | 2 | 0 |
| install.sh fatal on no-Go systems | yes | no |
| Real inotify file watching | no | yes |

---

## v4.1.1 (2026-03-09) - "Design System"

### Changed

- **Design system adoption (all pages):** 27 pages previously defined
  per-file `const btnPrimary / btnGhost / btnDanger / inputStyle` objects,
  producing inconsistent padding, missing hover states, and divergent font
  weights across the UI. All removed and replaced with the CSS design system:
  `.btn .btn-primary`, `.btn .btn-ghost`, `.btn .btn-danger`, `.btn-sm`,
  `.input`, `.data-table`.

- **New `.tabs-line` CSS variant:** The existing `.tabs` is a pill/segment
  control. Pages use an underline tab pattern - this is now a first-class
  design system member (`.tabs-line` wrapper + `.tab` / `.tab-active`),
  replacing 13 pages of duplicated inline `borderBottom` logic.

- **Hover / focus states restored uniformly:** Inline style buttons had no
  `:hover` or `:focus-visible` states. All buttons now inherit the glow and
  colour transitions from `index.css`.

- **Unused `import type React` removed** from 14 pages (was only needed
  for `React.CSSProperties` on the deleted style objects).
  Bundle size: −18 KB minified.

### Fixed

- **AppShell:** `ForcePasswordChange` overlay gates the full UI when
  `must_change_password` is set. Strength bar mirrors daemon
  `validatePasswordStrength` rules exactly (8 chars, upper+lower+digit+special).

- **SecurityPage:** New **Password** tab (first tab, default) exposes
  `POST /api/auth/change-password` to all logged-in users at any time.
  Session remains valid after change.

- **LoginPage:** `pending_token` for TOTP verification now sent in JSON body
  as daemon `HandleTOTPVerify` expects, not as a request header.

### Stats

| Metric | Before | After |
|--------|--------|-------|
| Pages with inline style objects | 25 | 0 |
| Pages using CSS design system | 2 | 38 |
| Unused React imports | 14 | 0 |
| JS bundle (minified) | 951 KB | 933 KB |

---

## v4.1.0 (2026-03-08) - "Terminal"

### Feature: Embedded PTY Terminal

- **New `/ws/terminal` WebSocket endpoint (daemon):** Spawns a `bash --login` PTY via `creack/pty` and pipes stdin/stdout over WebSocket. Authenticated by the global `sessionMiddleware` - same session validation as all other endpoints. Each connection gets its own isolated PTY; connections are torn down cleanly when the WebSocket closes.
- **Terminal resize support:** Client sends `{"type":"resize","cols":N,"rows":N}` messages; daemon calls `pty.Setsize()` so shell-aware programs (vim, htop, man) render correctly at any window size.
- **New `TerminalPage` (frontend):** Full xterm.js terminal (`@xterm/xterm` v5) with `FitAddon` (auto-resize) and `WebLinksAddon` (clickable URLs). Colour scheme matches the D-PlaneOS dark theme. Reconnect and Clear buttons in the title bar. Connection status indicator (green/amber/red dot).
- **Sidebar:** Terminal added to the System group (`terminal` icon).
- **Font regression fixed:** `index.html` was loading fonts from `fonts.googleapis.com`. All three fonts (Outfit, JetBrains Mono, Material Symbols Rounded) now load exclusively from `/assets/fonts/` - zero external requests at runtime, fully airgap-safe.

### Added
- `daemon/internal/handlers/terminal_handler.go` - PTY handler
- `daemon/vendor/github.com/creack/pty` v1.1.24
- `app-react/src/pages/TerminalPage.tsx`
- `@xterm/xterm`, `@xterm/addon-fit`, `@xterm/addon-web-links` npm dependencies

### Fixed
- `index.html` CDN font references removed; replaced with `/assets/fonts/fonts.css`
- `fonts.css` updated to use absolute paths (`/assets/fonts/...`)
- Dead `react-vendor` Rollup manual chunk removed from `vite.config.ts`

---


---


## v4.0.0 (2026-03-08) - **"React SPA"**

Upgrade from: v3.3.3 - Drop-in upgrade via `sudo ./scripts/upgrade-with-rollback.sh`

### ⚡ Architecture: Full React SPA Migration

The entire frontend has been rewritten from scratch. 41 standalone vanilla HTML/JS pages replaced by a single-page application built on React 19 + TypeScript + Vite + TanStack Query. The daemon is unchanged - this is a pure frontend replacement.

**Stack:**
- React 19 + TypeScript (0 type errors at build)
- TanStack Router (type-safe navigation - TS error on unregistered routes)
- TanStack Query (data fetching, caching, background refresh)
- Zustand (auth state, WebSocket hub)
- Vite build (tree-shaken, code-split by route)

**37 pages implemented across 10 phases:**

| Phase | Pages |
|-------|-------|
| 0 - Scaffold | AppShell, Sidebar, TopBar, auth/session infrastructure |
| 1 - Core Read-Only | Dashboard, Reporting, Hardware, Logs, Monitoring |
| 2 - Storage | Pools, Shares, NFS, Snapshot Scheduler, Replication |
| 3 - Docker | Docker (containers + compose tabs), Modules |
| 4 - Files | Files, ACL, Removable Media |
| 5 - Users & Security | Users (users/groups/roles tabs), Security, Directory (LDAP) |
| 6 - Network & System | Network, Settings, Alerts, Firewall, Certificates, UPS, Power, IPMI, HA |
| 7 - DevOps | Git Sync, GitOps, Cloud Sync |
| 8 - Admin | Audit, Support, Updates |
| 9 - Wizards | Setup Wizard |
| 10 - WebSocket | Real-time push for Docker state, pool health, disk temps |

### 🐛 Bug Fix: NFS Routes Not Registered (Daemon)

`nfs_handler.go` existed but its routes were never registered in `main.go`. NFS CRUD (`/api/nfs/exports`, `/api/nfs/status`, `/api/nfs/reload`) were silently unreachable in all previous v3.x releases. Routes are now registered.

### 🏗️ Infrastructure: Fully Offline Fonts

All three fonts are bundled in `app/assets/fonts/` - zero external requests at runtime:

| Font | Format | Purpose |
|------|--------|---------|
| `MaterialSymbolsRounded.woff2` | Variable | All icons |
| `outfit.woff2` | Variable (100–900) | UI chrome |
| `jetbrains-mono.woff2` | Variable (100–900) | Code / data display |

### 🏗️ Infrastructure: NixOS Deployment (Corrected)

NixOS configuration updated to reflect accurate current-state facts:
- `system.stateVersion` and `nixpkgs.url` corrected to `25.11` (current stable)
- Default kernel for NixOS 25.11 is `6.12`; our explicit pin to `6.6 LTS` is documented as intentional
- OpenZFS LTS branch is `2.3.x` (not 2.2); ZFS assertion updated to `>= 2.3`
- `lib.fakeHash` → `nixpkgs.lib.fakeHash` (was not in scope in `eachSystem` block - would have caused eval error)

### ✅ Compatibility

Drop-in replacement for v3.3.2. Daemon is unchanged. No schema changes, no migrations, no configuration changes required. The new frontend serves from the same `/opt/dplaneos/app` path.


---

## v3.3.3 (2026-03-07) - **"Async & Governance"**

Upgrade from: v3.3.2, v3.3.1, v3.3.0, or any v3.x - Drop-in upgrade via `sudo ./scripts/upgrade-with-rollback.sh`

### ⚖️ Governance: License Changed to AGPLv3

- **License changed from PolyForm Shield 1.0.0 to GNU Affero General Public License v3.0 (AGPLv3):** D-PlaneOS is now licensed under an OSI-approved open-source license. The AGPLv3 permits free use, modification, and distribution. Modified versions run as a network service must make their source available to users of that service. SPDX identifier: `AGPL-3.0-only`.

- **NixOS users - remove `allowUnfreePredicate`:** Under PolyForm Shield the Nix `meta.license` was set to `licenses.unfree`, requiring `allowUnfreePredicate` or `allowUnfree = true`. AGPLv3 is a free software license. Remove any `allowUnfreePredicate` blocks referencing `dplaneos-daemon` - they are now dead code. The flake's `meta.license` is updated to `licenses.agpl3Only`.

- **Contributor License Agreement introduced:** `CLA-INDIVIDUAL.md` and `CLA-ENTITY.md` added to the repository root. The CLA grants the maintainer the right to re-license commercially in the future; contributors retain full ownership. Signing is handled via CLA Assistant bot on pull requests.

### ⚡ Feature: Async Job Store (Daemon)

- **New `daemon/internal/jobs/jobs.go` package:** In-process, in-memory job store for long-running operations. Each job has a UUID, status (`running` → `done` / `failed`), result payload, and error string. Concurrent-safe. State is ephemeral - does not survive daemon restarts, acceptable because all jobs are short-lived.

- **New `GET /api/jobs/{id}` route (`jobs_handler.go`):** Poll for job status. Returns `{"status":"running"}` while in progress, `{"status":"done","result":{...}}` on success, or `{"status":"failed","error":"..."}` on failure.

- **8 blocking endpoints converted to async (HTTP 202):**

  | Endpoint | Typical duration |
  |---|---|
  | `POST /api/replication/send` | Seconds – hours |
  | `POST /api/replication/send-incremental` | Seconds – hours |
  | `POST /api/replication/receive` | Seconds – hours |
  | `POST /api/backup/rsync` | Minutes – hours |
  | `POST /api/docker/pull` | 30s – 10 min |
  | `POST /api/docker/update` | 1 – 5 min |
  | `POST /api/docker/compose/up` | 10s – 5 min |
  | `POST /api/docker/compose/down` | 5 – 60s |

  **Breaking change:** These endpoints now return `{"job_id":"<uuid>"}` immediately. API consumers that expect a result in the response body must update to the poll pattern.

### ⚡ Feature: Frontend Async Polling - `ui.pollJob()`

- **New `DPlaneUI.pollJob()` in `ui-components.js`:** Single consistent polling loop for all async operations. Shows loading overlay immediately, polls `GET /api/jobs/{id}` every 2 seconds, retries on transient network errors, enforces 30-minute hard timeout, hides overlay in all exit paths.

- **`docker.html` - 4 operations updated:** `composeUp`, `composeDown`, `pullImage`, `updateContainer` now dispatch via `ui.pollJob()`.

- **`replication.html` - 2 operations updated:** `runTask` and `startReplication` dispatch via `ui.pollJob()`. Replication start button now correctly restores its icon on job completion.

### 🐛 Bug: Navigation Stub Redirects and Missing NFS Entry

- **5 nav stub redirects replaced with direct links** in `nav-shared.js`: Interfaces → `network.html#interfaces`, DNS → `network.html#dns`, Routing → `network.html#routing`, System Settings → `settings.html`, File Upload → `files.html`. Stubs retained for existing bookmarks.

- **`data-page` mismatch fixed:** File Upload nav entry had `data-page="files-enhanced"`, breaking the active-page highlight on `files.html`. Fixed to `data-page="files"`.

- **NFS Exports added to nav:** `nfs.html` has been a complete, functional NFS management page since v3.x but had no navigation entry - unreachable without a direct URL. Now listed under Storage → NFS Exports (between Shares and Replication).

### 🏗️ NixOS: `ota-module.nix` Options

- **`options.services.dplaneos.ota` namespace added:** Two new tunable options: `ota.enable` (default: `true`) to disable the health-check timer independently of the daemon, and `ota.healthCheckDelay` (default: `"90s"`) to tune the post-boot wait. Module is gated on `lib.mkIf (cfg.enable && cfg.ota.enable)`.

### ✅ Compatibility

Drop-in replacement for v3.3.2 with one exception: the 8 async endpoints now return `{"job_id":"..."}` with HTTP 202 instead of blocking. All other API surface, schema, and configuration unchanged.

**NixOS users only:** Remove any `allowUnfreePredicate` or `allowUnfree` blocks referencing `dplaneos-daemon`.

---

## v3.3.2 (2026-03-01) - **"Runtime fixes"**

Upgrade from: v3.3.1, v3.3.0, or any v3.x - Drop-in upgrade via `sudo ./scripts/upgrade-with-rollback.sh`

### 🔒 Security: Eliminated `bash -c` Shell Construction in Replication

- **`replication_remote.go` - shell injection vector removed:** Both the normal and resume-token replication paths previously built a complete shell pipeline string via `fmt.Sprintf` and executed it with `executeCommand("/bin/bash", []string{"-c", fullCmd})`. Despite upstream input validation, string-formatted shell commands are an inherently fragile security boundary. The entire replication pipeline (`zfs send` → optional `pv` → `ssh recv`) is now implemented as three discrete `exec.Command` processes connected via Go `io.Pipe` in a new `execPipedZFSSend()` helper. No shell is invoked at any point.

- **Resume token validation added:** ZFS resume tokens are now validated with `isValidResumeToken()` (alphanumeric + base64 characters only, max 4096 bytes) before being used as a command argument. Previously the token was passed directly from the SSH remote into `fmt.Sprintf`.

- **Error responses no longer leak command strings:** The `"command": fullCmd` field previously included in replication failure responses exposed the full constructed shell command to API callers. This field has been removed.

### 🔒 Security: iSCSI Authentication Default Made Explicit

- **`iscsi.go` - `authentication=0` is now an explicit opt-out, not a silent default:** Every new iSCSI target previously had CHAP authentication disabled silently. A new `require_chap` boolean field has been added to `ISCSICreateRequest`. When `require_chap: true`, the TPG is created with `authentication=1`. When `require_chap: false` (the current default for backward compatibility), `authentication=0` is still set but a `SECURITY NOTICE` log line is emitted, making the decision auditable. This is a **non-breaking change** - existing API callers that do not include `require_chap` behave identically to before.

### 🐛 Bug: LDAP `TriggerSync` - Full Implementation

- **`ldap.go` + `ldap/client.go` - sync now actually syncs:** `POST /api/ldap/sync` previously connected to the LDAP server, bound the service account, and immediately returned `{"success": true}` with 0 users found/created/updated. No directory data was read or written. This has been replaced with a full implementation:
  - New `SyncAll()` method on the LDAP client performs a wildcard search against the configured `BaseDN` using the configured `UserFilter`, fetches all matching entries, and retrieves group memberships for each
  - `TriggerSync` upserts each user into the `users` table (`source='ldap'`, empty `password_hash`) applying group→role mapping via the existing `GroupMappings` config
  - Response now returns real counts: `users_found`, `users_created`, `users_updated`, `users_skipped`, and `errors` per user

### 🐛 Bug: Version String Never Embedded in Binary

- **`daemon/cmd/dplaned/main.go` - `Version` changed from `const` to `var`:** The `Version` identifier was declared as a `const`, but Go's `-ldflags "-X main.Version=..."` mechanism only works with package-level `var` declarations. As a result, all previous release builds reported `version: "dev"` at `/health` and in startup logs regardless of the version tag. Changed to `var` - version is now correctly embedded at build time and visible in the health endpoint.

- **README:** Removed "No other NAS OS does this" from the container update description (snapshot+rollback is standard practice in the NAS space). Removed unsupported "100× faster" benchmark claim from replication description. Changed "injection-hardened" to "allowlist-based input validation" (more accurate). Added explicit HA limitations section. Fixed LDAP feature list to reflect actual implementation.
- **INSTALLATION-GUIDE:** Removed "enterprise NAS" language.
- **SECURITY.md:** Updated command execution description to reflect the `bash -c` removal. Added HA and LDAP known limitations to the Known Limitations section.
- **THREAT-MODEL.md:** Updated T1 (Command Injection) to document the replication fix. Added T13 (HA Split-Brain) as a new threat entry with HIGH residual risk rating and mitigation guidance.
- **ADMIN-GUIDE:** Updated LDAP sync documentation to accurately describe the full-directory sync behavior.
- **HA `cluster.go`:** Package comment expanded with explicit NO-STONITH, NO-automatic-failover, NO-split-brain-protection, and NO-quorum warnings.

### ✅ Compatibility

Drop-in replacement for v3.3.1. No schema changes, no migrations, no configuration changes required. The `require_chap` field in iSCSI create requests defaults to `false` - existing API integrations are unaffected.

---

## v3.3.1 (2026-02-25) - **"Universal Compatibility"**

Upgrade from: v3.3.0, v3.2.1, or any v3.x - Drop-in upgrade via `sudo ./scripts/upgrade-with-rollback.sh`

### 🐛 Bug Fixes

- **Ubuntu readonly variable crash (Phase 0):** `install.sh`, `get.sh`, `scripts/pre-flight.sh`, and `scripts/system-audit.sh` previously sourced `/etc/os-release` directly, which caused Ubuntu to abort with `/etc/os-release: line 4: VERSION: readonly variable` and fail at Phase 0 before any installation occurred. All four files now use safe `grep`-based extraction into scoped variables - the OS-managed `VERSION` variable is never touched.

- **`TERM environment variable not set` warning:** `install.sh` called `clear` unconditionally at startup and on the completion screen. In non-interactive contexts (serial console, VM without TTY, piped install) this emitted a `TERM` warning that polluted output and confused users expecting a clean install log. Both `clear` calls are now guarded with `[ -n "$TERM" ]`.

### 🐧 NixOS Compatibility

- **NixOS no longer causes install termination:** `install.sh` Phase 0 OS detection previously treated any unrecognised `$ID` as a fatal error. NixOS is now detected as a named case, emits an informational warning directing users to `nixos/NIXOS-INSTALL-GUIDE.md`, and continues rather than aborting. All NixOS-specific files remain untouched under `nixos/`.

### 🚀 Phase 12: Dynamic IP Notification

- **Access URL displayed at completion:** Phase 12 now calculates the primary IPv4 address via `hostname -I` and displays it in a clearly bordered completion box - `http://<PRIMARY_IP>` - along with a notice that the VM screen may remain black after install. Eliminates the most common post-install support question.

### 📋 CI / Release Pipeline

- **Syncthing conflict file guard:** Both `validate.yml` and `release.yml` now fail immediately at checkout with a clear error listing any `.sync-conflict-*.go` files present in the tree, preventing the duplicate symbol build failures caused by Syncthing writing conflict copies into the working directory.
- **`*.sync-conflict-*` excluded from release tarballs:** `rsync` in the package step explicitly excludes all Syncthing conflict files regardless of the guard.
- **`build/` directory pre-created before daemon build:** `go build` does not create parent directories; the `mkdir -p ../build` step was missing, which would cause the release build to fail if `build/` did not already exist in the workspace.
- **`sha256sum` step simplified:** Removed fragile `cd /tmp && basename` pattern; checksum is now generated directly from the full `$TARBALL` path.
- **Double-trigger removed from `release.yml`:** The `on: release: types: [created]` trigger was firing the release job a second time when GitHub auto-created the release after a tag push. Now triggers only on `push: tags: v*`.
- **bcrypt CI password injection fixed:** `validate.yml` was embedding `CI_PASS` directly into a Python string literal via shell substitution. Password is now passed via `env:` and read with `os.environ` inside Python, making it safe for passwords containing `!`, `$`, or `'`.
- **ZFS cleanup order fixed:** `validate.yml` cleanup was calling `losetup -d` on all loop devices in a single command before `zpool destroy`, which hangs ZFS when its backing devices are detached first. Now destroys the pool first, then detaches each loop device individually.
- **Release notes regex hardened:** `extract-release-notes.py` now matches CHANGELOG headings with or without the `v` prefix (`## v3.3.1` or `## 3.3.1`).
- **Wrong install command in release notes:** The generated installation instructions used `sudo make install` (no `Makefile` exists); corrected to `sudo bash install.sh`.

### 📄 Licensing & Documentation

- **Corrected "open-source" misidentification:** D-PlaneOS is licensed under GNU Affero General Public License v3.0 (AGPLv3), which is **source-available**, not OSI-approved open-source. Corrected in `README.md`, `SHOWSTOPPER-MITIGATION-GUIDE.md`, `docs/SHOWSTOPPER-MITIGATION-GUIDE.md`, and `nixos/NIXOS-README.md`. Third-party dependency references (ZFS, Samba, Docker, nginx) correctly retain their "open-source" descriptions as those packages use OSI-approved licenses.

### ✅ Compatibility

Drop-in replacement for v3.3.0. No schema changes, no migrations, no config changes required.

---

## v3.3.0 (2026-02-22) - **"UX / Security Hardening"**

Upgrade from: v3.2.1, v3.2.0, or v3.1.x - Drop-in upgrade via `sudo ./scripts/upgrade-with-rollback.sh`

### ⚡ Architecture: Boot-Order Hardening (`dplaneos-init-db`)

- New `dplaneos-init-db.service` acts as mandatory gatekeeper before API and Event daemons start
- Executes `init-database-with-lock.sh` to eliminate schema-creation race conditions
- Runs `validate-db-schema.sh` to verify SQLite FTS5 integrity
- All core daemons strictly `Require` this service to complete before startup

### 🔒 Security: HMAC Audit Chain & Zero-Trust API

- **Tamper-proof audit log:** every administrative action hashed and chained with HMAC-SHA256; WebUI flags integrity violations immediately
- **Strict parameter validation:** all API inputs (ZFS pool names, Docker IDs, filesystem paths) pass through whitelist-only regex engine - prevents shell injection and malformed parameter attacks
- **RBAC foundation:** SQLite schema extended with dedicated Role-Based Access Control tables; groundwork for multi-user / enterprise deployments

### 🔌 Storage: Real-Time udev Reactivity

- New udev rules trigger immediate WebUI updates on hardware state changes
- Detects insertion/removal of USB storage devices, optical media (CD/DVD/Blu-ray)
- WebUI can issue physical eject commands to compatible drives
- Eject synchronized with ZFS unmount workflows - prevents data loss during media removal

### 🔐 Password UX - Unified & Predictable

**Backend (Go)**
- Password validation centralized via `validatePasswordStrength()` - eliminates rule drift between handlers
- All password inputs normalized with `strings.TrimSpace()` - prevents invisible copy/paste whitespace failures

**Frontend**
- Real-time strength checklist (mirrors backend rules), show/hide toggle, live confirm-match indicator
- Client-side pre-validation reduces failed API calls
- Affected pages: `login.html`, `users.html`, `setup-wizard.html`

### 🔔 Notifications & UX Hardening

- All toast notifications now fully dismissible (×), unified top-right positioning, hover pauses auto-dismiss
- **Unsaved Changes Guard:** Material Design 3 warning banner + browser `beforeunload` safeguard; applied to `network.html`, `settings.html`
- **Double-submit protection:** apply/save buttons disabled during API calls, safe re-enable via `finally` logic - prevents duplicate operations and race conditions

### 🎨 UI: Material Design 3 Proportions

- Migrated to 8px grid system with `rem` units for resolution-independent sizing
- Sidebar adapts to bottom navigation rail on mobile
- Material Symbols Rounded integrated as variable font (programmatic weight/theme adjustments)

### New Components

- `dplaneos-init-db.service`
- `password-strength.js`
- `unsaved-changes.js`

### ✅ Compatibility

Drop-in replacement for v3.2.1. No schema changes, no migrations required (optional FTS5 optimization available).

---

## v3.2.1 (2026-02-21) - **"XSS Sanitisation"**

### 🔒 Security: Frontend XSS sanitisation (T5 closure)
- Added `esc()` / `escapeHtml()` sanitiser to all frontend pages and the alert system
- Server-sourced values (`alert.title`, `alert.message`, `alert.alert_id`, log fields, UPS hardware strings, dataset names, error messages) are now escaped before `innerHTML` insertion
- Affected files: `alert-system.js`, `audit.html`, `docker.html`, `iscsi.html`, `pools.html`, `ups.html`, `reporting.html`, `system-updates.html`
- T5 residual risk downgraded from MEDIUM to LOW in `THREAT-MODEL.md`

### 📄 Documentation
- All version references bumped to v3.2.1 across all docs, scripts, and NixOS modules
- `THREAT-MODEL.md` updated to reflect v3.2.1 security posture (T5 mitigated, Known Gaps updated)

### ✅ Compatibility
- Drop-in replacement for v3.2.0. No schema changes, no daemon flag changes, no config changes.
- Binary rebuilt with `-X main.Version=3.2.1`

---

## v3.2.0 (2026-02-21) - **"networkd Persistence"**

### ⚡ Architecture: systemd-networkd file writer (networkdwriter)
- New package `internal/networkdwriter`: writes `/etc/systemd/network/50-dplane-*.{network,netdev}`
- All network changes now survive reboots AND `nixos-rebuild switch` - no extra steps required
- `networkctl reload` used for zero-downtime live reload (< 1 second)
- Works on every systemd distro: NixOS, Debian, Ubuntu, Arch
- nixwriter scope reduced to NixOS-only settings (firewall ports, Samba globals)
- hostname/timezone/NTP already persistent via OS-level tool calls - no nixwriter needed

### ✅ Completeness
- All 12 nixwriter methods fully wired; all 9 stanzas covered
- New `/api/firewall/sync` endpoint for explicit NixOS firewall port sync
- DNS now has POST handler (`action: set_dns`) + `SetGlobalDNS` via resolved dropin
- `HandleSettings` runtime POST wires hostname + timezone persist calls
- `/etc/systemd/network` added to `ReadWritePaths` in `module.nix`

---

## v3.1.0 (2026-02-21) - **"NixOS Architecture Hardening"**

### ⚡ Architecture: Static musl binary + nixwriter + boot reconciler
- Static musl binary via `pkgsStatic`: glibc-independent, survives NixOS upgrades
- `internal/nixwriter`: writes `dplane-generated.nix` fragments for persistent NixOS config
- Boot reconciler: re-applies VLANs/bonds/static IPs from SQLite DB on non-NixOS systems
- Samba persistence: declarative NixOS ownership + imperative share management via include bridge
- `/etc/systemd/network` naming convention: NixOS owns `10-`/`20-` prefix, D-PlaneOS owns `50-dplane-`

### 🔒 Security & Stability
- SSH hardening: `PasswordAuthentication=false`, `PermitRootLogin=no`; new `sshKeys` NixOS module option
- Support bundle: `POST /api/system/support-bundle` - streams diagnostic `.tar.gz` (ZFS, SMART, journal, audit tail)
- Pre-upgrade ZFS snapshots: automatic `@pre-upgrade-<timestamp>` on all pools before every `nixos-rebuild switch`; `GET /api/nixos/pre-upgrade-snapshots`
- Webhook alerting: generic HTTP webhooks for all system events; `GET/POST/DELETE /api/alerts/webhooks`, test endpoint
- Audit HMAC chain: tamper-evident audit log with HMAC-SHA256; `GET /api/system/audit/verify-chain`; key at `/var/lib/dplaneos/audit.key`

### 📊 Monitoring & Real-Time Alerting
- Background monitor: debounced alerting (5 min cooldown, 30 s hysteresis) for inotify, ZFS health, capacity
- WebSocket hub: real-time events at `WS /api/ws/monitor`
- ZFS pool heartbeat: active I/O test every 30 s; auto-stops Docker on pool failure
- Capacity guardian: configurable thresholds, emergency reserve dataset, auto-release at 95%+
- Deep ZFS health: per-disk risk scoring, SMART JSON integration; `GET /api/zfs/health`
- SMTP alerting: configurable SMTP for system alerts

### 🔁 GitOps
- Declarative `state.yaml`: schema for pools, datasets, shares with stdlib YAML parser
- By-ID enforcement: `/dev/disk/by-id/` required; bare `/dev/sdX` rejected at parse time
- Diff engine: CREATE / MODIFY / DELETE / BLOCKED / NOP classification with risk levels
- Safety contract: pool destroy always BLOCKED; dataset destroy blocked if used > 0 bytes; share remove blocked if active SMB connections
- Transactional apply: halts on unapproved BLOCKED items; idempotent operations
- Drift detection: background worker every 5 min; broadcasts `gitops.drift` WebSocket event
- API: `GET /api/gitops/plan`, `POST /api/gitops/apply`, `POST /api/gitops/approve`, `GET/PUT /api/gitops/state`

### 🏗️ Appliance Hardening (NixOS)
- A/B partition layout (`disko.nix`): EFI + system-a (8 G) + system-b (8 G) + persist (remaining)
- OTA update flow (`ota-update.sh`): Ed25519 signature verification, A/B slot switch, 90 s auto-revert health check
- NixOS OTA module (`ota-module.nix`): systemd health check timer, daemon integration
- Version pinning (`flake.nix`): kernel 6.6 LTS + OpenZFS 2.2, eval-time assertions
- Impermanence layer (`impermanence.nix`): ephemeral root, all state persisted to `/persist`

### New API routes
```
POST   /api/system/support-bundle
GET    /api/nixos/pre-upgrade-snapshots
GET    /api/system/audit/verify-chain
GET    /api/alerts/webhooks
POST   /api/alerts/webhooks
DELETE /api/alerts/webhooks/{id}
POST   /api/alerts/webhooks/{id}/test
GET    /api/gitops/status
GET    /api/gitops/plan
POST   /api/gitops/apply
POST   /api/gitops/approve
POST   /api/gitops/check
GET    /api/gitops/state
PUT    /api/gitops/state
WS     /api/ws/monitor
GET    /api/zfs/health
GET    /api/zfs/iostat
GET    /api/zfs/events
GET    /api/zfs/capacity
POST   /api/zfs/capacity/reserve
POST   /api/zfs/capacity/release
```

---

## v3.0.0 (2026-02-18) - **"Native Docker API"**

### ⚡ Major: Docker exec.Command → stdlib REST client

All container lifecycle operations now use the Docker Engine REST API directly over `/var/run/docker.sock` via a thin stdlib `net/http` client - zero new dependencies, no CGO, no shell involved.

**New package: `internal/dockerclient`** (pure stdlib, no imports outside std library)

| Method | Replaces |
|---|---|
| `ListAll(ctx)` | `docker ps -a --format {{json .}}` |
| `Inspect(ctx, id)` | `docker inspect --format ... NAME` |
| `Start(ctx, id)` | `docker start NAME` |
| `Stop(ctx, id, t)` | `docker stop -t T NAME` |
| `Restart(ctx, id, t)` | `docker restart NAME` |
| `Pause(ctx, id)` | `docker pause NAME` |
| `Unpause(ctx, id)` | `docker unpause NAME` |
| `Remove(ctx, id, force, vol)` | `docker rm [-f] [-v] NAME` |
| `PullImage(ctx, image)` | `docker pull IMAGE` |
| `Logs(ctx, id, opts)` | `docker logs --tail N NAME` |
| `WaitForHealthy(ctx, id, timeout)` | `docker inspect` polling loop |
| `IsAvailable(ctx)` | `docker info` / `which docker` |

### ⚡ Major: Linux netlink (`ip link/addr/route`) → stdlib syscall client

New package: `internal/netlinkx` - rtnetlink via raw `syscall.Socket(AF_NETLINK, ...)`, no external dependencies, no CGO. Replaces ~15 `ip(8)` exec calls across `system.go` and `network_advanced.go`.

### 🔒 Security fix: Git repository URL RCE via `ext::` transport

**Severity: Critical** - `ext::` transport executes arbitrary subprocesses as root daemon user. Fix: `validateRepoURL()` enforces allowlist of permitted schemes (`https://`, `http://`, `git://`, `ssh://`, `git@host:path`). Blocks `ext::`, `file://`, `fd::`, and custom transports. Applied at `TestConnectivity` and `SaveRepo`.

### 🎨 UI Consolidation
- Shared navigation injected via `nav-shared.js` - eliminates 8KB nav HTML duplicated across 20 pages
- `dplaneos-ui-complete.css` now includes global reset
- NixOS configuration files added (`nixos/flake.nix`, `nixos/module.nix`, `nixos/configuration-standalone.nix`, `nixos/setup-nixos.sh`)

---

## v2.2.1 (2026-02-18) - **"Security & Reliability Audit Fixes"**

### 🔴 Critical: Runtime ZFS Pool Loss → Docker Still Running
- New `pool_heartbeat.go` - `maybeStopDocker()`: calls `systemctl stop docker` on `SUSPENDED/UNAVAIL` or write-probe failure
- Guard fires only once per failure window, resets on pool recovery

### 🔴 Critical: Path Traversal in Git Sync compose_path
- `validateComposePath()`: rejects absolute paths/null bytes, `filepath.Clean()` + prefix check
- Applied in 4 places: `SaveRepo`, `DeployRepo`, `ExportToRepo`, `PushRepo`

### 🟡 Medium: Audit Buffer - Security Events Lost on SIGKILL
- 10 security-critical action types bypass buffer, write directly to SQLite: `login`, `login_failed`, `logout`, `auth_failed`, `permission_denied`, `user_created/deleted`, `password_changed`, `token_created/revoked`

### 🟡 Medium: Health Check - False Positives for Slow Apps
- `waitForHealthy()` polls every 2s with Docker `HEALTHCHECK` awareness; default raised from 5s to 30s; `unhealthy` fails immediately

### 🟢 Low: ECC Detection Unreliable in VMs
- VM detection via `/sys/class/dmi/id/product_name` and `/proc/cpuinfo` hypervisor bit; three states: Physical+ECC / Physical+no ECC / VM

---

## v2.2.0 (2026-02-17) - **"Git Sync: Bidirectional Multi-Repo"**

### ✨ New Feature: Bidirectional Git Sync

Full GitHub/Gitea integration for Docker Compose stacks - no external tool required.

| Direction | Trigger | Effect |
|---|---|---|
| Pull ← Git | Manual / Auto | Clone or pull repo, update local compose file |
| Deploy ← Git | Manual | `docker compose up -d` from repo compose file |
| Export → Git | Manual | Snapshot running containers as `docker-compose.yml` |
| Push → Git | Manual | `git commit + push` compose file to remote |

- Multi-repo syncs with per-sync credential references, auto-sync intervals, commit author identity
- Credential store (`git_credentials` table): PAT via `GIT_ASKPASS`, SSH key via `GIT_SSH_COMMAND`
- New backend: `git_sync_repos.go` - full CRUD + pull/push/deploy/export endpoints
- New frontend: `git-sync.html` (956 lines) - three-tab layout, per-sync cards, PAT setup wizard
- Legacy single-repo config fully preserved on "Legacy Config" tab

---

## v2.1.1 (2026-02-17) - **"Security, Stability & Architecture"**

### 🔴 Showstopper Fix: ZFS-Docker Boot Race (Critical)
- Hard systemd gate (`dplaneos-zfs-mount-wait.service`): polls until every configured pool is `ONLINE`, mounted, and writable
- `dplaned.service` and `docker.service` both `Require=` this gate - cannot start without it
- 5-minute timeout with 30-second progress logging

### 🟡 Notification Debouncing (Flooding Fix)
- `monitoring/background.go` rewritten: 30s hysteresis, 5 min cooldown, clear event on resolution, 2 min clearance cooldown

### 🟡 SQLite Durability (Power-Loss Safety)
- All 5 DB connections upgraded to `_synchronous=FULL`; consistent across all connections

### 🔒 Security Fixes (XSS)
- All `onclick="func('${serverData}')"` patterns replaced with `JSON.stringify()` across 9 pages
- `settings.html`: NixOS version display switched from `innerHTML` to `textContent`

### 🐛 Bug Fixes
- URL fixes (`&param=` → `?param=`): 5 locations
- `disk_discovery.go`: recursive `hasMountPoint()`, regex `diskNameInZpoolStatus()`
- `zfs_encryption.go`: `change-key -l` flag; key validation
- New: `daemon/internal/handlers/disk_discovery_test.go`

---

## v2.1.0 (2026-02-15) - **"ZFS-Docker Integration"**

### ⚡ Safe Container Updates (Killer Feature)

`POST /api/docker/update` - atomic container updates with ZFS data protection:
1. Creates ZFS snapshot of container volume
2. Pulls new image
3. Stops and restarts container
4. Runs health check (5s)
5. On failure: returns snapshot name for instant rollback

### Added
- ZFS Snapshot CRUD, Time Machine (file-level restore), Sandbox (ZFS clone environments)
- ZFS Remote Replication (`zfs send | ssh zfs recv`)
- ZFS Health Predictor (per-disk risk scoring, SMART integration)
- NixOS Config Guard (dry-activate validation, generation management)
- Docker Compose, Container Stats, Pool Capacity Guardian
- Routes: 105 → 171 (+66 new endpoints)

---

## v2.0.0 (2026-02-12) - **"Ground-Up Rewrite"**

Complete rewrite: PHP/Apache stack replaced by single Go binary (`dplaned`, 8 MB). Not an in-place upgrade from v1.x.

| Component | v1.x (PHP) | v2.0.0 (Go) |
|-----------|-----------|-------------|
| Backend | PHP-FPM + Apache | Single Go binary (`dplaned`, 8 MB) |
| Database | SQLite via PHP PDO | SQLite/WAL, 64 MB cache, FULL sync |
| Auth | PHP sessions | Session tokens + RBAC middleware |
| Frontend | PHP-rendered SPA | Static HTML + vanilla JS |
| Install | `install.sh` (50+ steps) | `make install` (one command) |

- 38 Go source files, 85 API routes, 41 HTML pages
- Full LDAP/AD integration, RBAC engine, buffered audit logging, WebSocket live updates
- ZFS encryption, removable media, replication, snapshot scheduler, firewall, TLS management
- Docker management, file browser, ACL/quota management, UPS monitoring, IPMI
- Input validation on all `exec.Command` calls, command whitelist, rate limiting

### ⚠️ Breaking Changes
Fresh install required. ZFS pools, datasets, shares, and Docker containers preserved on disk.

---

## v1.14.0-OMEGA (2026-02-01) - **"OMEGA Edition"**

First fully production-ready PHP release. Fixes 7 critical infrastructure bugs.

1. **www-data sudo permissions missing** (CRITICAL)
2. **SQLite write permissions** (CRITICAL)
3. **Login loop on cold start** (HIGH)
4. **API timeout handling** (HIGH)
5. **Silent session expiry** (MEDIUM)
6. **No loading feedback** (LOW)
7. **Style flash on load** (LOW)

---

## v1.12.0 (2026-01-31) - **"The Big Fix"**

45 vulnerabilities from comprehensive penetration test - 10 Critical, 7 High fixed.

---

## v1.11.0 (2026-01-31) - **"Vibecoded Security Theater Fix"**

- `execCommand()` checked if string `"escapeshellarg"` appeared in command, not whether arguments were actually escaped - 108 vulnerable call sites. Complete rewrite with strict command whitelisting.

---

## v1.10.0 (2026-01-31) - **"Smart State Polling & One-Click Updates"**

- ETag-based smart polling (95% bandwidth reduction, 88% CPU reduction)
- ZFS snapshot-based update system with automatic rollback
- License: MIT → GNU Affero General Public License v3.0 (AGPLv3)

---

## v1.9.0 (2026-01-30) - **"RBAC & Security Fixes"**

- Role-Based Access Control: Admin, User, Readonly roles
- 7 critical security fixes including session fixation, wildcard sudoers, Docker Compose YAML injection

---

## v1.8.0 (2026-01-28) - **"Power User Release"**

- File browser, ZFS native encryption, system service control, real-time monitoring - all 14 tabs functional

---

## v1.7.0 (2026-01-28) - **"The Paranoia Update"**

- UPS/USV management (NUT), automatic snapshot scheduling, system log viewer

---

## v1.6.0 (2026-01-28) - **"Disk Health & Notifications"**

- SMART monitoring, disk replacement tracking, notification center

---

## v1.2.0 - **"Initial Public Release"**

- ZFS management, Docker integration, system monitoring, session auth, audit logging, CSRF protection

---

## Upgrade Path

### v3.2.1 → v3.3.0
```bash
sudo ./scripts/upgrade-with-rollback.sh
```

### v1.14.0-OMEGA → v2.0.0
Fresh install required. ZFS pools and Docker containers preserved on disk.
```bash
tar xzf dplaneos-v2.0.0-production-vendored.tar.gz
cd dplaneos && sudo make install
```

---

## Support

**Security issues:** GitHub issues with `security` label. Response: Critical 24h, High 72h, Medium/Low 1 week.
**Bug reports:** GitHub issue with version, steps to reproduce, and logs.
**Feature requests:** GitHub issue with `enhancement` label.


