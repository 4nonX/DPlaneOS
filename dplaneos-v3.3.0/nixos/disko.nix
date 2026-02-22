# D-PlaneOS — disko Partition Layout
# ─────────────────────────────────────────────────────────────────────────────
# A/B system partition layout with a shared /persist partition.
#
# Hardware profile:
#   Boot disk: Patriot P300 128 GB NVMe  →  /dev/nvme0n1
#   Data disks: 4× SATA HDDs (WD/Seagate) — NOT touched here; managed by ZFS.
#
# Partition table (GPT on /dev/nvme0n1):
#
#   ┌─────────────────────────────────────────────────────────────────────┐
#   │  Part │ Size    │ Label      │ FS    │ Mount         │ Purpose       │
#   ├─────────────────────────────────────────────────────────────────────┤
#   │  1    │ 512 MB  │ ESP        │ FAT32 │ /boot         │ UEFI ESP      │
#   │  2    │ 20 GB   │ system-a   │ ext4  │ /             │ Active slot   │
#   │  3    │ 20 GB   │ system-b   │ ext4  │ /mnt/system-b │ Standby slot  │
#   │  4    │ ~78 GB  │ persist    │ ext4  │ /persist      │ Survives OTA  │
#   └─────────────────────────────────────────────────────────────────────┘
#
# Key properties:
#   - system-a and system-b are identical in size. OTA writes to the
#     inactive slot, then flips the bootloader to boot from it next time.
#   - /persist is NEVER wiped. It holds the D-PlaneOS SQLite DB, logs,
#     SSH host keys, and all daemon state. See impermanence.nix.
#   - /boot is the EFI System Partition shared between both slots.
#   - The remaining ~78 GB goes entirely to /persist — on a 128 GB NVMe
#     that is ample for logs, the daemon DB, and ZFS cache state.
#   - ZFS data disks (4× SATA HDDs) are separate physical disks.
#     They are NEVER touched by this file.
#
# Usage:
#   nix run github:nix-community/disko -- --mode disko /etc/nixos/disko.nix
#   WARNING: This wipes /dev/nvme0n1. Run ONLY during initial install.
#
# To override the disk device (e.g. if NVMe appears as nvme1n1):
#   disko.devices.disk.os.device = "/dev/nvme1n1";
# ─────────────────────────────────────────────────────────────────────────────

{ ... }:

{
  disko.devices = {

    disk.os = {
      # ── OS boot disk (Patriot P300 128 GB NVMe) ───────────────────────────
      # On the Gigabyte H610I with this drive in the M.2_1 slot, the kernel
      # assigns /dev/nvme0n1. Verify with: lsblk -d -o NAME,MODEL,SIZE
      device = "/dev/nvme0n1";
      type   = "disk";

      content = {
        type = "gpt";

        partitions = {

          # ── Partition 1: EFI System Partition ────────────────────────────
          ESP = {
            label   = "ESP";
            size    = "512M";
            type    = "EF00";   # EFI System Partition (GPT type GUID)
            content = {
              type       = "filesystem";
              format     = "vfat";
              mountpoint = "/boot";
              mountOptions = [ "defaults" "umask=0077" ];
            };
          };

          # ── Partition 2: System Slot A (active by default) ────────────────
          # The installed NixOS system lives here after first boot.
          # OTA updates write the new generation to slot B, then flip boot.
          # 20 GB is comfortable for a NixOS closure + Nix store entries.
          system-a = {
            label   = "system-a";
            size    = "20G";
            content = {
              type       = "filesystem";
              format     = "ext4";
              mountpoint = "/";
              mountOptions = [
                "defaults"
                "noatime"          # no access-time writes — reduces NVMe wear
                "errors=remount-ro"
              ];
              extraArgs = [
                "-I" "256"   # large inode for NixOS extended attributes
                "-m" "1"     # 1% reserved blocks (default 5% wastes space)
                "-L" "system-a"
              ];
            };
          };

          # ── Partition 3: System Slot B (OTA standby) ─────────────────────
          # Identical geometry to slot A. Not mounted during normal operation.
          # The OTA update script mounts it at /mnt/system-b when writing.
          system-b = {
            label   = "system-b";
            size    = "20G";
            content = {
              type       = "filesystem";
              format     = "ext4";
              mountpoint = "/mnt/system-b";
              mountOptions = [
                "defaults"
                "noatime"
                "errors=remount-ro"
                "noauto"           # do NOT mount automatically at boot
              ];
              extraArgs = [ "-I" "256" "-m" "1" "-L" "system-b" ];
            };
          };

          # ── Partition 4: Persistent State ────────────────────────────────
          # This partition SURVIVES every reboot and every OTA update.
          # All writable daemon state lives here.
          #
          # Contents (managed by impermanence.nix + daemon):
          #   /persist/dplaneos/          — daemon state root
          #   /persist/dplaneos/dplaneos.db — main SQLite DB
          #   /persist/dplaneos/audit.db   — audit log DB
          #   /persist/dplaneos/audit.key  — HMAC key
          #   /persist/dplaneos/gitops/    — state.yaml repo checkout
          #   /persist/nixos/              — NixOS machine config overrides
          #   /persist/log/                — rotated logs
          #   /persist/ssh/                — host SSH keys (survive OTA)
          #
          # Size: 100% = all remaining space (~78 GB on 128 GB NVMe after
          # ESP + two 20 GB slots). More than sufficient for logs and DB.
          persist = {
            label   = "persist";
            size    = "100%";   # use all remaining NVMe space
            content = {
              type       = "filesystem";
              format     = "ext4";
              mountpoint = "/persist";
              mountOptions = [
                "defaults"
                "noatime"
                "errors=remount-ro"
              ];
              extraArgs = [ "-I" "256" "-L" "persist" ];
            };
          };

        }; # partitions
      }; # content
    }; # disk.os

  }; # disko.devices
}
