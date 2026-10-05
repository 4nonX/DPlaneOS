# D-PlaneOS Live Boot: Persistence Layer
# ─────────────────────────────────────────────────────────────────────────────
# Configure ephemeral root with optional persistence to external storage.
#
# By default:
#   - Root (/) is tmpfs: everything lost on shutdown
#   - System state (/var) is tmpfs
#   - Logs (/var/log) are tmpfs
#   - Tmp (/tmp) is tmpfs
#
# Optional persistence:
#   - Mount USB drive labeled "dplane-persist" if present
#   - Link daemon state to USB for survival across reboots
#   - Gracefully degrade to ephemeral if USB not present
#
# Implementation uses nix-community/impermanence module for declarative
# definition of what's ephemeral vs. persistent.

{ config, lib, pkgs, ... }:

{
  # ── Root filesystem: ephemeral tmpfs for live boot ───────────────────────
  # Live boot runs entirely in RAM. Root is tmpfs (or overlay on squashfs).
  fileSystems."/" = {
    fsType = "tmpfs";
    options = [ "size=50%" ];
  };

  # ── Ephemeral /persist directory (tmpfs for daemon state) ──────────────────
  # Live boot always uses tmpfs for /persist (no persistent partition).
  # Optional USB storage can be mounted at /mnt/usb-persist for daemon state.
  fileSystems."/persist" = {
    fsType = "tmpfs";
    neededForBoot = true;
    options = [
      "size=2G"          # Limit tmpfs to 2GB (sufficient for daemon state)
      "mode=755"         # Standard permissions
    ];
  };

  # ── Optional: Mount external USB if present ──────────────────────────────
  # If a USB drive labeled "dplane-persist" is connected, mount it.
  # If not present, system still boots (tmpfs fallback).
  # Device discovery via udev label.
  fileSystems."/mnt/usb-persist" = lib.mkIf true {
    device = "/dev/disk/by-label/dplane-persist";
    fsType = "ext4";

    # Don't fail boot if USB not present
    options = [
      "nofail"
      "x-systemd.device-timeout=5"  # 5s timeout, then continue
      "x-systemd.automount"
    ];
  };

  # ── Declare persistent directories (impermanence) ────────────────────────
  # These directories are preserved either in tmpfs (ephemeral) or
  # linked to USB (persistent if mounted).

  environment.persistence."/persist" = {
    # Directories that must be persistent across sessions
    directories = [
      # User/group database (impermanence requires this for stable UIDs/GIDs)
      # Without this, users assigned ephemeral IDs will be reassigned on reboot
      "/var/lib/nixos"

      # System state (created by services at boot)
      "/var/cache"
      "/var/log"

      # D-PlaneOS daemon state (conditional: only if USB present)
      # See dplane-link-persist.service below
    ];

    # Critical files
    files = [
      # Machine identity (systemd requires this as a FILE, not a directory)
      "/etc/machine-id"

      # SSH host keys (if user wants to preserve identity)
      # Note: /etc/ssh is typically read-only from squashfs;
      # this is a fallback for future install-to-disk scenarios
      # "/etc/ssh/ssh_host_rsa_key"
      # "/etc/ssh/ssh_host_ed25519_key"
    ];
  };

  # ── Systemd service: Link daemon state to USB if present ────────────────
  # On boot, check if USB persist mount succeeded.
  # If so, bind /var/lib/dplaneos to it for daemon state preservation.

  systemd.services.dplane-link-persist = {
    description = "Link D-PlaneOS daemon state to persistent USB storage (if available)";
    # mountpoint: without it the USB check always failed (command not found
    # reads as "not mounted") and the state was never linked.
    path = [ pkgs.util-linux ];

    # Run after:
    #   - ZFS auto-import (pools ready)
    #   - USB mount timeout (know if USB is present)
    # Run before:
    #   - Daemon startup
    after = [
      "dplane-zfs-auto-import.service"
      "mnt-usb-persist.mount"  # systemd generates this from fileSystems entry
    ];

    before = [
      "dplaned.service"
    ];

    wantedBy = [ "multi-user.target" ];

    serviceConfig = {
      Type = "oneshot";
      RemainAfterExit = true;
      User = "root";
      Group = "root";
      StandardOutput = "journal";
      StandardError = "journal";
    };

    script = ''
      set -e
      export PATH=${lib.makeBinPath [ pkgs.coreutils pkgs.util-linux ]}:$PATH

      echo "[dplane-link-persist] Checking for USB persistence mount..."

      # Check if USB mount exists and is actually mounted
      if [ -d /mnt/usb-persist ] && mountpoint -q /mnt/usb-persist; then
        echo "✓ USB persist mount detected at /mnt/usb-persist"

        # Create state directory on USB
        mkdir -p /mnt/usb-persist/dplaneos-state
        mkdir -p /mnt/usb-persist/dplaneos-state/pgsql
        mkdir -p /mnt/usb-persist/dplaneos-state/docker

        # Link daemon state to USB
        # Note: daemon service must be configured to use this symlink
        ln -sfn /mnt/usb-persist/dplaneos-state /var/lib/dplaneos-persist

        echo "✓ Daemon state linked to USB for persistence"

        # Mark that persistence is available
        touch /run/dplane-persist-available

      else
        echo "⚠ USB persist mount not available (using tmpfs ephemeral)"
        echo "  To enable persistence, mount USB labeled 'dplane-persist'"
        touch /run/dplane-persist-ephemeral
      fi
    '';
  };

  # ── Mark system as live boot (useful for debugging/monitoring) ──────────
  environment.etc."dplaneos-live/mode".text = "live-boot";
  environment.etc."dplaneos-live/persist-status".source = "/run/dplane-persist-available";

  # ── Ensure daemon service respects persistence setup ──────────────────
  # If USB is mounted, daemon should use /var/lib/dplaneos-persist
  # Otherwise, use tmpfs /var/lib/dplaneos (lost on shutdown)
  #
  # This is handled by daemon configuration in applianceConfig (flake.nix),
  # with fallback to /persist/var/lib/dplaneos for tmpfs case.

  systemd.services.dplaned = lib.mkIf (config.services.dplaneos.enable or false) {
    # Ensure persistence link is created before daemon starts
    after = [ "dplane-link-persist.service" ];
  };
}
