# DPlaneOS API Error Reference

Quick reference for all HTTP error codes returned by the daemon.

---

## HTTP Status Codes

| Code | Meaning | When |
|------|---------|------|
| 200 | OK | Request succeeded (some handlers report a failed operation as `200` with `"success": false` and `error`) |
| 400 | Bad Request | Invalid input, validation failed |
| 401 | Unauthorized | Missing or invalid session |
| 403 | Forbidden | RBAC permission denied, token scope, AAL2 required, password change required, a destructive operation without a valid confirmation token |
| 404 | Not Found | Resource or endpoint does not exist |
| 405 | Method Not Allowed | Wrong HTTP method (e.g. GET on a POST-only route) |
| 409 | Conflict | Target already exists, a stack is busy with another operation |
| 429 | Too Many Requests | Rate limit exceeded (100 requests/minute per address), or login throttled after failed attempts |
| 500 | Internal Server Error | Server-side failure |
| 503 | Service Unavailable | The session store (database) cannot be reached: the request is refused, never let through |

---

## Input Validation Errors (400)

Returned when user input fails the security allowlist before reaching any system command.

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
| `path not allowed` | Path outside the pools' mountpoints and the other allowed roots (see the Administrator Guide, Allowed Base Paths) |
| `path must be absolute`, `invalid path` | Relative path, `..`, or control characters |
| `destination is a symbolic link` | A write would follow a symbolic link |
| `setuid/setgid modes are not allowed` | chmod with setuid/setgid bits |
| `Invalid ACL entry format` | Expected `u:user:rwx`, `g:group:rx`, etc. |

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
| `Unauthorized - no valid session` | No `X-Session-ID` header, or the session expired, was revoked or ended after 30 minutes without activity | Log in again |
| `Invalid credentials` | Login with a wrong user name or password | |
| `No authenticated user` | Session valid but user context missing | Re-login |

### 403 Forbidden

| Error | Cause | Fix |
|-------|-------|-----|
| `Forbidden - insufficient permissions` (with `required`, e.g. `storage:write`) | The user's roles lack this permission | Assign a role with the required permission |
| `Forbidden - token scope` | An API token's scopes do not include the action | Use a token with the needed scope |
| `action: "enable_totp"` | The operation needs a session that logged in with TOTP (AAL2) | Enable two-factor authentication and log in again |
| `Password change required` | The account must change its password first | Change the password |

### 429 Too Many Requests

| Error | Cause |
|-------|-------|
| `Rate limit exceeded` | More than 100 requests per minute from one address |
| `Too many failed attempts. Try again in N seconds.` | Failed logins from this address (2 s after the second failure, up to 30 s) |

**Built-in roles:**

| Role | Description |
|------|-------------|
| `admin` | Full access (all 31 permissions) |
| `operator` | Start/stop services, manage containers, view all |
| `user` | Read storage, files, and own profile |
| `viewer` | Read-only access to dashboards and status |

---

## ZFS Operation Errors (500)

| Error Pattern | Meaning | Resolution |
|---------------|---------|------------|
| `pool status failed` | `zpool status` returned an error | Check if pool is imported: `zpool import` |
| `Snapshot failed` | `zfs snapshot` failed | Check dataset exists and there is sufficient space |

Most ZFS errors carry the command's own message (`error`) unchanged.

---

## System Operation Errors (500)

| Error Pattern | Meaning | Resolution |
|---------------|---------|------------|
| `nginx config test failed` | Bad TLS certificate configuration (installs outside NixOS) | Check certificate paths; run `nginx -t` |
| `lsblk failed` | Block device listing error | Check the daemon log |
| `Session check unavailable (database)`, `Service temporarily unavailable (session store)` (503) | PostgreSQL does not answer | `systemctl status postgresql` |

---

## Daemon Logs

All errors are logged to `journalctl -u dplaned`; changes and security events are also in the audit log (Security › Audit Log).

### Log Format

```
2026-03-09 08:30:00 127.0.0.1:45678 POST /api/zfs/encryption/lock 324µs
```

### ZFS Events (via ZED hook)

ZED events are logged by the daemon and sent as alerts (`zfs.<severity>.<subclass>`, see [ALERTS.md](../admin/ALERTS.md)); without the daemon the hook logs them to syslog (`journalctl -t dplaneos-zed`).

---

## Quick Diagnostic Commands

```bash
# Daemon status
systemctl status dplaned

# Live logs
journalctl -u dplaned -f

# Health check (no auth required)
curl --unix-socket /run/dplaneos/dplaned.sock http://localhost/health

# DB integrity
sudo -u postgres psql dplaneos -c "\dt"

# ZFS pool health
zpool status
