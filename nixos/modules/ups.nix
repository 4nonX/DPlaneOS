# nixos/modules/ups.nix
# ─────────────────────────────────────────────────────────────────────────────
# DPlaneOS UPS integration (Network UPS Tools)
#
# The system provides NUT and three services. Which UPS there is, how it is
# connected and what happens when its battery runs low is set on the UPS
# page: the daemon writes NUT's configuration to /var/lib/dplaneos/nut
# (internal/sysapply) and starts the services. Each service only starts when
# its configuration file exists, so a system without a UPS runs none of them.
#
#   dplaneos-upsdrv   the driver that talks to the UPS (USB, serial, ...)
#   dplaneos-upsd     the server the driver reports to (local connections only)
#   dplaneos-upsmon   watches the UPS and shuts the system down in time; with
#                     a UPS on another machine's NUT server, only this runs
# ─────────────────────────────────────────────────────────────────────────────

{ config, lib, pkgs, ... }:

let
  cfg = config.services.dplaneos.ups;
  confDir = "/var/lib/dplaneos/nut";
  env = {
    NUT_CONFPATH  = confDir;
    NUT_STATEPATH = "/run/nut";
    NUT_ALTPIDPATH = "/run/nut";
  };
  common = {
    wantedBy = [ "multi-user.target" ];
    unitConfig.RequiresMountsFor = "/var/lib/dplaneos";
    environment = env;
    path = [ pkgs.nut ];
  };
in
{
  options.services.dplaneos.ups.enable = lib.mkOption {
    type = lib.types.bool;
    default = true;
    description = "Provide Network UPS Tools so that a UPS can be set up on the UPS page.";
  };

  config = lib.mkIf cfg.enable {
    environment.systemPackages = [ pkgs.nut ];
    # upsc and friends, run by hand, find the same configuration.
    environment.variables.NUT_CONFPATH = confDir;

    systemd.services.dplaneos-upsdrv = common // {
      description = "UPS driver";
      before = [ "dplaneos-upsd.service" ];
      unitConfig = common.unitConfig // { ConditionPathExists = "${confDir}/ups.conf"; };
      serviceConfig = {
        Type = "forking";
        RuntimeDirectory = "nut";
        RuntimeDirectoryPreserve = true;
        ExecStart = "${pkgs.runtimeShell} -c 'exec upsdrvctl -u root start'";
        ExecStop  = "${pkgs.runtimeShell} -c 'exec upsdrvctl -u root stop'";
        Restart = "on-failure";
        RestartSec = 10;
      };
    };

    systemd.services.dplaneos-upsd = common // {
      description = "UPS server";
      after  = [ "dplaneos-upsdrv.service" "network.target" ];
      before = [ "dplaneos-upsmon.service" ];
      unitConfig = common.unitConfig // { ConditionPathExists = "${confDir}/upsd.conf"; };
      serviceConfig = {
        Type = "forking";
        RuntimeDirectory = "nut";
        RuntimeDirectoryPreserve = true;
        ExecStart = "${pkgs.runtimeShell} -c 'exec upsd -u root'";
        ExecReload = "${pkgs.runtimeShell} -c 'exec upsd -u root -c reload'";
        Restart = "on-failure";
        RestartSec = 10;
      };
    };

    systemd.services.dplaneos-upsmon = common // {
      description = "UPS monitor (shuts the system down when the battery runs low)";
      after = [ "dplaneos-upsd.service" "network-online.target" ];
      wants = [ "network-online.target" ];
      unitConfig = common.unitConfig // { ConditionPathExists = "${confDir}/upsmon.conf"; };
      serviceConfig = {
        Type = "forking";
        RuntimeDirectory = "nut";
        RuntimeDirectoryPreserve = true;
        ExecStart = "${pkgs.runtimeShell} -c 'exec upsmon -u root'";
        ExecReload = "${pkgs.runtimeShell} -c 'exec upsmon -u root -c reload'";
        Restart = "on-failure";
        RestartSec = 10;
      };
    };
  };
}
