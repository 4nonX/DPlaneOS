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
| Boot disk | 20 GB SSD (separate from data disks) |
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

- **NixOS** (NixOS 25.11, Linux 6.6 LTS, OpenZFS 2.3 LTS) - the only supported platform

DPlaneOS is a NixOS appliance. For context on why this is intentional, see [NixOS Rationale](../reference/NIXOS-RATIONALE.md). For information on running DPlaneOS on other Linux distributions, see [Porting Guide](../reference/PORTING-GUIDE.md).

---

## Pre-Installation Checklist

- At least two physical drives: one for the OS (boot disk), one or more for data (ZFS pool)
- A USB stick (2 GB minimum) to write the installer ISO
- Static IP configured on the network switch/router (recommended)
- SSH public key ready (password login is disabled post-install)

---

## Installation

### 1. Download the ISO

From the [latest release](https://github.com/4nonX/DPlaneOS/releases/latest), download the ISO for your hardware architecture:

| Hardware | ISO filename |
|----------|-------------|
| x86_64 (Intel/AMD) | `dplaneos-v<version>-installer-amd64.iso` |
| aarch64 (Raspberry Pi 5, ARM servers) | `dplaneos-v<version>-installer-arm64.iso` |

Each ISO also has a `.sha256` checksum file. Verify before writing:

```bash
sha256sum -c dplaneos-v*-installer-amd64.iso.sha256
```

> The combined installer ISO handles both NAS and witness node installation. The "Install Witness Node" option is for replicated-topology HA clusters (Path B) where a separate witness machine is required. Shared-SAS clusters (Path A) do not need a witness node - install only from the standard NAS option on both data nodes.

### 2. Write to USB

```bash
# Linux / macOS
sudo dd if=dplaneos-v*-installer-amd64.iso of=/dev/sdX bs=4M status=progress conv=fsync

# Windows - use Rufus (https://rufus.ie) in DD image mode
```

Replace `/dev/sdX` with your USB device. Double-check with `lsblk` first.

### 3. Boot and Install

1. Boot the target machine from the USB stick
2. A menu appears on `tty1` with three options:
   - **Install DPlaneOS** - installs the full NAS system (offline, no internet required)
   - **Install Witness Node** - installs a minimal etcd-only system for HA quorum on replicated-topology clusters (Path B only; shared-SAS clusters do not need this)
   - **Shell** / **Reboot** - diagnostic access
3. Select "Install DPlaneOS" and follow the prompts: target disk, admin password, boot mode
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

Open `http://<server-ip>/` in a browser. The setup wizard runs automatically on first access. You will:

1. Create the admin account (username + password) - no pre-generated password
2. Select disks and configure a storage pool (optional - can skip and do later)
3. Set hostname and timezone

After the wizard completes, log in with the credentials you just created. If setup was interrupted (browser closed mid-wizard), simply reopen the URL - the wizard detects the partial state and resumes from the disk selection step.

**Session persistence:** By default the session is tab-scoped (lost on browser close). Check **Remember me** at login to persist the session in `localStorage` across browser restarts, within the 24-hour server-side TTL.

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

HTTPS is configured via the DPlaneOS web UI under Settings - TLS. The daemon provisions certificates via the ACME protocol (Let's Encrypt by default).

For internal-only deployments, a self-signed certificate can be generated from the same settings page.

### Firewall

The NixOS module opens TCP 80 and 443 by default (`services.dplaneos.openFirewall = true`). To restrict further, set `openFirewall = false` and declare your own `networking.firewall` rules in `configuration.nix`.

---

## OTA Upgrades

DPlaneOS uses an A/B slot upgrade system. The installed system receives updates via the OTA mechanism:

```bash
sudo dplaneos-ota-update
```

The upgrade fetches the new system closure, writes it to the inactive boot slot, and reboots. If the post-boot health check fails, the system automatically reverts to the previous slot.

**Manual NixOS rebuild** (advanced):

```bash
sudo nixos-rebuild switch --flake github:4nonX/DPlaneOS#dplaneos
```

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
| Daemon binary | `/opt/dplaneos/daemon/dplaned` |
| Web UI files | `/opt/dplaneos/app/` |
| Version | `/opt/dplaneos/VERSION` |
| Database state | `/var/lib/dplaneos/pgsql/` |
| HA config | `/etc/dplaneos/patroni.yaml` |
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
curl http://127.0.0.1:9000/health   # test daemon directly
```

### ZFS pools not visible

```bash
zpool list          # check pool status
zpool import -a     # import any un-imported pools
sudo systemctl restart dplaned
```

See [Troubleshooting Guide](TROUBLESHOOTING.md) for a full reference.
