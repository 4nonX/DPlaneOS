# nixos/witness-image-pi.nix
# DPlaneOS Witness SD card image for Raspberry Pi 3/4 (and other aarch64
# boards the generic NixOS SD image supports).
#   nix build .#witness-sd-aarch64
# Flash with Raspberry Pi Imager ("Use custom") or dd. To join without a
# screen, put dplaneos-witness.txt on the FIRMWARE partition before booting:
#   node=https://nas1.lan
#   code=dpq_...
#   mode=qdevice        (or: voter)
{ modulesPath, ... }:
{
  imports = [
    "${modulesPath}/installer/sd-card/sd-image-aarch64.nix"
    ./witness.nix
  ];
  sdImage.compressImage = true;
}
