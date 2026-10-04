# D-PlaneOS Live Boot ISO Configuration
# ─────────────────────────────────────────────────────────────────────────────
# Builds a bootable ISO that runs D-PlaneOS directly without installation.
#
# Differs from installer.nix:
#   - installer.nix: boots installer, runs install.sh, reboots to installed system
#   - live-iso.nix: boots directly into live D-PlaneOS system
#
# Both ISOs contain the full D-PlaneOS closure (for offline use).
# The difference is what runs at boot:
#   - Installer: TUI wizard for partitioning/installation
#   - Live boot: D-PlaneOS daemon and UI (no installation required)

{ config, lib, pkgs, modulesPath, self, targetSystem, ... }:

{
  imports = [
    # Base NixOS minimal ISO
    "${modulesPath}/installer/cd-dvd/installation-cd-minimal.nix"
  ];

  # ── Pre-bake the D-PlaneOS live closure into the ISO ──────────────────────
  # This allows the ISO to boot and run D-PlaneOS offline (no internet needed).
  isoImage.storeContents = [
    targetSystem  # The live D-PlaneOS system (passed via flake.nix)
  ];

  # Don't include build dependencies (keeps ISO size under 4GB)
  isoImage.includeSystemBuildDependencies = false;

  # ── Kernel and ZFS (match applianceConfig) ───────────────────────────────
  boot.kernelPackages = pkgs.linuxPackages_6_12;
  boot.supportedFilesystems = [ "zfs" "vfat" "ext4" ];
  boot.zfs.package = pkgs.zfs;
  boot.zfs.forceImportRoot = false;

  # Serial console for headless/IPMI installations
  boot.kernelParams = [ "console=tty0" "console=ttyS0,115200n8" ];

  # ── Disable graphics (headless server appliance) ───────────────────────────
  # NixOS minimal ISO enables hardware.graphics by default, pulling ~400MB of
  # Intel-specific packages (intel-media-driver, intel-compute-runtime, etc.)
  # that are unnecessary for a headless NAS live environment.
  # Removing them keeps the squashfs under the GitHub Actions disk limit.
  hardware.graphics.enable = lib.mkForce false;

  # ── System packages: tools useful for live environment ──────────────────────
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

    # Optional: Install-to-disk tools (future feature)
    # pkgs.python3
    # pkgs.disko
    # pkgs.gum
  ];

  # ── Boot behavior: Auto-login and display welcome message ────────────────
  # On first TTY, display welcome message instead of login prompt.
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
        journalctl -u dplaneos -f   # Follow daemon logs

    💾  PERSISTENCE (Optional)
        System state is ephemeral by default (lost on shutdown).
        To persist daemon configuration and Docker state:
          1. Mount USB drive labeled "dplane-persist"
          2. Daemon state will link to USB automatically on boot

    🛠️  TROUBLESHOOTING
        Check ZFS auto-import: journalctl -u dplane-zfs-auto-import
        Check daemon status: systemctl status dplaneos
        Check docker status: systemctl status docker

    📖  DOCUMENTATION
        See /etc/dplaneos-live/README for more information.

    ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━

    Type 'help' for common commands, or 'exit' to return to login.

  '';

  # ── Login shell initialization ───────────────────────────────────────────────
  # Display welcome message and status on first login
  programs.bash.loginShellInit = ''
    if [ "$(tty)" = "/dev/tty1" ] && [ -z "$DPLANE_WELCOME_SHOWN" ]; then
      clear
      cat /etc/dplaneos-live/welcome.txt

      echo ""
      echo "System startup in progress... Services should be ready in ~30 seconds."
      echo "Press Enter to dismiss this message."
      read -r

      export DPLANE_WELCOME_SHOWN=1
    fi
  '';

  # ── SSH for remote access ────────────────────────────────────────────────────
  services.openssh = {
    enable = true;
    settings = {
      PermitRootLogin = "yes";
      PasswordAuthentication = true;
      PubkeyAuthentication = true;
      UsePAM = true;
    };
    # Allow empty password for live boot (no persistent users)
    # User can set password interactively if needed
  };

  # ── Disable unneeded services in ISO environment ────────────────────────────
  services.rsyslogd.enable = false;  # Excessive disk writes to squashfs
  services.udisks2.enable = false;  # Not useful in live ISO
  services.ntp.enable = false;      # Can use systemd-timesyncd instead

  # ── Networking ───────────────────────────────────────────────────────────────
  # Live ISO should support DHCP out of the box
  networking.useDHCP = lib.mkForce true;
  networking.useNetworkd = true;

  # ── Nix configuration ────────────────────────────────────────────────────────
  # Disable GC in live (tmpfs-based)
  nix.gc.automatic = false;

  # Documentation disabled (appliance)
  documentation.nixos.enable = false;

  # ── Additional live-boot-specific configuration ──────────────────────────────

  # Set a meaningful hostname in live boot
  networking.hostName = lib.mkDefault "dplaneos-live";

  # ── System information file ──────────────────────────────────────────────────
  environment.etc."dplaneos-live/info.json".text = builtins.toJSON {
    mode = "live-boot";
    version = builtins.readFile ../VERSION;
    kernel = config.boot.kernelPackages.kernel.version;
    zfs = config.boot.zfs.package.version;
  };
}
