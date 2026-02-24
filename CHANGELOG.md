# D-PlaneOS Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/), and this project adheres to [Semantic Versioning](https://semver.org/).

---
# D-PlaneOS Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/), and this project adheres to [Semantic Versioning](https://semver.org/).

---

## v3.3.1 (2026-02-24) — **"Dependency Hygiene"**

Upgrade from: v3.3.0 — Drop-in upgrade via `sudo ./scripts/upgrade-with-rollback.sh`

### 🔧 Maintenance

- Fixed vendor/go.mod version mismatch: `golang.org/x/crypto` bumped from v0.13.0 to v0.14.0
- Synced `go.sum` and `vendor/` via `go mod tidy && go mod vendor`
- `make test` now works correctly in clean environments

### ✅ Compatibility

Drop-in replacement for v3.3.0. No schema changes, no API changes, no config changes.

---

## v3.3.0 (2026-02-22) — **"UX / Security Hardening"**

Upgrade from: v3.2.1, v3.2.0, or v3.1.x — Drop-in upgrade via `sudo ./scripts/upgrade-with-rollback.sh`

### ⚡ Architecture: Boot-Order Hardening (`dplaneos-init-db`)

- New `dplaneos-init-db.service` acts as mandatory gatekeeper before API and Event daemons start
- Executes `init-database-with-lock.sh` to eliminate schema-creation race conditions
- Runs `validate-db-schema.sh` to verify SQLite FTS5 integrity
- All core daemons strictly `Require` this service to complete before startup

### 🔒 Security: HMAC Audit Chain & Zero-Trust API

- **Tamper-proof audit log:** every administrative action hashed and chained with HMAC-SHA256; WebUI flags integrity violations immediately
- **Strict parameter validation:** all API inputs (ZFS pool names, Docker IDs, filesystem paths) pass through whitelist-only regex engine — prevents shell injection and malformed parameter attacks
- **RBAC foundation:** SQLite schema extended with dedicated Role-Based Access Control tables; groundwork for multi-user / enterprise deployments

### 🔌 Storage: Real-Time udev Reactivity

- New udev rules trigger immediate WebUI updates on hardware state changes
- Detects insertion/removal of USB storage devices, optical media (CD/DVD/Blu-ray)
- WebUI can issue physical eject commands to compatible drives
- Eject synchronized with ZFS unmount workflows — prevents data loss during media removal

### 🔐 Password UX — Unified & Predictable

**Backend (Go)**
- Password validation centralized via `validatePasswordStrength()` — eliminates rule drift between handlers
- All password inputs normalized with `strings.TrimSpace()` — prevents invisible copy/paste whitespace failures

**Frontend**
- Real-time strength checklist (mirrors backend rules), show/hide toggle, live confirm-match indicator
- Client-side pre-validation reduces failed API calls
- Affected pages: `login.html`, `users.html`, `setup-wizard.html`

### 🔔 Notifications & UX Hardening

- All toast notifications now fully dismissible (×), unified top-right positioning, hover pauses auto-dismiss
- **Unsaved Changes Guard:** Material Design 3 warning banner + browser `beforeunload` safeguard; applied to `network.html`, `settings.html`
- **Double-submit protection:** apply/save buttons disabled during API calls, safe re-enable via `finally` logic — prevents duplicate operations and race conditions

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

### ⚡ Major: Docker exec.Command → stdlib REST client

All container lifecycle operations now use the Docker Engine REST API directly over `/var/run/docker.sock` via a thin stdlib `net/http` client — zero new dependencies, no CGO, no shell involved.

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

New package: `internal/netlinkx` — rtnetlink via raw `syscall.Socket(AF_NETLINK, ...)`, no external dependencies, no CGO. Replaces ~15 `ip(8)` exec calls across `system.go` and `network_advanced.go`.

### 🔒 Security fix: Git repository URL RCE via `ext::` transport

**Severity: Critical** — `ext::` transport executes arbitrary subprocesses as root daemon user. Fix: `validateRepoURL()` enforces allowlist of permitted schemes (`https://`, `http://`, `git://`, `ssh://`, `git@host:path`). Blocks `ext::`, `file://`, `fd::`, and custom transports. Applied at `TestConnectivity` and `SaveRepo`.

### 🎨 UI Consolidation
- Shared navigation injected via `nav-shared.js` — eliminates 8KB nav HTML duplicated across 20 pages
- `dplaneos-ui-complete.css` now includes global reset
- NixOS configuration files added (`nixos/flake.nix`, `nixos/module.nix`, `nixos/configuration-standalone.nix`, `nixos/setup-nixos.sh`)

---

## v2.2.1 (2026-02-18) — **"Security & Reliability Audit Fixes"**

### 🔴 Critical: Runtime ZFS Pool Loss → Docker Still Running
- New `pool_heartbeat.go` — `maybeStopDocker()`: calls `systemctl stop docker` on `SUSPENDED/UNAVAIL` or write-probe failure
- Guard fires only once per failure window, resets on pool recovery

### 🔴 Critical: Path Traversal in Git Sync compose_path
- `validateComposePath()`: rejects absolute paths/null bytes, `filepath.Clean()` + prefix check
- Applied in 4 places: `SaveRepo`, `DeployRepo`, `ExportToRepo`, `PushRepo`

### 🟡 Medium: Audit Buffer — Security Events Lost on SIGKILL
- 10 security-critical action types bypass buffer, write directly to SQLite: `login`, `login_failed`, `logout`, `auth_failed`, `permission_denied`, `user_created/deleted`, `password_changed`, `token_created/revoked`

### 🟡 Medium: Health Check — False Positives for Slow Apps
- `waitForHealthy()` polls every 2s with Docker `HEALTHCHECK` awareness; default raised from 5s to 30s; `unhealthy` fails immediately

### 🟢 Low: ECC Detection Unreliable in VMs
- VM detection via `/sys/class/dmi/id/product_name` and `/proc/cpuinfo` hypervisor bit; three states: Physical+ECC / Physical+no ECC / VM

---

## v2.2.0 (2026-02-17) — **"Git Sync: Bidirectional Multi-Repo"**

### ✨ New Feature: Bidirectional Git Sync

Full GitHub/Gitea integration for Docker Compose stacks — no external tool required.

| Direction | Trigger | Effect |
|---|---|---|
| Pull ← Git | Manual / Auto | Clone or pull repo, update local compose file |
| Deploy ← Git | Manual | `docker compose up -d` from repo compose file |
| Export → Git | Manual | Snapshot running containers as `docker-compose.yml` |
| Push → Git | Manual | `git commit + push` compose file to remote |

- Multi-repo syncs with per-sync credential references, auto-sync intervals, commit author identity
- Credential store (`git_credentials` table): PAT via `GIT_ASKPASS`, SSH key via `GIT_SSH_COMMAND`
- New backend: `git_sync_repos.go` — full CRUD + pull/push/deploy/export endpoints
- New frontend: `git-sync.html` (956 lines) — three-tab layout, per-sync cards, PAT setup wizard
- Legacy single-repo config fully preserved on "Legacy Config" tab

---

## v2.1.1 (2026-02-17) — **"Security, Stability & Architecture"**

### 🔴 Showstopper Fix: ZFS-Docker Boot Race (Critical)
- Hard systemd gate (`dplaneos-zfs-mount-wait.service`): polls until every configured pool is `ONLINE`, mounted, and writable
- `dplaned.service` and `docker.service` both `Require=` this gate — cannot start without it
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

## v2.1.0 (2026-02-15) — **"ZFS-Docker Integration"**

### ⚡ Safe Container Updates (Killer Feature)

`POST /api/docker/update` — atomic container updates with ZFS data protection:
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

## v2.0.0 (2026-02-12) — **"Ground-Up Rewrite"**

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

## v1.14.0-OMEGA (2026-02-01) — **"OMEGA Edition"**

First fully production-ready PHP release. Fixes 7 critical infrastructure bugs.

1. **www-data sudo permissions missing** (CRITICAL)
2. **SQLite write permissions** (CRITICAL)
3. **Login loop on cold start** (HIGH)
4. **API timeout handling** (HIGH)
5. **Silent session expiry** (MEDIUM)
6. **No loading feedback** (LOW)
7. **Style flash on load** (LOW)

---

## v1.12.0 (2026-01-31) — **"The Big Fix"**

45 vulnerabilities from comprehensive penetration test — 10 Critical, 7 High fixed.

---

## v1.11.0 (2026-01-31) — **"Vibecoded Security Theater Fix"**

- `execCommand()` checked if string `"escapeshellarg"` appeared in command, not whether arguments were actually escaped — 108 vulnerable call sites. Complete rewrite with strict command whitelisting.

---

## v1.10.0 (2026-01-31) — **"Smart State Polling & One-Click Updates"**

- ETag-based smart polling (95% bandwidth reduction, 88% CPU reduction)
- ZFS snapshot-based update system with automatic rollback
- License: MIT → PolyForm Noncommercial License 1.0.0

---

## v1.9.0 (2026-01-30) — **"RBAC & Security Fixes"**

- Role-Based Access Control: Admin, User, Readonly roles
- 7 critical security fixes including session fixation, wildcard sudoers, Docker Compose YAML injection

---

## v1.8.0 (2026-01-28) — **"Power User Release"**

- File browser, ZFS native encryption, system service control, real-time monitoring — all 14 tabs functional

---

## v1.7.0 (2026-01-28) — **"The Paranoia Update"**

- UPS/USV management (NUT), automatic snapshot scheduling, system log viewer

---

## v1.6.0 (2026-01-28) — **"Disk Health & Notifications"**

- SMART monitoring, disk replacement tracking, notification center

---

## v1.2.0 — **"Initial Public Release"**

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
