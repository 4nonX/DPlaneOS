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

  config = {
    environment.systemPackages = with pkgs; [
      gum                                          # the installer's dialogs
      disko                                        # partitioning
      (python311.withPackages (ps: [ ps.bcrypt ])) # password rules and hash
      parted gptfdisk util-linux e2fsprogs dosfstools
      iproute2 inetutils
      pciutils lshw
      jq bc git
    ];

    environment.etc = {
      "dplaneos-install/install.sh".source = ./install.sh;
      "dplaneos-install/install.sh".mode   = "0755";
      "dplaneos-install/disko.nix".source  = ./disko.nix;
      "dplaneos-install/disko.nix".mode    = "0644";
      # Where the system to install is in this machine's Nix store.
      "dplaneos-install/system-path".text  = config.dplaneos.installer.targetSystem.outPath;
      "dplaneos-install/system-path".mode  = "0644";
    };

    # Offline: nothing is ever fetched. nixos-install copies the system from
    # this machine's own store.
    nix.settings = {
      experimental-features = [ "nix-command" "flakes" ];
      substituters          = lib.mkForce [];
      trusted-substituters  = lib.mkForce [];
    };
  };
}
