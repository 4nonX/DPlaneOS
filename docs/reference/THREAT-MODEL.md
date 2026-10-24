# DPlaneOS - Threat Model

## System Context

DPlaneOS is a NAS management layer running on NixOS. It manages storage (ZFS), containers (Docker), network (systemd-networkd), and identity on a single server. It runs as one Go binary (`dplaned`) behind nginx, which serves the web interface on port 80 (and 443 once a certificate is activated) and passes `/api` and `/ws` to the daemon's Unix socket (`/run/dplaneos/dplaned.sock`). The daemon binds no TCP port. For access from outside the local network, put a VPN or a reverse proxy with TLS in front.

**Trust boundary**: nginx. Everything behind it (dplaned, PostgreSQL, ZFS/Docker/systemd commands) is trusted. Everything in front (browser, network) is untrusted.

```
┌──────────────────────────────────────────────────────────┐
│                       UNTRUSTED                          │
│  Browser ──── network ──── nginx :80/:443 (TLS optional) │
└──────────────────────────┬───────────────────────────────┘
                           │ Unix socket
┌──────────────────────────────────────────────────────────┐
│                        TRUSTED                           │
│  dplaned (Go) ─┬─ PostgreSQL (node-local)                │
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
| Session tokens | HIGH | Browser; the DB `sessions` table holds only their SHA-256 |
| Secrets key | CRITICAL | `/var/lib/dplaneos/secrets.key` (opens every sealed secret below) |
| LDAP bind password | HIGH | DB, sealed with the secrets key (redacted in API responses) |
| OIDC client secret | HIGH | DB, sealed (never returned by the API) |
| SMTP password, Telegram bot token, Git credentials | MEDIUM | DB or settings files, sealed |
| TOTP secrets | HIGH | DB `totp_secrets`, sealed |
| Audit log + HMAC key | MEDIUM | DB `audit_logs` + `/var/lib/dplaneos/audit.key` |
| Configuration | LOW | DB tables + files under `/var/lib/dplaneos` and `/etc/dplaneos` |

## Threat Actors

| Actor | Capability | Goal |
|-------|-----------|------|
| Remote unauthenticated | HTTP to nginx (or a proxy in front) | Data theft, service disruption |
| Remote authenticated (low-priv) | Valid session, `viewer` or `user` role | Privilege escalation, unauthorized data access |
| Local network attacker | Direct access to nginx port 80/443 | Full API access without TLS if HTTP allowed |
| Physical attacker | Access to hardware | Disk theft, boot manipulation |
| Malicious container | Docker container with host mounts | Escape to host filesystem |

---

## Security Features

- **Destructive operation tokens:** 48-hex-char single-use confirmation tokens (`POST /api/confirm/issue`) required for pool destroy/export, Docker remove/prune/rmi, and zvol destroy. Tokens are in-memory, 60 s TTL, scoped to operation + target + user ID, consumed on first use.
- **Sessions:** 32-byte random session tokens; PostgreSQL stores only their SHA-256, so a copy of the database (a dump, a backup) holds no usable session. The session list shows these keys, never tokens. Client stores token in `sessionStorage` (tab-scoped, default) or `localStorage` (persistent, requires "Remember me" opt-in). Server-side TTL is 24 hours regardless of client storage choice.
- **Forced password change (server-enforced):** When `must_change_password = 1`, `sessionMiddleware` blocks every route except `/api/auth/change-password`, `/api/auth/logout`, and `/api/auth/session` with HTTP 403. Enforcement is server-side; the AppShell overlay provides the UI gate. API token holders bypass this check (machine-to-machine tokens are not subject to password-change policy).
- **Authentication Assurance Level (AAL2) enforcement:** Sessions carry an `aal` field: 1 = password only (AAL1), 2 = password + TOTP verified (AAL2). The `RequireAAL2` middleware is fail-closed: requests without a valid session token return HTTP 401; valid sessions with `aal < 2` return HTTP 403 with `action: "enable_totp"`. Only sessions with `aal >= 2` reach the handler. Applied to: pool destroy, resetting another user's password, fencing, CTDB and watchdog configuration, BMC enrolment, power control and certificate reset, and the iSCSI ALUA state. Rationale: these operations are irreversible or have large blast radius; a compromised password-only account must not be able to trigger them.
- **Session revocation on password change:** `ChangePassword` deletes all sessions for the user except the one that performed the change. Admin-initiated `reset-password` (`POST /api/users/{id}/reset-password`) deletes all sessions for the target user unconditionally.
- **CSRF:** HMAC-SHA256 double-submit tokens on all mutating requests
- **2FA:** TOTP (RFC 6238) with ±1 window clock drift tolerance, bcrypt-hashed backup codes
- **API tokens:** SHA-256 hashed, prefixed `dpl_`, scope-limited (read/write/admin)
- **RBAC:** 4 roles (viewer, user, operator, admin) enforced at handler level, with 31 discrete permissions
- **LDAP account segregation:** `users.source` ('local'/'ldap') is returned in `GET /api/auth/session`. The password-change UI disables itself for LDAP accounts and directs users to the directory server. The admin `reset-password` endpoint rejects LDAP accounts with an explicit error.
- **Command execution (ZFS):** All ZFS mutation operations (pool export/destroy/clear, dataset create/destroy/rename/promote, snapshot create/destroy/clone, vdev add/attach/replace/remove/online/offline, property get/set) now go through `internal/libzfs` (cgo direct C API call or subprocess fallback). Both paths pre-validate all arguments with the same allowlist validators before any system call or subprocess is created. No shell expansion occurs in either path. Read-only list queries retain subprocess calls with strict whitelist validation.
- **Command execution (other):** Docker, Samba, NFS, and network tools use allowlist-based validation via `internal/security/whitelist.go`; arguments passed as separate slice elements to `exec.Command` - no shell. Disk operations require `by-id` paths and pass pool-membership checks.
- **Database:** node-local PostgreSQL on every node; connections managed via `pgx/v5` pool.

---

## Threats & Mitigations

### T1: Command Injection via API Parameters

**Vector**: Attacker sends `{"pool":"tank; rm -rf /"}` to a ZFS endpoint, or manipulates replication parameters to inject shell commands.

**Mitigation**:
- All parameters validated by strict allowlist regex validators (`ValidatePoolName`, `ValidateDevicePath`, `ValidateDatasetName`, etc.) - rejects shell metacharacters with HTTP 400 before any command is executed.
- Strict `by-id` path enforcement and pool-membership checks for disk operations; an allowlist of `zfs set` properties (e.g., `quota`, `atime`, `compression`).
- Mandatory `filepath.Clean` normalization and explicit rejection of dot-slash patterns in all file-based operations.
- Go `exec.Command` passes arguments as a string array - no shell expansion, no `/bin/sh -c` for standard operations.
- networkdwriter (network persistence) writes files directly, no shell involved; `networkctl reload` is called with fixed args, no user input in the command line.
- Replication (`replication_remote.go`) connects `zfs send`, `pv` (optional) and `ssh ... zfs recv` as separate `exec.Command` processes linked by pipes; no shell is invoked. Resume tokens are validated before use as arguments.

**Residual risk**: NEGLIGIBLE. The multi-layered approach (allowlist validation + fixed argument arrays + no shell + path normalization + libzfs direct API for dataset mutations) effectively eliminates standard command injection vectors.

---

### T2: Authentication Bypass

**Vector**: Attacker crafts API requests without a valid session.

**Mitigation**:
- `sessionMiddleware` runs globally on all routes via `r.Use()`
- Public exceptions are explicitly allowlisted in the middleware, each with its own check: `/health`, `/metrics`; login, logout, auth check, TOTP verify and the OIDC flow; `/api/csrf`; `/api/system/status` and `/api/system/health`; `/api/system/setup-admin` (refused once an admin password is set); share links (`/api/s/`); the peer API between paired nodes (`/api/config/sync/peer/`, authenticated by the pair's certificate and secret); Git push notifications (signed); quorum enrolment and voter endpoints (join codes); the internal hooks of the timers (snapshots, SMART, rsync) and of the udev disk-event helper (a local caller with this boot's internal token, which only root can read: a request through the web server is refused even with a session)
- All other routes - including all ZFS, Docker, system, and RBAC routes - require a valid `X-Session-ID` header
- Session validation: token format check + DB lookup + username-header match; fail-closed (DB error → 401)
- TOTP: if enabled for a user, login issues a `pending_totp` session that can only call `/api/auth/totp/verify` - all other routes reject it
- OIDC handoff: the one-time code stored in `oidc_handoff` is consumed atomically via `DELETE ... RETURNING`; a second exchange attempt gets `sql.ErrNoRows` and a 401. The code expires in 2 minutes. This prevents an attacker who observes the `/login?oidc_handoff=` URL from replaying it.
- `must_change_password` gate: after session validation, `sessionMiddleware` queries the flag from the DB. Sessions with the flag set are restricted to three paths until the password is changed. This closes the path where a user with a forced-change password could bypass the client overlay by sending raw API requests.

**Residual risk**: LOW.

---

### T3: Privilege Escalation (RBAC Bypass)

**Vector**: A `viewer`-role user attempts `storage:write` operations.

**Mitigation**:
- `permRoute()` helper wraps sensitive handlers with `RequirePermission(resource, action)` middleware
- System roles (`admin`, `operator`, `user`, `viewer`) are immutable in the DB (`is_system = 1`)
- Role assignments support expiry dates

**Residual risk**: LOW. All routes now carry explicit `permRoute()` RBAC checks - 21 read-only endpoints (ZFS health, metrics, iSCSI/NVMe-oF status, firewall status, certificate list, HA status, job polling, and others) that were previously session-authenticated only have been wrapped. No authenticated user below the required role can reach any operational endpoint.

---

### T4: SQL Injection

**Vector**: Malicious input in API parameters reaches SQL queries.

**Mitigation**:
- All SQL uses parameterized queries (`$1`, `$2`, ...) - no string concatenation of values into queries
- Allowlist input validation rejects metacharacters before they reach the DB layer

**Residual risk**: NEGLIGIBLE.

---

### T5: Cross-Site Scripting (XSS)

**Vector**: Stored XSS via file names, share names, alert titles, or other server-sourced strings rendered in the UI.

**Mitigation**:
- nginx sends a CSP on every response: `default-src 'self'; script-src 'self' 'unsafe-inline'; style-src 'self' 'unsafe-inline'; img-src 'self' data: blob: https:; connect-src 'self' ws: wss:; font-src 'self'; frame-ancestors 'self';`, plus `X-Frame-Options`, `X-Content-Type-Options` and `Referrer-Policy`. Images may come from HTTPS sites: container icons can be remote URLs (`dplaneos.icon` label). The 2FA QR code is drawn by the daemon; the TOTP secret never leaves the machine.
- API responses are JSON, not rendered HTML
- The web interface is React, which escapes every value it renders; it does not set `innerHTML`

**Residual risk**: LOW. CSP `'unsafe-inline'` remains present for script/style compatibility; React's escaping closes the practical injection vector.

---

### T6: Cross-Site Request Forgery (CSRF)

**Vector**: Malicious page tricks an authenticated browser into making state-changing API requests.

**Mitigation**:
- `GET /api/csrf` issues a CSRF token stored with the session in the database
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

**Residual risk**: LOW if encryption is enabled and keys are locked. HIGH if encryption is not enabled - plaintext data on disks. The system disk (`/persist`: the database, the secrets key, the audit key) is not encrypted: the sealed secrets in the database are only as safe as the secrets key next to them.

---

### T9: Man-in-the-Middle

**Vector**: Attacker intercepts traffic between browser and server.

**Mitigation**:
- `dplaned` listens on a Unix socket (`/run/dplaneos/dplaned.sock`) - not reachable from the network at all; no TCP port is bound
- TLS at nginx once a certificate is activated (Security › Certificates); HTTP on port 80 stays available
- Session tokens transmitted in request headers, not URL parameters

**Residual risk**: LOW with HTTPS (or a VPN). HIGH when the web interface is used over plain HTTP on an untrusted network: passwords and session tokens travel in clear text.

---

### T10: LDAP Credential Exposure

**Vector**: LDAP bind password compromised via DB access or API.

**Mitigation**:
- Bind password stored in PostgreSQL, sealed with the secrets key
- GET `/api/ldap/config` redacts the password field
- Only accessible via authenticated + RBAC-checked API endpoint

**Residual risk**: MEDIUM. Root access to the host exposes the database and the secrets key. Inherent to single-server NAS architecture.

---

### T10a: OIDC Client Secret Exposure

**Vector**: OIDC client secret compromised via DB access or API. An attacker with the secret and the issuer/client-id could impersonate DPlaneOS at the IdP's token endpoint.

**Mitigation**:
- Client secret stored in PostgreSQL, sealed with the secrets key
- GET `/api/auth/oidc/config` omits the `client_secret` field entirely - it is never returned to the browser
- POST `/api/auth/oidc/config` preserves the existing secret when the field is sent empty, preventing accidental clearing
- Config endpoint is protected by `system:admin` RBAC

**Residual risk**: MEDIUM. Root access to the host exposes the DB directly. Use a confidential client with the narrowest IdP scope needed. Rotate the secret if DB access is suspected. Note: PKCE is required regardless, so a stolen secret alone cannot initiate a flow without also controlling the browser redirect.

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
- **Owner-gated pool import:** pools of shared storage groups are never imported at boot; the owner's daemon imports them only when the other candidates confirm they do not use them, or after a silent candidate left the quorate partition and the fencing delay passed. Manual imports of such pools are refused on non-owners.
- **Quorum (Corosync):** failover needs the quorate partition; two nodes without a third vote never fail over automatically. Epochs fence stale owners in configuration and group state.
- **Watchdog self-fencing and optional power fencing:** a storage owner that loses quorum stops resetting its watchdog and is reset before the takeover (watchdog timeout + 15 s); IPMI/Redfish or a PDU power the old owner off first when configured; ZFS multihost refuses imports of pools still in use.
- **Maintenance mode:** a node in maintenance (`POST /api/cluster/maintenance`) is never chosen to take a group over and is refused as the target of a move.

**Residual risk**: **LOW** with a third vote and the watchdog: a cut-off owner resets itself before the takeover, multihost refuses double imports of shared pools, and SCSI-3 PR (where supported) rejects writes at the disk. **LOW-MEDIUM** without a hardware watchdog (softdog does not reset a hung kernel; add power fencing) or without multihost on shared pools. Two nodes without a third vote never fail over automatically, so a split cannot cause a double import, only a manual takeover decision.

---

## Attack Surface Summary

| Surface | Exposure | Auth | Notes |
|---------|----------|------|-------|
| HTTP API (~550 routes) | All routes require a session except the public exceptions listed under T2 | Session middleware (global) + `must_change_password` gate | All operational routes carry per-route RBAC via `permRoute()`; self-service routes (`/api/rbac/me/roles`, `/api/rbac/me/permissions`) are session-only by design; the OIDC flow is public by necessity |
| WebSockets (`/ws/monitor`, `/ws/terminal`, `/ws/docker/exec`) | Authenticated | Session middleware + RBAC (`/ws/terminal`: system admin) | Validated before upgrade |
| `exec.Command` (zfs, zpool, docker, etc.) | Internal only | **Strict allowlist + libzfs direct API for ZFS dataset mutations** | Path-agnostic resolution via PATH; no shell; snapshot/clone/destroy go through cgo; pool import uses allowlist subprocess |
| networkdwriter file writes | `/etc/systemd/network/05-dplane-*` | Root filesystem permissions | Pure file I/O; `networkctl reload` fixed args |
| PostgreSQL database | Filesystem (`/var/lib/dplaneos/pgsql/`) | OS file permissions (root/postgres) | One database per node; sessions table holds hashed tokens, `must_change_password` flag enforced server-side |
| Client session token | `sessionStorage` (default) or `localStorage` (opt-in "Remember me") | 24-hour server-side TTL, revoked on password change | localStorage token survives browser restart; XSS with localStorage access is a higher-impact scenario than sessionStorage |
| systemd service | Root process | `CapabilityBoundingSet`, `NoNewPrivileges`, `MemoryMax` | Not a dedicated non-root user |

---

## Recommended Deployment

Run behind a VPN or reverse proxy with authentication (e.g. WireGuard, Tailscale, Cloudflare, Pangolin). Enable ZFS dataset encryption with a strong passphrase for protection against physical access. Do not expose nginx port 80 without TLS for internet-facing deployments. For internet-facing deployments, layered security middlewares are strongly recommended: CrowdSec (proactive IP reputation), GeoBlock (country-level filtering), and Fail2ban (reactive ban on suspicious behaviour) in front of the reverse proxy.

---

## Known Gaps (not mitigated)

- **ZFS keys not auto-locked on shutdown** - `zfs unload-key` must be called manually before powering down if encryption-at-rest is required
- **System disk not encrypted** - the database, the secrets key and the audit key are on `/persist` (ext4); someone with the system disk can open the sealed secrets
- **CSP not set by daemon** - CSP comes from nginx; the daemon socket is not reachable from the network, so this matters only with local shell access
- **OIDC handoff** - for up to 2 minutes after an SSO login the session token sits in `oidc_handoff` until the browser exchanges the one-time code
