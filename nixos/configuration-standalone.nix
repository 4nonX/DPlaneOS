# DPlaneOS appliance configuration
# ─────────────────────────────────────────────────────────────────────────────
# The system every DPlaneOS installation runs: the installer ISO installs it
# as a ready-built image, the live ISO boots it, and the VM tests boot it.
#
# Nothing in here is specific to one machine. What differs between
# installations is set at runtime, from the web interface, and takes effect
# without rebuilding the system:
#   host name, time zone, time servers, DNS      Settings, Network
#   addresses, VLANs, bonds                       Network
#   open firewall ports                           Firewall
#   users, shares, exports, pools, everything else
#
# A unique ZFS host ID is generated at first boot (module.nix).
#
# To build your own variant, import this file from your configuration and
# override what you need (see nixos/README.md).
# ─────────────────────────────────────────────────────────────────────────────

{ config, lib, pkgs, modulesPath, ... }:

{
  imports = [
    # Drivers for every common storage and network controller in the initrd,
    # and redistributable firmware: the same image boots on any machine.
    (modulesPath + "/profiles/all-hardware.nix")
    (modulesPath + "/installer/scan/not-detected.nix")
  ];

  # ─── Boot ───────────────────────────────────────────────────────────────
  boot.loader.systemd-boot.enable      = lib.mkDefault true;
  boot.loader.efi.canTouchEfiVariables = lib.mkDefault true;

  hardware.cpu.intel.updateMicrocode = lib.mkDefault pkgs.stdenv.hostPlatform.isx86_64;
  hardware.cpu.amd.updateMicrocode   = lib.mkDefault pkgs.stdenv.hostPlatform.isx86_64;

  # KVM for both vendors where the CPU has it (unused otherwise).
  boot.kernelModules = lib.optionals pkgs.stdenv.hostPlatform.isx86_64 [ "kvm-intel" "kvm-amd" ]
    ++ [ "tcp_bbr" ];

  # Intel VA-API for transcoding in containers (Jellyfin, Plex). The packages
  # are x86_64 only; the offline installer image leaves them out (flake.nix).
  hardware.graphics = {
    enable        = true;
    extraPackages = lib.optionals pkgs.stdenv.hostPlatform.isx86_64
      [ pkgs.intel-media-driver pkgs.intel-compute-runtime ];
  };

  # ─── ZFS ────────────────────────────────────────────────────────────────
  boot.supportedFilesystems = [ "zfs" ];
  services.zfs.autoScrub = { enable = true; interval = "monthly"; };
  services.zfs.trim.enable = true;

  # ─── Network ────────────────────────────────────────────────────────────
  # systemd-networkd with DHCP on every wired interface until the web
  # interface configures one (its files take precedence, see the Network
  # page). No interface name is assumed.
  networking.useNetworkd = true;
  networking.useDHCP     = lib.mkDefault true;
  systemd.network.enable = true;
  # DNS servers set in the web interface are a systemd-resolved drop-in.
  services.resolved.enable = true;
  # One interface with an address is enough to finish booting.
  systemd.network.wait-online.anyInterface = true;
  systemd.network.wait-online.timeout = 30;

  # The host name is set at runtime (Settings; "dplaneos" until then) and
  # kept in /etc/hostname, so NixOS must not own it.
  # (Priority below mkDefault: the VM test framework names its machines.)
  networking.hostName = lib.mkOverride 1100 "";

  # The time zone is set at runtime as well (UTC until then).
  time.timeZone      = lib.mkDefault null;
  i18n.defaultLocale = lib.mkDefault "en_US.UTF-8";
  services.timesyncd.enable = lib.mkDefault true;

  # mDNS: the system answers as <host name>.local.
  services.avahi = {
    enable   = true;
    nssmdns4 = true;
    publish  = { enable = true; addresses = true; workstation = true; };
  };

  # ─── System disk ────────────────────────────────────────────────────────
  services.fstrim = { enable = true; interval = "weekly"; };
  zramSwap = { enable = true; memoryPercent = 10; };
  services.journald.extraConfig = lib.mkDefault ''
    Storage=persistent
    Compress=yes
    MaxRetentionSec=1month
    MaxFileSec=1day
    SyncIntervalSec=5m
    SystemMaxUse=512M
  '';

  # Container logs go to the journal (bounded above) instead of growing as
  # JSON files on the system disk.
  virtualisation.docker.daemon.settings.log-driver = "journald";

  # ─── DPlaneOS ───────────────────────────────────────────────────────────
  services.dplaneos = {
    enable       = true;
    openFirewall = true;
    samba.enable = true;
    nfs.enable   = true;
    # MinIO is part of the image: the S3 page switches it on.
    s3.enable    = true;
  };

  # ─── Kernel settings for file serving ───────────────────────────────────
  # Defaults; vm.swappiness and the ZFS ARC limit can be changed under
  # Settings.
  boot.kernel.sysctl = {
    "fs.inotify.max_user_watches"   = 524288;
    "fs.inotify.max_user_instances" = 512;
    "fs.inotify.max_queued_events"  = 32768;
    "vm.swappiness"                 = 10;
    "vm.vfs_cache_pressure"         = 50;
    "vm.dirty_background_ratio"     = 5;
    "vm.dirty_ratio"                = 15;
    "net.core.rmem_max"             = 134217728;
    "net.core.wmem_max"             = 134217728;
    "net.ipv4.tcp_rmem"             = "4096 87380 67108864";
    "net.ipv4.tcp_wmem"             = "4096 65536 67108864";
    "net.ipv4.tcp_congestion_control" = "bbr";
  };

  # ─── Tools for the console and the web terminal ─────────────────────────
  environment.systemPackages = with pkgs; [
    vim htop git wget curl lsof iotop
    smartmontools hdparm nvme-cli lm_sensors
    pciutils usbutils nettools ethtool tcpdump
  ];

  # Do not change after installation: it selects NixOS's data migrations.
  system.stateVersion = "25.11";
}
