# DPlaneOS NixOS Module
# Declares all system-level requirements: packages, services, users, firewall.
# Imported by flake.nix and usable standalone via imports = [ ./module.nix ];

{ config, lib, pkgs, ... }:

# The Samba and NFS modules are imported below and are available to all
# DPlaneOS installations; both default to enabled.

let
  cfg = config.services.dplaneos;
  # ZFS's record of the imported pools (see dplaneos-zfs-import below).
  zpoolCache = "/var/lib/dplaneos/zpool.cache";
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
  imports = [
    (lib.mkRemovedOptionModule [ "services" "dplaneos" "ha" ] ''
      Patroni-based HA (Patroni, etcd, HAProxy, keepalived) was removed in DPlaneOS v15.
      Remove services.dplaneos.ha from your configuration and set HA up from the web interface:
      pair the nodes, form the cluster with a third vote, create storage groups
      (docs/admin/HIGH-AVAILABILITY.md).'')
  ] ++ [ ./console-network-wizard.nix ./modules/samba.nix ./modules/nfs.nix ./modules/ups.nix ./modules/cluster.nix ];

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
      default = if cfg.database.createLocally then "postgres://dplaneos@/dplaneos?host=/run/postgresql&sslmode=disable"
                else "postgres://dplaneos@localhost/dplaneos?sslmode=disable";
      defaultText = lib.literalExpression ''
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
    };

    s3.enable = lib.mkOption {
      type = lib.types.bool;
      default = false;
      description = ''
        Make the S3 Object Storage page usable: installs Versity Gateway
        (versitygw) for the daemon, which runs it as a unit of its own.
      '';
    };

    database.createLocally = lib.mkOption {
      type    = lib.types.bool;
      default = true;
      description = ''
        Run a local PostgreSQL for the daemon: data in dbPath, role and database
        "dplaneos", reached over the Unix socket with peer authentication (dplaned
        runs as root and is mapped to the dplaneos role). Every node has its own
        database, also in a cluster (nodes exchange configuration changes).
        Set to false to point dbDSN at a database you manage.
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
        # Not autoPrune: it runs "docker system prune", which also deletes
        # every stopped container. A container someone stopped must still be
        # there next week; only unused image layers are cleaned up (below).
        autoPrune.enable = false;
      }
      (lib.mkIf cfg.docker.enableNvidia { enableNvidia = true; })
    ];

    # Weekly: remove dangling image layers and build cache (never containers
    # or volumes).
    systemd.services.dplaneos-docker-image-prune = {
      description = "Remove unused Docker image layers";
      after = [ "docker.service" ];
      requires = [ "docker.service" ];
      serviceConfig.Type = "oneshot";
      script = ''
        ${config.virtualisation.docker.package}/bin/docker image prune -f
        ${config.virtualisation.docker.package}/bin/docker builder prune -f
      '';
    };
    systemd.timers.dplaneos-docker-image-prune = {
      wantedBy = [ "timers.target" ];
      timerConfig = { OnCalendar = "weekly"; Persistent = true; RandomizedDelaySec = "1h"; };
    };

    # ─── SSH ─────────────────────────────────────────────────────────────
    # Who can sign in: administrators of the web interface, with a key
    # (their accounts are in wheel, internal/fileaccounts; keys on the SSH
    # page). Port, password sign-in and root login can be changed on the SSH
    # page: the daemon writes them to a file that is included before the
    # settings below (sshd uses the first value it reads) and reloads sshd.
    services.openssh = {
      enable                  = true;
      # No "Port" line from here: the daemon's file names the port (22
      # unless changed) and opens it in the firewall.
      ports                   = lib.mkDefault [ ];
      openFirewall            = false;
      # Unset here so that they do not come before the daemon's file; the
      # defaults follow the Include below. (A configuration that sets them,
      # like the live system, keeps its values.)
      settings.PasswordAuthentication       = lib.mkDefault null;
      settings.KbdInteractiveAuthentication = lib.mkDefault null;
      settings.PermitRootLogin              = lib.mkDefault null;
      settings.AllowGroups                  = [ "wheel" "root" ];
      # In the persisted directory: clients see the same host after a system
      # update switched to the other system slot.
      hostKeys = [
        { path = "/var/lib/dplaneos/ssh/ssh_host_ed25519_key"; type = "ed25519"; }
        { path = "/var/lib/dplaneos/ssh/ssh_host_rsa_key"; type = "rsa"; bits = 4096; }
      ];
      extraConfig = lib.mkMerge [
        (lib.mkOrder (-100) "Include /var/lib/dplaneos/system/sshd_config.d/*.conf")
        (lib.mkAfter (lib.concatStringsSep "\n" (
          lib.optional (config.services.openssh.settings.PasswordAuthentication == null) "PasswordAuthentication no"
          ++ lib.optional (config.services.openssh.settings.KbdInteractiveAuthentication == null) "KbdInteractiveAuthentication no"
          ++ lib.optional (config.services.openssh.settings.PermitRootLogin == null) "PermitRootLogin no"
        )))
      ];
    };
    security.sudo.enable = true;
    # Replication from another system signs in as an administrator (root
    # does not sign in over SSH) and runs zfs through sudo, without a
    # terminal to type a password into.
    security.sudo.extraRules = [ {
      groups   = [ "wheel" ];
      commands = [ { command = "/run/current-system/sw/bin/zfs"; options = [ "NOPASSWD" ]; } ];
    } ];

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
      # Rules set in the web interface (Firewall page) and the ports of
      # services switched on there. The daemon writes them to files
      # (internal/sysapply) and reloads the firewall; they are read here
      # whenever it starts or reloads, so they need no rebuild.
      #
      #   firewall-rules          "allow tcp 8080 any" / "deny tcp 22 10.0.0.0/8"
      #   firewall-services.d/*   "tcp 21" / "tcp 40000:40100" (one file per service)
      #
      # Deny rules sit in front of everything (chain dplane-fw, first in
      # INPUT, loopback exempt), so they also close ports the system opens.
      {
        extraCommands = lib.mkAfter ''
          dplane_all="iptables${lib.optionalString config.networking.enableIPv6 " ip6tables"}"
          dplane_rule() {
            # $1 iptables|ip6tables|ip46tables, rest: rule
            cmd=$1; shift
            $cmd -w "$@"
          }
          for cmd in $dplane_all; do
            $cmd -w -D INPUT -j dplane-fw 2>/dev/null || true
            $cmd -w -F dplane-fw 2>/dev/null || true
            $cmd -w -X dplane-fw 2>/dev/null || true
            $cmd -w -N dplane-fw
            $cmd -w -A dplane-fw -i lo -j RETURN
          done
          if [ -r /var/lib/dplaneos/system/firewall-rules ]; then
            while read -r action proto port src; do
              case "$proto" in tcp|udp) ;; *) continue ;; esac
              case "$port" in ""|*[!0-9:]*) continue ;; esac
              case "$src" in
                any) cmds="$dplane_all"; match="" ;;
                *:*) cmds="${lib.optionalString config.networking.enableIPv6 "ip6tables"}"; match="-s $src" ;;
                *.*) cmds="iptables"; match="-s $src" ;;
                *) continue ;;
              esac
              case "$action" in
                allow) target=nixos-fw-accept ;;
                deny)  target=nixos-fw-refuse ;;
                *) continue ;;
              esac
              for cmd in $cmds; do
                # shellcheck disable=SC2086
                if [ "$action" = deny ]; then
                  dplane_rule "$cmd" -A dplane-fw -p "$proto" --dport "$port" $match -j "$target"
                else
                  dplane_rule "$cmd" -A nixos-fw -p "$proto" --dport "$port" $match -j "$target"
                fi
              done
            done < /var/lib/dplaneos/system/firewall-rules
          fi
          for f in /var/lib/dplaneos/system/firewall-services.d/*; do
            [ -r "$f" ] || continue
            while read -r proto port; do
              case "$proto" in tcp|udp) ;; *) continue ;; esac
              case "$port" in ""|*[!0-9:]*) continue ;; esac
              ip46tables -w -A nixos-fw -p "$proto" --dport "$port" -j nixos-fw-accept
            done < "$f"
          done
          for cmd in $dplane_all; do
            $cmd -w -I INPUT 1 -j dplane-fw
          done
        '';
        extraStopCommands = lib.mkAfter ''
          dplane_all="iptables${lib.optionalString config.networking.enableIPv6 " ip6tables"}"
          for cmd in $dplane_all; do
            $cmd -w -D INPUT -j dplane-fw 2>/dev/null || true
            $cmd -w -F dplane-fw 2>/dev/null || true
            $cmd -w -X dplane-fw 2>/dev/null || true
          done
        '';
      }
    ];

    # Switched off in the web interface: the firewall does not start.
    systemd.services.firewall.unitConfig.ConditionPathExists = "!/var/lib/dplaneos/system/firewall-disabled";

    # ─── System settings saved in the web interface ───────────────────────
    # Host name, time zone, NTP servers, kernel tuning, firewall rules: the
    # daemon applies them when they are changed; this applies them at boot,
    # before the network comes up (the host name is sent with DHCP requests)
    # and without waiting for the database.
    systemd.services.dplaneos-system-settings = {
      description = "System settings saved in the web interface";
      wantedBy    = [ "multi-user.target" "network-pre.target" ];
      before      = [ "network-pre.target" "systemd-networkd.service" "firewall.service" "systemd-timesyncd.service" "dplaned.service" ];
      after       = [ "systemd-remount-fs.service" "systemd-modules-load.service" ];
      unitConfig.DefaultDependencies = false;
      unitConfig.RequiresMountsFor = "/var/lib/dplaneos";
      path = [ pkgs.systemd ];
      serviceConfig = {
        Type = "oneshot";
        RemainAfterExit = true;
        ExecStart = "${cfg.daemonPackage}/bin/dplaned -apply-system-settings";
      };
    };

    # ─── nginx reverse proxy ──────────────────────────────────────────────
    services.nginx = {
      enable = true;
      # Named upstream so proxy_pass forwards the full URI unmodified.
      # Inline unix: proxy_pass with a path suffix causes nginx to strip
      # the location prefix, breaking all /api/* routes.
      upstreams."dplaned".servers."unix:${cfg.socketPath}" = {};
      # The daemon is told who the client is (X-Real-IP, overwriting what
      # the client sent) and which host name it used: sign-in throttling
      # and the audit log are per client address.
      recommendedProxySettings = true;
      virtualHosts."_" = {
        root       = "${cfg.frontendPackage}";
        # HTTPS from the first boot on: a self-signed certificate until one
        # is activated on the Certificates page (dplaneos-web-cert below;
        # the daemon replaces the content of these files and reloads nginx).
        addSSL            = true;
        sslCertificate    = "/var/lib/dplaneos/tls/web.crt";
        sslCertificateKey = "/var/lib/dplaneos/tls/web.key";
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
        # An update file brought by hand (Updates page): a few hundred MB,
        # written straight to disk by the daemon.
        locations."/api/system/update/upload" = {
          proxyPass = "http://dplaned";
          extraConfig = ''
            client_max_body_size 4g;
            proxy_request_buffering off;
            proxy_read_timeout 3h;
            proxy_send_timeout 3h;
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

    systemd.services.dplaneos-web-cert = {
      description = "Certificate of the web interface";
      before      = [ "nginx.service" ];
      # Named after the host, which the system settings set first.
      after       = [ "dplaneos-system-settings.service" ];
      requiredBy  = [ "nginx.service" ];
      unitConfig.RequiresMountsFor = "/var/lib/dplaneos";
      serviceConfig = { Type = "oneshot"; RemainAfterExit = true; };
      path = [ pkgs.openssl pkgs.coreutils ];
      script = ''
        dir=/var/lib/dplaneos/tls
        mkdir -p "$dir"
        if [ ! -s "$dir/web.crt" ] || [ ! -s "$dir/web.key" ] \
           || ! openssl x509 -in "$dir/web.crt" -noout 2>/dev/null; then
          name=$(cat /proc/sys/kernel/hostname)
          umask 077
          openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:prime256v1 -nodes \
            -days 3650 -subj "/CN=$name" -addext "subjectAltName=DNS:$name,DNS:localhost" \
            -keyout "$dir/web.key.new" -out "$dir/web.crt.new"
          mv "$dir/web.key.new" "$dir/web.key"
          mv "$dir/web.crt.new" "$dir/web.crt"
          echo "created a self-signed certificate for $name"
        fi
        chown root:nginx "$dir" "$dir/web.key"
        chmod 750 "$dir"
        chmod 640 "$dir/web.key"
        chmod 644 "$dir/web.crt"
      '';
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
      after       = [ "network.target" "zfs.target" "dplaneos-zfs-gate.service" "postgresql.service" "postgresql-setup.service" "systemd-journald.service" ];
      wants       = [ "dplaneos-zfs-gate.service" ]
        ++ lib.optionals cfg.database.createLocally [ "postgresql.service" "postgresql-setup.service" ];
      wantedBy    = [ "multi-user.target" ];
      # Every tool the daemon executes by name. A NixOS service PATH only has
      # these packages plus coreutils/findutils/grep/sed/systemd, so anything
      # missing here fails at runtime with "executable file not found".
      path = with pkgs; [
        coreutils pciutils docker docker-compose postgresql
        config.boot.zfs.package          # zfs, zpool (matches the kernel module)
        util-linux                       # lsblk, wipefs, mount, umount, mountpoint, eject, ionice, logger
        smartmontools hdparm nvme-cli lsscsi sg3_utils dmidecode ipmitool nut
        config.services.dplaneos.samba.package  # smbcontrol, smbstatus, testparm, net, wbinfo, pdbedit
        shadow                           # useradd, usermod, groupadd: accounts of the users (fileaccounts)
        avahi                            # avahi-daemon --reload (Time Machine discovery)
        nfs-utils acl                    # exportfs; getfacl/setfacl
        iproute2 iputils traceroute dnsutils nftables
        iptables                         # reading the firewall's open ports (Firewall page)
        git openssh rsync rclone pv      # GitOps, replication, cloud sync
        gnutar gzip curl which kmod procps fuse openssl nginx targetcli-fb
        bash  # timer units run bash -c "curl ..." (internal/systemd resolves ExecStart from this PATH)
        getent                           # getent passwd/group (accounts, quotas)
        config.nix.package config.system.build.nixos-rebuild
      ] ++ lib.optional cfg.ftp.enable vsftpd
        ++ lib.optional cfg.s3.enable versitygw;

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

        # The daemon is the system's administrator: it manages files of every
        # user, runs backups, loads kernel modules, sends raw SCSI and ARP
        # packets and switches system configurations. It therefore runs as
        # root with all capabilities. A reduced capability set (it had one:
        # SYS_ADMIN, NET_ADMIN, DAC_READ_SEARCH, CHOWN, FOWNER) contained
        # nothing, since CAP_SYS_ADMIN alone is equivalent to root, and it
        # broke the file manager and backups in folders owned by users, disk
        # power management, reservation probes and gratuitous ARP.
        # What limits the daemon is in the daemon: sessions and RBAC, the
        # command whitelist and the path checks.
        #
        # No mount namespace either (ProtectSystem/ProtectHome/PrivateTmp/
        # ReadWritePaths): the daemon creates and mounts ZFS datasets, imports
        # pools and mounts cold-tier FUSE remotes. In a private namespace those
        # mounts stay invisible to Samba, NFS and the host.
        NoNewPrivileges = true;

        # Logging: journal, so `journalctl -u dplaned` works (README, recovery docs, CI diagnostics).
        StandardOutput = "journal";
        StandardError = "journal";
        SyslogIdentifier = "dplaned";
      };
    };

    # ─── Unique ZFS host ID ───────────────────────────────────────────────
    # ZFS uses the host ID to tell systems apart: importing a pool that
    # another system has in use is refused, and multihost (the protection of
    # shared disks) needs a non-zero ID that differs between nodes. An
    # installed image is the same on every machine, so the ID cannot come from
    # the configuration: it is generated at first boot and kept. /etc/hostid
    # points at it. networking.hostId only satisfies the NixOS ZFS module.
    networking.hostId = lib.mkDefault "00000000";
    environment.etc.hostid.source = lib.mkForce "/var/lib/dplaneos/hostid";
    systemd.services.dplaneos-hostid = {
      description = "Unique ZFS host ID";
      wantedBy    = [ "zfs-import.target" "multi-user.target" ];
      before      = [ "zfs-import.target" "dplaneos-zfs-import.service" "dplaned.service" ];
      after       = [ "systemd-remount-fs.service" ];
      # zfs-import.target comes before local-fs.target: no default
      # dependencies (they would order this after basic.target).
      unitConfig.DefaultDependencies = false;
      unitConfig.RequiresMountsFor = "/var/lib/dplaneos";
      serviceConfig = { Type = "oneshot"; RemainAfterExit = true; };
      script = ''
        f=/var/lib/dplaneos/hostid
        mkdir -p /var/lib/dplaneos
        current=""
        if [ -s "$f" ]; then current=$(${pkgs.coreutils}/bin/od -An -tx1 "$f" | ${pkgs.coreutils}/bin/tr -d ' \n'); fi
        if [ "''${#current}" != 8 ] || [ "$current" = 00000000 ]; then
          while :; do
            ${pkgs.coreutils}/bin/head -c 4 /dev/urandom > "$f.new"
            [ "$(${pkgs.coreutils}/bin/od -An -tx1 "$f.new" | ${pkgs.coreutils}/bin/tr -d ' \n')" != 00000000 ] && break
          done
          ${pkgs.coreutils}/bin/chmod 644 "$f.new"
          ${pkgs.coreutils}/bin/mv "$f.new" "$f"
          echo "generated a ZFS host ID"
        fi
      '';
    };

    # ─── Network configuration set in the web interface ───────────────────
    # The daemon renders addresses, bonds, VLANs and DNS servers from its
    # database into /var/lib/dplaneos/network ("10-dplane-*"). At boot they
    # are put where systemd-networkd and systemd-resolved read them, before
    # either starts: the network comes up as configured without waiting for
    # the database. The files sort before the system's default (DHCP on
    # every wired interface), which keeps applying to all other interfaces.
    systemd.services.dplaneos-network = {
      description = "Network configuration set in the web interface";
      wantedBy    = [ "systemd-networkd.service" "multi-user.target" ];
      before      = [ "systemd-networkd.service" "systemd-resolved.service" "network-pre.target" ];
      wants       = [ "network-pre.target" ];
      unitConfig.DefaultDependencies = false;
      unitConfig.RequiresMountsFor = "/var/lib/dplaneos";
      serviceConfig = { Type = "oneshot"; RemainAfterExit = true; };
      script = ''
        src=/var/lib/dplaneos/network
        ${pkgs.coreutils}/bin/mkdir -p /run/systemd/network /run/systemd/resolved.conf.d
        ${pkgs.coreutils}/bin/rm -f /run/systemd/network/10-dplane-* /run/systemd/resolved.conf.d/10-dplane-*
        [ -d "$src" ] || exit 0
        for f in "$src"/10-dplane-*.network "$src"/10-dplane-*.netdev; do
          [ -e "$f" ] || continue
          ${pkgs.coreutils}/bin/install -m 644 "$f" /run/systemd/network/
        done
        if [ -e "$src/10-dplane-dns.conf" ]; then
          ${pkgs.coreutils}/bin/install -m 644 "$src/10-dplane-dns.conf" /run/systemd/resolved.conf.d/
        fi
      '';
    };

    # ─── Import the pools at boot ─────────────────────────────────────────
    # NixOS only imports pools named in its configuration; the pools of an
    # appliance are created in the web interface. Every pool that was
    # imported when the system went down is imported again from ZFS's own
    # record of imported pools: not the pools that were exported on purpose,
    # and not the pools of shared storage groups (cachefile=none: their owner
    # imports them once the cluster agrees who that is).
    # The record is kept in /var/lib/dplaneos (not /etc/zfs), which survives
    # an update to the other system slot.
    boot.extraModprobeConfig = "options zfs spa_config_path=${zpoolCache}";
    systemd.services.dplaneos-zfs-import = {
      description = "Import the ZFS pools of this system";
      wantedBy    = [ "zfs-import.target" ];
      before      = [ "zfs-import.target" "zfs-mount.service" "dplaneos-zfs-gate.service" ];
      after       = [ "systemd-udev-settle.service" "systemd-modules-load.service" "dplaneos-hostid.service" ];
      wants       = [ "systemd-udev-settle.service" ];
      unitConfig.DefaultDependencies = false;
      unitConfig.RequiresMountsFor = "/var/lib/dplaneos";
      serviceConfig = { Type = "oneshot"; RemainAfterExit = true; };
      script = ''
        zpool=${config.boot.zfs.package}/bin/zpool
        cache=${zpoolCache}
        if [ ! -s "$cache" ]; then
          echo "no pools recorded"
          exit 0
        fi
        # Disks of a large enclosure can take a while to appear.
        for attempt in 1 2 3 4 5 6; do
          "$zpool" import -c "$cache" -a -N 2>&1 || true
          # Nothing left to import from the record: done.
          if ! "$zpool" import -c "$cache" 2>/dev/null | ${pkgs.gnugrep}/bin/grep -q '^ *pool:'; then
            exit 0
          fi
          echo "not every recorded pool could be imported yet (attempt $attempt)"
          ${pkgs.coreutils}/bin/sleep 5
        done
        echo "giving up on the pools listed above: their disks did not appear"
        "$zpool" import -c "$cache" 2>&1 || true
        exit 0
      '';
    };

    # ─── ZFS boot gate ────────────────────────────────────────────────────
    # Runs after the pools were imported and mounted, and gives them a moment
    # to settle. It orders the daemon and Docker after the pools (a container
    # started before its pool is mounted would run on an empty directory and
    # block the mount), but it never keeps the daemon from starting: a pool
    # that is degraded or missing is exactly when the web interface is needed.
    # The marker tells the daemon whether every pool was usable
    # (GET /api/system/zfs-gate-status).
    systemd.services.dplaneos-zfs-gate = {
      description = "Wait for the ZFS pools";
      after       = [ "zfs.target" "zfs-import.target" "zfs-mount.service" ];
      before      = [ "dplaned.service" "docker.service" ];
      wantedBy    = [ "multi-user.target" ];
      serviceConfig = {
        Type            = "oneshot";
        RemainAfterExit = true;
        ExecStart       = pkgs.writeShellScript "zfs-gate" ''
          zpool=${config.boot.zfs.package}/bin/zpool
          marker=/run/dplaneos/zfs-ready
          mkdir -p /run/dplaneos
          rm -f "$marker"
          elapsed=0
          while [ "$elapsed" -lt 60 ]; do
            # No pools (first boot, or a node that owns none) or every pool
            # usable (ONLINE or DEGRADED): ready.
            if ! "$zpool" list -H -o health 2>/dev/null | grep -qvE '^(ONLINE|DEGRADED)$'; then
              : > "$marker"
              echo "ZFS gate: pools ready"
              exit 0
            fi
            sleep 2
            elapsed=$((elapsed + 2))
          done
          echo "ZFS gate: not every pool is usable after 60s; starting anyway:"
          "$zpool" list -H -o name,health || true
          exit 0
        '';
      };
    };
    # Docker waits for the pools as well (it is ordered after the gate above).
    systemd.services.docker = {
      after = [ "dplaneos-zfs-gate.service" ];
      wants = [ "dplaneos-zfs-gate.service" ];
    };

    # ─── Persistent state directories ─────────────────────────────────────
    systemd.tmpfiles.rules = [
      # The iSCSI target keeps the reservations of its initiators here;
      # without the directory it refuses every reservation command.
      "d /etc/target       0700 root root -"
      "d /etc/target/pr    0700 root root -"
      "d /etc/target/alua  0700 root root -"
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
