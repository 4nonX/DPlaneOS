# D-PlaneOS — OTA Update NixOS Module
# ─────────────────────────────────────────────────────────────────────────────
# Installs the OTA update script and two systemd units:
#
#   dplaneos-ota-health.service  — runs the health check once after boot
#   dplaneos-ota-health.timer    — fires the service 90s after boot
#
# The 90-second delay gives all services time to fully start before the
# health check evaluates them. If the check fails, the OTA script reverts
# the bootloader to the previous slot and reboots.
#
# Update distribution model:
#   Updates are distributed via GitHub (git pull + nixos-rebuild switch).
#   Nix's content-addressed store and flake lockfile pins provide integrity
#   guarantees at the system level. The OTA script handles A/B slot switching
#   and post-reboot health checks only — it does not verify signatures because
#   the Nix build chain already does this. If a non-GitHub distribution model
#   is adopted in the future, add signature verification at that point.
# ─────────────────────────────────────────────────────────────────────────────

{ config, lib, pkgs, ... }:

# ── Module-level option declarations ─────────────────────────────────────────

let
  cfg = config.services.dplaneos;

# ── OTA script package ────────────────────────────────────────────────────────
# Reads ota-update.sh at build time and wraps it with a hermetic PATH so
# the script works identically regardless of the host's environment.

  otaScript = pkgs.writeShellScriptBin "dplaneos-ota-update" (
    builtins.readFile ./ota-update.sh
  );

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

# ── Options ───────────────────────────────────────────────────────────────────

  options.services.dplaneos.ota = {

    enable = lib.mkOption {
      type    = lib.types.bool;
      default = true;
      description = ''
        Enable the OTA update health-check timer.
        When true, a one-shot systemd timer fires 90 seconds after boot and
        runs the post-update health check if a pending-revert marker exists.
        Disable only if you are managing updates entirely outside of D-PlaneOS.
      '';
    };

    healthCheckDelay = lib.mkOption {
      type    = lib.types.str;
      default = "90s";
      description = ''
        How long after boot to wait before running the OTA health check.
        Must be a valid systemd time span (e.g. "90s", "2min").
        Increase this if your services take longer than 90 seconds to start.
      '';
    };

  };

# ── Configuration ─────────────────────────────────────────────────────────────

  config = lib.mkIf (cfg.enable && cfg.ota.enable) {

    # ── Install the OTA script system-wide ──────────────────────────────────
    # Makes `dplaneos-ota-update` available in PATH for both the systemd
    # service and manual invocation by the administrator.
    environment.systemPackages = [ otaPackage ];

    # ── Health check service ─────────────────────────────────────────────────
    # Runs once after boot. Checks daemon API, ZFS pool health, /persist mount,
    # and Samba if shares are configured. If a pending-revert marker exists and
    # any check fails, initiates an automatic slot revert and reboots.
    # If no marker exists, exits immediately (no-op on normal boots).
    systemd.services.dplaneos-ota-health = {
      description = "D-PlaneOS OTA Update Health Check";
      after       = [
        "network.target"
        "dplaned.service"
        "smbd.service"
        "zfs.target"
      ];
      # NOT in wantedBy — activated exclusively by the timer below.
      # This prevents the health check from running on every boot even
      # when no OTA update is pending.
      serviceConfig = {
        Type            = "oneshot";
        RemainAfterExit = false;
        ExecStart       = "${otaPackage}/bin/dplaneos-ota-update --health-check";
        # Prefix with '-' so systemd does not treat a non-zero exit as
        # a service failure when the script self-initiates a reboot.
        ExecStartPost   = "-/bin/true";
      };
    };

    # ── Health check timer ───────────────────────────────────────────────────
    # One-shot timer — fires once per boot, never repeats.
    # If no pending-revert marker is present, the service exits in <1s.
    # Configured delay is tunable via services.dplaneos.ota.healthCheckDelay.
    systemd.timers.dplaneos-ota-health = {
      description = "D-PlaneOS OTA Health Check (post-boot, one-shot)";
      wantedBy    = [ "timers.target" ];
      timerConfig = {
        OnBootSec = cfg.ota.healthCheckDelay;
        Unit      = "dplaneos-ota-health.service";
        # No OnUnitActiveSec — ensures the timer fires exactly once per boot.
      };
    };

    # ── Persistent OTA state directory ──────────────────────────────────────
    # /persist/ota holds the pending-revert marker, active-slot marker,
    # and the OTA log. Lives on the persist partition so it survives reboots.
    systemd.tmpfiles.rules = [
      "d /persist/ota 0750 root root -"
    ];

  };
}
