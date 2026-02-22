# Security Policy

## Supported Versions

| Version | Supported |
|---------|-----------|
| 3.x     | ✅ Yes — active development |
| 2.x     | ⚠️ Critical fixes only |
| < 2.0   | ❌ No — upgrade required |

## Reporting a Vulnerability

**Please do not report security vulnerabilities via GitHub Issues.**

Security issues in D-PlaneOS are taken seriously. If you discover a vulnerability, please report it privately so we can address it before public disclosure.

### How to Report

**Email:** Create a GitHub Security Advisory at:
`https://github.com/YOUR_ORG/dplaneos/security/advisories/new`

Or email the maintainer directly (see profile). Please include:

1. **Description** of the vulnerability
2. **Affected component** (daemon, nginx config, frontend, etc.)
3. **Steps to reproduce** with minimal working example
4. **Impact assessment** — what an attacker could achieve
5. **Suggested fix** (optional but appreciated)

### What to Expect

- **Acknowledgement within 48 hours** of your report
- **Status update within 7 days** — confirming the issue and our planned timeline
- **Fix within 30 days** for critical issues, 90 days for others
- **Credit in the changelog** if you'd like (opt-in)

### Safe Harbour

We will not pursue legal action against researchers who:
- Report issues privately before public disclosure
- Do not access, modify, or delete data beyond what is needed to demonstrate the issue
- Do not disrupt service availability
- Give us reasonable time (30–90 days) to patch before disclosing

## Security Architecture

D-PlaneOS is designed as an internal network appliance. **It is not designed to be exposed directly to the public internet.** Key security properties:

- **Authentication:** bcrypt-hashed passwords, rate-limited login with exponential backoff
- **Sessions:** 32-byte random session tokens, stored hashed in SQLite
- **CSRF:** HMAC-SHA256 double-submit tokens on all mutating requests
- **2FA:** TOTP (RFC 6238) with ±1 window clock drift tolerance, bcrypt-hashed backup codes
- **API Tokens:** SHA-256 hashed, prefixed `dpl_`, scope-limited (read/write/admin)
- **RBAC:** 4 roles (viewer, operator, admin, system) enforced at handler level
- **Command execution:** Whitelist-only via `internal/security/whitelist.go` — no shell interpolation
- **Input validation:** Allowlist regexes on all user-supplied strings before syscall/exec
- **XSS:** All HTML output escaped; `Content-Security-Policy` via nginx
- **Transport:** nginx TLS termination recommended; see `nginx-dplaneos.conf`

## Known Limitations

- **ZFS delegation** (`zfs allow`) is complex; review carefully before enabling
- **rclone credentials** are stored in `/etc/dplaneos/rclone.conf` — restrict file permissions
- **Docker socket** is accessible to the daemon — containers with host mounts can escalate
- **LDAP bind password** is stored in SQLite — use a dedicated read-only LDAP account
