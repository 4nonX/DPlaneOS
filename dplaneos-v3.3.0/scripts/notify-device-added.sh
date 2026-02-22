#!/bin/bash
#
# D-PlaneOS - Device Added Notification
#
# Called by udev when a removable device is connected.
# Uses retry loop to handle kernel partition table race condition:
# USB drives often need 1-3 seconds before lsblk returns valid data.
#

DEVICE=$1
TYPE=$2

# Wait for device to settle — kernel needs time to read partition table.
# Without this, lsblk returns empty MODEL/SIZE/FSTYPE on fast USB hotplug.
MAX_RETRIES=5
RETRY_DELAY=1

for i in $(seq 1 $MAX_RETRIES); do
    SIZE=$(lsblk -n -o SIZE "$DEVICE" 2>/dev/null | head -1 | xargs)
    FSTYPE=$(lsblk -n -o FSTYPE "$DEVICE" 2>/dev/null | head -1 | xargs)

    # If we got a non-empty size, device is ready
    if [ -n "$SIZE" ] && [ "$SIZE" != "0B" ] && [ "$SIZE" != "" ]; then
        break
    fi

    # Not ready yet — wait and retry
    sleep $RETRY_DELAY
done

# Now get full device info (device should be settled)
MODEL=$(lsblk -n -o MODEL "$DEVICE" 2>/dev/null | head -1 | xargs)
LABEL=$(lsblk -n -o LABEL "$DEVICE" 2>/dev/null | head -1 | xargs)

# Default label fallback
if [ -z "$LABEL" ]; then
    LABEL=$MODEL
fi
if [ -z "$LABEL" ]; then
    LABEL="Removable Device"
fi

# Log event
logger -t dplaneos "Removable device connected: $DEVICE ($LABEL, $SIZE, $FSTYPE)"

# Create notification file for UI polling
NOTIFY_DIR="/var/lib/dplaneos/notifications"
mkdir -p "$NOTIFY_DIR"

cat > "$NOTIFY_DIR/device-$(basename $DEVICE).json" <<EOF
{
    "type": "device_added",
    "device": "$DEVICE",
    "device_type": "$TYPE",
    "label": "$LABEL",
    "model": "$MODEL",
    "size": "$SIZE",
    "filesystem": "$FSTYPE",
    "timestamp": $(date +%s)
}
EOF

# Trigger notification via daemon socket (if available)
if [ -S /run/dplaneos/dplaneos.sock ]; then
    echo "notify:device_added:$DEVICE:$LABEL" | nc -U /run/dplaneos/dplaneos.sock || true
fi

exit 0
