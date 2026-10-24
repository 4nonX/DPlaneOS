#!/usr/bin/env bash
#
# DPlaneOS - Hot-swap Disk Removed Notification
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

DAEMON_SOCK=/run/dplaneos/dplaned.sock

# Log via syslog
logger -t dplaneos "Disk removed: $DEVICE (type=$TYPE serial=$SERIAL)"

# POST event to daemon HTTP API
TOKEN=$(cat /run/dplaneos/internal-token 2>/dev/null) || exit 0  # daemon not running
curl -sf --max-time 5 --unix-socket "$DAEMON_SOCK" -X POST "http://localhost/api/internal/disk-event" \
    -H "Content-Type: application/json" -H "X-Internal-Token: $TOKEN" \
    -d "{\"action\":\"removed\",\"device\":\"$DEVICE\",\"device_type\":\"$TYPE\",\"serial\":\"$SERIAL\"}" || true

exit 0

