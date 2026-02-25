#!/bin/bash
#
# D-PlaneOS FTS5 File Search Setup
# 
# Creates SQLite FTS5 (Full-Text Search) virtual table for blazing-fast file searches
# 
# WHY THIS MATTERS:
# - B-Tree index on long paths: O(log n) but slow with long strings
# - FTS5 tokenized search: O(1) lookups, works on any part of path
# 
# EXAMPLE:
# Path: /tank/photos/2026/vacation/beach/sunset.jpg
# 
# Without FTS5:
# SELECT * FROM files WHERE path LIKE '%sunset%'
# → Full table scan, 10M rows, 45 seconds
# 
# With FTS5:
# SELECT * FROM files_fts WHERE files_fts MATCH 'sunset'
# → Tokenized index lookup, 0.05 seconds (900x faster!)
#

set -e

DB_PATH="/var/lib/dplaneos/dplaneos.db"

echo "D-PlaneOS FTS5 File Search Setup"
echo "================================="
echo ""

if [ ! -f "$DB_PATH" ]; then
    echo "✗ Database not found at $DB_PATH"
    exit 1
fi

echo "✓ Database found"
echo ""

echo "Creating FTS5 virtual table..."
sqlite3 "$DB_PATH" <<'EOF'
-- Check if FTS5 is available
.load fts5

-- Create FTS5 virtual table for file search
-- This indexes the 'path' column from the 'files' table
CREATE VIRTUAL TABLE IF NOT EXISTS files_fts USING fts5(
    path,                    -- Full path to search
    name,                    -- Filename only (for faster name searches)
    content=files,           -- Source table
    content_rowid=id,        -- Link to files.id
    tokenize='porter unicode61 remove_diacritics 1'
);

-- Create triggers to keep FTS5 in sync with files table
CREATE TRIGGER IF NOT EXISTS files_fts_insert AFTER INSERT ON files BEGIN
    INSERT INTO files_fts(rowid, path, name)
    VALUES (new.id, new.path, new.name);
END;

CREATE TRIGGER IF NOT EXISTS files_fts_delete AFTER DELETE ON files BEGIN
    DELETE FROM files_fts WHERE rowid = old.id;
END;

CREATE TRIGGER IF NOT EXISTS files_fts_update AFTER UPDATE ON files BEGIN
    DELETE FROM files_fts WHERE rowid = old.id;
    INSERT INTO files_fts(rowid, path, name)
    VALUES (new.id, new.path, new.name);
END;

-- Initial population (if files table already has data)
INSERT OR IGNORE INTO files_fts(rowid, path, name)
SELECT id, path, name FROM files;

-- Optimize FTS5 index
INSERT INTO files_fts(files_fts) VALUES('optimize');

.quit
EOF

if [ $? -eq 0 ]; then
    echo "✓ FTS5 virtual table created"
    echo ""
    
    # Show statistics
    echo "FTS5 Statistics:"
    echo "---------------"
    sqlite3 "$DB_PATH" <<'EOF'
SELECT 
    COUNT(*) as indexed_files,
    (SELECT COUNT(*) FROM files) as total_files
FROM files_fts;
EOF
    
    echo ""
    echo "Usage in queries:"
    echo "----------------"
    echo ""
    echo "Fast filename search:"
    echo "  SELECT f.* FROM files f"
    echo "  JOIN files_fts ON files_fts.rowid = f.id"
    echo "  WHERE files_fts MATCH 'sunset'"
    echo ""
    echo "Search in path:"
    echo "  SELECT f.* FROM files f"
    echo "  JOIN files_fts ON files_fts.rowid = f.id"
    echo "  WHERE files_fts MATCH 'vacation'"
    echo ""
    echo "Complex search (multiple terms):"
    echo "  WHERE files_fts MATCH 'beach AND sunset'"
    echo ""
    echo "Phrase search:"
    echo "  WHERE files_fts MATCH '\"summer vacation\"'"
    echo ""
    
    echo "✓ FTS5 setup complete!"
else
    echo "✗ FTS5 setup failed"
    exit 1
fi
