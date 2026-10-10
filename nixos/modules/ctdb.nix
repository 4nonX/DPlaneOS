# nixos/modules/ctdb.nix
# ─────────────────────────────────────────────────────────────────────────────
# DPlaneOS CTDB (Clustered TDB) Integration Module
#
# PURPOSE:
#   Enable Samba clustering via CTDB so SMB clients survive HA failover without
#   disconnecting. When the primary node fails and secondary takes over, clients
#   reconnect automatically and retain byte-range locks.
#
# ARCHITECTURE:
#   CTDB coordinates Samba state across cluster nodes via a shared database.
#   In HA deployments:
#   - Path A (shared storage): CTDB database on shared SCSI-3 PR protected pool
#   - Path B (replicated): CTDB database on replicated ZFS dataset
#
#   CTDB runs on every node listed in `nodes`; on failover a surviving node
#   takes over the public addresses and client connections.
#
# DEPENDENCIES:
#   - Samba must be enabled (CTDB is the clustering backend for Samba)
#   - Shared storage (pool) must be available and accessible
#
# OPERATIONAL NOTES:
#   CTDB is experimental in DPlaneOS. Do NOT enable on production clusters
#   without understanding operational procedures in HA-FAILURE-MODES.md.
#   Test failover behavior in staging first.
# ─────────────────────────────────────────────────────────────────────────────

{ config, lib, pkgs, ... }:

let
  cfg = config.services.dplaneos.ctdb;
  sambaCfg = config.services.dplaneos.samba;

in {

  options.services.dplaneos.ctdb = {

    enable = lib.mkOption {
      type = lib.types.bool;
      default = false;
      description = ''
        Enable CTDB clustering for Samba in HA deployments.
        Requires services.dplaneos.samba.enable = true and the cluster nodes in `nodes`.
        When enabled, Samba uses CTDB for state sharing across HA nodes.
        SMB clients survive failover without disconnecting (if witness/fencing works).
        EXPERIMENTAL - test in staging before production use.
      '';
    };

    nodes = lib.mkOption {
      type = lib.types.listOf lib.types.str;
      default = [ ];
      example = [ "10.0.0.1" "10.0.0.2" ];
      description = "Addresses of all CTDB nodes, identical and in the same order on every node.";
    };

    dataPool = lib.mkOption {
      type = lib.types.str;
      default = "tank";
      description = ''
        ZFS pool name where CTDB lock database resides.
        For Path A (shared storage): shared SCSI-3 PR protected pool (same on both nodes).
        For Path B (replicated): replicated pool (replicated via HA ZFS send/recv).
        CTDB database path: /var/lib/ctdb on this pool.
      '';
    };

    dataDataset = lib.mkOption {
      type = lib.types.str;
      default = "tank/ctdb";
      description = ''
        ZFS dataset for CTDB database directory (/var/lib/ctdb).
        This dataset must exist and be mounted before CTDB starts.
      '';
    };

    publicAddresses = lib.mkOption {
      type = lib.types.listOf lib.types.str;
      default = [];
      description = ''
        List of public IP addresses that clients connect to, one per line.
        These are the addresses CTDB moves between nodes.
        Example: [ "192.168.1.100/24" ]
        CTDB manages which node owns each public IP.
      '';
    };

    nodeTimeout = lib.mkOption {
      type = lib.types.int;
      default = 30;
      description = "Seconds to wait before declaring a node dead (CTDB node_timeout).";
    };

    recoveryTimeout = lib.mkOption {
      type = lib.types.int;
      default = 120;
      description = "Seconds for recovery process after node failure (CTDB recovery_timeout).";
    };

    logLevel = lib.mkOption {
      type = lib.types.int;
      default = 1;
      description = "CTDB log level (0=ERROR, 1=WARNING, 2=NOTICE, 3=INFO, 4=DEBUG).";
    };

  };

  config = lib.mkIf cfg.enable {

    # ── Guard conditions ────────────────────────────────────────────────────
    # CTDB requires Samba and its node list
    assertions = [
      {
        assertion = cfg.nodes != [ ];
        message = "services.dplaneos.ctdb.nodes must list the addresses of all CTDB nodes";
      }
      {
        assertion = sambaCfg.enable;
        message = "services.dplaneos.ctdb requires services.dplaneos.samba.enable = true";
      }
    ];

    # ── Prepare CTDB data directory ─────────────────────────────────────────
    # CTDB needs a stable directory for its database and lock files.
    # On mounted ZFS dataset /var/lib/ctdb.

    systemd.tmpfiles.rules = [
      "d /var/lib/ctdb 0755 root root -"
      "d /var/lib/ctdb/persistent 0755 root root -"
      "d /var/log/ctdb 0755 root root -"
    ];

    # Ensure /var/lib/ctdb survives across reboots
    # (if system uses tmpfs like NixOS often does)
    systemd.mounts = [
      {
        what = "${cfg.dataDataset}";
        where = "/var/lib/ctdb";
        type = "zfs";
        fsType = "zfs";
        options = "defaults";
        before = [ "ctdb.service" ];
        wantedBy = [ "multi-user.target" ];
      }
    ];

    # ── CTDB systemd service ────────────────────────────────────────────────
    systemd.services.ctdb = {
      description = "CTDB Cluster Database (Samba HA)";
      after = [
        "network-online.target"
        "zfs-mount.service"
        "dplaned.service"
      ];
      wants = [ "network-online.target" ];
      wantedBy = [ "multi-user.target" ];

      serviceConfig = {
        Type = "forking";
        PIDFile = "/var/run/ctdb/ctdbd.pid";
        ExecStartPre = "${pkgs.coreutils}/bin/mkdir -p /var/run/ctdb /var/lib/ctdb/persistent";
        # ctdbd writes its own pidfile in /var/run/ctdb
        ExecStart = ''
          ${pkgs.samba}/bin/ctdbd \
            --nlist /etc/ctdb/nodes \
            --socket /var/run/ctdb/ctdbd.socket \
            --public-addresses /etc/ctdb/public_addresses \
            --pidfile /var/run/ctdb/ctdbd.pid \
            --logfile /var/log/ctdb/ctdbd.log \
            --log-level ${toString cfg.logLevel} \
            --dbdir /var/lib/ctdb \
            --reclock /var/lib/ctdb/reclock \
            --node-timeout ${toString cfg.nodeTimeout} \
            --recovery-timeout ${toString cfg.recoveryTimeout}
        '';
        ExecStop = "${pkgs.samba}/bin/ctdb shutdown";
        Restart = "on-failure";
        RestartSec = 5;
      };

      # Dependencies
      requires = [ "network-online.target" "zfs-mount.service" ];
    };

    # ── Samba integration ───────────────────────────────────────────────────
    # Tell Samba to use CTDB for state sharing instead of local TDB files.
    services.samba.settings.global = lib.mkIf cfg.enable {
      "clustering" = "yes";
      "ctdbd socket" = "/var/run/ctdb/ctdbd.socket";
      # CTDB manages locks and leases, not local TDB
      "share modes" = "no";
      "locking" = "yes";
    };

    # ── CTDB configuration files ────────────────────────────────────────────

    # Cluster node list: each node's IP and hostname
    # This must be identical on all cluster nodes.
    environment.etc."ctdb/nodes".text = lib.concatMapStrings (n: n + "
") cfg.nodes;

    # Public addresses: VIPs managed by CTDB
    # Format: <ip>/<mask> <interface>
    # CTDB will migrate these IPs between nodes on failover.
    environment.etc."ctdb/public_addresses".text = ''
      ${lib.concatStringsSep "\n" cfg.publicAddresses}
    '';

    # Recovery daemon: runs after node failure to heal the cluster
    # This is a shell script that CTDB calls; we use a simple no-op for now.
    environment.etc."ctdb/events.d/00.recoverd".text = ''
      #!/bin/sh
      # Event handler called on cluster recovery
      # For now, just acknowledge recovery completion
      exit 0
    '';

    # ── Logging and monitoring ──────────────────────────────────────────────
    # Log directory is created above in systemd.tmpfiles.rules

    # ── Firewall rules for CTDB ─────────────────────────────────────────────
    # CTDB uses ports for inter-node communication
    networking.firewall = lib.mkIf config.networking.firewall.enable {
      allowedTCPPorts = [
        4379  # CTDB daemon
      ];
      allowedUDPPorts = [
        4379  # CTDB daemon
      ];
    };

    # ── Samba dependency on CTDB ────────────────────────────────────────────
    # Samba must not start before CTDB is ready
    systemd.services.samba = {
      after = lib.mkIf cfg.enable [ "ctdb.service" ];
      requires = lib.mkIf cfg.enable [ "ctdb.service" ];
    };

    # ── User/group for CTDB ─────────────────────────────────────────────────
    # CTDB runs as root (needs to manage IPs)
    users.users.ctdb = lib.mkIf false {  # Disabled - ctdb runs as root
      isSystemUser = true;
      group = "ctdb";
    };
    users.groups.ctdb = lib.mkIf false {};

  };

}
