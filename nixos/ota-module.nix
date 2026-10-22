# nixos/ota-module.nix
# -----------------------------------------------------------------------------
# DPlaneOS updates: the check after the reboot, and the automatic way back.
#
# An update (Updates page; POST /api/system/updates/nixos/apply) points the
# system flake /etc/nixos/flake.nix at the new release, builds it with
# `nixos-rebuild boot` as the next boot entry and reboots. After the boot
# dplaneos-ota-health runs dplaneos-update-health: when the new system works
# it is kept; otherwise the generation that ran before becomes the boot
# default again, the flake is restored and the machine reboots into it.
#
# This replaces the A/B partition scheme (ota-update.sh), which could not
# work: it needed boot entries named "DPlaneOS (Slot A/B)" that nothing
# created and a root filesystem per slot (the system always mounts
# system-a), and nothing built its update bundles. NixOS generations give
# the same atomic switch and rollback. (Older installs keep an unused
# system-b partition; disko.nix no longer creates it.)
# -----------------------------------------------------------------------------
{ config, lib, pkgs, ... }:

let
  cfg = config.services.dplaneos;

  updateHealth = pkgs.writeShellApplication {
    name = "dplaneos-update-health";
    runtimeInputs = with pkgs; [
      coreutils curl jq systemd
      config.boot.zfs.package   # zpool
      config.nix.package        # nix-env
    ];
    text = builtins.readFile ./dplaneos-update-health.sh;
  };
in {
  imports = [
    (lib.mkRemovedOptionModule [ "services" "dplaneos" "ota" "signingKey" ]
      "Updates are NixOS generations of the system flake (Updates page); signed A/B bundles are no longer used.")
  ];

  options.services.dplaneos.ota = {
    enable = lib.mkOption {
      type    = lib.types.bool;
      default = true;
      description = ''
        Check the system after an update's reboot and go back to the previous
        generation when the check fails. Disable only if you manage updates
        entirely outside of DPlaneOS.
      '';
    };
    healthCheckDelay = lib.mkOption {
      type    = lib.types.str;
      default = "60s";
      description = ''
        How long after boot the check starts (a systemd time span). The check
        then retries for checkSeconds while the services start.
      '';
    };
    checkSeconds = lib.mkOption {
      type    = lib.types.int;
      default = 300;
      description = "How long the check waits for the system to become healthy before it goes back.";
    };
  };

  config = lib.mkIf (cfg.enable && cfg.ota.enable) {
    environment.systemPackages = [ updateHealth ];

    systemd.services.dplaneos-ota-health = {
      description = "DPlaneOS update check after reboot";
      after    = [ "network.target" "dplaned.service" "nginx.service" "postgresql.service" "samba-smbd.service" "zfs.target" ];
      environment = {
        CHECK_SECONDS = toString cfg.ota.checkSeconds;
        CHECK_UNITS = lib.concatStringsSep " " ([ "nginx.service" ]
          ++ lib.optional cfg.database.createLocally "postgresql.service");
      };
      serviceConfig = {
        Type            = "oneshot";
        ExecStart       = "${updateHealth}/bin/dplaneos-update-health";
        TimeoutStartSec = cfg.ota.checkSeconds + 120;
      };
    };

    systemd.timers.dplaneos-ota-health = {
      description = "DPlaneOS update check after reboot";
      wantedBy    = [ "timers.target" ];
      timerConfig = {
        OnBootSec = cfg.ota.healthCheckDelay;
        Unit      = "dplaneos-ota-health.service";
      };
    };
  };
}
