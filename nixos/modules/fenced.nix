# nixos/modules/fenced.nix
# ─────────────────────────────────────────────────────────────────────────────
# DPlaneOS SCSI-3 Persistent Reservation Fencing Module
#
# WHAT THIS DOES:
#   Manages the dplane-fenced systemd service, which holds SCSI-3 persistent
#   reservations on all ZFS pool member disks. When an HA node fails, the
#   surviving node's reservations prevent the dead node from re-importing pools
#   even if it comes back up, because the disk controller rejects I/O from any
#   host that does not hold the reservation. This is the shared-SAS equivalent
#   of IPMI power-off fencing.
#
# CRITICAL CONSTRAINT - do NOT add BindsTo = dplaned.service here:
#   dplane-fenced MUST survive dplaned restarts. If the main daemon crashes and
#   systemd respawns it, the SCSI reservations must remain held continuously.
#   BindsTo would cause fenced to stop whenever dplaned stops, dropping
#   reservations exactly when the cluster is most vulnerable.
#
#   The service uses Wants + After for ordering: fenced starts after dplaned
#   but is NOT stopped if dplaned stops.
#
# CGROUP ISOLATION:
#   fenced.service runs in its own slice (dplaneos-fenced.slice) so it is not
#   terminated when the dplaneos.slice (which owns dplaned.service) is reset
#   by systemd during a failed-unit recovery. This matches the TrueNAS fenced
#   binary cgroup escape pattern.
#
# HARDWARE REQUIREMENT:
#   SCSI-3 persistent reservations require SAS/SATA disks connected via a
#   shared backplane. They are not supported on NVMe (which uses a different
#   reservation model) or on virtualized disks. This module is a no-op on
#   hardware where the disks do not respond to PRIN/PROUT commands.
#
# USAGE:
#   In configuration.nix:
#     services.dplaneos.fenced.enable = true;
#
#   For HA clusters with shared-SAS only:
#     services.dplaneos.fenced = {
#       enable     = true;
#       logLevel   = "debug";  # during initial setup
#     };
# ─────────────────────────────────────────────────────────────────────────────

{ config, lib, pkgs, ... }:

let
  cfg = config.services.dplaneos.fenced;
in {

  # ── Option declarations ─────────────────────────────────────────────────────

  options.services.dplaneos.fenced = {

    enable = lib.mkEnableOption "DPlaneOS SCSI-3 persistent reservation fencing";

    package = lib.mkOption {
      type        = lib.types.package;
      description = "The dplane-fenced binary package.";
    };

    socketPath = lib.mkOption {
      type    = lib.types.str;
      default = "/run/dplaneos/fenced.sock";
      readOnly = true;
      description = ''
        Unix socket path for communication between dplaned and dplane-fenced.
        Read-only: changing this would desync the two processes.
      '';
    };

    refreshInterval = lib.mkOption {
      type    = lib.types.int;
      default = 30;
      description = ''
        Seconds between disk enumeration refreshes. dplane-fenced re-scans
        ZFS pool members at this interval and fences any newly added disks.
      '';
    };

    logLevel = lib.mkOption {
      type    = lib.types.enum [ "info" "debug" ];
      default = "info";
      description = "Logging verbosity for dplane-fenced.";
    };

  };

  # ── Implementation ──────────────────────────────────────────────────────────

  config = lib.mkIf cfg.enable {

    # ── Dedicated systemd slice for cgroup isolation ──────────────────────────
    # This slice is independent of dplaneos.slice (which owns dplaned.service).
    # When systemd resets dplaneos.slice on a daemon crash, this slice and its
    # services are unaffected.
    systemd.slices.dplaneos-fenced = {
      description = "DPlaneOS SCSI fencing slice";
      sliceConfig = {
        # Memory and CPU are not constrained here; fenced is idle most of the
        # time (no disk I/O between reservation refreshes).
        DefaultDependencies = "no";
      };
    };

    # ── dplane-fenced service ─────────────────────────────────────────────────
    systemd.services.dplane-fenced = {
      description = "DPlaneOS SCSI-3 Persistent Reservation Fencing";
      documentation = [ "https://github.com/4nonX/DPlaneOS/blob/main/docs/fencing.md" ];

      # Start after ZFS pools are available and dplaned has written state.
      # Wants (not Requires): if dplaned is not yet up, fenced still starts
      # since it only reads zpool status (not dplaned's API).
      after    = [ "zfs.target" "dplaned.service" "network.target" ];
      wants    = [ "dplaned.service" ];
      wantedBy = [ "multi-user.target" ];

      # Run in the isolated slice, not the default dplaneos.slice.
      serviceConfig = {
        Slice = "dplaneos-fenced.slice";

        Type            = "simple";
        ExecStart       = "${cfg.package}/bin/dplane-fenced";
        Restart         = "on-failure";
        RestartSec      = "5s";
        # On hard failure, do not retry indefinitely: after 3 failures in 60s,
        # give up and leave existing reservations in place (they persist across
        # dplane-fenced restarts because APTPL=1 was set at registration time).
        StartLimitBurst = 3;
        StartLimitIntervalSec = "60s";

        # ── Capabilities ─────────────────────────────────────────────────────
        # CAP_SYS_RAWIO is required to issue SG_IO ioctls to SCSI devices.
        # No other capabilities are needed.
        AmbientCapabilities  = [ "CAP_SYS_RAWIO" ];
        CapabilityBoundingSet = [ "CAP_SYS_RAWIO" ];

        # ── Filesystem isolation ──────────────────────────────────────────────
        # fenced only needs:
        #   /dev         - SCSI generic devices (/dev/sgN)
        #   /run/dplaneos - its own socket
        #   /sys/class   - sysfs block-to-sg resolution
        #   /etc/machine-id - key derivation
        #   /proc/self   - standard Go runtime requirement
        ProtectSystem       = "strict";
        ProtectHome         = true;
        PrivateTmp          = true;
        PrivateNetwork      = false; # needs no network; kept false for simplicity
        ReadWritePaths      = [ "/run/dplaneos" ];
        ReadOnlyPaths       = [ "/etc/machine-id" "/sys/class/block" "/sys/class/scsi_generic" ];
        RuntimeDirectory    = "dplaneos";
        RuntimeDirectoryMode = "0750";

        # ── User/group ────────────────────────────────────────────────────────
        # Run as root: SG_IO requires CAP_SYS_RAWIO which cannot be delegated
        # to an unprivileged user without granting full raw device access.
        User  = "root";
        Group = "root";

        # ── Watchdog ──────────────────────────────────────────────────────────
        # If fenced hangs (e.g. stuck in a SCSI command), the watchdog kills
        # and restarts it after 60 seconds. Existing APTPL reservations survive
        # the restart because they are stored in the disk controller's NVRAM.
        WatchdogSec   = "60s";
        NotifyAccess  = "main";

        # ── Security hardening ────────────────────────────────────────────────
        NoNewPrivileges     = true;
        LockPersonality     = true;
        MemoryDenyWriteExecute = true;
        RestrictRealtime    = true;
        RestrictSUIDSGID    = true;
        SystemCallFilter    = [ "@system-service" "ioctl" ];
        SystemCallErrorNumber = "EPERM";
      };
    };

    # ── Extend dplaned's ReadWritePaths for the shared socket directory ───────
    # dplaned's fenced_client.go connects to /run/dplaneos/fenced.sock.
    # /run/dplaneos is already in dplaned's RuntimeDirectory; this is belt-and-
    # suspenders to ensure the path remains writable when both services are up.
    systemd.services.dplaned.serviceConfig.ReadWritePaths =
      lib.mkAfter [ "/run/dplaneos" ];

  };
}
