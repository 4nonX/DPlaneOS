#!/bin/bash
#
# D-PlaneOS v1.13.0 Automated Test Suite
# Tests all new features
#

set -e

BLUE='\033[0;34m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
RED='\033[0;31m'
NC='\033[0m'

PASSED=0
FAILED=0

test_pass() {
    echo -e "${GREEN}✓${NC} $1"
    PASSED=$((PASSED + 1))
}

test_fail() {
    echo -e "${RED}✗${NC} $1"
    FAILED=$((FAILED + 1))
}

echo -e "${BLUE}╔══════════════════════════════════════════════════════╗${NC}"
echo -e "${BLUE}║     D-PlaneOS v1.13.0 Test Suite                    ║${NC}"
echo -e "${BLUE}╚══════════════════════════════════════════════════════╝${NC}"
echo ""

# Test 1: Config Backup API
echo -e "${BLUE}[TEST 1] Config Backup API${NC}"
echo "Testing backup creation..."

RESPONSE=$(curl -s -X POST http://localhost/api/backup.php?action=create 2>/dev/null || echo "")

if echo "$RESPONSE" | grep -q "success"; then
    test_pass "Backup creation API responds"
    
    if echo "$RESPONSE" | grep -q "password"; then
        test_pass "Password generated"
    else
        test_fail "No password in response"
    fi
    
    if echo "$RESPONSE" | grep -q "checksum"; then
        test_pass "Checksum generated"
    else
        test_fail "No checksum in response"
    fi
else
    test_fail "Backup creation failed: $RESPONSE"
fi
echo ""

# Test 2: Backup List API
echo -e "${BLUE}[TEST 2] Backup List API${NC}"
RESPONSE=$(curl -s http://localhost/api/backup.php?action=list 2>/dev/null || echo "")

if echo "$RESPONSE" | grep -q "backups"; then
    test_pass "Backup list API responds"
    
    COUNT=$(echo "$RESPONSE" | jq '.backups | length' 2>/dev/null || echo "0")
    if [ "$COUNT" -gt 0 ]; then
        test_pass "Found $COUNT backup(s)"
    else
        test_fail "No backups in list"
    fi
else
    test_fail "Backup list failed"
fi
echo ""

# Test 3: Disk Status API
echo -e "${BLUE}[TEST 3] Disk Replacement API${NC}"
RESPONSE=$(curl -s http://localhost/api/disk-replacement.php?action=status 2>/dev/null || echo "")

if echo "$RESPONSE" | grep -q "pools"; then
    test_pass "Disk status API responds"
else
    test_fail "Disk status API failed"
fi
echo ""

# Test 4: Database Tables
echo -e "${BLUE}[TEST 4] Database Schema${NC}"
TABLES=$(sqlite3 /var/www/dplaneos/database.sqlite "SELECT name FROM sqlite_master WHERE type='table'" 2>/dev/null || echo "")

if echo "$TABLES" | grep -q "config_backups"; then
    test_pass "Table: config_backups exists"
else
    test_fail "Table: config_backups missing"
fi

if echo "$TABLES" | grep -q "disk_replacements"; then
    test_pass "Table: disk_replacements exists"
else
    test_fail "Table: disk_replacements missing"
fi

if echo "$TABLES" | grep -q "disk_actions"; then
    test_pass "Table: disk_actions exists"
else
    test_fail "Table: disk_actions missing"
fi
echo ""

# Test 5: File Permissions
echo -e "${BLUE}[TEST 5] File Permissions${NC}"

if [ -d /var/backups/dplaneos ]; then
    PERMS=$(stat -c "%a" /var/backups/dplaneos)
    if [ "$PERMS" == "700" ]; then
        test_pass "Backup directory permissions correct (700)"
    else
        test_fail "Backup directory permissions wrong ($PERMS, expected 700)"
    fi
    
    OWNER=$(stat -c "%U:%G" /var/backups/dplaneos)
    if [ "$OWNER" == "www-data:www-data" ]; then
        test_pass "Backup directory owner correct"
    else
        test_fail "Backup directory owner wrong ($OWNER)"
    fi
else
    test_fail "Backup directory doesn't exist"
fi
echo ""

# Test 6: Dependencies
echo -e "${BLUE}[TEST 6] Dependencies${NC}"

if command -v openssl >/dev/null 2>&1; then
    test_pass "OpenSSL installed"
    
    # Test encryption/decryption
    echo "test data" > /tmp/test.txt
    openssl enc -aes-256-cbc -salt -pbkdf2 -in /tmp/test.txt -out /tmp/test.enc -pass pass:testpass 2>/dev/null
    if [ -f /tmp/test.enc ]; then
        test_pass "OpenSSL encryption works"
        
        openssl enc -d -aes-256-cbc -pbkdf2 -in /tmp/test.enc -out /tmp/test.dec -pass pass:testpass 2>/dev/null
        if diff /tmp/test.txt /tmp/test.dec >/dev/null 2>&1; then
            test_pass "OpenSSL decryption works"
        else
            test_fail "OpenSSL decryption failed"
        fi
    else
        test_fail "OpenSSL encryption failed"
    fi
    rm -f /tmp/test.* 2>/dev/null
else
    test_fail "OpenSSL not found"
fi

if command -v smartctl >/dev/null 2>&1; then
    test_pass "smartmontools installed"
else
    test_fail "smartmontools not found"
fi
echo ""

# Test 7: JavaScript Files
echo -e "${BLUE}[TEST 7] Frontend Files${NC}"

if [ -f /var/www/dplaneos/js/backup.js ]; then
    SIZE=$(wc -l < /var/www/dplaneos/js/backup.js)
    if [ $SIZE -gt 100 ]; then
        test_pass "backup.js exists ($SIZE lines)"
    else
        test_fail "backup.js too small ($SIZE lines)"
    fi
else
    test_fail "backup.js not found"
fi

if [ -f /var/www/dplaneos/js/disk-replacement.js ]; then
    SIZE=$(wc -l < /var/www/dplaneos/js/disk-replacement.js)
    if [ $SIZE -gt 100 ]; then
        test_pass "disk-replacement.js exists ($SIZE lines)"
    else
        test_fail "disk-replacement.js too small ($SIZE lines)"
    fi
else
    test_fail "disk-replacement.js not found"
fi
echo ""

# Test 8: Scripts
echo -e "${BLUE}[TEST 8] Scripts${NC}"

if [ -f /var/www/dplaneos/scripts/auto-backup.php ]; then
    if [ -x /var/www/dplaneos/scripts/auto-backup.php ]; then
        test_pass "auto-backup.php is executable"
    else
        test_fail "auto-backup.php not executable"
    fi
else
    test_fail "auto-backup.php not found"
fi
echo ""

# Test 9: Backup Creation & Cleanup
echo -e "${BLUE}[TEST 9] Backup Lifecycle${NC}"

# Count existing backups
BEFORE=$(ls -1 /var/backups/dplaneos/*.tar.gz.enc 2>/dev/null | wc -l || echo 0)
echo "Existing backups: $BEFORE"

# Create test backup
echo "Creating test backup..."
RESPONSE=$(curl -s -X POST http://localhost/api/backup.php?action=create 2>/dev/null || echo "")

if echo "$RESPONSE" | grep -q "success"; then
    AFTER=$(ls -1 /var/backups/dplaneos/*.tar.gz.enc 2>/dev/null | wc -l || echo 0)
    
    if [ $AFTER -gt $BEFORE ]; then
        test_pass "Backup file created on disk"
        
        # Get filename
        FILENAME=$(echo "$RESPONSE" | jq -r '.file' 2>/dev/null)
        if [ ! -z "$FILENAME" ] && [ -f "/var/backups/dplaneos/$FILENAME" ]; then
            test_pass "Backup file exists: $FILENAME"
            
            # Check file size
            SIZE=$(stat -c%s "/var/backups/dplaneos/$FILENAME")
            if [ $SIZE -gt 1000 ]; then
                test_pass "Backup file size reasonable ($SIZE bytes)"
            else
                test_fail "Backup file suspiciously small ($SIZE bytes)"
            fi
        else
            test_fail "Backup filename not found"
        fi
    else
        test_fail "No new backup file created"
    fi
else
    test_fail "Backup creation returned error"
fi
echo ""

# Test 10: Database Queries
echo -e "${BLUE}[TEST 10] Database Operations${NC}"

# Test backup metadata query
COUNT=$(sqlite3 /var/www/dplaneos/database.sqlite "SELECT COUNT(*) FROM config_backups" 2>/dev/null || echo "0")
if [ $COUNT -gt 0 ]; then
    test_pass "Backup metadata stored in database ($COUNT records)"
else
    test_fail "No backup metadata in database"
fi

# Test disk actions table
RESULT=$(sqlite3 /var/www/dplaneos/database.sqlite "INSERT INTO disk_actions (pool, device, action, details) VALUES ('test_pool', '/dev/sdz', 'offline', 'test action'); SELECT last_insert_rowid();" 2>/dev/null || echo "")
if [ ! -z "$RESULT" ] && [ "$RESULT" != "0" ]; then
    test_pass "Can insert disk actions"
    sqlite3 /var/www/dplaneos/database.sqlite "DELETE FROM disk_actions WHERE id = $RESULT" 2>/dev/null
else
    test_fail "Cannot insert disk actions"
fi
echo ""

# Summary
echo -e "${BLUE}╔══════════════════════════════════════════════════════╗${NC}"
echo -e "${BLUE}║                  Test Summary                        ║${NC}"
echo -e "${BLUE}╚══════════════════════════════════════════════════════╝${NC}"
echo ""
TOTAL=$((PASSED + FAILED))
echo "Total tests: $TOTAL"
echo -e "${GREEN}Passed: $PASSED${NC}"
echo -e "${RED}Failed: $FAILED${NC}"
echo ""

if [ $FAILED -eq 0 ]; then
    echo -e "${GREEN}╔══════════════════════════════════════════════════════╗${NC}"
    echo -e "${GREEN}║         All tests passed! ✓                          ║${NC}"
    echo -e "${GREEN}╚══════════════════════════════════════════════════════╝${NC}"
    exit 0
else
    echo -e "${RED}╔══════════════════════════════════════════════════════╗${NC}"
    echo -e "${RED}║         Some tests failed!                           ║${NC}"
    echo -e "${RED}╚══════════════════════════════════════════════════════╝${NC}"
    exit 1
fi
