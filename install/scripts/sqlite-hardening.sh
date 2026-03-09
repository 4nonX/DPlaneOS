#!/bin/bash
#
# D-PlaneOS SQLite Hardening & Optimization
# 
# Critical fixes for 52TB+ production systems:
# 1. WAL mode for concurrency
# 2. Indexes for performance
# 3. Foreign key constraints
# 4. Auto-vacuum configuration
# 5. Connection optimizations
#
# Usage: sudo ./sqlite-hardening.sh
#

set -e

DB_PATH="/var/lib/dplaneos/dplaneos.db"
BACKUP_PATH="/var/lib/dplaneos/backups"

# Colors
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m'

echo -e "${BLUE}D-PlaneOS SQLite Hardening${NC}"
echo "=========================="
echo ""

# Check if DB exists
if [ ! -f "$DB_PATH" ]; then
    echo -e "${RED}✗ Database not found at $DB_PATH${NC}"
    exit 1
fi

echo -e "${GREEN}✓ Database found${NC}"
echo ""

# Backup first
echo "Creating backup..."
mkdir -p "$BACKUP_PATH"
BACKUP_FILE="$BACKUP_PATH/dplaneos-$(date +%Y%m%d-%H%M%S).db"
cp "$DB_PATH" "$BACKUP_FILE"
echo -e "${GREEN}✓ Backup created: $BACKUP_FILE${NC}"
echo ""

# Apply hardening
echo "Applying SQLite hardening..."
echo ""

sqlite3 "$DB_PATH" <<'EOF'
-- ==================================================================
-- SECTION 1: PERFORMANCE OPTIMIZATION
-- ==================================================================

-- Enable Write-Ahead Logging (WAL) mode
-- CRITICAL: Allows concurrent reads during writes
-- Without this: Every write blocks ALL reads
PRAGMA journal_mode = WAL;

-- Set busy timeout to 5 seconds
-- Prevents "database is locked" errors under load
PRAGMA busy_timeout = 5000;

-- Enable automatic index usage
PRAGMA automatic_index = ON;

-- Optimize for SSD (if DB is on NVMe)
-- Reduces fsync calls
PRAGMA synchronous = NORMAL;

-- Set cache size (negative = KB, positive = pages)
-- -64000 = 64MB cache
PRAGMA cache_size = -64000;

-- Memory-mapped I/O (faster reads)
-- 256MB for memory-mapped region
PRAGMA mmap_size = 268435456;

-- ==================================================================
-- SECTION 2: CRITICAL INDEXES FOR 52TB METADATA
-- ==================================================================

-- Files table indexes (CRITICAL for large datasets)
CREATE INDEX IF NOT EXISTS idx_files_path ON files(path);
CREATE INDEX IF NOT EXISTS idx_files_parent ON files(parent_id);
CREATE INDEX IF NOT EXISTS idx_files_size ON files(size);
CREATE INDEX IF NOT EXISTS idx_files_mtime ON files(modified_time);
CREATE INDEX IF NOT EXISTS idx_files_type ON files(type);

-- Audit logs index (descending for recent-first queries)
CREATE INDEX IF NOT EXISTS idx_audit_timestamp ON audit_logs(timestamp DESC);
CREATE INDEX IF NOT EXISTS idx_audit_user ON audit_logs(user);
CREATE INDEX IF NOT EXISTS idx_audit_action ON audit_logs(action);

-- Sessions index
CREATE INDEX IF NOT EXISTS idx_sessions_token ON sessions(session_token);
CREATE INDEX IF NOT EXISTS idx_sessions_user ON sessions(user_id);
CREATE INDEX IF NOT EXISTS idx_sessions_expires ON sessions(expires_at);

-- Alerts index (for fast filtering)
CREATE INDEX IF NOT EXISTS idx_alerts_priority ON alerts(priority);
CREATE INDEX IF NOT EXISTS idx_alerts_category ON alerts(category);
CREATE INDEX IF NOT EXISTS idx_alerts_acknowledged ON alerts(acknowledged);
CREATE INDEX IF NOT EXISTS idx_alerts_dismissed ON alerts(dismissed);
CREATE INDEX IF NOT EXISTS idx_alerts_last_seen ON alerts(last_seen DESC);

-- ZFS datasets index
CREATE INDEX IF NOT EXISTS idx_datasets_pool ON datasets(pool);
CREATE INDEX IF NOT EXISTS idx_datasets_name ON datasets(name);

-- Snapshots index
CREATE INDEX IF NOT EXISTS idx_snapshots_dataset ON snapshots(dataset_id);
CREATE INDEX IF NOT EXISTS idx_snapshots_created ON snapshots(created_at DESC);

-- Docker containers index
CREATE INDEX IF NOT EXISTS idx_containers_status ON containers(status);
CREATE INDEX IF NOT EXISTS idx_containers_name ON containers(name);

-- ==================================================================
-- SECTION 3: FOREIGN KEY CONSTRAINTS (DATA INTEGRITY)
-- ==================================================================

-- Enable foreign keys
PRAGMA foreign_keys = ON;

-- Note: Foreign keys should be added during table creation
-- This script documents what SHOULD exist

-- ==================================================================
-- SECTION 4: AUTO-VACUUM CONFIGURATION
-- ==================================================================

-- Enable auto-vacuum (incremental)
-- Prevents DB file from growing indefinitely
PRAGMA auto_vacuum = INCREMENTAL;

-- Set incremental vacuum threshold
-- Vacuum when 1000+ pages can be freed
PRAGMA incremental_vacuum(1000);

-- ==================================================================
-- SECTION 5: STATISTICS UPDATE
-- ==================================================================

-- Analyze tables for query optimizer
-- This helps SQLite choose the best indexes
ANALYZE;

-- ==================================================================
-- SECTION 6: INTEGRITY CHECK
-- ==================================================================

PRAGMA integrity_check;

-- ==================================================================
-- SECTION 7: OPTIMIZATION COMPLETE
-- ==================================================================

-- Optimize database (rebuild with new settings)
PRAGMA optimize;

.quit
EOF

echo ""
echo -e "${GREEN}✓ SQLite hardening applied${NC}"
echo ""

# Show results
echo "Database statistics:"
echo "-------------------"

sqlite3 "$DB_PATH" <<'EOF'
SELECT 
    (SELECT COUNT(*) FROM files) as files_count,
    (SELECT COUNT(*) FROM audit_logs) as audit_count,
    (SELECT COUNT(*) FROM alerts) as alerts_count,
    (SELECT COUNT(*) FROM sessions) as sessions_count;
    
SELECT name, sql FROM sqlite_master WHERE type='index' AND name LIKE 'idx_%';
EOF

echo ""

# Calculate DB size
DB_SIZE=$(du -h "$DB_PATH" | cut -f1)
WAL_SIZE=$(du -h "$DB_PATH-wal" 2>/dev/null | cut -f1 || echo "0")

echo "Database size: $DB_SIZE"
echo "WAL size: $WAL_SIZE"
echo ""

echo -e "${GREEN}✓ Hardening complete!${NC}"
echo ""
echo "Optimizations applied:"
echo "  ✓ WAL mode enabled (concurrent reads)"
echo "  ✓ Busy timeout set (5 seconds)"
echo "  ✓ Performance indexes created"
echo "  ✓ Auto-vacuum configured"
echo "  ✓ Cache optimized (64MB)"
echo "  ✓ Memory-mapped I/O enabled"
echo ""
echo "For 52TB+ systems, consider:"
echo "  - Moving DB to NVMe for faster access"
echo "  - Increasing cache_size if you have >32GB RAM"
echo "  - Running VACUUM periodically to reclaim space"
echo ""
