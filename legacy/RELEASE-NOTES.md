# Release Notes

**Release Date:** 2026-02-27
**Type:** Protocol Completeness + Compatibility Release

---

## Overview

This release completes NAS protocol coverage and fixes cross-distro compatibility issues that blocked production use on non-NixOS systems. NFS and iSCSI are now fully implemented — not stubbed, not claimed — with real backend handlers, database persistence, `/etc/exports` generation, and complete management UIs. A deep install-time wiring audit also fixed 8 bugs that caused silent failures on fresh installs.

All changes are backward-compatible. No database migrations required for existing installs.

---

## What's New

### Full NFS Export Management

NFS was previously listed in the UI and documentation but had no backend implementation — any NFS-related API call would fail silently.

Now complete:

- **Database-backed exports** — `nfs_exports` table stores path, clients, options, and enabled state
- **`/etc/exports` generation** — D-PlaneOS owns and regenerates the file from the database on every change; a comment warns against manual edits
- **Live apply** — every create / update / delete / toggle immediately calls `exportfs -ra` — no service restart needed
- **Input validation** — path (absolute, exists, no `..`), clients (IP / subnet / hostname / wildcard allowlist regex), and options (allowlist regex) all validated before any write
- **Graceful degradation** — all endpoints check for `nfs-kernel-server` first and return a structured "not installed" response with the install command if missing
- **Dedicated UI** — `nfs.html` with exports list, enable/disable toggles, add-export form, one-click option presets, and inline security notes about `no_root_squash` and `*` wildcards
- **`install.sh` integration** — bootstraps `/etc/exports` and enables `nfs-kernel-server` if already present at install time

**API endpoints added:** `GET/POST /api/nfs/exports`, `PUT/DELETE /api/nfs/exports/{id}`, `GET /api/nfs/status`, `POST /api/nfs/reload`

---

### Fixed: iSCSI Target Management

iSCSI had a backend handler and routes but four bugs made it unusable in production:

1. **Hardcoded NixOS binary path** — `GetISCSIZvolList` called `/run/current-system/sw/bin/zfs` (NixOS-specific). Fixed to use plain `zfs` from `$PATH`, which works on all platforms.
2. **Orphaned storage objects on delete** — `DeleteISCSITarget` removed the target but left the backing `/backstores/block` object in `targetcli`, accumulating ghost entries. Fixed to clean up the backstore after every deletion.
3. **Hard errors when `targetcli` not installed** — all five write-path handlers now check `which targetcli` first and return a structured `{"installed": false, "message": "..."}` response rather than crashing with an exec error.
4. **`install.sh` integration** — enables and starts the `target` systemd service if `targetcli-fb` is already installed at install time.

---

### Fixed: Install-Time Wiring Bugs

Eight bugs found and fixed during a full wiring audit of all 256 API routes against all 50 UI pages:

- **Ubuntu readonly variable crash (Phase 0)** — sourcing `/etc/os-release` directly caused a `VERSION: readonly variable` error that hard-aborted the install before anything was installed. All four install scripts now use safe `grep`-based extraction.
- **`TERM not set` warning** — `clear` calls are now guarded with `[ -n "$TERM" ]` to prevent noise on headless / serial installs.
- **NixOS detection** — previously aborted with a fatal error on unrecognised distro IDs. NixOS is now a named case that emits a warning and continues.
- **Phase 12 access URL** — install completion now displays `http://<PRIMARY_IP>` so new users don't have to guess the address.
- **CI guard** — Syncthing conflict files (`.sync-conflict-*.go`) caused duplicate symbol build failures. Both `validate.yml` and `release.yml` now check for and fail early on conflict files.
- **Samba wiring** — Samba config was being managed by the UI but the daemon was never actually writing `smb.conf`. Fixed.
- **Realtime service** — broken import reference caused the realtime metrics WebSocket to fail on startup. Fixed.
- **IPMI page** — was calling deprecated / removed API endpoints. Updated to current endpoint paths.

---

### UI Consolidation

Navigation reduced from 49 entries across 7 sections to 23, without removing any functionality. Pages that were separate nav entries are now tabs within their parent page.

All redirect stubs remain in place so existing bookmarks and direct links continue to work.

---

## Files Changed

### New Files
- `daemon/internal/handlers/nfs_handler.go` — Full NFS export management handler
- `app/pages/nfs.html` — NFS management UI
- `RELEASE-NOTES.md` — This file

### Modified Files
- `daemon/internal/handlers/iscsi.go` — 4 bug fixes
- `daemon/cmd/dplaned/main.go` — 7 NFS routes registered
- `app/pages/pools.html` — Quotas tab (full UI), Encryption tab (full UI), structural fix
- `app/assets/js/nav-shared.js` — Navigation consolidated from 49 → 23 entries
- `install.sh` — NFS and iSCSI service bootstrap; 4 install-time bug fixes
- `INSTALLATION-GUIDE.md` — NFS and iSCSI added to optional services table
- `README.md` — Sharing section updated; route count corrected

---

## Upgrade

Drop-in upgrade from any v3.x. No configuration changes required.

```bash
sudo bash /opt/dplaneos/scripts/upgrade-with-rollback.sh
```

To add NFS or iSCSI support to an existing install, install the relevant package after upgrading — D-PlaneOS will detect and manage it automatically:

```bash
# NFS
sudo apt install nfs-kernel-server

# iSCSI
sudo apt install targetcli-fb
```
