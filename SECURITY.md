# Security Policy

## Supported Versions

| Version | Supported |
|---------|-----------|
| 6.x | Active development - all fixes |
| 5.x | Maintenance/LTS - security fixes only |
| 4.x | End of life - upgrade required |
| 3.x | End of life - upgrade required |
| < 3.0 | End of life - upgrade required |

## Reporting a Vulnerability

**Do not report security vulnerabilities via GitHub Issues.**

Report privately via GitHub Security Advisories:
`https://github.com/4nonX/D-PlaneOS/security/advisories/new`

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

D-PlaneOS is designed as an internal network appliance. It is not intended to be exposed directly to the public internet. Key properties:

- **Authentication:** bcrypt-hashed passwords, rate-limited login with exponential backoff
- **Sessions:** 32-byte random session tokens, stored hashed in SQLite
- **CSRF:** HMAC-SHA256 double-submit tokens on all mutating requests
- **2FA:** TOTP (RFC 6238) with ±1 window clock drift tolerance, bcrypt-hashed backup codes
- **API tokens:** SHA-256 hashed, prefixed `dpl_`, scope-limited (read/write/admin)
- **RBAC:** 4 roles (viewer, user, operator, admin) enforced at handler level, with 34 discrete permissions
- **Command execution:** Allowlist-based validation via `internal/security/whitelist.go`; arguments passed as separate slice elements to `exec.Command` - no shell. **v6.1.0 Hardening:** Strict `by-id` path enforcement and pool-membership safety checks for disk operations ensure enterprise-grade storage security.

For the full threat model, see [docs/reference/THREAT-MODEL.md](docs/reference/THREAT-MODEL.md).

## Known Limitations

- **HA is node monitoring, not true HA:** The cluster module provides heartbeat detection and manual promotion. There is no STONITH, no automatic failover, and no split-brain protection. See [docs/reference/THREAT-MODEL.md](docs/reference/THREAT-MODEL.md) T13.
- **Partial RBAC coverage:** Many operational routes are session-authenticated but lack per-route `RequirePermission` checks
- **ZFS delegation** (`zfs allow`) is complex; review carefully before enabling
- **rclone credentials** are stored in `/etc/dplaneos/rclone.conf` - restrict file permissions
- **Docker socket** is accessible to the daemon - containers with host mounts can escalate
- **LDAP bind password** is stored in SQLite - use a dedicated read-only LDAP account

