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
#           configures rpc.idmapd for user/group name mapping
#           and enforces nfs4_disable_idmapping=0 via sysctl.
#
# TWO-LAYER SPLIT:
#
#   NixOS layer (this file) owns:
#     - services.nfs.server with NFSv4.2 and optional version floor
#     - rpc.idmapd configuration (/etc/idmapd.conf)
#     - sysctl fs.nfs.nfs4_disable_idmapping = 0
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

  # NFSv4 version floor for /etc/nfs.conf [nfsd] section.
  # services.nfs.settings uses attrsOf (attrsOf str) - no raw strings.
  # NFSv4.0 and NFSv3 are disabled when minVersion is 4.1 or 4.2.
  nfsdVersionSettings =
    { "vers4" = "y"; "vers4.1" = "y"; "vers4.2" = "y"; }
    // lib.optionalAttrs (cfg.minVersion == "4.1" || cfg.minVersion == "4.2") { "vers3" = "n"; }
    // lib.optionalAttrs (cfg.minVersion == "4.2") { "vers4.0" = "n"; };

in {

  # ── Option declarations ───────────────────────────────────────────────────

  options.services.dplaneos.nfs = {

    enable = lib.mkOption {
      type        = lib.types.bool;
      default     = true;
      description = "Enable DPlaneOS NFSv4 server with idmapping and ACL support. NFS is a core NAS feature and is enabled by default.";
    };

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

    services.nfs.server.enable = true;

    # NFSv4 version floor via services.nfs.settings (replaces the deprecated
    # extraNfsdConfig string interface removed in nixpkgs 26.05).
    services.nfs.settings.nfsd = nfsdVersionSettings;

    # ── ID mapping: kernel must perform name<->uid translation ───────────
    # Setting nfs4_disable_idmapping=0 tells the kernel to pass uid/gid
    # as user@domain strings rather than raw integers. Required for
    # correct OWNER@ display in NFSv4 ACLs and for macOS compatibility.
    boot.kernel.sysctl."fs.nfs.nfs4_disable_idmapping" = 0;

    # ── idmapd configuration ──────────────────────────────────────────────
    # rpc.idmapd translates uid/gid <-> user@domain for NFSv4 transports.
    # nixpkgs 26.05 manages /etc/idmapd.conf itself via services.nfs.idmapd.
    # Writing environment.etc."idmapd.conf" directly conflicts; use the
    # module's own settings API instead.
    services.nfs.idmapd.settings = {
      General = {
        Verbosity  = "0";
        Domain     = cfg.nfs4Domain;
      };
      Mapping = {
        "Nobody-User"  = "nobody";
        "Nobody-Group" = "nogroup";
      };
      Translation = {
        Method = "nsswitch";
      };
    };

    # ── Firewall ──────────────────────────────────────────────────────────
    networking.firewall = lib.mkIf cfg.openFirewall {
      allowedTCPPorts = [ 2049 111 ];
      allowedUDPPorts = [ 2049 111 ];
    };

  };
}
