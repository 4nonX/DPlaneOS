# nixos/modules/samba.nix
# ─────────────────────────────────────────────────────────────────────────────
# D-PlaneOS Samba Integration Module
#
# ARCHITECTURE PROBLEM THIS SOLVES:
#
#   Before: D-PlaneOS wrote directly to /etc/samba/smb.conf (imperative).
#           NixOS doesn't know about this file, so every nixos-rebuild
#           regenerates smb.conf from scratch, wiping all UI-configured shares.
#
#   After:  NixOS OWNS Samba via services.samba. It writes the [global]
#           section and then appends:
#               include = /var/lib/dplaneos/smb-shares.conf
#           D-PlaneOS writes ONLY the per-share stanzas into that file.
#           On rebuild: NixOS regenerates [global], D-PlaneOS's shares survive.
#
# TWO-LAYER SPLIT:
#
#   NixOS layer (this file) — owns:
#     • services.samba.enable
#     • [global] section (workgroup, security, logging, VFS globals)
#     • include = /var/lib/dplaneos/smb-shares.conf
#     • Firewall ports (445, 139, 137/udp, 138/udp)
#     • systemd service ordering (after ZFS, after dplaned)
#     • passdb backend (tdbsam — persisted via impermanence.nix)
#
#   D-PlaneOS layer (/var/lib/dplaneos/smb-shares.conf) — owns:
#     • Every [share-name] stanza
#     • Per-share path, permissions, valid users, VFS objects
#     • Written atomically by daemon on every share create/update/delete
#
# USAGE:
#   In configuration.nix or flake.nix:
#     imports = [
#       ./dplane-generated.nix          # network config
#       ./modules/samba.nix             # samba module
#     ];
#     services.dplaneos.samba.enable = true;
#
#   Or with full options:
#     services.dplaneos.samba = {
#       enable    = true;
#       workgroup = "MYCOMPANY";
#       timeMachine = true;
#     };
# ─────────────────────────────────────────────────────────────────────────────

{ config, lib, pkgs, ... }:

let
  cfg = config.services.dplaneos.samba;

  # Path the D-PlaneOS daemon writes per-share stanzas to.
  # Passed to dplaned via: --smb-conf /var/lib/dplaneos/smb-shares.conf
  sharesConfPath = "/var/lib/dplaneos/smb-shares.conf";

  # The [global] section is fully owned by NixOS.
  # It is rendered here from options — never touched by the daemon.
  globalSection = ''
    [global]
       workgroup = ${cfg.workgroup}
       server string = ${cfg.serverString}
       netbios name = ${cfg.netbiosName}

       # Authentication
       security = user
       passdb backend = tdbsam
       map to guest = ${if cfg.allowGuest then "Bad User" else "Never"}
       ${lib.optionalString cfg.allowGuest "guest account = nobody"}

       # Protocol — SMB2/3 only (SMB1 disabled, security risk)
       server min protocol = SMB2
       server max protocol = SMB3

       # Character encoding
       unix charset = UTF-8
       dos charset = CP850

       # Logging (NixOS journal, not a file — use: journalctl -u smbd)
       logging = systemd
       log level = 1

       # Performance
       socket options = TCP_NODELAY IPTOS_LOWDELAY SO_RCVBUF=131072 SO_SNDBUF=131072
       read raw = yes
       write raw = yes
       use sendfile = yes

       ${lib.optionalString cfg.timeMachine ''
       # macOS Time Machine (Bonjour/mDNS advertisement)
       fruit:metadata = stream
       fruit:model = MacSamba
       fruit:posix_rename = yes
       fruit:veto_appledouble = no
       fruit:nfs_aces = no
       fruit:wipe_intentionally_left_blank_rfork = yes
       fruit:delete_empty_adfiles = yes
       vfs objects = catia fruit streams_xattr
       ''}

       ${lib.optionalString (cfg.extraGlobalConfig != "") ''
       # Extra global config (from D-PlaneOS settings UI)
       ${cfg.extraGlobalConfig}
       ''}

       # Include D-PlaneOS share definitions (written by web UI)
       # This line is the bridge: NixOS owns [global], daemon owns share stanzas.
       include = ${sharesConfPath}
  '';

in {

  # ── Option declarations ─────────────────────────────────────────────────────

  options.services.dplaneos.samba = {

    enable = lib.mkEnableOption "D-PlaneOS Samba integration";

    workgroup = lib.mkOption {
      type    = lib.types.str;
      default = "WORKGROUP";
      description = "SMB workgroup name. Change to match your Windows domain if applicable.";
      example = "MYCOMPANY";
    };

    serverString = lib.mkOption {
      type    = lib.types.str;
      default = "D-PlaneOS NAS";
      description = "Samba server description shown in network browsers.";
    };

    netbiosName = lib.mkOption {
      type    = lib.types.str;
      default = config.networking.hostName;
      description = "NetBIOS name. Defaults to the system hostname.";
    };

    allowGuest = lib.mkOption {
      type    = lib.types.bool;
      default = false;
      description = ''
        Allow guest (unauthenticated) access to shares marked guest ok = yes.
        Disabled by default — guest access is a security risk on production NAS.
      '';
    };

    timeMachine = lib.mkOption {
      type    = lib.types.bool;
      default = false;
      description = ''
        Enable macOS Time Machine support (Bonjour advertisement + fruit VFS).
        Set to true if any Macs will back up to this NAS.
        Corresponds to the "Time Machine" toggle in the D-PlaneOS Shares UI.
      '';
    };

    openFirewall = lib.mkOption {
      type    = lib.types.bool;
      default = true;
      description = "Open Samba ports in the NixOS firewall (TCP 445, 139; UDP 137, 138).";
    };

    extraGlobalConfig = lib.mkOption {
      type    = lib.types.lines;
      default = "";
      description = ''
        Extra lines appended verbatim to the [global] section.
        Corresponds to the "Extra Global Parameters" field in the Shares UI.
        This value should be set programmatically by the D-PlaneOS settings
        handler, not edited by hand.
      '';
    };

    sharesConfPath = lib.mkOption {
      type    = lib.types.path;
      default = sharesConfPath;
      readOnly = true;
      description = ''
        Path where the D-PlaneOS daemon writes per-share SMB stanzas.
        This is the file passed to dplaned via --smb-conf.
        Read-only — changing this would desync the daemon and NixOS.
      '';
    };

  };

  # ── Implementation ──────────────────────────────────────────────────────────

  config = lib.mkIf cfg.enable {

    # ── Samba service ─────────────────────────────────────────────────────────
    # NixOS services.samba manages smbd/nmbd/winbindd lifetimes, the
    # passdb backend, and the main smb.conf.
    # We hand it the [global] section we built above.
    services.samba = {
      enable = true;
      package = pkgs.samba;

      # Use our rendered config. services.samba writes this to
      # /etc/samba/smb.conf at activation time.
      # The "include = ..." line at the bottom pulls in daemon-managed shares.
      settings = {
        global = {
          "workgroup"           = cfg.workgroup;
          "server string"       = cfg.serverString;
          "netbios name"        = cfg.netbiosName;
          "security"            = "user";
          "passdb backend"      = "tdbsam";
          "map to guest"        = if cfg.allowGuest then "Bad User" else "Never";
          "server min protocol" = "SMB2";
          "server max protocol" = "SMB3";
          "unix charset"        = "UTF-8";
          "dos charset"         = "CP850";
          "logging"             = "systemd";
          "log level"           = "1";
          "socket options"      = "TCP_NODELAY IPTOS_LOWDELAY SO_RCVBUF=131072 SO_SNDBUF=131072";
          "read raw"            = "yes";
          "write raw"           = "yes";
          "use sendfile"        = "yes";
          # ── THE BRIDGE LINE ───────────────────────────────────────────────
          # NixOS writes the [global] section; D-PlaneOS writes the shares.
          # This include makes NixOS aware of D-PlaneOS's share stanzas
          # without giving the daemon ownership of the whole smb.conf.
          "include"             = sharesConfPath;
        } // lib.optionalAttrs cfg.timeMachine {
          "fruit:metadata"                        = "stream";
          "fruit:model"                           = "MacSamba";
          "fruit:posix_rename"                    = "yes";
          "fruit:veto_appledouble"                = "no";
          "fruit:nfs_aces"                        = "no";
          "fruit:wipe_intentionally_left_blank_rfork" = "yes";
          "fruit:delete_empty_adfiles"            = "yes";
          "vfs objects"                           = "catia fruit streams_xattr";
        };
      };

      # openFirewall handled below so we can also open UDP ports
      openFirewall = false;
    };

    # ── Ensure the shares config file exists before smbd starts ──────────────
    # smbd will refuse to start if the include target is missing.
    # We create an empty-but-valid stub; the daemon will populate it.
    systemd.services.dplaneos-smb-shares-stub = {
      description = "D-PlaneOS Samba shares config stub";
      # Must run before smbd starts, after the persist directory is mounted
      before  = [ "smbd.service" ];
      wantedBy = [ "smbd.service" ];
      serviceConfig = {
        Type            = "oneshot";
        RemainAfterExit = true;
        ExecStart = pkgs.writeShellScript "smb-stub" ''
          set -e
          CONF="${sharesConfPath}"
          DIR="$(dirname "$CONF")"
          mkdir -p "$DIR"
          if [ ! -f "$CONF" ]; then
            echo "# D-PlaneOS per-share SMB config"              >  "$CONF"
            echo "# Written by D-PlaneOS daemon on share changes" >> "$CONF"
            echo "# DO NOT EDIT — changes will be overwritten"    >> "$CONF"
            echo ""                                               >> "$CONF"
            echo "# (no shares configured yet)"                  >> "$CONF"
            echo "Created $CONF stub"
          fi
        '';
      };
    };

    # ── smbd ordering: after D-PlaneOS daemon has written the shares file ─────
    # dplaned writes smb-shares.conf at startup (it reads the DB and rewrites
    # the file to match). We want smbd to start AFTER that write.
    systemd.services.smbd = {
      after  = [ "dplaned.service" "dplaneos-smb-shares-stub.service" ];
      wants  = [ "dplaneos-smb-shares-stub.service" ];
    };

    # ── Also make dplaned reload samba when it changes the shares file ────────
    # dplaned calls `smbcontrol all reload-config` after writing the file.
    # Ensure dplaned can run smbcontrol — add it to the allowed commands.
    # (The capability CAP_SYS_ADMIN in module.nix's service config covers this.)

    # ── Firewall ──────────────────────────────────────────────────────────────
    networking.firewall = lib.mkIf cfg.openFirewall {
      allowedTCPPorts = [ 445 139 ];
      allowedUDPPorts = [ 137 138 ];
    };

    # ── Avahi: advertise Samba over mDNS so it appears in Finder/Explorer ─────
    services.avahi = {
      enable   = true;
      nssmdns4 = true;
      publish  = {
        enable      = true;
        addresses   = true;
        workstation = true;
        extraServices = lib.optionalString cfg.timeMachine ''
          <?xml version="1.0" standalone='no'?>
          <!DOCTYPE service-group SYSTEM "avahi-service.dtd">
          <service-group>
            <name replace-wildcards="yes">%h</name>
            <service>
              <type>_smb._tcp</type>
              <port>445</port>
            </service>
            <service>
              <type>_adisk._tcp</type>
              <port>9</port>
              <txt-record>dk0=adVN=${cfg.netbiosName},adVF=0x82</txt-record>
              <txt-record>sys=waMa=0,adVF=0x100</txt-record>
            </service>
          </service-group>
        '';
      };
    };

    # ── Persist Samba state across reboots ────────────────────────────────────
    # passdb.tdb, secrets.tdb, etc. contain Samba user credentials.
    # Without persistence, all Samba passwords are lost on reboot.
    # Note: impermanence.nix also persists /var/lib/samba — this is a
    # belt-and-suspenders assertion using tmpfiles for non-impermanence setups.
    systemd.tmpfiles.rules = [
      "d /var/lib/samba          0755 root root -"
      "d /var/lib/samba/private  0700 root root -"
      "d /var/log/samba          0755 root root -"
      "d /var/lib/dplaneos       0775 root root -"  # ensure parent exists
    ];

    # ── ReadWritePaths extension for dplaned service ──────────────────────────
    # The dplaned systemd service (declared in module.nix) needs write access
    # to the shares config path. We extend its ReadWritePaths here.
    # lib.mkAfter ensures this appends rather than replaces module.nix's list.
    systemd.services.dplaned.serviceConfig.ReadWritePaths =
      lib.mkAfter [ (builtins.dirOf sharesConfPath) ];

  };
}
