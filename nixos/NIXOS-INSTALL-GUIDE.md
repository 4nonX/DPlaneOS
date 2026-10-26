# DPlaneOS: Installation Guide for NixOS Beginners

> **Audience**: you have never used NixOS. You want a NAS.
> This guide takes you from an empty server to a running DPlaneOS, step by step. You do not need to know NixOS for it: the installer ISO installs the complete system. The last part explains the few NixOS ideas you will meet afterwards.
>
> The short version for experienced users: [Installation Guide](../docs/admin/INSTALLATION-GUIDE.md).

---

## What is NixOS (30-second version)

NixOS is a Linux distribution where the **whole system is built from a description**. DPlaneOS ships that description (the release); your machine adds its hardware configuration. Every build is kept as a **generation**: if an update goes wrong, the previous generation is still there, in the boot menu and one command away.

**Why this is good for a NAS:**
- A broken update rolls back by itself (the system checks itself after the update's reboot).
- A failed system disk: reinstall, import the ZFS pool, restore the daily configuration backup from the pool.
- The configuration can live in Git, if you want (see [Git-Driven NAS](../docs/admin/GITOPS-DRIVEN-NAS.md)).

---

## Requirements

- A PC or server for the NAS: 64-bit (x86_64, or aarch64 with UEFI firmware), 4 GB RAM minimum, 8 GB or more recommended
- **UEFI** firmware (legacy BIOS / CSM is not supported)
- A **system disk** of 32 GB or more (SSD or NVMe recommended): DPlaneOS goes here, and it is erased
- Your data disks (for the ZFS pool: the installer does not touch them)
- A USB stick of 4 GB or more
- A second computer, and an Ethernet cable (the installer needs no internet, but you reach the web interface over the network)

---

## Part 1: Install (about 15 minutes)

### Step 1.1: Download the ISO

From the [latest release](https://github.com/4nonX/DPlaneOS/releases/latest), download `dplaneos-v<version>-installer-amd64.iso` (Intel/AMD) or `dplaneos-v<version>-installer-arm64.iso` (ARM), and its `.sha256` file. Check it:

```bash
ISO=dplaneos-v16.0.2-installer-amd64.iso   # the file you downloaded (…-arm64.iso on ARM)
sha256sum -c "$ISO.sha256"
```

### Step 1.2: Write it to the USB stick

**Windows:** [Rufus](https://rufus.ie/) (DD image mode) or [balenaEtcher](https://etcher.balena.io/).
**Mac/Linux:**
```bash
lsblk                          # find the USB stick (CAREFUL: pick the right device!)
sudo dd if="$ISO" of=/dev/sdX bs=4M status=progress conv=fsync
```

### Step 1.3: Boot from the USB stick

1. Plug the USB stick into the server.
2. Start it and enter the firmware setup (usually F2, F12 or DEL). Make sure **UEFI** boot is on and CSM / legacy boot is off.
3. Boot from the USB stick.

The installer starts by itself, on the screen and on the serial console. A headless server: SSH to the installer as `root` with the password `dplaneos` (this password exists only on the installer).

### Step 1.4: Install

1. Choose **Install DPlaneOS**.
2. Choose the **system disk**. Only disks of 32 GB or more are offered, never the USB stick. Everything on the chosen disk is erased; data disks are not touched.
3. Set the password of the `admin` user (8 characters or more, with upper and lower case, a digit and a symbol).
4. Confirm. The installer partitions the disk and installs the system from the ISO, without internet.
5. When it is done, it shows the address of the web interface. Remove the USB stick and press Enter to reboot.

---

## Part 2: First steps in the web interface

### Step 2.1: Open the web interface

On your other computer, open the address the installer showed (for example `http://192.168.1.42`), or try `http://dplaneos.local`. If you do not know the address, look in your router's DHCP leases.

Log in as `admin` with the password from the installer. The setup wizard opens:

1. **Admin account**: log in again with the same password.
2. **Disks** and **pool**: select data disks and create a pool (or skip it and do it later under Storage › ZFS Pools).
3. **System**: hostname and time zone.

### Step 2.2: An existing ZFS pool

A pool from another system (TrueNAS, another Linux): Storage › ZFS Pools › Import. A pool that was not exported on the old system needs "force" (it was last used by another host).

### Step 2.3: SSH access (optional)

The installed system allows SSH with keys only (no password login, no root login). Add your public key for a user under Identity › SSH Keys, then:

```bash
ssh <user>@<server-ip>
```

---

## Part 3: The NixOS ideas you will meet

### Updates

System › System Updates: check, update. The system builds the next release, reboots into it and checks itself; if the check fails, it goes back to the previous generation. See [Updates](../docs/admin/OTA-UPDATES.md).

### Something broke after an update?

```bash
sudo nixos-rebuild switch --rollback    # the previous generation
```

If the system does not boot, choose an older generation in the boot menu.

### Settings of your own

The system is built from `/etc/nixos/flake.nix`, which DPlaneOS writes. Your own NixOS settings go in `/etc/nixos/local.nix`; see [NIXOS-README.md](NIXOS-README.md#your-own-nixos-settings). Most settings (network, shares, users, firewall rules) are made in the web interface and need no NixOS at all.

### Installing a program

There is no `apt install`. For a quick tool:

```bash
nix-shell -p htop      # available in this shell only
```

To keep it, add it to `/etc/nixos/local.nix` (`environment.systemPackages = [ pkgs.htop ];`) and rebuild:

```bash
sudo nixos-rebuild switch --flake /etc/nixos#dplaneos --impure
```

### Check status

```bash
systemctl status dplaned     # DPlaneOS daemon
zpool status                 # ZFS pools
docker ps                    # containers
sudo dplaneos-recovery       # recovery menu (passwords, 2FA, services, pools)
```

---

## Troubleshooting

### The installer says the machine started in legacy BIOS mode

Enable UEFI in the firmware setup (disable CSM / legacy boot) and boot the USB stick again.

### No disk is offered

The system disk must have 32 GB or more. Check the cables and the firmware's SATA/NVMe settings, then choose **Shell** and run `lsblk`.

### The web interface does not answer

```bash
journalctl -u dplaned -n 50    # daemon log
journalctl -u nginx -n 50      # web server log
```

More: [Troubleshooting](../docs/admin/TROUBLESHOOTING.md), [Recovery](../docs/admin/RECOVERY.md).

---

## Cheat sheet: NixOS vs. Debian

| I want to... | Debian | DPlaneOS (NixOS) |
|--------------|--------|-------|
| Install a package | `apt install vim` | `local.nix` + rebuild (or `nix-shell -p vim` for now) |
| Update | `apt update && apt upgrade` | System › System Updates |
| Roll back | fix things by hand | automatic after a failed update; `nixos-rebuild switch --rollback` |
| Open a firewall port | `ufw allow 8080` | Security › Firewall |
| Change the network | edit `/etc/network/interfaces` | Network page (reverts unless confirmed) |
