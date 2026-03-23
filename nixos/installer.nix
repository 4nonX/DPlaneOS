# D-PlaneOS Installer ISO — Offline Capable
# ─────────────────────────────────────────────────────────────────────────────
# Builds a bootable installer ISO that works with NO internet connection.
#
# How offline install works:
#   The ISO contains two NixOS closures in its squashfs nix store:
#     1. The live installer environment (the ISO itself)
#     2. The D-PlaneOS target system closure (dplaneos nixosConfiguration)
#   nixos-install copies paths from the local /nix/store on the booted ISO
#   rather than fetching from cache.nixos.org. Zero network required.
#
# ISO size: ~3-5 GB (zstd compressed) due to both closures being baked in.
# Build time: 20-40 min on first build, cached on subsequent builds.
#
# Build:   nix build .#iso
# Output:  result/iso/dplaneos-<version>-installer-amd64.iso
# Write:   dd if=result/iso/*.iso of=/dev/sdX bs=4M status=progress conv=fsync
# ─────────────────────────────────────────────────────────────────────────────

{ modulesPath, pkgs, lib, config, self, ... }:

let
  # The D-PlaneOS target system closure — baked into the ISO's nix store.
  # This is the exact system that gets installed onto the target disk.
  dplaneosSystem = self.nixosConfigurations.dplaneos.config.system.build.toplevel;

in {
  imports = [
    # Minimal bootable ISO — console only, no desktop, no Calamares.
    # Server-appropriate: works over serial, IPMI SOL, KVM-over-IP.
    "${modulesPath}/installer/cd-dvd/installation-cd-minimal.nix"
  ];

  # ── Pre-bake the D-PlaneOS target closure into the ISO's nix store ────────
  # Core of offline installation — nixos-install reads from here,
  # never contacts cache.nixos.org or any external binary cache.
  isoImage.storeContents = [
    dplaneosSystem
  ];

  # Set false to keep ISO smaller; installed system fetches updates normally.
  # Set true if you need the installed system to rebuild itself offline.
  isoImage.includeSystemBuildDependencies = false;

  # ── Installer tools ────────────────────────────────────────────────────────
  environment.systemPackages = with pkgs; [
    gum                                          # TUI wizard
    disko                                        # declarative partitioning
    (python3.withPackages (ps: [ ps.bcrypt ]))   # password hashing
    parted gptfdisk util-linux e2fsprogs         # disk tools
    iproute2 inetutils                           # network / IP detection
    pciutils lshw                                # hardware enumeration
    jq bc git
  ];

  # ── Kernel — match appliance pin exactly ──────────────────────────────────
  boot.kernelPackages          = pkgs.linuxPackages_6_6;
  boot.supportedFilesystems    = [ "zfs" "vfat" "ext4" ];
  boot.zfs.package             = pkgs.zfs;

  # Serial console support for headless/IPMI installs
  boot.kernelParams = [ "console=tty0" "console=ttyS0,115200n8" ];

  # ── Auto-login and launch installer ───────────────────────────────────────
  services.getty.autologinUser = lib.mkForce "root";

  environment.etc = {
    "dplaneos-install/install.sh".source     = ./install.sh;
    "dplaneos-install/install.sh".mode       = "0755";
    "dplaneos-install/disko.nix".source      = ./disko.nix;
    "dplaneos-install/disko.nix".mode       = "0644";
    # Tell install.sh where the pre-built closure lives in the nix store
    "dplaneos-install/system-path".text      = "${dplaneosSystem}";
    "dplaneos-install/system-path".mode      = "0644";
  };

  programs.bash.loginShellInit = ''
    if [ "$(tty)" = "/dev/tty1" ]; then
      clear
      exec bash /etc/dplaneos-install/install.sh
    fi
  '';

  # ── SSH for remote/headless installs ──────────────────────────────────────
  # Temporary installer password — this system has no sensitive data.
  # Users can SSH to the installer as root with password "dplaneos".
  services.openssh = {
    enable                          = true;
    settings.PermitRootLogin        = "yes";
    settings.PasswordAuthentication = true;
  };
  users.users.root.password = lib.mkForce "dplaneos";

  # ── Nix — disable all substituters for true offline operation ─────────────
  nix.settings = {
    experimental-features = [ "nix-command" "flakes" ];
    substituters          = lib.mkForce [];
    trusted-substituters  = lib.mkForce [];
  };

  # DHCP for IP detection at the completion screen
  networking.useDHCP = lib.mkForce true;

  # ── ISO metadata ──────────────────────────────────────────────────────────
  image.fileName               = lib.mkForce "dplaneos-installer-amd64.iso";
  isoImage.volumeID             = lib.mkForce "DPLANEOS_INSTALL";
  isoImage.makeEfiBootable      = true;
  isoImage.makeUsbBootable      = true;
  isoImage.squashfsCompression  = "zstd -Xcompression-level 9";
  system.stateVersion = "25.11";
}
