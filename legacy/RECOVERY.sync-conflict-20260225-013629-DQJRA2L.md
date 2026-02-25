# D-PlaneOS Recovery Playbook

**Version:** 1.5.0  
**Purpose:** System recovery procedures for administrators  
**Audience:** System administrators, DevOps, support staff

---

## Quick Reference

| Scenario | Page | Severity |
|----------|------|----------|
| Lost admin password | [→](#lost-admin-password) | 🟡 Medium |
| Database corruption | [→](#database-corruption) | 🔴 Critical |
| Web interface down | [→](#web-interface-down) | 🔴 Critical |
| System won't boot | [→](#system-wont-boot) | 🔴 Critical |
| Pool degraded | [→](#pool-degraded) | 🔴 Critical |
| Accidental pool deletion | [→](#accidental-pool-deletion) | 🔴 Critical |
| Container issues | [→](#container-issues) | 🟡 Medium |
| Disk failure | [→](#disk-failure) | 🟠 High |
| System compromise suspected | [→](#security-incident) | 🔴 Critical |

---

## Prerequisites

### Required Access
- Root or sudo access to the system
- Physical or SSH access to the server
- Access to backup storage (if applicable)

### Required Knowledge
- Basic Linux command line
- Understanding of ZFS concepts
- System paths:
  - `/var/dplane/` - D-PlaneOS installation
  - `/var/dplane/database/dplane.db` - Main database
  - `/var/dplane/backups/` - Automatic backups
  - `/var/log/nginx/` - Web server logs

---

## Recovery Scenarios

### Lost Admin Password

**Symptoms:**
- Cannot login to web interface
- Know username but forgot password

**Recovery Steps:**

1. **Access system via SSH/console**
   ```bash
   ssh root@your-server
   ```

2. **Reset password to default (admin/admin)**
   ```bash
   sqlite3 /var/dplane/database/dplane.db "UPDATE users SET password='$2y$10$92IXUNpkjO0rOQ5byMi.Ye4oKoEa3Ro9llC/.og/at2.uheWG/igi' WHERE username='admin'"
   ```

3. **Verify reset**
   ```bash
   # Try login at http://your-server
   # Username: admin
   # Password: admin
   ```

4. **Change password immediately**
   - Login to web interface
   - Navigate to user settings
   - Set new strong password

**Alternative: Create new admin user**
```bash
# If you want a different username
sqlite3 /var/dplane/database/dplane.db
> INSERT INTO users (username, password, email) 
  VALUES ('newadmin', '$2y$10$92IXUNpkjO0rOQ5byMi.Ye4oKoEa3Ro9llC/.og/at2.uheWG/igi', 'admin@localhost');
> .quit
```

**Prevention:**
- Store password in password manager
- Document password in secure location
- Consider creating backup admin account

---

### Database Corruption

**Symptoms:**
- "System database is corrupted" error
- Web interface shows database errors
- Cannot login even with correct credentials

**Diagnosis:**
```bash
# Check database file
ls -lh /var/dplane/database/dplane.db

# Test database integrity
sqlite3 /var/dplane/database/dplane.db "PRAGMA integrity_check;"
# Should return: ok

# If corrupted, will show errors
```

**Recovery Steps:**

**Option 1: Restore from automatic backup**
```bash
# List available backups
ls -lh /var/dplane/backups/

# Choose most recent backup
BACKUP=$(ls -t /var/dplane/backups/dplane-*.db | head -1)
echo "Using backup: $BACKUP"

# Stop services
systemctl stop nginx php8.2-fpm

# Restore database
cp /var/dplane/database/dplane.db /var/dplane/database/dplane.db.corrupted
cp $BACKUP /var/dplane/database/dplane.db
chown www-data:www-data /var/dplane/database/dplane.db
chmod 644 /var/dplane/database/dplane.db

# Start services
systemctl start php8.2-fpm nginx

# Verify
curl http://localhost/
```

**Option 2: Dump and restore**
```bash
# If database is partially readable
sqlite3 /var/dplane/database/dplane.db .dump > /tmp/dump.sql

# Create new database
mv /var/dplane/database/dplane.db /var/dplane/database/dplane.db.old
sqlite3 /var/dplane/database/dplane.db < /var/dplane/system/database/schema.sql

# Restore data
sqlite3 /var/dplane/database/dplane.db < /tmp/dump.sql

# Fix permissions
chown www-data:www-data /var/dplane/database/dplane.db
chmod 644 /var/dplane/database/dplane.db
```

**Option 3: Fresh database (LAST RESORT)**
```bash
# WARNING: Loses all settings and audit log
# But pools/datasets are NOT affected (they're in ZFS)

systemctl stop nginx php8.2-fpm

mv /var/dplane/database/dplane.db /var/dplane/database/dplane.db.corrupt
sqlite3 /var/dplane/database/dplane.db < /var/dplane/system/database/schema.sql
chown www-data:www-data /var/dplane/database/dplane.db
chmod 644 /var/dplane/database/dplane.db

systemctl start php8.2-fpm nginx

# Login with: admin / admin
# Reconfigure settings
```

**Prevention:**
- Regular backups: `0 2 * * * cp /var/dplane/database/dplane.db /backup/`
- Monitor disk space
- Use ZFS for /var/dplane if possible
- Enable ZFS snapshots of root filesystem

---

### Web Interface Down

**Symptoms:**
- Cannot access http://server-ip
- "Connection refused" or timeout
- 502 Bad Gateway

**Diagnosis:**
```bash
# Check if services are running
systemctl status nginx
systemctl status php8.2-fpm

# Check if ports are listening
ss -tlnp | grep :80

# Check logs
tail -50 /var/log/nginx/error.log
journalctl -u nginx -n 50
journalctl -u php8.2-fpm -n 50
```

**Recovery Steps:**

**Issue 1: Nginx not running**
```bash
# Start nginx
systemctl start nginx

# If fails, check config
nginx -t

# If config error, restore from package
cd /tmp
tar -xzf /path/to/dplaneos-v1.3.2.tar.gz
cp dplaneos-v1.3.2/system/dashboard /var/www/dplane -r
systemctl restart nginx
```

**Issue 2: PHP-FPM not running**
```bash
# Start PHP-FPM
systemctl start php8.2-fpm

# If fails, check config
php-fpm8.2 -t

# Check socket exists
ls -la /var/run/php/php8.2-fpm.sock
```

**Issue 3: Permission issues**
```bash
# Fix permissions
chown -R www-data:www-data /var/dplane
chmod -R 755 /var/dplane/system
chmod 644 /var/dplane/database/dplane.db

# Fix web root
chown -R www-data:www-data /var/www/dplane
```

**Issue 4: Port conflict**
```bash
# Check what's using port 80
lsof -i :80

# If another service is using it, stop it or change D-PlaneOS port
# Edit /etc/nginx/sites-available/dplaneos
# Change: listen 80; to listen 8080;
systemctl restart nginx
```

**Prevention:**
- Monitor services with external tool
- Set up automatic service restart
- Configure webhook alerts

---

### System Won't Boot

**Symptoms:**
- Server won't boot to OS
- ZFS import failure on boot
- System hangs at boot

**Recovery Steps:**

**Option 1: Boot from rescue media**
```bash
# Boot from Ubuntu/Debian live USB
# Mount root filesystem
mkdir /mnt/root
mount /dev/sdX1 /mnt/root

# Chroot into system
mount --bind /dev /mnt/root/dev
mount --bind /proc /mnt/root/proc
mount --bind /sys /mnt/root/sys
chroot /mnt/root

# Now you can repair system
```

**Option 2: ZFS import issues**
```bash
# Boot into single user mode or rescue

# Check pool status
zpool import

# Force import pool
zpool import -f poolname

# If pool is degraded but bootable
zpool scrub poolname
zpool status -v poolname
```

**Option 3: Grub issues**
```bash
# From rescue media
mount /dev/sdX1 /mnt
mount --bind /dev /mnt/dev
mount --bind /proc /mnt/proc
mount --bind /sys /mnt/sys
chroot /mnt

# Reinstall grub
grub-install /dev/sdX
update-grub

# Reboot
exit
reboot
```

**Prevention:**
- Regular system backups
- Test restore procedures
- Keep rescue media ready
- Document boot device order

---

### Pool Degraded

**Symptoms:**
- Pool status shows DEGRADED
- Email/webhook alert received
- Dashboard shows pool health warning

**Diagnosis:**
```bash
# Check pool status
zpool status -v poolname

# Check disk health
for disk in /dev/sd?; do
  echo "=== $disk ==="
  smartctl -H $disk
done

# Check system logs
dmesg | grep -i error
journalctl -n 100 | grep -i zfs
```

**Recovery Steps:**

**Scenario 1: Disk removed but reattached**
```bash
# Disk was temporarily disconnected
# Check if disk is visible
lsblk

# Online the disk
zpool online poolname /dev/sdX

# Verify
zpool status poolname
```

**Scenario 2: Disk failed**
```bash
# Disk is permanently failed
# Have replacement disk ready

# Replace disk (if hot-swap)
# Otherwise, shutdown and replace

# After replacement
zpool replace poolname /dev/sdX_old /dev/sdX_new

# Monitor resilver
watch zpool status -v poolname

# Will take hours for large pools
```

**Scenario 3: Temporary error**
```bash
# Sometimes ZFS marks disk degraded after transient error
# Clear errors if disk is actually healthy

# Check if disk passes SMART test
smartctl -t short /dev/sdX
# Wait 2 minutes
smartctl -a /dev/sdX

# If healthy, clear errors
zpool clear poolname /dev/sdX

# Verify
zpool status poolname
```

**Prevention:**
- Use redundant RAID (mirror, raidz)
- Monitor SMART data
- Replace disks showing warnings
- Keep spare disks available
- Set up alert webhooks

---

### Accidental Pool Deletion

**Symptoms:**
- Pool no longer appears in `zpool list`
- Data is gone
- Dashboard shows no pools

**Reality Check:**
⚠️ **If pool was destroyed, data is GONE**
⚠️ **ZFS destroy is immediate and permanent**
⚠️ **Only backups can recover data**

**Possible Recovery (if recent):**

```bash
# Check if pool still exists but not imported
zpool import

# If pool shows up, import it
zpool import poolname

# If pool shows up but damaged
zpool import -F poolname

# If pool completely gone, check backups
```

**Data Recovery Options:**

1. **Replication backup**
   ```bash
   # If you configured replication
   # On backup server:
   zfs send backup/poolname@latest | ssh original-server zfs receive poolname
   ```

2. **File-level backups**
   ```bash
   # If you backed up files externally
   # Recreate pool and restore files
   zpool create poolname /dev/sdX /dev/sdY
   rsync -avP /backup/location/ /poolname/
   ```

3. **Snapshot recovery (if pool exists)**
   ```bash
   # If pool exists and you have snapshots
   zfs list -t snapshot
   zfs rollback poolname@snapshot-name
   ```

**Prevention:**
- **ALWAYS** have replication or external backups
- Enable confirmation prompts (D-PlaneOS already does this)
- Test restore procedures regularly
- Consider ZFS send/receive replication
- Document recovery procedures

**NOTE:** D-PlaneOS requires pool name confirmation before destruction, but human error is still possible.

---

### Container Issues

**Symptoms:**
- Container won't start
- Container stuck in restart loop
- Cannot access container services

**Diagnosis:**
```bash
# Check container status
docker ps -a

# Check container logs
docker logs containername

# Check Docker daemon
systemctl status docker
journalctl -u docker -n 50
```

**Recovery Steps:**

**Issue 1: Container won't start**
```bash
# Check why it failed
docker logs containername

# Common fixes:
# - Port conflict
docker ps | grep PORT
# Kill conflicting container or change port

# - Volume missing
docker inspect containername | grep -A 10 Mounts
# Recreate volumes

# - Remove and recreate
docker rm containername
# Redeploy via D-PlaneOS UI
```

**Issue 2: Docker daemon issues**
```bash
# Restart Docker
systemctl restart docker

# If fails, check disk space
df -h

# Check Docker storage
docker system df
docker system prune  # Remove unused data
```

**Issue 3: Compose file corruption**
```bash
# Check compose files
ls -la /var/dplane/compose/

# Validate syntax
cd /var/dplane/compose
docker-compose -f filename.yml config

# Recreate if needed via UI
```

**Prevention:**
- Monitor container resources
- Set restart policies
- Keep compose files backed up
- Document container configs

---

### Disk Failure

**Symptoms:**
- SMART alerts
- I/O errors in logs
- System slowness
- Pool degraded status

**Diagnosis:**
```bash
# Check all disks
for disk in /dev/sd?; do
  echo "=== $disk ==="
  smartctl -H $disk
  smartctl -A $disk | grep -E "Reallocated|Current_Pending|Offline"
done

# Check system logs
dmesg | tail -100 | grep -i error
```

**Immediate Actions:**

1. **If disk is failing:**
   ```bash
   # Prepare replacement
   # Order new disk immediately
   
   # If pool is redundant (mirror/raidz)
   # System can run degraded temporarily
   
   # If no redundancy
   # BACKUP DATA IMMEDIATELY
   rsync -avP /poolname/ /external-backup/
   ```

2. **Replace disk:**
   ```bash
   # See "Pool Degraded" section above
   # Use zpool replace command
   ```

**Prevention:**
- Use redundant configurations
- Monitor SMART data weekly
- Replace disks at first sign of failure
- Keep spare disks on hand
- Test replacements before deployment

---

### Security Incident

**Symptoms:**
- Suspicious audit log entries
- Unexpected system changes
- Unknown containers running
- High resource usage
- Alerts from monitoring

**Immediate Response:**

1. **Isolate system**
   ```bash
   # Stop web access
   systemctl stop nginx
   
   # Block network (if needed)
   iptables -A INPUT -p tcp --dport 80 -j DROP
   iptables -A INPUT -p tcp --dport 443 -j DROP
   ```

2. **Investigate**
   ```bash
   # Check audit log
   sqlite3 /var/dplane/database/dplane.db \
     "SELECT * FROM audit_log ORDER BY timestamp DESC LIMIT 100"
   
   # Check for security blocks
   grep SECURITY /var/log/nginx/error.log
   
   # Check active sessions
   ls -la /var/lib/php/sessions/
   
   # Check running containers
   docker ps
   
   # Check system processes
   ps aux | grep -v "\[" | head -50
   ```

3. **Contain**
   ```bash
   # Kill suspicious processes
   kill -9 PID
   
   # Stop suspicious containers
   docker stop suspicious-container
   docker rm suspicious-container
   
   # Clear sessions
   rm /var/lib/php/sessions/sess_*
   ```

4. **Recover**
   ```bash
   # Change admin password
   sqlite3 /var/dplane/database/dplane.db \
     "UPDATE users SET password='NEW_HASH' WHERE username='admin'"
   
   # Restore from backup if compromised
   # See "Database Corruption" section
   
   # Review and tighten security
   # See SECURITY.md and THREAT-MODEL.md
   ```

5. **Post-Incident**
   - Review all audit logs
   - Check for backdoors
   - Update all passwords
   - Review firewall rules
   - Enable TLS if not already
   - Consider full reinstall if deeply compromised

**Prevention:**
- Use strong passwords
- Enable webhook alerts
- Monitor audit logs
- Use HTTPS
- Restrict network access
- Keep system updated

---

## Emergency Contacts

### Critical Paths

```bash
# System files
/var/dplane/                    # Installation directory
/var/dplane/database/dplane.db  # Main database
/var/dplane/backups/            # Automatic backups
/var/www/dplane                 # Web root (symlink)

# Logs
/var/log/nginx/error.log        # Web server errors
/var/log/nginx/access.log       # Access log
/var/log/syslog                 # System log

# Services
systemctl status nginx          # Web server
systemctl status php8.2-fpm     # PHP processor
systemctl status docker         # Container runtime
```

### Quick Commands

```bash
# Restart all services
systemctl restart nginx php8.2-fpm docker

# Check system health
zpool status
docker ps
df -h
free -h

# Emergency stop
systemctl stop nginx php8.2-fpm

# Full backup
tar -czf /tmp/dplane-backup.tar.gz /var/dplane

# Check version
cat /var/dplane/VERSION
```

---

## Testing Recovery Procedures

### Monthly Tests

1. **Test password reset**
   - Reset password to test value
   - Verify login works
   - Reset back to real password

2. **Test database backup/restore**
   - Copy database to test location
   - Restore from backup
   - Verify data intact

3. **Test service restart**
   - Stop services
   - Verify stops cleanly
   - Start services
   - Verify starts cleanly

### Quarterly Tests

1. **Test pool resilver**
   - Offline a disk (in redundant pool)
   - Online it again
   - Verify resilver works

2. **Test container restore**
   - Remove a non-critical container
   - Restore from compose file
   - Verify works correctly

### Annual Tests

1. **Test full system restore**
   - Set up test instance
   - Restore from complete backup
   - Verify all functionality

2. **Test disaster recovery**
   - Simulate complete system loss
   - Restore on new hardware
   - Document time and issues

---

## Conclusion

**Key Principles:**
1. **Backups are mandatory** - No backup = no recovery
2. **Test recovery regularly** - Untested backup = no backup
3. **Document everything** - Future you will thank you
4. **Have spare parts** - Disk failure at 2 AM is not fun
5. **Monitor proactively** - Fix issues before they're emergencies

**Resources:**
- `SECURITY.md` - Security procedures
- `THREAT-MODEL.md` - Security architecture
- `CHANGELOG.md` - Version history
- `/var/dplane/backups/` - Automatic backups

---

**Document Version:** 1.0  
**System Version:** 1.5.0  
**Last Updated:** 2026-01-28
