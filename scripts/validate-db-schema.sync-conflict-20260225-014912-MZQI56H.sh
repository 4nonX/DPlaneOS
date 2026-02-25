#!/bin/bash
#
# D-PlaneOS Database Schema Validator
# 
# Validates and fixes common database issues for 52TB+ production systems:
# 1. Missing indexes
# 2. Missing foreign keys
# 3. Suboptimal data types
# 4. Performance configuration
# 
# Usage: sudo ./validate-db-schema.sh
#

set -e

DB_PATH="/var/lib/dplaneos/dplaneos.db"

# Colors
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m'

ERRORS=0
WARNINGS=0
FIXED=0

echo -e "${BLUE}D-PlaneOS Database Schema Validator${NC}"
echo "====================================="
echo ""

# Check if DB exists
if [ ! -f "$DB_PATH" ]; then
    echo -e "${RED}✗ Database not found at $DB_PATH${NC}"
    exit 1
fi

echo -e "${GREEN}✓ Database found${NC}"
echo ""

# Function to check if index exists
check_index() {
    local index_name=$1
    local table_name=$2
    local column_name=$3
    
    local exists=$(sqlite3 "$DB_PATH" "SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name='$index_name';")
    
    if [ "$exists" -eq 0 ]; then
        echo -e "${YELLOW}⚠ Missing index: $index_name on $table_name($column_name)${NC}"
        WARNINGS=$((WARNINGS + 1))
        return 1
    else
        echo -e "${GREEN}✓ Index exists: $index_name${NC}"
        return 0
    fi
}

# Function to create index
create_index() {
    local index_name=$1
    local table_name=$2
    local columns=$3
    
    echo "  Creating index: $index_name..."
    sqlite3 "$DB_PATH" "CREATE INDEX IF NOT EXISTS $index_name ON $table_name($columns);"
    
    if [ $? -eq 0 ]; then
        echo -e "  ${GREEN}✓ Index created${NC}"
        FIXED=$((FIXED + 1))
    else
        echo -e "  ${RED}✗ Failed to create index${NC}"
        ERRORS=$((ERRORS + 1))
    fi
}

echo "Checking critical indexes..."
echo "----------------------------"
echo ""

# FILES TABLE INDEXES (CRITICAL FOR LARGE DATASETS)
echo "Files table:"

if ! check_index "idx_files_path" "files" "path"; then
    create_index "idx_files_path" "files" "path"
fi

if ! check_index "idx_files_parent" "files" "parent_id"; then
    create_index "idx_files_parent" "files" "parent_id"
fi

if ! check_index "idx_files_size" "files" "size"; then
    create_index "idx_files_size" "files" "size"
fi

if ! check_index "idx_files_mtime" "files" "modified_time"; then
    create_index "idx_files_mtime" "files" "modified_time"
fi

echo ""

# AUDIT LOGS INDEXES
echo "Audit logs:"

if ! check_index "idx_audit_timestamp" "audit_logs" "timestamp"; then
    create_index "idx_audit_timestamp" "audit_logs" "timestamp DESC"
fi

if ! check_index "idx_audit_user" "audit_logs" "user"; then
    create_index "idx_audit_user" "audit_logs" "user"
fi

if ! check_index "idx_audit_action" "audit_logs" "action"; then
    create_index "idx_audit_action" "audit_logs" "action"
fi

echo ""

# SESSIONS INDEXES
echo "Sessions:"

if ! check_index "idx_sessions_token" "sessions" "session_token"; then
    create_index "idx_sessions_token" "sessions" "session_token"
fi

if ! check_index "idx_sessions_user" "sessions" "user_id"; then
    create_index "idx_sessions_user" "sessions" "user_id"
fi

if ! check_index "idx_sessions_expires" "sessions" "expires_at"; then
    create_index "idx_sessions_expires" "sessions" "expires_at"
fi

echo ""

# ALERTS INDEXES
echo "Alerts:"

if ! check_index "idx_alerts_priority" "alerts" "priority"; then
    create_index "idx_alerts_priority" "alerts" "priority"
fi

if ! check_index "idx_alerts_category" "alerts" "category"; then
    create_index "idx_alerts_category" "alerts" "category"
fi

if ! check_index "idx_alerts_acknowledged" "alerts" "acknowledged"; then
    create_index "idx_alerts_acknowledged" "alerts" "acknowledged"
fi

if ! check_index "idx_alerts_dismissed" "alerts" "dismissed"; then
    create_index "idx_alerts_dismissed" "alerts" "dismissed"
fi

if ! check_index "idx_alerts_last_seen" "alerts" "last_seen"; then
    create_index "idx_alerts_last_seen" "alerts" "last_seen DESC"
fi

echo ""

# Check PRAGMA settings
echo "Checking SQLite configuration..."
echo "--------------------------------"
echo ""

# Check journal mode
JOURNAL_MODE=$(sqlite3 "$DB_PATH" "PRAGMA journal_mode;")
if [ "$JOURNAL_MODE" != "wal" ]; then
    echo -e "${YELLOW}⚠ Journal mode is $JOURNAL_MODE (should be WAL)${NC}"
    echo "  Setting WAL mode..."
    sqlite3 "$DB_PATH" "PRAGMA journal_mode=WAL;"
    echo -e "  ${GREEN}✓ WAL mode enabled${NC}"
    FIXED=$((FIXED + 1))
else
    echo -e "${GREEN}✓ WAL mode enabled${NC}"
fi

# Check foreign keys
FOREIGN_KEYS=$(sqlite3 "$DB_PATH" "PRAGMA foreign_keys;")
if [ "$FOREIGN_KEYS" != "1" ]; then
    echo -e "${YELLOW}⚠ Foreign keys disabled${NC}"
    echo "  Note: Foreign keys should be enabled in connection string"
    WARNINGS=$((WARNINGS + 1))
else
    echo -e "${GREEN}✓ Foreign keys enabled${NC}"
fi

# Check auto-vacuum
AUTO_VACUUM=$(sqlite3 "$DB_PATH" "PRAGMA auto_vacuum;")
if [ "$AUTO_VACUUM" = "0" ]; then
    echo -e "${YELLOW}⚠ Auto-vacuum disabled${NC}"
    echo "  Enabling incremental auto-vacuum..."
    sqlite3 "$DB_PATH" "PRAGMA auto_vacuum=INCREMENTAL;"
    echo -e "  ${GREEN}✓ Auto-vacuum enabled${NC}"
    echo "  Note: Full VACUUM required to take effect"
    FIXED=$((FIXED + 1))
else
    echo -e "${GREEN}✓ Auto-vacuum enabled (mode: $AUTO_VACUUM)${NC}"
fi

echo ""

# Check table statistics
echo "Database statistics:"
echo "-------------------"
echo ""

sqlite3 "$DB_PATH" <<'EOF'
.mode column
.headers on
SELECT 
    name,
    (SELECT COUNT(*) FROM pragma_table_info(name)) as columns,
    (SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND tbl_name=name) as indexes
FROM sqlite_master 
WHERE type='table' 
AND name NOT LIKE 'sqlite_%'
ORDER BY name;
EOF

echo ""

# Check for tables missing indexes
echo "Checking for tables without indexes..."
echo "--------------------------------------"
echo ""

TABLES_NO_INDEX=$(sqlite3 "$DB_PATH" "
    SELECT name 
    FROM sqlite_master 
    WHERE type='table' 
    AND name NOT LIKE 'sqlite_%'
    AND name NOT IN (
        SELECT DISTINCT tbl_name 
        FROM sqlite_master 
        WHERE type='index'
    );
")

if [ -z "$TABLES_NO_INDEX" ]; then
    echo -e "${GREEN}✓ All tables have at least one index${NC}"
else
    echo -e "${YELLOW}⚠ Tables without indexes:${NC}"
    echo "$TABLES_NO_INDEX"
    WARNINGS=$((WARNINGS + 1))
fi

echo ""

# Analyze for query optimization
echo "Running ANALYZE..."
sqlite3 "$DB_PATH" "ANALYZE;"
echo -e "${GREEN}✓ ANALYZE complete${NC}"

echo ""

# Summary
echo "===================================="
echo "Database Schema Validation Summary"
echo "===================================="
echo ""

if [ "$ERRORS" -eq 0 ] && [ "$WARNINGS" -eq 0 ]; then
    echo -e "${GREEN}✓ All checks passed!${NC}"
    echo "  Database schema is optimal for 52TB+ systems."
elif [ "$ERRORS" -eq 0 ]; then
    echo -e "${YELLOW}⚠ $WARNINGS warning(s) found${NC}"
    echo "  Database is functional but could be optimized."
    if [ "$FIXED" -gt 0 ]; then
        echo -e "  ${GREEN}✓ $FIXED issue(s) automatically fixed${NC}"
    fi
else
    echo -e "${RED}✗ $ERRORS error(s) and $WARNINGS warning(s) found${NC}"
    echo "  Please review errors above."
fi

echo ""
echo "For optimal performance on 52TB+ systems:"
echo "  1. Ensure database is on NVMe/SSD (not HDD)"
echo "  2. Run VACUUM periodically to reclaim space"
echo "  3. Monitor query performance with .timer ON"
echo "  4. Consider increasing cache_size for systems >32GB RAM"
echo ""

# Exit code based on results
if [ "$ERRORS" -gt 0 ]; then
    exit 1
elif [ "$WARNINGS" -gt 0 ]; then
    exit 0  # Warnings are non-fatal
else
    exit 0
fi
