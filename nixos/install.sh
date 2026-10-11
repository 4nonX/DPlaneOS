#!/usr/bin/env bash
# DPlaneOS Installer
# ─────────────────────────────────────────────────────────────────────────────
# Works fully offline: the ISO contains the complete DPlaneOS system.
#
# Flow:
#   1. Welcome screen
#   2. Select the disk for the operating system
#   3. Set the administrator password
#   4. Confirm and install
#   5. Complete: remove the installation medium, restart
#
# Unattended (provisioning, tests): no questions are asked when
#   DPLANEOS_INSTALL_UNATTENDED=1
#   DPLANEOS_INSTALL_DISK=/dev/...            the disk to erase and install on
#   DPLANEOS_INSTALL_PASSWORD_FILE=/path      file with the administrator password
# are set; the script exits when the installation is done (0) or failed (1)
# and does not restart the machine.
# ─────────────────────────────────────────────────────────────────────────────
set -euo pipefail

DPLANEOS_VERSION=$(cat /etc/dplaneos-install/VERSION 2>/dev/null || echo "unknown")
INSTALL_DIR="/etc/dplaneos-install"
LOG_FILE="/tmp/dplaneos-install.log"
UNATTENDED="${DPLANEOS_INSTALL_UNATTENDED:-0}"

# The system needs 1 GB for the boot partition, 20 GB for the system and
# room for its state (database, logs, containers).
MIN_DISK_BYTES=$((32 * 1000 * 1000 * 1000))

BORDER_COLOR="#6366f1"
TITLE_COLOR="#a5b4fc"
SUCCESS_COLOR="#4ade80"
ERROR_COLOR="#f87171"
DIM_COLOR="#6b7280"

INSTALL_DISK=""
ADMIN_PASS=""
PRIMARY_IP=""

log() { echo "[$(date '+%H:%M:%S')] $*" | tee -a "$LOG_FILE"; }

die() {
    if [ "$UNATTENDED" = 1 ]; then
        echo "INSTALLATION FAILED: $*" >&2
        echo "Log: $LOG_FILE" >&2
        tail -n 40 "$LOG_FILE" >&2 || true
        exit 1
    fi
    clear
    gum style \
        --border double \
        --border-foreground "$ERROR_COLOR" \
        --padding "1 2" \
        --margin "1" \
        "$(gum style --foreground "$ERROR_COLOR" --bold "✗ Installation Failed")" \
        "" \
        "$*" \
        "" \
        "$(gum style --foreground "$DIM_COLOR" "Full log: $LOG_FILE")" \
        "$(gum style --foreground "$DIM_COLOR" "Press Enter to return to the menu.")"
    read -r _
    show_welcome
}

show_welcome() {
    clear
    gum style \
        --border double \
        --border-foreground "$BORDER_COLOR" \
        --padding "1 4" \
        --margin "1" \
        --align center \
        "$(gum style --foreground "$TITLE_COLOR" --bold \
'██████╗ ██████╗ ██╗      █████╗ ███╗   ██╗███████╗ ██████╗ ███████╗
██╔══██╗██╔══██╗██║     ██╔══██╗████╗  ██║██╔════╝██╔═══██╗██╔════╝
██║  ██║██████╔╝██║     ███████║██╔██╗ ██║█████╗  ██║   ██║███████╗
██║  ██║██╔═══╝ ██║     ██╔══██║██║╚██╗██║██╔══╝  ██║   ██║╚════██║
██████╔╝██║     ███████╗██║  ██║██║ ╚████║███████╗╚██████╔╝███████║
╚═════╝ ╚═╝     ╚══════╝╚═╝  ╚═╝╚═╝  ╚═══╝╚══════╝ ╚═════╝ ╚══════╝')" \
        "" \
        "$(gum style --foreground "$DIM_COLOR" \
            "Infrastructure Control Plane  •  v${DPLANEOS_VERSION}")" \
        "$(gum style --foreground "$DIM_COLOR" \
            "AGPLv3  •  github.com/4nonX/DPlaneOS")"

    echo ""
    ACTION=$(gum choose \
        --header "Select an action:" \
        --header.foreground "$TITLE_COLOR" \
        --selected.foreground "$TITLE_COLOR" \
        --cursor.foreground "$BORDER_COLOR" \
        "Install DPlaneOS" \
        "Shell" \
        "Reboot")

    case "$ACTION" in
        "Install DPlaneOS")    do_install ;;
        "Shell")                exec bash ;;
        "Reboot")               reboot ;;
    esac
}

# candidate_disks prints one line per disk the system can be installed on:
#   /dev/name<TAB>bytes<TAB>model
# Whole disks of at least MIN_DISK_BYTES that are not in use (the medium
# this installer runs from has a mounted file system and is left out).
candidate_disks() {
    local name size type model
    # MODEL last: it may be empty or contain spaces.
    lsblk -d -n -b -o NAME,SIZE,TYPE,MODEL 2>/dev/null | while read -r name size type model; do
        [ "$type" = "disk" ] || continue
        [ "${size:-0}" -ge "$MIN_DISK_BYTES" ] || continue
        if lsblk -n -o MOUNTPOINTS "/dev/$name" 2>/dev/null | grep -q '[^[:space:]]'; then
            continue
        fi
        printf '/dev/%s\t%s\t%s\n' "$name" "$size" "${model:-Unknown}"
    done | sort
}

human_size() {
    awk -v s="$1" 'BEGIN {
        if (s >= 1000000000000) printf "%.1f TB", s / 1000000000000;
        else printf "%.0f GB", s / 1000000000;
    }'
}

select_disk() {
    clear
    gum style --foreground "$TITLE_COLOR" --bold --margin "1 0 0 1" \
        "Step 1 of 3 - Select the system disk"
    gum style --foreground "$DIM_COLOR" --margin "0 0 1 1" \
        "This disk will hold the DPlaneOS operating system and its state." \
        "Your data disks are NOT touched here - ZFS pools are created after install."

    local disk_list=() dev size model
    while IFS=$'\t' read -r dev size model; do
        disk_list+=("$(printf '%-14s %-9s %s' "$dev" "$(human_size "$size")" "$model")")
    done < <(candidate_disks)

    if [ ${#disk_list[@]} -eq 0 ]; then
        die "No suitable disk found (at least 32 GB, not in use).\nCheck hardware connections and retry."
    fi

    local selected
    selected=$(gum choose \
        --header "Available disks - select the system disk:" \
        --header.foreground "$TITLE_COLOR" \
        --cursor.foreground "$BORDER_COLOR" \
        "${disk_list[@]}")

    INSTALL_DISK=$(echo "$selected" | awk '{print $1}')

    local part_count
    part_count=$(lsblk "$INSTALL_DISK" --noheadings -o TYPE 2>/dev/null \
        | grep -c "part" || true)

    if [ "$part_count" -gt 0 ]; then
        echo ""
        gum style --foreground "$ERROR_COLOR" --bold --margin "0 0 0 1" \
            "⚠  WARNING: $INSTALL_DISK has $part_count existing partition(s)."
        gum style --foreground "$DIM_COLOR" --margin "0 0 1 1" \
            "ALL data on $INSTALL_DISK will be permanently destroyed."
        gum confirm \
            --prompt.foreground "$ERROR_COLOR" \
            "Continue and erase $INSTALL_DISK?" \
            || show_welcome
    fi

    log "Selected disk: $INSTALL_DISK ($selected)"
}

# password_problem prints why a password is not acceptable (the rules of the
# web interface), or nothing.
password_problem() {
    python3 - "$1" <<'PY'
import sys, unicodedata
p = sys.argv[1]
if len(p) < 8:
    print("Password must be at least 8 characters.")
    sys.exit()
missing = []
if not any(c.isupper() for c in p): missing.append("an uppercase letter")
if not any(c.islower() for c in p): missing.append("a lowercase letter")
if not any(c.isdigit() for c in p): missing.append("a digit")
if not any(unicodedata.category(c)[0] in "PS" for c in p): missing.append("a special character")
if missing:
    print("Password must contain " + ", ".join(missing) + ".")
PY
}

set_admin_password() {
    clear
    gum style --foreground "$TITLE_COLOR" --bold --margin "1 0 0 1" \
        "Step 2 of 3 - Administrator password"
    gum style --foreground "$DIM_COLOR" --margin "0 0 1 1" \
        "This password is used to log into the DPlaneOS web interface (user: admin)." \
        "Minimum 8 characters. Must contain upper, lower, digit, and symbol."

    while true; do
        local pass1 pass2 problem
        pass1=$(gum input \
            --password \
            --placeholder "Enter admin password" \
            --prompt "  › " \
            --prompt.foreground "$BORDER_COLOR" \
            --header "Password:" \
            --header.foreground "$TITLE_COLOR")

        pass2=$(gum input \
            --password \
            --placeholder "Re-enter admin password" \
            --prompt "  › " \
            --prompt.foreground "$BORDER_COLOR" \
            --header "Confirm password:" \
            --header.foreground "$TITLE_COLOR")

        if [ "$pass1" != "$pass2" ]; then
            gum style --foreground "$ERROR_COLOR" --margin "0 0 0 1" \
                "✗  Passwords do not match. Try again."
            continue
        fi

        problem=$(password_problem "$pass1")
        if [ -n "$problem" ]; then
            gum style --foreground "$ERROR_COLOR" --margin "0 0 0 1" "✗  $problem"
            continue
        fi

        ADMIN_PASS="$pass1"
        break
    done

    log "Admin password configured."
}

# The installed system starts through systemd-boot, which needs UEFI.
# (DPLANEOS_INSTALL_ASSUME_UEFI: for the installation test, whose installer
# machine is started without firmware; the installed disk is then booted
# with UEFI.)
require_uefi() {
    if [ ! -d /sys/firmware/efi ] && [ "${DPLANEOS_INSTALL_ASSUME_UEFI:-0}" != 1 ]; then
        die "This machine was started in legacy BIOS mode.\n\nDPlaneOS needs UEFI: enable UEFI boot (and disable CSM / legacy boot)\nin the firmware setup, then start the installer again."
    fi
}

confirm_install() {
    clear
    gum style \
        --border rounded \
        --border-foreground "$BORDER_COLOR" \
        --padding "1 3" \
        --margin "1" \
        "$(gum style --foreground "$TITLE_COLOR" --bold "Step 3 of 3 - Installation Summary")" \
        "" \
        "  System disk: $(gum style --foreground "$TITLE_COLOR" "$INSTALL_DISK")" \
        "  Admin user:  $(gum style --foreground "$TITLE_COLOR" "admin")" \
        "  Install:     $(gum style --foreground "$SUCCESS_COLOR" "Offline (no internet required)")" \
        "" \
        "$(gum style --foreground "$ERROR_COLOR" --bold \
            "  ⚠  ALL EXISTING DATA ON $INSTALL_DISK WILL BE DESTROYED")"

    echo ""
    gum confirm \
        --prompt.foreground "$ERROR_COLOR" \
        --default=false \
        "Begin installation?" \
        || show_welcome
}

progress_step() {
    local title="$1"
    local cmd="$2"
    log "Starting: $title"
    if [ "$UNATTENDED" = 1 ]; then
        bash -c "$cmd >> $LOG_FILE 2>&1" || die "Step failed: $title"
    else
        gum spin \
            --spinner dot \
            --spinner.foreground "$BORDER_COLOR" \
            --title "  $title" \
            -- bash -c "$cmd >> $LOG_FILE 2>&1" \
            || die "Step failed: $title\n\nSee log for details: $LOG_FILE"
    fi
    log "Complete: $title"
}

run_install() {
    if [ "$UNATTENDED" != 1 ]; then
        clear
        gum style --foreground "$TITLE_COLOR" --bold --margin "1 0 1 1" \
            "Installing DPlaneOS v${DPLANEOS_VERSION}..."
    fi

    local SYSTEM_PATH
    SYSTEM_PATH=$(cat "$INSTALL_DIR/system-path" 2>/dev/null || echo "")
    if [ -z "$SYSTEM_PATH" ] || [ ! -d "$SYSTEM_PATH" ]; then
        die "This installation medium is incomplete (the system is missing from it).\nDownload the ISO again and check its checksum."
    fi

    # The partitioning script was prepared with the installer; only the
    # placeholder device in it is replaced by the chosen disk.
    local partition_script="/tmp/dplaneos-partition.sh"
    [ -r "$INSTALL_DIR/disko-script" ] \
        || die "This installation medium is incomplete (the partitioning script is missing)."
    grep -q '/dev/nvme0n1' "$INSTALL_DIR/disko-script" \
        || die "The partitioning script has no device to replace."
    sed "s|/dev/nvme0n1|${INSTALL_DISK}|g" "$INSTALL_DIR/disko-script" > "$partition_script"
    chmod 700 "$partition_script"

    progress_step \
        "Partitioning $INSTALL_DISK (boot, system, persistent state)..." \
        "'$partition_script'"

    mountpoint -q /mnt/persist || die "The persistent state partition is not mounted."

    progress_step \
        "Installing system (offline - reading from the installation medium)..." \
        "nixos-install \
            --root /mnt \
            --system '$SYSTEM_PATH' \
            --no-root-passwd \
            --no-channel-copy"

    # The administrator's password, as a hash, where the system keeps its
    # state: /var/lib/dplaneos lives on the persistent partition. The daemon
    # creates the admin account with it at first start and deletes the file.
    _PASS_TMP=$(mktemp)
    chmod 600 "$_PASS_TMP"
    printf '%s' "$ADMIN_PASS" > "$_PASS_TMP"

    progress_step \
        "Setting admin credentials..." \
        "
        HASH=\$(python3 -c \"
import bcrypt, sys
with open(sys.argv[1], 'rb') as f:
    pw = f.read()
print(bcrypt.hashpw(pw, bcrypt.gensalt(rounds=12)).decode())
\" $_PASS_TMP)
        test -n \"\$HASH\"
        install -d -m 755 /mnt/persist/var/lib/dplaneos
        ( umask 077; printf '%s' \"\$HASH\" > /mnt/persist/var/lib/dplaneos/.first-boot-password )
        "

    rm -f "$_PASS_TMP"

    progress_step \
        "Finishing..." \
        "sync; umount -R /mnt"

    PRIMARY_IP=$(ip route get 1.1.1.1 2>/dev/null \
        | awk '{for(i=1;i<=NF;i++) if ($i=="src") {print $(i+1); exit}}' \
        || echo "")
    [ -z "$PRIMARY_IP" ] && PRIMARY_IP="<address of this machine>"

    log "Installation complete."
}

show_complete() {
    clear
    gum style \
        --border double \
        --border-foreground "$SUCCESS_COLOR" \
        --padding "2 6" \
        --margin "1" \
        --align center \
        "$(gum style --foreground "$SUCCESS_COLOR" --bold \
            "✓  DPlaneOS Installed Successfully")" \
        "" \
        "After the restart, open a browser on any device in your network and go to:" \
        "" \
        "$(gum style --foreground "$TITLE_COLOR" --bold \
            "    http://${PRIMARY_IP}")" \
        "" \
        "$(gum style --foreground "$DIM_COLOR" "(the address may differ if your network assigns another one;")" \
        "$(gum style --foreground "$DIM_COLOR" " the console of the installed system shows it)")" \
        "" \
        "$(gum style --foreground "$DIM_COLOR" "Username:  admin")" \
        "$(gum style --foreground "$DIM_COLOR" "Password:  (as configured)")" \
        "" \
        "$(gum style --foreground "$DIM_COLOR" \
            "Remove the installation medium, then press Enter to restart.")"

    echo ""
    read -r _
    reboot
}

do_install() {
    require_uefi
    select_disk
    set_admin_password
    confirm_install
    run_install
    show_complete
}

if [ "$UNATTENDED" = 1 ]; then
    INSTALL_DISK="${DPLANEOS_INSTALL_DISK:?DPLANEOS_INSTALL_DISK is not set}"
    [ -b "$INSTALL_DISK" ] || die "$INSTALL_DISK is not a disk"
    candidate_disks | cut -f1 | grep -qxF "$INSTALL_DISK" \
        || die "$INSTALL_DISK cannot be used: it is in use, or smaller than 32 GB"
    ADMIN_PASS=$(cat "${DPLANEOS_INSTALL_PASSWORD_FILE:?DPLANEOS_INSTALL_PASSWORD_FILE is not set}")
    problem=$(password_problem "$ADMIN_PASS")
    [ -z "$problem" ] || die "$problem"
    require_uefi
    run_install
    echo "DPlaneOS v${DPLANEOS_VERSION} is installed on $INSTALL_DISK."
    exit 0
fi

trap '
    echo ""
    if gum confirm "Abort and return to menu?" 2>/dev/null; then
        show_welcome
    fi
' INT

show_welcome
