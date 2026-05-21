# DPlaneOS - Threat Model

## System Context

DPlaneOS is a NAS management layer running on NixOS. It manages storage (ZFS), containers (Docker), network (systemd-networkd), and identity on a single server. It runs as one Go binary (`dplaned`) listening on `127.0.0.1:9000` by default. External access is via reverse proxy (nginx/Caddy/Pangolin).

**Trust boundary**: the reverse proxy. Everything behind it (dplaned, PostgreSQL, ZFS/Docker/systemd commands) is trusted. Everything in front (browser, network) is untrusted.

```
┌──────────────────────────────────────────────────────────┐
│                       UNTRUSTED                          │
│  Browser ──── Internet ──── Reverse Proxy (TLS)          │
└──────────────────────────┬───────────────────────────────┘
                           │ TLS terminated, localhost only
┌──────────────────────────────────────────────────────────┐
│                        TRUSTED                           │
│  dplaned (Go) ─┬─ PostgreSQL / Patroni                   │
│                ├─ libzfs (cgo) → ZFS kernel              │
│                ├─ exec.Command → docker / samba / nfs    │
│                ├─ networkdwriter → /etc/systemd/network/ │
│                └─ /dev/sd* │ /mnt/* │ /var/lib/dplaneos/ │
└──────────────────────────────────────────────────────────┘
```

## Assets

| Asset | Value | Location |
|-------|-------|----------|
| User data (files, datasets) | CRITICAL | ZFS pools on `/mnt/*` |
| ZFS pool metadata | CRITICAL | Pool vdevs |
| ZFS encryption keys (loaded) | CRITICAL | Kernel memory |
| PostgreSQL database | HIGH | `/var/lib/dplaneos/pgsql/` |
| Session tokens | HIGH | DB `sessions` table |
| LDAP bind password | HIGH | DB `ldap_config` table (redacted in API responses) |
| Telegram bot token | MEDIUM | DB `telegram_config` table |
| TOTP secrets | HIGH | DB `totp_secrets` table |
| Audit log + HMAC key | MEDIUM | DB `audit_logs` + `/var/lib/dplaneos/audit.key` |
| Configuration | LOW | DB tables + CLI flags |

## Threat Actors

| Actor | Capability | Goal |
|-------|-----------|------|
| Remote unauthenticated | HTTP to reverse proxy | Data theft, service disruption |
| Remote authenticated (low-priv) | Valid session, `viewer` or `user` role | Privilege escalation, unauthorized data access |
| Local network attacker | Direct access to port 9000 if misconfigured | Full API access without TLS |
| Physical attacker | Access to hardware | Disk theft, boot manipulation |
| Malicious container | Docker container with host mounts | Escape to host filesystem |

---

## Security Features

- **Sessions:** 32-byte random session tokens, stored hashed in PostgreSQL
- **CSRF:** HMAC-SHA256 double-submit tokens on all mutating requests
- **2FA:** TOTP (RFC 6238) with ±1 window clock drift tolerance, bcrypt-hashed backup codes
- **API tokens:** SHA-256 hashed, prefixed `dpl_`, scope-limited (read/write/admin)
- **RBAC:** 4 roles (viewer, user, operator, admin) enforced at handler level, with 34 discrete permissions
- **Command execution (ZFS):** Most ZFS operations now go through `internal/libzfs` (cgo direct C API call or subprocess fallback). Both paths pre-validate all arguments with the same allowlist validators before any system call or subprocess is created. No shell expansion occurs in either path.
- **Command execution (other):** Docker, Samba, NFS, and network tools use allowlist-based validation via `internal/security/whitelist.go`; arguments passed as separate slice elements to `exec.Command` - no shell. **v6.1.0 Hardening:** Strict `by-id` path enforcement and pool-membership safety checks for disk operations ensure enterprise-grade storage security.
- **Database:** PostgreSQL with Patroni for HA; connections managed via `pgx/v5` pool.

---

## Threats & Mitigations

### T1: Command Injection via API Parameters

**Vector**: Attacker sends `{"pool":"tank; rm -rf /"}` to a ZFS endpoint, or manipulates replication parameters to inject shell commands.

**Mitigation**:
- All parameters validated by strict allowlist regex validators (`ValidatePoolName`, `ValidateDevicePath`, `ValidateDatasetName`, etc.) - rejects shell metacharacters with HTTP 400 before any command is executed.
- **v6.1.0 Hardening:** Standardized on strict `by-id` path enforcement and pool-membership safety checks for disk operations. Implemented a mandatory allowlist for `zfs set` properties, ensuring only safe parameters (e.g., `quota`, `atime`, `compression`) can be modified via the API.
- Mandatory `filepath.Clean` normalization and explicit rejection of dot-slash patterns in all file-based operations.
- Go `exec.Command` passes arguments as a string array - no shell expansion, no `/bin/sh -c` for standard operations.
- networkdwriter (network persistence) writes files directly, no shell involved; `networkctl reload` is called with fixed args, no user input in the command line.
- **v3.3.2 fix:** The ZFS replication handler (`replication_remote.go`) previously constructed shell commands via `fmt.Sprintf` and executed them with `bash -c`. This has been replaced with `execPipedZFSSend()`, which connects `zfs send`, `pv` (optional), and `ssh recv` as discrete `exec.Command` processes linked via Go `io.Pipe`. No shell is invoked. Resume tokens are now validated with `isValidResumeToken()` before use as arguments.

**Residual risk**: NEGLIGIBLE. The multi-layered approach (allowlist validation + fixed argument arrays + no shell + path normalization) effectively eliminates standard command injection vectors.

---

### T2: Authentication Bypass

**Vector**: Attacker crafts API requests without a valid session.

**Mitigation**:
- `sessionMiddleware` runs globally on all routes via `r.Use()`
- Public exceptions are explicitly allowlisted in the middleware: `/health`, `/api/auth/*`, `/api/csrf`
- All other routes - including all ZFS, Docker, system, and RBAC routes - require a valid `X-Session-ID` header
- Session validation: token format check + DB lookup + username-header match; fail-closed (DB error → 401)
- TOTP: if enabled for a user, login issues a `pending_totp` session that can only call `/api/auth/totp/verify` - all other routes reject it

**Residual risk**: LOW.

---

### T3: Privilege Escalation (RBAC Bypass)

**Vector**: A `viewer`-role user attempts `storage:write` operations.

**Mitigation**:
- `permRoute()` helper wraps sensitive handlers with `RequirePermission(resource, action)` middleware
- System roles (`admin`, `operator`, `user`, `viewer`) are immutable in the DB (`is_system = 1`)
- Role assignments support expiry dates

**Residual risk**: MEDIUM. Session middleware enforces authentication on all routes, but `permRoute()` is not applied to every operational route - several ZFS, Docker, snapshot, and system routes are session-authenticated only, without a per-route RBAC permission check. Any authenticated user (including `viewer`) can reach them. This is a known gap.

---

### T4: SQL Injection

**Vector**: Malicious input in API parameters reaches SQL queries.

**Mitigation**:
- All SQL uses `?` parameterized queries - no string concatenation in query construction
- Allowlist input validation rejects metacharacters before they reach the DB layer

**Residual risk**: NEGLIGIBLE.

---

### T5: Cross-Site Scripting (XSS)

**Vector**: Stored XSS via file names, share names, alert titles, or other server-sourced strings rendered in the UI.

**Mitigation**:
- CSP header set in `nginx-dplaneos.conf`: `default-src 'self'; script-src 'self' 'unsafe-inline'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; connect-src 'self' ws: wss:; font-src 'self'; frame-ancestors 'self';`
- API responses are JSON, not rendered HTML
- All server-sourced values interpolated into `innerHTML` are passed through an `esc()` / `escapeHtml()` sanitiser before insertion - applied consistently across all frontend pages and the alert system (v3.3.0)

**Residual risk**: LOW. CSP `'unsafe-inline'` remains present for style/script compatibility; the sanitiser closes the practical injection vector. Residual theoretical risk requires both a server-side data injection and a sanitiser bypass simultaneously.

---

### T6: Cross-Site Request Forgery (CSRF)

**Vector**: Malicious page tricks an authenticated browser into making state-changing API requests.

**Mitigation**:
- `GET /api/csrf` issues a CSRF token stored server-side in the session
- Token is required on state-changing requests (validated in `sessionMiddleware`)
- Session tokens are transmitted in `X-Session-ID` header (not cookies), which browsers do not auto-send cross-origin

**Residual risk**: LOW. Header-based sessions are inherently CSRF-resistant. CSRF token provides defence-in-depth.

---

### T7: Denial of Service

**Vector**: Flood of API requests exhausts resources.

**Mitigation**:
- In-process rate limiter: 100 requests/minute per source IP; returns HTTP 429 and logs a security event
- PostgreSQL persistent connections with pool management via `pgx/v5`
- Buffered audit logging prevents I/O stalls on high event volume
- Graceful shutdown (15 s timeout) drains in-flight requests on SIGTERM

**Residual risk**: MEDIUM. A valid authenticated user can trigger expensive operations (ZFS scrub, Docker image pull, dataset encryption) that the rate limiter does not separately throttle. The 100 req/min limit applies uniformly, not per-operation cost.

---

### T8: Data at Rest (Stolen Hardware)

**Vector**: Attacker steals physical server or individual disks.

**Mitigation**:
- ZFS native encryption (AES-256-GCM or AES-128-CCM) supported per dataset
- UI exposes lock / unlock / change-key operations (`/api/zfs/encryption/*`)
- `zfs unload-key` available via UI; not automatically called on daemon SIGTERM - operator must lock datasets before physical removal

**Residual risk**: LOW if encryption is enabled and keys are locked. HIGH if encryption is not enabled - plaintext data on disks. PostgreSQL state is also plaintext on disk; ZFS pool-level encryption covers it only if the DB lives on an encrypted dataset.

---

### T9: Man-in-the-Middle

**Vector**: Attacker intercepts traffic between browser and server.

**Mitigation**:
- `dplaned` defaults to `127.0.0.1:9000` - not reachable from the network without explicit reconfiguration
- TLS terminated at reverse proxy (nginx/Caddy/Pangolin)
- Session tokens transmitted in request headers, not URL parameters

**Residual risk**: LOW with correct reverse proxy setup. HIGH if the daemon is exposed directly to the network or TLS is misconfigured.

---

### T10: LDAP Credential Exposure

**Vector**: LDAP bind password compromised via DB access or API.

**Mitigation**:
- Bind password stored in PostgreSQL (not in a plaintext config file)
- GET `/api/ldap/config` redacts the password field
- Only accessible via authenticated + RBAC-checked API endpoint

**Residual risk**: MEDIUM. Root access to the host exposes the DB file directly. Inherent to single-server NAS architecture.

---

### T11: Container Escape

**Vector**: Malicious Docker container with host filesystem bind mount.

**Mitigation**:
- DPlaneOS manages container lifecycle but does not enforce container security policies
- Users are responsible for configuring bind mounts and network policies

**Residual risk**: HIGH. Docker-level concern outside DPlaneOS's control.

---

### T12: Audit Log Tampering

**Vector**: Attacker with DB write access removes or alters audit entries.

**Mitigation**:
- HMAC-SHA256 chain: each audit row includes a `row_hash` computed over its content + the previous row's hash, keyed by `audit.key`
- Chain integrity verifiable via `GET /api/system/audit/verify-chain`
- `audit.key` is a 32-byte random key stored separately from the DB

**Residual risk**: LOW if `audit.key` is protected. An attacker with both DB write access and the key can forge the chain - but these together represent full root compromise.

### T13: HA Split-Brain / Data Corruption on Failover

**Vector**: Network partition isolates the active node from standby. Both nodes now consider themselves active and attempt to import the same ZFS pools simultaneously, corrupting pool state.

**Mitigation**:
- **SCSI-3 Persistent Reservations (`dplane-fenced`) - shared-SAS topology:** Each node registers an 8-byte key derived from `/etc/machine-id` at startup. The primary holds a Write Exclusive Registrants Only (WERO) reservation on every ZFS pool member disk (APTPL=1: survives power cycles, stored in disk controller NVRAM). On unclean failover, the surviving node calls `FencedPreempt(device)` for each disk. The PROUT PREEMPT command evicts the faulted node's registration at the controller; subsequent I/O from the faulted node receives RESERVATION CONFLICT, regardless of what that node believes about its own state. Split-brain at the storage layer is physically impossible: the disk firmware is the arbiter, not a software vote.
- **Patroni-gated pool import:** ZFS pools are imported and mounted only after Patroni confirms the local node holds the primary role. Standby nodes hold pools unexported until promotion. This gate operates independently of fencing, providing a second layer of protection.
- **Automated DB failover (Patroni/etcd):** PostgreSQL leader election runs through a three-member etcd cluster. On shared-SAS deployments the third member is co-located on node A as a lightweight process (no separate machine required). On replicated deployments a separate witness node provides the third vote. In either case, 2-of-3 quorum is required before Patroni promotes, preventing DB split-brain.
- **IPMI/Redfish or SBD fencing - replicated topology:** Where SCSI-3 PR is unavailable (nodes do not share physical storage), the surviving node powers off the peer via BMC (IPMI/Redfish) or waits for an SBD lease expiry before importing pools. Promotion is blocked until fencing is confirmed.
- **Hysteresis and maintenance mode:** The `checkFailover()` guard enforces a hysteresis window before promoting to suppress flapping. `POST /api/ha/maintenance` suppresses automatic failover entirely during scheduled maintenance.

**Residual risk**: **LOW** for shared-SAS deployments - SCSI-3 PR makes storage-layer split-brain physically impossible regardless of software state. **LOW-MEDIUM** for replicated deployments - relies on software fencing (IPMI/SBD), which is effective but requires BMC or shared block device reachability; a partition that simultaneously isolates both the peer and the fencing mechanism could delay promotion without causing data corruption (the standby will not promote without confirmed fencing).

---

## Attack Surface Summary

| Surface | Exposure | Auth | Notes |
|---------|----------|------|-------|
| HTTP API (~400 routes) | All routes require session except `/health` and `/api/auth/*` | Session middleware (global) | ~24 routes also have per-route RBAC; remainder session-only |
| WebSocket (`/api/ws/monitor`) | Authenticated | Session middleware | Validated before upgrade |
| `exec.Command` (zfs, zpool, docker, etc.) | Internal only | **Strict sentence-based allowlist** | Path-agnostic resolution via PATH; no shell |
| networkdwriter file writes | `/etc/systemd/network/50-dplane-*` | Root filesystem permissions | Pure file I/O; `networkctl reload` fixed args |
| PostgreSQL database | Filesystem (`/var/lib/dplaneos/pgsql/`) | OS file permissions (root/postgres) | Managed by Patroni/etcd |
| systemd service | Root process | `CapabilityBoundingSet`, `NoNewPrivileges`, `MemoryMax` | Not a dedicated non-root user |

---

## Recommended Deployment

Run behind a VPN or reverse proxy with authentication (e.g. WireGuard, Tailscale, Cloudflare, Pangolin). Enable ZFS dataset encryption with a strong passphrase for protection against physical access. Do not expose port 9000 directly to the internet. For internet-facing deployments, layered security middlewares are strongly recommended: CrowdSec (proactive IP reputation), GeoBlock (country-level filtering), and Fail2ban (reactive ban on suspicious behaviour) in front of the reverse proxy.

---

## Known Gaps (not mitigated)

- **Partial RBAC coverage** - many operational routes (ZFS, Docker, snapshots, replication, system) are session-authenticated but lack per-route `RequirePermission` checks
- **ZFS keys not auto-locked on shutdown** - `zfs unload-key` must be called manually before powering down if encryption-at-rest is required
- **PostgreSQL plaintext** - DB is not encrypted independently; relies on ZFS pool-level encryption if the pool is configured that way
- **No API request signing** - no HMAC or nonce scheme for critical destructive operations (pool export, dataset destroy, Docker remove)
- **CSP not set by daemon** - CSP only present in nginx config; direct connections to port 9000 have no CSP
