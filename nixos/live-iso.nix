# D-PlaneOS Live Boot ISO Configuration
# ─────────────────────────────────────────────────────────────────────────────
# Turns the live system (configuration-live.nix, flake output dplaneos-live)
# into a bootable ISO: flake.nix extends that configuration with this module,
# so the ISO boots D-PlaneOS itself (daemon, web interface, database).
#
# Differs from installer.nix:
#   - installer.nix: boots the installer, which installs the D-PlaneOS system
#     kept in its store and reboots into it
#   - live-iso.nix: is the D-PlaneOS system, running from the ISO in RAM
#
# Until 16.0.1 this module built a plain NixOS installer CD with the live
# system only copied into its store: the ISO booted NixOS without D-PlaneOS.

{ config, lib, pkgs, modulesPath, ... }:

{
  imports = [
    "${modulesPath}/installer/cd-dvd/iso-image.nix"
  ];

  # Drivers for real machines (disk controllers, network cards), as on the
  # installer ISO.
  hardware.enableAllHardware = true;

  isoImage.makeEfiBootable = true;
  isoImage.makeUsbBootable = true;

  # The root is the ISO's: a tmpfs over the squashfs store. /persist stays a
  # tmpfs (live-persistence.nix; the state drive is mounted separately).
  fileSystems = lib.mkImageMediaOverride (config.lib.isoFileSystems // {
    "/persist" = {
      device = "tmpfs";
      fsType = "tmpfs";
      neededForBoot = true;
      options = [ "size=2G" "mode=755" ];
    };
  });
  swapDevices = lib.mkImageMediaOverride [ ];
  boot.initrd.luks.devices = lib.mkImageMediaOverride { };

  # The ISO brings its own boot loader; the live system installs none.
  boot.loader.systemd-boot.enable = lib.mkForce false;

  # Serial console for headless/IPMI machines
  boot.kernelParams = [ "console=tty0" "console=ttyS0,115200n8" ];

  # ── Disable graphics (headless server appliance) ───────────────────────────
  # Keeps ~400 MB of GPU drivers (intel-media-driver, intel-compute-runtime)
  # out of the squashfs, within the GitHub Actions disk limit.
  hardware.graphics.enable = lib.mkForce false;

  # ── System packages: tools useful in the live environment ──────────────────
  environment.systemPackages = [
    # Pool and filesystem management
    pkgs.zfs
    pkgs.util-linux
    pkgs.e2fsprogs

    # System utilities
    pkgs.curl
    pkgs.wget
    pkgs.git
    pkgs.jq
    pkgs.htop
    pkgs.tmux
    pkgs.vim
    pkgs.nano

    # Network utilities
    pkgs.iproute2
    pkgs.inetutils
    pkgs.netcat
    pkgs.nmap

    # Debugging / monitoring
    pkgs.lshw
    pkgs.pciutils
    pkgs.dmidecode
    pkgs.ethtool
  ];

  # ── Console: log in as root and show the welcome text ──────────────────────
  services.getty.autologinUser = lib.mkForce "root";

  environment.etc."dplaneos-live/welcome.txt".text = ''

    ╔═══════════════════════════════════════════════════════════════════════════╗
    ║                                                                           ║
    ║                    Welcome to D-PlaneOS Live Boot                         ║
    ║                                                                           ║
    ╚═══════════════════════════════════════════════════════════════════════════╝

    This is a live environment. D-PlaneOS is running in RAM.
    Existing ZFS pools will be automatically imported from your drives.

    ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━

    🌐  WEB INTERFACE
        Open http://<this-machine-ip>/ in your browser
        (Find your IP: ip addr show | grep "inet ")

    🖥️  COMMAND LINE
        zpool list                  # List ZFS pools
        zfs list                    # List datasets
        docker ps                   # List containers
        journalctl -u dplaned -f    # Follow daemon logs

    💾  PERSISTENCE (Optional)
        System state is ephemeral by default (lost on shutdown).
        To keep the daemon's database, settings and network configuration:
          1. Format a drive as ext4 labelled "dplane-persist"
          2. Attach it and reboot: the state is kept there from then on

    🛠️  TROUBLESHOOTING
        Check ZFS auto-import: journalctl -u dplane-zfs-auto-import
        Check daemon status: systemctl status dplaned
        Check docker status: systemctl status docker

    ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━

  '';

  programs.bash.loginShellInit = ''
    if [ "$(tty)" = "/dev/tty1" ] && [ -z "$DPLANE_WELCOME_SHOWN" ]; then
      clear
      cat /etc/dplaneos-live/welcome.txt
      export DPLANE_WELCOME_SHOWN=1
    fi
  '';

  # Documentation disabled (appliance)
  documentation.nixos.enable = false;

  networking.hostName = lib.mkDefault "dplaneos-live";

  # ── System information file ──────────────────────────────────────────────────
  environment.etc."dplaneos-live/info.json".text = builtins.toJSON {
    mode = "live-boot";
    version = lib.fileContents ../VERSION;
    kernel = config.boot.kernelPackages.kernel.version;
    zfs = config.boot.zfs.package.version;
  };
}
