# nixos/witness-image-x86.nix
# DPlaneOS Witness disk images for x86_64 (UEFI): a raw image to write to a
# USB stick or SSD of a mini PC, and a qcow2 image for VMs (Proxmox, libvirt,
# any hypervisor). The root file system grows to the disk on first boot.
#   nix build .#witness-image-x86_64    raw
#   nix build .#witness-qcow2-x86_64    qcow2
# To join without a screen, put dplaneos-witness.txt on the EFI partition
# (FAT, label ESP) before the first boot; see witness.nix.
{ config, lib, pkgs, modulesPath, ... }:
let
  image = format: import "${modulesPath}/../lib/make-disk-image.nix" {
    inherit lib config pkgs format;
    name = "dplaneos-witness-x86_64";
    partitionTableType = "efi";
    diskSize = "auto";
    additionalSpace = "1024M";
  };
in
{
  imports = [
    ./witness.nix
    "${modulesPath}/profiles/qemu-guest.nix"
  ];
  boot.loader.systemd-boot.enable = true;
  boot.loader.efi.canTouchEfiVariables = false;
  boot.loader.grub.enable = false;
  boot.growPartition = true;
  boot.initrd.availableKernelModules = [ "ahci" "xhci_pci" "nvme" "usb_storage" "uas" "sd_mod" "sdhci_pci" ];
  fileSystems."/" = { device = "/dev/disk/by-label/nixos"; fsType = "ext4"; autoResize = true; };
  fileSystems."/boot" = { device = "/dev/disk/by-label/ESP"; fsType = "vfat"; };

  system.build.witnessRaw = image "raw";
  system.build.witnessQcow2 = image "qcow2-compressed";
}
