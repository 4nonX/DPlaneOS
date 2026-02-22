#!/bin/bash
#
# D-PlaneOS - Device Removed Notification
#
# Called by udev when a removable device is disconnected
#

DEVICE=$1
TYPE=$2

# Log event
logger -t dplaneos "Removable device disconnected: $DEVICE ($TYPE)"

# Remove notification file
NOTIFY_DIR="/var/lib/dplaneos/notifications"
rm -f "$NOTIFY_DIR/device-$(basename $DEVICE).json"

# Create removal event
mkdir -p "$NOTIFY_DIR"
cat > "$NOTIFY_DIR/removed-$(basename $DEVICE).json" <<EOJSON
{
    "type": "device_removed",
    "device": "$DEVICE",
    "device_type": "$TYPE",
    "timestamp": $(date +%s)
}
EOJSON

# Trigger notification via daemon (if available)
if [ -S /var/run/dplaneos-daemon.sock ]; then
    echo "notify:device_removed:$DEVICE" | nc -U /var/run/dplaneos-daemon.sock || true
fi

exit 0
