# DPlaneOS - system updates
# ─────────────────────────────────────────────────────────────────────────────
# Installs dplaneos-update (nixos/dplaneos-update.sh) and what it needs:
#
#   /etc/dplaneos/update-signing.pub   the key update files are checked against
#   <system>/dplaneos-version          the version of each installed system
#   dplaneos-update-confirm.timer      a few minutes after every boot: an
#                                      update that is on trial is kept if the
#                                      system is healthy, removed if it is not
#
# An update is a new system generation, started once on trial; the previous
# generation stays installed to go back to. See docs/admin/OTA-UPDATES.md.
# ─────────────────────────────────────────────────────────────────────────────

{ config, lib, pkgs, ... }:

let
  cfg = config.services.dplaneos;

  updatePackage = pkgs.writeShellApplication {
    name = "dplaneos-update";
    runtimeInputs = [
      pkgs.coreutils pkgs.gawk pkgs.gnugrep pkgs.gnused pkgs.gnutar pkgs.zstd pkgs.gzip
      pkgs.util-linux pkgs.curl pkgs.openssl pkgs.python3 pkgs.systemd
      config.nix.package
      config.boot.zfs.package   # zpool: the pools must be back after an update
    ];
    # The script sets its own options and is checked as written.
    bashOptions = [ ];
    checkPhase = "";
    text = builtins.readFile ./dplaneos-update.sh;
  };

in {

  options.services.dplaneos.ota = {

    enable = lib.mkOption {
      type    = lib.types.bool;
      default = true;
      description = ''
        Install the update tool and check, a few minutes after every boot,
        whether an update that is on trial can be kept.
      '';
    };

    healthCheckDelay = lib.mkOption {
      type    = lib.types.str;
      default = "2min";
      description = ''
        How long after boot the check of an update on trial starts (a systemd
        time span). The check itself then waits up to five minutes for the
        system to become healthy.
      '';
    };

    publicKeyFile = lib.mkOption {
      type    = lib.types.path;
      default = ./update-signing.pub;
      description = ''
        Public key (PEM, Ed25519) update files must be signed with. The
        default is the key of the DPlaneOS releases. A system built from a
        fork that publishes its own updates sets its own key here.
      '';
    };

    releaseURL = lib.mkOption {
      type    = lib.types.nullOr lib.types.str;
      default = null;
      example = "https://api.github.com/repos/example/fork/releases/latest";
      description = ''
        Where the latest release is announced, in the format of GitHub's
        "latest release" API. Null: the DPlaneOS releases. Its update files
        must be signed with the key in publicKeyFile.
      '';
    };

    version = lib.mkOption {
      type    = lib.types.str;
      default = cfg.daemonPackage.version or "unknown";
      defaultText = "the version of the daemon package";
      description = "Version this system reports and updates are compared with.";
    };

  };

  config = lib.mkIf (cfg.enable && cfg.ota.enable) {

    environment.systemPackages = [ updatePackage ];
    environment.etc."dplaneos/update-signing.pub".source = cfg.ota.publicKeyFile;

    # The daemon runs the tool (Updates page).
    systemd.services.dplaned.path = [ updatePackage ];
    systemd.services.dplaned.environment = lib.mkIf (cfg.ota.releaseURL != null) {
      DPLANEOS_UPDATE_URL = cfg.ota.releaseURL;
    };

    # Every system knows its version: the running one, and each generation
    # that is installed next to it.
    system.extraSystemBuilderCmds = ''
      echo ${lib.escapeShellArg cfg.ota.version} > $out/dplaneos-version
    '';

    # A boot entry for the running system, the previous one, and one on trial.
    boot.loader.systemd-boot.configurationLimit = lib.mkDefault 4;

    systemd.services.dplaneos-update-confirm = {
      description = "Keep or remove a system update that is on trial";
      after = [ "network.target" "dplaned.service" "zfs.target" "multi-user.target" ];
      unitConfig.ConditionPathExists = "/var/lib/dplaneos/updates/pending";
      serviceConfig = {
        Type      = "oneshot";
        ExecStart = "${updatePackage}/bin/dplaneos-update confirm";
        # The check waits for the system to settle; then come the boot
        # loader and, if the update is removed, a restart.
        TimeoutStartSec = "20min";
      };
    };

    systemd.timers.dplaneos-update-confirm = {
      description = "Check a system update that is on trial";
      wantedBy    = [ "timers.target" ];
      timerConfig = {
        OnBootSec = cfg.ota.healthCheckDelay;
        Unit      = "dplaneos-update-confirm.service";
      };
    };

    systemd.tmpfiles.rules = [
      "d /var/lib/dplaneos/updates 0750 root root -"
    ];

  };
}
