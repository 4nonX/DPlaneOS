# DPlaneOS Installer ISO - Offline Capable
# ─────────────────────────────────────────────────────────────────────────────
# Builds a bootable installer ISO that works with NO internet connection.
#
# How offline install works:
#   The ISO contains two NixOS closures in its squashfs nix store:
#     1. The live installer environment (the ISO itself)
#     2. The DPlaneOS target system closure (dplaneos nixosConfiguration)
#   nixos-install copies paths from the local /nix/store on the booted ISO
#   rather than fetching from cache.nixos.org. Zero network required.
#
# ISO size: about 2 GB (zstd compressed), both closures included.
# Build time: 20-40 min on first build, cached on subsequent builds.
#
# Build:   nix build .#iso
# Output:  result/iso/dplaneos-<version>-installer-amd64.iso
# Write:   dd if=result/iso/*.iso of=/dev/sdX bs=4M status=progress conv=fsync
# ─────────────────────────────────────────────────────────────────────────────

{ modulesPath, pkgs, lib, config, self, targetSystem, ... }:

let
  # The DPlaneOS target system closure - baked into the ISO's nix store.
  # This is the exact system that gets installed onto the target disk.
  dplaneosSystem = targetSystem;

in {
  imports = [
    # Minimal bootable ISO - console only, no desktop, no Calamares.
    # Server-appropriate: works over serial, IPMI SOL, KVM-over-IP.
    "${modulesPath}/installer/cd-dvd/installation-cd-minimal.nix"
    # install.sh, the partition layout, the tools (shared with the
    # installation test).
    ./installer-common.nix
  ];

  dplaneos.installer.targetSystem = dplaneosSystem;

  # ── Pre-bake the DPlaneOS target closure into the ISO's nix store ────────
  # Core of offline installation - nixos-install reads from here,
  # never contacts cache.nixos.org or any external binary cache.
  isoImage.storeContents = [
    dplaneosSystem
  ];

  # Set false to keep ISO smaller; installed system fetches updates normally.
  # Set true if you need the installed system to rebuild itself offline.
  isoImage.includeSystemBuildDependencies = false;

  # ── Kernel - match appliance pin exactly ──────────────────────────────────
  # Must stay in sync with applianceConfig in flake.nix. A mismatch causes
  # both kernels to appear in the squashfs, adding ~600 MB to the ISO.
  boot.kernelPackages          = pkgs.linuxPackages_6_12;
  boot.supportedFilesystems    = [ "zfs" "vfat" "ext4" ];
  boot.zfs.package             = pkgs.zfs;
  boot.zfs.forceImportRoot     = false; # new default in 26.11; set explicitly to silence warning

  # ── Disable GPU/graphics stack - NAS installer is always headless ─────────
  # The NixOS minimal ISO profile enables hardware.graphics by default, which
  # pulls intel-compute-runtime, intel-graphics-compiler, intel-media-driver,
  # mesa, and related packages. None of these are needed for a server NAS
  # installer that runs on a serial console or SSH. Disabling them removes
  # ~400 MB from the x86_64 squashfs closure and keeps the ISO within the
  # GitHub Actions runner disk limit.
  hardware.graphics.enable     = lib.mkForce false;

  # Serial console support for headless/IPMI installs
  boot.kernelParams = [ "console=tty0" "console=ttyS0,115200n8" ];

  # ── Auto-login and launch installer ───────────────────────────────────────
  services.getty.autologinUser = lib.mkForce "root";

  programs.bash.loginShellInit = ''
    if [ "$(tty)" = "/dev/tty1" ]; then
      clear
      exec bash /etc/dplaneos-install/install.sh
    fi
  '';

  # ── SSH for remote/headless installs ──────────────────────────────────────
  # Temporary installer password - this system has no sensitive data.
  # Users can SSH to the installer as root with password "dplaneos".
  services.openssh = {
    enable                          = true;
    settings.PermitRootLogin        = "yes";
    settings.PasswordAuthentication = true;
  };
  users.users.root.password = lib.mkForce "dplaneos";
  users.users.root.initialHashedPassword = lib.mkForce null;

  # DHCP for IP detection at the completion screen
  networking.useDHCP = lib.mkForce true;

  # ── ISO metadata ──────────────────────────────────────────────────────────
  image.fileName                = let arch = if pkgs.stdenv.hostPlatform.isAarch64 then "arm64" else "amd64";
                                  in "dplaneos-installer-${arch}.iso";
  isoImage.volumeID             = lib.mkForce "DPLANEOS_INSTALL";
  isoImage.makeEfiBootable      = true;
  isoImage.makeUsbBootable      = true;
  isoImage.squashfsCompression  = "zstd -Xcompression-level 9";
  system.stateVersion = "25.11";
}
