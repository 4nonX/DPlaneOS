#!/bin/bash
#
# D-PlaneOS Recovery CLI
# 
# Emergency system recovery tool for when web UI is unavailable.
# Can be run from SSH or console (TTY).
#
# Usage: sudo dplaneos-recovery
#

set -e

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
BOLD='\033[1m'
NC='\033[0m'

# Check root
if [ "$EUID" -ne 0 ]; then
    echo "This tool must be run as root"
    echo "Usage: sudo dplaneos-recovery"
    exit 1
fi

# Check if dialog is available
if ! command -v dialog &>/dev/null; then
    echo "Installing dialog for better UI..."
    apt-get update -qq && apt-get install -y dialog -qq
fi

DIALOG_OK=0
DIALOG_CANCEL=1
DIALOG_ESC=255

show_main_menu() {
    while true; do
        choice=$(dialog --clear --title "D-PlaneOS Recovery CLI" \
            --menu "Select an option:" 22 60 15 \
            1 "System Status" \
            2 "Check Services" \
            3 "Restart Services" \
            4 "Check Database" \
            5 "Reset Admin Password" \
            6 "Check ZFS Pools" \
            7 "Import/Export Pool" \
            8 "Check Network" \
            9 "Fix Permissions" \
            10 "UPS Status" \
            11 "View Logs" \
            12 "Run Diagnostics" \
            13 "Emergency Shutdown" \
            14 "Exit" \
            3>&1 1>&2 2>&3)
        
        result=$?
        
        if [ $result -ne $DIALOG_OK ]; then
            clear
            exit 0
        fi
        
        case $choice in
            1) show_system_status ;;
            2) check_services ;;
            3) restart_services ;;
            4) check_database ;;
            5) reset_admin_password ;;
            6) check_zfs_pools ;;
            7) import_export_pool ;;
            8) check_network ;;
            9) fix_permissions ;;
            10) check_ups_status ;;
            11) view_logs ;;
            12) run_diagnostics ;;
            13) emergency_shutdown ;;
            14) clear; exit 0 ;;
        esac
    done
}

show_system_status() {
    STATUS_TEXT=$(cat <<EOF
System Information:
-------------------
Hostname: $(hostname)
OS: $(cat /etc/os-release | grep PRETTY_NAME | cut -d= -f2 | tr -d '"')
Kernel: $(uname -r)
Uptime: $(uptime -p)

Resources:
----------
CPU Cores: $(nproc)
RAM Total: $(free -h | awk '/^Mem:/{print $2}')
RAM Used: $(free -h | awk '/^Mem:/{print $3}')
Disk Used: $(df -h / | awk 'NR==2{print $5}')

D-PlaneOS:
----------
Installation: $([ -d /opt/dplaneos ] && echo "Found" || echo "Not found")
Database: $([ -f /var/lib/dplaneos/dplaneos.db ] && echo "Found" || echo "Not found")
nginx: $(systemctl is-active nginx 2>/dev/null || echo "not running")
Go Daemon: $(systemctl is-active dplaned 2>/dev/null || echo "not running")
EOF
)
    
    dialog --title "System Status" --msgbox "$STATUS_TEXT" 25 70
}

check_services() {
    SERVICES_TEXT=$(cat <<EOF
Service Status:
===============

nginx: $(systemctl is-active nginx 2>/dev/null || echo "STOPPED")
  $(systemctl status nginx 2>&1 | head -3 | tail -2)

Go Daemon: $(systemctl is-active dplaned 2>/dev/null || echo "STOPPED")
  $(systemctl status dplaned 2>&1 | head -3 | tail -2)

Ports:
------
Port 80: $(netstat -tuln | grep -q ":80 " && echo "OPEN" || echo "CLOSED")
Port 443: $(netstat -tuln | grep -q ":443 " && echo "OPEN" || echo "CLOSED")
EOF
)
    
    dialog --title "Service Status" --msgbox "$SERVICES_TEXT" 20 70
}

restart_services() {
    dialog --infobox "Restarting services..." 5 40
    
    systemctl restart nginx 2>&1
