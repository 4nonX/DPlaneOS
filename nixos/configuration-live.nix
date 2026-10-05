# D-PlaneOS Live Boot Configuration
# ─────────────────────────────────────────────────────────────────────────────
# Boots directly into D-PlaneOS without requiring installation to disk.
# System runs from RAM with optional USB persistence.
#
# Key differences from installed system:
#   - Root filesystem: tmpfs (ephemeral)
#   - Persistence: optional USB mount
#   - ZFS pools: auto-imported from attached drives
#   - OTA updates: disabled (no persistent /boot)
#   - Daemon state: lives in RAM or optional USB
#
# Boot flow:
#   1. ISO boots with read-only squashfs root
#   2. impermanence overlays /var, /etc with tmpfs
#   3. dplane-zfs-auto-import scans and imports any pools
#   4. D-PlaneOS daemon starts and serves the UI via nginx on port 80
#   5. User can manage pools, run Docker, optionally install to disk

{ config, lib, pkgs, ... }:

{
  imports = [
    # Base configuration (shared with installed system)
    ./configuration-standalone.nix
    ./module.nix

    # Live-specific additions
    ./live-zfs-auto-import.nix
    ./live-persistence.nix

    # System generation scripts (shared)
    ./ota-module.nix
    ./dplane-generated.nix
  ];

  # ── Disable features not applicable to live boot ───────────────────────────

  # OTA updates require persistent /boot partition. Live boot has no persistent storage.
  services.dplaneos.ota.enable = lib.mkForce false;

  # ── Journald configuration for live boot ─────────────────────────────────
  # Live boot has /var as tmpfs, so persistent journal storage doesn't work.
  # Override to use volatile storage so journald is functional in live environment.
  services.journald.extraConfig = lib.mkForce ''
    Storage=volatile
    Compress=yes
    MaxRetentionSec=1day
    SyncIntervalSec=5m
    SystemMaxUse=128M
    RuntimeMaxUse=128M
  '';

  # Ensure /run/log/journal directory exists with correct permissions before journald starts
  systemd.tmpfiles.rules = [
    "d /run/log 0755 root root -"
    "d /run/log/journal 0755 root root -"
    "d /run/log/journal-remote 0755 systemd-journal-remote systemd-journal 0"
  ];

  # Boot loader is not applicable to live ISO (boots from GRUB/bootloader already)
  boot.loader.grub.enable = false;
  # Use mkForce to override applianceConfig's true (live ISO doesn't use EFI)
  boot.loader.efi.canTouchEfiVariables = lib.mkForce false;

  # ── Kernel and ZFS configuration (match installed system) ──────────────────
  # Use mkDefault so applianceConfig can set this without conflict
  boot.kernelPackages = lib.mkDefault pkgs.linuxPackages_6_12;
  boot.zfs.package = lib.mkDefault pkgs.zfs;
  boot.supportedFilesystems = [ "zfs" "vfat" "ext4" ];

  # ARC cache: 17GB (same as installed, for consistent behavior)
  # Use mkDefault so applianceConfig can set this without conflict
  boot.kernelParams = lib.mkDefault [ "zfs.zfs_arc_max=17179869184" ];

  # ── Network configuration (live boot requires DHCP for automatic connectivity) ────────────────────────────
  networking.useNetworkd = true;
  # Use mkForce to override standalone's false (live boot needs DHCP to get network automatically)
  networking.useDHCP = lib.mkForce true;

  # ── PostgreSQL for daemon state (ephemeral in live boot) ───────────────────
  # module.nix runs the local database (services.dplaneos.database.createLocally,
  # socket DSN, peer auth). In live boot it lives on the tmpfs root and is
  # recreated on each boot; only memory settings are tuned here.
  services.postgresql.settings = {
    max_connections = 50;  # Reduced for low-memory VM
    shared_buffers = "64MB";
    effective_cache_size = "256MB";
  };

  # ── D-PlaneOS daemon and frontend ──────────────────────────────────────────
  # (Provided by applianceConfig in flake.nix, same as installed system)
  # services.dplaneos.daemonPackage = ...
  # services.dplaneos.frontendPackage = ...

  # ── Assertions (same as installed system) ────────────────────────────────
  assertions = [
    {
      assertion = lib.versionAtLeast config.boot.kernelPackages.kernel.version "6.12";
      message = "D-PlaneOS requires Linux kernel >= 6.12 LTS.";
    }
    {
      assertion = lib.versionAtLeast config.boot.zfs.package.version "2.3";
      message = "D-PlaneOS requires OpenZFS >= 2.3 (LTS branch).";
    }
  ];

  # ── System information in MOTD ───────────────────────────────────────────
  services.getty.helpLine = ''

    Welcome to D-PlaneOS Live Boot
    ──────────────────────────────────────────────────────────────────────

    Web UI:              http://<ip-address>/
    SSH:                 root@<ip-address>

    ZFS pools will be automatically imported from attached drives.

    Commands:
      zpool list              # List pools
      zfs list                # List datasets
      docker ps               # List containers

    System state is ephemeral (lost on shutdown).
    To persist daemon config, mount USB labeled "dplane-persist".

    Type 'exit' to return to login prompt.
  '';

  # ── SSH for remote access to live environment ───────────────────────────
  # Live boot allows password auth for convenience; override module.nix's false
  services.openssh = {
    enable = true;
    settings = {
      PermitRootLogin = lib.mkForce "yes";
      PasswordAuthentication = lib.mkForce true;
      UsePAM = true;
    };
  };

  # ── System packages (minimal, reuse from standalone) ────────────────────
  # Additional tools useful in live environment are added in live-iso.nix

  # ── No automatic garbage collection in live (tmpfs-based) ──────────────
  # Use mkForce to override applianceConfig's true (tmpfs limited, GC would thrash)
  nix.gc.automatic = lib.mkForce false;

  # ── Documentation disabled (appliance, live environment) ────────────────
  documentation.nixos.enable = false;

  # ── System state: use impermanence for ephemeral root ───────────────────
  # See live-persistence.nix for details on /persist mount

  # ── Create required directories and files for sandboxed services ─────────
  # The daemon uses ProtectSystem=strict with ReadWritePaths that require
  # all listed directories/files to exist. In ephemeral live boot with tmpfs
  # root, these aren't created automatically. Build them during activation.
  system.activationScripts.ensureEtcDirs = ''
    # Create all ReadWritePaths directories from module.nix
    mkdir -p /var/log/dplaneos
    mkdir -p /var/lib/dplaneos
    mkdir -p /opt/dplaneos
    mkdir -p /etc/dplaneos
    mkdir -p /run/dplaneos
    mkdir -p /etc/cron.d
    mkdir -p /etc/systemd/system
    mkdir -p /etc/systemd/network
    mkdir -p /etc/systemd/resolved.conf.d

    # Create required /etc files (empty is fine)
    touch /etc/crontab
    touch /etc/exports
  '';

  # ── Ensure services start after ZFS auto-import ───────────────────────
  systemd.services.dplaned = lib.mkIf (config.services.dplaneos.enable or false) {
    after = [ "dplane-zfs-auto-import.service" ];
    serviceConfig = {
      # Relax security hardening in ephemeral live environment
      ProtectSystem = lib.mkForce "off";
      ProtectHome = lib.mkForce false;
      NoNewPrivileges = lib.mkForce false;
    };
  };

  systemd.services.docker = lib.mkIf config.virtualisation.docker.enable {
    after = [ "dplane-zfs-auto-import.service" ];
  };
}
