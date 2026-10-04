# DPlaneOS NixOS Module
# Declares all system-level requirements: packages, services, users, firewall.
# Imported by flake.nix and usable standalone via imports = [ ./module.nix ];

{ config, lib, pkgs, ... }:

# Samba, NFS, and fenced modules are imported below and are available to all
# DPlaneOS installations. Samba and NFS default to enabled; fenced defaults to disabled.

let
  cfg = config.services.dplaneos;
in {
  imports = [ ./ha.nix ./console-network-wizard.nix ./modules/samba.nix ./modules/nfs.nix ./modules/fenced.nix ./modules/ctdb.nix ];

  options.services.dplaneos = {
    enable = lib.mkEnableOption "DPlaneOS NAS daemon";

    daemonPackage = lib.mkOption {
      type        = lib.types.package;
      description = ''
        The dplaned binary package. Set this to the output of the flake's
        dplaneos-daemon derivation. In flake.nix this is wired automatically
        via specialArgs; standalone users set it to a local derivation or a
        pre-built store path.
        Example (in configuration.nix with flake):
          services.dplaneos.daemonPackage = self.packages.x86_64-linux.dplaneos-daemon;
      '';
    };

    frontendPackage = lib.mkOption {
      type        = lib.types.package;
      description = ''
        Pre-built frontend static files served by nginx. Set this to the
        output of the flake's dplaneos-frontend derivation. In flake.nix
        this is wired automatically; standalone users set it to a local
        derivation or the pre-built store path.
        Example (in configuration.nix with flake):
          services.dplaneos.frontendPackage = self.packages.x86_64-linux.dplaneos-frontend;
      '';
    };

    socketPath = lib.mkOption {
      type     = lib.types.str;
      default  = "/run/dplaneos/dplaned.sock";
      readOnly = true;
      description = ''
        Unix socket path for nginx-to-daemon communication.
        nginx proxies /api/ and /ws to this socket; no TCP port is consumed.
        Read-only: changing this would desync the daemon and nginx.
      '';
    };

    dbDSN = lib.mkOption {
      type    = lib.types.str;
      default = if cfg.ha.enable
                then "postgres://dplaneos@localhost:5000/dplaneos?sslmode=disable"
                else "postgres://dplaneos@/dplaneos?host=/run/postgresql&sslmode=disable";
      description = ''
        PostgreSQL Data Source Name. Without HA the daemon connects over the local
        Unix socket: NixOS PostgreSQL does not listen on TCP unless enableTCPIP is set,
        and its default pg_hba.conf requires a password for TCP connections.
      '';
    };

    openFirewall = lib.mkOption {
      type    = lib.types.bool;
      default = true;
      description = "Open TCP port 80 (and 443 if TLS is configured) in the firewall.";
    };

    sshKeys = lib.mkOption {
      type        = lib.types.listOf lib.types.str;
      default     = [];
      description = ''
        SSH public keys authorised for the root user.
        Password authentication is disabled; at least one key is required
        for remote access after installation.
        Example: [ "ssh-ed25519 AAAA... user@host" ]
      '';
    };

    dbPath = lib.mkOption {
      type        = lib.types.str;
      default     = "/var/lib/dplaneos/pgsql";
      description = "Path where the PostgreSQL data directory is located.";
    };

    docker = {
      enableNvidia = lib.mkEnableOption ''
        NVIDIA Container Toolkit for Docker (sets virtualisation.docker.enableNvidia).
        Enable when compose stacks use NVIDIA GPU reservations or the nvidia runtime.
        Proprietary NVIDIA drivers on the host are still configured by the operator.
      '';
    };

    coldTier = {
      rootPath = lib.mkOption {
        type    = lib.types.str;
        default = "/mnt/cold";
        description = ''
          Base directory under which rclone FUSE mounts are created by the daemon.
          The daemon creates per-remote subdirectories here (e.g. /mnt/cold/s3-backup).
          This path is created at boot and added to dplaned ReadWritePaths so FUSE
          mounts survive under ProtectSystem=strict.
        '';
      };
    };

  };

  config = lib.mkIf cfg.enable {
    # ─── OpenZFS version assertion ────────────────────────────────────────
    # RAID-Z parity expansion (v9.1+) requires OpenZFS 2.2.0 or later.
    # zpool attach on a RAID-Z vdev silently creates a mirror instead of
    # expanding on older versions, which is a silent data-layout mismatch.
    assertions = [{
      assertion = lib.versionAtLeast config.boot.zfs.package.version "2.2.0";
      message =
        "DPlaneOS v9.1+ requires OpenZFS 2.2.0 or later for RAID-Z parity " +
        "expansion (zpool attach on raidz silently creates a mirror on older versions). " +
        "Set boot.zfs.package = pkgs.zfs_2_2 or use NixOS 23.11+.";
    }];

    # ─── Required system packages ────────────────────────────────────────
    environment.systemPackages = [
      pkgs.zfs
      pkgs.docker
      pkgs.docker-compose
      pkgs.nginx
      pkgs.nfs-utils
      pkgs.smartmontools
      pkgs.ipmitool          # IPMI LAN+ for STONITH fencing and sensor monitoring
      pkgs.dmidecode         # hardware identity (vendor, model, serial) on x86
      pkgs.lm_sensors        # optional: local sensor fallback if BMC unavailable
      pkgs.pv
      pkgs.rclone
      pkgs.fuse3        # fusermount3 for rclone cold tier FUSE mounts
      pkgs.openssh
      pkgs.git
      pkgs.targetcli-fb
      pkgs.curl
      pkgs.bash
      pkgs.coreutils
      pkgs.postgresql
    ];

    # ─── ZFS ─────────────────────────────────────────────────────────────
    boot.supportedFilesystems = [ "zfs" ];
    boot.zfs.forceImportRoot  = false;   # never force-import root pool
    services.zfs.autoScrub.enable   = true;
    services.zfs.autoScrub.interval = "monthly";
    services.zfs.trim.enable        = true;
    services.zfs.zed.settings = {
      ZED_DEBUG_LOG = "/var/log/zed.log";
    };
    services.zfs.zed.enableMail = false;

    environment.etc."zfs/zed.d/dplaneos-notify.sh" = {
      source = pkgs.writeShellScript "dplaneos-notify" ''
        #!/usr/bin/env bash
        DAEMON_SOCKET="/run/dplaneos/dplaneos.sock"
        LOG_TAG="dplaneos-zed"

        case "$ZEVENT_SUBCLASS" in
            pool_destroy|vdev_remove|device_removal) SEVERITY="critical" ;;
            statechange)
                case "$ZEVENT_VDEV_STATE_STR" in
                    FAULTED|UNAVAIL|REMOVED) SEVERITY="critical" ;;
                    DEGRADED) SEVERITY="warning" ;;
                    *) SEVERITY="info" ;;
                esac
                ;;
            scrub_finish|resilver_finish) SEVERITY="info" ;;
            io_failure|checksum_failure) SEVERITY="warning" ;;
            *) SEVERITY="info" ;;
        esac

        logger -t "$LOG_TAG" "[$SEVERITY] Pool=$ZEVENT_POOL Event=$ZEVENT_SUBCLASS State=$ZEVENT_VDEV_STATE_STR Device=$ZEVENT_VDEV_PATH"

        if [ -S "$DAEMON_SOCKET" ]; then
            echo "zfs_event:$SEVERITY:$ZEVENT_POOL:$ZEVENT_SUBCLASS:$ZEVENT_VDEV_STATE_STR" | ${pkgs.socat}/bin/socat -t2 - UNIX-CONNECT:"$DAEMON_SOCKET" 2>/dev/null || true
        fi
        exit 0
      '';
      mode = "0755";
    };

    # NVMe-oF target (kernel nvmet) - modules load at boot; dplaned writes configfs at runtime.
    # fuse is included here for rclone cold tier FUSE mounts (managed by dplaned at runtime).
    # tun is required for OpenVPN and Tailscale Docker containers (/dev/net/tun device node).
    # WireGuard is built into kernel 6.6+ (CONFIG_WIREGUARD=y) - no separate module needed.
    boot.kernelModules = [ "nvmet" "nvmet-tcp" "fuse" "tun" ];

    # ─── Docker ──────────────────────────────────────────────────────────
    virtualisation.docker = lib.mkMerge [
      {
        enable           = true;
        storageDriver    = "overlay2";
        autoPrune.enable = true;
      }
      (lib.mkIf cfg.docker.enableNvidia { enableNvidia = true; })
    ];

    # ─── SSH ─────────────────────────────────────────────────────────────
    services.openssh = {
      enable                  = true;
      settings.PasswordAuthentication = false;
      settings.PermitRootLogin        = "no";
    };

    # ─── SSH authorised keys (replaces password auth) ───────────────────
    users.users.root.openssh.authorizedKeys.keys = cfg.sshKeys;

    # Shared group for Unix socket access between dplaned (root) and nginx.
    # The socket is created 0660 root:dplaned so only these two can connect.
    users.groups.dplaned = {};
    users.users.nginx.extraGroups = [ "dplaned" ];

    # ─── Firewall ─────────────────────────────────────────────────────────
    networking.firewall = lib.mkIf cfg.openFirewall {
      enable              = true;
      allowedTCPPorts     = [ 80 443 ];
    };

    # ─── nginx reverse proxy ──────────────────────────────────────────────
    services.nginx = {
      enable = true;
      # Named upstream so proxy_pass forwards the full URI unmodified.
      # Inline unix: proxy_pass with a path suffix causes nginx to strip
      # the location prefix, breaking all /api/* routes.
      upstreams."dplaned".servers."unix:${cfg.socketPath}" = {};
      virtualHosts."_" = {
        root       = "${cfg.frontendPackage}";
        locations."/" = {
          tryFiles = "$uri $uri/ /index.html";
        };
        locations."/.well-known/acme-challenge/" = {
          proxyPass = "http://127.0.0.1:8080";
        };
        locations."/api/" = {
          proxyPass = "http://dplaned";
          proxyWebsockets = true;
          extraConfig = ''
            proxy_read_timeout 300s;
            proxy_send_timeout 300s;
          '';
        };
        locations."/ws" = {
          proxyPass = "http://dplaned";
          proxyWebsockets = true;
        };
      };
    };

    # ─── DPlaneOS daemon systemd service ────────────────────────────────
    systemd.services.dplaned = {
      description = "DPlaneOS NAS Daemon";
      # postgresql-setup.service creates roles/databases (ensureUsers, ensureDatabases,
      # initialScript) after postgresql.service is up. Ordering only on postgresql.service
      # lets the daemon connect before the dplaneos role exists.
      after       = [ "network.target" "zfs.target" "dplaneos-zfs-gate.service" "postgresql.service" "postgresql-setup.service" "systemd-journald.service" ] ++ lib.optionals cfg.ha.enable [ "haproxy.service" "patroni.service" ];
      requires    = [ "dplaneos-zfs-gate.service" ] ++ lib.optionals cfg.ha.enable [ "patroni.service" ];
      wantedBy    = [ "multi-user.target" ];
      path        = with pkgs; [ coreutils pciutils docker docker-compose postgresql ];

      serviceConfig = {
        Type            = "simple";
        ExecStartPre    = [
          "${pkgs.coreutils}/bin/mkdir -p /var/lib/dplaneos /var/log/dplaneos /run/dplaneos /etc/dplaneos"
          "${pkgs.coreutils}/bin/chmod 755 /run/dplaneos"
          "${pkgs.coreutils}/bin/chmod 755 /var/lib/dplaneos"
          # Verify daemon binary exists
          "/bin/sh -c 'test -x ${cfg.daemonPackage}/bin/dplaned || (echo \"FATAL: Daemon binary not found at ${cfg.daemonPackage}/bin/dplaned\" >&2; exit 1)'"
          # Wait for PostgreSQL to be ready before starting daemon
          # Non-HA PostgreSQL listens on the Unix socket only (services.postgresql.enableTCPIP
          # defaults to false), so probe the socket directory, not localhost.
          "/bin/sh -c 'echo \"[dplaned-pre] Checking PostgreSQL connectivity...\"; for i in $(${pkgs.coreutils}/bin/seq 1 30); do if ${pkgs.postgresql}/bin/pg_isready -h ${if cfg.ha.enable then "localhost" else "/run/postgresql"} -U dplaneos -d dplaneos 2>&1; then echo \"[dplaned-pre] PostgreSQL ready on attempt $i\"; exit 0; fi; ${pkgs.coreutils}/bin/sleep 1; done; echo \"FATAL: PostgreSQL not ready after 30 seconds\" >&2; exit 1'"
        ];
        ExecStart       = "/bin/sh -c 'echo \"[dplaned] Starting with DSN: ${cfg.dbDSN}\"; exec ${cfg.daemonPackage}/bin/dplaned -db-dsn \"${cfg.dbDSN}\" -listen ${cfg.socketPath} -socket-group dplaned'";
        WorkingDirectory = "/var/lib/dplaneos";
        Restart         = "on-failure";
        RestartSec      = "5s";

        # Security hardening (matches systemd/dplaned.service)
        NoNewPrivileges       = true;
        PrivateTmp            = true;
        ProtectSystem         = "strict";
        ProtectHome           = true;
        ReadWritePaths        = [
          # Daemon state - bind-mounted from /persist/dplaneos by impermanence.nix.
          # All writes here physically land on the persist partition.
          "/var/log/dplaneos"
          "/var/lib/dplaneos"   # PostgreSQL state, Patroni config, gitops/
          # OS config files the daemon manages (NixOS owns /etc; daemon owns subtrees)
          "/opt/dplaneos"
          "/etc/dplaneos"
          "/run/dplaneos"
          "/etc/crontab"
          "/etc/cron.d"
          "/etc/exports"
          "/etc/systemd/system"
          # networkdwriter: DPlaneOS writes 50-dplane-*.{network,netdev} here
          # These files survive nixos-rebuild - NixOS only manages its own prefixed files
          "/etc/systemd/network"
          "/etc/systemd/resolved.conf.d"
          "/sys/kernel/config"
          # /etc/samba - removed: NixOS now owns smb.conf via modules/samba.nix
          # Daemon writes to /var/lib/dplaneos/smb-shares.conf instead
          # Cold Tier: rclone FUSE mount root (per-remote subdirs created at runtime)
          cfg.coldTier.rootPath
        ];
        CapabilityBoundingSet = [
          "CAP_SYS_ADMIN"
          "CAP_NET_ADMIN"
          "CAP_DAC_READ_SEARCH"
          "CAP_CHOWN"
          "CAP_FOWNER"
        ];
        AmbientCapabilities   = [
          "CAP_SYS_ADMIN"
          "CAP_NET_ADMIN"
          "CAP_DAC_READ_SEARCH"
          "CAP_CHOWN"
          "CAP_FOWNER"
        ];

        # Logging: journal, so `journalctl -u dplaned` works (README, recovery docs, CI diagnostics).
        StandardOutput = "journal";
        StandardError = "journal";
        SyslogIdentifier = "dplaned";
      };
    };

    # ─── ZFS boot gate ────────────────────────────────────────────────────
    # Blocks dplaned until ZFS pools are ONLINE and writable.
    # Mirrors the logic in systemd/dplaneos-zfs-mount-wait.service.
    systemd.services.dplaneos-zfs-gate = {
      description = "DPlaneOS ZFS Mount Gate";
      after       = [ "zfs.target" ];
      before      = [ "dplaned.service" ];
      wantedBy    = [ "multi-user.target" ];
      serviceConfig = {
        Type            = "oneshot";
        RemainAfterExit = true;
        ExecStart       = pkgs.writeShellScript "zfs-gate" ''
          #!/usr/bin/env sh
          set -e
          timeout=120
          elapsed=0
          while [ $elapsed -lt $timeout ]; do
            pool_list=$(zpool list -H -o health 2>/dev/null || true)
            if [ -z "$pool_list" ]; then
              # No pools exist: first boot or standby node with no imported pools.
              # Either case is valid - pass immediately so dplaned can start.
              echo "ZFS gate: no pools present (first boot or standby node) - gate open"
              exit 0
            fi
            if echo "$pool_list" | grep -q ONLINE; then
              echo "ZFS gate: pools ONLINE - gate open"
              exit 0
            fi
            sleep 2
            elapsed=$((elapsed + 2))
          done
          echo "ZFS gate timeout after ${toString 120}s - pools exist but not ONLINE"
          exit 1
        '';
      };
    };

    # ─── Persistent state directories ─────────────────────────────────────
    systemd.tmpfiles.rules = [
      "d /var/lib/dplaneos        0775 root root -"
      "d /var/log/dplaneos        0755 root root -"
      "d /etc/dplaneos            0755 root root -"
      "d /run/dplaneos            0700 root root -"
      # Cold Tier root: rclone FUSE mounts land under this directory.
      # The daemon creates per-remote subdirectories at mount time.
      "d ${cfg.coldTier.rootPath} 0755 root root -"
    ];
  };
}
