#!/bin/bash
#
# D-PlaneOS Health Check Script
#
# Verifies all components are running correctly
# Can be run at any time to check system status
#

# Colors
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m'

FAILED_CHECKS=0
TOTAL_CHECKS=0

check_pass() {
    echo -e "${GREEN}✓${NC} $1"
    TOTAL_CHECKS=$((TOTAL_CHECKS + 1))
}

check_fail() {
    echo -e "${RED}✗${NC} $1"
    FAILED_CHECKS=$((FAILED_CHECKS + 1))
    TOTAL_CHECKS=$((TOTAL_CHECKS + 1))
}

check_warn() {
    echo -e "${YELLOW}⚠${NC} $1"
    TOTAL_CHECKS=$((TOTAL_CHECKS + 1))
}

echo "=========================================="
echo "    D-PlaneOS Health Check"
echo "=========================================="
echo ""

# 1. File System
echo "📁 File System:"
if [ -d "/opt/dplaneos" ]; then
    check_pass "Installation directory exists"
else
    check_fail "Installation directory not found"
fi

if [ -f "/opt/dplaneos/install.sh" ]; then
    check_pass "Installer present"
else
    check_warn "Installer not found (may have been removed)"
fi

# 2. Permissions
echo ""
echo "🔒 Permissions:"
if [ -w "/opt/dplaneos/app/uploads" ]; then
    check_pass "Upload directory is writable"
else
    check_fail "Upload directory not writable (run: sudo chown -R www-data:www-data /opt/dplaneos/app/uploads)"
fi

if [ -w "/var/log/dplaneos" ]; then
    check_pass "Log directory is writable"
else
    check_fail "Log directory not writable"
fi

# 3. Webserver
echo ""
echo "🌐 Webserver:"
if systemctl is-active --quiet nginx 2>/dev/null; then
    check_pass "Nginx is running"
elif systemctl is-active --quiet apache2 2>/dev/null; then
    check_pass "Apache is running"
elif systemctl is-active --quiet httpd 2>/dev/null; then
    check_pass "Httpd is running"
else
    check_fail "No webserver running"
fi

# Check if site config exists
if [ -f "/etc/nginx/sites-enabled/dplaneos" ]; then
    check_pass "Nginx site config installed"
elif [ -f "/etc/apache2/sites-enabled/dplaneos.conf" ]; then
    check_pass "Apache site config installed"
elif [ -f "/etc/httpd/conf.d/dplaneos.conf" ]; then
    check_pass "Httpd site config installed"
else
    check_warn "Site config not found in standard locations"
fi

# 4. PHP
echo ""
echo "🐘 PHP:"
# Go daemon health check
if systemctl is-active dplaned >/dev/null 2>&1; then
    echo -e "${GREEN}✓ Go daemon (dplaned): running${NC}"
else
    echo -e "${RED}✗ Go daemon (dplaned): not running${NC}"
    WARNINGS=$((WARNINGS + 1))
fi

# Check PHP-FPM
# (PHP-FPM removed — Go architecture, dplaned checked above)

# 5. Services
echo ""
echo "⚙️  D-PlaneOS Services:"

# Main daemon
if systemctl is-active --quiet dplaneos-daemon 2>/dev/null; then
    check_pass "Main daemon running (systemd)"
elif pgrep -f "dplaneos-daemon" &>/dev/null; then
    check_pass "Main daemon running (process)"
else
    check_fail "Main daemon not running"
fi

# Realtime service
if systemctl is-active --quiet dplaneos-realtime 2>/dev/null; then
    check_pass "Realtime service running (systemd)"
elif pgrep -f "dplaneos-realtime" &>/dev/null; then
    check_pass "Realtime service running (process)"
else
    check_warn "Realtime service not running (dashboard graphs may not work)"
fi

# 6. Ports
echo ""
echo "🔌 Ports:"

# Main daemon (8080)
if curl -s --max-time 2 http://localhost:8080/api/health &>/dev/null; then
    check_pass "Main daemon responding on port 8080"
else
    check_fail "Main daemon not responding on port 8080"
fi

# Realtime service (8081 or alternative)
REALTIME_RESPONDING=0
for port in 8081 8082 8083 8084 8085; do
    if curl -s --max-time 2 http://localhost:$port/health &>/dev/null; then
        check_pass "Realtime service responding on port $port"
        REALTIME_RESPONDING=1
        break
    fi
done

if [ $REALTIME_RESPONDING -eq 0 ]; then
    check_warn "Realtime service not responding on any port"
fi

# Webserver (80)
if curl -s --max-time 2 http://localhost/ &>/dev/null; then
    check_pass "Webserver responding on port 80"
else
    check_fail "Webserver not responding on port 80"
fi

# 7. Assets
echo ""
echo "📦 Assets:"

if [ -f "/opt/dplaneos/app/assets/css/material-symbols-local.css" ]; then
    check_pass "Local icon CSS present"
else
    check_warn "Local icon CSS not found"
fi

if [ -f "/opt/dplaneos/app/assets/fonts/material-symbols-rounded.woff2" ]; then
    check_pass "Icon font file present (offline support)"
else
    check_warn "Icon font not downloaded (system will use fallbacks)"
fi

# 8. Configuration
echo ""
echo "⚙️  Configuration:"

if [ -f "/etc/dplaneos-status" ]; then
    check_pass "Installation status file exists"
    
    # Read version
    VERSION=$(grep "INSTALLED_VERSION" /etc/dplaneos-status | cut -d'=' -f2)
    if [ -n "$VERSION" ]; then
        echo "   Version: $VERSION"
    fi
else
    check_warn "Installation status file not found"
fi

# 9. ZFS (if available)
echo ""
echo "💾 Storage:"

if command -v zfs &> /dev/null; then
    check_pass "ZFS available"
    
    # Check for pools
    POOL_COUNT=$(zpool list 2>/dev/null | tail -n +2 | wc -l)
    if [ "$POOL_COUNT" -gt 0 ]; then
        check_pass "ZFS pools found: $POOL_COUNT"
    else
        check_warn "No ZFS pools configured"
    fi
else
    check_warn "ZFS not installed (optional)"
fi

# 10. Docker (if available)
echo ""
echo "🐳 Docker:"

if command -v docker &> /dev/null; then
    check_pass "Docker available"
    
    # Check if running
    if systemctl is-active --quiet docker 2>/dev/null; then
        check_pass "Docker service running"
    else
        check_warn "Docker service not running"
    fi
    
    # Check container count
    CONTAINER_COUNT=$(docker ps -a 2>/dev/null | tail -n +2 | wc -l)
    if [ "$CONTAINER_COUNT" -gt 0 ]; then
        echo "   Containers: $CONTAINER_COUNT"
    fi
else
    check_warn "Docker not installed (optional)"
fi

# Summary
echo ""
echo "=========================================="
echo "Summary:"
echo "  Total checks: $TOTAL_CHECKS"
echo "  Failed: $FAILED_CHECKS"
echo "  Success rate: $(( (TOTAL_CHECKS - FAILED_CHECKS) * 100 / TOTAL_CHECKS ))%"
echo "=========================================="

if [ $FAILED_CHECKS -eq 0 ]; then
    echo -e "${GREEN}All checks passed!${NC} ✅"
    echo ""
    echo "Access D-PlaneOS at: http://$(hostname -I | awk '{print $1}')"
    exit 0
else
    echo -e "${YELLOW}Some checks failed${NC} ⚠️"
    echo ""
    echo "Please review the failed checks above and fix any issues."
    exit 1
fi
