# nixos/modules/ups.nix
# ─────────────────────────────────────────────────────────────────────────────
# DPlaneOS UPS integration (Network UPS Tools)
#
# The hardware side (driver, port) is declared here; the shutdown policy set on
# the UPS page (low-battery level, final delay, action) arrives through the
# JSON bridge (dplane-generated.nix → lowBattery / finalDelay / action) and is
# applied on the next nixos-rebuild. The daemon reads status with upsc.
#
#   services.dplaneos.ups = {
#     enable = true;
#     driver = "usbhid-ups";   # or apcsmart, blazer_usb, ...
#     port   = "auto";
#   };
# ─────────────────────────────────────────────────────────────────────────────

{ config, lib, pkgs, ... }:

let
  cfg = config.services.dplaneos.ups;
  passFile = "/var/lib/dplaneos/nut/upsmon.pass";
in
{
  options.services.dplaneos.ups = {
    enable = lib.mkEnableOption "UPS monitoring and automatic shutdown via NUT";

    name = lib.mkOption {
      type = lib.types.str;
      default = "ups";
      description = "NUT name of the UPS (used by upsc and the UPS page).";
    };

    driver = lib.mkOption {
      type = lib.types.str;
      default = "usbhid-ups";
      example = "apcsmart";
      description = "NUT driver for the UPS.";
    };

    port = lib.mkOption {
      type = lib.types.str;
      default = "auto";
      example = "/dev/ttyS0";
      description = "Port the UPS is attached to (auto for USB).";
    };

    description = lib.mkOption {
      type = lib.types.str;
      default = "DPlaneOS UPS";
      description = "Human-readable UPS description.";
    };

    lowBattery = lib.mkOption {
      type = lib.types.nullOr (lib.types.ints.between 1 99);
      default = null;
      description = "Battery charge (%) at which the UPS counts as low and shutdown starts. Set from the UPS page.";
    };

    finalDelay = lib.mkOption {
      type = lib.types.ints.between 0 600;
      default = 5;
      description = "Seconds upsmon waits after the low-battery event before shutting down. Set from the UPS page.";
    };

    action = lib.mkOption {
      type = lib.types.enum [ "shutdown" "hibernate" ];
      default = "shutdown";
      description = "What to do on low battery. Set from the UPS page.";
    };
  };

  config = lib.mkIf cfg.enable {
    power.ups = {
      enable = true;
      mode = "standalone";
      # No timed events; upssched is only the NOTIFYCMD default.
      schedulerRules = toString (pkgs.writeText "dplaneos-upssched.conf" ''
        CMDSCRIPT ${pkgs.coreutils}/bin/true
      '');

      ups.${cfg.name} = {
        inherit (cfg) driver port description;
        directives = lib.optional (cfg.lowBattery != null)
          "override.battery.charge.low = ${toString cfg.lowBattery}";
      };

      users.upsmon = {
        passwordFile = passFile;
        upsmon = "primary";
      };

      upsmon.monitor.${cfg.name}.user = "upsmon";
      upsmon.settings = {
        FINALDELAY = cfg.finalDelay;
        SHUTDOWNCMD =
          if cfg.action == "hibernate"
          then "${pkgs.systemd}/bin/systemctl hibernate"
          else "${pkgs.systemd}/bin/shutdown now";
      };
    };

    # upsd and upsmon share one generated password; create it before either starts.
    systemd.services.dplaneos-ups-secret = {
      description = "DPlaneOS NUT shared secret";
      before = [ "upsd.service" "upsmon.service" ];
      requiredBy = [ "upsd.service" "upsmon.service" ];
      serviceConfig = { Type = "oneshot"; RemainAfterExit = true; };
      script = ''
        umask 077
        mkdir -p "$(dirname ${passFile})"
        if [ ! -s ${passFile} ]; then
          ${pkgs.coreutils}/bin/head -c 24 /dev/urandom | ${pkgs.coreutils}/bin/base64 > ${passFile}
        fi
      '';
    };
  };
}
