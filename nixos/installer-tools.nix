# nixos/installer-tools.nix
# -----------------------------------------------------------------------------
# What the installer needs, apart from the ISO itself: its tools, install.sh,
# the partition layout and the path of the system it installs. Used by the
# installer ISO (installer.nix) and by the installer's VM test
# (tests/installer.nix), so the test runs exactly what the ISO runs.
#
# targetSystem: the system closure that is installed; diskoScripts: the
# partitioning scripts per system partition size (flake.nix installerDisko).
# Both are module arguments.
# -----------------------------------------------------------------------------
{ lib, pkgs, targetSystem, diskoScripts, ... }:

{
  environment.systemPackages = with pkgs; [
    gum                                          # TUI wizard
    disko                                        # declarative partitioning
    (python311.withPackages (ps: [ ps.bcrypt ])) # password hashing (one python: two collided on bin/python3)
    parted gptfdisk util-linux e2fsprogs         # disk tools
    iproute2 inetutils                           # network / IP detection
    pciutils lshw                                # hardware enumeration
    jq bc git
  ];

  # python3.11's HTML docs do not build in nixpkgs 26.05 (Sphinx 9.1 with
  # docutils 0.22.4); documentation outputs would pull them in with python311.
  documentation.doc.enable = false;
  documentation.nixos.enable = false;
  environment.extraOutputsToInstall = lib.mkForce [ ];

  environment.etc = {
    "dplaneos-install/install.sh".source  = ./install.sh;
    "dplaneos-install/install.sh".mode    = "0755";
    "dplaneos-install/disko.nix".source   = ./disko.nix;
    "dplaneos-install/disko.nix".mode     = "0644";
    # Built with the ISO: partitioning needs no Nix evaluation offline.
    "dplaneos-install/disko-40G".source = diskoScripts."40G";
    "dplaneos-install/disko-24G".source = diskoScripts."24G";
    # Tell install.sh where the pre-built closure lives in the nix store
    "dplaneos-install/system-path".text   = targetSystem.outPath;
    "dplaneos-install/system-path".mode   = "0644";
  };
}
