# D-PlaneOS Live Boot: Automatic ZFS Pool Discovery and Import
# ─────────────────────────────────────────────────────────────────────────────
# On boot, automatically scan for ZFS pools on all block devices and import them.
# This is the primary use case for live boot: manage existing storage without
# requiring installation.
#
# What this does:
#   1. Runs early in boot (after zfs module loads, before services start)
#   2. Scans all block devices for ZFS pool metadata
#   3. Imports the pools that are free to import: exported, or last used by
#      this live system. A pool another system used last and did not export
#      (the installed system, a cluster node on shared disks) is not forced:
#      importing it twice can destroy it. The log names it; when that system
#      is really off, `zpool import -f <pool>` takes it over.
#   4. Mounts all imported datasets
#   5. Logs results to journalctl for debugging
#   6. At shutdown, exports the pools, so the installed system (or another
#      machine) imports them again without a force

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

      # Step 2: import the pools that are free to import
      # -a: import all pools found
      # -N: don't mount (we do it separately)
      # no -f: a pool in use by (or last used by) another system stays as it is
      echo ""
      echo "[2/4] Importing ZFS pools..."
      if zpool import -a -N 2>&1; then
        IMPORT_STATUS="OK"
        echo "✓ Pool import succeeded"
      else
        IMPORT_STATUS="PARTIAL"
        echo "⚠ Some pools were not imported (above: a pool another system used last is not forced)"
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

    # At shutdown (dplaned and Docker have stopped: they start after this)
    preStop = "${config.boot.zfs.package}/bin/zpool export -a || true";

    # Explicitly set TERM for better output
    environment.TERM = "xterm-256color";

    # Don't fail boot if auto-import fails (pools may not be present)
    # But log failures for debugging
    serviceConfig.StandardOutput = "journal";
    serviceConfig.StandardError = "journal";
  };
}
