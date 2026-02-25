#!/bin/bash
#
# D-PlaneOS Update Handler
# Version: 1.13.1
#
# Handles GitHub-based updates with automatic sudoers sync
#

set -e

BLUE='\033[0;34m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
RED='\033[0;31m'
NC='\033[0m'

UPDATE_DIR="/tmp/dplaneos_update"
INSTALL_DIR="/var/www/dplaneos"

echo -e "${BLUE}D-PlaneOS Update Handler v1.13.1${NC}"
echo ""

# Function to log steps
log_step() {
    echo -e "${BLUE}[UPDATE]${NC} $1"
}

log_success() {
    echo -e "${GREEN}✓${NC} $1"
}

log_warning() {
    echo -e "${YELLOW}⚠${NC} $1"
}

log_error() {
    echo -e "${RED}✗${NC} $1"
}

# Check if running as root
if [[ $EUID -ne 0 ]]; then
   log_error "This script must be run as root"
   exit 1
fi

# Download latest release
log_step "Downloading latest release from GitHub..."
# TODO: Add actual GitHub release URL
# wget -O /tmp/dplaneos-latest.tar.gz https://github.com/your-org/dplaneos/releases/latest/download/dplaneos-latest.tar.gz

# For now, assume update is in UPDATE_DIR
if [ ! -d "$UPDATE_DIR" ]; then
    log_error "Update directory not found: $UPDATE_DIR"
    exit 1
fi

# Create backup of current installation
log_step "Creating backup of current installation..."
BACKUP_FILE="/var/backups/dplaneos-pre-update-$(date +%Y%m%d-%H%M%S).tar.gz"
tar czf "$BACKUP_FILE" -C "$INSTALL_DIR" . 2>/dev/null || true
log_success "Backup created: $BACKUP_FILE"

# Update files
log_step "Updating files..."
cp -r "$UPDATE_DIR"/* "$INSTALL_DIR/"
chown -R www-data:www-data "$INSTALL_DIR"
log_success "Files updated"

# === SUDOERS SYNC (CRITICAL!) ===
log_step "Checking sudoers configuration..."

SUDOERS_NEW="$UPDATE_DIR/config/sudoers.conf"
SUDOERS_CURRENT="/etc/sudoers.d/dplaneos"

if [ -f "$SUDOERS_NEW" ]; then
    if ! diff -q "$SUDOERS_NEW" "$SUDOERS_CURRENT" >/dev/null 2>&1; then
        log_warning "Sudoers file has changed, updating..."
        
        # Validate new sudoers file before applying
        if visudo -c -f "$SUDOERS_NEW" >/dev/null 2>&1; then
            cp "$SUDOERS_NEW" "$SUDOERS_CURRENT"
            chmod 440 "$SUDOERS_CURRENT"
            log_success "Sudoers updated and validated"
            echo "STP:5:Sudo-Permissions updated."
        else
            log_error "New sudoers file is invalid! Keeping old version."
            exit 1
        fi
    else
        log_success "Sudoers up-to-date"
    fi
else
    # Fallback: Use check-sudoers.sh if available
    if [ -f "$INSTALL_DIR/scripts/check-sudoers.sh" ]; then
        log_step "Running sudoers update checker..."
        bash "$INSTALL_DIR/scripts/check-sudoers.sh" --auto-update
    fi
fi
# === END SUDOERS SYNC ===

# Run database migrations if needed
log_step "Checking for database migrations..."
if [ -d "$UPDATE_DIR/sql" ]; then
    for migration in "$UPDATE_DIR"/sql/*.sql; do
        if [ -f "$migration" ]; then
            log_step "Running migration: $(basename $migration)"
            sqlite3 "$INSTALL_DIR/database.sqlite" < "$migration" 2>&1 || log_warning "Migration may have already been applied"
        fi
    done
    log_success "Database migrations complete"
fi

# Update config files
log_step "Updating configuration files..."

# Logrotate
if [ -f "$UPDATE_DIR/config/logrotate-dplaneos" ]; then
    cp "$UPDATE_DIR/config/logrotate-dplaneos" /etc/logrotate.d/dplaneos
    chmod 644 /etc/logrotate.d/dplaneos
    log_success "Logrotate configuration updated"
fi

# Docker daemon config
if [ -f "$UPDATE_DIR/config/docker-daemon.json" ]; then
    if [ -f /etc/docker/daemon.json ]; then
        cp /etc/docker/daemon.json /etc/docker/daemon.json.backup.$(date +%Y%m%d)
    fi
    cp "$UPDATE_DIR/config/docker-daemon.json" /etc/docker/daemon.json
    systemctl restart docker
    log_success "Docker log rotation updated"
fi

# Restart services
log_step "Restarting services..."
systemctl reload apache2
log_success "Apache reloaded"

# Run integrity check
if [ -f "$INSTALL_DIR/scripts/integrity-check.sh" ]; then
    log_step "Running integrity check..."
    bash "$INSTALL_DIR/scripts/integrity-check.sh" || log_warning "Some integrity checks failed"
fi

# Cleanup
log_step "Cleaning up..."
rm -rf "$UPDATE_DIR"
log_success "Update temporary files removed"

# Display new version
if [ -f "$INSTALL_DIR/VERSION" ]; then
    NEW_VERSION=$(cat "$INSTALL_DIR/VERSION")
    echo ""
    echo -e "${GREEN}╔══════════════════════════════════════════╗${NC}"
    echo -e "${GREEN}║   Update Complete! ✓                    ║${NC}"
    echo -e "${GREEN}║   Version: $NEW_VERSION                        ║${NC}"
    echo -e "${GREEN}╚══════════════════════════════════════════╝${NC}"
    echo ""
fi

log_success "D-PlaneOS has been updated successfully!"
echo ""
echo "Backup saved to: $BACKUP_FILE"
echo "Access your system at: http://$(hostname -I | awk '{print $1}')"
echo ""
