# D-PlaneOS Threat Model

**Version:** 1.5.0  
**Last Updated:** 2026-01-28  
**Status:** Production

---

## Executive Summary

This document defines the security boundaries, attack surfaces, and protection mechanisms of D-PlaneOS. It is designed for:

- **System Administrators** evaluating risk
- **Security Auditors** reviewing architecture
- **Contributors** understanding security requirements

**Trust Model:** Defense in depth with clearly defined boundaries between user input, web layer, command execution, and system operations.

---

## System Architecture

```
┌─────────────────────────────────────────────────────┐
│                   User / Browser                     │ ← Untrusted
└──────────────────────┬──────────────────────────────┘
                       │ HTTPS/HTTP
                       │ (Trust Boundary #1)
                       ↓
┌─────────────────────────────────────────────────────┐
│              Nginx Web Server                        │
│              + PHP-FPM                               │ ← Session Auth
└──────────────────────┬──────────────────────────────┘
                       │ Internal
                       │ (Trust Boundary #2)
                       ↓
┌─────────────────────────────────────────────────────┐
│              API Layer (PHP)                         │
│              - Input Validation                      │
│              - Command Broker                        │ ← Input Sanitization
└──────────────────────┬──────────────────────────────┘
                       │ exec() with validation
                       │ (Trust Boundary #3)
                       ↓
┌─────────────────────────────────────────────────────┐
│              System Commands                         │
│              - ZFS (via sudo)                        │
│              - Docker (via sudo)                     │ ← Privilege Boundary
│              - SMART (via sudo)                      │
└─────────────────────────────────────────────────────┘
```

---

## Trust Boundaries

### Boundary #1: Network → Web Server

**Protection Mechanisms:**
- HTTPS (if configured via reverse proxy)
- Session-based authentication
- 30-minute session timeout
- Rate limiting on login attempts

**Threats Mitigated:**
- Unauthorized access
- Session hijacking (with HTTPS)
- Brute force attacks

**Threats NOT Mitigated:**
- DDoS (requires external mitigation)
- Network sniffing on HTTP (use HTTPS)

---

### Boundary #2: Web Server → API Layer

**Protection Mechanisms:**
- Session validation on every request
- CSRF protection (session-bound)
- Input validation per parameter type
- SQL injection protection (parameterized queries)

**Threats Mitigated:**
- Unauthorized API access
- CSRF attacks
- SQL injection
- XSS (output escaping)

**Threats NOT Mitigated:**
- Authenticated user performing malicious actions (by design)

---

### Boundary #3: API Layer → System Commands

**Protection Mechanisms (v1.3.1+):**
- Command injection detection (active)
- Pattern blocking: `&&`, `||`, `;`, `|`, `` ` ``, `$`, `>`, `<`
- Token validation (alphanumeric + specific chars only)
- Command Broker infrastructure (available for strict whitelist)

**Threats Mitigated:**
- Command injection via user input
- Shell escape attacks
- Privilege escalation via command manipulation

**Threats NOT Mitigated:**
- Bugs in ZFS/Docker themselves
- Kernel vulnerabilities

---

### Boundary #4: System Commands → Storage/Containers

**Protection Mechanisms:**
- sudoers with specific command whitelist
- No password required for whitelisted commands only
- www-data user isolation
- File system permissions

**Threats Mitigated:**
- Unauthorized system access
- Lateral movement from web compromise

**Threats NOT Mitigated:**
- Root compromise (if attacker gains root, all bets off)
- Physical access to storage

---

## Attack Surfaces

### 1. Web Interface

**Exposure:** HTTP/HTTPS port (default: 80/443)

**Attack Vectors:**
- Authentication bypass
- Session hijacking
- XSS
- CSRF

**Current Protection:**
| Attack Type | Protection | Status |
|-------------|------------|--------|
| Auth bypass | Session validation | ✅ Active |
| Session hijacking | HTTPS + timeouts | 🟡 HTTPS external |
| XSS | Output escaping | ✅ Active |
| CSRF | Session binding | ✅ Active |
| Brute force | Rate limiting | ✅ Active |

---

### 2. API Endpoints

**Exposure:** Same as web interface (authenticated only)

**Attack Vectors:**
- Command injection
- SQL injection
- Parameter tampering
- Unauthorized operations

**Current Protection:**
| Attack Type | Protection | Status |
|-------------|------------|--------|
| Command injection | Pattern detection | ✅ Active (v1.3.1+) |
| SQL injection | Parameterized queries | ✅ Active |
| Parameter tampering | Type validation | ✅ Active |
| Unauth operations | Session check | ✅ Active |

---

### 3. Database

**Exposure:** Internal only (SQLite file)

**Attack Vectors:**
- File tampering (if OS compromised)
- Database corruption
- Privilege escalation via DB

**Current Protection:**
| Attack Type | Protection | Status |
|-------------|------------|--------|
| File tampering | File permissions | ✅ Active |
| Corruption | Integrity checks | ✅ Active (v1.3.1+) |
| Unauthorized access | No remote access | ✅ By design |

---

### 4. System Commands

**Exposure:** Internal only (via sudo)

**Attack Vectors:**
- Privilege escalation
- Command injection
- Unauthorized command execution

**Current Protection:**
| Attack Type | Protection | Status |
|-------------|------------|--------|
| Privilege escalation | sudoers whitelist | ✅ Active |
| Command injection | Pattern blocking | ✅ Active (v1.3.1+) |
| Unauth commands | sudoers restriction | ✅ Active |

---

## Threat Actors & Scenarios

### Threat Actor #1: Remote Attacker (No Access)

**Capabilities:**
- Network access to web interface
- Standard web attack tools
- No credentials

**Likely Attacks:**
1. Brute force login
2. Exploit web vulnerabilities (XSS, CSRF)
3. Attempt SQL/Command injection

**Mitigations:**
- ✅ Rate limiting
- ✅ Session security
- ✅ Input validation
- ✅ Command injection protection

**Residual Risk:** 🟢 Low (multiple layers)

---

### Threat Actor #2: Authenticated User (Malicious)

**Capabilities:**
- Valid login credentials
- Full API access
- Knowledge of system

**Likely Attacks:**
1. Data destruction (destroy pools/datasets)
2. Container manipulation
3. Resource exhaustion

**Mitigations:**
- ✅ Audit logging (all actions recorded)
- ✅ Confirmation required for destructive ops
- 🟡 Limited: Authenticated users trusted by design

**Residual Risk:** 🟡 Medium (by design - single user system)

---

### Threat Actor #3: Compromised Web Server

**Capabilities:**
- Full access to www-data user
- Can execute PHP code
- Can make system calls via sudo

**Likely Attacks:**
1. Privilege escalation
2. Lateral movement
3. Data exfiltration

**Mitigations:**
- ✅ sudoers whitelist (limited commands)
- ✅ www-data isolation (no login shell)
- ✅ Command validation
- 🟡 No privilege drop beyond www-data

**Residual Risk:** 🟡 Medium (sudo whitelist limits scope)

---

### Threat Actor #4: Physical Access

**Capabilities:**
- Direct hardware access
- Can reboot system
- Can access storage devices

**Likely Attacks:**
1. Boot from USB, mount filesystems
2. Extract database
3. Modify system files

**Mitigations:**
- ❌ Not in scope (secure your hardware)
- 🔵 Recommendation: Encrypt disks at OS level
- 🔵 Recommendation: BIOS/UEFI password

**Residual Risk:** 🔴 High (physical access = game over)

---

## Known Limitations

### Security Limitations

1. **Single User System**
   - Only one admin account
   - No role-based access control
   - All authenticated users have full access
   - **Mitigation:** Don't share credentials

2. **No Built-in TLS**
   - Default installation uses HTTP
   - Passwords transmitted in clear (if no reverse proxy)
   - **Mitigation:** Use reverse proxy with TLS (Caddy, nginx)

3. **SQLite Concurrency**
   - Write locks can cause delays under heavy load
   - Potential DoS via database contention
   - **Mitigation:** Read-only fallback mode, monitoring

4. **Sudo Whitelist Scope**
   - www-data can run specific commands without password
   - If web layer compromised, attacker has these commands
   - **Mitigation:** Commands are validated, whitelist is minimal

5. **No API Authentication Tokens**
   - Only session-based auth
   - No programmatic API access without session
   - **Mitigation:** Plan for v2.x

---

### Operational Limitations

1. **No Built-in Backup**
   - System doesn't auto-backup itself
   - Database can be lost if not backed up
   - **Mitigation:** Use replication feature, external backups

2. **No High Availability**
   - Single point of failure
   - No failover support
   - **Mitigation:** Enterprise deployments use external HA

3. **No Audit Alert Integration**
   - Audit log exists but no automatic alerting
   - Requires external monitoring
   - **Mitigation:** Use webhook alerts (v1.3.0+)

---

## Out of Scope

The following threats are explicitly **out of scope** for D-PlaneOS:

1. **Network Security**
   - Firewall configuration
   - Network segmentation
   - DDoS mitigation
   - **Responsibility:** System administrator

2. **Physical Security**
   - Server room access
   - Hardware tampering
   - Theft
   - **Responsibility:** Facility management

3. **Host OS Hardening**
   - Kernel security
   - System updates
   - SELinux/AppArmor
   - **Responsibility:** System administrator

4. **Backup & Disaster Recovery**
   - Offsite backups
   - Disaster recovery planning
   - Business continuity
   - **Responsibility:** System administrator

5. **Compliance**
   - GDPR, HIPAA, etc.
   - Data retention policies
   - Legal requirements
   - **Responsibility:** Organization

---

## Security Assumptions

D-PlaneOS security model assumes:

1. ✅ **Trusted Network**
   - System deployed on trusted/isolated network
   - Or behind properly configured firewall
   - Or using HTTPS reverse proxy

2. ✅ **Secure Host OS**
   - Ubuntu/Debian installation is hardened
   - System updates are applied
   - No other compromised services on host

3. ✅ **Physical Security**
   - Server is in physically secure location
   - No unauthorized physical access
   - BIOS/boot process secured

4. ✅ **Administrator Trustworthiness**
   - Admin users are vetted
   - Credentials are protected
   - No credential sharing

5. ✅ **Regular Monitoring**
   - Audit logs are reviewed
   - Alert webhooks are configured
   - Anomalies are investigated

**If any assumption is violated, security guarantees may not hold.**

---

## Incident Response

### If You Suspect Compromise

1. **Immediate Actions:**
   ```bash
   # Check audit log for suspicious activity
   sqlite3 /var/dplane/database/dplane.db \
     "SELECT * FROM audit_log ORDER BY timestamp DESC LIMIT 100"
   
   # Check system logs
   grep SECURITY /var/log/nginx/error.log
   
   # Check active sessions
   ls -la /var/lib/php/sessions/
   ```

2. **Containment:**
   ```bash
   # Stop web services
   systemctl stop nginx php8.2-fpm
   
   # Block network access (if needed)
   iptables -A INPUT -p tcp --dport 80 -j DROP
   ```

3. **Investigation:**
   - Review audit log for unauthorized actions
   - Check for unexpected pools/datasets/containers
   - Verify user accounts in database
   - Check for modified system files

4. **Recovery:**
   - Restore from backup if compromised
   - Change admin password
   - Review and tighten firewall rules
   - Consider TLS deployment

---

## Security Best Practices

### Deployment Recommendations

1. **Network Security**
   ```bash
   # Allow only specific IPs
   ufw allow from 192.168.1.0/24 to any port 80
   ufw enable
   ```

2. **TLS Deployment**
   ```bash
   # Use Caddy as reverse proxy
   apt install caddy
   # Configure Caddy with automatic HTTPS
   ```

3. **Regular Backups**
   ```bash
   # Automated database backup
   0 2 * * * cp /var/dplane/database/dplane.db \
     /backup/dplane-$(date +\%Y\%m\%d).db
   ```

4. **Monitoring**
   ```bash
   # Watch for security events
   tail -f /var/log/nginx/error.log | grep SECURITY
   ```

5. **Strong Password**
   ```bash
   # Change default admin password immediately
   # Use 20+ character random password
   ```

---

## Security Contact

**Report Security Vulnerabilities:**

- Create GitHub issue with `security` label
- Email: security@dplaneos.example (if configured)
- Do NOT publicly disclose until patched

**Response Time:**
- Critical: 24 hours
- High: 72 hours  
- Medium: 1 week

---

## Changelog

### v1.5.0
- Enhanced sudoers with least-privilege separation
- Explicit command whitelist per operation
- Comprehensive threat model documentation
- Recovery playbook for administrators

### v1.3.1
- Command injection protection (active)
- Database integrity checks

### v1.3.0
- Enhanced audit logging
- Webhook alerts

---

## Conclusion

D-PlaneOS implements defense in depth with multiple security layers. While not immune to all attacks, it provides enterprise-grade protection for its threat model scope.

**Key Strengths:**
- Clear trust boundaries
- Multiple protection layers
- Comprehensive audit trail
- Active command injection protection

**Key Limitations:**
- Single user system
- Requires external TLS
- Physical access = compromise

**Recommendation:** Suitable for production use in trusted environments with proper network security and monitoring.

---

**Document Version:** 1.0  
**System Version:** 1.5.0  
**Reviewed:** 2026-01-28
