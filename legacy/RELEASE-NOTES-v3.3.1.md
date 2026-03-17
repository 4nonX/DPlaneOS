# D-PlaneOS v3.3.1 Release Notes

**Release Date:** 2026-02-27
**Type:** Protocol Completeness + Compatibility Release

---

## Overview

v3.3.1 completes NAS protocol coverage and fixes cross-distro compatibility issues that
blocked production use on non-NixOS systems. NFS and iSCSI are now fully implemented -
not stubbed, not claimed - with real backend handlers, database persistence, `/etc/exports`
generation, and complete management UIs. A deep install-time wiring audit also fixed 8
bugs that caused silent failures on fresh installs.

All changes are backward-compatible. No database migrations required for existing installs.

---

## What Works Fully Out of the Box (after `sudo ./install.sh` on Ubuntu 24.04)

- **ZFS** pools, datasets, snapshots, encryption, replication, scrubs, SMART monitoring
- **File explorer**, chunked uploads, ACL manager
- **Docker** container management, Compose stacks, sandbox clones, safe container updates
- **RBAC** (users, groups, roles, permissions), LDAP / Active Directory, API tokens, 2FA
- **Network** configuration (interfaces, bonds, VLANs, routing, DNS)
- **System** settings, logs, audit trail, IPMI / sensors
- **GitOps** state reconciliation, git-sync repositories
- **Alerts** (SMTP, Telegram, webhooks - all wired to real APIs)
- **Firewall**, certificates, HA cluster, cloud sync (rclone), system metrics, history charts
- **NixOS** config guard (NixOS only)

## Optional Protocols (install separately, fully managed once installed)

| Protocol | Install command | Notes |
|---|---|---|
| SMB / Windows shares | `sudo apt install samba` | D-PlaneOS writes and manages `smb.conf` |
| AFP / Time Machine (macOS) | included with Samba | Uses Samba's `fruit` module - no extra package |
| NFS exports | `sudo apt install nfs-kernel-server` | D-PlaneOS writes `/etc/exports` and runs `exportfs -ra` |
| iSCSI block targets | `sudo apt install targetcli-fb` | D-PlaneOS manages LIO targets via `targetcli` |
| UPS monitoring | `sudo apt install nut` | D-PlaneOS reads status via `upsc` |

Each optional protocol is auto-detected at runtime. If not installed, the UI shows a clear
message with the install command rather than returning a 500 error.

---

## New: Full NFS Export Management

**Previously:** NFS was listed in UI subtitles and documentation but had no backend implementation.
Any NFS-related API call would fail silently.

**Now:** Complete NFS server management:

- **Database-backed exports** - `nfs_exports` table stores path, clients, options, and enabled state
- **`/etc/exports` generation** - D-PlaneOS owns and regenerates the file from the database on every change; a comment warns against manual edits
- **Live apply** - every create/update/delete/toggle immediately calls `exportfs -ra` - no service restart needed
- **Input validation** - path (absolute, exists, no `..`), clients (IP/subnet/hostname/wildcard allowlist regex), and options (allowlist regex) all validated before any write
- **Graceful degradation** - all endpoints check for `nfs-kernel-server` first and return a structured "not installed" response with the install command if missing
- **Dedicated UI** - `nfs.html` with exports list, enable/disable toggles, add-export form, one-click option presets, and inline security notes about `no_root_squash` and `*` wildcards
- **`install.sh` integration** - bootstraps `/etc/exports` and enables `nfs-kernel-server` if already present at install time

**API endpoints added:** `GET/POST /api/nfs/exports`, `PUT/DELETE /api/nfs/exports/{id}`, `GET /api/nfs/status`, `POST /api/nfs/reload`

---

## Fixed: iSCSI Target Management

iSCSI had a backend handler and routes but four bugs made it unusable in production:

1. **Hardcoded NixOS binary path** - `GetISCSIZvolList` called `/run/current-system/sw/bin/zfs` (NixOS-specific path). Fixed to use plain `zfs` from `$PATH`, which works on all platforms.

2. **Orphaned storage objects on delete** - `DeleteISCSITarget` removed the target but left the backing `/backstores/block` object in `targetcli`, causing accumulation of ghost entries. Fixed to clean up the backstore after every target deletion.

3. **Hard errors when `targetcli` not installed** - all five write-path handlers (`GetISCSIStatus`, `GetISCSITargets`, `CreateISCSITarget`, `DeleteISCSITarget`, `GetISCSIZvolList`) now check `which targetcli` first and return a structured `{"installed": false, "message": "..."}` response rather than crashing with an exec error.

4. **`install.sh` integration** - enables and starts the `target` systemd service if `targetcli-fb` is already installed at install time.

---

## Fixed: Install-Time Wiring Bugs

Eight bugs found and fixed during a full wiring audit of all 256 API routes against all 50 UI pages:

- **`install.sh` Phase 0** - Ubuntu readonly variable crash: sourcing `/etc/os-release` directly caused `/etc/os-release: line 4: VERSION: readonly variable` and hard-aborted the install at Phase 0. All four install scripts now use safe `grep`-based extraction.
- **`install.sh` Phase 0** - `TERM environment variable not set` warning on serial/headless installs. Both `clear` calls are now guarded with `[ -n "$TERM" ]`.
- **NixOS detection** - previously aborted the install with a fatal error on unrecognised distro IDs. NixOS is now a named case that emits a warning and continues.
- **Phase 12** - access URL now displayed at completion (`http://<PRIMARY_IP>`) so new users don't have to guess the address.
- **CI guard** - Syncthing conflict files (`.sync-conflict-*.go`) caused duplicate symbol build failures in CI. Both `validate.yml` and `release.yml` now check for and fail early on conflict files.
- **Samba wiring** - Samba config was being managed by the UI but the daemon was never actually writing `smb.conf`. Fixed: daemon now generates the config and `smbcontrol` reloads on every share change.
- **Realtime service** - broken import reference caused the realtime metrics WebSocket to fail on startup. Fixed.
- **IPMI page** - was calling deprecated/removed API endpoints. Updated to current endpoint paths.

---

## UI Consolidation

Navigation reduced from 49 entries across 7 sections to 23, without removing any functionality.
Pages that were separate nav entries are now tabs within their parent page:

| Removed from nav | Now found at |
|---|---|
| Snapshots | ZFS Pools → Advanced tab |
| Quotas | ZFS Pools → Quotas tab |
| Snapshot Scheduler | ZFS Pools → Scheduler tab |
| ZFS Encryption | ZFS Pools → Encryption tab |
| ACL Manager | File Explorer → ACL Manager tab |
| File Upload | File Explorer → Enhanced Upload tab |
| Interfaces / DNS / Routing | Network Settings (tabs) |
| Groups | Users & Groups (tab) |
| Roles & Permissions | Users & Groups (tab) |
| Firewall | Security Settings (tab) |
| Certificates | Security Settings (tab) |
| System Settings | Settings → Configuration tab |
| Power Management | Settings → Power tab |
| Monitoring | Reporting & Hardware (tab) |
| Hardware | Reporting & Hardware (tab) |
| IPMI | Reporting & Hardware (tab) |
| Removable Media | Reporting & Hardware (tab) |
| System Logs | Reporting & Hardware (tab) |

All redirect stubs remain in place so existing bookmarks and direct links continue to work.

---

## Files Changed

### New Files
- `daemon/internal/handlers/nfs_handler.go` - Full NFS export management handler
- `app/pages/nfs.html` - NFS management UI
- `RELEASE-NOTES-v3.3.1.md` - This file

### Modified Files
- `daemon/internal/handlers/iscsi.go` - 4 bug fixes (path, leak, graceful degradation, install integration)
- `daemon/cmd/dplaned/main.go` - 7 NFS routes registered
- `app/pages/pools.html` - Quotas tab (full UI), Encryption tab (full UI), structural fix (scheduler tab now inside `<main>`)
- `app/assets/js/nav-shared.js` - Navigation consolidated from 49 → 23 entries; full rewrite to eliminate accumulated duplicate blocks
- `install.sh` - NFS and iSCSI service bootstrap; 4 install-time bug fixes
- `INSTALLATION-GUIDE.md` - NFS and iSCSI added to optional services table
- `README.md` - Sharing section updated; route count corrected

---

## Upgrade Notes

Drop-in upgrade from v3.3.0 or any v3.x. No configuration changes required.

```bash
sudo bash /opt/dplaneos/scripts/upgrade-with-rollback.sh
```

If you want NFS or iSCSI support on an existing install, install the relevant package
after upgrading - D-PlaneOS will detect and manage it automatically:

```bash
# NFS
sudo apt install nfs-kernel-server

# iSCSI
sudo apt install targetcli-fb
```
