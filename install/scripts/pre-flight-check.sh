#!/bin/bash
# D-PlaneOS Pre-Flight System Check
# Verifies system is ready for daemon startup

set -e

RED='\033[0;31m'
YELLOW='\033[1;33m'
GREEN='\033[0;32m'
NC='\033[0m' # No Color

ERRORS=0
WARNINGS=0

echo "╔════════════════════════════════════════════════════════════╗"
echo "║         D-PlaneOS v$(cat "$(dirname "$0")/../VERSION" 2>/dev/null | tr -d "[:space:]" || echo "?") Pre-Flight Check                 ║"
echo "╚════════════════════════════════════════════════════════════╝"
echo ""

# 1. Check Inotify Availability
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo "1. Inotify Watch Availability"
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"

INOTIFY_LIMIT=$(cat /proc/sys/fs/inotify/max_user_watches)
INOTIFY_USED=$(find /proc/*/fd -lname 'anon_inode:inotify' 2>/dev/null | wc -l)
INOTIFY_AVAILABLE=$((INOTIFY_LIMIT - INOTIFY_USED))
INOTIFY_PERCENT=$((INOTIFY_USED * 100 / INOTIFY_LIMIT))

echo "Limit:     $(numfmt --to=iec-i $INOTIFY_LIMIT) watches"
echo "Used:      $(numfmt --to=iec-i $INOTIFY_USED) watches ($INOTIFY_PERCENT%)"
echo "Available: $(numfmt --to=iec-i $INOTIFY_AVAILABLE) watches"

if [ $INOTIFY_PERCENT -gt 80 ]; then
    echo -e "${RED}✗ CRITICAL: Inotify usage >80%${NC}"
    echo "  Other processes (Docker, Plex, etc.) are consuming watches"
    echo "  D-PlaneOS may not get enough watches for 52TB indexing"
    ERRORS=$((ERRORS + 1))
    
    echo ""
    echo "Top Inotify Consumers:"
    for pid in $(find /proc/*/fd -lname 'anon_inode:inotify' 2>/dev/null | sed 's|/proc/\([0-9]*\)/.*|\1|' | sort -u); do
        if [ -f "/proc/$pid/cmdline" ]; then
            cmd=$(cat /proc/$pid/cmdline | tr '\0' ' ')
            count=$(find /proc/$pid/fd -lname 'anon_inode:inotify' 2>/dev/null | wc -l)
            if [ $count -gt 100 ]; then
                echo "  PID $pid ($count watches): $cmd"
            fi
        fi
    done
    
elif [ $INOTIFY_PERCENT -gt 50 ]; then
    echo -e "${YELLOW}⚠ WARNING: Inotify usage >50%${NC}"
    echo "  May impact real-time indexing under heavy load"
    WARNINGS=$((WARNINGS + 1))
else
    echo -e "${GREEN}✓ OK: Plenty of watches available${NC}"
fi

echo ""

# 2. Check ZFS ARC Configuration
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo "2. ZFS ARC Memory Configuration"
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"

TOTAL_RAM_KB=$(grep MemTotal /proc/meminfo | awk '{print $2}')
TOTAL_RAM_GB=$((TOTAL_RAM_KB / 1024 / 1024))

if [ -f /sys/module/zfs/parameters/zfs_arc_max ]; then
    ARC_MAX=$(cat /sys/module/zfs/parameters/zfs_arc_max)
    ARC_GB=$((ARC_MAX / 1024 / 1024 / 1024))
    ARC_PERCENT=$((ARC_GB * 100 / TOTAL_RAM_GB))
    
    echo "Total RAM:  ${TOTAL_RAM_GB}GB"
    echo "ZFS ARC:    ${ARC_GB}GB ($ARC_PERCENT%)"
    
    # Check for ECC
    HAS_ECC="No"
    if command -v dmidecode &> /dev/null && [ "$(id -u)" -eq 0 ]; then
        if dmidecode -t memory 2>/dev/null | grep -q "Error Correction Type:" && \
           ! dmidecode -t memory 2>/dev/null | grep -q "Error Correction Type: None"; then
            HAS_ECC="Yes"
        fi
    fi
    
    echo "ECC RAM:    $HAS_ECC"
    
    if [ "$HAS_ECC" = "No" ] && [ $ARC_PERCENT -gt 40 ]; then
        echo -e "${RED}✗ CRITICAL: ARC too high for Non-ECC RAM${NC}"
        echo "  With Non-ECC RAM, ARC should be ≤40% to reduce bit-flip risk"
        echo "  Current: ${ARC_PERCENT}% - Risk of memory corruption in cache"
        ERRORS=$((ERRORS + 1))
    elif [ $ARC_PERCENT -gt 50 ]; then
        echo -e "${YELLOW}⚠ WARNING: ARC >50% of RAM${NC}"
        echo "  May cause memory pressure under heavy load"
        WARNINGS=$((WARNINGS + 1))
    else
        echo -e "${GREEN}✓ OK: ARC properly sized${NC}"
    fi
else
    echo "ZFS not installed or loaded"
fi

echo ""

# 3. Check CPU I/O Wait
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo "3. CPU I/O Wait Baseline"
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"

# Take 3 samples over 3 seconds
IOWAIT_SUM=0
for i in 1 2 3; do
    IOWAIT=$(vmstat 1 2 | tail -1 | awk '{print $16}')
    IOWAIT_SUM=$((IOWAIT_SUM + IOWAIT))
    sleep 1
done
IOWAIT_AVG=$((IOWAIT_SUM / 3))

echo "Average I/O Wait: ${IOWAIT_AVG}%"

if [ $IOWAIT_AVG -gt 20 ]; then
    echo -e "${RED}✗ CRITICAL: High I/O wait (>20%)${NC}"
    echo "  System under heavy disk load"
    echo "  UI may experience lag during indexing"
    ERRORS=$((ERRORS + 1))
elif [ $IOWAIT_AVG -gt 10 ]; then
    echo -e "${YELLOW}⚠ WARNING: Elevated I/O wait (>10%)${NC}"
    echo "  Possible ZFS scrub or heavy disk activity"
    WARNINGS=$((WARNINGS + 1))
else
    echo -e "${GREEN}✓ OK: Low I/O wait${NC}"
fi

echo ""

# 4. Check Swap Configuration
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo "4. Memory & Swap Configuration"
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"

SWAPPINESS=$(cat /proc/sys/vm/swappiness)
SWAP_TOTAL=$(grep SwapTotal /proc/meminfo | awk '{print $2}')
SWAP_USED=$(grep SwapCached /proc/meminfo | awk '{print $2}')

echo "Swappiness: $SWAPPINESS"
echo "Swap Total: $((SWAP_TOTAL / 1024))MB"
echo "Swap Used:  $((SWAP_USED / 1024))MB"

if [ $SWAPPINESS -gt 10 ]; then
    echo -e "${YELLOW}⚠ WARNING: Swappiness >10${NC}"
    echo "  System may swap aggressively under memory pressure"
    echo "  Recommended: vm.swappiness=10"
    WARNINGS=$((WARNINGS + 1))
elif [ $SWAPPINESS -eq 10 ]; then
    echo -e "${GREEN}✓ OK: Swappiness optimized${NC}"
fi

echo ""

# 5. Check Docker Inotify Competition
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo "5. Docker Container Check"
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"

if command -v docker &> /dev/null; then
    DOCKER_CONTAINERS=$(docker ps --format '{{.Names}}' 2>/dev/null | wc -l)
    echo "Running containers: $DOCKER_CONTAINERS"
    
    if [ $DOCKER_CONTAINERS -gt 10 ]; then
        echo -e "${YELLOW}⚠ WARNING: Many Docker containers running${NC}"
        echo "  Each may consume inotify watches"
        echo "  Consider stopping unused containers before indexing"
        WARNINGS=$((WARNINGS + 1))
    elif [ $DOCKER_CONTAINERS -gt 0 ]; then
        echo -e "${GREEN}✓ OK: Reasonable number of containers${NC}"
    fi
else
    echo "Docker not installed"
fi

echo ""

# Summary
echo "╔════════════════════════════════════════════════════════════╗"
echo "║                     SUMMARY                                ║"
echo "╠════════════════════════════════════════════════════════════╣"

if [ $ERRORS -gt 0 ]; then
    echo -e "║  ${RED}✗ CRITICAL ISSUES:  $ERRORS${NC}                                    ║"
    echo "║                                                            ║"
    echo "║  System is NOT READY for production use                   ║"
    echo "║  Fix critical issues before starting daemon                ║"
    echo "╚════════════════════════════════════════════════════════════╝"
    exit 1
elif [ $WARNINGS -gt 0 ]; then
    echo -e "║  ${YELLOW}⚠ WARNINGS:         $WARNINGS${NC}                                    ║"
    echo "║                                                            ║"
    echo "║  System is functional but may have performance issues      ║"
    echo "║  Review warnings and optimize if possible                  ║"
    echo "╚════════════════════════════════════════════════════════════╝"
    exit 0
else
    echo -e "║  ${GREEN}✓ ALL CHECKS PASSED${NC}                                       ║"
    echo "║                                                            ║"
    echo "║  System is ready for D-PlaneOS daemon                      ║"
    echo "╚════════════════════════════════════════════════════════════╝"
    exit 0
fi
