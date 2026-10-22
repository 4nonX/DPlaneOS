#!/usr/bin/env bash
#
# DPlaneOS Recovery CLI
#
# Emergency tool for the console or SSH when the web UI cannot be used.
# Password resets and 2FA go through the daemon binary (dplaned
# -recover-...), which sets passwords exactly as the web UI does (bcrypt,
# SCRAM keys, SMB password) for any local user.
#
# Usage: sudo dplaneos-recovery
#
# Environment (set by the NixOS package; defaults for other installs):
#   DPLANEOS_DSN          database DSN of the daemon
#   DPLANEOS_SECRETS_KEY  the daemon's secrets key (-secrets-key)
#   DPLANED               the daemon binary

set -u

DSN="${DPLANEOS_DSN:-postgres://dplaneos@/dplaneos?host=/run/postgresql&sslmode=disable}"
SECRETS_KEY="${DPLANEOS_SECRETS_KEY:-/var/lib/dplaneos/secrets.key}"
DPLANED="${DPLANED:-$(command -v dplaned || echo /opt/dplaneos/bin/dplaned)}"

if [ "$EUID" -ne 0 ]; then
    echo "This tool must be run as root: sudo dplaneos-recovery"
    exit 1
fi
if ! command -v dialog >/dev/null; then
    echo "dialog is not installed (Debian: apt install dialog)"
    exit 1
fi

DIALOG_OK=0

msg() { dialog --title "$1" --msgbox "$2" "${3:-20}" "${4:-76}"; }

recover() { # flags...; prints the daemon's answer
    "$DPLANED" -db-dsn "$DSN" -secrets-key "$SECRETS_KEY" "$@" 2>&1
}

show_main_menu() {
    while true; do
        choice=$(dialog --clear --title "DPlaneOS Recovery CLI" \
            --menu "Select an option:" 22 64 15 \
            1 "System Status" \
            2 "Check Services" \
            3 "Restart Services" \
            4 "Check Database" \
            5 "Reset a User's Password" \
            6 "Turn Off a User's Two-Factor Authentication" \
            7 "Check ZFS Pools" \
            8 "Import/Export Pool" \
            9 "Check Network" \
            10 "UPS Status" \
            11 "View Logs" \
            12 "Run Diagnostics" \
            13 "Emergency Shutdown" \
            14 "Exit" \
            3>&1 1>&2 2>&3)
        if [ $? -ne $DIALOG_OK ]; then
            clear
            exit 0
        fi
        case $choice in
            1) show_system_status ;;
            2) check_services ;;
            3) restart_services ;;
            4) check_database ;;
            5) reset_password ;;
            6) disable_2fa ;;
            7) check_zfs_pools ;;
            8) import_export_pool ;;
            9) check_network ;;
            10) check_ups_status ;;
            11) view_logs ;;
            12) run_diagnostics ;;
            13) emergency_shutdown ;;
            14) clear; exit 0 ;;
        esac
    done
}

show_system_status() {
    msg "System Status" "System:
  Hostname: $(hostname)
  OS:       $(sed -n "s/^PRETTY_NAME=//p" /etc/os-release 2>/dev/null | tr -d '"')
  Kernel:   $(uname -r)
  Uptime:   $(uptime -p 2>/dev/null || uptime)
  DPlaneOS: $("$DPLANED" -version 2>/dev/null || echo "daemon binary not found ($DPLANED)")

Resources:
  CPU cores: $(nproc)
  Memory:    $(free -h | awk '/^Mem:/{print $3 " used of " $2}')
  Root disk: $(df -h / | awk 'NR==2{print $5 " used"}')

Services:
  dplaned:    $(systemctl is-active dplaned 2>/dev/null)
  postgresql: $(systemctl is-active postgresql 2>/dev/null)
  nginx:      $(systemctl is-active nginx 2>/dev/null)" 24 76
}

check_services() {
    local out=""
    for svc in dplaned postgresql nginx samba-smbd smbd nfs-server; do
        systemctl cat "$svc.service" >/dev/null 2>&1 || continue
        out+="$svc: $(systemctl is-active "$svc" 2>/dev/null)\n"
        out+="  $(systemctl show -p ActiveEnterTimestamp --value "$svc" 2>/dev/null)\n"
    done
    out+="\nListening (web):\n$(ss -Htln 2>/dev/null | awk '$4 ~ /:(80|443)$/ {print "  " $4}')"
    msg "Service Status" "$(printf "%b" "$out")" 24 76
}

restart_services() {
    dialog --infobox "Restarting services..." 5 40
    local out=""
    for svc in postgresql dplaned nginx; do
        if systemctl restart "$svc" 2>/dev/null; then out+="$svc: restarted\n"; else out+="$svc: restart FAILED (journalctl -u $svc)\n"; fi
    done
    msg "Restart Services" "$(printf "%b" "$out")" 10 60
}

check_database() {
    local pg conn users
    pg=$(systemctl is-active postgresql 2>/dev/null)
    if users=$(recover -recover-list-users); then
        conn="OK"
    else
        conn="FAILED"
    fi
    msg "Database Status" "PostgreSQL service: $pg
Connection:         $conn

Local users (name, role, state):
$users" 24 76
}

pick_user() { # title -> prints the chosen user
    local list items=()
    list=$(recover -recover-list-users) || { msg "Error" "Cannot read the users:\n$list" 10 70; return 1; }
    while IFS=$'\t' read -r name role state; do
        [ -n "$name" ] && items+=("$name" "$role, $state")
    done <<<"$list"
    [ ${#items[@]} -gt 0 ] || { msg "Error" "No local users found." 6 40; return 1; }
    dialog --title "$1" --menu "User:" 20 64 12 "${items[@]}" 3>&1 1>&2 2>&3
}

reset_password() {
    local user pass confirm out
    user=$(pick_user "Reset a User's Password") || return
    pass=$(dialog --title "Password of $user" --insecure --passwordbox \
        "New password (at least 8 characters with upper and lower case, a digit and a special character):" \
        10 64 3>&1 1>&2 2>&3) || return
    confirm=$(dialog --title "Password of $user" --insecure --passwordbox "Confirm:" 8 64 3>&1 1>&2 2>&3) || return
    if [ "$pass" != "$confirm" ]; then
        msg "Error" "The passwords do not match." 6 40
        return
    fi
    if out=$(printf '%s\n' "$pass" | recover -recover-reset-password "$user"); then
        msg "Done" "$out\n\nYou can log in to the web UI now." 9 64
    else
        msg "Error" "$out" 10 70
    fi
}

disable_2fa() {
    local user out
    user=$(pick_user "Turn Off Two-Factor Authentication") || return
    dialog --title "Confirm" --yesno "Turn off two-factor authentication for $user?" 7 60 || return
    if out=$(recover -recover-disable-2fa "$user"); then
        msg "Done" "$out" 8 70
    else
        msg "Error" "$out" 10 70
    fi
}

check_zfs_pools() {
    msg "ZFS Pool Status" "$(zpool status 2>&1 || echo "zpool failed: is the ZFS module loaded?")" 30 90
}

import_export_pool() {
    local action pool result
    action=$(dialog --title "ZFS Import/Export" --menu "Select action:" 10 50 3 \
        1 "Import a pool" 2 "Export a pool" 3 "Back" 3>&1 1>&2 2>&3) || return
    case "$action" in
        1)
            msg "Importable Pools" "$(zpool import 2>&1)" 20 76
            pool=$(dialog --title "Import Pool" --inputbox "Pool name to import:" 8 50 3>&1 1>&2 2>&3) || return
            [ -z "$pool" ] && return
            if result=$(zpool import "$pool" 2>&1); then msg "Import" "Pool '$pool' imported." 7 50
            else msg "Import Failed" "$result" 12 76; fi
            ;;
        2)
            pool=$(dialog --title "Export Pool" --inputbox "Pool name to export:" 8 50 3>&1 1>&2 2>&3) || return
            [ -z "$pool" ] && return
            dialog --title "Confirm Export" --yesno "Export '$pool'? Its datasets are unmounted and its shares stop." 7 64 || return
            if result=$(zpool export "$pool" 2>&1); then msg "Export" "Pool '$pool' exported." 7 50
            else msg "Export Failed" "$result" 12 76; fi
            ;;
    esac
}

check_network() {
    msg "Network Status" "Interfaces:
$(ip -br addr 2>&1)

Routes:
$(ip route 2>&1)

DNS:
$(resolvectl dns 2>/dev/null || grep nameserver /etc/resolv.conf)" 30 90
}

check_ups_status() {
    local out ups
    if command -v upsc >/dev/null; then
        ups=$(upsc -l 2>/dev/null | head -1)
        if [ -n "$ups" ]; then out=$(upsc "$ups" 2>&1 | head -40); else out="No UPS found (upsc -l is empty)."; fi
    else
        out="NUT (upsc) is not installed."
    fi
    msg "UPS Status" "$out" 25 76
}

view_logs() {
    local source out
    source=$(dialog --title "View Logs" --menu "Select log source:" 14 60 5 \
        1 "dplaned - last 100 lines" \
        2 "dplaned - errors only" \
        3 "Updates - last check after a reboot" \
        4 "Kernel errors" \
        5 "All services - last 5 minutes" \
        3>&1 1>&2 2>&3) || return
    case "$source" in
        1) out=$(journalctl -n 100 --no-pager -u dplaned 2>&1) ;;
        2) out=$(journalctl --no-pager -p err -u dplaned 2>&1 | tail -100) ;;
        3) out=$(journalctl -n 50 --no-pager -u dplaneos-ota-health 2>&1) ;;
        4) out=$(journalctl --no-pager -k -p err 2>&1 | tail -80) ;;
        5) out=$(journalctl --no-pager --since "5 minutes ago" 2>&1 | tail -100) ;;
    esac
    msg "Logs" "$out" 35 110
}

run_diagnostics() {
    local svc_out="" svc
    for svc in dplaned postgresql nginx samba-smbd smbd nfs-server; do
        systemctl cat "$svc.service" >/dev/null 2>&1 || continue
        svc_out+="  $svc: $(systemctl is-active "$svc" 2>/dev/null)\n"
    done
    msg "Diagnostics" "Services:
$(printf "%b" "$svc_out")
ZFS pools:
$(zpool list -H -o name,health,size,alloc,free 2>/dev/null | sed 's/^/  /' || echo "  ZFS not available")

Disk space:
$(df -h / /persist /var 2>/dev/null | awk 'NR==1 || /^\// {print "  "$0}' | sort -u)

Memory:
$(free -h | head -2 | sed 's/^/  /')

Load:
  $(uptime)" 35 84
}

emergency_shutdown() {
    local action
    action=$(dialog --title "Emergency Shutdown" --menu "Select action:" 10 55 3 \
        1 "Power off system" 2 "Reboot system" 3 "Cancel" 3>&1 1>&2 2>&3) || return
    case "$action" in
        1) dialog --title "Confirm" --yesno "Power off the system NOW?" 6 45 && systemctl poweroff ;;
        2) dialog --title "Confirm" --yesno "Reboot the system NOW?" 6 45 && systemctl reboot ;;
    esac
}

show_main_menu
