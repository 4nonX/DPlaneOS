# nixos/modules/samba.nix
# ─────────────────────────────────────────────────────────────────────────────
# DPlaneOS Samba integration
#
# The system provides Samba (smbd, nmbd, winbindd), its firewall ports, the
# Bonjour advertisement and the NSS hook for directory users. The
# configuration itself is not part of the system: the daemon writes the
# complete smb.conf (internal/smbconf: [global] from the settings, one
# section per share) to /var/lib/dplaneos/smb-shares.conf, and
# /etc/samba/smb.conf is a link to that file. Every setting of the Shares
# page and joining a directory domain therefore take effect when they are
# made, without rebuilding anything.
# ─────────────────────────────────────────────────────────────────────────────

{ config, lib, pkgs, ... }:

let
  cfg = config.services.dplaneos.samba;

  # The smb.conf the daemon writes (dplaned -smb-conf).
  confPath = "/var/lib/dplaneos/smb-shares.conf";
in
{
  options.services.dplaneos.samba = {
    enable = lib.mkOption {
      type        = lib.types.bool;
      default     = true;
      description = "Enable DPlaneOS Samba (SMB) integration. Samba is a core NAS feature and is enabled by default.";
    };

    openFirewall = lib.mkOption {
      type    = lib.types.bool;
      default = true;
      description = "Open Samba ports in the NixOS firewall (TCP 445, 139; UDP 137, 138).";
    };

    package = lib.mkOption {
      type        = lib.types.package;
      default     = import ../samba-package.nix pkgs;
      defaultText = lib.literalExpression "pkgs.samba.override { enableLDAP = true; }";
      description = ''
        The Samba package: smbd, nmbd, winbindd, and the tools the daemon
        calls (net, wbinfo, testparm). Built with Active Directory support,
        which joining a domain needs.
      '';
    };

    sharesConfPath = lib.mkOption {
      type     = lib.types.path;
      default  = confPath;
      readOnly = true;
      description = ''
        Where the DPlaneOS daemon writes smb.conf (passed to it as -smb-conf).
        Read-only: changing it would separate the daemon from Samba.
      '';
    };
  };

  config = lib.mkIf cfg.enable {

    services.samba = {
      enable       = true;
      package      = cfg.package;
      openFirewall = false;   # below, with the UDP ports
    };

    # Samba reads the daemon's file. (services.samba would write a file of
    # its own here; it is replaced, not merged: two sources for [global]
    # could contradict each other.)
    environment.etc."samba/smb.conf".source = lib.mkForce confPath;

    # A configuration must exist before Samba starts for the first time;
    # the daemon replaces it as soon as it runs.
    systemd.services.dplaneos-smb-conf = {
      description = "Initial Samba configuration";
      before      = [ "samba-smbd.service" "samba-nmbd.service" "samba-winbindd.service" ];
      requiredBy  = [ "samba-smbd.service" "samba-nmbd.service" "samba-winbindd.service" ];
      unitConfig.RequiresMountsFor = "/var/lib/dplaneos";
      serviceConfig = { Type = "oneshot"; RemainAfterExit = true; };
      script = ''
        conf=${confPath}
        ${pkgs.coreutils}/bin/mkdir -p "$(${pkgs.coreutils}/bin/dirname "$conf")"
        if [ ! -s "$conf" ] || ! ${pkgs.gnugrep}/bin/grep -q '^\[global\]' "$conf"; then
          ${pkgs.coreutils}/bin/cat > "$conf.new" <<'EOF'
        # Initial configuration; DPlaneOS replaces this file from its database.
        [global]
           workgroup = WORKGROUP
           server string = DPlaneOS NAS
           security = user
           passdb backend = tdbsam
           map to guest = Never
           server min protocol = SMB2
           logging = systemd
           load printers = no
           printing = bsd
           printcap name = /dev/null
           disable spoolss = yes
           idmap config * : backend = tdb
           idmap config * : range = 3000-7999
        EOF
          ${pkgs.coreutils}/bin/mv "$conf.new" "$conf"
        fi
      '';
    };

    # Users and groups of a directory domain resolve through winbind once
    # the system has joined one (without a domain winbind has none to offer).
    system.nssModules   = [ cfg.package ];
    system.nssDatabases = {
      passwd = lib.mkAfter [ "winbind" ];
      group  = lib.mkAfter [ "winbind" ];
    };

    networking.firewall = lib.mkIf cfg.openFirewall {
      allowedTCPPorts = [ 445 139 ];
      allowedUDPPorts = [ 137 138 ];
    };

    # ── Avahi: advertise Samba over mDNS so it appears in Finder/Explorer ─────
    services.avahi = {
      enable   = true;
      nssmdns4 = true;
      publish  = {
        enable       = true;
        addresses    = true;
        workstation  = true;
        userServices = true;
      };
      extraServiceFiles.smb = ''
        <?xml version="1.0" standalone='no'?>
        <!DOCTYPE service-group SYSTEM "avahi-service.dtd">
        <service-group>
          <name replace-wildcards="yes">%h</name>
          <service>
            <type>_smb._tcp</type>
            <port>445</port>
          </service>
        </service-group>
      '';
    };

    # Time Machine discovery (_adisk._tcp) must name the actual target shares,
    # which are chosen per share in the UI. /etc is read-only, so the daemon
    # writes the service file under /var/lib/dplaneos (smbconf.AvahiPath) and
    # reloads avahi; this link makes avahi read it.
    environment.etc."avahi/services/dplaneos-timemachine.service".source =
      "/var/lib/dplaneos/avahi/dplaneos-timemachine.service";

    # passdb.tdb and secrets.tdb hold the SMB passwords and the domain
    # membership (impermanence.nix persists /var/lib/samba).
    systemd.tmpfiles.rules = [
      "d /var/lib/samba          0755 root root -"
      "d /var/lib/samba/private  0700 root root -"
      "d /var/log/samba          0755 root root -"
      "d /var/lib/dplaneos       0775 root root -"
    ];

    # No ReadWritePaths for dplaned: any of them gives it a private mount
    # namespace, hiding its ZFS mounts from Samba (see module.nix).
  };
}
