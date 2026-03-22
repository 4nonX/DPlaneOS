{ config, lib, pkgs, ... }:

let
  cfg = config.services.dplaneos.ha;
in {
  options.services.dplaneos.ha = {
    enable = lib.mkEnableOption "D-PlaneOS High Availability (Patroni + HAProxy)";

    role = lib.mkOption {
      type = lib.types.enum [ "primary" "secondary" ];
      default = "primary";
      description = "Initial HA role of this node.";
    };

    localAddress = lib.mkOption {
      type = lib.types.str;
      description = "IP address of this node.";
    };

    peerAddress = lib.mkOption {
      type = lib.types.str;
      description = "IP address of the peer (the other database node).";
    };

    witnessAddress = lib.mkOption {
      type = lib.types.str;
      description = "IP address of the etcd witness node.";
    };

    etcdEndpoints = lib.mkOption {
      type = lib.types.listOf lib.types.str;
      default = [ "http://127.0.0.1:2379" ];
      description = "List of etcd endpoints for the Patroni DCS.";
    };
  };

  config = lib.mkIf cfg.enable {
    # ─── Etcd ─────────────────────────────────────────────────────────────
    # Standard 3-node etcd cluster for reliable DCS and Patroni leader election
    services.etcd = {
      enable = true;
      name = "etcd-${cfg.localAddress}";
      listenClientUrls = [ "http://0.0.0.0:2379" ];
      listenPeerUrls = [ "http://0.0.0.0:2380" ];
      advertiseClientUrls = [ "http://${cfg.localAddress}:2379" ];
      initialAdvertisePeerUrls = [ "http://${cfg.localAddress}:2380" ];
      initialCluster = [
        "etcd-${cfg.localAddress}=http://${cfg.localAddress}:2380"
        "etcd-${cfg.peerAddress}=http://${cfg.peerAddress}:2380"
        "etcd-witness=http://${cfg.witnessAddress}:2380"
      ];
      initialClusterState = "new";
    };

    # ─── Patroni ──────────────────────────────────────────────────────────
    # Managed via raw systemd service to respect the exact config/secret layout 
    # requested: /etc/patroni/patroni.yml with 0600 permissions.
    systemd.services.patroni = {
      description = "Patroni High Availability PostgreSQL";
      after = [ "network.target" "etcd.service" ];
      requires = [ "etcd.service" ];
      wantedBy = [ "multi-user.target" ];
      environment = {
        PATH = lib.mkForce "${pkgs.patroni}/bin:${pkgs.postgresql_15}/bin:${pkgs.coreutils}/bin";
      };
      serviceConfig = {
        Type = "simple";
        User = "postgres";
        Group = "postgres";
        # Patroni configuration file is expected to be provisioned by the operator 
        # or installer to safely store the replicator password.
        ExecStart = "${pkgs.patroni}/bin/patroni /etc/patroni/patroni.yml";
        Restart = "always";
        RestartSec = "5s";
      };
    };

    # Create the postgres user required by Patroni
    users.users.postgres = {
      isSystemUser = true;
      group = "postgres";
      extraGroups = [ "dplaneos" ]; # Allow dplaneos to access postgres stuff if needed
    };
    users.groups.postgres = {};

    # Setup the patroni config directory
    systemd.tmpfiles.rules = [
      "d /etc/patroni 0700 postgres postgres -"
      "d /var/lib/postgresql 0750 postgres postgres -"
      "d /var/lib/postgresql/15 0750 postgres postgres -"
      "d /var/lib/postgresql/15/data 0700 postgres postgres -"
    ];

    # ─── HAProxy ──────────────────────────────────────────────────────────
    # Transparently routes traffic to the PostgreSQL primary.
    # Connects to Patroni's REST API (:8008) to discover the primary mode.
    services.haproxy = {
      enable = true;
      config = ''
        global
            maxconn 1000
            log /dev/log local0
            user haproxy
            group haproxy

        defaults
            log global
            mode tcp
            retries 3
            timeout client 30m
            timeout connect 4s
            timeout server 30m
            timeout check 5s

        listen postgresql
            bind 127.0.0.1:5000
            option httpchk GET /primary
            http-check expect status 200
            default-server inter 3s fall 3 rise 2 on-marked-down shutdown-sessions
            server postgresLocal  ${cfg.localAddress}:5432 maxconn 500 check port 8008
            server postgresPeer   ${cfg.peerAddress}:5432  maxconn 500 check port 8008
      '';
    };
  };
}
