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

  # upsmon runs its notify command as its own user; the daemon's UPS event
  # socket is writable by that user's group (dplaned -ups-event-group).
  eventSocket = "/run/dplaneos/ups-events.sock";
  notifyTypes = [ "ONLINE" "ONBATT" "LOWBATT" "FSD" "COMMOK" "COMMBAD" "SHUTDOWN"
                  "REPLBATT" "NOCOMM" "NOPARENT" "CAL" "OFF" "BYPASS" "OVER" ];
  notifyCmd = pkgs.writeShellScript "dplaneos-ups-notify" ''
    # $1: NUT's message; NOTIFYTYPE and UPSNAME come in the environment.
    printf 'ups_event:%s:%s:%s\n' "$NOTIFYTYPE" "$UPSNAME" "$1" \
      | ${pkgs.socat}/bin/socat -t2 - UNIX-CONNECT:${eventSocket} 2>/dev/null || true
  '';
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
      # "hibernate" stays accepted for older state files and shuts down too:
      # the system boots with nohibernate (ZFS), so a hibernate fails and the
      # machine would run until the battery is empty.
      type = lib.types.enum [ "shutdown" "hibernate" ];
      default = "shutdown";
      description = "What to do on low battery: shut down (hibernation is not possible with ZFS).";
    };
  };

  config = lib.mkIf cfg.enable {
    power.ups = {
      enable = true;
      mode = "standalone";

      ups.${cfg.name} = {
        inherit (cfg) driver port description;
        # The driver derives "low battery" from battery.charge.low only with
        # ignorelb (ups.conf(5)); without it the override changes the value
        # shown and the UPS's own LB flag still decides when to shut down.
        directives = lib.optionals (cfg.lowBattery != null) [
          "ignorelb"
          "override.battery.charge.low = ${toString cfg.lowBattery}"
        ];
      };

      users.upsmon = {
        passwordFile = passFile;
        upsmon = "primary";
      };

      upsmon.monitor.${cfg.name}.user = "upsmon";
      upsmon.settings = {
        FINALDELAY = cfg.finalDelay;
        SHUTDOWNCMD = "${pkgs.systemd}/bin/shutdown now";
        # Every power event goes to the daemon (alerts) as well as the log.
        # No WALL: upsmon's notify child broadcasts before it runs NOTIFYCMD,
        # and on low battery the shutdown killed it before the alert went out.
        NOTIFYCMD = "${notifyCmd}";
        NOTIFYFLAG = map (t: [ t "SYSLOG+EXEC" ]) notifyTypes;
      };
    };

    warnings = lib.optional (cfg.action == "hibernate")
      "services.dplaneos.ups.action = \"hibernate\" is not possible with ZFS (nohibernate): the system shuts down instead.";

    services.dplaneos.upsEventGroup = config.power.ups.upsmon.group;

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
