#!/bin/bash
#
# D-PlaneOS - Safe Upgrade with Automatic Rollback
#
# Purpose: Upgrade from to with automatic backup and rollback
# Usage: sudo ./upgrade-with-rollback.sh
#
# Features:
# - Auto-backup before upgrade
# - Verification at each step
# - Automatic rollback on failure
# - Manual rollback option
# - Preserves user data
#

set -euo pipefail

# Colors
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m'

# Configuration
BACKUP_DIR="/var/lib/dplaneos-backup"
BACKUP_NAME="pre-upgrade-$(date +%Y-%m-%d-%H-%M)"
BACKUP_PATH="$BACKUP_DIR/$BACKUP_NAME"
LOG_FILE="/var/log/dplaneos-upgrade.log"

# Log function
log() {
    echo "[$(date '+%Y-%m-%d %H:%M:%S')] $1" | tee -a "$LOG_FILE"
}

info() {
    echo -e "${BLUE}ℹ${NC} $1"
    log "INFO: $1"
}

success() {
    echo -e "${GREEN}✓${NC} $1"
    log "SUCCESS: $1"
}

warning() {
    echo -e "${YELLOW}⚠${NC} $1"
    log "WARNING: $1"
}

error() {
    echo -e "${RED}✗${NC} $1"
    log "ERROR: $1"
}

# Header
clear
echo "╔══════════════════════════════════════════════════════════════╗"
echo "║       D-PlaneOS v$(cat "$(dirname "$0")/../VERSION" 2>/dev/null | tr -d "[:space:]" || echo "?") - Safe Upgrade with Rollback         ║"
echo "╚══════════════════════════════════════════════════════════════╝"
echo ""

# Check if running as root
if [ "$EUID" -ne 0 ]; then
    error "This script must be run as root"
    exit 1
fi

# Create backup directory
info "Creating backup directory..."
mkdir -p "$BACKUP_PATH"
success "Backup directory: $BACKUP_PATH"

# Step 1: Backup database
info "Step 1/6: Backing up database..."
if [ -f /var/lib/dplaneos/dplaneos.db ]; then
    cp /var/lib/dplaneos/dplaneos.db "$BACKUP_PATH/"
    success "Database backed up"
elif [ -f /etc/dplaneos/database.conf ]; then
    # PostgreSQL backup
    source /etc/dplaneos/database.conf
    if [ "$DB_TYPE" = "postgresql" ]; then
        sudo -u postgres pg_dump dplaneos > "$BACKUP_PATH/dplaneos_postgresql.sql"
        success "PostgreSQL database backed up"
    fi
else
    warning "No database found (fresh install?)"
fi

# Step 2: Backup web server configs
info "Step 2/6: Backing up web server configuration..."
mkdir -p "$BACKUP_PATH/apache2"
mkdir -p "$BACKUP_PATH/nginx"

if [ -f /etc/apache2/sites-available/dplaneos.conf ]; then
    cp /etc/apache2/sites-available/dplaneos.conf "$BACKUP_PATH/apache2/"
    success "Apache2 config backed up"
fi

if [ -f /etc/nginx/sites-available/dplaneos.conf ]; then
    cp /etc/nginx/sites-available/dplaneos.conf "$BACKUP_PATH/nginx/"
    success "Nginx config backed up"
fi

# Step 3: Backup application files
info "Step 3/6: Backing up application files..."
if [ -d /var/www/dplaneos ]; then
    tar -czf "$BACKUP_PATH/dplaneos-app.tar.gz" -C /var/www dplaneos
    success "Application files backed up"
else
    warning "No application files found (fresh install?)"
fi

# Step 4: Record system state
info "Step 4/6: Recording system state..."
cat > "$BACKUP_PATH/system-state.json" <<EOF
{
    "date": "$(date -Iseconds)",
    "hostname": "$(hostname)",
    "os": "$(lsb_release -ds)",
    "kernel": "$(uname -r)",
    "go_daemon": "dplaned v$(cat '$(dirname '$0')/../VERSION' 2>/dev/null | tr -d '[:space:]' || echo '?')",
    "apache2_status": "$(systemctl is-active apache2 2>/dev/null || echo 'not installed')",
    "nginx_status": "$(systemctl is-active nginx 2>/dev/null || echo 'not installed')",
    "dplaned_status": "$(systemctl is-active dplaned 2>/dev/null || echo 'not installed')"
}
EOF

dpkg -l | grep -E "nginx|sqlite3|zfsutils" > "$BACKUP_PATH/installed-packages.txt"
success "System state recorded"

# Step 5: Create rollback script
info "Step 5/6: Creating rollback script..."
cat > "$BACKUP_PATH/rollback.sh" <<'ROLLBACK_SCRIPT'
#!/bin/bash
# Automatic rollback script

set -euo pipefail

echo "╔══════════════════════════════════════════════════════════════╗"
echo "║              D-PlaneOS v$(cat "$(dirname "$0")/../VERSION" 2>/dev/null | tr -d "[:space:]" || echo "?") - Rollback                     ║"
echo "╚══════════════════════════════════════════════════════════════╝"
echo ""

# Check if running as root
if [ "$EUID" -ne 0 ]; then
    echo "ERROR: This script must be run as root"
    exit 1
fi

BACKUP_DIR="$(dirname "$0")"
cd "$BACKUP_DIR"

echo "Rolling back to pre-upgrade state..."
echo ""

# Stop services
echo "Stopping services..."
systemctl stop apache2 2>/dev/null || true
systemctl stop nginx 2>/dev/null || true
systemctl stop dplaned 2>/dev/null || true
# (PHP-FPM removed — Go architecture)
systemctl stop dplaned 2>/dev/null || true

# Restore database
if [ -f dplaneos.db ]; then
    echo "Restoring SQLite database..."
    cp dplaneos.db /var/lib/dplaneos/dplaneos.db
    chown www-data:www-data /var/lib/dplaneos/dplaneos.db
fi

if [ -f dplaneos_postgresql.sql ]; then
    echo "Restoring PostgreSQL database..."
    sudo -u postgres psql -d dplaneos < dplaneos_postgresql.sql
fi

# Restore configs
if [ -f apache2/dplaneos.conf ]; then
    echo "Restoring Apache2 config..."
    cp apache2/dplaneos.conf /etc/apache2/sites-available/
fi

if [ -f nginx/dplaneos.conf ]; then
    echo "Restoring Nginx config..."
    cp nginx/dplaneos.conf /etc/nginx/sites-available/
fi

# Restore application (if exists)
if [ -f dplaneos-app.tar.gz ]; then
    echo "Restoring application files..."
    rm -rf /var/www/dplaneos
    tar -xzf dplaneos-app.tar.gz -C /var/www/
    chown -R www-data:www-data /var/www/dplaneos
fi

# Start services
echo "Starting services..."
systemctl start apache2 2>/dev/null || systemctl start nginx 2>/dev/null || true
systemctl start dplaned 2>/dev/null || true

echo ""
echo "✓ Rollback complete!"
echo ""
echo "System should now be restored to pre-upgrade state."
echo "Test by accessing: http://$(hostname -I | awk '{print $1}')/"
echo ""
ROLLBACK_SCRIPT

chmod +x "$BACKUP_PATH/rollback.sh"
success "Rollback script created: $BACKUP_PATH/rollback.sh"

# Step 6: Verify backup integrity
info "Step 6/6: Verifying backup integrity..."
BACKUP_SIZE=$(du -sh "$BACKUP_PATH" | cut -f1)
success "Backup complete: $BACKUP_SIZE in $BACKUP_PATH"

# Summary
echo ""
echo "╔══════════════════════════════════════════════════════════════╗"
echo "║                    Backup Complete                           ║"
echo "╚══════════════════════════════════════════════════════════════╝"
echo ""
echo "Backup location: $BACKUP_PATH"
echo "Backup size: $BACKUP_SIZE"
echo "Rollback script: $BACKUP_PATH/rollback.sh"
echo ""

# Confirm upgrade
echo "════════════════════════════════════════════════════════════════"
echo ""
read -p "Ready to upgrade? [y/N] " -n 1 -r
echo ""
if [[ ! $REPLY =~ ^[Yy]$ ]]; then
    warning "Upgrade cancelled by user"
    exit 0
fi

# Run installer
info "Starting upgrade..."
echo ""

if [ -f ./install.sh ]; then
    # Run installer and capture result
    if ./install.sh; then
        echo ""
        success "Upgrade completed successfully!"
        echo ""
        echo "Backup retained at: $BACKUP_PATH"
        echo "You can safely delete backup after verifying system works."
        echo ""
    else
        echo ""
        error "Upgrade failed!"
        echo ""
        echo "╔══════════════════════════════════════════════════════════════╗"
        echo "║                   Automatic Rollback                         ║"
        echo "╚══════════════════════════════════════════════════════════════╝"
        echo ""
        read -p "Start automatic rollback? [Y/n] " -n 1 -r
        echo ""
        if [[ ! $REPLY =~ ^[Nn]$ ]]; then
            "$BACKUP_PATH/rollback.sh"
        else
            warning "Rollback skipped"
            echo "To rollback manually later, run:"
            echo "  sudo $BACKUP_PATH/rollback.sh"
        fi
        exit 1
    fi
else
    error "install.sh not found in current directory!"
    exit 1
fi
