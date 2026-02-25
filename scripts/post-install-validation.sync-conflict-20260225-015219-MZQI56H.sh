#!/bin/bash
#
# D-PlaneOS - Post-Install Validation
# 
# This script runs AFTER installation to verify everything works.
# If ANY critical check fails, installation is considered failed.
#
# VALIDATES:
# 1. All services running
# 2. Web UI accessible
# 3. Login works
# 4. Database writable
# 5. ZFS commands work
# 6. APIs respond
#
# Usage: Called automatically by install.sh
#

set -e

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
BOLD='\033[1m'
NC='\033[0m'

ERRORS=0
WARNINGS=0

echo ""
echo -e "${BOLD}${BLUE}D-PlaneOS v$(cat "$(dirname "$0")/../VERSION" 2>/dev/null | tr -d "[:space:]" || echo "?") Post-Install Validation${NC}"
echo "==========================================="
echo ""

log_pass() {
    echo -e "${GREEN}✓${NC} $1"
}

log_fail() {
    echo -e "${RED}✗${NC} $1"
    ERRORS=$((ERRORS + 1))
}

log_warn() {
    echo -e "${YELLOW}⚠${NC} $1"
    WARNINGS=$((WARNINGS + 1))
}

log_info() {
    echo -e "${BLUE}ℹ${NC} $1"
}

# ============================================================
# CHECK 1: Services Running
# ============================================================

echo "1. Checking services..."

# nginx
if systemctl is-active nginx &>/dev/null; then
    log_pass "nginx running"
else
    log_fail "nginx not running"
    log_info "Try: systemctl start nginx"
fi

# PHP-FPM (detect version)
DAEMON_ACTIVE=$(systemctl is-active dplaned 2>/dev/null || echo "inactive")
DAEMON_SERVICE="dplaned"

if systemctl is-active "$PHP_SERVICE" &>/dev/null; then
    log_pass "PHP-FPM running (v${PHP_VERSION})"
else
    log_fail "PHP-FPM not running"
    log_info "Try: systemctl start $PHP_SERVICE"
fi

echo ""

# ============================================================
# CHECK 2: Database
# ============================================================

echo "2. Checking database..."

DB_PATH="/var/lib/dplaneos/dplaneos.db"

if [ -f "$DB_PATH" ]; then
    log_pass "Database file exists"
    
    # Check permissions
    if [ -r "$DB_PATH" ] && [ -w "$DB_PATH" ]; then
        log_pass "Database readable and writable"
    else
        log_fail "Database permissions incorrect"
        log_info "Try: chmod 664 $DB_PATH"
    fi
    
    # Check WAL mode
    WAL_MODE=$(sqlite3 "$DB_PATH" "PRAGMA journal_mode;" 2>/dev/null || echo "error")
    if [ "$WAL_MODE" = "wal" ]; then
        log_pass "WAL mode enabled"
    else
        log_warn "WAL mode not enabled (journal_mode: $WAL_MODE)"
    fi
    
    # Check FTS5
    FTS5_EXISTS=$(sqlite3 "$DB_PATH" "SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='files_fts';" 2>/dev/null || echo "0")
    if [ "$FTS5_EXISTS" -eq 1 ]; then
        log_pass "FTS5 search enabled"
    else
        log_warn "FTS5 table not found"
    fi
else
    log_fail "Database file not found"
fi

echo ""

# ============================================================
# CHECK 3: Web UI Accessibility
# ============================================================

echo "3. Checking web UI..."

# Try to access index page
HTTP_RESPONSE=$(curl -s -o /dev/null -w "%{http_code}" http://localhost/ 2>/dev/null || echo "000")

if [ "$HTTP_RESPONSE" = "200" ] || [ "$HTTP_RESPONSE" = "302" ]; then
    log_pass "Web UI accessible (HTTP $HTTP_RESPONSE)"
else
    log_fail "Web UI not accessible (HTTP $HTTP_RESPONSE)"
    log_info "Check nginx error log: /var/log/nginx/error.log"
fi

# Check if PHP is being processed
DAEMON_TEST=$(curl -sf http://localhost/health 2>/dev/null || echo "fail")
if [ -z "$PHP_TEST" ]; then
    log_pass "PHP processing works"
else
    log_warn "PHP code visible in output (not being processed)"
fi

echo ""

# ============================================================
# CHECK 4: PHP Functionality
# ============================================================

echo "4. Checking PHP functionality..."

# Test PHP execution
DAEMON_WORKS=$(curl -sf http://localhost/api/system/info 2>/dev/null || echo "fail")
if [ "$PHP_WORKS" = "ok" ]; then
    log_pass "PHP executable works"
else
    log_fail "PHP execution failed"
fi

# Check SQLite database accessibility
DAEMON_DB=$(sqlite3 /var/lib/dplaneos/dplaneos.db "SELECT 1" 2>/dev/null || echo "fail")
if [ "$DAEMON_DB" = "1" ]; then
    log_pass "SQLite database accessible"
else
    log_fail "SQLite database not accessible"
    log_info "Check: ls -la /var/lib/dplaneos/dplaneos.db"
fi

# Test database via Go daemon API
DB_API_TEST=$(curl -sf http://localhost:9000/api/system/info 2>/dev/null || echo "fail")

if [ "$PHP_DB_TEST" = "ok" ]; then
    log_pass "PHP can access database"
else
    log_fail "PHP cannot access database"
    log_info "Error: $PHP_DB_TEST"
fi

echo ""

# ============================================================
# CHECK 5: File Permissions
# ============================================================

echo "5. Checking file permissions..."

# Check installation directory
if [ -d "/opt/dplaneos" ]; then
    log_pass "Installation directory exists"
    
    # Check www-data ownership
    OWNER=$(stat -c '%U' /opt/dplaneos/app 2>/dev/null || echo "unknown")
    if [ "$OWNER" = "www-data" ]; then
        log_pass "Correct ownership (www-data)"
    else
        log_warn "Ownership incorrect: $OWNER (should be www-data)"
    fi
else
    log_fail "Installation directory not found"
fi

# Check database directory
if [ -d "/var/lib/dplaneos" ]; then
    DB_DIR_PERMS=$(stat -c '%a' /var/lib/dplaneos 2>/dev/null || echo "000")
    if [ "$DB_DIR_PERMS" = "775" ] || [ "$DB_DIR_PERMS" = "755" ]; then
        log_pass "Database directory permissions correct ($DB_DIR_PERMS)"
    else
        log_warn "Database directory permissions: $DB_DIR_PERMS (recommend 775)"
    fi
fi

echo ""

# ============================================================
# CHECK 6: sudoers Configuration
# ============================================================

echo "6. Checking sudoers..."

SUDOERS_FILE="/etc/sudoers.d/dplaneos"

if [ -f "$SUDOERS_FILE" ]; then
    log_pass "sudoers file exists"
    
    # Check permissions
    SUDOERS_PERMS=$(stat -c '%a' "$SUDOERS_FILE")
    if [ "$SUDOERS_PERMS" = "440" ]; then
        log_pass "sudoers permissions correct"
    else
        log_fail "sudoers permissions incorrect: $SUDOERS_PERMS (should be 440)"
    fi
    
    # Validate syntax
    if visudo -c -f "$SUDOERS_FILE" &>/dev/null; then
        log_pass "sudoers syntax valid"
    else
        log_fail "sudoers syntax invalid"
    fi
else
    log_fail "sudoers file not found"
fi

echo ""

# ============================================================
# CHECK 7: ZFS Functionality
# ============================================================

echo "7. Checking ZFS..."

if command -v zpool &>/dev/null; then
    log_pass "ZFS utilities installed"
    
    # Try to list pools (should work even with no pools)
    if zpool list &>/dev/null; then
        log_pass "zpool command works"
        
        POOL_COUNT=$(zpool list -H 2>/dev/null | wc -l)
        if [ "$POOL_COUNT" -gt 0 ]; then
            log_info "$POOL_COUNT ZFS pool(s) found"
        else
            log_info "No ZFS pools (create pools after installation)"
        fi
    else
        log_warn "zpool command failed (may need module load)"
    fi
else
    log_warn "ZFS not installed (optional)"
fi

echo ""

# ============================================================
# CHECK 8: System Tuning
# ============================================================

echo "8. Checking system tuning..."

# Check inotify limits
INOTIFY_WATCHES=$(sysctl -n fs.inotify.max_user_watches 2>/dev/null || echo "0")
if [ "$INOTIFY_WATCHES" -ge 524288 ]; then
    log_pass "inotify watches: $INOTIFY_WATCHES (optimized)"
elif [ "$INOTIFY_WATCHES" -ge 8192 ]; then
    log_warn "inotify watches: $INOTIFY_WATCHES (default, not optimized)"
else
    log_warn "inotify watches: $INOTIFY_WATCHES (too low)"
fi

echo ""

# ============================================================
# CHECK 9: Recovery CLI
# ============================================================

echo "9. Checking recovery CLI..."

if [ -f "/usr/local/bin/dplaneos-recovery" ]; then
    log_pass "Recovery CLI installed"
    
    if [ -x "/usr/local/bin/dplaneos-recovery" ]; then
        log_pass "Recovery CLI executable"
    else
        log_warn "Recovery CLI not executable"
    fi
else
    log_warn "Recovery CLI not found (run recovery-cli-install.sh)"
fi

echo ""

# ============================================================
# CHECK 10: Login Test (CRITICAL!)
# ============================================================

echo "10. Testing login functionality..."

# Test login via Go daemon API
LOGIN_TEST=$(curl -sf -X POST http://localhost:9000/api/auth/login \
    -H "Content-Type: application/json" \
    -d '{"username":"admin","password":"admin"}' 2>/dev/null)

if echo "$LOGIN_TEST" | grep -q "session_id\|token\|success"; then
    log_pass "Login functionality validated (Go daemon)"
else
    log_fail "Login functionality broken"
    log_info "Check: systemctl status dplaned"
fi

echo ""

# ============================================================
# SUMMARY
# ============================================================

echo "==========================================="
echo "Post-Install Validation Summary"
echo "==========================================="
echo ""

if [ $ERRORS -eq 0 ] && [ $WARNINGS -eq 0 ]; then
    echo -e "${BOLD}${GREEN}✓ ALL CHECKS PASSED!${NC}"
    echo ""
    echo "D-PlaneOS installation successful!"
    echo ""
    echo "Next steps:"
    echo "1. Access web UI: http://$(hostname -I | awk '{print $1}')"
    echo "2. Login: admin / admin"
    echo "3. Change password immediately"
    echo ""
    exit 0
elif [ $ERRORS -eq 0 ]; then
    echo -e "${BOLD}${YELLOW}⚠ $WARNINGS WARNING(S)${NC}"
    echo ""
    echo "Installation completed with warnings."
    echo "Some features may not work optimally."
    echo ""
    echo "Access web UI: http://$(hostname -I | awk '{print $1}')"
    echo ""
    exit 0
else
    echo -e "${BOLD}${RED}✗ $ERRORS ERROR(S), $WARNINGS WARNING(S)${NC}"
    echo ""
    echo "Installation completed but has CRITICAL errors!"
    echo "System may not function correctly."
    echo ""
    echo "Fix errors above or use recovery CLI:"
    echo "  sudo dplaneos-recovery"
    echo ""
    exit 1
fi
