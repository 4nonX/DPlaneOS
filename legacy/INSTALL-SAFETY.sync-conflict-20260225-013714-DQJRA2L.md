# D-PlaneOS v1.8.0 - Installation Safety Features

## Overview

This version includes significant safety improvements to prevent system damage during installation and provide clear rollback capabilities.

## New Safety Features

### 1. Pre-Flight Validation
Before making ANY changes to your system, the installer performs comprehensive checks:

- ✅ Disk space verification (minimum 500MB required)
- ✅ Required command availability (zfs, zpool, docker, php, systemctl)
- ✅ Sudoers syntax validation using `visudo -c`
- ✅ File integrity verification via SHA256 checksums

**If ANY check fails, installation aborts immediately with no changes made.**

### 2. Dry-Run Mode
Test the installation without making any changes:

```bash
sudo bash install.sh --dry-run
```

This shows:
- What files would be installed
- Where they would go
- What permissions would be set
- What services would be enabled

Perfect for:
- Testing on new systems
- Verifying before production deployment
- Understanding what the installer does

### 3. Enhanced Sudoers Handling

**Critical improvement:** Sudoers file is now handled with extreme care:

1. **Pre-installation validation** - Syntax checked BEFORE copying
2. **Timestamped backups** - Existing config backed up with timestamp
3. **Temp-file staging** - New config validated in /tmp first
4. **Production validation** - Final check after moving to /etc/sudoers.d/
5. **Automatic rollback** - If validation fails, immediately restores backup

**Rollback sequence:**
```
Validate new sudoers → FAIL
└─> Restore from backup/sudoers/dplaneos.TIMESTAMP
    └─> Validate restored config → SUCCESS
        └─> Exit with error (no damage done)
```

### 4. Automatic Backups

All critical operations create timestamped backups:

- **Sudoers:** `/var/dplane/backups/sudoers/dplaneos.YYYYMMDD_HHMMSS`
- **System files:** `/var/dplane/backups/system.YYYYMMDD_HHMMSS` (on upgrade)

### 5. Version Tracking

Installation creates `/var/dplane/VERSION` containing exactly:
```
1.8.0
```

This enables:
- Accurate upgrade detection
- Version auditing
- Bug report correlation

### 6. Error Handling

The installer now uses:
```bash
set -e          # Exit immediately on any error
set -u          # Fail on undefined variables
set -o pipefail # Fail if any command in a pipe fails
```

Plus explicit error checking on critical operations:
```bash
cp -r ./system /var/dplane/ || {
    echo "Failed to copy system files"
    exit 1
}
```

## Usage Examples

### Standard Installation
```bash
sudo bash install.sh
```

### Test Without Changes
```bash
sudo bash install.sh --dry-run
```

### Emergency Skip Validation (NOT RECOMMENDED)
```bash
sudo bash install.sh --skip-validation
```

**Warning:** Only use `--skip-validation` if:
- You've manually verified the package
- You're in a recovery scenario
- You understand the risks

## Rollback Procedures

### If Installation Fails Mid-Way

1. **Sudoers issues:**
   ```bash
   # Backups are in:
   ls /var/dplane/backups/sudoers/
   
   # Restore manually if needed:
   sudo cp /var/dplane/backups/sudoers/dplaneos.TIMESTAMP /etc/sudoers.d/dplaneos
   sudo visudo -c  # Verify
   ```

2. **System files issues:**
   ```bash
   # Backups are in:
   ls /var/dplane/backups/
   
   # Restore manually:
   sudo rm -rf /var/dplane/system
   sudo mv /var/dplane/backups/system.TIMESTAMP /var/dplane/system
   ```

### Complete Removal

If you need to completely remove D-PlaneOS:

```bash
sudo systemctl stop nginx php8.2-fpm
sudo rm -rf /var/dplane
sudo rm /etc/sudoers.d/dplaneos
sudo rm /var/www/dplane
sudo rm /etc/cron.d/dplaneos-monitor
```

## Verification After Install

Check installation was successful:

```bash
# Check version
cat /var/dplane/VERSION

# Check sudoers syntax
sudo visudo -c

# Check services
systemctl status nginx php8.2-fpm docker

# Check web interface
curl -I http://localhost
```

## What Changed From Previous Versions

### Packaging
- ❌ Removed: Nested v1.6.0 directory (packaging error)
- ✅ Clean: Single version only
- ✅ Added: SHA256SUMS for integrity verification

### Installer
- ✅ Fixed: Version banner now shows v1.8.0 (was v1.5.0)
- ✅ Fixed: VERSION file now contains 1.8.0 (was 1.5.0)
- ✅ Added: `--dry-run` mode
- ✅ Added: `--skip-validation` mode
- ✅ Added: Pre-flight system checks
- ✅ Added: Sudoers validation before install
- ✅ Added: File integrity verification
- ✅ Added: Automatic rollback on sudoers failure
- ✅ Added: Timestamped backups
- ✅ Improved: Error handling with set -u and set -o pipefail

### Security
- ✅ Sudoers now validated BEFORE touching /etc/sudoers.d/
- ✅ Temporary staging prevents corruption
- ✅ Automatic rollback prevents lockout
- ✅ Checksums verify package integrity

## Known Limitations

1. **No atomic transactions** - If failure occurs mid-install, some files may be in place
2. **Manual cleanup required** - No automatic uninstaller
3. **Limited rollback scope** - Only sudoers and system files have automatic rollback
4. **Database not backed up** - Database changes are not automatically reversed

## Recommendations

1. **Always test with --dry-run first**
2. **Read the output carefully** - Installer tells you what it's doing
3. **Keep backups** - The installer creates them, don't delete them
4. **Verify checksums** - If SHA256SUMS verification fails, don't proceed
5. **Test sudoers manually** - After install, verify with: `sudo -u www-data sudo /usr/sbin/zpool list`

## Emergency Contacts

If you encounter issues:

1. Check `/var/dplane/backups/` for recovery files
2. Review installer output (redirect to file: `sudo bash install.sh 2>&1 | tee install.log`)
3. Verify sudoers: `sudo visudo -c`
4. Check service status: `systemctl status nginx php8.2-fpm`

## Version History

- **v1.8.0** - Emergency safety cleanup (this version)
  - Fixed packaging disaster (nested versions)
  - Added comprehensive safety rails
  - Fixed version mismatches
  - Added integrity verification
  
- **v1.7.0 and earlier** - See CHANGELOG.md

---

**This version prioritizes safety over convenience.**  
Better to take an extra 30 seconds for validation than to risk system damage.
