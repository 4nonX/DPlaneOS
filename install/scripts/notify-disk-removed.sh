#!/bin/bash
#
# D-PlaneOS - Hot-swap Disk Removed Notification
#
# Called by udev when a pool-eligible disk (SATA/SAS/NVMe) is disconnected.
# No settle wait is needed - the device is already gone by the time
# udev fires the remove action.
#
# Usage: notify-disk-removed.sh <device> <type> <serial>
#   device - full device path, e.g. /dev/sda
#   type   - sata | nvme | sas
#   serial - udev ID_SERIAL (may be empty string)
#

DEVICE=$1
TYPE=$2
SERIAL=$3

# Read daemon port from config, defaulting to 9000
DAEMON_PORT=9000
if [ -f /etc/dplaneos/daemon.conf ]; then
    _port=$(grep -E '^[[:space:]]*port[[:space:]]*=' /etc/dplaneos/daemon.conf \
            | head -1 | sed 's/.*=[[:space:]]*//' | tr -d '[:space:]')
    [ -n "$_port" ] && DAEMON_PORT="$_port"
fi

# Log via syslog
logger -t dplaneos "Disk removed: $DEVICE (type=$TYPE serial=$SERIAL)"

# POST event to daemon HTTP API
curl -sf --max-time 5 -X POST "http://127.0.0.1:${DAEMON_PORT}/api/internal/disk-event" \
    -H "Content-Type: application/json" \
    -d "{\"action\":\"removed\",\"device\":\"$DEVICE\",\"device_type\":\"$TYPE\",\"serial\":\"$SERIAL\"}" || true

exit 0

