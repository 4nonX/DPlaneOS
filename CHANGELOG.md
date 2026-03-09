# D-PlaneOS Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/), and this project adheres to [Semantic Versioning](https://semver.org/).

---

## v4.3.0 (2026-03-09) — "Automation"

Upgrade from: v4.2.0 — Drop-in upgrade via `sudo bash install.sh --upgrade`

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
  Cron jobs call this endpoint enabling Go-side hooks — snapshot, retain, replicate.

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

## v4.2.0 (2026-03-09) — "Disk Lifecycle"

Upgrade from: v4.1.2 — Drop-in upgrade via `sudo bash install.sh --upgrade`

### Architecture

This release implements the four pillars of disk lifecycle management —
the foundation required for serious NAS infrastructure:

**1. Disk Discovery (enriched)**
`GET /api/system/disks` now returns stable identifiers for every disk:
`by_id_path` (`/dev/disk/by-id/wwn-0x…`), `by_path_path`, `wwn`, `size_bytes`,
`rpm`, `pool_name`, `health`, `temp_c`. Type detection extended to SAS and USB.
Pool membership and per-vdev health resolved from a single `zpool status -P -v`
pass at discovery time.

**2. Device Renaming / Stable Identifiers (enforced)**
Pool creation via the UI now enforces `/dev/disk/by-id/` paths — matching
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
  POST to `http://127.0.0.1:9000/api/internal/disk-event` via curl — replacing
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
- **Background monitor**: `CheckMountStatus()` implemented — write-tests each
  pool's mountpoint every 60 seconds, broadcasts `mountError` on failure.
- **Disk temperature monitoring**: reads `/sys/class/hwmon/` sensors every
  5 minutes, falls back to `smartctl`, broadcasts `diskTempWarning` at 45°C
  warning / 55°C critical thresholds.

### Fixed

- `diskTempWarning` WebSocket event was subscribed in frontend but never
  broadcast by daemon — now implemented end-to-end.
- `CheckMountStatus` was an empty stub — now performs real write-test.
- Pool creation accepted raw `/dev/sdX` paths that become invalid after
  reboot — now auto-promotes to by-id or rejects with actionable error.
- Disk type detection did not distinguish SAS from HDD, or USB from SATA —
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

## v4.1.2 (2026-03-09) — "Completeness"

Upgrade from: v4.1.1 — Drop-in upgrade via `sudo bash install.sh --upgrade`

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
  - `GET /api/system/updates/check` — runs `apt-get update` + `apt list
    --upgradable`, returns structured package list with security flag, non-blocking via job queue
  - `POST /api/system/updates/apply` — runs `apt-get upgrade -y`, non-blocking
  - `POST /api/system/updates/apply-security` — security-only upgrade via
    `unattended-upgrades`, non-blocking
  - `GET /api/system/updates/daemon-version` — checks GitHub Releases API,
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

## v4.1.1 (2026-03-09) — "Design System"

### Changed

- **Design system adoption (all pages):** 27 pages previously defined
  per-file `const btnPrimary / btnGhost / btnDanger / inputStyle` objects,
  producing inconsistent padding, missing hover states, and divergent font
  weights across the UI. All removed and replaced with the CSS design system:
  `.btn .btn-primary`, `.btn .btn-ghost`, `.btn .btn-danger`, `.btn-sm`,
  `.input`, `.data-table`.

- **New `.tabs-line` CSS variant:** The existing `.tabs` is a pill/segment
  control. Pages use an underline tab pattern — this is now a first-class
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

## v4.1.0 (2026-03-08) — "Terminal"

### Feature: Embedded PTY Terminal

- **New `/ws/terminal` WebSocket endpoint (daemon):** Spawns a `bash --login` PTY via `creack/pty` and pipes stdin/stdout over WebSocket. Authenticated by the global `sessionMiddleware` — same session validation as all other endpoints. Each connection gets its own isolated PTY; connections are torn down cleanly when the WebSocket closes.
- **Terminal resize support:** Client sends `{"type":"resize","cols":N,"rows":N}` messages; daemon calls `pty.Setsize()` so shell-aware programs (vim, htop, man) render correctly at any window size.
- **New `TerminalPage` (frontend):** Full xterm.js terminal (`@xterm/xterm` v5) with `FitAddon` (auto-resize) and `WebLinksAddon` (clickable URLs). Colour scheme matches the D-PlaneOS dark theme. Reconnect and Clear buttons in the title bar. Connection status indicator (green/amber/red dot).
- **Sidebar:** Terminal added to the System group (`terminal` icon).
- **Font regression fixed:** `index.html` was loading fonts from `fonts.googleapis.com`. All three fonts (Outfit, JetBrains Mono, Material Symbols Rounded) now load exclusively from `/assets/fonts/` — zero external requests at runtime, fully airgap-safe.

### Added
- `daemon/internal/handlers/terminal_handler.go` — PTY handler
- `daemon/vendor/github.com/creack/pty` v1.1.24
- `app-react/src/pages/TerminalPage.tsx`
- `@xterm/xterm`, `@xterm/addon-fit`, `@xterm/addon-web-links` npm dependencies

### Fixed
- `index.html` CDN font references removed; replaced with `/assets/fonts/fonts.css`
- `fonts.css` updated to use absolute paths (`/assets/fonts/...`)
- Dead `react-vendor` Rollup manual chunk removed from `vite.config.ts`

---


---


## v4.0.0 (2026-03-08) — **"React SPA"**

Upgrade from: v3.3.3 — Drop-in upgrade via `sudo ./scripts/upgrade-with-rollback.sh`

### ⚡ Architecture: Full React SPA Migration

The entire frontend has been rewritten from scratch. 41 standalone vanilla HTML/JS pages replaced by a single-page application built on React 19 + TypeScript + Vite + TanStack Query. The daemon is unchanged — this is a pure frontend replacement.

**Stack:**
- React 19 + TypeScript (0 type errors at build)
- TanStack Router (type-safe navigation — TS error on unregistered routes)
- TanStack Query (data fetching, caching, background refresh)
- Zustand (auth state, WebSocket hub)
- Vite build (tree-shaken, code-split by route)

**37 pages implemented across 10 phases:**

| Phase | Pages |
|-------|-------|
| 0 — Scaffold | AppShell, Sidebar, TopBar, auth/session infrastructure |
| 1 — Core Read-Only | Dashboard, Reporting, Hardware, Logs, Monitoring |
| 2 — Storage | Pools, Shares, NFS, Snapshot Scheduler, Replication |
| 3 — Docker | Docker (containers + compose tabs), Modules |
| 4 — Files | Files, ACL, Removable Media |
| 5 — Users & Security | Users (users/groups/roles tabs), Security, Directory (LDAP) |
| 6 — Network & System | Network, Settings, Alerts, Firewall, Certificates, UPS, Power, IPMI, HA |
| 7 — DevOps | Git Sync, GitOps, Cloud Sync |
| 8 — Admin | Audit, Support, Updates |
| 9 — Wizards | Setup Wizard |
| 10 — WebSocket | Real-time push for Docker state, pool health, disk temps |

### 🐛 Bug Fix: NFS Routes Not Registered (Daemon)

`nfs_handler.go` existed but its routes were never registered in `main.go`. NFS CRUD (`/api/nfs/exports`, `/api/nfs/status`, `/api/nfs/reload`) were silently unreachable in all previous v3.x releases. Routes are now registered.

### 🏗️ Infrastructure: Fully Offline Fonts

All three fonts are bundled in `app/assets/fonts/` — zero external requests at runtime:

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
- `lib.fakeHash` → `nixpkgs.lib.fakeHash` (was not in scope in `eachSystem` block — would have caused eval error)

### ✅ Compatibility

Drop-in replacement for v3.3.2. Daemon is unchanged. No schema changes, no migrations, no configuration changes required. The new frontend serves from the same `/opt/dplaneos/app` path.


---

## v3.3.3 (2026-03-07) — **"Async & Governance"**

Upgrade from: v3.3.2, v3.3.1, v3.3.0, or any v3.x — Drop-in upgrade via `sudo ./scripts/upgrade-with-rollback.sh`

### ⚖️ Governance: License Changed to AGPLv3

- **License changed from PolyForm Shield 1.0.0 to GNU Affero General Public License v3.0 (AGPLv3):** D-PlaneOS is now licensed under an OSI-approved open-source license. The AGPLv3 permits free use, modification, and distribution. Modified versions run as a network service must make their source available to users of that service. SPDX identifier: `AGPL-3.0-only`.

- **NixOS users — remove `allowUnfreePredicate`:** Under PolyForm Shield the Nix `meta.license` was set to `licenses.unfree`, requiring `allowUnfreePredicate` or `allowUnfree = true`. AGPLv3 is a free software license. Remove any `allowUnfreePredicate` blocks referencing `dplaneos-daemon` — they are now dead code. The flake's `meta.license` is updated to `licenses.agpl3Only`.

- **Contributor License Agreement introduced:** `CLA-INDIVIDUAL.md` and `CLA-ENTITY.md` added to the repository root. The CLA grants the maintainer the right to re-license commercially in the future; contributors retain full ownership. Signing is handled via CLA Assistant bot on pull requests.

### ⚡ Feature: Async Job Store (Daemon)

- **New `daemon/internal/jobs/jobs.go` package:** In-process, in-memory job store for long-running operations. Each job has a UUID, status (`running` → `done` / `failed`), result payload, and error string. Concurrent-safe. State is ephemeral — does not survive daemon restarts, acceptable because all jobs are short-lived.

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

### ⚡ Feature: Frontend Async Polling — `ui.pollJob()`

- **New `DPlaneUI.pollJob()` in `ui-components.js`:** Single consistent polling loop for all async operations. Shows loading overlay immediately, polls `GET /api/jobs/{id}` every 2 seconds, retries on transient network errors, enforces 30-minute hard timeout, hides overlay in all exit paths.

- **`docker.html` — 4 operations updated:** `composeUp`, `composeDown`, `pullImage`, `updateContainer` now dispatch via `ui.pollJob()`.

- **`replication.html` — 2 operations updated:** `runTask` and `startReplication` dispatch via `ui.pollJob()`. Replication start button now correctly restores its icon on job completion.

### 🐛 Bug: Navigation Stub Redirects and Missing NFS Entry

- **5 nav stub redirects replaced with direct links** in `nav-shared.js`: Interfaces → `network.html#interfaces`, DNS → `network.html#dns`, Routing → `network.html#routing`, System Settings → `settings.html`, File Upload → `files.html`. Stubs retained for existing bookmarks.

- **`data-page` mismatch fixed:** File Upload nav entry had `data-page="files-enhanced"`, breaking the active-page highlight on `files.html`. Fixed to `data-page="files"`.

- **NFS Exports added to nav:** `nfs.html` has been a complete, functional NFS management page since v3.x but had no navigation entry — unreachable without a direct URL. Now listed under Storage → NFS Exports (between Shares and Replication).

### 🏗️ NixOS: `ota-module.nix` Options

- **`options.services.dplaneos.ota` namespace added:** Two new tunable options: `ota.enable` (default: `true`) to disable the health-check timer independently of the daemon, and `ota.healthCheckDelay` (default: `"90s"`) to tune the post-boot wait. Module is gated on `lib.mkIf (cfg.enable && cfg.ota.enable)`.

### ✅ Compatibility

Drop-in replacement for v3.3.2 with one exception: the 8 async endpoints now return `{"job_id":"..."}` with HTTP 202 instead of blocking. All other API surface, schema, and configuration unchanged.

**NixOS users only:** Remove any `allowUnfreePredicate` or `allowUnfree` blocks referencing `dplaneos-daemon`.

---

## v3.3.2 (2026-03-01) — **"Runtime fixes"**

Upgrade from: v3.3.1, v3.3.0, or any v3.x — Drop-in upgrade via `sudo ./scripts/upgrade-with-rollback.sh`

### 🔒 Security: Eliminated `bash -c` Shell Construction in Replication

- **`replication_remote.go` — shell injection vector removed:** Both the normal and resume-token replication paths previously built a complete shell pipeline string via `fmt.Sprintf` and executed it with `executeCommand("/bin/bash", []string{"-c", fullCmd})`. Despite upstream input validation, string-formatted shell commands are an inherently fragile security boundary. The entire replication pipeline (`zfs send` → optional `pv` → `ssh recv`) is now implemented as three discrete `exec.Command` processes connected via Go `io.Pipe` in a new `execPipedZFSSend()` helper. No shell is invoked at any point.

- **Resume token validation added:** ZFS resume tokens are now validated with `isValidResumeToken()` (alphanumeric + base64 characters only, max 4096 bytes) before being used as a command argument. Previously the token was passed directly from the SSH remote into `fmt.Sprintf`.

- **Error responses no longer leak command strings:** The `"command": fullCmd` field previously included in replication failure responses exposed the full constructed shell command to API callers. This field has been removed.

### 🔒 Security: iSCSI Authentication Default Made Explicit

- **`iscsi.go` — `authentication=0` is now an explicit opt-out, not a silent default:** Every new iSCSI target previously had CHAP authentication disabled silently. A new `require_chap` boolean field has been added to `ISCSICreateRequest`. When `require_chap: true`, the TPG is created with `authentication=1`. When `require_chap: false` (the current default for backward compatibility), `authentication=0` is still set but a `SECURITY NOTICE` log line is emitted, making the decision auditable. This is a **non-breaking change** — existing API callers that do not include `require_chap` behave identically to before.

### 🐛 Bug: LDAP `TriggerSync` - Full Implementation

- **`ldap.go` + `ldap/client.go` — sync now actually syncs:** `POST /api/ldap/sync` previously connected to the LDAP server, bound the service account, and immediately returned `{"success": true}` with 0 users found/created/updated. No directory data was read or written. This has been replaced with a full implementation:
  - New `SyncAll()` method on the LDAP client performs a wildcard search against the configured `BaseDN` using the configured `UserFilter`, fetches all matching entries, and retrieves group memberships for each
  - `TriggerSync` upserts each user into the `users` table (`source='ldap'`, empty `password_hash`) applying group→role mapping via the existing `GroupMappings` config
  - Response now returns real counts: `users_found`, `users_created`, `users_updated`, `users_skipped`, and `errors` per user

### 🐛 Bug: Version String Never Embedded in Binary

- **`daemon/cmd/dplaned/main.go` — `Version` changed from `const` to `var`:** The `Version` identifier was declared as a `const`, but Go's `-ldflags "-X main.Version=..."` mechanism only works with package-level `var` declarations. As a result, all previous release builds reported `version: "dev"` at `/health` and in startup logs regardless of the version tag. Changed to `var` — version is now correctly embedded at build time and visible in the health endpoint.

- **README:** Removed "No other NAS OS does this" from the container update description (snapshot+rollback is standard practice in the NAS space). Removed unsupported "100× faster" benchmark claim from replication description. Changed "injection-hardened" to "allowlist-based input validation" (more accurate). Added explicit HA limitations section. Fixed LDAP feature list to reflect actual implementation.
- **INSTALLATION-GUIDE:** Removed "enterprise NAS" language.
- **SECURITY.md:** Updated command execution description to reflect the `bash -c` removal. Added HA and LDAP known limitations to the Known Limitations section.
- **THREAT-MODEL.md:** Updated T1 (Command Injection) to document the replication fix. Added T13 (HA Split-Brain) as a new threat entry with HIGH residual risk rating and mitigation guidance.
- **ADMIN-GUIDE:** Updated LDAP sync documentation to accurately describe the full-directory sync behavior.
- **HA `cluster.go`:** Package comment expanded with explicit NO-STONITH, NO-automatic-failover, NO-split-brain-protection, and NO-quorum warnings.

### ✅ Compatibility

Drop-in replacement for v3.3.1. No schema changes, no migrations, no configuration changes required. The `require_chap` field in iSCSI create requests defaults to `false` — existing API integrations are unaffected.

---

## v3.3.1 (2026-02-25) — **"Universal Compatibility"**

Upgrade from: v3.3.0, v3.2.1, or any v3.x — Drop-in upgrade via `sudo ./scripts/upgrade-with-rollback.sh`

### 🐛 Bug Fixes

- **Ubuntu readonly variable crash (Phase 0):** `install.sh`, `get.sh`, `scripts/pre-flight.sh`, and `scripts/system-audit.sh` previously sourced `/etc/os-release` directly, which caused Ubuntu to abort with `/etc/os-release: line 4: VERSION: readonly variable` and fail at Phase 0 before any installation occurred. All four files now use safe `grep`-based extraction into scoped variables — the OS-managed `VERSION` variable is never touched.

- **`TERM environment variable not set` warning:** `install.sh` called `clear` unconditionally at startup and on the completion screen. In non-interactive contexts (serial console, VM without TTY, piped install) this emitted a `TERM` warning that polluted output and confused users expecting a clean install log. Both `clear` calls are now guarded with `[ -n "$TERM" ]`.

### 🐧 NixOS Compatibility

- **NixOS no longer causes install termination:** `install.sh` Phase 0 OS detection previously treated any unrecognised `$ID` as a fatal error. NixOS is now detected as a named case, emits an informational warning directing users to `nixos/NIXOS-INSTALL-GUIDE.md`, and continues rather than aborting. All NixOS-specific files remain untouched under `nixos/`.

### 🚀 Phase 12: Dynamic IP Notification

- **Access URL displayed at completion:** Phase 12 now calculates the primary IPv4 address via `hostname -I` and displays it in a clearly bordered completion box — `http://<PRIMARY_IP>` — along with a notice that the VM screen may remain black after install. Eliminates the most common post-install support question.

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
- License: MIT → GNU Affero General Public License v3.0 (AGPLv3)

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
