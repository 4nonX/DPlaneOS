# What a machine needs to run the DPlaneOS installer (install.sh): the
# system to install, the partition layout, and the tools. Used by the
# installer ISO (installer.nix) and by the installation test
# (tests/install.nix), so that the test runs what the ISO runs.
{ pkgs, lib, config, ... }:

{
  options.dplaneos.installer.targetSystem = lib.mkOption {
    type = lib.types.package;
    description = ''
      The system install.sh puts on the disk (config.system.build.toplevel
      of the appliance configuration). It must be in this machine's Nix
      store: the installation copies it from there, without any network.
    '';
  };

  options.dplaneos.installer.diskoScript = lib.mkOption {
    type = lib.types.package;
    description = ''
      The script that partitions, formats and mounts the system disk
      (config.system.build.diskoScript of the system to install, built from
      disko.nix). It is prepared here, when the installer is built: at
      installation time nothing is evaluated or built, only the placeholder
      device in it is replaced by the chosen disk.
    '';
  };

  config = {
    environment.systemPackages = with pkgs; [
      gum                                          # the installer's dialogs
      (python311.withPackages (ps: [ ps.bcrypt ])) # password rules and hash
      parted gptfdisk util-linux e2fsprogs dosfstools
      iproute2 inetutils
      pciutils lshw
      jq bc git
    ];

    environment.etc = {
      "dplaneos-install/install.sh".source = ./install.sh;
      "dplaneos-install/install.sh".mode   = "0755";
      "dplaneos-install/disko-script".source = config.dplaneos.installer.diskoScript;
      # Where the system to install is in this machine's Nix store.
      "dplaneos-install/system-path".text  = config.dplaneos.installer.targetSystem.outPath;
      "dplaneos-install/system-path".mode  = "0644";
    };

    # ── Documentation - suppress broken python3.11 doc build ─────────────────
    # nixpkgs 26.05 ships Sphinx 9.1 + docutils 0.22.4 which fail to build
    # python3.11 HTML docs. NixOS defaults documentation.doc.enable = true,
    # which causes system-path to pull the .doc output for every systemPackage,
    # including python311. Disabling doc outputs entirely prevents the broken
    # derivation from entering the installer closure.
    documentation.doc.enable    = false;
    documentation.nixos.enable  = false;
    environment.extraOutputsToInstall = lib.mkForce [];

    # Offline: nothing is ever fetched. nixos-install copies the system from
    # this machine's own store.
    nix.settings = {
      experimental-features = [ "nix-command" "flakes" ];
      substituters          = lib.mkForce [];
      trusted-substituters  = lib.mkForce [];
    };
  };
}
