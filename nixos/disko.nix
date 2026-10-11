# DPlaneOS : partition layout of the system disk
# ─────────────────────────────────────────────────────────────────────────────
# The installer (install.sh) writes this layout to the disk chosen for the
# operating system. Data disks are separate and never touched here: they
# belong to the ZFS pools created in the web interface.
#
#   ┌──────┬────────┬─────────┬───────┬───────────┬───────────────────────────┐
#   │ Part │ Size   │ Label   │ FS    │ Mount     │ Holds                     │
#   ├──────┼────────┼─────────┼───────┼───────────┼───────────────────────────┤
#   │ 1    │ 1 GB   │ ESP     │ FAT32 │ /boot     │ boot loader, kernels      │
#   │ 2    │ 20 GB  │ system  │ ext4  │ /         │ the system (Nix store)    │
#   │ 3    │ rest   │ persist │ ext4  │ /persist  │ all state                 │
#   └──────┴────────┴─────────┴───────┴───────────┴───────────────────────────┘
#
#   - The system partition holds the installed versions of the system: the
#     running one and the previous one (a system update installs the new
#     version next to the old and keeps the old to return to, see
#     docs/admin/OTA-UPDATES.md). Nothing on it is state.
#   - /persist holds everything that is: the database, settings, keys, logs,
#     containers (impermanence.nix binds the directories into place).
#   - The smallest disk this fits on is 32 GB (install.sh checks).
#
# The device below is a placeholder; the installer replaces it with the
# chosen disk. The partitions are found by their labels at boot.
# ─────────────────────────────────────────────────────────────────────────────

{ ... }:

{
  disko.devices = {

    disk.os = {
      device = "/dev/nvme0n1";
      type   = "disk";

      content = {
        type = "gpt";

        partitions = {

          # ── EFI System Partition ──────────────────────────────────────────
          # Kernel and initrd of every installed version live here (three at
          # most: running, previous, one on trial).
          ESP = {
            label   = "ESP";
            size    = "1G";
            type    = "EF00";
            content = {
              type       = "filesystem";
              format     = "vfat";
              mountpoint = "/boot";
              mountOptions = [ "defaults" "umask=0077" ];
            };
          };

          # ── System ────────────────────────────────────────────────────────
          system = {
            label   = "system";
            size    = "20G";
            content = {
              type       = "filesystem";
              format     = "ext4";
              mountpoint = "/";
              mountOptions = [
                "defaults"
                "noatime"          # no access-time writes
                "errors=remount-ro"
              ];
              extraArgs = [
                "-I" "256"
                "-m" "1"     # 1% reserved blocks instead of 5%
                "-L" "system"
              ];
            };
          };

          # ── Persistent state ──────────────────────────────────────────────
          persist = {
            label   = "persist";
            size    = "100%";
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
