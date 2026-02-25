# D-PlaneOS Internet-Facing Deployment Guide

## ⚠️ CRITICAL - READ BEFORE DEPLOYING

This guide is **MANDATORY** for internet-facing deployments. Skip any step at your own peril.

---

## Prerequisites

- [ ] Fresh Ubuntu 22.04/24.04 or Debian 12 server
- [ ] Valid SSL certificate (Let's Encrypt recommended)
- [ ] Static IP or dynamic DNS
- [ ] Firewall configured (UFW or iptables)
- [ ] Backups configured
- [ ] Monitoring configured

---

## Security Architecture

```
Internet
    ↓
Firewall (UFW)
    ↓
Fail2ban (Ban malicious IPs)
    ↓
Nginx (HTTPS, rate limiting, security headers)
    ↓
PHP-FPM (Application layer)
    ↓
D-PlaneOS Security Layer (Rate limiting, CSRF, brute force protection)
    ↓
Application
```

---

## Part 1: System Hardening

### 1.1 Update System

```bash
apt update && apt upgrade -y
apt install -y ufw fail2ban nginx certbot python3-certbot-nginx
```

### 1.2 Configure Firewall

```bash
# Default policies
ufw default deny incoming
ufw default allow outgoing

# Allow SSH (change port if using non-standard)
ufw allow 22/tcp

# Allow HTTP/HTTPS
ufw allow 80/tcp
ufw allow 443/tcp

# Enable firewall
ufw enable
ufw status verbose
```

### 1.3 Harden SSH

Edit `/etc/ssh/sshd_config`:

```
PermitRootLogin no
PasswordAuthentication no
PubkeyAuthentication yes
X11Forwarding no
MaxAuthTries 3
```

Restart SSH:
```bash
systemctl restart sshd
```

---

## Part 2: Nginx Configuration

### 2.1 SSL Certificate

**Option A: Let's Encrypt (Recommended)**
```bash
certbot --nginx -d your-nas.example.com
```

**Option B: Self-signed (Testing Only)**
```bash
openssl req -x509 -nodes -days 365 -newkey rsa:2048 \
  -keyout /etc/ssl/private/dplaneos-selfsigned.key \
  -out /etc/ssl/certs/dplaneos-selfsigned.crt
```

### 2.2 Nginx Security Configuration

Create `/etc/nginx/conf.d/security.conf`:

```nginx
# Hide Nginx version
server_tokens off;

# Rate limiting zones
limit_req_zone $binary_remote_addr zone=login:10m rate=5r/m;
limit_req_zone $binary_remote_addr zone=api:10m rate=100r/m;
limit_req_zone $binary_remote_addr zone=general:10m rate=300r/m;

# Connection limiting
limit_conn_zone $binary_remote_addr zone=addr:10m;
limit_conn addr 10;

# Request body size
client_max_body_size 100M;
client_body_buffer_size 128k;

# Timeouts
client_body_timeout 10s;
client_header_timeout 10s;
keepalive_timeout 10s;
send_timeout 10s;
```

### 2.3 D-PlaneOS Site Configuration

Create `/etc/nginx/sites-available/dplaneos`:

```nginx
upstream php-fpm {
    server unix:/run/php/php8.2-fpm.sock;
}

# Redirect HTTP to HTTPS
server {
    listen 80;
    listen [::]:80;
    server_name your-nas.example.com;
    
    location /.well-known/acme-challenge/ {
        root /var/www/html;
    }
    
    location / {
        return 301 https://$server_name$request_uri;
    }
}

# HTTPS Server
server {
    listen 443 ssl http2;
    listen [::]:443 ssl http2;
    server_name your-nas.example.com;
    
    root /var/www/dplane;
    index index.php index.html;
    
    # SSL Configuration
    ssl_certificate /etc/letsencrypt/live/your-nas.example.com/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/your-nas.example.com/privkey.pem;
    ssl_protocols TLSv1.2 TLSv1.3;
    ssl_ciphers 'ECDHE-ECDSA-AES128-GCM-SHA256:ECDHE-RSA-AES128-GCM-SHA256:ECDHE-ECDSA-AES256-GCM-SHA384:ECDHE-RSA-AES256-GCM-SHA384';
    ssl_prefer_server_ciphers off;
    ssl_session_cache shared:SSL:10m;
    ssl_session_timeout 10m;
    ssl_stapling on;
    ssl_stapling_verify on;
    
    # Security Headers (redundant with PHP layer, defense in depth)
    add_header Strict-Transport-Security "max-age=31536000; includeSubDomains; preload" always;
    add_header X-Frame-Options "DENY" always;
    add_header X-Content-Type-Options "nosniff" always;
    add_header X-XSS-Protection "1; mode=block" always;
    add_header Referrer-Policy "strict-origin-when-cross-origin" always;
    
    # Rate limiting
    location /login.php {
        limit_req zone=login burst=3 nodelay;
        try_files $uri =404;
        fastcgi_pass php-fpm;
        fastcgi_index index.php;
        include fastcgi_params;
        fastcgi_param SCRIPT_FILENAME $document_root$fastcgi_script_name;
    }
    
    location /api/ {
        limit_req zone=api burst=20 nodelay;
        try_files $uri $uri/ =404;
        location ~ \.php$ {
            try_files $uri =404;
            fastcgi_pass php-fpm;
            fastcgi_index index.php;
            include fastcgi_params;
            fastcgi_param SCRIPT_FILENAME $document_root$fastcgi_script_name;
        }
    }
    
    location / {
        limit_req zone=general burst=50 nodelay;
        try_files $uri $uri/ /index.php?$query_string;
    }
    
    location ~ \.php$ {
        try_files $uri =404;
        fastcgi_pass php-fpm;
        fastcgi_index index.php;
        include fastcgi_params;
        fastcgi_param SCRIPT_FILENAME $document_root$fastcgi_script_name;
    }
    
    # Deny access to sensitive files
    location ~ /\. {
        deny all;
        access_log off;
        log_not_found off;
    }
    
    location ~ /\.git {
        deny all;
    }
    
    location ~ /database/ {
        deny all;
    }
    
    location ~ /includes/ {
        deny all;
    }
    
    # Logging
    access_log /var/log/nginx/dplaneos-access.log;
    error_log /var/log/nginx/dplaneos-error.log warn;
}
```

Enable site:
```bash
ln -s /etc/nginx/sites-available/dplaneos /etc/nginx/sites-enabled/
nginx -t
systemctl reload nginx
```

---

## Part 3: Fail2ban Configuration

### 3.1 D-PlaneOS Jail

Create `/etc/fail2ban/jail.d/dplaneos.conf`:

```ini
[dplaneos-login]
enabled = true
port = http,https
filter = dplaneos-login
logpath = /var/dplane/logs/auth.log
maxretry = 5
findtime = 300
bantime = 3600
action = iptables-multiport[name=dplaneos, port="http,https", protocol=tcp]

[dplaneos-api]
enabled = true
port = http,https
filter = dplaneos-api
logpath = /var/dplane/logs/security.log
maxretry = 50
findtime = 60
bantime = 1800
action = iptables-multiport[name=dplaneos-api, port="http,https", protocol=tcp]
```

### 3.2 Fail2ban Filters

Create `/etc/fail2ban/filter.d/dplaneos-login.conf`:

```ini
[Definition]
failregex = ^.* SECURITY: Brute force detected from IP <HOST>
            ^.* login_failed.* from <HOST>
ignoreregex =
```

Create `/etc/fail2ban/filter.d/dplaneos-api.conf`:

```ini
[Definition]
failregex = ^.* SECURITY: Rate limit exceeded for IP <HOST>
            ^.* SECURITY: Blocked command injection attempt from <HOST>
ignoreregex =
```

### 3.3 Restart Fail2ban

```bash
systemctl restart fail2ban
fail2ban-client status
```

---

## Part 4: Install D-PlaneOS

### 4.1 Verify Package Integrity

```bash
# Extract
tar -xzf dplaneos-v1.8.0-CLEAN.tar.gz
cd dplaneos-v1.8.0-CLEAN

# Verify checksums
sha256sum -c SHA256SUMS
```

**If verification fails: STOP. Do not proceed.**

### 4.2 Dry Run Test

```bash
sudo bash install.sh --dry-run
```

Review output carefully.

### 4.3 Install

```bash
sudo bash install.sh
```

### 4.4 Verify Installation

```bash
# Check version
cat /var/dplane/VERSION  # Should be: 1.8.0

# Check services
systemctl status nginx php8.2-fpm docker

# Check sudoers
sudo visudo -c

# Check security layer
php -l /var/dplane/system/dashboard/includes/security.php
```

---

## Part 5: Post-Installation Security

### 5.1 Change Default Password

**IMMEDIATELY:**
```
1. Access https://your-nas.example.com
2. Login with admin/admin
3. Go to Users
4. Change admin password to strong passphrase (20+ chars)
5. Logout and re-login
```

### 5.2 Configure IP Allowlist (Optional but Recommended)

If you have static IP or VPN:

Edit `/var/dplane/system/dashboard/includes/security.php`:

```php
// Change this line:
define('IP_ALLOWLIST', []);

// To this (example):
define('IP_ALLOWLIST', [
    '192.168.1.0/24',  // Your home network
    '10.0.0.0/8',      // Your VPN
    '203.0.113.5'      // Your static IP
]);
```

Reload PHP-FPM:
```bash
systemctl reload php8.2-fpm
```

### 5.3 Configure Security Logging

Create log directory:
```bash
mkdir -p /var/dplane/logs
chown www-data:www-data /var/dplane/logs
```

Enable application logging in PHP:
```bash
echo "error_log = /var/dplane/logs/php-errors.log" >> /etc/php/8.2/fpm/php.ini
systemctl reload php8.2-fpm
```

### 5.4 Set Up Log Rotation

Create `/etc/logrotate.d/dplaneos`:

```
/var/dplane/logs/*.log {
    daily
    rotate 30
    compress
    delaycompress
    notifempty
    missingok
    sharedscripts
    postrotate
        systemctl reload php8.2-fpm > /dev/null 2>&1
    endscript
}
```

### 5.5 Configure Automatic Updates (Ubuntu)

```bash
apt install -y unattended-upgrades
dpkg-reconfigure -plow unattended-upgrades
```

---

## Part 6: Monitoring & Alerts

### 6.1 Monitor Failed Logins

```bash
# Watch fail2ban
tail -f /var/log/fail2ban.log

# Watch application logs
tail -f /var/dplane/logs/*.log
```

### 6.2 Set Up Alerts (Optional)

Configure in D-PlaneOS dashboard:
1. Go to Alerts page
2. Add webhook (Slack, Discord, email)
3. Test alert
4. Enable for: Login failures, Rate limit hits, System errors

### 6.3 Regular Security Checks

Weekly checklist:
```bash
# Check banned IPs
fail2ban-client status dplaneos-login
fail2ban-client status dplaneos-api

# Check recent logins
sqlite3 /var/dplane/database/dplane.db "SELECT * FROM audit_log WHERE action='login' ORDER BY timestamp DESC LIMIT 20;"

# Check rate limit hits
sqlite3 /var/dplane/database/dplane.db "SELECT ip, requests, banned_until FROM rate_limits WHERE banned_until > strftime('%s','now');"

# Check system updates
apt list --upgradable
```

---

## Part 7: Security Testing

### 7.1 Test Rate Limiting

From external IP:
```bash
# Should get banned after 100 requests in 5 minutes
for i in {1..110}; do
  curl -k https://your-nas.example.com/api/storage/pools.php
done
```

Expected: HTTP 429 after ~100 requests

### 7.2 Test Brute Force Protection

From external IP:
```bash
# Should get banned after 5 failed attempts
for i in {1..6}; do
  curl -k -X POST https://your-nas.example.com/login.php \
    -d "username=admin&password=wrong"
done
```

Expected: HTTP 403 after 5 attempts

### 7.3 Test CSRF Protection

```bash
curl -k -X POST https://your-nas.example.com/api/storage/pools.php \
  -H "Content-Type: application/json" \
  -d '{"action":"create","name":"test"}'
```

Expected: HTTP 403 "Invalid or expired CSRF token"

### 7.4 SSL Test

```bash
nmap --script ssl-enum-ciphers -p 443 your-nas.example.com
```

Expected: Only TLSv1.2 and TLSv1.3, strong ciphers only

---

## Part 8: Backup & Recovery

### 8.1 Backup Critical Data

```bash
# Create backup script
cat > /usr/local/bin/dplaneos-backup.sh << 'EOF'
#!/bin/bash
BACKUP_DIR="/backup/dplaneos/$(date +%Y%m%d_%H%M%S)"
mkdir -p "$BACKUP_DIR"

# Database
cp /var/dplane/database/dplane.db "$BACKUP_DIR/"

# Configuration
cp -r /var/dplane/system/config "$BACKUP_DIR/"

# Sudoers
cp /etc/sudoers.d/dplaneos "$BACKUP_DIR/"

# Compress
tar -czf "$BACKUP_DIR.tar.gz" -C "$(dirname $BACKUP_DIR)" "$(basename $BACKUP_DIR)"
rm -rf "$BACKUP_DIR"

# Cleanup old backups (keep 30 days)
find /backup/dplaneos -name "*.tar.gz" -mtime +30 -delete
EOF

chmod +x /usr/local/bin/dplaneos-backup.sh
```

### 8.2 Schedule Backups

```bash
crontab -e
# Add:
0 2 * * * /usr/local/bin/dplaneos-backup.sh
```

---

## Part 9: Emergency Procedures

### 9.1 If Compromised

```bash
# 1. Immediately block all access
ufw deny 443/tcp
ufw deny 80/tcp

# 2. Check what happened
grep -r "SECURITY:" /var/dplane/logs/
fail2ban-client status dplaneos-login

# 3. Check for backdoors
find /var/dplane -name "*.php" -mtime -7
find /var/www -name "*.php" -mtime -7

# 4. Restore from backup
# 5. Change all passwords
# 6. Review logs thoroughly before re-enabling
```

### 9.2 If Locked Out

```bash
# SSH into server
sudo sqlite3 /var/dplane/database/dplane.db

# Clear rate limits
DELETE FROM rate_limits;
DELETE FROM brute_force_bans;

# Reset admin password
UPDATE users SET password = '$2y$10$92IXUNpkjO0rOQ5byMi.Ye4oKoEa3Ro9llC/.og/at2.uheWG/igi' WHERE username = 'admin';
# Password is: password (CHANGE IMMEDIATELY)

.quit
```

---

## Part 10: Maintenance

### Daily

- Monitor fail2ban bans
- Check for failed logins
- Review error logs

### Weekly

- Review rate limit hits
- Check for system updates
- Verify backups

### Monthly

- Full security audit
- Update SSL certificates (if not auto-renewed)
- Review and rotate logs
- Test restore procedure

---

## Security Checklist

Before going live:

- [ ] Firewall configured and enabled
- [ ] Fail2ban configured and running
- [ ] Nginx HTTPS with valid SSL
- [ ] Rate limiting tested
- [ ] Brute force protection tested
- [ ] CSRF protection tested
- [ ] Default password changed
- [ ] Backups configured
- [ ] Monitoring configured
- [ ] Emergency procedures documented
- [ ] IP allowlist configured (if applicable)
- [ ] All services running
- [ ] Security headers verified
- [ ] No exposed debug endpoints
- [ ] Logs rotating properly

---

## Security Contact

If you discover vulnerabilities:
- **DO NOT** create public GitHub issues
- Contact privately via SECURITY.md
- Allow 90 days for fix before disclosure

---

## Updates

Check for security updates:
```bash
wget https://releases.dplaneos.dev/latest/SHA256SUMS
# Compare with installed version
```

---

**You are now ready for internet-facing deployment.**

**Remember: Security is ongoing. Monitor, update, test regularly.**
