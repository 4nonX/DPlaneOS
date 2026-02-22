#!/bin/bash
#
# D-PlaneOS - Eject Button Handler
#
# Called by udev when the eject button is pressed on an optical drive
#

DEVICE=$1

logger -t dplaneos "Eject requested: $DEVICE"

# Unmount if mounted
MOUNTPOINT=$(findmnt -n -o TARGET "$DEVICE" 2>/dev/null)
if [ -n "$MOUNTPOINT" ]; then
    logger -t dplaneos "Unmounting $DEVICE from $MOUNTPOINT before eject"
    umount "$DEVICE" 2>/dev/null || {
        logger -t dplaneos "WARNING: Could not unmount $DEVICE — device busy"
        exit 1
    }
fi

# Eject the tray
eject "$DEVICE" 2>/dev/null || true

# Remove notification file
rm -f "/var/lib/dplaneos/notifications/optical-$(basename $DEVICE).json"

# Trigger notification via daemon (if available)
if [ -S /var/run/dplaneos-daemon.sock ]; then
    echo "notify:device_ejected:$DEVICE" | nc -U /var/run/dplaneos-daemon.sock || true
fi

exit 0
