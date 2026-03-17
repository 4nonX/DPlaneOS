# D-PlaneOS API Error Reference

Quick reference for all HTTP error codes returned by the daemon.

---

## HTTP Status Codes

| Code | Meaning | When |
|------|---------|------|
| 200 | OK | Request succeeded |
| 400 | Bad Request | Invalid input, validation failed |
| 401 | Unauthorized | Missing or invalid session |
| 403 | Forbidden | RBAC permission denied |
| 404 | Not Found | Resource or endpoint does not exist |
| 405 | Method Not Allowed | Wrong HTTP method (e.g. GET on a POST-only route) |
| 409 | Conflict | Target already exists |
| 429 | Too Many Requests | Rate limit exceeded (100 req/min per IP) |
| 500 | Internal Server Error | Server-side failure |

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
| `Path must start with /mnt/` | ACL or trash operation on system path |
| `Invalid ACL entry format` | Expected `u:user:rwx`, `g:group:rx`, etc. |
| `User 'X' not found` | ACL user does not exist locally or in LDAP |
| `Group 'X' not found` | ACL group does not exist locally or in LDAP |
| `Can only trash files under /mnt/` | Trash operation outside storage pool |

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
| `Unauthorized` | No `X-Session-ID` header | Include a valid session token |
| `No authenticated user` | Session valid but user context missing | Re-login |

### 403 Forbidden

| Error | Cause | Fix |
|-------|-------|-----|
| `Permission denied: storage:write` | User role lacks this permission | Assign a role with the required permission |
| `Permission denied: docker:execute` | Attempting container exec without execute permission | Assign `operator` or `admin` role |

**Built-in roles:**

| Role | Description |
|------|-------------|
| `admin` | Full access (all 34 permissions) |
| `operator` | Start/stop services, manage containers, view all |
| `user` | Read storage, files, and own profile |
| `viewer` | Read-only access to dashboards and status |

---

## ZFS Operation Errors (500)

| Error Pattern | Meaning | Resolution |
|---------------|---------|------------|
| `pool status failed` | `zpool status` returned an error | Check if pool is imported: `zpool import` |
| `Failed to list datasets` | `zfs list` failed | Verify ZFS kernel module: `modprobe zfs` |
| `Snapshot failed` | `zfs snapshot` failed | Check dataset exists and there is sufficient space |
| `pool 'X' is degraded` | Disk failure detected | Replace failed disk, then `zpool replace` |

---

## System Operation Errors (500)

| Error Pattern | Meaning | Resolution |
|---------------|---------|------------|
| `ufw failed` | Firewall command error | Check `ufw status`; ensure ufw is installed |
| `nginx config test failed` | Bad SSL cert configuration | Check certificate paths; run `nginx -t` manually |
| `hdparm failed` | Disk power management error | Verify device supports APM |
| `lsblk failed` | Block device listing error | Check `/dev/` permissions |
| `getfacl/setfacl failed` | ACL operation error | Ensure `acl` mount option is set on the filesystem |

---

## Daemon Logs

All errors are logged to `journalctl -u dplaned` and `/var/log/dplaneos/audit.log`.

### Log Format

```
2026-03-09 08:30:00 127.0.0.1:45678 POST /api/zfs/encryption/lock 324µs
```

### Security Events

```
SECURITY: Invalid session token from admin@192.168.1.50
SECURITY: RBAC denied storage:write for user jdoe
```

### ZFS Events (via ZED hook)

```
[critical] Pool=tank Event=statechange State=FAULTED Device=/dev/sdb
[warning]  Pool=tank Event=io_failure State=DEGRADED Device=/dev/sdc
[info]     Pool=tank Event=scrub_finish State=ONLINE
```

---

## Quick Diagnostic Commands

```bash
# Daemon status
systemctl status dplaned

# Live logs
journalctl -u dplaned -f

# Health check (no auth required)
curl http://localhost:9000/health

# DB integrity
sqlite3 /var/lib/dplaneos/dplaneos.db "PRAGMA integrity_check"

# ZFS pool health
zpool status
