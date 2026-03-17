# D-PlaneOS v3.3.2 Release Notes

**Release Date:** 2026-03-01
**Type:** Security + Bug Fix Release
**Codename:** "Audit Response"

---

## Overview

v3.3.2 is a targeted security and correctness release prompted by an external code audit.
It addresses four specific findings: a shell-injection vector in the replication handler, a silent
insecure default in iSCSI target creation, a non-functional LDAP directory sync, and
overstated capability claims in documentation.

No new features are added. All changes are backward-compatible with v3.3.1.

---

## What Changed

### 1. Replication: `bash -c` Shell Pipeline Replaced with Go Pipes

**Affected file:** `daemon/internal/handlers/replication_remote.go`

**The problem:** The ZFS replication handler - for both normal sends and resume-token resumption - built
a complete shell pipeline string using `fmt.Sprintf` and executed it via `bash -c`:

```go
// Before - vulnerable pattern
fullCmd := fmt.Sprintf(
    "/usr/sbin/zfs send -t %s | /usr/bin/ssh %s %s /usr/sbin/zfs recv -s -F %s",
    token, strings.Join(sshArgs, " "), sshTarget, remoteDataset,
)
output, err := executeCommand("/bin/bash", []string{"-c", fullCmd})
```

Despite input validation (character blocklists, `isValidSSHUser`, snapshot name regex), constructing
shell command strings is an inherently brittle security boundary. A single gap in validation -
an edge case in the regex, an unexpected encoding, a future code path - collapses into
remote code execution.

Additionally, error responses included a `"command": fullCmd` field that exposed the full
constructed shell string to API callers.

**The fix:** A new `execPipedZFSSend()` helper connects `zfs send`, optional `pv` (bandwidth
throttling), and `ssh recv` as three separate `exec.Command` processes linked via Go `io.Pipe`.
Each argument is a discrete element in the process's `argv`. No shell is invoked at any point:

```go
// After - shell-free piped execution
output, err := execPipedZFSSend(
    sendArgs,                  // []string - argv for zfs send
    sshArgs, sshTarget,       // []string, string - argv for ssh
    []string{"recv", "-s", "-F", remoteDataset},
    rateLimitBytes,           // optional pv rate
)
```

ZFS resume tokens fetched from the remote are now validated with `isValidResumeToken()` (base64
alphabet only, max 4096 bytes) before use. The `"command"` field has been removed from error
responses.

**Impact:** No behavioral change for callers. The API contract is identical. Replication and
resume work the same way. The attack surface for shell injection in this path is eliminated.

---

### 2. iSCSI: Authentication Disabled Silently → Explicit Opt-Out

**Affected file:** `daemon/internal/handlers/iscsi.go`

**The problem:** Every new iSCSI target was created with `authentication=0` (CHAP disabled),
with only a `//nolint` comment as explanation. Operators had no way to know CHAP was off without
reading source code.

ACL-only authentication (IQN matching) is weaker than CHAP because IQNs are not secrets and can
be spoofed by any initiator that knows the expected IQN.

**The fix:** `ISCSICreateRequest` gains a `require_chap` boolean field:

```json
{
  "iqn": "iqn.2024-01.com.example:storage",
  "backing_dev": "/dev/zvol/tank/lun0",
  "portal_ip": "192.168.1.100",
  "portal_port": 3260,
  "require_chap": true
}
```

- `require_chap: true` → `authentication=1` (CHAP required before any initiator login)
- `require_chap: false` (default) → `authentication=0` + a `SECURITY NOTICE` log line

This is a **non-breaking change**. Existing API integrations that omit `require_chap` behave
identically to v3.3.1. The difference is that the insecure choice is now logged and visible.

**Recommended action for new targets:** Set `require_chap: true` and configure CHAP credentials
via the `POST /api/iscsi/acls` endpoint. Reserve `require_chap: false` for isolated networks
where initiator IQN spoofing is not a realistic threat.

---

### 3. LDAP Directory Sync: Stub Replaced with Real Implementation

**Affected files:** `daemon/internal/handlers/ldap.go`, `daemon/internal/ldap/client.go`

**The problem:** `POST /api/ldap/sync` connected to the LDAP server, bound the service account,
then immediately returned:

```json
{"success": true, "data": {"message": "Sync completed", "duration_ms": 12}}
```

…with zero users actually read or written. The `logSync` call recorded `0, 0, 0, 0` for
found/created/updated/skipped. Nothing was synced.

**The fix:** Two components:

**`ldap/client.go` - new `SyncAll()` method:**
Performs a wildcard directory search (`{username}` → `*`) using the configured `UserFilter`
and `BaseDN`. Returns all matching entries with full attribute fetch and group membership
resolution.

**`ldap.go` - `TriggerSync` now calls `SyncAll()`:**
Iterates the returned users, upserts each into the `users` table (`source='ldap'`,
`password_hash=''`), applies group→role mapping via `MapGroupsToRoles()`, and returns
real counts:

```json
{
  "success": true,
  "data": {
    "message": "Sync completed: found=48 created=3 updated=44 skipped=1",
    "duration_ms": 387,
    "users_found": 48,
    "users_created": 3,
    "users_updated": 44,
    "users_skipped": 1,
    "errors": ["group fetch for svc-account: timeout"]
  }
}
```

LDAP users authenticate via LDAP bind (not local password). Their `password_hash` in the
local `users` table is intentionally empty - local password login is disabled for LDAP-sourced
accounts.

**Note on JIT provisioning:** The existing JIT path (auto-create on first login) is unchanged
and continues to work. `TriggerSync` is the background batch sync - it pre-populates all
accounts from the directory so users can be referenced in ACLs and shares before their
first login.

---

### 4. Documentation: Conservative, Accurate Language Throughout

All capability claims in user-facing documentation have been reviewed and corrected.

| Document | Change |
|----------|--------|
| `README.md` | Removed "No other NAS OS does this" from container update section (snapshot+rollback is standard practice across NAS platforms including TrueNAS, Unraid, and Proxmox). |
| `README.md` | Removed "100× faster" from replication description - this was an unsupported benchmark claim. The accurate statement is that ZFS send transfers only changed blocks after the initial seed, which is substantially more efficient than rsync for large unchanged datasets. |
| `README.md` | Changed "injection-hardened" to "allowlist-based input validation" - more accurate description of the actual security model. |
| `README.md` | Renamed "HA cluster" to "HA node monitoring" in feature list; added explicit HA limitations section. |
| `README.md` | Updated LDAP feature list to reflect actual implementation (full directory sync, not framework-only). |
| `INSTALLATION-GUIDE.md` | Removed "enterprise NAS" from subtitle and completion message. |
| `SECURITY.md` | Updated command execution description to reflect `bash -c` removal; added HA limitations and LDAP storage limitations to Known Limitations. |
| `THREAT-MODEL.md` | Updated T1 (Command Injection) to document the replication fix. Added T13 (HA Split-Brain) with HIGH residual risk rating, vector description, and mitigation guidance. |
| `ADMIN-GUIDE.md` | Updated LDAP sync documentation to accurately describe the full-directory sync behavior and response fields. |
| `ha/cluster.go` | Package comment expanded with explicit warnings: no STONITH, no automatic failover, no split-brain protection, no quorum. |

---

---

### 5. Build: Version String Was Never Embedded in Any Release Binary

**Affected file:** `daemon/cmd/dplaned/main.go`

**The problem:** Every released binary reported `"version":"dev"` in the `/health` endpoint and startup logs. `Version` was declared as a `const`, and Go's `-ldflags "-X main.Version=3.3.2"` linker substitution only works with package-level `var` declarations - `const` values are inlined at compile time, before the linker runs.

**The fix:** Changed `const (Version = "dev")` to `var (Version = "dev")`. v3.3.2 is the first release where `/health` correctly returns `{"status":"ok","version":"3.3.2"}`.

---

## What Was NOT Changed

The following items from the audit are **acknowledged limitations**, not bugs, and are documented
rather than changed in this release:

- **HA is not Pacemaker/Corosync:** The HA module provides node monitoring and manual failover
  coordination. This is intentional scope - full STONITH and automatic failover require
  significantly more infrastructure complexity. The scope is now clearly documented. A future
  release may add optional Pacemaker integration.

- **Custom Docker client:** The Docker management layer uses a custom HTTP-over-Unix-socket
  client rather than the official SDK. This reduces binary size and dependency surface. It is
  a deliberate tradeoff acknowledged in the codebase. If Docker API compatibility issues arise,
  they will be addressed individually.

- **Regex-based command validation:** The whitelist approach in `security/whitelist.go` is
  a valid defense-in-depth layer, now supplemented by the elimination of shell invocation in
  the replication path. Replacing it entirely with libzfs or the Docker SDK is a larger effort
  tracked separately.

---

## Upgrade Notes

Drop-in upgrade from v3.3.1. No schema changes, no migrations, no configuration changes.

```bash
sudo bash /opt/dplaneos/scripts/upgrade-with-rollback.sh
```

The `require_chap` field on iSCSI create requests is optional and defaults to `false`.
Existing API integrations that omit it are unaffected.

---

## Files Changed

### Modified
- `daemon/cmd/dplaned/main.go` - `Version` changed from `const` to `var` (enables `-ldflags -X` version embedding)
- `daemon/internal/handlers/replication_remote.go` - shell pipeline → Go pipes; resume token validation; removed command leak from error response
- `daemon/internal/handlers/iscsi.go` - explicit `require_chap` field; SECURITY NOTICE log for auth=0
- `daemon/internal/handlers/ldap.go` - `TriggerSync` now calls `SyncAll()`, upserts users, returns real counts
- `daemon/internal/ldap/client.go` - new `SyncAll()` method and `SyncResult` type
- `daemon/internal/ha/cluster.go` - package comment expanded with explicit limitation warnings
- `VERSION` - `3.3.1` → `3.3.2`
- `README.md` - conservative, accurate language throughout
- `INSTALLATION-GUIDE.md` - removed "enterprise" language
- `SECURITY.md` - updated command execution description; added known limitations
- `THREAT-MODEL.md` - T1 updated; T13 (HA Split-Brain) added
- `ADMIN-GUIDE.md` - LDAP sync documentation updated
- `CHANGELOG.md` - v3.3.2 entry added

### Added
- `RELEASE-NOTES-v3.3.2.md` - this file
