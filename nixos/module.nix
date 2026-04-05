# D-PlaneOS NixOS Module
# Declares all system-level requirements: packages, services, users, firewall.
# Imported by flake.nix and usable standalone via imports = [ ./module.nix ];

{ config, lib, pkgs, ... }:

# Samba integration is provided by ./modules/samba.nix which is imported
# separately in configuration.nix. This module focuses on the daemon itself.

let
  cfg = config.services.dplaneos;
in {
  imports = [ ./ha.nix ./console-network-wizard.nix ];

  options.services.dplaneos = {
    enable = lib.mkEnableOption "D-PlaneOS NAS daemon";

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

    listenAddress = lib.mkOption {
      type    = lib.types.str;
      default = "127.0.0.1";
      description = "Address the daemon listens on (nginx proxies to it).";
    };

    listenPort = lib.mkOption {
      type    = lib.types.port;
      default = 9000;
      description = "Port the daemon listens on.";
    };

    dbDSN = lib.mkOption {
      type    = lib.types.str;
      default = if cfg.ha.enable 
                then "postgres://dplaneos@localhost:5000/dplaneos?sslmode=disable"
                else "postgres://dplaneos@localhost/dplaneos?sslmode=disable";
      description = "PostgreSQL Data Source Name.";
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
  };

  config = lib.mkIf cfg.enable {
    # ─── Required system packages ────────────────────────────────────────
    environment.systemPackages = [
      pkgs.zfs
      pkgs.docker
      pkgs.docker-compose
      pkgs.nginx
      pkgs.nfs-utils
      pkgs.smartmontools
      pkgs.ipmitool
      pkgs.pv
      pkgs.rclone
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
            echo "zfs_event:$SEVERITY:$ZEVENT_POOL:$ZEVENT_SUBCLASS:$ZEVENT_VDEV_STATE_STR" | timeout 2 nc -U "$DAEMON_SOCKET" 2>/dev/null || true
        fi
        exit 0
      '';
      mode = "0755";
    };

    # NVMe-oF target (kernel nvmet) — modules load at boot; dplaned writes configfs at runtime.
    boot.kernelModules = [ "nvmet" "nvmet-tcp" ];

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

    # ─── Firewall ─────────────────────────────────────────────────────────
    networking.firewall = lib.mkIf cfg.openFirewall {
      enable              = true;
      allowedTCPPorts     = [ 80 443 ];
    };

    # ─── nginx reverse proxy ──────────────────────────────────────────────
    services.nginx = {
      enable = true;
      virtualHosts."_" = {
        root       = "/opt/dplaneos/app";
        locations."/" = {
          tryFiles = "$uri $uri/ /index.html";
        };
        locations."/.well-known/acme-challenge/" = {
          proxyPass = "http://127.0.0.1:8080";
        };
        locations."/api/" = {
          proxyPass = "http://${cfg.listenAddress}:${toString cfg.listenPort}";
          proxyWebsockets = true;
          extraConfig = ''
            proxy_read_timeout 300s;
            proxy_send_timeout 300s;
          '';
        };
        locations."/ws" = {
          proxyPass = "http://${cfg.listenAddress}:${toString cfg.listenPort}";
          proxyWebsockets = true;
        };
      };
    };

    # ─── D-PlaneOS daemon systemd service ────────────────────────────────
    systemd.services.dplaned = {
      description = "D-PlaneOS NAS Daemon";
      after       = [ "network.target" "zfs.target" "dplaneos-zfs-gate.service" ] ++ lib.optionals cfg.ha.enable [ "haproxy.service" "patroni.service" ];
      requires    = [ "dplaneos-zfs-gate.service" ] ++ lib.optionals cfg.ha.enable [ "patroni.service" ];
      wantedBy    = [ "multi-user.target" ];
      path        = with pkgs; [ coreutils pciutils docker docker-compose ];

      serviceConfig = {
        Type            = "simple";
        Environment     = lib.optionals cfg.ha.enable [ "PGPASSFILE=/etc/dplaneos/pg-password" ];
        ExecStartPre    = [
          "${pkgs.coreutils}/bin/mkdir -p /var/lib/dplaneos /var/log/dplaneos /run/dplaneos /etc/dplaneos"
          "${pkgs.coreutils}/bin/chmod 755 /run/dplaneos"
        ];
        ExecStart       = "${cfg.daemonPackage}/bin/dplaned -db-dsn \"${cfg.dbDSN}\" -listen ${cfg.listenAddress}:${toString cfg.listenPort}";
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
          # networkdwriter: D-PlaneOS writes 50-dplane-*.{network,netdev} here
          # These files survive nixos-rebuild - NixOS only manages its own prefixed files
          "/etc/systemd/network"
          "/etc/systemd/resolved.conf.d"
          "/sys/kernel/config"
          # /etc/samba - removed: NixOS now owns smb.conf via modules/samba.nix
          # Daemon writes to /var/lib/dplaneos/smb-shares.conf instead
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
      };
    };

    # ─── ZFS boot gate ────────────────────────────────────────────────────
    # Blocks dplaned until ZFS pools are ONLINE and writable.
    # Mirrors the logic in systemd/dplaneos-zfs-mount-wait.service.
    systemd.services.dplaneos-zfs-gate = {
      description = "D-PlaneOS ZFS Mount Gate";
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
            if zpool list -H -o health 2>/dev/null | grep -q ONLINE; then
              echo "ZFS pools ONLINE - gate open"
              exit 0
            fi
            sleep 2
            elapsed=$((elapsed + 2))
          done
          echo "ZFS gate timeout after ${toString 120}s - pools not ONLINE"
          exit 1
        '';
      };
    };

    # ─── Persistent state directories ─────────────────────────────────────
    systemd.tmpfiles.rules = [
      "d /var/lib/dplaneos 0775 root root -"
      "d /var/log/dplaneos 0755 root root -"
      "d /etc/dplaneos     0755 root root -"
      "d /opt/dplaneos/app 0755 root root -"
      "d /run/dplaneos     0700 root root -"
    ];
  };
}
