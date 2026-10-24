# DPlaneOS Installation Guide

DPlaneOS is a NixOS appliance. Installation means booting the ISO and answering a handful of prompts - the system builds itself from a pre-baked closure with no internet required.

For a full walkthrough aimed at users who have never touched NixOS, see [NixOS Install Guide](../../nixos/NIXOS-INSTALL-GUIDE.md).

---

## System Requirements

### Minimum

| Component | Minimum |
|-----------|---------|
| CPU | Dual-core x86_64 or ARM64 |
| RAM | 4 GB |
| Boot disk | 32 GB SSD (separate from data disks; 64 GB and more get a 40 GB system partition) |
| Firmware | UEFI (the installer refuses legacy BIOS/CSM boot) |
| Network | 100 Mbps Ethernet |

### Recommended

| Component | Recommended |
|-----------|-------------|
| CPU | Quad-core Intel/AMD or better |
| RAM | 16 GB+ |
| Boot disk | 60 GB+ NVMe |
| Network | 1 Gbps+ Ethernet |

### ECC RAM

ECC RAM is strongly recommended for ZFS at any scale. ZFS protects data against on-disk corruption but cannot detect corruption that originates in RAM before a write. ECC hardware closes this gap entirely.

DPlaneOS detects ECC presence via `dmidecode` at startup and shows an advisory notice on the dashboard if non-ECC RAM is found. See [NON-ECC-WARNING.md](../hardware/NON-ECC-WARNING.md) for a detailed risk assessment.

### Supported Platform

- **NixOS** (NixOS 26.05, Linux 6.12 LTS, OpenZFS 2.4) - the only supported platform

DPlaneOS is a NixOS appliance. For context on why this is intentional, see [NixOS Rationale](../reference/NIXOS-RATIONALE.md). For information on running DPlaneOS on other Linux distributions, see [Porting Guide](../reference/PORTING-GUIDE.md).

---

## Pre-Installation Checklist

- At least two physical drives: one for the OS (boot disk), one or more for data (ZFS pool)
- A USB stick of 4 GB or more (the installer ISO, which holds the complete system, is about 2 GB)
- UEFI boot enabled in the firmware (CSM / legacy boot off)
- A DHCP reservation for the server on your router (recommended; the installed system uses DHCP on every Ethernet port, and a static address can be set in the web interface)
- An SSH public key, if you want shell access: the installed system allows SSH with keys only (no password login, no root login). Add the key in the web interface (Identity › SSH Keys) after the first login

---

## Installation

### 1. Download the ISO

From the [latest release](https://github.com/4nonX/DPlaneOS/releases/latest), download the ISO for your hardware architecture:

| Hardware | ISO filename |
|----------|-------------|
| x86_64 (Intel/AMD) | `dplaneos-v<version>-installer-amd64.iso` |
| aarch64 (ARM servers with UEFI firmware; a Raspberry Pi 5 only with UEFI firmware such as the EDK2 port) | `dplaneos-v<version>-installer-arm64.iso` |

Each ISO also has a `.sha256` checksum file. Verify before writing:

```bash
sha256sum -c dplaneos-v*-installer-amd64.iso.sha256
```

> A third vote for a cluster needs no installer: any small Linux machine runs the one-line command from the HA page (see [THIRD-VOTE.md](THIRD-VOTE.md)).

### 2. Write to USB

```bash
# Linux / macOS
sudo dd if=dplaneos-v*-installer-amd64.iso of=/dev/sdX bs=4M status=progress conv=fsync

# Windows - use Rufus (https://rufus.ie) in DD image mode
```

Replace `/dev/sdX` with your USB device. Double-check with `lsblk` first.

### 3. Boot and Install

1. Boot the target machine from the USB stick
2. A menu appears on the screen (`tty1`) and on the serial console (`ttyS0`) with three options:
   - **Install DPlaneOS** - installs the full NAS system (offline, no internet required)
   - **Shell** / **Reboot** - diagnostic access
3. Select "Install DPlaneOS" and follow the prompts: target disk (at least 32 GB; the USB stick itself is not offered), admin password
4. The installer partitions the disk, installs the NixOS closure from the ISO, and reboots
5. Remove the USB stick when prompted

**Headless / remote install:** SSH to the installer as `root` with password `dplaneos` (installer environment only - this password does not persist to the installed system).

**Installation time:** 5-10 minutes from boot to running system.

### 4. First Access

```bash
# Find the server IP (if unknown)
ip addr show

# Or check your router's DHCP leases
```

Open `http://<server-ip>/` in a browser and log in as `admin` with the password chosen in the installer. On the first login the setup wizard opens:

1. Admin account: log in again with the installer's password (the wizard cannot replace it)
2. Select disks and configure a storage pool (optional - can skip and do later)
3. Set hostname and timezone

After the wizard completes, log in again. If setup was interrupted (browser closed mid-wizard), log in again: the wizard opens from the start until it has been completed.

**Session persistence:** By default the session is tab-scoped (lost on browser close). Check **Remember me** at login to keep the session across browser restarts; a session ends 24 hours after login either way.

---

## Post-Installation

### Verify Services

```bash
# Main daemon
sudo systemctl status dplaned

# nginx
sudo systemctl status nginx

# All DPlaneOS units
systemctl list-units 'dplaneos-*' 'dplaned*'

# Database
sudo -u postgres psql dplaneos -c "SELECT count(*) FROM roles;"
# Expected: 4 (admin, operator, viewer, user)
```

### HTTPS (Recommended)

HTTPS is configured in the web interface under Security › Certificates: request a certificate through ACME (Let's Encrypt), import one, or generate a self-signed certificate for internal-only use, then activate it. nginx serves it once the pending NixOS changes are applied (System › Settings → tab **NixOS** → apply); HTTP stays available alongside HTTPS.

### Firewall

The firewall is on. It opens TCP 80 and 443 for the web interface (`services.dplaneos.openFirewall`), 22 for SSH, and the ports of the services that are enabled (SMB 139/445 and WS-Discovery 5357, NFS 2049, and 5403 for a cluster's third vote). Rules from the Firewall page (Security › Firewall) apply on top: allow or deny by port, protocol and source. NixOS settings of your own go in `/etc/nixos/local.nix` (see [Updates](OTA-UPDATES.md)).

---

## OTA Upgrades

An update builds the new release as a new NixOS generation and reboots into it; if the check after the reboot fails, the system goes back to the previous generation by itself. See [Updates](OTA-UPDATES.md).

**Via the web UI (recommended):** Updates - Check for updates - Update.

**Via CLI:**

```bash
# Updates: the Updates page (builds the release, reboots, checks, goes back on failure).
# A manual rebuild always goes through the system flake:
sudo nixos-rebuild switch --flake /etc/nixos#dplaneos --impure
```

To go back by hand after an update, see [Updates](OTA-UPDATES.md#going-back-by-hand).

---

## Uninstall / Reinstall

There is no partial uninstall path. DPlaneOS is the OS. To remove it, reinstall a different operating system on the boot disk.

ZFS data pools on separate disks are not affected by reinstalling the boot disk. Import them after reinstalling:

```bash
zpool import -a
```

---

## Key Paths

| Item | Path |
|------|------|
| Daemon binary | `dplaned` on the PATH (in the Nix store) |
| Installed release | `/etc/dplaneos-source.json`, or `dplaned -version` |
| System flake | `/etc/nixos/flake.nix` |
| Daemon state | `/var/lib/dplaneos/` |
| Database state | `/var/lib/dplaneos/pgsql/` |
| Logs | `/var/log/dplaneos/` |
| Persistent state root | `/persist/` |

---

## Troubleshooting Installation

### Installer does not launch automatically

Connect via SSH (`root` / `dplaneos`) and run:

```bash
bash /etc/dplaneos-install/install.sh
```

### Daemon will not start after install

```bash
sudo journalctl -u dplaned -n 50
# Common causes: ZFS gate timeout, PostgreSQL not ready
sudo systemctl status dplaneos-zfs-gate
sudo systemctl status postgresql
```

### Cannot reach the web interface

```bash
sudo systemctl status nginx
curl --unix-socket /run/dplaneos/dplaned.sock http://localhost/health   # test daemon directly
```

### ZFS pools not visible

```bash
zpool list          # check pool status
zpool import -a     # import any un-imported pools
sudo systemctl restart dplaned
```

See [Troubleshooting Guide](TROUBLESHOOTING.md) for a full reference.
