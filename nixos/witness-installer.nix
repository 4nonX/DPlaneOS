# DPlaneOS Witness Node Installer ISO
# ─────────────────────────────────────────────────────────────────────────────
# Minimal bootable ISO for the etcd quorum witness node.
#
# The witness runs ONLY etcd - no ZFS, no DPlaneOS daemon, no Docker.
# Any machine with 512 MB RAM, 4 GB disk, and LAN access to both primary
# nodes qualifies: an x86 mini PC, a spare VM, or a Raspberry Pi 4.
#
# Unlike the main installer, this ISO does NOT bake in a pre-configured system
# closure. Etcd cluster formation requires IP addresses known only at
# deployment time. The ISO instead bundles a setup wizard (witness-setup.sh)
# that collects the three node IPs and installs NixOS with the correct
# configuration from the network.
#
# Network access is required during install - the witness is not air-gapped.
#
# Build:   nix build .#iso-witness
# Output:  result/iso/dplaneos-witness-installer-amd64.iso
# Write:   dd if=result/iso/*.iso of=/dev/sdX bs=4M status=progress conv=fsync
# ─────────────────────────────────────────────────────────────────────────────

{ modulesPath, pkgs, lib, ... }:

{
  imports = [
    "${modulesPath}/installer/cd-dvd/installation-cd-minimal.nix"
  ];

  environment.systemPackages = with pkgs; [
    gum
    etcd
    parted
    gptfdisk
    util-linux
    e2fsprogs
    iproute2
    inetutils
    curl
    jq
  ];

  boot.kernelPackages    = pkgs.linuxPackages_6_6;
  boot.kernelParams      = [ "console=tty0" "console=ttyS0,115200n8" ];
  networking.useDHCP     = lib.mkForce true;

  services.getty.autologinUser = lib.mkForce "root";

  environment.etc = {
    "dplaneos-witness/witness-setup.sh" = {
      source = ./witness-setup.sh;
      mode   = "0755";
    };
    "dplaneos-witness/patroni-witness.nix" = {
      source = ./patroni-witness.nix;
      mode   = "0644";
    };
  };

  programs.bash.loginShellInit = ''
    if [ "$(tty)" = "/dev/tty1" ]; then
      clear
      exec bash /etc/dplaneos-witness/witness-setup.sh
    fi
  '';

  # Temporary installer password - no sensitive data on this live ISO.
  services.openssh = {
    enable                          = true;
    settings.PermitRootLogin        = "yes";
    settings.PasswordAuthentication = true;
  };
  users.users.root.password             = lib.mkForce "dplaneos";
  users.users.root.initialHashedPassword = lib.mkForce null;

  nix.settings = {
    experimental-features = [ "nix-command" "flakes" ];
    trusted-public-keys   = [
      "cache.nixos.org-1:6NCHdD59X431o0gWypbMrAURkbJ16ZPMQFGspcDShjY="
    ];
  };

  image.fileName               = let arch = if pkgs.stdenv.hostPlatform.isAarch64 then "arm64" else "amd64";
                                 in "dplaneos-witness-installer-${arch}.iso";
  isoImage.volumeID            = lib.mkForce "DPLANEOS_WITNESS";
  isoImage.makeEfiBootable     = true;
  isoImage.makeUsbBootable     = true;
  isoImage.squashfsCompression = "zstd -Xcompression-level 9";
  system.stateVersion          = "25.11";
}
