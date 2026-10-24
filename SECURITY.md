# Security Policy

## Supported Versions

| Version | Supported |
|---------|-----------|
| 16.x | Supported - all fixes, as new releases |
| 15.x and older | Not supported - update to 16 (installs from before 16.0: see "Installs from before 16.0" in [OTA-UPDATES.md](docs/admin/OTA-UPDATES.md)) |

Fixes ship in new releases; installed systems get them through the Updates page.

## Reporting a Vulnerability

**Do not report security vulnerabilities via GitHub Issues.**

Report privately via GitHub Security Advisories:
`https://github.com/4nonX/DPlaneOS/security/advisories/new`

Or email the maintainer directly (see GitHub profile). Include:

1. Description of the vulnerability
2. Affected component (daemon, nginx config, frontend, etc.)
3. Steps to reproduce with a minimal working example
4. Impact assessment: what an attacker could achieve
5. Suggested fix (optional)

### Response Timeline

- Acknowledgement within 48 hours
- Status update within 7 days confirming the issue and timeline
- Fix within 30 days for critical issues, 90 days for others
- Credit in the changelog if you would like it (opt-in)

### Safe Harbour

We will not pursue legal action against researchers who:
- Report issues privately before public disclosure
- Do not access, modify, or delete data beyond what is needed to demonstrate the issue
- Do not disrupt service availability
- Give us reasonable time (30–90 days) to patch before disclosing

## Security Architecture

DPlaneOS is designed as an internal network appliance. It is not intended to be exposed directly to the public internet. Key properties:

- **Authentication:** bcrypt-hashed passwords, rate-limited login with exponential backoff
- **Sessions:** 32-byte random session tokens; PostgreSQL stores only their SHA-256
- **CSRF:** HMAC-SHA256 double-submit tokens on all mutating requests
- **2FA:** TOTP (RFC 6238) with ±1 window clock drift tolerance, bcrypt-hashed backup codes
- **API tokens:** SHA-256 hashed, prefixed `dpl_`, scope-limited (read/write/admin)
- **RBAC:** 4 roles (viewer, user, operator, admin) enforced per route, with 31 discrete permissions; sensitive operations also require a session that logged in with TOTP (AAL2)
- **Command execution:** Allowlist-based validation via `internal/security/whitelist.go`; arguments passed as separate slice elements to `exec.Command` - no shell. Disk operations require `by-id` paths and pass pool-membership checks.
- **Secrets:** passwords of external services, TOTP secrets and keys are sealed in the database with the node's secrets key (`/var/lib/dplaneos/secrets.key`)
- **Web server:** nginx sends a CSP, `X-Frame-Options`, `X-Content-Type-Options` and `Referrer-Policy`; HTTPS once a certificate is activated

For the full threat model, see [docs/reference/THREAT-MODEL.md](docs/reference/THREAT-MODEL.md).

## Known Limitations

- **HA is quorum-based:** nodes keep their own databases and form a Corosync cluster; storage groups fail over only with quorum and after a fencing delay (watchdog), with optional power fencing. Secrets travel between paired nodes encrypted for the pair and are sealed under each node's own key.
- **Plain HTTP by default:** until a certificate is activated (Security › Certificates), passwords and session tokens travel unencrypted; use HTTPS or a VPN on untrusted networks
- **The system disk is not encrypted:** the database, the secrets key and the audit key are on `/persist`; someone with the disk can open the sealed secrets
- **ZFS delegation** (`zfs allow`) is complex; review carefully before enabling
- **rclone credentials** are stored in rclone's own format in `/etc/dplaneos/rclone.conf` (not sealed)
- **Docker socket** is accessible to the daemon - containers with host mounts can escalate
- **LDAP bind password** is sealed in PostgreSQL - still use a dedicated read-only LDAP account

