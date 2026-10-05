{ config, lib, pkgs, ... }:

let
  cfg = config.services.dplaneos.ha;
  # Safe witness etcd client endpoint used in Patroni config.
  # Evaluated lazily; the assertion in config.assertions catches the null case.
  witnessEtcdHost =
    if cfg.colocatedWitness then "${cfg.localAddress}:2381"
    else if cfg.witnessAddress != null then "${cfg.witnessAddress}:2379"
    else "";  # unreachable: assertion enforces one of the above
in {
  options.services.dplaneos.ha = {
    enable = lib.mkEnableOption "DPlaneOS High Availability (Patroni + HAProxy)";

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

    fencing = {
      enable = lib.mkEnableOption "IPMI/Redfish STONITH fencing for automatic failover";
      bmcIP = lib.mkOption {
        type = lib.types.nullOr lib.types.str;
        default = null;
        description = "IP address of the peer node's BMC.";
      };
      bmcUser = lib.mkOption {
        type = lib.types.nullOr lib.types.str;
        default = null;
        description = "Username for the peer node's BMC.";
      };
      bmcPasswordFile = lib.mkOption {
        type = lib.types.nullOr lib.types.str;
        default = null;
        description = "Path to a file containing the peer node's BMC password.";
      };
    };

    witnessAddress = lib.mkOption {
      type    = lib.types.nullOr lib.types.str;
      default = null;
      description = ''
        IP address of the etcd witness node (Path B / three-machine setup).
        Leave null when colocatedWitness = true - the witness etcd runs on this node.
      '';
    };

    colocatedWitness = lib.mkEnableOption ''
      Run a co-located etcd witness member on port 2381 on this node (Path A').
      When enabled, no separate witness machine is required: this node runs both
      the data-path etcd (port 2379) and a vote-only witness etcd (port 2381).
      The initialCluster and Patroni etcd3 hosts are configured automatically.
      witnessAddress does not need to be set.
    '';

    etcdEndpoints = lib.mkOption {
      type = lib.types.listOf lib.types.str;
      default = [ "http://127.0.0.1:2379" ];
      description = "List of etcd endpoints for the Patroni DCS.";
    };

    virtualIP = lib.mkOption {
      type = lib.types.nullOr lib.types.str;
      default = null;
      description = "The floating Virtual IP (VIP) managed by Keepalived.";
    };

    interface = lib.mkOption {
      type = lib.types.nullOr lib.types.str;
      default = null;
      description = "The network interface to bind the Keepalived VIP to.";
    };

    vrrpPassword = lib.mkOption {
      type = lib.types.nullOr lib.types.str;
      default = null;
      description = "Password for Keepalived VRRP authentication between peers.";
    };

    sbd = {
      pool = lib.mkOption {
        type    = lib.types.str;
        default = "";
        description = ''
          ZFS pool holding the SBD lease dataset. Leave empty to disable
          ZFS-property SBD fencing (single-node safe; zero overhead).
          When non-empty, a one-shot service dplaneos-sbd-init creates
          pool/dataset at first boot if it does not already exist.
        '';
      };
      dataset = lib.mkOption {
        type    = lib.types.str;
        default = "sbd-lease";
        description = "Dataset name within ha.sbd.pool used for lease fencing (e.g. sbd-lease).";
      };
    };

    watchdog = {
      enable = lib.mkEnableOption ''
        Hardware watchdog self-fence. When enabled, the daemon pets /dev/watchdog
        on every heartbeat tick while quorum is healthy. If quorum is lost the
        daemon stops petting it; the kernel hard-resets the node after the timeout.
        This removes the BMC/PDU network-reachability assumption from the fencing
        chain and is the correct safety floor for mini-PC hardware without IPMI.
        The timeout MUST be less than failover_after_seconds in the HA timing config.
      '';
      device = lib.mkOption {
        type    = lib.types.str;
        default = "/dev/watchdog";
        description = "Watchdog device path. Use /dev/watchdog0 on systems with multiple watchdogs.";
      };
      timeoutSecs = lib.mkOption {
        type    = lib.types.int;
        default = 30;
        description = ''
          Hardware watchdog timeout in seconds. The kernel resets the node if
          the watchdog is not pet within this interval. Must be less than
          ha.failoverAfterSecs (if set) to guarantee the loser has reset before
          the survivor promotes. Default 30s matches the SBD lease TTL default.
        '';
      };
    };
  };

  config = lib.mkIf cfg.enable {
    # ─── Assertions ───────────────────────────────────────────────────────
    assertions = [
      {
        assertion = cfg.colocatedWitness || cfg.witnessAddress != null;
        message   = "services.dplaneos.ha: set either colocatedWitness = true (Path A', no third machine) or witnessAddress = \"<IP>\" (Path B)";
      }
    ];

    # ─── Hardware watchdog self-fence ─────────────────────────────────────
    # When enabled, systemd opens the watchdog device and sets the runtime
    # timeout. The dplaned daemon pets it from the heartbeat loop; losing
    # quorum stops the petting and the kernel hard-resets the node. This
    # removes the IPMI/PDU network-reachability requirement for fencing.
    # softdog is the software fallback for VMs and boards without a hardware
    # watchdog device; it resets via kernel panic rather than hardware reset.
    systemd.watchdog = lib.mkIf cfg.watchdog.enable {
      runtimeTime = "${toString cfg.watchdog.timeoutSecs}s";
      rebootTime  = "${toString (cfg.watchdog.timeoutSecs * 2)}s";
    };

    # Load softdog if the device is the default /dev/watchdog and no hardware
    # watchdog driver is present. A real hardware watchdog (iTCO, sp5100_tco,
    # etc.) takes precedence; softdog only claims the device when no other
    # driver does.
    boot.kernelModules = lib.mkIf (cfg.watchdog.enable && cfg.watchdog.device == "/dev/watchdog") [
      "softdog"
    ];

    # ─── ZFS hostId ───────────────────────────────────────────────────────
    # module.nix sets boot.supportedFilesystems = ["zfs"], which triggers the
    # NixOS assertion that networking.hostId must be set. HA nodes are already
    # uniquely identified by their local IP, so derive a stable 8-char hex id
    # from it. Operators can override with: networking.hostId = lib.mkForce "...";
    networking.hostId = lib.mkDefault (
      builtins.substring 0 8 (builtins.hashString "md5" cfg.localAddress)
    );

    # ─── HA Firewall ─────────────────────────────────────────────────────
    # The base module.nix opens only 80 and 443. HA requires additional ports
    # between cluster members. Without these, etcd cannot form a cluster,
    # Patroni cannot check peer health via HAProxy, and streaming replication
    # cannot connect.
    networking.firewall.allowedTCPPorts = [
      2379  # etcd client API
      2380  # etcd peer (raft) communication
      5432  # PostgreSQL - streaming replication between Patroni nodes
      8008  # Patroni REST API - HAProxy health checks against both nodes
    ] ++ lib.optionals cfg.colocatedWitness [
      2381  # co-located etcd witness client API (Path A')
      2382  # co-located etcd witness peer (raft)
    ];

    # ─── Etcd ─────────────────────────────────────────────────────────────
    # Three-member etcd cluster for reliable DCS and Patroni leader election.
    # Path A' (colocatedWitness): witness runs on this node, port 2382 peer / 2381 client.
    # Path B: witness is a separate machine on the standard port 2380.
    services.etcd = {
      enable = true;
      name = "etcd-${cfg.localAddress}";
      listenClientUrls = [ "http://0.0.0.0:2379" ];
      listenPeerUrls = [ "http://0.0.0.0:2380" ];
      advertiseClientUrls = [ "http://${cfg.localAddress}:2379" ];
      initialAdvertisePeerUrls = [ "http://${cfg.localAddress}:2380" ];
      initialCluster =
        if cfg.colocatedWitness then [
          "etcd-${cfg.localAddress}=http://${cfg.localAddress}:2380"
          "etcd-${cfg.peerAddress}=http://${cfg.peerAddress}:2380"
          "etcd-witness=http://${cfg.localAddress}:2382"   # co-located witness peer port
        ] else [
          "etcd-${cfg.localAddress}=http://${cfg.localAddress}:2380"
          "etcd-${cfg.peerAddress}=http://${cfg.peerAddress}:2380"
          "etcd-witness=http://${cfg.witnessAddress}:2380"
        ];
      initialClusterState = "new";
    };

    # ─── Co-located etcd witness (Path A') ────────────────────────────────
    # A second etcd process on this node. Uses port 2381 (client) and 2382
    # (peer raft). Data stored in /var/lib/etcd-witness so it does not conflict
    # with the data-path etcd. Runs as the etcd OS user created by services.etcd.
    systemd.services.etcd-witness = lib.mkIf cfg.colocatedWitness {
      description = "DPlaneOS co-located etcd witness (port 2381)";
      after    = [ "network.target" ];
      wantedBy = [ "multi-user.target" ];
      environment = {
        ETCD_NAME                        = "etcd-witness";
        ETCD_DATA_DIR                    = "/var/lib/etcd-witness";
        ETCD_LISTEN_CLIENT_URLS          = "http://0.0.0.0:2381";
        ETCD_ADVERTISE_CLIENT_URLS       = "http://${cfg.localAddress}:2381";
        ETCD_LISTEN_PEER_URLS            = "http://0.0.0.0:2382";
        ETCD_INITIAL_ADVERTISE_PEER_URLS = "http://${cfg.localAddress}:2382";
        ETCD_INITIAL_CLUSTER             = lib.concatStringsSep "," [
          "etcd-${cfg.localAddress}=http://${cfg.localAddress}:2380"
          "etcd-${cfg.peerAddress}=http://${cfg.peerAddress}:2380"
          "etcd-witness=http://${cfg.localAddress}:2382"
        ];
        ETCD_INITIAL_CLUSTER_STATE = "new";
      };
      serviceConfig = {
        Type           = "simple";
        User           = "etcd";
        Group          = "etcd";
        ExecStart      = "${pkgs.etcd}/bin/etcd";
        Restart        = "always";
        RestartSec     = "5s";
        StateDirectory = "etcd-witness";
      };
    };

    # ─── Patroni config auto-generation ──────────────────────────────────
    # Generates /etc/dplaneos/patroni.yaml on first boot if not present.
    # /etc/dplaneos is bind-mounted from /persist via impermanence.nix so the
    # file survives OTA slot swaps. Random replication and superuser passwords
    # are generated once and stay stable for the lifetime of the cluster.
    systemd.services.dplaneos-patroni-init = {
      description = "DPlaneOS Patroni Config Init";
      after       = [ "local-fs.target" ];
      before      = [ "patroni.service" ];
      wantedBy    = [ "multi-user.target" ];
      serviceConfig = {
        Type            = "oneshot";
        RemainAfterExit = true;
        ExecStart       = pkgs.writeShellScript "patroni-init" ''
          set -eu
          CONFIG="/etc/dplaneos/patroni.yaml"
          if [ -f "$CONFIG" ]; then
            echo "patroni-init: $CONFIG already exists, nothing to do"
            exit 0
          fi
          echo "patroni-init: first boot - generating $CONFIG"
          REPL_PASS=$(tr -dc 'A-Za-z0-9' < /dev/urandom | head -c 32)
          SUPER_PASS=$(tr -dc 'A-Za-z0-9' < /dev/urandom | head -c 32)
          mkdir -p /etc/dplaneos
          cat > "$CONFIG" <<EOF
scope: dplaneos
name: dplaneos-${cfg.localAddress}

restapi:
  listen: 0.0.0.0:8008
  connect_address: ${cfg.localAddress}:8008

etcd3:
  hosts: ${cfg.localAddress}:2379,${cfg.peerAddress}:2379,${witnessEtcdHost}

bootstrap:
  dcs:
    ttl: 30
    loop_wait: 10
    retry_timeout: 10
    maximum_lag_on_failover: 1048576
  initdb:
    - encoding: UTF8
    - data-checksums
  pg_hba:
    - host replication replicator ${cfg.peerAddress}/32 md5
    - host replication replicator 127.0.0.1/32 md5
    - host all all 127.0.0.1/32 trust

postgresql:
  listen: 0.0.0.0:5432
  connect_address: ${cfg.localAddress}:5432
  data_dir: /var/lib/dplaneos/pgsql
  bin_dir: ${pkgs.postgresql_15}/bin
  parameters:
    max_connections: 200
    shared_buffers: 256MB
    wal_level: replica
    max_wal_senders: 5
    max_replication_slots: 5
  authentication:
    replication:
      username: replicator
      password: $REPL_PASS
    superuser:
      username: postgres
      password: $SUPER_PASS

tags:
  nofailover: false
  noloadbalance: false
  clonefrom: false
  nosync: false
EOF
          chmod 0600 "$CONFIG"
          chown postgres:postgres "$CONFIG"
          echo "patroni-init: config written to $CONFIG"
        '';
      };
    };

    # ─── Patroni ──────────────────────────────────────────────────────────
    systemd.services.patroni = {
      description = "Patroni High Availability PostgreSQL";
      after = [ "network.target" "etcd.service" "dplaneos-patroni-init.service" ];
      requires = [ "etcd.service" "dplaneos-patroni-init.service" ];
      wantedBy = [ "multi-user.target" ];
      environment = {
        PATH = lib.mkForce "${pkgs.patroni}/bin:${pkgs.postgresql_15}/bin:${pkgs.coreutils}/bin";
      };
      serviceConfig = {
        Type       = "simple";
        User       = "postgres";
        Group      = "postgres";
        ExecStart  = "${pkgs.patroni}/bin/patroni /etc/dplaneos/patroni.yaml";
        Restart    = "always";
        RestartSec = "5s";
      };
    };

    # Create the postgres user required by Patroni
    users.users.postgres = {
      isSystemUser = true;
      group = "postgres";
      extraGroups = [ "dplaneos" ];
    };
    users.groups.postgres = {};

    systemd.tmpfiles.rules = [
      "d /var/lib/dplaneos/pgsql 0700 postgres postgres -"
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

    # ─── Keepalived ───────────────────────────────────────────────────────
    # VRRP handles floating the VIP to the healthy primary node.
    # Health checks ping the DPlaneOS daemon directly.
    services.keepalived = lib.mkIf (cfg.virtualIP != null && cfg.interface != null) {
      enable = true;
      vrrpScripts.check_dplaneos = {
        script = "${pkgs.curl}/bin/curl -sf --unix-socket /run/dplaneos/dplaned.sock http://localhost/health";
        interval = 2;
        fall = 3;
        rise = 2;
      };
      vrrpInstances.dplaneos_vip = {
        interface = cfg.interface;
        state = if cfg.role == "primary" then "MASTER" else "BACKUP";
        priority = if cfg.role == "primary" then 100 else 90;
        virtualRouterId = 51;
        virtualIps = [ { addr = cfg.virtualIP; } ];
        trackScripts = [ "check_dplaneos" ];
        # notify_backup fires when this node loses the VRRP VIP (transitions
        # MASTER -> BACKUP). The sequence is:
        #
        #   Daemon-UP path (normal):
        #   1. ALUA standby: move iSCSI targets to Standby so initiators see a
        #      clean path-state change. Non-fatal if targetcli unavailable.
        #   2. Daemon-mediated pool export (4s deadline). On timeout the daemon
        #      force-reboots itself via syscall.Reboot to prevent split-brain.
        #
        #   Daemon-DOWN path (daemon restarting or crashed):
        #   The || true on the curl calls is required: a non-zero curl exit would
        #   wedge Keepalived's state machine. But || true means a down daemon is
        #   silent and the daemon-mediated steps never run - split-brain risk.
        #   The daemon-down path replicates the daemon's work directly using the
        #   same binaries (targetcli, zpool) with the same deadlines:
        #   - ALUA standby via targetcli (non-fatal, mirrors ALUAStandby handler)
        #   - Per-pool export via timeout(1) with 4s deadline (matches ExportPoolTimeout)
        #   - reboot -f on any export failure (matches ForceSelfReboot behaviour)
        #   If zpool export also fails, the node reboots to prevent split-brain.
        #
        # This mirrors TrueNAS's ZPOOL_EXPORT_TIMEOUT = 4s pattern.
        extraConfig = ''
          notify_backup "${pkgs.writeShellScript "vrrp-notify-backup" ''
            TOKEN=$(cat /var/lib/dplaneos/internal-token 2>/dev/null || true)

            # Probe daemon. One attempt, 2s timeout, no retry.
            DAEMON_UP=0
            if ${pkgs.curl}/bin/curl -sf --max-time 2 \
                --unix-socket /run/dplaneos/dplaned.sock \
                http://localhost/health >/dev/null 2>&1; then
              DAEMON_UP=1
            fi

            if [ "$DAEMON_UP" = "1" ]; then
              # Daemon-UP path: ALUA standby then daemon-mediated pool export.
              ${pkgs.curl}/bin/curl -sf --max-time 5 -X POST \
                --unix-socket /run/dplaneos/dplaned.sock \
                -H "X-Internal-Token: $TOKEN" \
                http://localhost/api/ha/alua-standby || true
              ${pkgs.curl}/bin/curl -sf --max-time 10 -X POST \
                --unix-socket /run/dplaneos/dplaned.sock \
                -H "X-Internal-Token: $TOKEN" \
                http://localhost/api/ha/standby || true
            else
              # Daemon-DOWN path: daemon unavailable, replicate its work directly.
              # The daemon's ALUAStandby and BecomeStandby handlers are shell-
              # transparent wrappers: they call targetcli and zpool. Both binaries
              # are available here. Run them directly with the same deadlines.
              ${pkgs.util-linux}/bin/logger -t dplaneos-ha -p daemon.crit \
                "STONITH: daemon unreachable during notify_backup - executing failover sequence directly"

              # Step 1 (mirrors POST /api/ha/alua-standby):
              # Set all ALUA-enabled iSCSI targets to Standby so initiators see a
              # clean path-state change instead of an abrupt loss.
              # Non-fatal: not all deployments use iSCSI ALUA.
              for iqn in $(${pkgs.targetcli-fb}/bin/targetcli /iscsi ls 2>/dev/null \
                           | grep -oE 'iqn\.[^ ]+' 2>/dev/null); do
                ${pkgs.targetcli-fb}/bin/targetcli "/iscsi/$iqn/tpg1" \
                  set attribute alua_support=1 2>/dev/null || true
                ${pkgs.targetcli-fb}/bin/targetcli \
                  "/iscsi/$iqn/tpg1/alua/default_tg_pt_gp" \
                  set alua_access_state=2 2>/dev/null || true
                ${pkgs.util-linux}/bin/logger -t dplaneos-ha "STONITH: set ALUA Standby on $iqn (direct path)"
              done
              ${pkgs.targetcli-fb}/bin/targetcli / saveconfig 2>/dev/null || true

              # Step 2 (mirrors BecomeStandby / exportAllPools):
              # Export each pool with the same 4-second per-pool deadline as
              # ExportPoolTimeout in ha/standby.go. On timeout or error, reboot
              # immediately - same guarantee as daemon ForceSelfReboot.
              # stderr is NOT suppressed: failures belong in syslog.
              for pool in $(${config.boot.zfs.package}/bin/zpool list -H -o name 2>/dev/null); do
                if ${pkgs.coreutils}/bin/timeout 4 ${config.boot.zfs.package}/bin/zpool export "$pool"; then
                  ${pkgs.util-linux}/bin/logger -t dplaneos-ha "STONITH: exported pool $pool (direct path)"
                else
                  ${pkgs.util-linux}/bin/logger -t dplaneos-ha -p daemon.crit \
                    "STONITH: export of $pool failed or timed out within 4s - rebooting to prevent split-brain"
                  ${pkgs.systemd}/bin/reboot -f
                  exit 1
                fi
              done
            fi
          ''}"
        '' + lib.optionalString (cfg.vrrpPassword != null) ''
          authentication {
            auth_type PASS
            auth_pass ${cfg.vrrpPassword}
          }
        '';
      };
    };

    # ─── Systemd Overrides for Patroni HA Split-Brain Protection ─────────
    # We must restrict the native NixOS ZFS import units to only execute if
    # this node is actually the Patroni Primary. If the node boots into Standby,
    # the datasets remain physically unmounted to avert split-brain data corruption.
    systemd.services.zfs-import-cache = lib.mkIf cfg.fencing.enable {
      after = [ "patroni.service" ];
      requires = [ "patroni.service" ];
      preStart = ''
        echo "HA GUARD: Waiting for Patroni leadership determination..."
        deadline=120
        elapsed=0
        while [ $elapsed -lt $deadline ]; do
          status=$(${pkgs.curl}/bin/curl -s -o /dev/null -w "%{http_code}" --max-time 3 http://localhost:8008/primary || echo "000")
          if [ "$status" = "200" ]; then
            echo "HA GUARD: Node is Primary. Proceeding to mount ZFS volumes over Native Cache."
            exit 0
          elif [ "$status" = "503" ]; then
            echo "HA GUARD: Node is Standby. Aborting native ZFS mounts."
            exit 1
          fi
          sleep 2
          elapsed=$((elapsed + 2))
        done
        echo "HA GUARD: Timed out after $deadline s waiting for Patroni. Aborting ZFS mount (fail-safe)."
        exit 1
      '';
    };

    systemd.services.zfs-import-scan = lib.mkIf cfg.fencing.enable {
      after = [ "patroni.service" ];
      requires = [ "patroni.service" ];
      preStart = ''
        echo "HA GUARD: Waiting for Patroni leadership determination..."
        deadline=120
        elapsed=0
        while [ $elapsed -lt $deadline ]; do
          status=$(${pkgs.curl}/bin/curl -s -o /dev/null -w "%{http_code}" --max-time 3 http://localhost:8008/primary || echo "000")
          if [ "$status" = "200" ]; then
            echo "HA GUARD: Node is Primary. Proceeding to mount ZFS volumes over Native Scan."
            exit 0
          elif [ "$status" = "503" ]; then
            echo "HA GUARD: Node is Standby. Aborting native ZFS mounts."
            exit 1
          fi
          sleep 2
          elapsed=$((elapsed + 2))
        done
        echo "HA GUARD: Timed out after $deadline s waiting for Patroni. Aborting ZFS mount (fail-safe)."
        exit 1
      '';
    };

    # ─── SBD lease dataset init ──────────────────────────────────────────
    # Creates the ZFS dataset used for ZFS-property SBD fencing if it does
    # not already exist. Only runs when ha.sbd.pool is non-empty.
    # The daemon manages the actual lease property writes at runtime.
    systemd.services.dplaneos-sbd-init = lib.mkIf (cfg.sbd.pool != "") {
      description = "DPlaneOS SBD Lease Dataset Init";
      path        = [ config.boot.zfs.package ];  # zfs list/create
      after       = [ "zfs.target" "dplaneos-zfs-gate.service" ];
      before      = [ "dplaned.service" ];
      wantedBy    = [ "multi-user.target" ];
      serviceConfig = {
        Type            = "oneshot";
        RemainAfterExit = true;
        ExecStart       = pkgs.writeShellScript "dplaneos-sbd-init" ''
          set -eu
          DATASET="${cfg.sbd.pool}/${cfg.sbd.dataset}"
          if zfs list -H "$DATASET" > /dev/null 2>&1; then
            echo "dplaneos-sbd-init: dataset $DATASET already exists"
          else
            echo "dplaneos-sbd-init: creating $DATASET"
            zfs create -o mountpoint=none "$DATASET"
          fi
        '';
      };
    };
  };
}
