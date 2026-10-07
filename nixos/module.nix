# DPlaneOS NixOS Module
# Declares all system-level requirements: packages, services, users, firewall.
# Imported by flake.nix and usable standalone via imports = [ ./module.nix ];

{ config, lib, pkgs, ... }:

# Samba, NFS, and fenced modules are imported below and are available to all
# DPlaneOS installations. Samba and NFS default to enabled; fenced defaults to disabled.

let
  cfg = config.services.dplaneos;
  dplanedNamespaceOpts =
    let sc = config.systemd.services.dplaned.serviceConfig or {};
    in builtins.filter (o: sc ? ${o} && !(builtins.elem sc.${o} [ false "no" "off" [] ])) [
      "ReadWritePaths" "ReadOnlyPaths" "InaccessiblePaths" "BindPaths" "BindReadOnlyPaths"
      "TemporaryFileSystem" "PrivateTmp" "PrivateMounts" "PrivateDevices" "ProtectSystem"
      "ProtectHome" "ProtectKernelTunables" "ProtectKernelModules" "ProtectControlGroups"
    ];
  # pg_isready target for the pre-start probe, following the DSN.
  pgProbeHost = if lib.hasInfix "host=/run/postgresql" cfg.dbDSN then "/run/postgresql" else "localhost";
  # NixOS owns smb.conf (modules/samba.nix) and includes the daemon's share file.
  # Without this flag the daemon falls back to /etc/samba/smb.conf, a read-only
  # store link, and UI share changes never reach Samba.
  smbConfFlag = lib.optionalString config.services.dplaneos.samba.enable
    " -smb-conf ${config.services.dplaneos.samba.sharesConfPath}";
  secretsFlags = " -secrets-key ${cfg.secrets.keyFile}"
    + lib.optionalString (cfg.secrets.fallbackKeyFile != null)
      " -secrets-key-fallback ${cfg.secrets.fallbackKeyFile}";
in {
  imports = [ ./ha.nix ./console-network-wizard.nix ./modules/samba.nix ./modules/nfs.nix ./modules/fenced.nix ./modules/ctdb.nix ./modules/ups.nix ./modules/cluster.nix ];

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
      default = if cfg.ha.enable then "postgres://dplaneos@localhost:5000/dplaneos?sslmode=disable"
                else if cfg.database.createLocally then "postgres://dplaneos@/dplaneos?host=/run/postgresql&sslmode=disable"
                else "postgres://dplaneos@localhost/dplaneos?sslmode=disable";
      defaultText = lib.literalExpression ''
        HA: localhost:5000 (HAProxy -> Patroni primary)
        database.createLocally: Unix socket /run/postgresql
        otherwise: localhost'';
      description = "PostgreSQL Data Source Name.";
    };

    ftp = {
      enable = lib.mkOption {
        type = lib.types.bool;
        default = true;
        description = ''
          Make the FTP/FTPS page usable: installs vsftpd for the daemon (which runs
          it as a runtime unit with its config in /var/lib/dplaneos/ftp) and the
          PAM service vsftpd needs for local-user logins.
        '';
      };
      openFirewall = lib.mkOption {
        type = lib.types.bool;
        default = false;
        description = "Open TCP 21 and the default passive range 40000-40100 for FTP.";
      };
    };

    s3.enable = lib.mkOption {
      type = lib.types.bool;
      default = false;
      description = ''
        Make the S3 Object Storage page usable: installs MinIO for the daemon,
        which runs it as a runtime unit. Off by default because of its size.
      '';
    };

    database.createLocally = lib.mkOption {
      type    = lib.types.bool;
      default = !cfg.ha.enable;
      defaultText = lib.literalExpression "!config.services.dplaneos.ha.enable";
      description = ''
        Run a local PostgreSQL for the daemon: data in dbPath, role and database
        "dplaneos", reached over the Unix socket with peer authentication (dplaned
        runs as root and is mapped to the dplaneos role). With HA, Patroni manages
        PostgreSQL instead. Set to false to point dbDSN at a database you manage.
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

    secrets = {
      keyFile = lib.mkOption {
        type    = lib.types.str;
        default = "/var/lib/dplaneos/secrets.key";
        description = ''
          AES-256 key (32 bytes) that encrypts secrets stored in the database
          (Git tokens, LDAP/AD/OIDC/SMTP passwords, TOTP seeds). Created on
          first start if missing. Both nodes of an HA pair must use the same
          key; give it as a path outside the Nix store (copy it once from the
          first node, or deploy it with agenix/sops-nix).
        '';
      };
      fallbackKeyFile = lib.mkOption {
        type    = lib.types.nullOr lib.types.str;
        default = null;
        description = ''
          Previous key of this node, kept while moving to a shared key: values
          only this key opens are re-sealed under keyFile at startup. Remove it
          once GET /api/system/secrets/status reports healthy.
        '';
      };
    };

    coldTier = {
      rootPath = lib.mkOption {
        type    = lib.types.str;
        default = "/mnt/cold";
        description = ''
          Base directory under which rclone FUSE mounts are created by the daemon.
          The daemon creates per-remote subdirectories here (e.g. /mnt/cold/s3-backup).
          This path is created at boot.
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
    } {
      # Any of these gives dplaned a private mount namespace (a slave of the
      # host): datasets it creates or imports are then mounted only inside the
      # service, invisible to Samba, NFS and users until a reboot.
      assertion = dplanedNamespaceOpts == [];
      message =
        "systemd.services.dplaned must not use mount-namespace options " +
        "(${lib.concatStringsSep ", " dplanedNamespaceOpts}): its ZFS mounts " +
        "would not be visible outside the service.";
    }];

    # ─── Required system packages ────────────────────────────────────────
    environment.systemPackages = [
      config.boot.zfs.package
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

    # ZED selects zedlets by the prefix before the first "-": "all-" runs
    # for every event. "dplaneos-notify.sh" matched no event and never ran.
    environment.etc."zfs/zed.d/all-dplaneos-notify.sh" = {
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

        ${pkgs.util-linux}/bin/logger -t "$LOG_TAG" "[$SEVERITY] Pool=$ZEVENT_POOL Event=$ZEVENT_SUBCLASS State=$ZEVENT_VDEV_STATE_STR Device=$ZEVENT_VDEV_PATH"

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
    networking.firewall = lib.mkMerge [
      (lib.mkIf cfg.openFirewall {
        enable              = true;
        allowedTCPPorts     = [ 80 443 ];
      })
      # FTP control port and the default passive range (FTP page defaults).
      (lib.mkIf (cfg.ftp.enable && cfg.ftp.openFirewall) {
        allowedTCPPorts      = [ 21 ];
        allowedTCPPortRanges = [ { from = 40000; to = 40100; } ];
      })
    ];

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
        # Replication streams between nodes (replicated storage groups): no
        # body size limit, no buffering, no time limit on a long send.
        locations."/api/config/sync/peer/zfs-recv" = {
          proxyPass = "http://dplaned";
          extraConfig = ''
            client_max_body_size 0;
            proxy_request_buffering off;
            proxy_read_timeout 24h;
            proxy_send_timeout 24h;
          '';
        };
        locations."/ws" = {
          proxyPass = "http://dplaned";
          proxyWebsockets = true;
        };
      };
    };

    # ─── FTP: PAM service for vsftpd local-user logins ─────────────────
    security.pam.services.vsftpd = lib.mkIf cfg.ftp.enable { };

    # ─── Local PostgreSQL (non-HA) ─────────────────────────────────────
    # Without HA nothing else provides the daemon's database. Data lives in
    # dbPath (under /var/lib/dplaneos, persisted by impermanence).
    services.postgresql = lib.mkIf cfg.database.createLocally {
      enable          = true;
      # Above nixpkgs' mkDefault (/var/lib/postgresql/<ver>), below a plain user setting.
      dataDir         = lib.mkOverride 900 cfg.dbPath;
      ensureDatabases = [ "dplaneos" ];
      ensureUsers     = [ {
        name = "dplaneos";
        ensureDBOwnership = true;
        ensureClauses.createdb = true;
      } ];
      # dplaned runs as root and connects as role dplaneos over the socket.
      identMap = ''
        dplaneos root     dplaneos
        dplaneos dplaneos dplaneos
      '';
      authentication = lib.mkBefore ''
        local dplaneos dplaneos peer map=dplaneos
      '';
    };

    # Never silently start an empty cluster next to existing data: if dataDir has
    # no cluster yet but one exists in the NixOS default location, stop and say so.
    systemd.services.postgresql.preStart = lib.mkIf cfg.database.createLocally (lib.mkBefore ''
      if [ ! -e "${config.services.postgresql.dataDir}/PG_VERSION" ]; then
        for existing in /var/lib/postgresql/*/PG_VERSION; do
          if [ -e "$existing" ] && [ "$(dirname "$existing")" != "${config.services.postgresql.dataDir}" ]; then
            echo "DPlaneOS: refusing to initialise an empty database in ${config.services.postgresql.dataDir}:" >&2
            echo "an existing cluster was found in $(dirname "$existing")." >&2
            echo "Move it there, set services.postgresql.dataDir to it, or set" >&2
            echo "services.dplaneos.database.createLocally = false and point dbDSN at it." >&2
            exit 1
          fi
        done
      fi
    '');

    # ─── DPlaneOS daemon systemd service ────────────────────────────────
    systemd.services.dplaned = {
      description = "DPlaneOS NAS Daemon";
      # postgresql-setup.service creates roles/databases (ensureUsers, ensureDatabases,
      # initialScript) after postgresql.service is up. Ordering only on postgresql.service
      # lets the daemon connect before the dplaneos role exists.
      after       = [ "network.target" "zfs.target" "dplaneos-zfs-gate.service" "postgresql.service" "postgresql-setup.service" "systemd-journald.service" ] ++ lib.optionals cfg.ha.enable [ "haproxy.service" "patroni.service" ];
      requires    = [ "dplaneos-zfs-gate.service" ] ++ lib.optionals cfg.ha.enable [ "patroni.service" ];
      wants       = lib.optionals cfg.database.createLocally [ "postgresql.service" "postgresql-setup.service" ];
      wantedBy    = [ "multi-user.target" ];
      # Every tool the daemon executes by name. A NixOS service PATH only has
      # these packages plus coreutils/findutils/grep/sed/systemd, so anything
      # missing here fails at runtime with "executable file not found".
      path = with pkgs; [
        coreutils pciutils docker docker-compose postgresql
        config.boot.zfs.package          # zfs, zpool (matches the kernel module)
        util-linux                       # lsblk, wipefs, mount, umount, mountpoint, eject, ionice, logger
        smartmontools hdparm nvme-cli lsscsi sg3_utils dmidecode ipmitool nut
        samba                            # smbcontrol, smbstatus, testparm, net, wbinfo
        avahi                            # avahi-daemon --reload (Time Machine discovery)
        nfs-utils acl krb5               # exportfs; getfacl/setfacl; kinit/klist
        iproute2 iputils traceroute dnsutils nftables
        git openssh rsync rclone pv      # GitOps, replication, cloud sync
        gnutar gzip curl which kmod procps fuse openssl nginx targetcli-fb
        bash  # timer units run bash -c "curl ..." (internal/systemd resolves ExecStart from this PATH)
        (lib.getBin glibc)               # getent
        config.nix.package config.system.build.nixos-rebuild
      ] ++ lib.optional cfg.ftp.enable vsftpd
        ++ lib.optional cfg.s3.enable minio;

      serviceConfig = {
        Type            = "simple";
        ExecStartPre    = [
          "${pkgs.coreutils}/bin/mkdir -p /var/lib/dplaneos /var/log/dplaneos /run/dplaneos /etc/dplaneos"
          "${pkgs.coreutils}/bin/chmod 755 /run/dplaneos"
          "${pkgs.coreutils}/bin/chmod 755 /var/lib/dplaneos"
          # Verify daemon binary exists
          "/bin/sh -c 'test -x ${cfg.daemonPackage}/bin/dplaned || (echo \"FATAL: Daemon binary not found at ${cfg.daemonPackage}/bin/dplaned\" >&2; exit 1)'"
          # Wait for PostgreSQL to be ready before starting daemon
          # Probe where the DSN points: the Unix socket directory for socket DSNs
          # (NixOS PostgreSQL has no TCP listener unless enableTCPIP is set), else localhost.
          "/bin/sh -c 'echo \"[dplaned-pre] Checking PostgreSQL connectivity...\"; for i in $(${pkgs.coreutils}/bin/seq 1 30); do if ${pkgs.postgresql}/bin/pg_isready -h ${pgProbeHost} -U dplaneos -d dplaneos 2>&1; then echo \"[dplaned-pre] PostgreSQL ready on attempt $i\"; exit 0; fi; ${pkgs.coreutils}/bin/sleep 1; done; echo \"FATAL: PostgreSQL not ready after 30 seconds\" >&2; exit 1'"
        ];
        ExecStart       = "/bin/sh -c 'echo \"[dplaned] Starting with DSN: ${cfg.dbDSN}\"; exec ${cfg.daemonPackage}/bin/dplaned -db-dsn \"${cfg.dbDSN}\" -listen ${cfg.socketPath} -socket-group dplaned${smbConfFlag}${secretsFlags}'";
        WorkingDirectory = "/var/lib/dplaneos";
        Restart         = "on-failure";
        RestartSec      = "5s";

        # Security hardening (matches systemd/dplaned.service)
        NoNewPrivileges       = true;
        # No mount namespace (ProtectSystem/ProtectHome/PrivateTmp/ReadWritePaths):
        # the daemon creates and mounts ZFS datasets, imports pools and mounts
        # cold-tier FUSE remotes. In a private namespace those mounts stay
        # invisible to Samba, NFS and the host (systemd makes the namespace a
        # slave), and mountpoints outside ReadWritePaths cannot be created.
        # It runs as root with CAP_SYS_ADMIN, so the namespace protected little.
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
            # Absolute path: a oneshot's PATH has no zpool, and a missing
            # binary must not read as "no pools".
            pool_list=$(${config.boot.zfs.package}/bin/zpool list -H -o health 2>/dev/null || true)
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
    ] ++ lib.optionals cfg.database.createLocally [
      # A custom PostgreSQL dataDir must exist and belong to postgres before it starts.
      "d ${config.services.postgresql.dataDir} 0700 postgres postgres -"
    ];
  };
}
