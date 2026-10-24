#!/usr/bin/env bash
#
# DPlaneOS - Hot-swap Disk Added Notification
#
# Called by udev when a pool-eligible disk (SATA/SAS/NVMe) is connected.
# Uses a retry loop identical to notify-device-added.sh to handle the
# kernel partition-table settle race that causes lsblk to return empty
# data on fast hot-plug events.
#
# Usage: notify-disk-added.sh <device> <type> <serial> <wwn>
#   device - full device path, e.g. /dev/sda
#   type   - sata | nvme | sas
#   serial - udev ID_SERIAL (may be empty string)
#   wwn    - udev ID_WWN   (may be empty string)
#

DEVICE=$1
TYPE=$2
SERIAL=$3
WWN=$4

DAEMON_SOCK=/run/dplaneos/dplaned.sock

# Wait for device to settle - kernel needs time after a hot-plug event
# before lsblk returns valid MODEL/SIZE data. Without this loop, the
# first attempt typically returns empty output on fast SATA hot-swap.
MAX_RETRIES=5
RETRY_DELAY=1

for i in $(seq 1 $MAX_RETRIES); do
    SIZE=$(lsblk -n -d -o SIZE "$DEVICE" 2>/dev/null | head -1 | xargs)

    # Device is ready once we get a non-empty, non-zero size
    if [ -n "$SIZE" ] && [ "$SIZE" != "0B" ]; then
        break
    fi

    sleep $RETRY_DELAY
done

# Collect device info now that it has settled
MODEL=$(lsblk -n -d -o MODEL "$DEVICE" 2>/dev/null | head -1 | xargs)

# Defaults when data is unavailable
[ -z "$MODEL" ] && MODEL="Unknown"
[ -z "$SIZE"  ] && SIZE="Unknown"

# Log via syslog
logger -t dplaneos "Disk added: $DEVICE (type=$TYPE model=$MODEL size=$SIZE serial=$SERIAL wwn=$WWN)"

# POST event to daemon HTTP API
TOKEN=$(cat /run/dplaneos/internal-token 2>/dev/null) || exit 0  # daemon not running
curl -sf --max-time 5 --unix-socket "$DAEMON_SOCK" -X POST "http://localhost/api/internal/disk-event" \
    -H "Content-Type: application/json" -H "X-Internal-Token: $TOKEN" \
    -d "{\"action\":\"added\",\"device\":\"$DEVICE\",\"device_type\":\"$TYPE\",\"serial\":\"$SERIAL\",\"wwn\":\"$WWN\",\"model\":\"$MODEL\",\"size\":\"$SIZE\"}" || true

exit 0

