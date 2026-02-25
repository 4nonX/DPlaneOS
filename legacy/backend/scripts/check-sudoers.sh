#!/bin/bash
#
# D-PlaneOS Sudoers Update Checker
# Version: 1.13.0-FINAL
#
# Ensures sudoers rules are up-to-date with current version
#

SUDOERS_FILE="/etc/sudoers.d/dplaneos"
SUDOERS_TEMPLATE="/var/www/dplaneos/config/sudoers.template"
SUDOERS_VERSION_FILE="/var/www/dplaneos/.sudoers-version"

GREEN='\033[0;32m'
YELLOW='\033[1;33m'
RED='\033[0;31m'
NC='\033[0m'

# Expected sudoers version for current D-PlaneOS release
EXPECTED_VERSION="1.14.0"

check_sudoers_version() {
    if [ -f "$SUDOERS_VERSION_FILE" ]; then
        CURRENT_VERSION=$(cat "$SUDOERS_VERSION_FILE")
    else
        CURRENT_VERSION="unknown"
    fi
    
    if [ "$CURRENT_VERSION" != "$EXPECTED_VERSION" ]; then
        echo -e "${YELLOW}⚠ Sudoers rules outdated: $CURRENT_VERSION (expected: $EXPECTED_VERSION)${NC}"
        return 1
    else
        echo -e "${GREEN}✓ Sudoers rules up-to-date: $CURRENT_VERSION${NC}"
        return 0
    fi
}

update_sudoers() {
    echo "Updating sudoers rules..."
    
    # Create backup
    if [ -f "$SUDOERS_FILE" ]; then
        cp "$SUDOERS_FILE" "${SUDOERS_FILE}.backup.$(date +%Y%m%d-%H%M%S)"
    fi
    
    # Install new sudoers file
    cat > "$SUDOERS_FILE" << 'SUDOERS'
# D-PlaneOS Sudoers Rules
# Version: 1.14.0
# Auto-generated - Do not edit manually

# Allow www-data to run ZFS commands
www-data ALL=(root) NOPASSWD: /usr/sbin/zpool
www-data ALL=(root) NOPASSWD: /usr/sbin/zfs

# Allow www-data to run Docker commands
www-data ALL=(root) NOPASSWD: /usr/bin/docker
www-data ALL=(root) NOPASSWD: /usr/bin/docker-compose
www-data ALL=(root) NOPASSWD: /usr/local/bin/docker-compose

# Allow www-data to manage system services
www-data ALL=(root) NOPASSWD: /bin/systemctl restart smbd
www-data ALL=(root) NOPASSWD: /bin/systemctl restart nmbd
www-data ALL=(root) NOPASSWD: /bin/systemctl reload smbd
www-data ALL=(root) NOPASSWD: /bin/systemctl reload nmbd
www-data ALL=(root) NOPASSWD: /bin/systemctl restart apache2
www-data ALL=(root) NOPASSWD: /bin/systemctl reload apache2

# Allow www-data to manage NFS exports
www-data ALL=(root) NOPASSWD: /usr/sbin/exportfs

# Allow www-data to check disk status
www-data ALL=(root) NOPASSWD: /sbin/blockdev
www-data ALL=(root) NOPASSWD: /usr/sbin/smartctl

# Allow www-data to run backup scripts
www-data ALL=(root) NOPASSWD: /var/www/dplaneos/scripts/auto-backup.php

# Allow www-data to manage network configuration (for future features)
www-data ALL=(root) NOPASSWD: /bin/ip addr
www-data ALL=(root) NOPASSWD: /bin/ip link

# Allow www-data to validate SSH keys (App Store private repos)
www-data ALL=(root) NOPASSWD: /usr/bin/ssh-keygen

# Allow www-data to manage Tailscale
www-data ALL=(root) NOPASSWD: /usr/bin/tailscale
www-data ALL=(root) NOPASSWD: /bin/systemctl start tailscaled
www-data ALL=(root) NOPASSWD: /bin/systemctl stop tailscaled
www-data ALL=(root) NOPASSWD: /bin/systemctl enable tailscaled
www-data ALL=(root) NOPASSWD: /bin/systemctl is-active tailscaled
www-data ALL=(root) NOPASSWD: /var/www/dplaneos/scripts/install-tailscale.sh

# Disable requiretty for www-data
Defaults:www-data !requiretty
SUDOERS
    
    # Set correct permissions
    chmod 440 "$SUDOERS_FILE"
    
    # Validate sudoers file
    if visudo -c -f "$SUDOERS_FILE" >/dev/null 2>&1; then
        echo -e "${GREEN}✓ Sudoers file validated successfully${NC}"
        
        # Update version file
        echo "$EXPECTED_VERSION" > "$SUDOERS_VERSION_FILE"
        
        echo -e "${GREEN}✓ Sudoers rules updated to version $EXPECTED_VERSION${NC}"
        return 0
    else
        echo -e "${RED}✗ Sudoers file validation failed! Restoring backup...${NC}"
        
        # Restore backup if validation failed
        if [ -f "${SUDOERS_FILE}.backup.$(date +%Y%m%d-%H%M%S)" ]; then
            mv "${SUDOERS_FILE}.backup.$(date +%Y%m%d-%H%M%S)" "$SUDOERS_FILE"
        fi
        
        return 1
    fi
}

# Main execution
if ! check_sudoers_version; then
    if [ "$1" == "--auto-update" ]; then
        update_sudoers
    else
        echo ""
        echo "Run with --auto-update to automatically update sudoers rules"
        echo "Or manually run: sudo $0 --auto-update"
    fi
fi
