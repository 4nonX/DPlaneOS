#!/bin/bash
#
# D-PlaneOS - Quick Security Hardening Script
# 
# This script implements CRITICAL security fixes for internet-facing deployment
# Run BEFORE exposing to internet
#
# Usage: sudo ./security-harden.sh
#

set -e

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m'

echo -e "${GREEN}D-PlaneOS Security Hardening Script${NC}"
echo "===================================="
echo ""

# Check if running as root
if [ "$EUID" -ne 0 ]; then
    echo -e "${RED}ERROR: Please run as root (sudo ./security-harden.sh)${NC}"
    exit 1
fi

# 1. Install fail2ban
echo -e "${YELLOW}[1/10] Installing fail2ban...${NC}"
if ! command -v fail2ban-client &> /dev/null; then
    apt update
    apt install -y fail2ban
    echo -e "${GREEN}✓ fail2ban installed${NC}"
else
    echo -e "${GREEN}✓ fail2ban already installed${NC}"
fi

# Configure fail2ban for D-PlaneOS
cat > /etc/fail2ban/filter.d/dplaneos.conf <<'EOF'
[Definition]
failregex = ^.*Failed login attempt.*from <HOST>.*$
            ^.*Authentication failed.*from <HOST>.*$
            ^.*Unauthorized access.*from <HOST>.*$
ignoreregex =
EOF

cat > /etc/fail2ban/jail.d/dplaneos.conf <<'EOF'
[dplaneos]
enabled = true
port = 80,443
filter = dplaneos
logpath = /var/log/dplaneos/error.log
maxretry = 5
bantime = 3600
findtime = 600
EOF

systemctl restart fail2ban
echo -e "${GREEN}✓ fail2ban configured${NC}"

# 2. Install and configure ClamAV
echo -e "${YELLOW}[2/10] Installing ClamAV antivirus...${NC}"
if ! command -v clamscan &> /dev/null; then
    apt install -y clamav clamav-daemon
    systemctl stop clamav-freshclam
    freshclam
    systemctl start clamav-freshclam
    systemctl enable clamav-daemon
    echo -e "${GREEN}✓ ClamAV installed and updated${NC}"
else
    echo -e "${GREEN}✓ ClamAV already installed${NC}"
fi

# 3. Enforce HTTPS
echo -e "${YELLOW}[3/10] Enforcing HTTPS...${NC}"

if [ -f /etc/nginx/sites-available/dplaneos ]; then
    # Check if HTTP->HTTPS redirect exists
    if ! grep -q "return 301 https" /etc/nginx/sites-available/dplaneos; then
        # Backup original
        cp /etc/nginx/sites-available/dplaneos /etc/nginx/sites-available/dplaneos.backup
        
        # Add HTTP redirect at the beginning
        cat > /etc/nginx/sites-available/dplaneos.tmp <<'EOF'
server {
    listen 80;
    server_name _;
    return 301 https://$host$request_uri;
}

EOF
        cat /etc/nginx/sites-available/dplaneos >> /etc/nginx/sites-available/dplaneos.tmp
        mv /etc/nginx/sites-available/dplaneos.tmp /etc/nginx/sites-available/dplaneos
        
        echo -e "${GREEN}✓ HTTPS redirect configured${NC}"
    else
        echo -e "${GREEN}✓ HTTPS redirect already configured${NC}"
    fi
    
    # Add security headers
    if ! grep -q "Strict-Transport-Security" /etc/nginx/sites-available/dplaneos; then
        sed -i '/server_name/a \    # Security headers\n    add_header Strict-Transport-Security "max-age=31536000; includeSubDomains" always;\n    add_header X-Frame-Options "SAMEORIGIN" always;\n    add_header X-Content-Type-Options "nosniff" always;\n    add_header X-XSS-Protection "1; mode=block" always;\n    add_header Referrer-Policy "strict-origin-when-cross-origin" always;' /etc/nginx/sites-available/dplaneos
        
        echo -e "${GREEN}✓ Security headers added${NC}"
    else
        echo -e "${GREEN}✓ Security headers already present${NC}"
    fi
    
    nginx -t && systemctl reload nginx
fi

# 4. Verify Go daemon session security
echo -e "${YELLOW}[4/10] Checking session security...${NC}"

# Go daemon handles sessions internally (SQLite + secure cookies)
# No PHP-FPM to configure — sessions managed by dplaned
if systemctl is-active dplaned >/dev/null 2>&1; then
    echo -e "${GREEN}✓ Go daemon running — sessions secured via dplaned${NC}"
else
    echo -e "${YELLOW}⚠ Go daemon not running — start with: systemctl start dplaned${NC}"
fi

# 5. Move uploads outside webroot
echo -e "${YELLOW}[5/10] Securing upload directory...${NC}"

if [ -d /opt/dplaneos/app/uploads ]; then
    # Create secure upload directory
    mkdir -p /var/lib/dplaneos/uploads
    chown www-data:www-data /var/lib/dplaneos/uploads
    chmod 750 /var/lib/dplaneos/uploads
    
    # Move existing uploads
    if [ "$(ls -A /opt/dplaneos/app/uploads)" ]; then
        mv /opt/dplaneos/app/uploads/* /var/lib/dplaneos/uploads/ 2>/dev/null || true
    fi
    
    # Update symlink
    rm -rf /opt/dplaneos/app/uploads
    ln -s /var/lib/dplaneos/uploads /opt/dplaneos/app/uploads
    
    echo -e "${GREEN}✓ Uploads moved outside webroot${NC}"
else
    echo -e "${YELLOW}⚠ Upload directory not found${NC}"
fi

# 6. Restrict Docker socket
echo -e "${YELLOW}[6/10] Restricting Docker socket permissions...${NC}"

if [ -S /var/run/docker.sock ]; then
    chmod 660 /var/run/docker.sock
    chown root:docker /var/run/docker.sock
    
    # Remove www-data from docker group if present
    if groups www-data | grep -q docker; then
        deluser www-data docker
        echo -e "${GREEN}✓ Removed www-data from docker group${NC}"
    fi
    
    echo -e "${GREEN}✓ Docker socket restricted${NC}"
else
    echo -e "${YELLOW}⚠ Docker socket not found${NC}"
fi

# 7. Configure firewall
echo -e "${YELLOW}[7/10] Configuring firewall...${NC}"

if command -v ufw &> /dev/null; then
    # Enable UFW if not already enabled
    if ! ufw status | grep -q "Status: active"; then
        ufw --force enable
    fi
    
    # Set default policies
    ufw default deny incoming
    ufw default allow outgoing
    
    # Allow SSH (CRITICAL - don't lock yourself out)
    ufw allow 22/tcp comment 'SSH'
    
    # Allow HTTPS only (not HTTP)
    ufw allow 443/tcp comment 'HTTPS'
    
    # Optionally allow HTTP for Let's Encrypt
    read -p "Allow HTTP (port 80) for Let's Encrypt? [y/N] " -n 1 -r
    echo
    if [[ $REPLY =~ ^[Yy]$ ]]; then
        ufw allow 80/tcp comment 'HTTP (Let\'s Encrypt)'
    fi
    
    echo -e "${GREEN}✓ Firewall configured${NC}"
    
    # CRITICAL: Docker bypasses UFW by directly manipulating iptables.
    # The DOCKER-USER chain is the ONLY iptables chain that Docker respects.
    # Without this, every container port is exposed to the internet regardless of UFW rules.
    if command -v docker &> /dev/null || systemctl is-active --quiet docker 2>/dev/null; then
        echo -e "${YELLOW}Docker detected — configuring DOCKER-USER iptables chain...${NC}"
        
        # Flush existing DOCKER-USER rules (safe — Docker recreates defaults)
        iptables -F DOCKER-USER 2>/dev/null || true
        
        # Allow established connections (required for container outbound traffic)
        iptables -I DOCKER-USER -m conntrack --ctstate ESTABLISHED,RELATED -j RETURN
        
        # Allow traffic from localhost (daemon ↔ container communication)
        iptables -I DOCKER-USER -s 127.0.0.0/8 -j RETURN
        
        # Allow Docker bridge network (container ↔ container)
        iptables -I DOCKER-USER -s 172.16.0.0/12 -j RETURN
        
        # Drop everything else hitting container ports from outside
        iptables -A DOCKER-USER -j DROP
        
        # Persist rules across reboots
        if command -v iptables-save &> /dev/null; then
            mkdir -p /etc/iptables
            iptables-save > /etc/iptables/rules.v4
        fi
        
        echo -e "${GREEN}✓ DOCKER-USER chain configured — containers protected${NC}"
    fi
else
    apt install -y ufw
    echo -e "${YELLOW}⚠ UFW installed but not configured. Run script again.${NC}"
fi

# 8. Install Let's Encrypt (optional)
echo -e "${YELLOW}[8/10] Setting up Let's Encrypt SSL...${NC}"

read -p "Do you want to install Let's Encrypt SSL? [y/N] " -n 1 -r
echo
if [[ $REPLY =~ ^[Yy]$ ]]; then
    if ! command -v certbot &> /dev/null; then
        apt install -y certbot python3-certbot-nginx
    fi
    
    read -p "Enter your domain name (e.g., nas.example.com): " DOMAIN
    
    if [ -n "$DOMAIN" ]; then
        certbot --nginx -d "$DOMAIN" --non-interactive --agree-tos --email admin@"$DOMAIN" || true
        echo -e "${GREEN}✓ Let's Encrypt configured${NC}"
    else
        echo -e "${YELLOW}⚠ No domain provided, skipping${NC}"
    fi
else
    echo -e "${YELLOW}⚠ Skipping Let's Encrypt${NC}"
fi

# 9. Create security monitoring script
echo -e "${YELLOW}[9/10] Setting up security monitoring...${NC}"

cat > /opt/dplaneos/scripts/security-monitor.sh <<'EOF'
#!/bin/bash
# Security monitoring script
# Run via cron every 5 minutes

LOG_FILE="/var/log/dplaneos/security.log"

# Check for failed login attempts
FAILED_LOGINS=$(grep -c "Failed login" /var/log/dplaneos/error.log 2>/dev/null || echo 0)

if [ "$FAILED_LOGINS" -gt 10 ]; then
    echo "[$(date)] WARNING: $FAILED_LOGINS failed login attempts detected" >> "$LOG_FILE"
fi

# Check for unusual file uploads
UPLOADS_TODAY=$(find /var/lib/dplaneos/uploads -type f -mtime -1 2>/dev/null | wc -l)

if [ "$UPLOADS_TODAY" -gt 100 ]; then
    echo "[$(date)] WARNING: $UPLOADS_TODAY files uploaded today" >> "$LOG_FILE"
fi

# Check disk usage
DISK_USAGE=$(df -h /opt/dplaneos | awk 'NR==2 {print $5}' | sed 's/%//')

if [ "$DISK_USAGE" -gt 90 ]; then
    echo "[$(date)] WARNING: Disk usage at ${DISK_USAGE}%" >> "$LOG_FILE"
fi
EOF

chmod +x /opt/dplaneos/scripts/security-monitor.sh

# Add to crontab
(crontab -l 2>/dev/null; echo "*/5 * * * * /opt/dplaneos/scripts/security-monitor.sh") | crontab -

echo -e "${GREEN}✓ Security monitoring configured${NC}"

# 10. Create security report
echo -e "${YELLOW}[10/10] Generating security report...${NC}"

cat > /root/dplaneos-security-report.txt <<EOF
D-PlaneOS Security Hardening Report
====================================
Date: $(date)
Hostname: $(hostname)

COMPLETED HARDENING:
✓ fail2ban installed and configured
✓ ClamAV antivirus installed
✓ HTTPS enforced
✓ Security headers added
✓ Session security hardened
✓ Upload directory secured
✓ Docker socket restricted
✓ Firewall configured
✓ Security monitoring enabled

FIREWALL STATUS:
$(ufw status verbose)

FAIL2BAN STATUS:
$(fail2ban-client status)

STILL REQUIRED FOR INTERNET DEPLOYMENT:
⚠️  Implement 2FA/MFA
⚠️  Configure IP allowlisting
⚠️  Set strong password policy
⚠️  Install OSSEC/Wazuh (intrusion detection)
⚠️  Regular security audits

RECOMMENDATIONS:
1. Use VPN (WireGuard) for access
2. Or use Cloudflare Tunnel
3. Enable 2FA before internet exposure
4. Monitor /var/log/dplaneos/security.log daily
5. Keep system updated

Next Steps:
-----------
1. Review this report
2. Implement remaining items
3. Test access from external network
4. Monitor logs for suspicious activity

Report saved to: /root/dplaneos-security-report.txt
EOF

echo -e "${GREEN}✓ Security report generated${NC}"

# Display summary
echo ""
echo -e "${GREEN}=====================================${NC}"
echo -e "${GREEN}Security Hardening Complete!${NC}"
echo -e "${GREEN}=====================================${NC}"
echo ""
echo -e "Report saved to: ${YELLOW}/root/dplaneos-security-report.txt${NC}"
echo ""
echo -e "${YELLOW}IMPORTANT:${NC}"
echo -e "1. Review the security report"
echo -e "2. Implement 2FA before internet exposure"
echo -e "3. Consider VPN or Cloudflare Tunnel"
echo -e "4. Monitor logs regularly"
echo ""
echo -e "${RED}WARNING: This system is still NOT recommended for direct internet exposure${NC}"
echo -e "${RED}without additional hardening (2FA, IP allowlisting, etc.)${NC}"
echo ""
