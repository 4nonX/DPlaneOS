# DPlaneOS API Error Reference

Quick reference for all HTTP error codes returned by the daemon.

---

## HTTP Status Codes

| Code | Meaning | When |
|------|---------|------|
| 200 | OK | Request succeeded |
| 400 | Bad Request | Invalid input, validation failed |
| 401 | Unauthorized | No valid session and no valid API token |
| 403 | Forbidden | The account's roles, or the API token's scope, do not allow it |
| 423 | Locked | A GitOps apply is already running |
| 404 | Not Found | Resource or endpoint does not exist |
| 405 | Method Not Allowed | Wrong HTTP method (e.g. GET on a POST-only route) |
| 409 | Conflict | Target already exists |
| 429 | Too Many Requests | More than 100 requests in a minute from one client address |
| 500 | Internal Server Error | Server-side failure |

---

## Input Validation Errors (400)

Returned when input fails validation, before any system command runs. Many handlers answer a failed operation with HTTP 200 and `{"success": false, "error": "..."}` instead of an error status: check `success`.

### ZFS Names

| Error | Cause | Valid Format |
|-------|-------|-------------|
| `Invalid dataset name: must start with letter` | Dataset starts with number or symbol | `tank/data` - letters, numbers, `/`, `-`, `_`, `.` |
| `Invalid dataset name: invalid characters` | Shell metacharacters detected (`;`, `$`, `` ` ``, `\|`, `&`) | `^[a-zA-Z][a-zA-Z0-9_\-\.\/]{0,254}$` |
| `Invalid pool name` | Pool name fails regex | `^[a-zA-Z][a-zA-Z0-9_\-\.]{0,254}$` |
| `Invalid snapshot name` | Bad `dataset@snapshot` format | `tank/data@backup-2026` |
| `Invalid encryption algorithm` | Unsupported algorithm | `aes-128-ccm`, `aes-256-gcm` |

### Device Paths

| Error | Cause | Valid Format |
|-------|-------|-------------|
| `invalid device path` | Device not matching expected pattern | `/dev/sdb`, `/dev/nvme0n1` |
| `invalid mount point` | Path not under `/mnt/` or `/media/` | `/mnt/usb-backup` |

### Files and ACLs

| Error | Cause |
|-------|-------|
| `path not allowed` | A file, ACL, share or export path outside the pools and removable media (the system's own directories are not reachable through the API) |
| `invalid path`, `path must be absolute` | Not an absolute path, or one with `..` or control characters |
| `Invalid ACL entry format` | Expected `u:user:rwx`, `g:group:rx`, etc. |
| `User 'X' not found` | ACL user does not exist locally or in the directory |
| `Group 'X' not found` | ACL group does not exist locally or in the directory |

### Docker

| Error | Cause |
|-------|-------|
| `Invalid action` | Action not in: `start`, `stop`, `restart`, `pause`, `unpause`, `remove` |
| `Invalid container ID` | Container ID empty or malformed |

### Firewall

| Error | Cause |
|-------|-------|
| `Port is required` | Missing port in firewall rule |
| `Invalid port format` | Non-numeric port |
| `Invalid source IP` | Malformed IP address |
| `Invalid action` | Action not `allow`, `deny`, or `delete` |

### Certificates

| Error | Cause |
|-------|-------|
| `Invalid certificate name` | Name contains unsafe characters |
| `Certificate not found` | Referenced certificate does not exist |
| `Key file not found` | Private key missing for certificate |

### ZFS Quotas

| Error | Cause | Valid Format |
|-------|-------|-------------|
| `Invalid user/group name` | Quota target name starts with a digit, dot, or dash, or contains unsafe characters | Must start with a letter or `_`; remaining chars: `[a-zA-Z0-9_\-.]`; max 256 chars |

### Snapshots

| Error | Cause |
|-------|-------|
| `Invalid dataset name` | Schedule references invalid dataset |
| `Retention must be 1-1000` | Retention count out of range |

---

## Authentication and RBAC Errors

### 401 Unauthorized

| Error | Cause | Fix |
|-------|-------|-----|
| `Unauthorized` | Neither a session (`X-Session-ID` and `X-User`) nor an API token (`Authorization: Bearer dpl_...`) that is valid | Sign in, or send a valid token |
| `No authenticated user` | Session valid but user context missing | Sign in again |

Requests that change something with a session also need the session's CSRF token (`X-CSRF-Token`, from `GET /api/csrf`). API tokens need none.

### 403 Forbidden

| Error | Cause | Fix |
|-------|-------|-----|
| `Forbidden - insufficient permissions` (with `"required": "storage:write"`) | None of the account's roles has this permission | Give the account a role that has it |
| `Forbidden - token scope` | The API token's scope does not cover the action (a *read* token on a change, a *write* token on an admin action) | Use a token with the needed scope |
| `Forbidden: this API token is read-only` | A *read* token on a request that changes something | Use a *write* or *admin* token |
| `Forbidden: this token is not permitted to access ...` | The token is limited to certain paths | Extend its allowlist or use another token |

**Built-in roles:**

| Role | Description |
|------|-------------|
| `admin` | Full access |
| `operator` | Start/stop services, manage containers, view all |
| `user` | Read storage, files, and own profile |
| `viewer` | Read-only access to dashboards and status |

---

## Operation Errors

A command that fails returns what the command said, with the secrets and the unhelpful parts removed. Frequent ones:

| Error Pattern | Meaning | Resolution |
|---------------|---------|------------|
| `pool status failed` | `zpool status` returned an error | Is the pool imported? Storage › ZFS Pools shows pools that can be imported |
| `Snapshot failed` | `zfs snapshot` failed | Check that the dataset exists and the pool has space |
| `pool 'X' is degraded` | A disk failed or is missing | Replace the disk (Storage › ZFS Pools › the pool) |
| `security refusal: ...` | The command allowlist refused what the daemon was about to run | A defect: report it with the daemon log (the refusal is logged as `SECURITY: command refused by the allowlist`) |

---

## Daemon Logs

The daemon logs to the journal: `journalctl -u dplaned`, or System › Logs. What users and the system did is in the audit log (Security › Audit Log), which is kept in the database and chained so that a removed or changed entry is noticed.

Each request is one line:

```
2026/10/12 08:30:00 192.168.1.50 POST /api/zfs/encryption/lock 324µs
```

---

## Quick Diagnostic Commands

```bash
# Daemon status
systemctl status dplaned

# Live logs
journalctl -u dplaned -f

# Health check (no auth required), from the system itself
curl --unix-socket /run/dplaneos/dplaned.sock http://localhost/health
# ... and from another machine
curl -k https://<system>/api/system/health

# DB integrity
sudo -u postgres psql dplaneos -c "\dt"

# ZFS pool health
zpool status
