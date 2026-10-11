# DPlaneOS Installation Guide

DPlaneOS is installed from an ISO onto a disk of its own. The installer asks two things, the disk and the administrator password, and needs no network.

---

## Requirements

| | Minimum | Recommended |
|---|---|---|
| CPU | 64-bit, x86_64 or aarch64 | four cores or more |
| Firmware | UEFI | |
| RAM | 8 GB | 16 GB or more (ZFS uses free memory as cache) |
| System disk | 32 GB, separate from the data disks | SSD, 64 GB or more |
| Data disks | one | two or more, for redundancy |
| Network | wired Ethernet | |

**The system disk is erased completely.** The data disks are not touched by the installer or by any later update.

**ECC memory** is strongly recommended for ZFS: ZFS protects data on disk, not data that is damaged in memory before it is written. The dashboard says so when it finds none; see [Non-ECC RAM Warning](../hardware/NON-ECC-WARNING.md).

**Platform.** The installed system is NixOS with the kernel and OpenZFS of the pinned NixOS release; you do not need to know NixOS to run it. The installer ISO is the supported way to run DPlaneOS ([why](../reference/NIXOS-RATIONALE.md)).

---

## 1. Download

From the [latest release](https://github.com/4nonX/DPlaneOS/releases/latest):

| Hardware | File |
|---|---|
| x86_64 (Intel/AMD) | `dplaneos-v<version>-installer-amd64.iso` |
| aarch64 with UEFI firmware (ARM servers, VMs) | `dplaneos-v<version>-installer-arm64.iso` |

Check the download (the `.sha256` file is next to the ISO), and its signature if you want to be sure where it comes from:

```bash
sha256sum -c dplaneos-v*-installer-amd64.iso.sha256
cosign verify-blob --bundle dplaneos-v*-installer-amd64.iso.bundle dplaneos-v*-installer-amd64.iso
```

To try DPlaneOS without installing, use the live ISO instead: [Live Boot Quick Start](LIVE-BOOT-QUICKSTART.md).

## 2. Write it to a USB stick

```bash
# Linux / macOS: replace /dev/sdX with the stick (check with lsblk first)
sudo dd if=dplaneos-v*-installer-amd64.iso of=/dev/sdX bs=4M status=progress conv=fsync
```

On Windows, use [Rufus](https://rufus.ie) in DD image mode.

## 3. Install

1. Start the machine from the stick, in UEFI mode.
2. The installer's menu appears. Choose **Install DPlaneOS**.
3. Choose the system disk. Disks of 32 GB or more that are not in use are offered, each with what it holds now (empty, a ZFS pool by name, other file systems). A disk that belongs to a ZFS pool is only accepted after you type the name of that pool: installing on it destroys the pool.
4. Set the password of the administrator (`admin`): at least 8 characters, with an upper-case and a lower-case letter, a digit and a special character.
5. Confirm. The disk is partitioned (boot, system, persistent state) and the system is copied from the stick; this takes a few minutes. Answering *No*, or leaving with Ctrl-C, returns to the menu and writes nothing.
6. Remove the stick and restart when asked.

The other entries of the menu: **Reset the administrator password of an installed system** (see [Recovery](RECOVERY.md)), **Allow remote access to this installer (SSH)**, **Shell**, **Reboot**.

**Without a screen.** The installer also runs on the serial console (115200 baud). To install over the network, choose *Allow remote access to this installer (SSH)* at the console: SSH is started with a password made up for that session and shown on the screen; sign in as `root` and the installer starts. Remote access is off until you ask for it.

## 4. First sign-in

The system gets its address from DHCP; the installer shows it at the end, and the system's console shows it after the start. Open

```
https://<address>/
```

and sign in as `admin` with the password you gave the installer. The certificate is self-signed until you install one (Certificates), so the browser warns once. `http://` works too.

No DHCP on the network: at the console, sign in as `admin` and run `sudo dplaneos-console-net` to set an address, then make it permanent under Network.

Then, in this order:

1. **Storage › Pools:** create a pool from the data disks, or import an existing one.
2. **Datasets and Shares:** a dataset for each kind of data, shared over SMB or NFS.
3. **Users:** every user of the web interface is also the SMB and FTP account of the same name and password.
4. **System › Settings:** host name, time zone, time servers.
5. **Alerts:** an e-mail address, a webhook or Telegram, so that a failed disk does not go unnoticed.
6. **Snapshots** and, if you have a second system, **Replication**.

---

## Updates

**System › Updates** finds new releases, or takes an update file you upload (for systems without internet access). An update is installed next to the running version, started once on trial after a restart, and kept only if the system comes up healthy; otherwise the system returns to the previous version by itself. Update files are signed, and a system only installs what is signed with the release key it was installed with. See [System Updates](OTA-UPDATES.md).

## Reinstalling, and moving the disks

Reinstalling erases the system disk, and with it the settings, users and shares (they are in the system's database). The pools on the data disks are untouched: import them under **Storage › Pools** afterwards. To keep the configuration across a reinstall, see [Recovery, section 6](RECOVERY.md#6-the-database-and-the-configuration).

## If something goes wrong

| | |
|---|---|
| The installer says the machine was not started in UEFI mode | Switch the firmware to UEFI boot (disable CSM / legacy boot). The installed system starts through a UEFI boot loader |
| No disk is offered | The disk is smaller than 32 GB, or in use (mounted, or the stick itself) |
| The installer does not appear | At the console prompt: `bash /etc/dplaneos-install/install.sh` |
| The web interface does not answer after the first start | [Recovery, section 3](RECOVERY.md#3-the-web-interface-does-not-answer) |
| The password given to the installer does not work | Reset it from the stick: [Recovery, section 1](RECOVERY.md#1-lost-administrator-password) |

More: [Troubleshooting](TROUBLESHOOTING.md), [Recovery](RECOVERY.md).
