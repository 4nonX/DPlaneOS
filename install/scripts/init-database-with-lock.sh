#!/bin/bash
#
# D-PlaneOS - Database Initialization with Lock
#
# FIX: Prevents race condition between installer and daemon startup
# - Creates lock file during initialization
# - Daemon waits for lock to be released
# - Ensures DB is fully initialized before first access
#

set -e

DB_PATH="/var/lib/dplaneos/dplaneos.db"
LOCK_FILE="/var/run/dplaneos/db.lock"
LOCK_DIR=$(dirname "$LOCK_FILE")

# Ensure lock directory exists
mkdir -p "$LOCK_DIR"

# Function to acquire lock with timeout
acquire_lock() {
    local timeout=30
    local waited=0
    
    while [ -f "$LOCK_FILE" ]; do
        if [ $waited -ge $timeout ]; then
            echo "ERROR: Timeout waiting for database lock" >&2
            return 1
        fi
        
        echo "Waiting for database initialization lock..."
        sleep 1
        waited=$((waited + 1))
    done
    
    # Create lock file with our PID
    echo $$ > "$LOCK_FILE"
    return 0
}

# Function to release lock
release_lock() {
    rm -f "$LOCK_FILE"
}

# Trap to ensure lock is released on exit
trap release_lock EXIT INT TERM

# Initialize database
init_database() {
    # Acquire lock
    if ! acquire_lock; then
        exit 1
    fi
    
    echo "Initializing D-PlaneOS database..."
    
    # Create database directory
    mkdir -p "$(dirname "$DB_PATH")"
    
    # Create an empty database file so the daemon can open it on first start.
    # Schema is created by the daemon's initSchema() on startup — do NOT
    # duplicate it here. Keeping schema in one place (schema.go) prevents drift.
    if [ ! -f "$DB_PATH" ]; then
        sqlite3 "$DB_PATH" "PRAGMA journal_mode=WAL;"
    fi
    
    # Set permissions — only chown if www-data exists (not present in CI/minimal installs)
    chmod 660 "$DB_PATH"
    if id www-data &>/dev/null; then
        chown www-data:www-data "$DB_PATH"
    fi
    
    echo "Database initialized successfully"
    
    # Release lock (also done by trap)
    release_lock
}

# Check if database needs initialization
if [ ! -f "$DB_PATH" ] || [ ! -s "$DB_PATH" ]; then
    init_database
else
    echo "Database already initialized"
fi

exit 0
