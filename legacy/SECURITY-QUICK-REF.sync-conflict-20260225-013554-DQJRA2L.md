# D-PlaneOS Security Quick Reference

## 🔴 CRITICAL - Before Internet Deployment

### Pre-Flight Checklist
```
□ Read INTERNET-DEPLOYMENT.md completely
□ Configure firewall (UFW)
□ Install & configure fail2ban
□ Set up Nginx with HTTPS
□ Change default password
□ Configure IP allowlist (recommended)
□ Test all security features
□ Enable monitoring
□ Configure backups
```

---

## 🛡️ Security Layers (Defense in Depth)

### Layer 1: Network (UFW Firewall)
- Block all incoming except 22, 80, 443
- Rate limit SSH connections
- Log all denied packets

### Layer 2: Application Firewall (fail2ban)
- Auto-ban after 5 failed logins (5 min window → 30 min ban)
- Auto-ban after 50 API hits (1 min window → 30 min ban)
- Monitor logs for injection attempts

### Layer 3: Web Server (Nginx)
- Force HTTPS redirect
- Rate limiting (5/min login, 100/min API, 300/min general)
- Security headers (HSTS, CSP, X-Frame-Options)
- Hide version information

### Layer 4: PHP Application (security.php)
- Rate limiting (100 req/5min → 1hr ban)
- Brute force protection (5 attempts/5min → 30min ban)
- CSRF tokens on all state-changing requests
- Session hijacking detection
- IP allowlisting (optional)
- Secure session cookies (HttpOnly, Secure, SameSite)

### Layer 5: Application Logic
- Authentication required on ALL API endpoints
- Input validation & sanitization
- Prepared SQL statements (no SQL injection)
- escapeshellarg() on ALL shell parameters
- Audit logging of all actions

---

## 🚨 Common Attack Vectors & Mitigations

| Attack | Mitigation | Testing |
|--------|-----------|---------|
| **Brute Force Login** | Max 5 attempts → 30min ban | Try 6 wrong passwords |
| **API Flooding** | 100 req/5min → 1hr ban | curl in loop 110 times |
| **SQL Injection** | Prepared statements only | `' OR '1'='1` should fail |
| **Command Injection** | escapeshellarg() + validation | `; rm -rf /` should be blocked |
| **CSRF** | Token required on POST/PUT/DELETE | POST without token = 403 |
| **Session Hijacking** | User-Agent validation | Change UA = session killed |
| **Unencrypted Traffic** | Force HTTPS redirect | HTTP → HTTPS 301 |
| **Path Traversal** | /mnt/ restriction + realpath() | `../../../etc/passwd` blocked |
| **XSS** | CSP headers + escaping | `<script>alert(1)</script>` blocked |

---

## 📊 Security Monitoring

### Real-Time

```bash
# Watch fail2ban activity
tail -f /var/log/fail2ban.log

# Watch application security events
tail -f /var/dplane/logs/*.log | grep SECURITY

# Current bans
fail2ban-client status dplaneos-login
fail2ban-client status dplaneos-api
```

### Daily Checks

```bash
# Failed login attempts
sqlite3 /var/dplane/database/dplane.db \
  "SELECT * FROM brute_force_log WHERE success=0 AND timestamp > strftime('%s','now','-1 day');"

# Rate limited IPs
sqlite3 /var/dplane/database/dplane.db \
  "SELECT ip, requests, banned_until FROM rate_limits WHERE banned_until > strftime('%s','now');"

# Recent admin actions
sqlite3 /var/dplane/database/dplane.db \
  "SELECT * FROM audit_log WHERE action IN ('pool_destroy','user_delete','share_delete') AND timestamp > datetime('now','-1 day');"
```

---

## 🔥 Emergency Procedures

### Compromised? Lock it down NOW

```bash
# 1. Block all web access
ufw deny 443/tcp
ufw deny 80/tcp

# 2. Review what happened
grep "SECURITY:" /var/dplane/logs/*.log
fail2ban-client status

# 3. Kill all sessions
sqlite3 /var/dplane/database/dplane.db "DELETE FROM rate_limits; DELETE FROM brute_force_bans;"
rm /var/lib/php/sessions/sess_*

# 4. Change all passwords
# 5. Review code for backdoors
# 6. Restore from clean backup if needed
```

### Locked Out? Reset from SSH

```bash
# Clear bans
sqlite3 /var/dplane/database/dplane.db << EOF
DELETE FROM rate_limits;
DELETE FROM brute_force_bans;
EOF

# Reset admin password to "password" (CHANGE IMMEDIATELY)
sqlite3 /var/dplane/database/dplane.db << EOF
UPDATE users SET password = '\$2y\$10\$92IXUNpkjO0rOQ5byMi.Ye4oKoEa3Ro9llC/.og/at2.uheWG/igi' WHERE username = 'admin';
EOF
```

---

## 🎯 Security Testing Commands

### Test Rate Limiting
```bash
# From external IP - should get HTTP 429 after 100 requests
for i in {1..110}; do curl -k https://your-nas.example.com/api/storage/pools.php; done
```

### Test Brute Force Protection
```bash
# From external IP - should get HTTP 403 after 5 attempts
for i in {1..6}; do 
  curl -k -X POST https://your-nas.example.com/login.php \
    -d "username=admin&password=wrong"; 
done
```

### Test CSRF Protection
```bash
# Should get HTTP 403
curl -k -X POST https://your-nas.example.com/api/storage/pools.php \
  -H "Content-Type: application/json" -d '{"action":"test"}'
```

### Test SSL Configuration
```bash
# Should show only TLSv1.2/1.3
nmap --script ssl-enum-ciphers -p 443 your-nas.example.com
```

### Test Security Headers
```bash
curl -I https://your-nas.example.com | grep -E "Strict-Transport|X-Frame|X-Content|Content-Security"
```

---

## 📦 Configuration Files Reference

| File | Purpose |
|------|---------|
| `/var/dplane/system/dashboard/includes/security.php` | Core security layer |
| `/etc/nginx/sites-available/dplaneos` | Web server config |
| `/etc/fail2ban/jail.d/dplaneos.conf` | Fail2ban rules |
| `/etc/sudoers.d/dplaneos` | Privilege configuration |
| `/var/dplane/database/dplane.db` | Application database |

---

## 🔧 Security Configuration Tweaks

### Increase Rate Limit (if needed)
Edit `/var/dplane/system/dashboard/includes/security.php`:
```php
define('RATE_LIMIT_REQUESTS', 200);  // Default: 100
```

### Longer Ban Times
```php
define('RATE_LIMIT_BAN_DURATION', 7200);  // 2 hours instead of 1
define('BRUTE_FORCE_BAN', 3600);          // 1 hour instead of 30 min
```

### Enable IP Allowlist
```php
define('IP_ALLOWLIST', [
    '192.168.1.0/24',  // Your home network
    '203.0.113.5'      // Your static IP
]);
```

After changes:
```bash
systemctl reload php8.2-fpm
```

---

## 📞 Getting Help

### Logs to Check
1. `/var/log/nginx/dplaneos-error.log` - Web server errors
2. `/var/dplane/logs/php-errors.log` - PHP errors
3. `/var/log/fail2ban.log` - Ban activity
4. `/var/log/syslog` - System events

### Report Security Issues
- **DO NOT** create public GitHub issues
- See SECURITY.md for private reporting
- Include: logs, steps to reproduce, impact assessment

---

## ✅ Healthy System Indicators

```bash
# All should return "active (running)"
systemctl status nginx
systemctl status php8.2-fpm
systemctl status fail2ban

# Should show your site
curl -I https://your-nas.example.com

# Should show active jails
fail2ban-client status

# Should show no errors
nginx -t
visudo -c

# Should return 1.8.0
cat /var/dplane/VERSION
```

---

## 🔒 Security Best Practices

1. **Never** expose on port 80 only (always HTTPS)
2. **Never** use default passwords
3. **Never** disable CSRF protection
4. **Never** trust user input
5. **Always** use strong passwords (20+ chars)
6. **Always** enable fail2ban
7. **Always** monitor logs
8. **Always** keep backups
9. **Always** test security features
10. **Always** update promptly

---

**Print this, keep it handy, reference it often.**

**Security is not a feature - it's a process.**
