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
#   DPLANEOS_INSTALL_ERASE_DISK=1             needed when the disk is not empty
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
    back_to_menu
}

# back_to_menu starts the installer afresh. It never returns: a step that
# was left ("No" at a confirmation, an error, Ctrl-C) must not go on where
# it stopped. (It used to call the menu as a function; leaving the menu with
# Esc then returned into the installation that had just been declined.)
back_to_menu() {
    exec bash "$0"
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
        "Reset the administrator password of an installed system" \
        "Allow remote access to this installer (SSH)" \
        "Shell" \
        "Reboot")

    case "$ACTION" in
        "Install DPlaneOS")    do_install ;;
        "Reset the administrator password of an installed system") do_reset_password ;;
        "Allow remote access to this installer (SSH)") do_remote_access ;;
        "Shell")                exec bash ;;
        "Reboot")               reboot ;;
        *)                      back_to_menu ;;   # left with Esc: show it again
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

# disk_contents says what a disk holds, in a few words: "empty", the ZFS
# pool it belongs to, or the file systems on it.
disk_contents() {
    local dev="$1" pools others
    pools=$(lsblk -rn -o FSTYPE,LABEL "$dev" 2>/dev/null | awk '$1 == "zfs_member" { print $2 }' | sort -u | paste -sd, -)
    if [ -n "$pools" ]; then
        echo "ZFS pool $pools"
        return
    fi
    others=$(lsblk -rn -o FSTYPE "$dev" 2>/dev/null | grep -v '^$' | sort -u | paste -sd, -)
    if [ -n "$others" ]; then
        echo "in use: $others"
    elif lsblk -rn -o TYPE "$dev" 2>/dev/null | grep -q part; then
        echo "partitioned"
    else
        echo "empty"
    fi
}

select_disk() {
    clear
    gum style --foreground "$TITLE_COLOR" --bold --margin "1 0 0 1" \
        "Step 1 of 3 - Select the system disk"
    gum style --foreground "$DIM_COLOR" --margin "0 0 1 1" \
        "This disk will hold the DPlaneOS operating system and its state, and is" \
        "erased completely. The list says what each disk holds now: do not choose" \
        "a disk of a pool you want to keep. The other disks are not touched."

    local disk_list=() dev size model
    while IFS=$'\t' read -r dev size model; do
        disk_list+=("$(printf '%-14s %-9s %-26s %s' "$dev" "$(human_size "$size")" "$model" "[$(disk_contents "$dev")]")")
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

    local contents
    contents=$(disk_contents "$INSTALL_DISK")
    case "$contents" in
        empty) ;;
        "ZFS pool "*)
            # A member of a pool: the name of the pool has to be typed.
            local pool typed
            pool="${contents#ZFS pool }"
            echo ""
            gum style --foreground "$ERROR_COLOR" --bold --margin "0 0 0 1" \
                "⚠  $INSTALL_DISK is part of the $contents."
            gum style --foreground "$DIM_COLOR" --margin "0 0 1 1" \
                "Installing here erases this disk and damages or destroys that pool." \
                "If this is the disk you mean, type the name of the pool to go on."
            typed=$(gum input --placeholder "name of the pool" --prompt "  › " --prompt.foreground "$ERROR_COLOR") || back_to_menu
            [ "$typed" = "$pool" ] || back_to_menu
            ;;
        *)
            echo ""
            gum style --foreground "$ERROR_COLOR" --bold --margin "0 0 0 1" \
                "⚠  WARNING: $INSTALL_DISK is not empty ($contents)."
            gum style --foreground "$DIM_COLOR" --margin "0 0 1 1" \
                "ALL data on $INSTALL_DISK will be permanently destroyed."
            gum confirm \
                --default=false \
                --prompt.foreground "$ERROR_COLOR" \
                "Continue and erase $INSTALL_DISK?" \
                || back_to_menu
            ;;
    esac

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
        "Administrator password"
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
        || back_to_menu
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

# ── Remote access to the installer ───────────────────────────────────────────
# For machines without a usable console: SSH is started with a password made
# up now and shown here. It is gone when the installer restarts.
do_remote_access() {
    clear
    local pw addrs
    pw=$(python3 -c "import secrets, string; a = string.ascii_lowercase + string.digits; print('-'.join(''.join(secrets.choice(a) for _ in range(5)) for _ in range(3)))")
    if ! printf 'root:%s\n' "$pw" | chpasswd 2>>"$LOG_FILE" || ! systemctl start sshd 2>>"$LOG_FILE"; then
        die "Remote access could not be switched on."
    fi
    addrs=$(ip -4 -o addr show scope global 2>/dev/null | awk '{print $4}' | cut -d/ -f1 | tr '\n' ' ')
    gum style --border rounded --border-foreground "$BORDER_COLOR" --padding "1 4" --margin "1" \
        "$(gum style --foreground "$TITLE_COLOR" --bold "Remote access is on until this installer restarts.")" \
        "" \
        "  ssh root@${addrs:-<address of this machine>}" \
        "  Password: $(gum style --foreground "$SUCCESS_COLOR" --bold "$pw")" \
        "" \
        "$(gum style --foreground "$DIM_COLOR" "The installer starts when you sign in. Anybody who has this password")" \
        "$(gum style --foreground "$DIM_COLOR" "can install on this machine: use it on a network you trust.")"
    echo ""
    echo "Press Enter to return to the menu."
    read -r _
    back_to_menu
}

# ── Lost administrator password ──────────────────────────────────────────────
# An installed system keeps its state on the partition labelled "persist".
# A new password is left there as a hash; the system takes it at its next
# start (and removes the file): the administrator can sign in again, without
# the second factor, and every session of that account is ended. Nothing
# else on the system changes.

# installed_systems prints the persist partitions of installed systems.
installed_systems() {
    lsblk -rn -o PATH,PARTLABEL 2>/dev/null | awk '$2 == "persist" { print $1 }'
}

# write_password_reset PARTITION: leaves the hash of ADMIN_PASS there.
write_password_reset() {
    local part="$1" mnt hash pass_tmp
    mnt=$(mktemp -d)
    mount "$part" "$mnt" || die "$part cannot be mounted."
    if [ ! -d "$mnt/var/lib/dplaneos" ]; then
        umount "$mnt"
        die "$part does not hold the state of an installed DPlaneOS system."
    fi
    pass_tmp=$(mktemp)
    chmod 600 "$pass_tmp"
    printf '%s' "$ADMIN_PASS" > "$pass_tmp"
    hash=$(python3 -c "
import bcrypt, sys
with open(sys.argv[1], 'rb') as f:
    pw = f.read()
print(bcrypt.hashpw(pw, bcrypt.gensalt(rounds=12)).decode())
" "$pass_tmp") || true
    rm -f "$pass_tmp"
    if [ -z "$hash" ]; then
        umount "$mnt"
        die "The password could not be prepared."
    fi
    ( umask 077; printf '%s\n' "$hash" > "$mnt/var/lib/dplaneos/.reset-admin-password" )
    sync
    umount "$mnt"
    rmdir "$mnt" 2>/dev/null || true
    log "Password reset left on $part."
}

do_reset_password() {
    clear
    gum style --foreground "$TITLE_COLOR" --bold --margin "1 0 0 1" \
        "Reset the administrator password"
    gum style --foreground "$DIM_COLOR" --margin "0 0 1 1" \
        "For an installed system whose administrator cannot sign in any more." \
        "The system takes the new password at its next start. Two-factor sign-in" \
        "is switched off for the administrator and its sessions are ended." \
        "Other users, settings and data are not touched."

    local parts part
    parts=$(installed_systems)
    if [ -z "$parts" ]; then
        gum style --foreground "$ERROR_COLOR" --margin "0 0 1 1" \
            "No installed DPlaneOS system was found on the disks of this machine."
        echo "Press Enter to return to the menu."
        read -r _
        back_to_menu
        return
    fi
    if [ "$(printf '%s\n' "$parts" | wc -l)" -gt 1 ]; then
        # shellcheck disable=SC2086
        part=$(gum choose --header "Which system?" --header.foreground "$TITLE_COLOR" $parts)
    else
        part="$parts"
    fi
    [ -n "$part" ] || back_to_menu

    set_admin_password
    write_password_reset "$part"

    clear
    gum style --border rounded --border-foreground "$SUCCESS_COLOR" --padding "1 4" --margin "1" \
        "$(gum style --foreground "$SUCCESS_COLOR" --bold "The new password is in place.")" \
        "" \
        "$(gum style --foreground "$DIM_COLOR" "Remove the installation medium, then press Enter to restart.")" \
        "$(gum style --foreground "$DIM_COLOR" "Sign in to the web interface as admin with the new password.")"
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

# Without questions (tests): DPLANEOS_RESET_PASSWORD=1 leaves the password
# from DPLANEOS_INSTALL_PASSWORD_FILE on the one installed system there is.
if [ "${DPLANEOS_RESET_PASSWORD:-0}" = 1 ]; then
    UNATTENDED=1
    ADMIN_PASS=$(cat "${DPLANEOS_INSTALL_PASSWORD_FILE:?DPLANEOS_INSTALL_PASSWORD_FILE is not set}")
    problem=$(password_problem "$ADMIN_PASS")
    [ -z "$problem" ] || die "$problem"
    parts=$(installed_systems)
    [ -n "$parts" ] || die "No installed DPlaneOS system was found."
    [ "$(printf '%s\n' "$parts" | wc -l)" = 1 ] || die "More than one installed system: $parts"
    write_password_reset "$parts"
    echo "The administrator password of the system on $parts was reset."
    exit 0
fi

if [ "$UNATTENDED" = 1 ]; then
    INSTALL_DISK="${DPLANEOS_INSTALL_DISK:?DPLANEOS_INSTALL_DISK is not set}"
    [ -b "$INSTALL_DISK" ] || die "$INSTALL_DISK is not a disk"
    candidate_disks | cut -f1 | grep -qxF "$INSTALL_DISK" \
        || die "$INSTALL_DISK cannot be used: it is in use, or smaller than 32 GB"
    ADMIN_PASS=$(cat "${DPLANEOS_INSTALL_PASSWORD_FILE:?DPLANEOS_INSTALL_PASSWORD_FILE is not set}")
    problem=$(password_problem "$ADMIN_PASS")
    [ -z "$problem" ] || die "$problem"
    # A disk that holds something is only erased when that is said too.
    contents=$(disk_contents "$INSTALL_DISK")
    if [ "$contents" != "empty" ] && [ "${DPLANEOS_INSTALL_ERASE_DISK:-0}" != 1 ]; then
        die "$INSTALL_DISK is not empty ($contents). Set DPLANEOS_INSTALL_ERASE_DISK=1 to erase it."
    fi
    require_uefi
    run_install
    echo "DPlaneOS v${DPLANEOS_VERSION} is installed on $INSTALL_DISK."
    exit 0
fi

trap '
    echo ""
    if gum confirm "Abort and return to menu?" 2>/dev/null; then
        back_to_menu
    fi
' INT

show_welcome
