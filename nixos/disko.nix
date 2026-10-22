# DPlaneOS : disko Partition Layout
# ─────────────────────────────────────────────────────────────────────────────
# A/B system partition layout with a shared /persist partition.
#
# Hardware profile:
#   Boot disk: Patriot P300 128 GB NVMe  →  /dev/nvme0n1
#   Data disks: 4× SATA HDDs (WD/Seagate) : NOT touched here; managed by ZFS.
#
# Partition table (GPT on /dev/nvme0n1):
#
#   ┌─────────────────────────────────────────────────────────────────────┐
#   │  Part │ Size    │ Label      │ FS    │ Mount         │ Purpose       │
#   ├─────────────────────────────────────────────────────────────────────┤
#   │  1    │ 512 MB  │ ESP        │ FAT32 │ /boot         │ UEFI ESP      │
#   │  2    │ 40 GB   │ system-a   │ ext4  │ /             │ Nix store     │
#   │  3    │ ~78 GB  │ persist    │ ext4  │ /persist      │ Daemon state  │
#   └─────────────────────────────────────────────────────────────────────┘
#
# Key properties:
#   - / holds the Nix store and the NixOS generations. An update builds the
#     new release as another generation here and boots it (ota-module.nix);
#     the previous generations stay bootable. (Installs before 15.2 have a
#     second, unused 20 GB partition "system-b" from the former A/B scheme.)
#     SSH host keys, and all daemon state. See impermanence.nix.
#   - /boot is the EFI System Partition (one boot entry per generation).
#   - The remaining ~78 GB goes entirely to /persist : on a 128 GB NVMe
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
            # Labels are DPlaneOS-specific: the partitions are mounted by
            # partlabel, and another disk with a partition labelled "ESP"
            # (common) made /dev/disk/by-partlabel/ESP ambiguous.
            label   = "dplaneos-esp";
            size    = "512M";
            type    = "EF00";   # EFI System Partition (GPT type GUID)
            content = {
              type       = "filesystem";
              format     = "vfat";
              mountpoint = "/boot";
              mountOptions = [ "defaults" "umask=0077" ];
            };
          };

          # ── Partition 2: System (Nix store, generations) ──────────────────
          # Updates add a generation here; 40 GB holds a few generations and
          # the build of the next one.
          system-a = {
            label   = "dplaneos-system";
            size    = "40G";
            content = {
              type       = "filesystem";
              format     = "ext4";
              mountpoint = "/";
              mountOptions = [
                "defaults"
                "noatime"          # no access-time writes : reduces NVMe wear
                "errors=remount-ro"
              ];
              extraArgs = [
                "-I" "256"   # large inode for NixOS extended attributes
                "-m" "1"     # 1% reserved blocks (default 5% wastes space)
                "-L" "dplaneos-system"
              ];
            };
          };

          # ── Partition 3: Persistent State ────────────────────────────────
          # This partition SURVIVES every reboot and every update.
          # All writable daemon state lives here.
          #
          # Contents (managed by impermanence.nix + daemon):
          #   /persist/dplaneos/          : daemon state root
          #   /persist/postgresql/         : PostgreSQL data
          #   /persist/dplaneos/audit.key  : HMAC key
          #   /persist/dplaneos/gitops/    : state.yaml repo checkout
          #   /persist/nixos/              : NixOS machine config overrides
          #   /persist/log/                : rotated logs
          #   /persist/ssh/                : host SSH keys (survive OTA)
          #
          # Size: 100% = all remaining space (~78 GB on 128 GB NVMe after
          # ESP + 40 GB system). More than sufficient for logs and DB.
          persist = {
            label   = "dplaneos-persist";
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
              extraArgs = [ "-I" "256" "-L" "dplaneos-persist" ];
            };
          };

        }; # partitions
      }; # content
    }; # disk.os

  }; # disko.devices
}
