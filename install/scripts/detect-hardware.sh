#!/bin/bash
# Hardware Detection Script for D-PlaneOS
# Detects system capabilities and recommends optimal settings

echo "╔════════════════════════════════════════════════════════════╗"
echo "║         D-PlaneOS Hardware Detection & Tuning             ║"
echo "╚════════════════════════════════════════════════════════════╝"
echo ""

# Detect CPU
detect_cpu() {
    CPU_VENDOR=$(grep -m1 "vendor_id" /proc/cpuinfo | awk '{print $3}')
    CPU_MODEL=$(grep -m1 "model name" /proc/cpuinfo | cut -d: -f2 | xargs)
    CPU_CORES=$(grep -c "^processor" /proc/cpuinfo)
    CPU_PHYSICAL=$(grep "^core id" /proc/cpuinfo | sort -u | wc -l)
    
    if [ "$CPU_PHYSICAL" -eq 0 ]; then
        CPU_PHYSICAL=$CPU_CORES
    fi
    
    case "$CPU_VENDOR" in
        GenuineIntel) CPU_VENDOR="Intel" ;;
        AuthenticAMD) CPU_VENDOR="AMD" ;;
        *) CPU_VENDOR="Unknown" ;;
    esac
}

# Detect Memory
detect_memory() {
    TOTAL_RAM_KB=$(grep MemTotal /proc/meminfo | awk '{print $2}')
    TOTAL_RAM_GB=$((TOTAL_RAM_KB / 1024 / 1024))
    
    # Round up if >= 0.5GB
    REMAINDER=$((TOTAL_RAM_KB % 1048576))
    if [ $REMAINDER -ge 524288 ]; then
        TOTAL_RAM_GB=$((TOTAL_RAM_GB + 1))
    fi
    
    # Detect ECC (requires dmidecode and root)
    HAS_ECC="No"
    if command -v dmidecode &> /dev/null && [ "$(id -u)" -eq 0 ]; then
        if dmidecode -t memory 2>/dev/null | grep -q "Error Correction Type:" && \
           ! dmidecode -t memory 2>/dev/null | grep -q "Error Correction Type: None"; then
            HAS_ECC="Yes"
        fi
    fi
}

# Detect Storage
detect_storage() {
    # Check for ZFS
    if command -v zpool &> /dev/null; then
        HAS_ZFS="Yes"
    else
        HAS_ZFS="No"
    fi
    
    # Check for Btrfs
    if command -v btrfs &> /dev/null; then
        HAS_BTRFS="Yes"
    else
        HAS_BTRFS="No"
    fi
    
    # Detect primary disk type
    DISK_TYPE="Unknown"
    if lsblk -d -o NAME,ROTA 2>/dev/null | grep -q "nvme"; then
        DISK_TYPE="NVMe"
    elif lsblk -d -o NAME,ROTA 2>/dev/null | grep -q "0$"; then
        DISK_TYPE="SSD"
    elif lsblk -d -o NAME,ROTA 2>/dev/null | grep -q "1$"; then
        DISK_TYPE="HDD"
    fi
}

# Detect Virtualization
detect_virtualization() {
    VIRT_TYPE="Bare Metal"
    if command -v systemd-detect-virt &> /dev/null; then
        VIRT=$(systemd-detect-virt)
        if [ "$VIRT" != "none" ]; then
            VIRT_TYPE="$VIRT"
        fi
    fi
}

# Calculate Recommendations
calculate_recommendations() {
    # ZFS ARC Recommendations
    if [ $TOTAL_RAM_GB -ge 128 ]; then
        ZFS_ARC_GB=32
    elif [ $TOTAL_RAM_GB -ge 64 ]; then
        ZFS_ARC_GB=16
    elif [ $TOTAL_RAM_GB -ge 32 ]; then
        ZFS_ARC_GB=8
    elif [ $TOTAL_RAM_GB -ge 16 ]; then
        if [ "$HAS_ECC" = "Yes" ]; then
            ZFS_ARC_GB=6
        else
            ZFS_ARC_GB=4
        fi
    elif [ $TOTAL_RAM_GB -ge 8 ]; then
        ZFS_ARC_GB=2
    elif [ $TOTAL_RAM_GB -ge 4 ]; then
        ZFS_ARC_GB=1
    else
        ZFS_ARC_GB=0  # Below minimum
    fi
    
    ZFS_ARC_BYTES=$((ZFS_ARC_GB * 1024 * 1024 * 1024))
    
    # Inotify Recommendations
    if [ $TOTAL_RAM_GB -ge 32 ]; then
        INOTIFY_WATCHES=1048576
    elif [ $TOTAL_RAM_GB -ge 16 ]; then
        INOTIFY_WATCHES=524288
    elif [ $TOTAL_RAM_GB -ge 8 ]; then
        INOTIFY_WATCHES=262144
    elif [ $TOTAL_RAM_GB -ge 4 ]; then
        INOTIFY_WATCHES=131072
    else
        INOTIFY_WATCHES=65536
    fi
    
    # Worker Thread Recommendations
    if [ $CPU_PHYSICAL -ge 16 ]; then
        WORKERS=8
    elif [ $CPU_PHYSICAL -ge 8 ]; then
        WORKERS=4
    elif [ $CPU_PHYSICAL -ge 4 ]; then
        WORKERS=2
    else
        WORKERS=1
    fi
    
    # Adjust for virtualization
    if [ "$VIRT_TYPE" != "Bare Metal" ]; then
        ZFS_ARC_BYTES=$((ZFS_ARC_BYTES * 3 / 4))
        ZFS_ARC_GB=$((ZFS_ARC_BYTES / 1024 / 1024 / 1024))
        INOTIFY_WATCHES=$((INOTIFY_WATCHES / 2))
    fi
}

# Display Results
display_results() {
    echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
    echo "DETECTED HARDWARE"
    echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
    echo "CPU:          $CPU_VENDOR $CPU_MODEL"
    echo "Cores:        $CPU_PHYSICAL physical, $CPU_CORES logical"
    echo "RAM:          ${TOTAL_RAM_GB}GB (ECC: $HAS_ECC)"
    echo "Storage:      $DISK_TYPE (ZFS: $HAS_ZFS, Btrfs: $HAS_BTRFS)"
    echo "Platform:     $VIRT_TYPE"
    echo ""
    echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
    echo "RECOMMENDED SETTINGS"
    echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
    echo "ZFS ARC:      ${ZFS_ARC_GB}GB"
    echo "Inotify:      $INOTIFY_WATCHES watches"
    echo "Workers:      $WORKERS threads"
    echo ""
    
    # Check if system meets minimum requirements
    if [ $TOTAL_RAM_GB -lt 4 ]; then
        echo "⚠️  WARNING: Less than 4GB RAM detected"
        echo "   D-PlaneOS will run but may be slow with ZFS"
        echo "   Consider ext4 or Btrfs for low-memory systems"
        echo ""
    fi
    
    if [ $CPU_PHYSICAL -lt 2 ]; then
        echo "⚠️  WARNING: Single-core CPU detected"
        echo "   System will work but may be slow during intensive operations"
        echo ""
    fi
    
    if [ "$HAS_ECC" = "No" ] && [ $TOTAL_RAM_GB -ge 32 ]; then
        echo "⚠️  NOTICE: Non-ECC RAM with ${TOTAL_RAM_GB}GB detected"
        echo "   For data integrity, ECC RAM is recommended for large systems"
        echo "   See: NON-ECC-WARNING.md"
        echo ""
    fi
}

# Export settings for installer
export_settings() {
    cat > /tmp/dplaneos-hardware-profile.env <<EOF
# D-PlaneOS Hardware Profile
# Generated: $(date)

# CPU
CPU_VENDOR="$CPU_VENDOR"
CPU_MODEL="$CPU_MODEL"
CPU_CORES=$CPU_CORES
CPU_PHYSICAL=$CPU_PHYSICAL

# Memory
TOTAL_RAM_GB=$TOTAL_RAM_GB
HAS_ECC="$HAS_ECC"

# Storage
HAS_ZFS="$HAS_ZFS"
HAS_BTRFS="$HAS_BTRFS"
DISK_TYPE="$DISK_TYPE"

# Platform
VIRT_TYPE="$VIRT_TYPE"

# Recommendations
ZFS_ARC_BYTES=$ZFS_ARC_BYTES
ZFS_ARC_GB=$ZFS_ARC_GB
INOTIFY_WATCHES=$INOTIFY_WATCHES
WORKERS=$WORKERS
EOF
    
    echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
    echo "Profile saved to: /tmp/dplaneos-hardware-profile.env"
    echo "This will be used by the installer for optimal configuration"
    echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
}

# Main
detect_cpu
detect_memory
detect_storage
detect_virtualization
calculate_recommendations
display_results
export_settings
