#!/bin/bash
#
# D-PlaneOS Upgrade Script: v1.12.0 → v1.13.0
# Features: Config Backup & Disk Replacement Wizard
#

set -e

BLUE='\033[0;34m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
RED='\033[0;31m'
NC='\033[0m'

echo -e "${BLUE}╔══════════════════════════════════════════════════════╗${NC}"
echo -e "${BLUE}║     D-PlaneOS Upgrade: v1.12.0 → v1.13.0           ║${NC}"
echo -e "${BLUE}║     Config Backup & Disk Replacement Features      ║${NC}"
echo -e "${BLUE}╚══════════════════════════════════════════════════════╝${NC}"
echo ""

# Check if running as root
if [[ $EUID -ne 0 ]]; then
   echo -e "${RED}This script must be run as root${NC}"
   exit 1
fi

# Check current version
if [ ! -f /var/www/dplaneos/VERSION ]; then
    echo -e "${RED}ERROR: D-PlaneOS installation not found${NC}"
    exit 1
fi

CURRENT_VERSION=$(cat /var/www/dplaneos/VERSION)
echo -e "${YELLOW}Current version: $CURRENT_VERSION${NC}"

if [ "$CURRENT_VERSION" != "1.12.0" ]; then
    echo -e "${YELLOW}WARNING: This upgrade is designed for v1.12.0${NC}"
    read -p "Continue anyway? (y/N) " -n 1 -r
    echo
    if [[ ! $REPLY =~ ^[Yy]$ ]]; then
        exit 1
    fi
fi

echo ""
echo -e "${BLUE}Step 1/8: Creating pre-upgrade backup...${NC}"
if [ -f /var/www/dplaneos/database.sqlite ]; then
    BACKUP_FILE="/var/backups/dplaneos-pre-v1.13.0-$(date +%Y%m%d-%H%M%S).sqlite"
    cp /var/www/dplaneos/database.sqlite "$BACKUP_FILE"
    echo -e "${GREEN}✓ Database backed up to: $BACKUP_FILE${NC}"
fi

echo ""
echo -e "${BLUE}Step 2/8: Installing new API files...${NC}"
cp -v api/backup.php /var/www/dplaneos/api/
cp -v api/disk-replacement.php /var/www/dplaneos/api/
chown www-data:www-data /var/www/dplaneos/api/*.php
chmod 644 /var/www/dplaneos/api/*.php
echo -e "${GREEN}✓ API files installed${NC}"

echo ""
echo -e "${BLUE}Step 3/8: Installing JavaScript files...${NC}"
mkdir -p /var/www/dplaneos/js
cp -v js/backup.js /var/www/dplaneos/js/
cp -v js/disk-replacement.js /var/www/dplaneos/js/
chown www-data:www-data /var/www/dplaneos/js/*.js
chmod 644 /var/www/dplaneos/js/*.js
echo -e "${GREEN}✓ JavaScript files installed${NC}"

echo ""
echo -e "${BLUE}Step 4/8: Running database migration...${NC}"
if command -v sqlite3 >/dev/null 2>&1; then
    sqlite3 /var/www/dplaneos/database.sqlite < sql/003_backup_and_disk_replacement.sql
    echo -e "${GREEN}✓ Database migrated${NC}"
else
    echo -e "${YELLOW}⚠ sqlite3 not found, migration must be done manually${NC}"
fi

echo ""
echo -e "${BLUE}Step 5/8: Creating backup directory...${NC}"
mkdir -p /var/backups/dplaneos
chown www-data:www-data /var/backups/dplaneos
chmod 700 /var/backups/dplaneos
echo -e "${GREEN}✓ Backup directory created${NC}"

echo ""
echo -e "${BLUE}Step 6/8: Installing automated backup script...${NC}"
cp -v scripts/auto-backup.php /var/www/dplaneos/scripts/
chmod +x /var/www/dplaneos/scripts/auto-backup.php
chown www-data:www-data /var/www/dplaneos/scripts/auto-backup.php
touch /var/log/dplaneos-backup.log
chown www-data:www-data /var/log/dplaneos-backup.log
echo -e "${GREEN}✓ Auto-backup script installed${NC}"

echo ""
echo -e "${BLUE}Step 7/10: Checking dependencies...${NC}"

# Check for OpenSSL (required for encryption)
if ! command -v openssl >/dev/null 2>&1; then
    echo -e "${YELLOW}⚠ Installing OpenSSL...${NC}"
    apt-get update -qq
    apt-get install -y openssl
fi
echo -e "${GREEN}✓ OpenSSL available${NC}"

# Check for smartmontools (for disk SMART data)
if ! command -v smartctl >/dev/null 2>&1; then
    echo -e "${YELLOW}Installing smartmontools for disk health monitoring...${NC}"
    apt-get install -y smartmontools
fi
echo -e "${GREEN}✓ smartmontools available${NC}"

echo ""
echo -e "${BLUE}Step 8/10: Installing log rotation...${NC}"
cp -v config/logrotate-dplaneos /etc/logrotate.d/dplaneos
chmod 644 /etc/logrotate.d/dplaneos
echo -e "${GREEN}✓ Logrotate configured${NC}"

# Docker log rotation
if [ -f /etc/docker/daemon.json ]; then
    cp /etc/docker/daemon.json /etc/docker/daemon.json.backup.$(date +%Y%m%d)
fi
cp config/docker-daemon.json /etc/docker/daemon.json
systemctl restart docker
echo -e "${GREEN}✓ Docker log rotation configured${NC}"

echo ""
echo -e "${BLUE}Step 9/10: Updating sudoers rules...${NC}"
chmod +x scripts/check-sudoers.sh
./scripts/check-sudoers.sh --auto-update
echo -e "${GREEN}✓ Sudoers rules updated${NC}"

echo ""
echo -e "${BLUE}Step 10/10: Updating version...${NC}"
echo "1.13.0" > /var/www/dplaneos/VERSION
echo -e "${GREEN}✓ Version updated to 1.13.0${NC}"

echo ""
echo -e "${GREEN}╔══════════════════════════════════════════════════════╗${NC}"
echo -e "${GREEN}║          Upgrade to v1.13.0 Complete! ✓             ║${NC}"
echo -e "${GREEN}╚══════════════════════════════════════════════════════╝${NC}"
echo ""
echo -e "${BLUE}New Features:${NC}"
echo "  • Config Backup & Restore (Settings → Backup)"
echo "  • Disk Replacement Wizard (Storage → Disk Health)"
echo "  • Automated scheduled backups"
echo "  • Encrypted backup archives"
echo ""
echo -e "${YELLOW}Next Steps:${NC}"
echo "  1. Navigate to Settings → Backup & Restore"
echo "  2. Create your first config backup"
echo "  3. Download and store securely (with password!)"
echo "  4. Optional: Schedule automatic backups"
echo ""
echo -e "${BLUE}Documentation: /var/www/dplaneos/docs/RELEASE_NOTES_v1.13.0.md${NC}"
echo ""
