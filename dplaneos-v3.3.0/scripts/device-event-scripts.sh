#!/bin/bash
#
# D-PlaneOS - Device Event Notification Scripts
#
# Multiple scripts in one file for easier management
#

SCRIPT_NAME=$(basename "$0")

# ==================== Device Removed ====================
if [ "$SCRIPT_NAME" = "notify-device-removed.sh" ]; then
    DEVICE=$1
    TYPE=$2
    
    logger -t dplaneos "Removable device disconnected: $DEVICE"
    
    # Remove notification file
    NOTIFY_DIR="/var/lib/dplaneos/notifications"
    rm -f "$NOTIFY_DIR/device-$(basename $DEVICE).json"
    
    # Notify daemon
    if [ -S /var/run/dplaneos-daemon.sock ]; then
        echo "notify:device_removed:$DEVICE" | nc -U /var/run/dplaneos-daemon.sock || true
    fi
    
    exit 0
fi

# ==================== Optical Media Change ====================
if [ "$SCRIPT_NAME" = "notify-optical-media-change.sh" ]; then
    DEVICE=$1
    
    # Check if media is present
    if blkid "$DEVICE" >/dev/null 2>&1; then
        # Media inserted
        MEDIA_TYPE="unknown"
        
        # Try to detect media type
        SIZE_INFO=$(isoinfo -d -i "$DEVICE" 2>/dev/null | grep "Volume size" | awk '{print $4}')
        if [ -n "$SIZE_INFO" ]; then
            SIZE_MB=$(( SIZE_INFO * 2048 / 1024 / 1024 ))
            
            if [ $SIZE_MB -gt 40000 ]; then
                MEDIA_TYPE="bluray"
            elif [ $SIZE_MB -gt 4000 ]; then
                MEDIA_TYPE="dvd"
            else
                MEDIA_TYPE="cd"
            fi
        fi
        
        logger -t dplaneos "Optical media inserted in $DEVICE: $MEDIA_TYPE"
        
        # Create notification
        NOTIFY_DIR="/var/lib/dplaneos/notifications"
        mkdir -p "$NOTIFY_DIR"
        
        cat > "$NOTIFY_DIR/optical-$(basename $DEVICE).json" <<EOF
{
    "type": "media_inserted",
    "device": "$DEVICE",
    "media_type": "$MEDIA_TYPE",
    "timestamp": $(date +%s)
}
EOF
        
        # Notify daemon
        if [ -S /var/run/dplaneos-daemon.sock ]; then
            echo "notify:media_inserted:$DEVICE:$MEDIA_TYPE" | nc -U /var/run/dplaneos-daemon.sock || true
        fi
    else
        # Media removed
        logger -t dplaneos "Optical media removed from $DEVICE"
        
        rm -f "/var/lib/dplaneos/notifications/optical-$(basename $DEVICE).json"
        
        if [ -S /var/run/dplaneos-daemon.sock ]; then
            echo "notify:media_removed:$DEVICE" | nc -U /var/run/dplaneos-daemon.sock || true
        fi
    fi
    
    exit 0
fi

# ==================== Eject Button Pressed ====================
if [ "$SCRIPT_NAME" = "handle-eject-request.sh" ]; then
    DEVICE=$1
    
    logger -t dplaneos "Eject button pressed on $DEVICE"
    
    # Check if mounted
    MOUNTPOINT=$(lsblk -n -o MOUNTPOINT "$DEVICE" 2>/dev/null)
    
    if [ -n "$MOUNTPOINT" ]; then
        # Try to unmount
        umount "$MOUNTPOINT" 2>/dev/null
        
        if [ $? -eq 0 ]; then
            logger -t dplaneos "Auto-unmounted $DEVICE from $MOUNTPOINT"
            
            # Eject disc
            eject "$DEVICE" 2>/dev/null
        else
            logger -t dplaneos "Cannot eject $DEVICE - device in use"
            
            # Notify user
            if [ -S /var/run/dplaneos-daemon.sock ]; then
                echo "notify:eject_failed:$DEVICE:Device in use" | nc -U /var/run/dplaneos-daemon.sock || true
            fi
        fi
    else
        # Not mounted, just eject
        eject "$DEVICE" 2>/dev/null
    fi
    
    exit 0
fi

echo "Unknown script name: $SCRIPT_NAME"
exit 1
