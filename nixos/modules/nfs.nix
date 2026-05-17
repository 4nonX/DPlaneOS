# nixos/modules/nfs.nix
# -----------------------------------------------------------------------------
# DPlaneOS NFSv4 Integration Module
#
# ARCHITECTURE PROBLEM THIS SOLVES:
#
#   Before: configuration.nix contained a single line:
#               services.nfs.server.enable = true;
#           This gives NFSv3-only service with no idmapping, no ACL tools,
#           and no minimum-version enforcement.
#
#   After:  This module owns the NFS server configuration. It enables NFSv4.2,
#           configures rpc.idmapd for user/group name mapping, installs
#           nfs4-acl-tools so the daemon can call nfs4_getfacl / nfs4_setfacl,
#           and enforces nfs4_disable_idmapping=0 via sysctl.
#
# TWO-LAYER SPLIT:
#
#   NixOS layer (this file) owns:
#     - services.nfs.server with NFSv4.2 and optional version floor
#     - rpc.idmapd configuration (/etc/idmapd.conf)
#     - sysctl fs.nfs.nfs4_disable_idmapping = 0
#     - nfs4-acl-tools in system packages and dplaned PATH
#     - Firewall ports (TCP/UDP 2049, TCP/UDP 111)
#
#   DPlaneOS daemon layer owns:
#     - /etc/exports contents (written by the NFS handler on every export change)
#     - Per-export options (clients, access mode, squash settings)
#
# USAGE:
#   In configuration.nix:
#     imports = [
#       ./modules/nfs.nix
#     ];
#     services.dplaneos.nfs = {
#       enable = true;
#       nfs4Domain = "nas.example.com";
#     };
#
#   Or with defaults (domain = "localdomain", minimum NFSv4.2):
#     services.dplaneos.nfs.enable = true;
# -----------------------------------------------------------------------------

{ config, lib, pkgs, ... }:

let
  cfg = config.services.dplaneos.nfs;

  # Minimum-version enforcement block for /etc/nfs.conf [nfsd] section.
  # NFSv4.0 and NFSv3 are disabled when minVersion is 4.1 or 4.2.
  # NFSv4.0 is additionally disabled when minVersion is 4.2.
  minVersionConfig = lib.concatLines (
    [ "vers4=y" "vers4.1=y" "vers4.2=y" ]
    ++ lib.optional (cfg.minVersion == "4.1" || cfg.minVersion == "4.2") "vers3=n"
    ++ lib.optional (cfg.minVersion == "4.2") "vers4.0=n"
  );
in {

  # ── Option declarations ───────────────────────────────────────────────────

  options.services.dplaneos.nfs = {

    enable = lib.mkEnableOption "DPlaneOS NFSv4 server with idmapping and ACL support";

    nfs4Domain = lib.mkOption {
      type    = lib.types.str;
      default = "localdomain";
      description = ''
        NFSv4 ID mapping domain written to /etc/idmapd.conf (Domain=).
        Set this to your DNS domain (e.g. "nas.example.com") so that
        user@domain strings in NFSv4 OWNER attributes resolve correctly
        across Linux and macOS clients.
      '';
      example = "nas.example.com";
    };

    minVersion = lib.mkOption {
      type    = lib.types.enum [ "4" "4.1" "4.2" ];
      default = "4.2";
      description = ''
        Minimum NFS protocol version the server will offer.
        "4.2" (default): only NFSv4.1 and NFSv4.2, enables server-side copy
          and sparse file support.
        "4.1": NFSv4.1 and 4.2; disables NFSv3 and NFSv4.0.
        "4":   all NFSv4 minor versions; NFSv3 is still disabled.
        Legacy NFSv3 clients should set this to "4" and add NFSv3 firewall
        rules manually.
      '';
    };

    openFirewall = lib.mkOption {
      type    = lib.types.bool;
      default = true;
      description = ''
        Open NFS firewall ports: TCP/UDP 2049 (nfsd) and TCP/UDP 111 (portmap/rpcbind).
        Disable if you manage firewall rules externally.
      '';
    };

  };

  # ── Implementation ────────────────────────────────────────────────────────

  config = lib.mkIf cfg.enable {

    # ── NFS kernel server ─────────────────────────────────────────────────

    services.nfs.server = {
      enable = true;
      # Append NFSv4 version flags to /etc/nfs.conf [nfsd] section.
      # NixOS merges this with any existing nfsd config.
      extraNfsdConfig = minVersionConfig;
    };

    # ── ID mapping: kernel must perform name<->uid translation ───────────
    # Setting nfs4_disable_idmapping=0 tells the kernel to pass uid/gid
    # as user@domain strings rather than raw integers. Required for
    # correct OWNER@ display in NFSv4 ACLs and for macOS compatibility.
    boot.kernel.sysctl."fs.nfs.nfs4_disable_idmapping" = 0;

    # ── idmapd configuration ──────────────────────────────────────────────
    # rpc.idmapd translates uid/gid <-> user@domain for NFSv4 transports.
    # The Domain here must match the client's /etc/idmapd.conf Domain.
    environment.etc."idmapd.conf".text = ''
      [General]
      Verbosity = 0
      Domain = ${cfg.nfs4Domain}

      [Mapping]
      Nobody-User  = nobody
      Nobody-Group = nogroup

      [Translation]
      Method = nsswitch
    '';

    # ── ACL tools ─────────────────────────────────────────────────────────
    # nfs4-acl-tools provides nfs4_getfacl and nfs4_setfacl.
    # Added to both systemPackages (shell access) and dplaned PATH (daemon exec).
    environment.systemPackages = [ pkgs.nfs4-acl-tools ];

    systemd.services.dplaned.path = lib.mkAfter [ pkgs.nfs4-acl-tools ];

    # ── Firewall ──────────────────────────────────────────────────────────
    networking.firewall = lib.mkIf cfg.openFirewall {
      allowedTCPPorts = [ 2049 111 ];
      allowedUDPPorts = [ 2049 111 ];
    };

  };
}
