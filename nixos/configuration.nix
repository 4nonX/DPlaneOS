# ═══════════════════════════════════════════════════════════════
#  D-PlaneOS v5.3.2 : NixOS Configuration
# ═══════════════════════════════════════════════════════════════
#
#  The Immutable NAS: NixOS (system) + ZFS (data) + GitOps (containers)
#
#  Install:
#    1. git clone https://github.com/4nonX/D-PlaneOS && cd D-PlaneOS/nixos
#    2. sudo bash setup-nixos.sh          # auto-configures everything
#    3. sudo nixos-rebuild switch --flake .#dplaneos
#
#  Update:  git pull && sudo nixos-rebuild switch --flake .#dplaneos
#  Rollback: sudo nixos-rebuild switch --rollback
#
# ═══════════════════════════════════════════════════════════════

{ config, pkgs, lib, dplaned, dplaneos-frontend, dplaneos-recovery, ... }:

# dplane-generated.nix is the v5.0 JSON-to-Nix bridge.
# The daemon writes /var/lib/dplaneos/dplane-state.json (pure JSON).
# That file is read here at eval time - no dynamic Nix syntax, no Surgeon.
# setup-nixos.sh installs the bridge and seeds an empty state file on first boot.

let

  # ┌─────────────────────────────────────────────────────────┐
  # │  YOUR NAS CONFIGURATION : edit these values             │
  # │  (setup-nixos.sh fills these in automatically)          │
  # └─────────────────────────────────────────────────────────┘

  # ZFS pool names : must exist before first boot
  # setup-nixos.sh auto-detects your pools
  zpools = [ "tank" ];

  # ZFS pool mountpoints : dplaned needs write access
  # Default: /poolname. Override if your pools mount elsewhere.
  zpoolMountpoints = map (p: "/${p}") zpools;

  # Samba workgroup
  sambaWorkgroup = "WORKGROUP";

in {

  # ═══════════════════════════════════════════════════════════
  #  SYSTEM BASICS
  # ═══════════════════════════════════════════════════════════

  imports = [
    ./dplane-generated.nix   # v5.0 JSON-to-Nix bridge (written by daemon, read by Nix)
    ./modules/samba.nix      # D-PlaneOS Samba integration module
  ];

  system.stateVersion = "25.11";

  nix.settings.experimental-features = [ "nix-command" "flakes" ];

  networking.hostName = "dplaneos";

  # ZFS requires a stable host ID : setup-nixos.sh generates this
  networking.hostId = "CHANGE_ME";

  # setup-nixos.sh sets your timezone
  time.timeZone = "Europe/Berlin";

  i18n.defaultLocale = "en_US.UTF-8";

  # ═══════════════════════════════════════════════════════════
  #  BOOTLOADER
  # ═══════════════════════════════════════════════════════════

  # UEFI (default : most hardware since ~2012)
  boot.loader.systemd-boot.enable = true;
  boot.loader.efi.canTouchEfiVariables = true;

  # Legacy BIOS? Uncomment below, comment out the two lines above:
  # boot.loader.grub.enable = true;
  # boot.loader.grub.device = "/dev/sda";

  # ═══════════════════════════════════════════════════════════
  #  ZFS : the foundation
  # ═══════════════════════════════════════════════════════════

  boot.supportedFilesystems = [ "zfs" ];
  boot.zfs.forceImportRoot = false;
  boot.zfs.extraPools = zpools;

  services.zfs.autoScrub = {
    enable = true;
    interval = "monthly";
  };

  # Automatic snapshots : your time machine
  services.zfs.autoSnapshot = {
    enable = true;
    frequent = 4;     # every 15 min, keep 4
    hourly = 24;
    daily = 7;
    weekly = 4;
    monthly = 12;
  };

  # ZFS ARC (read cache) : auto-sized to 50% of RAM
  # Override: set an explicit value in bytes, e.g. 8589934592 for 8GB
  # Rule of thumb: 1 GB per TB of storage, minimum 1 GB
  boot.kernelParams = let
    # Default: auto (ZFS uses 50% of RAM). Override below if needed.
    # To set manually: replace null with byte value, e.g. 8589934592
    arcMaxOverride = null;
  in lib.optionals (arcMaxOverride != null) [
    "zfs.zfs_arc_max=${toString arcMaxOverride}"
  ];

  # ═══════════════════════════════════════════════════════════
  #  KERNEL TUNING (NAS-optimized)
  # ═══════════════════════════════════════════════════════════

  boot.kernel.sysctl = {
    "fs.inotify.max_user_watches" = 524288;
    "fs.inotify.max_user_instances" = 512;
    "vm.swappiness" = 10;
    "vm.vfs_cache_pressure" = 50;
    "net.core.rmem_max" = 16777216;
    "net.core.wmem_max" = 16777216;
  };

  # ═══════════════════════════════════════════════════════════
  #  D-PLANEOS DAEMON
  # ═══════════════════════════════════════════════════════════

  systemd.services.dplaned = {
    description = "D-PlaneOS System Daemon";

    # Boot gate: wait for ZFS + Docker before starting
    after = [
      "network.target"
      "zfs-import.target"
      "zfs-mount.service"
      "docker.service"
    ];
    wants = [ "zfs-import.target" ];
    requires = [ "zfs-mount.service" ];
    wantedBy = [ "multi-user.target" ];

    # PATH for subprocess commands (zfs, docker, smbcontrol, etc.)
    path = with pkgs; [
      zfs smartmontools hdparm dmidecode
      acl ethtool ipmitool util-linux
      coreutils gnugrep gnused gawk
      docker docker-compose
      samba nfs-utils
      openssl git openssh rclone
      curl
    ];

    serviceConfig = {
      Type = "simple";
      ExecStart = lib.concatStringsSep " " [
        "${dplaned}/bin/dplaned"
        "-listen 127.0.0.1:9000"
        "-config-dir /var/lib/dplaneos/config"
        "-smb-conf /var/lib/dplaneos/smb-shares.conf"
      ];
      WorkingDirectory = "/var/lib/dplaneos";
      Restart = "always";
      RestartSec = 5;
      User = "root";
      Group = "root";

      # Directories managed by systemd
      RuntimeDirectory = "dplaneos";
      RuntimeDirectoryMode = "0755";
      StateDirectory = "dplaneos";
      LogsDirectory = "dplaneos";

      # Security hardening : strict, but with ZFS pool access
      NoNewPrivileges = true;
      PrivateTmp = true;
      ProtectSystem = "strict";
      ProtectHome = true;

      # CRITICAL: ZFS pool mountpoints must be listed here
      # ProtectSystem=strict blocks ALL writes except these paths
      ReadWritePaths = [
        "/var/log/dplaneos"
        "/var/lib/dplaneos"
        "/run/dplaneos"
        "/var/run/docker.sock"
        "/etc/exports"
      ] ++ zpoolMountpoints;

      AmbientCapabilities = [
        "CAP_SYS_ADMIN"        # ZFS, mount operations
        "CAP_NET_ADMIN"        # network config, firewall
        "CAP_DAC_READ_SEARCH"  # file browsing across users
        "CAP_CHOWN"            # file ownership management
        "CAP_FOWNER"           # ACL operations
      ];
      LimitNOFILE = 65536;
      TasksMax = 4096;
      MemoryMax = "1G";
      MemoryHigh = "768M";
      OOMScoreAdjust = -900;
    };
  };

  # State directories
  systemd.tmpfiles.rules = [
    "d /var/lib/dplaneos 0750 root root -"
    "d /var/lib/dplaneos/backups 0750 root root -"
    "d /var/lib/dplaneos/config 0750 root root -"
    "d /var/lib/dplaneos/config/ssl 0700 root root -"
    "d /var/lib/dplaneos/stacks 0750 root root -"
    "d /var/lib/dplaneos/git-repos 0750 root root -"
    "f /var/lib/dplaneos/smb-shares.conf 0644 root root -"
  ];

  # ═══════════════════════════════════════════════════════════
  #  NGINX (reverse proxy → dplaned)
  # ═══════════════════════════════════════════════════════════

  services.nginx = {
    enable = true;
    recommendedGzipSettings = true;
    recommendedOptimisation = true;
    recommendedProxySettings = true;

    virtualHosts."dplaneos" = {
      default = true;
      root = "${dplaneos-frontend}";

      extraConfig = ''
        add_header X-Frame-Options "SAMEORIGIN" always;
        add_header X-Content-Type-Options "nosniff" always;
        add_header X-XSS-Protection "1; mode=block" always;
        add_header Referrer-Policy "no-referrer-when-downgrade" always;
        client_max_body_size 10G;
      '';

      locations = {
        "~* \\.(css|js|jpg|jpeg|png|gif|ico|svg|woff|woff2|ttf|eot)$" = {
          extraConfig = ''
            access_log off;
            expires 30d;
            add_header Cache-Control "public, immutable";
          '';
        };

        "/" = { tryFiles = "$uri $uri/ /index.html"; };

        "/api/" = {
          proxyPass = "http://127.0.0.1:9000";
          proxyWebsockets = false;
          extraConfig = ''
            proxy_read_timeout 120s;
            proxy_connect_timeout 10s;
          '';
        };

        # WebSocket : 7 day timeout for persistent connections
        "/ws/" = {
          proxyPass = "http://127.0.0.1:9000";
          proxyWebsockets = true;
          extraConfig = ''
            proxy_connect_timeout 7d;
            proxy_send_timeout 7d;
            proxy_read_timeout 7d;
          '';
        };

        "/health" = { proxyPass = "http://127.0.0.1:9000/health"; };

        # Block sensitive paths
        "~ \\.php$" = { extraConfig = "deny all;"; };
        "~ /\\." = { extraConfig = "deny all;"; };
        "~ /(config|daemon|scripts|systemd)/" = { extraConfig = "deny all;"; };
      };
    };
  };

  # ═══════════════════════════════════════════════════════════
  #  SAMBA (SMB file sharing)
  # ═══════════════════════════════════════════════════════════

  services.samba = {
    enable = true;
    openFirewall = true;

    settings.global = {
      "workgroup"     = sambaWorkgroup;
      "server string" = "D-PlaneOS NAS";
      "security"      = "user";
      "map to guest"  = "Bad User";
      "log file"      = "/var/log/samba/log.%m";
      "max log size"  = 1000;

      # Performance tuning
      "socket options" = "TCP_NODELAY IPTOS_LOWDELAY SO_RCVBUF=131072 SO_SNDBUF=131072";
      "read raw"       = "yes";
      "write raw"      = "yes";
      "use sendfile"   = "yes";
      "aio read size"  = 16384;
      "aio write size" = 16384;
    };

    # Shares managed dynamically by dplaned
    extraConfig = "include = /var/lib/dplaneos/smb-shares.conf";
  };

  # ═══════════════════════════════════════════════════════════
  #  NFS
  # ═══════════════════════════════════════════════════════════

  services.nfs.server.enable = true;

  # ═══════════════════════════════════════════════════════════
  #  DOCKER (containers on ZFS)
  # ═══════════════════════════════════════════════════════════

  virtualisation.docker = {
    enable = true;

    # Native ZFS storage driver : containers as ZFS datasets
    storageDriver = "zfs";

    autoPrune = { enable = true; dates = "weekly"; };

    daemon.settings = {
      "log-driver" = "json-file";
      "log-opts" = { "max-size" = "10m"; "max-file" = "3"; };
      "default-address-pools" = [ { base = "172.17.0.0/16"; size = 24; } ];
    };
  };

  # Docker-ZFS boot gate: Docker must not start until ZFS pools are ready
  # This is the NixOS equivalent of dplaneos-zfs-mount-wait.service
  systemd.services.docker = {
    after = [ "zfs-mount.service" "zfs-import.target" ];
    requires = [ "zfs-mount.service" ];
  };

  # ═══════════════════════════════════════════════════════════
  #  UPS MONITORING (NUT) : uncomment to enable
  # ═══════════════════════════════════════════════════════════

  # services.nut = {
  #   server = {
  #     enable = true;
  #     listen = [ { address = "127.0.0.1"; } ];
  #   };
  #   ups.myups = {
  #     driver = "usbhid-ups";
  #     port = "auto";
  #     description = "My UPS";
  #   };
  #   users.admin = {
  #     upsmon = "primary";
  #     passwordFile = "/var/lib/dplaneos/config/nut-password";
  #   };
  #   upsmon.enable = true;
  # };

  # ═══════════════════════════════════════════════════════════
  #  NETWORKING & DISCOVERY
  # ═══════════════════════════════════════════════════════════

  networking.firewall = {
    enable = true;
    allowedTCPPorts = [
      80 443        # HTTP/HTTPS (web UI)
      445           # SMB
      2049          # NFS
      22            # SSH
    ];
    allowedUDPPorts = [
      5353          # mDNS (Avahi)
    ];
  };

  # Makes NAS discoverable as "dplaneos.local" on local network
  services.avahi = {
    enable = true;
    nssmdns4 = true;
    publish = { enable = true; addresses = true; workstation = true; };
  };

  # ═══════════════════════════════════════════════════════════
  #  MONITORING & HARDWARE
  # ═══════════════════════════════════════════════════════════

  services.smartd = {
    enable = true;
    autodetect = true;
    notifications.wall.enable = true;
  };

  # Removable media detection : notify dplaned via API
  services.udev.extraRules = ''
    ACTION=="add", SUBSYSTEMS=="usb", KERNEL=="sd[a-z][0-9]*", ENV{DEVTYPE}=="partition", RUN+="${pkgs.curl}/bin/curl -sf -X POST http://127.0.0.1:9000/api/system/device-event -d '{\"action\":\"add\",\"device\":\"%E{DEVNAME}\",\"type\":\"usb\"}' -H 'Content-Type: application/json' || true"
    ACTION=="remove", SUBSYSTEMS=="usb", KERNEL=="sd[a-z][0-9]*", ENV{DEVTYPE}=="partition", RUN+="${pkgs.curl}/bin/curl -sf -X POST http://127.0.0.1:9000/api/system/device-event -d '{\"action\":\"remove\",\"device\":\"%E{DEVNAME}\",\"type\":\"usb\"}' -H 'Content-Type: application/json' || true"
  '';

  # ═══════════════════════════════════════════════════════════
  #  PACKAGES
  # ═══════════════════════════════════════════════════════════

  environment.systemPackages = with pkgs; [
    # D-PlaneOS
    dplaned
    dplaneos-recovery

    # Storage
    zfs smartmontools hdparm parted

    # System
    dmidecode acl ethtool ipmitool lsof pciutils usbutils

    # Network
    iperf3 nfs-utils

    # Containers
    docker-compose

    # Backup & sync
    rclone rsync

    # Git-sync
    git openssh

    # Shell
    htop tmux nano curl wget
    postgresql
  ];

  # ═══════════════════════════════════════════════════════════
  #  SSH
  # ═══════════════════════════════════════════════════════════

  services.openssh = {
    enable = true;
    settings = {
      PermitRootLogin = "yes";
      PasswordAuthentication = true;
    };
  };

  # ═══════════════════════════════════════════════════════════
  #  USERS
  # ═══════════════════════════════════════════════════════════

  users.users.admin = {
    isNormalUser = true;
    extraGroups = [ "wheel" "docker" ];
  };

  security.sudo.wheelNeedsPassword = true;

  # ═══════════════════════════════════════════════════════════
  #  SCHEDULED TASKS (the NixOS way : systemd timers)
  # ═══════════════════════════════════════════════════════════

  # Daily database backup (logical dump) with 30-day retention
  systemd.timers.dplaneos-db-backup = {
    wantedBy = [ "timers.target" ];
    timerConfig = {
      OnCalendar = "*-*-* 03:00:00";
      Persistent = true;
    };
  };

  systemd.services.dplaneos-db-backup = {
    description = "D-PlaneOS database backup";
    serviceConfig = {
      Type = "oneshot";
      ExecStart = pkgs.writeShellScript "dplaneos-db-backup" ''
        ${pkgs.postgresql}/bin/pg_dump -U postgres dplaneos > /var/lib/dplaneos/backups/dplaneos-$(date +%Y%m%d-%H%M%S).sql
        ${pkgs.findutils}/bin/find /var/lib/dplaneos/backups -name "dplaneos-*.sql" -mtime +30 -delete
      '';
    };
  };
}
