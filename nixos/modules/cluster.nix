# nixos/modules/cluster.nix
# ─────────────────────────────────────────────────────────────────────────────
# Cluster quorum: Corosync votequorum and the third vote (Design 0001 phase 3a,
# ADR-0004, ADR-0009).
#
# Nothing is configured here by hand. dplaned writes the cluster definition
# (System > High Availability) to /var/lib/dplaneos/corosync and starts these
# units; each one only runs once its file exists:
#
#   dplaneos-corosync  corosync.conf written     (this node is in a cluster)
#   dplaneos-qdevice   qdevice-enabled written   (the cluster has a third vote)
#   dplaneos-qnetd     qnetd-enabled written     (this node IS a third vote for
#                                                  other clusters)
#
# /etc/corosync/{corosync.conf,authkey,qdevice,qnetd} are symlinks into the
# state directory, so the configuration and the certificate stores survive
# reboots on an ephemeral root and the upstream tools use their default paths.
# ─────────────────────────────────────────────────────────────────────────────
{ config, lib, pkgs, ... }:

let
  cfg   = config.services.dplaneos;
  state = "/var/lib/dplaneos/corosync";
  tools = [ pkgs.corosync pkgs.corosync-qdevice pkgs.nss.tools pkgs.bash pkgs.coreutils ];
  run   = bin: "${pkgs.bash}/bin/bash -c 'exec ${bin}'";
in {
  options.services.dplaneos.cluster.openFirewall = lib.mkOption {
    type        = lib.types.bool;
    default     = true;
    description = ''
      Open UDP 5405 (Corosync between cluster nodes) and TCP 5403 (third vote,
      used only while this node serves as one). Corosync traffic is
      authenticated and encrypted with the cluster key.
    '';
  };

  config = lib.mkIf cfg.enable {
    environment.systemPackages = [ pkgs.corosync pkgs.corosync-qdevice ];

    users.users.coroqnetd = { isSystemUser = true; group = "coroqnetd"; };
    users.groups.coroqnetd = { };

    systemd.tmpfiles.rules = [
      "d ${state}          0700 root      root      -"
      "d ${state}/qdevice  0700 root      root      -"
      "d ${state}/qnetd    0770 coroqnetd coroqnetd -"
      "d ${state}/xfer     0711 root      root      -"
      "d /etc/corosync     0755 root      root      -"
      "L+ /etc/corosync/corosync.conf - - - - ${state}/corosync.conf"
      "L+ /etc/corosync/authkey       - - - - ${state}/authkey"
      "L+ /etc/corosync/qdevice       - - - - ${state}/qdevice"
      "L+ /etc/corosync/qnetd         - - - - ${state}/qnetd"
    ];

    systemd.services.dplaneos-corosync = {
      description = "Corosync cluster engine (DPlaneOS cluster quorum)";
      after       = [ "network-online.target" ];
      wants       = [ "network-online.target" ];
      wantedBy    = [ "multi-user.target" ];
      path        = tools;
      unitConfig.ConditionPathExists = "${state}/corosync.conf";
      serviceConfig = {
        Type           = "simple";
        ExecStart      = run "corosync -f";
        Restart        = "on-failure";
        RestartSec     = "2s";
        StateDirectory = "corosync";
      };
    };

    systemd.services.dplaneos-qdevice = {
      description = "Corosync third-vote client (DPlaneOS)";
      requires    = [ "dplaneos-corosync.service" ];
      after       = [ "dplaneos-corosync.service" ];
      wantedBy    = [ "multi-user.target" ];
      path        = tools;
      unitConfig.ConditionPathExists = "${state}/qdevice-enabled";
      serviceConfig = {
        Type              = "simple";
        ExecStart         = run "corosync-qdevice -f";
        Restart           = "on-failure";
        RestartSec        = "2s";
        RuntimeDirectory  = "corosync-qdevice";
      };
    };

    systemd.services.dplaneos-qnetd = {
      description = "Corosync third-vote server (DPlaneOS serves as third vote)";
      after       = [ "network-online.target" ];
      wants       = [ "network-online.target" ];
      wantedBy    = [ "multi-user.target" ];
      path        = tools;
      unitConfig.ConditionPathExists = "${state}/qnetd-enabled";
      serviceConfig = {
        Type                 = "simple";
        ExecStart            = run "corosync-qnetd -f";
        Restart              = "on-failure";
        RestartSec           = "2s";
        User                 = "coroqnetd";
        Group                = "coroqnetd";
        RuntimeDirectory     = "corosync-qnetd";
        RuntimeDirectoryMode = "0770";
      };
    };

    # The daemon runs corosync-quorumtool, corosync-cfgtool and the certificate
    # tools (which call certutil/pk12util from NSS).
    systemd.services.dplaned.path = tools;

    networking.firewall = lib.mkIf cfg.cluster.openFirewall {
      allowedUDPPorts = [ 5405 ];
      allowedTCPPorts = [ 5403 ];
    };
  };
}
