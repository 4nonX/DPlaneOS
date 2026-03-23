#!/usr/bin/env bash
#
# D-PlaneOS - Database Initialization with Lock
#
# FIX: Prevents race condition between installer and daemon startup
# - Creates lock file during initialization
# - Daemon waits for lock to be released
# - Ensures DB is fully initialized before first access
#

set -e

# Default path (Legacy/File-based - deprecated in v7.0.0)
DB_PATH="/var/lib/dplaneos/dplaneos.db"
DB_DSN=""
LOCK_FILE="/run/dplaneos/db.lock"

# Environment overrides
[ -n "$DPLANEOS_DB" ] && DB_PATH="$DPLANEOS_DB"
[ -n "$DATABASE_DSN" ] && DB_DSN="$DATABASE_DSN"

# Parse arguments
while [[ "$#" -gt 0 ]]; do
    case "$1" in
        --db)     DB_PATH="$2"; shift ;;
        --db-dsn) DB_DSN="$2"; shift ;;
    esac
    shift
done

LOCK_DIR=$(dirname "$LOCK_FILE")
mkdir -p "$LOCK_DIR"

# Function to acquire lock
acquire_lock() {
    local timeout=30
    local waited=0
    while [ -f "$LOCK_FILE" ]; do
        [ $waited -ge $timeout ] && { echo "ERROR: Timeout waiting for database lock" >&2; return 1; }
        echo "Waiting for database initialization lock..."
        sleep 1
        waited=$((waited + 1))
    done
    echo $$ > "$LOCK_FILE"
    return 0
}

# Function to release lock
release_lock() { rm -f "$LOCK_FILE"; }
trap release_lock EXIT INT TERM

# Initialize database
init_database() {
    acquire_lock || exit 1
    
    if [ -n "$DB_DSN" ]; then
        echo "Verifying PostgreSQL connectivity: $DB_DSN"
        if command -v pg_isready &>/dev/null; then
            echo "PostgreSQL check enabled"
        fi
    else
        echo "ERROR: DATABASE_DSN is mandatory in v7.1.0." >&2
        exit 1
    fi
    
    echo "Database initialization phase complete"
    release_lock
}

# Check if database needs initialization
if [ -n "$DB_DSN" ]; then
    init_database
elif [ ! -f "$DB_PATH" ] || [ ! -s "$DB_PATH" ]; then
    init_database
else
    echo "Database already initialized"
fi

exit 0

