# D-PlaneOS v3.3.1 Installation Guide

**Complete enterprise NAS with RBAC**

---

## System Requirements

### **Minimum:**
- CPU: Dual-core x86_64
- RAM: 4 GB
- Storage: 20 GB for OS + data drives
- Network: 1 Gbps Ethernet

### **Recommended:**
- CPU: Quad-core Intel/AMD (i3/Ryzen 3 or better)
- RAM: 16 GB+
- Storage: ZFS pools (any size)
- Network: 2.5 Gbps+ Ethernet

### **Recommended (for data integrity):**
- **ECC RAM** — strongly recommended for ZFS integrity at scale

  ZFS is exceptional at detecting and correcting corruption *on disk*, but it
  cannot protect data that was already corrupted *in RAM* before being written.
  ECC RAM detects and corrects single-bit memory errors in hardware, closing
  this gap entirely.

  **Without ECC RAM:**
  - Home use / media library: acceptable risk with regular scrubs and backups
  - Business / critical data / databases: not recommended

  **D-PlaneOS v3.3.0 behaviour:**
  - Detects ECC presence via `dmidecode` on startup
  - Shows a persistent advisory notice in the dashboard if non-ECC RAM is found
  - **Never blocks installation or operation** — ECC is your choice, not ours
  - See `NON-ECC-WARNING.md` for full mitigation strategies

  **Minimum ECC-capable platforms:** Intel Xeon, AMD EPYC, AMD Ryzen Pro,
  any server/workstation board with ECC UDIMM support.

### **Supported Platforms:**
- Ubuntu 24.04 LTS (recommended)
- Debian 12
- NixOS (experimental — see NixOS section)
- Any systemd-based Linux with ZFS support

---

## Pre-Installation Checklist

- [ ] Fresh Ubuntu 24.04 install (or compatible)
- [ ] Root/sudo access
- [ ] Internet connection
- [ ] At least 2 drives (1 for OS, 1+ for storage)
- [ ] Static IP configured (recommended)
- [ ] Hostname set (`hostnamectl set-hostname nas`)

---

## Installation Steps

### **Step 1: Download Package**

```bash
# Download latest release
wget https://github.com/4nonX/D-PlaneOS/releases/download/v3.3.0/dplaneos-v3.3.0.tar.gz

# Verify SHA256
sha256sum dplaneos-v3.3.1.tar.gz
# SHA256 published with each release on GitHub
```

### **Step 2: Extract**

```bash
tar -xzf dplaneos-v3.3.1.tar.gz
cd dplaneos-v3.3.1
```

### **Step 3: Run Installer**

```bash
sudo ./install.sh
```

**What happens:**
1. System dependencies installed (ZFS, Docker, SQLite, Go)
2. D-PlaneOS daemon compiled
3. Database initialized with RBAC schema
4. systemd service created
5. nginx reverse proxy configured
6. First admin user setup prompt

**Installation time:** ~5-10 minutes (depending on internet speed)

### **Step 4: Access Web Interface**

```bash
# Get your IP
ip addr show

# Open browser
http://YOUR-IP/
```

**First login redirects to Setup Wizard automatically**

---

## Setup Wizard

### **Step 1: Welcome**
- Overview of features
- Click "Get Started"

### **Step 2: Storage Discovery**
- System scans all available disks
- Click on disks to select
- Choose pool type:
  - Single (testing only)
  - Mirror (2+ disks, 50% usable)
  - RAID-Z1 (3+ disks, 1 disk failure)
  - RAID-Z2 (4+ disks, 2 disk failures) **← Recommended**
  - RAID-Z3 (5+ disks, 3 disk failures)
- Click "Create Pool"

### **Step 3: Admin Account**
- Username: `admin` (recommended)
- Password: (strong, 12+ characters)
- Email: (for recovery)
- Click "Create Admin"

**First user automatically gets admin role (full permissions)**

### **Step 4: Initial Scan**
- System indexes your storage
- Progress: 0-100%
- Time: ~30 seconds for empty pool

### **Step 5: Complete**
- System ready
- Click "Go to Dashboard"

**Total setup time: 3 minutes**

---

## Post-Installation

### **1. Verify Services**

```bash
# Check daemon
sudo systemctl status dplaned

# Should show: active (running)

# Check database
sudo sqlite3 /var/lib/dplaneos/dplaneos.db "SELECT COUNT(*) FROM roles;"
# Should return: 4 (admin, operator, viewer, user)
```

### **2. Configure Network (Optional)**

```bash
# Set static IP (if not already done)
sudo nmcli con mod "Wired connection 1" ipv4.addresses 192.168.1.100/24
sudo nmcli con mod "Wired connection 1" ipv4.gateway 192.168.1.1
sudo nmcli con mod "Wired connection 1" ipv4.dns "8.8.8.8"
sudo nmcli con mod "Wired connection 1" ipv4.method manual
sudo nmcli con up "Wired connection 1"
```

### **3. Enable TLS (Highly Recommended)**

```bash
# Install certbot
sudo apt install certbot python3-certbot-nginx

# Get certificate (requires domain pointing to your IP)
sudo certbot --nginx -d nas.yourdomain.com

# Auto-renewal
sudo systemctl enable certbot.timer
```

### **4. Setup Firewall**

```bash
# Allow HTTP/HTTPS
sudo ufw allow 80/tcp
sudo ufw allow 443/tcp

# Allow SSH (be careful!)
sudo ufw allow 22/tcp

# Enable firewall
sudo ufw enable
```

### **5. Configure Fail2Ban (Security)**

```bash
# Install
sudo apt install fail2ban

# Configure for D-PlaneOS
sudo tee /etc/fail2ban/jail.d/dplaneos.conf << EOF
[dplaneos]
enabled = true
port = 80,443
filter = dplaneos
logpath = /var/log/dplaneos/access.log
maxretry = 5
bantime = 3600
findtime = 600
EOF

# Restart
sudo systemctl restart fail2ban
```

---

## Upgrading from v3.3.0

**Note:** v3.3.1 is backward compatible. Upgrade preserves all data and configuration.

**If you have data in v3.3.0:**

1. Backup your data:
   ```bash
   zfs snapshot tank@pre-upgrade
   zfs send tank@pre-upgrade > /backup/tank-pre-upgrade.zfs
   ```

2. Fresh install

3. Import existing pool:
   ```bash
   zpool import tank
   ```

4. Recreate users via UI with proper roles

---

## Troubleshooting Installation

### **Installer fails with "ZFS not found"**

```bash
# Install ZFS manually
sudo apt update
sudo apt install zfsutils-linux
sudo modprobe zfs
```

### **Daemon won't start**

```bash
# Check logs
sudo journalctl -u dplaned -n 50

# Common issue: Port 8080 already in use
sudo lsof -i :8080
# Kill conflicting process or change port in /etc/dplaneos/config.json
```

### **Can't access web interface**

```bash
# Check nginx
sudo systemctl status nginx

# Check firewall
sudo ufw status

# Check if daemon is listening
sudo netstat -tlnp | grep 8080
```

### **Database initialization failed**

```bash
# Re-initialize database
sudo rm /var/lib/dplaneos/dplaneos.db
sudo systemctl restart dplaned

# Check if it recreated
ls -lh /var/lib/dplaneos/dplaneos.db
```

---

## Uninstall

```bash
cd dplaneos-v3.3.1
sudo ./uninstall.sh

# Remove all data (WARNING: destroys everything)
sudo zpool destroy tank  # if you want to delete pool
sudo rm -rf /var/lib/dplaneos
sudo rm -rf /etc/dplaneos
```

---

## Next Steps

After installation:

1. **Create Users:** Settings → Users → Create User
2. **Assign Roles:** Settings → Users → Select User → Assign Role
3. **Configure Storage:** Storage → Create datasets
4. **Setup Docker:** Containers → Deploy containers
5. **Configure Monitoring:** Monitoring → Set alert thresholds

**Read:** `ADMIN-GUIDE.md` for detailed administration

---

## Support

- Documentation: `/usr/share/doc/dplaneos/`
- Logs: `/var/log/dplaneos/`
- Config: `/etc/dplaneos/config.json`
- Database: `/var/lib/dplaneos/dplaneos.db`

**Log locations:**
- Daemon: `journalctl -u dplaned`
- Web: `/var/log/nginx/access.log`
- Audit: SQLite database (view via UI: Settings → Audit Log)

---

**Installation complete! Your enterprise NAS is ready.**


---

## NixOS Installation

For the full NixOS installation guide, see **INSTALLATION-GUIDE-NIXOS.md** (in the release archive or at the repository root).

**Quick note on PolyForm licensing and `nixpkgs.config.allowUnfreePredicate`:**

NixOS classifies packages by license. D-PlaneOS uses the PolyForm Shield 1.0.0 license — source-available but not OSI-approved, so NixOS marks it as `unfree`. This means a plain `nixos-rebuild switch` will fail with:

```
error: Package 'dplaneos-daemon' has an unfree license 'PolyForm Shield 1.0.0',
refusing to evaluate.
```

The `configuration-standalone.nix` provided in `nixos/` already handles this with a **targeted allowlist** — it permits only the D-PlaneOS daemon, not all unfree packages globally:

```nix
nixpkgs.config.allowUnfreePredicate = pkg:
  builtins.elem (lib.getName pkg) [
    "dplaneos-daemon"
  ];
```

This is already present in the config file. You do not need to add it manually.

If you prefer to audit it yourself before building, open `/etc/nixos/configuration.nix` and verify this block is present before running `nixos-rebuild switch`.
