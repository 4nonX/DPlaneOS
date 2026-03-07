# D-PlaneOS on NixOS — Complete Installation Guide

> **Audience**: You've never used NixOS before. You want a NAS.
> This guide takes you from "empty server" to "D-PlaneOS running" — step by step, no prior knowledge required.

---

## What is NixOS (30-Second Version)

NixOS is a Linux distribution where the **entire system is defined in a single text file**: `configuration.nix`. You describe everything — packages, services, firewall rules. Then you run `sudo nixos-rebuild switch` and NixOS builds the system exactly as described.

**Why is this great for a NAS?**
- Broken update? → `sudo nixos-rebuild switch --rollback` — one command and everything is restored
- Server dies? → Install NixOS on new hardware, copy `configuration.nix`, import ZFS pool — done
- Your entire NAS config is version-controllable (Git)

---

## Requirements

- A PC/server for the NAS (minimum 4 GB RAM, recommended 8+)
- A USB stick (minimum 2 GB) for the NixOS installer
- A **separate boot disk** (SSD/HDD/NVMe) — NixOS goes here
- Your data disks (used for the ZFS pool — **not** for NixOS)
- A second computer to follow this guide
- Ethernet cable (WiFi works too but is harder during install)

---

## License

D-PlaneOS is licensed under the [GNU Affero General Public License v3.0 (AGPLv3)](https://www.gnu.org/licenses/agpl-3.0.html).
You can use, modify, and distribute it freely. If you run a modified version
as a network service, you must make your modified source available to users
of that service. AGPLv3 is OSI-approved — NixOS treats it as free software
and no special configuration is required.

---

## Part 1: Installing NixOS (~20 minutes)

### Step 1.1 — Download ISO

Go to **https://nixos.org/download** and download the **Minimal ISO image** (64-bit). Not the Graphical ISO — we don't need a desktop.

### Step 1.2 — Create USB Stick

**Windows:** Use [Rufus](https://rufus.ie/) or [balenaEtcher](https://etcher.balena.io/)
**Mac/Linux:**
```bash
# Find your USB stick (CAREFUL: pick the right device!)
lsblk

# Write the ISO (replace /dev/sdX with your USB stick)
sudo dd if=nixos-minimal-*.iso of=/dev/sdX bs=4M status=progress
```

### Step 1.3 — Boot from USB

1. Plug USB stick into the NAS server
2. Start the server, enter BIOS (usually F2, F12, or DEL during boot)
3. Boot order: set USB stick first
4. Save and reboot

You'll land on a command line: `[nixos@nixos:~]$` — this is the NixOS live installer.

### Step 1.4 — Check Internet

```bash
ping -c 3 google.com
```

If it works → continue. If not:

```bash
# WiFi (if needed):
sudo systemctl start wpa_supplicant
wpa_cli
> add_network
> set_network 0 ssid "YourWiFiName"
> set_network 0 psk "YourWiFiPassword"
> enable_network 0
> quit
```

### Step 1.5 — Partition the Boot Disk

**WARNING: This erases EVERYTHING on the selected disk. Make sure you pick the right one — NOT your data disks!**

```bash
# Show all disks
lsblk

# Example: /dev/sda is your boot SSD (120GB)
#          /dev/sdb, /dev/sdc, /dev/sdd are data disks → DON'T TOUCH
```

**For UEFI systems** (most modern servers/PCs since ~2012):

```bash
# Partition
sudo parted /dev/sda -- mklabel gpt
sudo parted /dev/sda -- mkpart ESP fat32 1MB 512MB
sudo parted /dev/sda -- set 1 esp on
sudo parted /dev/sda -- mkpart primary 512MB 100%

# Format
sudo mkfs.fat -F 32 -n BOOT /dev/sda1
sudo mkfs.ext4 -L nixos /dev/sda2

# Mount
sudo mount /dev/disk/by-label/nixos /mnt
sudo mkdir -p /mnt/boot
sudo mount /dev/disk/by-label/BOOT /mnt/boot
```

**For older BIOS/MBR systems:**

```bash
sudo parted /dev/sda -- mklabel msdos
sudo parted /dev/sda -- mkpart primary 1MB 100%

sudo mkfs.ext4 -L nixos /dev/sda1

sudo mount /dev/disk/by-label/nixos /mnt
```

### Step 1.6 — Generate Base Config

```bash
sudo nixos-generate-config --root /mnt
```

This creates two files:
- `/mnt/etc/nixos/hardware-configuration.nix` — auto-detected hardware (NEVER edit manually)
- `/mnt/etc/nixos/configuration.nix` — we'll replace this with D-PlaneOS config

### Step 1.7 — Copy D-PlaneOS Config

Replace the generated `configuration.nix` with ours:

**Option A: Clone the repo:**
```bash
nix-shell -p git --run "git clone https://github.com/4nonX/D-PlaneOS /tmp/dplaneos"
sudo cp /tmp/dplaneos/nixos/configuration-standalone.nix /mnt/etc/nixos/configuration.nix
sudo cp /tmp/dplaneos/nixos/setup-nixos.sh /mnt/root/setup-nixos.sh
```

**Option B: From a second USB stick:**
```bash
lsblk
sudo mount /dev/sdX1 /media
sudo cp /media/configuration-standalone.nix /mnt/etc/nixos/configuration.nix
sudo cp /media/setup-nixos.sh /mnt/root/setup-nixos.sh
sudo umount /media
```

### Step 1.8 — Install

```bash
sudo nixos-install
```

This takes 5-15 minutes (depending on internet speed). At the end you'll be asked for a **root password** — choose a secure one.

```bash
# Done! Reboot.
sudo reboot
```

**Remove the USB stick!** The server now boots from the boot disk into your new NixOS.

---

## Part 2: Setting Up D-PlaneOS (~5 minutes)

### Step 2.1 — Log In

After reboot you'll see a login prompt:

```
User: root
Password: (what you chose during nixos-install)
```

### Step 2.2 — Run Setup Script

```bash
bash /root/setup-nixos.sh
```

The script automatically:
- Generates and applies a unique ZFS host ID
- Confirms or changes the timezone
- Checks the ZFS pool name
- Detects UEFI/BIOS boot loader

### Step 2.3 — Build the System

```bash
sudo nixos-rebuild switch
```

**First build will fail** due to missing package hashes. This is expected!

1. The error shows the correct hash: `got: sha256-AbCdEf...=`
2. Edit the config: `sudo nano /etc/nixos/configuration.nix`
3. Search for `AAAA` (Ctrl+W), replace with the correct hash
4. Rebuild: `sudo nixos-rebuild switch`
5. Repeat if a second hash error appears (vendorHash)

After max 3 attempts, everything builds.

### Step 2.4 — Find Your IP

```bash
ip addr show | grep "inet "
# Look for the address that is NOT 127.0.0.1
# Example: 192.168.1.42
```

### Step 2.5 — Import ZFS Pool

**If you have an existing ZFS pool** (e.g. from TrueNAS/Debian migration):
```bash
zpool import
zpool import tank    # replace "tank" with your pool name
zpool status
```

**If you need to create a new pool:**
```bash
lsblk

# Mirror (2 disks, recommended)
zpool create tank mirror /dev/sdb /dev/sdc

# OR: RAIDZ1 (3+ disks, one can fail)
zpool create tank raidz1 /dev/sdb /dev/sdc /dev/sdd

# Docker dataset
zfs create tank/docker
```

### Step 2.6 — Open Browser

On your regular PC:

```
http://192.168.1.42
```
(Replace with the IP from step 2.4)

Or try:
```
http://dplaneos.local
```

**You should see the D-PlaneOS Setup Wizard!**

---

## Part 3: Daily Operations — The 5 Commands You Need

### Change something
```bash
sudo nano /etc/nixos/configuration.nix
sudo nixos-rebuild switch
```

### Something broke?
```bash
sudo nixos-rebuild switch --rollback
```

### Update the system
```bash
sudo nix-channel --update
sudo nixos-rebuild switch
```

### Reboot
```bash
sudo reboot
```

### Check status
```bash
systemctl status dplaned    # D-PlaneOS daemon
zpool status                 # ZFS pools
docker ps                    # Docker containers
```

---

## Troubleshooting

### "hash mismatch" during nixos-rebuild

The sha256 hashes in the config need to be filled in. See Step 2.3.

### ZFS pool not imported at boot

```bash
sudo zpool import -f tank
# Check that hostId in configuration.nix matches your machine
```

### D-PlaneOS shows blank page

```bash
journalctl -u dplaned -f    # Daemon logs
journalctl -u nginx -f      # Web server logs
```

### SSH not working

The config enables password login by default. If you disabled it:
```bash
# On the server directly:
sudo nano /etc/nixos/configuration.nix
# Change: PasswordAuthentication = false;
# To:     PasswordAuthentication = true;
sudo nixos-rebuild switch
```

### Installing packages

**Not** `apt install` — that doesn't exist on NixOS. Instead:

```bash
# Temporary (current session only):
nix-shell -p vim

# Permanent (survives reboots):
sudo nano /etc/nixos/configuration.nix
# Add to environment.systemPackages: vim
sudo nixos-rebuild switch
```

---

## Cheat Sheet: NixOS vs. Debian

| I want to... | Debian | NixOS |
|--------------|--------|-------|
| Install a package | `apt install vim` | Add to `configuration.nix` + `nixos-rebuild switch` |
| Start a service | `systemctl enable nginx` | `services.nginx.enable = true;` + rebuild |
| Edit config | `nano /etc/nginx/nginx.conf` | `nano /etc/nixos/configuration.nix` + rebuild |
| Update | `apt update && apt upgrade` | `nix-channel --update && nixos-rebuild switch` |
| Rollback | manually fix things | `nixos-rebuild switch --rollback` |
| What packages do I have? | `dpkg -l` | It's all in `configuration.nix` |
| Open firewall port | `ufw allow 8080` | `networking.firewall.allowedTCPPorts = [ 8080 ];` + rebuild |
