# D-PlaneOS — OTA Update NixOS Module (Task 4.2)
# ─────────────────────────────────────────────────────────────────────────────
# Installs the OTA update script and two systemd units:
#
#   dplaneos-ota-health.service  — runs the health check once after boot
#   dplaneos-ota-health.timer    — fires the service 90s after boot
#
# The 90-second delay gives all services time to fully start before the
# health check evaluates them. If the check fails, the OTA script reverts
# the bootloader to the previous slot and reboots.
# ─────────────────────────────────────────────────────────────────────────────

{ config, lib, pkgs, ... }:

let
  cfg = config.services.dplaneos;

  otaScript = pkgs.writeShellScriptBin "dplaneos-ota-update" (
    builtins.readFile ./ota-update.sh
  );

  # Wrap in a package so it's available in PATH
  otaPackage = pkgs.symlinkJoin {
    name    = "dplaneos-ota";
    paths   = [ otaScript ];
    buildInputs = [ pkgs.makeWrapper ];
    postBuild = ''
      wrapProgram $out/bin/dplaneos-ota-update \
        --prefix PATH : ${lib.makeBinPath [
          pkgs.coreutils
          pkgs.util-linux    # mountpoint, lsblk, blkid, findmnt
          pkgs.systemd       # bootctl, systemctl
          pkgs.sqlite        # sqlite3
          pkgs.curl
          pkgs.gnutar
          pkgs.openssl
          pkgs.python3
          pkgs.zfs
        ]}
    '';
  };

in {
  config = lib.mkIf cfg.enable {

    # ── Install the OTA script system-wide ────────────────────────────────
    environment.systemPackages = [ otaPackage ];

    # ── Health check service ──────────────────────────────────────────────
    # Runs once after boot. If a pending-revert marker exists and health
    # checks fail, initiates an automatic slot revert.
    systemd.services.dplaneos-ota-health = {
      description   = "D-PlaneOS OTA Update Health Check";
      after         = [
        "network.target"
        "dplaned.service"
        "smbd.service"
        "zfs.target"
      ];
      # Does NOT use wantedBy — activated only by the timer below.
      # This prevents it from running on every boot without a pending update.
      serviceConfig = {
        Type            = "oneshot";
        RemainAfterExit = false;
        ExecStart       = "${otaPackage}/bin/dplaneos-ota-update --health-check";
        # Allow reboot on revert
        ExecStartPost   = "-/bin/true";
      };
    };

    # ── Health check timer ─────────────────────────────────────────────────
    # Fires once, 90 seconds after boot. One-shot — not recurring.
    # If no pending-revert marker exists, the health check service exits
    # immediately (no-op).
    systemd.timers.dplaneos-ota-health = {
      description = "D-PlaneOS OTA Health Check (90s after boot)";
      wantedBy    = [ "timers.target" ];
      timerConfig = {
        OnBootSec  = "90s";   # 90 seconds after boot
        Unit       = "dplaneos-ota-health.service";
        # Do not repeat — this is a one-shot post-boot check.
        # (systemd timers are one-shot by default when OnBootSec is the only trigger)
      };
    };

    # ── /persist/ota directory ────────────────────────────────────────────
    systemd.tmpfiles.rules = [
      "d /persist/ota 0750 root root -"
    ];

  };
}
