#!/bin/bash
#
# D-PlaneOS ZFS Mount Readiness Gate
#
# This script is the hard gate between ZFS pool import and Docker startup.
# It BLOCKS until ALL configured ZFS pools are fully mounted and writable.
#
# Why this exists:
#   After a power loss or with large pools (>10TB), ZFS pool import can take
#   30-120 seconds. If Docker starts before ZFS mounts are ready, containers
#   will write to the bare (empty) mountpoint directories on the root FS
#   instead of the ZFS dataset. When ZFS mounts later, those writes are
#   invisible and the container "loses" its data. This is a silent data loss
#   scenario. This gate prevents it entirely.
#
# Behavior:
#   - If no ZFS pools exist yet (fresh install): passes immediately
#   - If pools are configured: waits up to 5 minutes for all to be mounted
#   - If a pool fails to mount within timeout: logs error, blocks Docker
#   - Writes readiness marker to /run/dplaneos/zfs-ready for other services

set -euo pipefail

READY_MARKER="/run/dplaneos/zfs-ready"
TIMEOUT=300        # 5 minutes maximum wait
POLL_INTERVAL=2    # check every 2 seconds
LOG_TAG="dplaneos-zfs-gate"

log()  { echo "$(date '+%Y-%m-%d %H:%M:%S') [INFO]  $*" | tee -a /var/log/dplaneos/zfs-gate.log; logger -t "$LOG_TAG" "$*"; }
warn() { echo "$(date '+%Y-%m-%d %H:%M:%S') [WARN]  $*" | tee -a /var/log/dplaneos/zfs-gate.log; logger -t "$LOG_TAG" -p daemon.warning "$*"; }
fail() { echo "$(date '+%Y-%m-%d %H:%M:%S') [ERROR] $*" | tee -a /var/log/dplaneos/zfs-gate.log; logger -t "$LOG_TAG" -p daemon.err "$*"; }

mkdir -p /run/dplaneos /var/log/dplaneos
rm -f "$READY_MARKER"

# --- Step 1: Check if ZFS is available at all ---
if ! command -v zpool &>/dev/null; then
    log "ZFS not available — gate passed (no ZFS to wait for)"
    touch "$READY_MARKER"
    exit 0
fi

# --- Step 2: Get list of pools that should be imported ---
CONFIGURED_POOLS=""
if [ -f /etc/dplaneos/expected-pools.conf ]; then
    # Admin-configured list of pools that MUST be mounted before Docker starts
    CONFIGURED_POOLS=$(grep -v '^#' /etc/dplaneos/expected-pools.conf | grep -v '^$' || true)
fi

if [ -z "$CONFIGURED_POOLS" ]; then
    # Auto-detect: use currently imported pools (best effort for first boot)
    CONFIGURED_POOLS=$(zpool list -H -o name 2>/dev/null || true)
fi

if [ -z "$CONFIGURED_POOLS" ]; then
    log "No ZFS pools configured — gate passed (fresh install or no pools)"
    touch "$READY_MARKER"
    exit 0
fi

log "Waiting for ZFS pools to be fully mounted: $(echo $CONFIGURED_POOLS | tr '\n' ' ')"

# --- Step 3: Poll until all pools are ONLINE and their mountpoints are writable ---
ELAPSED=0
while [ $ELAPSED -lt $TIMEOUT ]; do
    ALL_READY=true
    PENDING=""

    for POOL in $CONFIGURED_POOLS; do
        # Check pool health
        HEALTH=$(zpool list -H -o health "$POOL" 2>/dev/null || echo "UNAVAIL")
        
        if [ "$HEALTH" = "UNAVAIL" ] || [ "$HEALTH" = "FAULTED" ]; then
            warn "Pool '$POOL' health: $HEALTH — still waiting..."
            ALL_READY=false
            PENDING="$PENDING $POOL"
            continue
        fi

        # Check that datasets are actually mounted (not just pool imported)
        MOUNTED=$(zfs list -H -o name,mounted "$POOL" 2>/dev/null | awk '{print $2}' | head -1)
        if [ "$MOUNTED" != "yes" ]; then
            warn "Pool '$POOL' imported but not yet mounted — still waiting..."
            ALL_READY=false
            PENDING="$PENDING $POOL"
            continue
        fi

        # Check mountpoint is actually writable (the critical test)
        MOUNTPOINT=$(zfs get -H -o value mountpoint "$POOL" 2>/dev/null || echo "")
        if [ -z "$MOUNTPOINT" ] || [ "$MOUNTPOINT" = "none" ] || [ "$MOUNTPOINT" = "legacy" ]; then
            log "Pool '$POOL' has no standard mountpoint — skipping write check"
            continue
        fi

        if ! touch "${MOUNTPOINT}/.dplaneos_mount_probe" 2>/dev/null; then
            warn "Pool '$POOL' mountpoint '${MOUNTPOINT}' not writable yet — still waiting..."
            ALL_READY=false
            PENDING="$PENDING $POOL"
            continue
        fi
        rm -f "${MOUNTPOINT}/.dplaneos_mount_probe"
        log "Pool '$POOL' is ONLINE, mounted, and writable at '${MOUNTPOINT}' ✓"
    done

    if $ALL_READY; then
        log "All ZFS pools ready — releasing Docker/daemon gate after ${ELAPSED}s"
        touch "$READY_MARKER"
        exit 0
    fi

    sleep $POLL_INTERVAL
    ELAPSED=$((ELAPSED + POLL_INTERVAL))

    # Log progress every 30 seconds to avoid log spam
    if [ $((ELAPSED % 30)) -eq 0 ]; then
        log "Still waiting for pools:${PENDING} (${ELAPSED}s / ${TIMEOUT}s timeout)"
    fi
done

# --- Timeout reached ---
fail "TIMEOUT after ${TIMEOUT}s: ZFS pools not ready:${PENDING}"
fail "Docker and dplaned will NOT start until ZFS is mounted."
fail "Check: journalctl -u zfs-mount.service"
fail "Check: zpool status"
fail "To force override (DATA LOSS RISK): touch $READY_MARKER && systemctl start docker dplaned"
exit 1
