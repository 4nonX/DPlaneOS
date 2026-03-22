#!/usr/bin/env bash
#
# D-PlaneOS ZED Hook - Real-time ZFS Event Notification
#
# Install to: /etc/zfs/zed.d/dplaneos-notify.sh
# ZED calls this script on disk failures, scrub results, resilver events, etc.
# The daemon gets IMMEDIATE notification instead of waiting for polling.
#
# ZED provides these environment variables:
#   ZEVENT_CLASS    - Event type (e.g., "statechange", "scrub_finish")
#   ZEVENT_SUBCLASS - Sub-event (e.g., "pool_degraded")
#   ZEVENT_POOL     - Affected pool name
#   ZEVENT_VDEV_PATH - Failed device path (if applicable)
#   ZEVENT_VDEV_STATE_STR - New device state

DAEMON_SOCKET="/run/dplaneos/dplaneos.sock"
LOG_TAG="dplaneos-zed"

# Map ZED events to severity levels
case "$ZEVENT_SUBCLASS" in
    pool_destroy|vdev_remove|device_removal)
        SEVERITY="critical"
        ;;
    statechange)
        case "$ZEVENT_VDEV_STATE_STR" in
            FAULTED|UNAVAIL|REMOVED)
                SEVERITY="critical"
                ;;
            DEGRADED)
                SEVERITY="warning"
                ;;
            *)
                SEVERITY="info"
                ;;
        esac
        ;;
    scrub_finish|resilver_finish)
        SEVERITY="info"
        ;;
    io_failure|checksum_failure)
        SEVERITY="warning"
        ;;
    *)
        SEVERITY="info"
        ;;
esac

# Log to syslog
logger -t "$LOG_TAG" "[$SEVERITY] Pool=$ZEVENT_POOL Event=$ZEVENT_SUBCLASS State=$ZEVENT_VDEV_STATE_STR Device=$ZEVENT_VDEV_PATH"

# Notify daemon via socket (non-blocking, fire-and-forget)
if [ -S "$DAEMON_SOCKET" ]; then
    echo "zfs_event:$SEVERITY:$ZEVENT_POOL:$ZEVENT_SUBCLASS:$ZEVENT_VDEV_STATE_STR" \
        | timeout 2 nc -U "$DAEMON_SOCKET" 2>/dev/null || true
fi

exit 0
