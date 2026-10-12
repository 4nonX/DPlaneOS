# D-PlaneOS Live Boot: Automatic ZFS Pool Discovery and Import
# ─────────────────────────────────────────────────────────────────────────────
# On boot, automatically scan for ZFS pools on all block devices and import them.
# This is the primary use case for live boot: manage existing storage without
# requiring installation.
#
# What this does:
#   1. Runs early in boot (after zfs module loads, before services start)
#   2. Scans all block devices for ZFS pool metadata
#   3. Imports any pools found with standard settings
#   4. Mounts all imported datasets
#   5. Logs results to journalctl for debugging
#
# What this does NOT do:
#   - Does not create new pools (user must do via daemon UI or CLI)
#   - Does not modify pool properties (read-write import only)
#   - Does not handle pool conflicts (will fail gracefully if pool already imported)

{ config, lib, pkgs, ... }:

{
  boot.supportedFilesystems = [ "zfs" ];

  systemd.services.dplane-zfs-auto-import = {
    description = "D-PlaneOS: Automatic ZFS Pool Discovery and Import";

    # Run after ZFS kernel module is loaded and standard ZFS import completes
    after = [
      "zfs-import.service"
      "zfs-mount.service"
    ];

    # Start before services that need pools (daemon, docker)
    before = [
      "dplaned.service"
      "docker.service"
    ];

    # Run once at boot and stay running (Type = oneshot + RemainAfterExit = true)
    wantedBy = [ "multi-user.target" ];

    # Run as root (required for zpool commands)
    serviceConfig.Type = "oneshot";
    serviceConfig.RemainAfterExit = true;
    serviceConfig.User = "root";
    serviceConfig.Group = "root";

    # Script to perform auto-import
    script = ''
      set -e  # Fail on any error for visibility, but catch below

      export PATH=${lib.makeBinPath [ config.boot.zfs.package pkgs.util-linux pkgs.coreutils pkgs.gnugrep ]}:$PATH

      echo "=== D-PlaneOS ZFS Auto-Import ==="
      echo "Timestamp: $(date -u +%Y-%m-%dT%H:%M:%SZ)"

      # Step 1: List all block devices (for debugging)
      echo ""
      echo "[1/4] Scanning block devices..."
      DEVICES=$(lsblk -nd -o NAME,SIZE,TYPE 2>/dev/null | grep disk || echo "(no disks found)")
      echo "$DEVICES"

      # Step 2: import the pools that are free to import.
      # -a: all pools found; -N: do not mount yet (step 4).
      # Never -f: a pool that was last used by another system and not
      # exported there may still be in use (a shelf shared with another
      # node, a disk taken from a running machine), and forcing it in would
      # let two systems write to it. Such pools are listed and left alone;
      # the administrator imports them under Storage, ZFS Pools, Import,
      # where the question is asked.
      echo ""
      echo "[2/4] Importing ZFS pools..."
      if zpool import -a -N 2>&1; then
        IMPORT_STATUS="OK"
        echo "✓ Pool import succeeded"
      else
        IMPORT_STATUS="PARTIAL"
        echo "⚠ Not every pool was imported"
      fi
      LEFT=$(zpool import 2>/dev/null | grep -c '^ *pool:' || true)
      if [ "''${LEFT:-0}" -gt 0 ]; then
        echo "$LEFT pool(s) were not imported (last used by another system, or incomplete):"
        zpool import 2>/dev/null | grep -E '^ *(pool|state|status):' || true
        echo "Import them in the web interface (Storage, ZFS Pools, Import) if no other system uses them."
      fi

      # Step 3: List imported pools (verify success)
      echo ""
      echo "[3/4] Listing imported pools..."
      POOL_COUNT=$(zpool list -H 2>/dev/null | wc -l)
      if [ "$POOL_COUNT" -gt 0 ]; then
        echo "Found $POOL_COUNT pool(s):"
        zpool list -H 2>/dev/null | while read -r line; do
          echo "  - $line"
        done
      else
        echo "(no pools found or imported)"
      fi

      # Step 4: Mount all datasets
      echo ""
      echo "[4/4] Mounting ZFS datasets..."
      if zfs mount -a 2>&1; then
        echo "✓ Dataset mounting succeeded"
      else
        echo "⚠ Dataset mounting had errors (non-fatal)"
      fi

      # Final status
      echo ""
      echo "=== Auto-Import Complete ==="
      echo "Status: $IMPORT_STATUS"
      echo "Pools available: $(zpool list -H 2>/dev/null | wc -l)"
      echo ""
    '';

    # At shutdown the pools are exported: the system that has them
    # afterwards (the installed one, most of the time) finds them free and
    # imports them without being asked to force anything. The daemon and
    # Docker are stopped before this unit (it is ordered before them).
    preStop = ''
      export PATH=${lib.makeBinPath [ config.boot.zfs.package pkgs.coreutils ]}:$PATH
      for pool in $(zpool list -H -o name 2>/dev/null); do
        zpool export "$pool" 2>&1 || echo "pool $pool could not be exported: the next system has to import it with force"
      done
    '';

    # Explicitly set TERM for better output
    environment.TERM = "xterm-256color";

    # Don't fail boot if auto-import fails (pools may not be present)
    # But log failures for debugging
    serviceConfig.StandardOutput = "journal";
    serviceConfig.StandardError = "journal";
  };

  # ── Optional: systemd timer to re-scan for pools (in case drives plugged in later)
  # For MVP, disabled. Can be enabled in future for hot-swap support.
  # systemd.timers.dplane-zfs-rescan = {
  #   description = "Periodically rescan for new ZFS pools";
  #   timerConfig.OnBootSec = "10min";
  #   timerConfig.OnUnitActiveSec = "30min";
  #   wantedBy = [ "timers.target" ];
  # };
  #
  # systemd.services.dplane-zfs-rescan = {
  #   description = "ZFS pool rescan";
  #   script = ''...similar to above...'';
  # };
}
