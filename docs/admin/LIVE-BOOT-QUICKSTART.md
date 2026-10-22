# D-PlaneOS Live Boot Quick Start

**Live boot mode** lets you run D-PlaneOS directly from USB without installing it to disk. The entire system runs in RAM, and you can trial the software, manage existing ZFS pools, run containers, and optionally install to disk when ready.

**What to expect:**
- Full D-PlaneOS daemon and UI running from RAM
- Existing ZFS pools automatically discovered and imported
- Zero disk writes to the USB (read-only squashfs)
- All state lost on shutdown (ephemeral) unless you attach a persistence USB
- Same storage and container capabilities as the installed version

---

## Getting Started (5 minutes)

### 1. Download and Flash ISO

Download the latest live boot ISO from [releases](https://github.com/4nonX/D-PlaneOS/releases):
- **x86_64:** `dplaneos-v14.7.0-live-x86_64.iso` (~3.3 GB)
- **ARM64:** `dplaneos-v14.7.0-live-arm64.iso` (~3.0 GB)

Flash to a USB drive (8GB+):

**Linux/macOS:**
```bash
# Identify USB device
lsblk  # or diskutil list

# Flash ISO
sudo dd if=dplaneos-v14.7.0-live-x86_64.iso of=/dev/sdX bs=4M status=progress
sudo sync
```

**Windows:**
Use [balenaEtcher](https://www.balena.io/etcher/) or [Rufus](https://rufus.ie/)

### 2. Boot from USB

1. Plug USB into target machine
2. Boot from USB (F12, Esc, or DEL during startup to enter boot menu)
3. Select USB drive as boot device
4. System boots in ~30 seconds

### 3. Access the UI

Once booted, open your browser and navigate to:
```
http://<machine-ip>:9000
```

Find the machine's IP:
- Check your router's DHCP client list
- Or SSH in: `ssh root@<ip>` (no password set yet)
- Or use serial console if available

---

## What's Available Now

### Manage Existing Storage

Your ZFS pools are automatically imported and mounted. In the UI:
- **View pools** under Storage → Pools
- **Manage datasets** (create, rename, delete, set properties)
- **Create snapshots** for backup
- **Configure shares** (SMB, NFS, iSCSI)
- **Monitor health** (scrub status, SMART alerts, disk errors)

### Run Containers

Docker is fully functional:
```bash
# Via CLI
docker ps
docker run -it ubuntu bash

# Via UI
Storage → Containers → Add Stack
# Paste your docker-compose.yml
```

### Command-Line Access

SSH into the live system:
```bash
ssh root@<ip>

# No password required (live mode, ephemeral)
# Use these commands:
zpool list              # List pools
zfs list                # List datasets
docker ps               # List containers
systemctl status        # Service status
```

### File Explorer

In the UI:
- **Storage → File Explorer** lets you browse and upload files
- Upload to the mounted ZFS pool
- Downloaded files persist on the pool (not lost on shutdown)

---

## Optional: Enable Persistence

By default, daemon state and config are **lost on shutdown** (ephemeral mode). To keep them across reboots, attach a persistence drive.

### Prepare the Persistence Drive

1. **Format a USB drive** as ext4, labelled `dplane-persist`. This needs Linux, or the live system itself:
   ```bash
   sudo mkfs.ext4 -L dplane-persist /dev/sdY1
   ```

2. **Attach it and reboot.** At boot, the system waits up to 10 seconds for the drive. From then on, the state below is kept on the drive. A drive attached after boot is used from the next boot.

### What Persists

- The daemon's database, settings, secrets key and firewall rules (`/var/lib/dplaneos`)
- Daemon configuration (`/etc/dplaneos`) and iSCSI targets (`/etc/target`)
- Network settings made in the UI (the `05-dplane-*` files of `/etc/systemd/network`)
- Data on ZFS pools persists anyway: it is on the pool's disks

### What Does NOT Persist

- Logs (`/var/log`) and `/tmp`
- SSH host keys (new ones each boot)
- Docker images and containers outside ZFS pools

---

## Advanced: Optional Install to Disk

If you want to install D-PlaneOS permanently to disk:

### Method 1: Via CLI (Advanced)

```bash
ssh root@<ip>

# Run the installer (still in development)
/etc/dplaneos-live/install.sh
# Follow prompts to select target disk and partition
```

### Method 2: Via UI (Coming Soon)

A UI-based installer is planned.

### Method 3: Manual (Not Recommended)

Use the [Installation Guide](INSTALLATION-GUIDE.md) to manually install from the ISO.

---

## Troubleshooting

### "No ZFS pools found"

If your pools aren't showing up:
1. Verify drives are connected and detected: `lsblk`
2. Check auto-import logs: `journalctl -u dplane-zfs-auto-import`
3. Manually import: `zpool import -a`

### "Web UI not responding"

1. Verify daemon is running: `systemctl status dplaned`
2. Check firewall: `sudo ufw status` (should be disabled by default)
3. Verify port 9000 is listening: `netstat -tlnp | grep 9000`
4. Check logs: `journalctl -u dplaned -n 50`

### "Can't SSH into machine"

1. Verify machine is on the network: `ping <ip>`
2. Verify SSH is running: `netstat -tlnp | grep 22`
3. Try SSH with verbose output: `ssh -vvv root@<ip>`

### "Persistence USB not detected"

1. Check the label: `lsblk -o NAME,LABEL,FSTYPE` must show `dplane-persist` and `ext4`
2. The drive must be attached at boot; reboot after attaching it
3. Check the log: `journalctl -b -u dplane-link-persist`

### System Performance Low

Live boot runs in RAM (default 2GB tmpfs for /var). If you see slowdowns:
1. Reduce running containers: `docker ps -a`
2. Check RAM usage: `free -h`
3. Monitor disk I/O: `iostat -x 1`

---

## Next Steps

### Trial Use Cases

- **Manage existing pool** without committing to new OS
- **Test Docker workflows** before installation
- **Backup & replicate** using D-PlaneOS tools
- **Rescue mission** if existing NAS becomes unavailable
- **Evaluate feature set** before deciding to install

### When Ready to Install

See [Installation Guide](INSTALLATION-GUIDE.md) for permanent disk installation.

### Learn More

- [Admin Guide](ADMIN-GUIDE.md) - Complete configuration and management
- [Architecture](../reference/ARCHITECTURE.md) - How D-PlaneOS works
- [Backup & Replication](BACKUP-REPLICATION.md) - Data protection strategies
- [High Availability](HIGH-AVAILABILITY.md) - Multi-node clustering (advanced)

---

## FAQ

**Q: Is it safe to run live for production use?**  
A: ZFS is production-grade, but D-PlaneOS in live boot is best for trials or temporary workloads. For permanent deployments, install to disk. Data on ZFS pools is never at risk; only daemon state is ephemeral.

**Q: Can I attach the persistence drive mid-session?**  
A: It is used from the next boot. Reboot after attaching it; settings made before then stay in RAM and are lost.

**Q: Can I reboot and keep daemon state?**  
A: Only if you've attached a `dplane-persist` USB. Without it, state resets after each reboot.

**Q: Will updates work in live mode?**  
A: OTA updates are disabled in live boot. To upgrade, download a new ISO and reboot.

**Q: Can I access stored files without the daemon?**  
A: Yes, ZFS pools are standard and can be imported on any Linux system with ZFS tools.

---

**Need help?** Open an issue on [GitHub](https://github.com/4nonX/D-PlaneOS/issues) or check the [Troubleshooting Guide](TROUBLESHOOTING.md).
