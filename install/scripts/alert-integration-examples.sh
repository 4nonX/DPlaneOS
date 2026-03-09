#!/bin/bash
#
# D-PlaneOS Alert Integration Example
# 
# Shows how to create alerts from system monitoring scripts
# This would run periodically via cron/systemd timer
#

# Example: ZFS Pool Health Monitoring with Alert Throttling
check_zfs_health() {
    local pool=$1
    local status=$(zpool status $pool | grep "state:" | awk '{print $2}')
    
    if [ "$status" != "ONLINE" ]; then
        # Create critical alert via API
        curl -X POST http://localhost/api/alerts \
            -H "Content-Type: application/json" \
            -H "X-CSRF-Token: $(get_csrf_token)" \
            -d "{
                \"category\": \"zfs\",
                \"priority\": \"critical\",
                \"title\": \"ZFS Pool $pool Degraded\",
                \"message\": \"Pool $pool is in $status state. Immediate attention required!\",
                \"group_key\": \"zfs_pool_${pool}_degraded\",
                \"details\": {
                    \"pool\": \"$pool\",
                    \"status\": \"$status\",
                    \"checked_at\": \"$(date -Iseconds)\"
                }
            }"
    fi
}

# Example: Disk SMART Errors with Throttling
check_disk_smart() {
    local disk=$1
    
    # Get SMART errors
    local errors=$(smartctl -a /dev/$disk | grep "Reallocated_Sector_Ct" | awk '{print $10}')
    
    if [ "$errors" -gt 0 ]; then
        # Warning alert (bell notification)
        curl -X POST http://localhost/api/alerts \
            -H "Content-Type: application/json" \
            -H "X-CSRF-Token: $(get_csrf_token)" \
            -d "{
                \"category\": \"disk\",
                \"priority\": \"warning\",
                \"title\": \"SMART Errors on $disk\",
                \"message\": \"Disk $disk has $errors reallocated sectors\",
                \"group_key\": \"disk_${disk}_smart_errors\",
                \"details\": {
                    \"disk\": \"$disk\",
                    \"error_count\": \"$errors\"
                }
            }"
    fi
}

# Example: Temperature Monitoring
check_temperature() {
    local temp=$(sensors | grep "CPU Temperature" | awk '{print $3}' | sed 's/+//;s/°C//')
    
    if (( $(echo "$temp > 80" | bc -l) )); then
        # Warning alert
        curl -X POST http://localhost/api/alerts \
            -H "Content-Type: application/json" \
            -H "X-CSRF-Token: $(get_csrf_token)" \
            -d "{
                \"category\": \"hardware\",
                \"priority\": \"warning\",
                \"title\": \"High CPU Temperature\",
                \"message\": \"CPU temperature is ${temp}°C (threshold: 80°C)\",
                \"group_key\": \"cpu_temp_high\",
                \"details\": {
                    \"temperature\": \"${temp}°C\",
                    \"threshold\": \"80°C\"
                }
            }"
    fi
}

# Example: USB Device Added (Normal alert, auto-dismiss)
on_usb_added() {
    local device=$1
    local label=$2
    
    # Normal alert (toast, auto-dismiss 10s)
    curl -X POST http://localhost/api/alerts \
        -H "Content-Type: application/json" \
        -H "X-CSRF-Token: $(get_csrf_token)" \
        -d "{
            \"category\": \"removable_media\",
            \"priority\": \"normal\",
            \"title\": \"USB Device Connected\",
            \"message\": \"$label connected as $device\",
            \"details\": {
                \"device\": \"$device\",
                \"label\": \"$label\"
            }
        }"
}

# Example: Alert Flood Prevention
# This shows how the system prevents 1000 checksum errors from flooding the UI
simulate_zfs_checksum_flood() {
    # Simulate 100 checksum errors in quick succession
    for i in {1..100}; do
        # All use same group_key - will be throttled/grouped
        curl -X POST http://localhost/api/alerts \
            -H "Content-Type: application/json" \
            -H "X-CSRF-Token: $(get_csrf_token)" \
            -d "{
                \"category\": \"zfs\",
                \"priority\": \"warning\",
                \"title\": \"ZFS Checksum Error\",
                \"message\": \"Checksum error on dataset tank/data\",
                \"group_key\": \"zfs_checksum_tank_data\"
            }" &
    done
    
    wait
    
    # Result: Only 1 alert created with count=100
    # Message becomes: "(100 occurrences) Checksum error..."
}

# PHP Integration Example (for use in other PHP files)
# PHP example removed — use Go API: curl -X POST http://localhost/api/alerts

# Main
echo "D-PlaneOS Alert Integration Examples"
echo "======================================"
echo ""
echo "These examples show how to integrate alerts into your monitoring scripts."
echo ""
echo "Run individual checks:"
echo "  check_zfs_health tank"
echo "  check_disk_smart sda"
echo "  check_temperature"
echo ""
echo "Test alert flood prevention:"
echo "  simulate_zfs_checksum_flood"
