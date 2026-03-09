#!/bin/bash
#
# D-PlaneOS ZED Hook — Real-time ZFS Event Notification
#
# Install to: /etc/zfs/zed.d/dplaneos-notify.sh
# ZED calls this script on disk failures, scrub results, resilver events, etc.
# The daemon gets IMMEDIATE notification instead of waiting for polling.
#
# ZED provides these environment variables:
#   ZEVENT_CLASS    — Event type (e.g., "statechange", "scrub_finish")
#   ZEVENT_SUBCLASS — Sub-event (e.g., "pool_degraded")
#   ZEVENT_POOL     — Affected pool name
#   ZEVENT_VDEV_PATH — Failed device path (if applicable)
#   ZEVENT_VDEV_STATE_STR — New device state

NOTIFY_DIR="/var/lib/dplaneos/notifications"
DAEMON_SOCKET="/run/dplaneos/dplaneos.sock"
LOG_TAG="dplaneos-zed"

mkdir -p "$NOTIFY_DIR"

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

TIMESTAMP=$(date +%s)

# Write event file for UI polling
cat > "$NOTIFY_DIR/zfs-event-${TIMESTAMP}.json" <<EOF
{
    "type": "zfs_event",
    "severity": "$SEVERITY",
    "class": "$ZEVENT_CLASS",
    "subclass": "$ZEVENT_SUBCLASS",
    "pool": "$ZEVENT_POOL",
    "vdev_path": "$ZEVENT_VDEV_PATH",
    "vdev_state": "$ZEVENT_VDEV_STATE_STR",
    "timestamp": $TIMESTAMP
}
EOF

# Log to syslog
logger -t "$LOG_TAG" "[$SEVERITY] Pool=$ZEVENT_POOL Event=$ZEVENT_SUBCLASS State=$ZEVENT_VDEV_STATE_STR Device=$ZEVENT_VDEV_PATH"

# Notify daemon via socket (non-blocking, fire-and-forget)
if [ -S "$DAEMON_SOCKET" ]; then
    echo "zfs_event:$SEVERITY:$ZEVENT_POOL:$ZEVENT_SUBCLASS:$ZEVENT_VDEV_STATE_STR" \
        | timeout 2 nc -U "$DAEMON_SOCKET" 2>/dev/null || true
fi

# Critical events: also attempt Telegram alert directly
if [ "$SEVERITY" = "critical" ]; then
    # Read Telegram config from DB (if sqlite3 is available)
    if command -v sqlite3 &>/dev/null; then
        DB="/var/lib/dplaneos/dplaneos.db"
        if [ -f "$DB" ]; then
            BOT_TOKEN=$(sqlite3 "$DB" "SELECT bot_token FROM telegram_config WHERE enabled=1 LIMIT 1" 2>/dev/null)
            CHAT_ID=$(sqlite3 "$DB" "SELECT chat_id FROM telegram_config WHERE enabled=1 LIMIT 1" 2>/dev/null)
            
            if [ -n "$BOT_TOKEN" ] && [ -n "$CHAT_ID" ]; then
                MSG="🚨 CRITICAL ZFS EVENT\nPool: $ZEVENT_POOL\nEvent: $ZEVENT_SUBCLASS\nDevice: $ZEVENT_VDEV_PATH\nState: $ZEVENT_VDEV_STATE_STR"
                curl -s -X POST "https://api.telegram.org/bot${BOT_TOKEN}/sendMessage" \
                    -d "chat_id=${CHAT_ID}" \
                    -d "text=${MSG}" \
                    -d "parse_mode=HTML" >/dev/null 2>&1 &
            fi
        fi
    fi
fi

exit 0
