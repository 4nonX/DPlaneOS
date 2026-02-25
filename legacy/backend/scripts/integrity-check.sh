#!/bin/bash
#
# D-PlaneOS System Integrity Check v1.13.0
# Verifies installation completeness and system health
#

set -e

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m'

ERRORS=0
WARNINGS=0
CHECKS=0

check() {
    CHECKS=$((CHECKS + 1))
}

pass() {
    echo -e "${GREEN}✓${NC} $1"
}

fail() {
    echo -e "${RED}✗${NC} $1"
    ERRORS=$((ERRORS + 1))
}

warn() {
    echo -e "${YELLOW}⚠${NC} $1"
    WARNINGS=$((WARNINGS + 1))
}

info() {
    echo -e "${BLUE}ℹ${NC} $1"
}

echo -e "${BLUE}╔══════════════════════════════════════════════════════╗${NC}"
echo -e "${BLUE}║     D-PlaneOS System Integrity Check v1.13.0        ║${NC}"
echo -e "${BLUE}╚══════════════════════════════════════════════════════╝${NC}"
echo ""

# Check 1: Version
echo -e "${BLUE}[1/15] Checking version...${NC}"
check
if [ -f /var/www/dplaneos/VERSION ]; then
    VERSION=$(cat /var/www/dplaneos/VERSION)
    if [ "$VERSION" == "1.13.0" ]; then
        pass "Version: $VERSION"
    else
        warn "Version is $VERSION, expected 1.13.0"
    fi
else
    fail "VERSION file not found"
fi
echo ""

# Check 2: Core API files
echo -e "${BLUE}[2/15] Checking API files...${NC}"
check
REQUIRED_APIS=(
    "/var/www/dplaneos/api/backup.php"
    "/var/www/dplaneos/api/disk-replacement.php"
    "/var/www/dplaneos/api/zfs.php"
    "/var/www/dplaneos/api/docker.php"
)
for api in "${REQUIRED_APIS[@]}"; do
    if [ -f "$api" ]; then
        pass "$(basename $api)"
    else
        fail "Missing: $api"
    fi
done
echo ""

# Check 3: JavaScript files
echo -e "${BLUE}[3/15] Checking JavaScript files...${NC}"
check
REQUIRED_JS=(
    "/var/www/dplaneos/js/backup.js"
    "/var/www/dplaneos/js/disk-replacement.js"
)
for js in "${REQUIRED_JS[@]}"; do
    if [ -f "$js" ]; then
        pass "$(basename $js)"
    else
        fail "Missing: $js"
    fi
done
echo ""

# Check 4: Database
echo -e "${BLUE}[4/15] Checking database...${NC}"
check
if [ -f /var/www/dplaneos/database.sqlite ]; then
    pass "Database file exists"
    
    # Check tables
    TABLES=$(sqlite3 /var/www/dplaneos/database.sqlite "SELECT name FROM sqlite_master WHERE type='table'" 2>/dev/null || echo "")
    
    if echo "$TABLES" | grep -q "config_backups"; then
        pass "Table: config_backups"
    else
        fail "Missing table: config_backups"
    fi
    
    if echo "$TABLES" | grep -q "disk_replacements"; then
        pass "Table: disk_replacements"
    else
        fail "Missing table: disk_replacements"
    fi
    
    if echo "$TABLES" | grep -q "disk_actions"; then
        pass "Table: disk_actions"
    else
        fail "Missing table: disk_actions"
    fi
else
    fail "Database file not found"
fi
echo ""

# Check 5: Backup directory
echo -e "${BLUE}[5/15] Checking backup directory...${NC}"
check
if [ -d /var/backups/dplaneos ]; then
    pass "Backup directory exists"
    
    PERMS=$(stat -c "%a" /var/backups/dplaneos)
    if [ "$PERMS" == "700" ]; then
        pass "Permissions: 700 (correct)"
    else
        warn "Permissions: $PERMS (should be 700)"
    fi
    
    OWNER=$(stat -c "%U:%G" /var/backups/dplaneos)
    if [ "$OWNER" == "www-data:www-data" ]; then
        pass "Owner: www-data:www-data (correct)"
    else
        warn "Owner: $OWNER (should be www-data:www-data)"
    fi
else
    fail "Backup directory not found"
fi
echo ""

# Check 6: Scripts
echo -e "${BLUE}[6/15] Checking scripts...${NC}"
check
if [ -f /var/www/dplaneos/scripts/auto-backup.php ]; then
    pass "auto-backup.php exists"
    if [ -x /var/www/dplaneos/scripts/auto-backup.php ]; then
        pass "auto-backup.php is executable"
    else
        warn "auto-backup.php is not executable"
    fi
else
    fail "auto-backup.php not found"
fi
echo ""

# Check 7: Dependencies
echo -e "${BLUE}[7/15] Checking dependencies...${NC}"
check

if command -v openssl >/dev/null 2>&1; then
    pass "OpenSSL installed"
else
    fail "OpenSSL not found (required for backups)"
fi

if command -v smartctl >/dev/null 2>&1; then
    pass "smartmontools installed"
else
    warn "smartmontools not found (needed for disk health)"
fi

if command -v docker >/dev/null 2>&1; then
    pass "Docker installed"
else
    warn "Docker not found"
fi

if command -v zpool >/dev/null 2>&1; then
    pass "ZFS installed"
else
    fail "ZFS not found"
fi

if command -v sqlite3 >/dev/null 2>&1; then
    pass "SQLite3 installed"
else
    warn "SQLite3 CLI not found"
fi
echo ""

# Check 8: Apache configuration
echo -e "${BLUE}[8/15] Checking Apache...${NC}"
check
if systemctl is-active --quiet apache2; then
    pass "Apache2 is running"
else
    fail "Apache2 is not running"
fi

if [ -f /etc/apache2/sites-enabled/dplaneos.conf ]; then
    pass "D-PlaneOS site enabled"
else
    warn "D-PlaneOS Apache config not found in sites-enabled"
fi
echo ""

# Check 9: PHP configuration
echo -e "${BLUE}[9/15] Checking PHP...${NC}"
check
PHP_VERSION=$(php -v 2>/dev/null | head -n1 | cut -d' ' -f2 | cut -d'.' -f1,2)
if [ ! -z "$PHP_VERSION" ]; then
    pass "PHP version: $PHP_VERSION"
    
    if php -m | grep -q sqlite3; then
        pass "PHP SQLite3 module loaded"
    else
        fail "PHP SQLite3 module not found"
    fi
    
    if php -m | grep -q curl; then
        pass "PHP cURL module loaded"
    else
        warn "PHP cURL module not found"
    fi
else
    fail "PHP not found"
fi
echo ""

# Check 10: File permissions
echo -e "${BLUE}[10/15] Checking file permissions...${NC}"
check
WEB_OWNER=$(stat -c "%U:%G" /var/www/dplaneos 2>/dev/null || echo "NOTFOUND")
if [ "$WEB_OWNER" == "www-data:www-data" ]; then
    pass "Web directory owner: www-data:www-data"
else
    warn "Web directory owner: $WEB_OWNER (should be www-data:www-data)"
fi
echo ""

# Check 11: ZFS pools
echo -e "${BLUE}[11/15] Checking ZFS pools...${NC}"
check
if command -v zpool >/dev/null 2>&1; then
    POOLS=$(zpool list -H -o name 2>/dev/null || echo "")
    if [ ! -z "$POOLS" ]; then
        echo "$POOLS" | while read pool; do
            STATUS=$(zpool status $pool | grep state: | awk '{print $2}')
            if [ "$STATUS" == "ONLINE" ]; then
                pass "Pool $pool: ONLINE"
            else
                warn "Pool $pool: $STATUS"
            fi
        done
    else
        info "No ZFS pools found (this may be normal for fresh install)"
    fi
else
    warn "ZFS not installed"
fi
echo ""

# Check 12: Docker containers
echo -e "${BLUE}[12/15] Checking Docker containers...${NC}"
check
if command -v docker >/dev/null 2>&1; then
    RUNNING=$(docker ps -q 2>/dev/null | wc -l)
    TOTAL=$(docker ps -a -q 2>/dev/null | wc -l)
    info "Docker containers: $RUNNING running, $TOTAL total"
else
    warn "Docker not installed"
fi
echo ""

# Check 13: Log files
echo -e "${BLUE}[13/15] Checking log files...${NC}"
check
if [ -f /var/log/dplaneos-backup.log ]; then
    pass "Backup log exists"
    SIZE=$(stat -c%s /var/log/dplaneos-backup.log)
    info "Backup log size: $SIZE bytes"
else
    warn "Backup log not found (will be created on first backup)"
fi

if [ -f /var/log/apache2/error.log ]; then
    pass "Apache error log exists"
    ERRORS_TODAY=$(grep "$(date +%Y-%m-%d)" /var/log/apache2/error.log 2>/dev/null | wc -l || echo 0)
    if [ $ERRORS_TODAY -gt 0 ]; then
        warn "Apache logged $ERRORS_TODAY errors today"
    else
        pass "No Apache errors today"
    fi
else
    warn "Apache error log not found"
fi
echo ""

# Check 14: Network connectivity
echo -e "${BLUE}[14/15] Checking network...${NC}"
check
if ping -c 1 8.8.8.8 >/dev/null 2>&1; then
    pass "Internet connectivity OK"
else
    warn "No internet connectivity (may affect updates)"
fi

if ip addr | grep -q "inet "; then
    IP=$(ip addr | grep "inet " | grep -v "127.0.0.1" | head -n1 | awk '{print $2}' | cut -d'/' -f1)
    pass "Network configured: $IP"
else
    fail "No network configuration found"
fi
echo ""

# Check 15: Disk space
echo -e "${BLUE}[15/15] Checking disk space...${NC}"
check
ROOT_USAGE=$(df -h / | tail -n1 | awk '{print $5}' | sed 's/%//')
if [ $ROOT_USAGE -lt 80 ]; then
    pass "Root filesystem: ${ROOT_USAGE}% used"
elif [ $ROOT_USAGE -lt 90 ]; then
    warn "Root filesystem: ${ROOT_USAGE}% used (getting full)"
else
    fail "Root filesystem: ${ROOT_USAGE}% used (critically full)"
fi

if [ -d /var/backups/dplaneos ]; then
    BACKUP_SIZE=$(du -sh /var/backups/dplaneos 2>/dev/null | awk '{print $1}')
    info "Backup directory size: $BACKUP_SIZE"
fi
echo ""

# Summary
echo -e "${BLUE}╔══════════════════════════════════════════════════════╗${NC}"
echo -e "${BLUE}║                    Summary                           ║${NC}"
echo -e "${BLUE}╚══════════════════════════════════════════════════════╝${NC}"
echo ""
echo "Total checks: $CHECKS"
echo -e "${GREEN}Passed: $((CHECKS - ERRORS - WARNINGS))${NC}"
echo -e "${YELLOW}Warnings: $WARNINGS${NC}"
echo -e "${RED}Errors: $ERRORS${NC}"
echo ""

if [ $ERRORS -eq 0 ] && [ $WARNINGS -eq 0 ]; then
    echo -e "${GREEN}╔══════════════════════════════════════════════════════╗${NC}"
    echo -e "${GREEN}║     System is fully operational! ✓                  ║${NC}"
    echo -e "${GREEN}╚══════════════════════════════════════════════════════╝${NC}"
    exit 0
elif [ $ERRORS -eq 0 ]; then
    echo -e "${YELLOW}╔══════════════════════════════════════════════════════╗${NC}"
    echo -e "${YELLOW}║     System is operational with minor warnings       ║${NC}"
    echo -e "${YELLOW}╚══════════════════════════════════════════════════════╝${NC}"
    exit 0
else
    echo -e "${RED}╔══════════════════════════════════════════════════════╗${NC}"
    echo -e "${RED}║     System has errors - please review above         ║${NC}"
    echo -e "${RED}╚══════════════════════════════════════════════════════╝${NC}"
    exit 1
fi
