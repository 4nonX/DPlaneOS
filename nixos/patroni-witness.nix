# D-PlaneOS Patroni/etcd Witness Node
#
# This is a minimal NixOS configuration intended for a 3rd "witness" node
# (e.g., a Raspberry Pi or small MicroVM) which is required purely to
# maintain etcd quorum in the event of a network partition between the
# two primary D-PlaneOS nodes.
#
# It runs absolutely nothing else—no ZFS, no daemon, no Docker.

{ config, lib, pkgs, ... }:

{
  options.services.dplaneos.ha.witness = {
    enable = lib.mkEnableOption "D-PlaneOS Patroni Witness Node";

    localAddress = lib.mkOption {
      type = lib.types.str;
      description = "IP address of this witness node.";
    };

    nodeAAddress = lib.mkOption {
      type = lib.types.str;
      description = "IP address of D-PlaneOS Node A.";
    };

    nodeBAddress = lib.mkOption {
      type = lib.types.str;
      description = "IP address of D-PlaneOS Node B.";
    };
  };

  config = lib.mkIf config.services.dplaneos.ha.witness.enable {
    # Essential packages
    environment.systemPackages = with pkgs; [
      etcd
      coreutils
      bash
      curl
    ];

    # Minimal SSH configuration
    services.openssh = {
      enable = true;
      settings.PasswordAuthentication = false;
      settings.PermitRootLogin = "prohibit-password";
    };

    # Etcd service (Witness node)
    services.etcd = let 
      cfg = config.services.dplaneos.ha.witness;
    in {
      enable = true;
      name = "etcd-witness";
      listenClientUrls = [ "http://0.0.0.0:2379" ];
      listenPeerUrls = [ "http://0.0.0.0:2380" ];
      advertiseClientUrls = [ "http://${cfg.localAddress}:2379" ];
      initialAdvertisePeerUrls = [ "http://${cfg.localAddress}:2380" ];
      
      # The cluster MUST exactly match the definitions on Node A and Node B
      initialCluster = [
        "etcd-${cfg.nodeAAddress}=http://${cfg.nodeAAddress}:2380"
        "etcd-${cfg.nodeBAddress}=http://${cfg.nodeBAddress}:2380"
        "etcd-witness=http://${cfg.localAddress}:2380"
      ];
      initialClusterState = "new";
    };

    # Firewall (Port 22, Port 2379/2380 for etcd)
    networking.firewall.allowedTCPPorts = [ 22 2379 2380 ];
  };
}
