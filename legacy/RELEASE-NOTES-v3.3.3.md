# D-PlaneOS v3.3.3 Release Notes

**Release Date:** 2026-03-07
**Type:** Frontend + Navigation Fix Release
**Codename:** "UI Consistency"

---

## Governance Changes

### License: PolyForm Shield 1.0.0 -> GNU AGPLv3

D-PlaneOS is now licensed under the **GNU Affero General Public License v3.0
(AGPLv3)**. This is an OSI-approved open-source license. Key effects:

- NixOS no longer classifies D-PlaneOS as "unfree" - no `allowUnfreePredicate`
  configuration is required
- The `legacy/` directory retains its original PolyForm Noncommercial license
  for historical reference
- All active source files (daemon, frontend, scripts, NixOS config) are now
  AGPL-3.0-only

### Contributor License Agreement (CLA)

Two CLA documents are now included in the repository root:

- `CLA-INDIVIDUAL.md` : for solo contributors
- `CLA-ENTITY.md` : for organisations

Contributors are asked to sign the individual CLA via the CLA Assistant bot
on GitHub before their first pull request is merged. The CLA grants the
maintainer the right to re-license the project in the future while contributors
retain full ownership of their contributions.

---

## Overview

v3.3.3 is a focused frontend release addressing two categories of issues:

1. **Blocking HTTP operations : 8 long-running API endpoints returned responses only after the operation completed (up to 10 minutes).
 The browser held open HTTP connections for the entire duration, causing timeout errors on large image pulls, ZFS send/receive operations, and compose deployments. These are now fully async with in-UI job polling.

2. **Navigation inconsistencies : Several nav entries pointed to 4-line redirect stub pages rather than real pages, one fully implemented page (NFS Exports) had no nav entry at all, and a `data-page` mismatch caused the nav highlight to break on the File Explorer page.

No backend changes. No schema changes. Drop-in upgrade from v3.3.2.

---

## What Changed

### 1. Async Job Polling : `ui.pollJob()`

**Affected file:** `app/assets/js/ui-components.js` : `pollJob()` method added to `DPlaneUI` class

**The problem:** The following operations called their API endpoint synchronously and blocked until completion:

| Endpoint | Typical duration |
|----------|-----------------|
| `POST /api/docker/pull` | 30s – 10min |
| `POST /api/docker/update` | 1 – 5min |
| `POST /api/docker/compose/up` | 10s – 5min |
| `POST /api/docker/compose/down` | 5 – 60s |
| `POST /api/replication/send` | Seconds – hours |
| `POST /api/replication/send-incremental` | Seconds – hours |
| `POST /api/replication/receive` | Seconds – hours |
| `POST /api/backup/rsync` | Minutes – hours |

These endpoints were made async in v3.3.2 on the backend - they now return `{"job_id":"<uuid>"}` immediately.
 v3.3.3 completes the frontend side.

**The fix:** New `DPlaneUI.pollJob()` method added to `ui-components.js`:

```javascript
const job = await ui.pollJob(jobId, 'Loading message...');
// job.status === 'done' | 'failed'
// job.result  : backend result payload
// job.error   : error message if failed
```

Behaviour:
- Calls `ui.showLoading(message)` immediately, keeping the existing overlay UX
- Polls `GET /api/jobs/{id}` every 2 seconds
- Returns when status is `done` or `failed`
- On network blips: retries silently (no error to user)
- Hard timeout: 30 minutes, returns `{status:'failed', error:'Timed out...'}`
- Calls `ui.hideLoading()` in all exit paths

**Affected pages:**

`app/pages/docker.html` : 4 functions updated:
- `composeUp` : polls for stack start completion
- `composeDown` : polls for stack stop completion
- `pullImage` : polls for image pull completion
- `updateContainer` : polls for container update (pull + recreate) completion

`app/pages/replication.html` : 2 functions updated:
- `runTask` : polls for scheduled task run completion
- `startReplication` : polls for manual remote replication completion; button now correctly restores its Material Symbol icon on completion

UX is identical to before. The user sees the loading overlay with a descriptive message, followed by a success or error toast. The output panel is populated from `job.result` on completion.

---

### 2. Navigation: Stub Pages -> Direct Links

**Affected file:** `app/assets/js/nav-shared.js`

Five nav entries previously linked to 4-line `meta refresh` stub files:

| Nav entry | Was | Now |
|-----------|-----|-----|
| Interfaces | `network-interfaces.html` | `network.html#interfaces` |
| DNS | `network-dns.html` | `network.html#dns` |
| Routing | `network-routing.html` | `network.html#routing` |
| System Settings | `system-settings.html` | `settings.html` |
| File Upload | `files-enhanced.html` | `files.html` |

The stub files themselves are **retained** - existing bookmarks continue to work via their `meta refresh` redirect. Only the nav hrefs are fixed.

The `data-page="files-enhanced"` attribute on the File Upload entry has been corrected to `data-page="files"`, so the nav highlight now activates correctly on `files.html`.

---

### 3. Navigation: NFS Exports Added

**Affected file:** `app/assets/js/nav-shared.js`

`nfs.html` is a complete, fully-functional NFS export management page (454 lines) that existed in the codebase since v3.x but had no nav entry. It is now listed under **Storage : NFS Exports**, between Shares and Replication.

The `pageMap` is updated so visiting `nfs.html` correctly activates the Storage section and highlights the NFS Exports entry in the sub-nav.

---

## What Was NOT Changed

- Backend daemon: no changes
- Database schema: no changes
- API contracts: no changes
- Install/upgrade scripts: no changes
- The 5 redirect stub pages themselves are retained for bookmark compatibility

---

## Files Changed

### Modified
- `app/assets/js/ui-components.js` : `pollJob()` method added to `DPlaneUI` class
- `app/pages/docker.html` : `composeUp`, `composeDown`, `pullImage`, `updateContainer` use `pollJob`
- `app/pages/replication.html` : `runTask`, `startReplication` use `pollJob`
- `app/assets/js/nav-shared.js` : stub hrefs fixed; NFS Exports added; `files-enhanced` data-page corrected
- `VERSION` : `3.3.2` -> `3.3.3`
- `CHANGELOG.md` : v3.3.3 entry added
- All 33 HTML pages and JS asset files : `?v=3.3.x` -> `?v=3.3.3`

### Added
- `RELEASE-NOTES-v3.3.3.md` : this file

---

## Upgrade Notes

Drop-in upgrade from v3.3.2:

```bash
sudo bash /opt/dplaneos/scripts/upgrade-with-rollback.sh
```

No configuration changes required. No service restarts required beyond the daemon upgrade.
