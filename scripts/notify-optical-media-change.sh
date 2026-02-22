#!/bin/bash
#
# D-PlaneOS - Optical Media Change Notification
#
# Called by udev when optical media is inserted or removed (disc change event)
#

DEVICE=$1

# Check if disc is present
if [ -b "$DEVICE" ]; then
    LABEL=$(blkid -o value -s LABEL "$DEVICE" 2>/dev/null)
    FSTYPE=$(blkid -o value -s TYPE "$DEVICE" 2>/dev/null)
    SIZE=$(blockdev --getsize64 "$DEVICE" 2>/dev/null)
    
    if [ -n "$LABEL" ] || [ -n "$FSTYPE" ]; then
        logger -t dplaneos "Optical media inserted: $DEVICE ($LABEL, $FSTYPE)"
        
        NOTIFY_DIR="/var/lib/dplaneos/notifications"
        mkdir -p "$NOTIFY_DIR"
        cat > "$NOTIFY_DIR/optical-$(basename $DEVICE).json" <<EOJSON
{
    "type": "optical_media_inserted",
    "device": "$DEVICE",
    "label": "${LABEL:-Unknown Disc}",
    "filesystem": "$FSTYPE",
    "size": "$SIZE",
    "timestamp": $(date +%s)
}
EOJSON
    else
        logger -t dplaneos "Optical media removed: $DEVICE"
        rm -f "/var/lib/dplaneos/notifications/optical-$(basename $DEVICE).json"
    fi
fi

# Trigger notification via daemon (if available)
if [ -S /var/run/dplaneos-daemon.sock ]; then
    echo "notify:optical_change:$DEVICE" | nc -U /var/run/dplaneos-daemon.sock || true
fi

exit 0
