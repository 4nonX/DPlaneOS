# D-PlaneOS NixOS Module
# Declares all system-level requirements: packages, services, users, firewall.
# Imported by flake.nix and usable standalone via imports = [ ./module.nix ];

{ config, lib, pkgs, ... }:

# Samba integration is provided by ./modules/samba.nix which is imported
# separately in configuration.nix. This module focuses on the daemon itself.

let
  cfg = config.services.dplaneos;
in {
  options.services.dplaneos = {
    enable = lib.mkEnableOption "D-PlaneOS NAS daemon";

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

    dbPath = lib.mkOption {
      type    = lib.types.path;
      default = "/var/lib/dplaneos/dplaneos.db";
      description = "Path to the SQLite database file.";
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
  };

  config = lib.mkIf cfg.enable {
    # ─── Required system packages ────────────────────────────────────────
    environment.systemPackages = with pkgs; [
      zfs
      docker
      docker-compose
      nginx
      sqlite
      # samba — now managed by modules/samba.nix (services.samba)
      nfs-utils
      smartmontools
      ipmitool          # optional: BMC/IPMI sensor readout
      pv                # used for replication bandwidth throttling
      rclone            # cloud sync
      ufw               # firewall management
      openssh
      git
      curl
      bash
      coreutils
    ];

    # ─── ZFS ─────────────────────────────────────────────────────────────
    boot.supportedFilesystems = [ "zfs" ];
    boot.zfs.forceImportRoot  = false;   # never force-import root pool
    services.zfs.autoScrub.enable   = true;
    services.zfs.autoScrub.interval = "monthly";
    services.zfs.trim.enable        = true;

    # ─── Docker ──────────────────────────────────────────────────────────
    virtualisation.docker = {
      enable         = true;
      storageDriver  = "overlay2";
      autoPrune.enable = true;
    };

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
      after       = [ "network.target" "zfs.target" "dplaneos-zfs-gate.service" ];
      requires    = [ "dplaneos-zfs-gate.service" ];
      wantedBy    = [ "multi-user.target" ];

      serviceConfig = {
        Type            = "simple";
        ExecStartPre    = [
          "${pkgs.coreutils}/bin/mkdir -p /var/lib/dplaneos /var/log/dplaneos /run/dplaneos /etc/dplaneos"
          "${pkgs.coreutils}/bin/chmod 755 /run/dplaneos"
        ];
        ExecStart       = "${pkgs.dplaneos-daemon}/bin/dplaned -db ${cfg.dbPath} -listen ${cfg.listenAddress}:${toString cfg.listenPort}";
        WorkingDirectory = "/opt/dplaneos";
        Restart         = "on-failure";
        RestartSec      = "5s";

        # Security hardening (matches systemd/dplaned.service)
        NoNewPrivileges       = true;
        PrivateTmp            = true;
        ProtectSystem         = "strict";
        ProtectHome           = true;
        ReadWritePaths        = [
          # Daemon state — bind-mounted from /persist/dplaneos by impermanence.nix.
          # All writes here physically land on the persist partition.
          "/var/log/dplaneos"
          "/var/lib/dplaneos"   # dplaneos.db, audit.db, audit.key, gitops/
          # OS config files the daemon manages (NixOS owns /etc; daemon owns subtrees)
          "/opt/dplaneos"
          "/etc/dplaneos"
          "/run/dplaneos"
          "/etc/crontab"
          "/etc/exports"
          # networkdwriter: D-PlaneOS writes 50-dplane-*.{network,netdev} here
          # These files survive nixos-rebuild — NixOS only manages its own prefixed files
          "/etc/systemd/network"
          "/etc/systemd/resolved.conf.d"
          # /etc/samba — removed: NixOS now owns smb.conf via modules/samba.nix
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
          #!/bin/sh
          set -e
          timeout=120
          elapsed=0
          while [ $elapsed -lt $timeout ]; do
            if zpool list -H -o health 2>/dev/null | grep -q ONLINE; then
              echo "ZFS pools ONLINE — gate open"
              exit 0
            fi
            sleep 2
            elapsed=$((elapsed + 2))
          done
          echo "ZFS gate timeout after ${toString 120}s — pools not ONLINE"
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
    ];
  };
}
