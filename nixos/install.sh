#!/usr/bin/env bash
# D-PlaneOS Installer
# ─────────────────────────────────────────────────────────────────────────────
# TUI installer — mirrors the TrueNAS SCALE installation flow.
# Works fully offline: the ISO contains the complete D-PlaneOS closure.
#
# Flow:
#   1. Welcome screen
#   2. Select boot/OS disk
#   3. Set admin password
#   4. Select boot mode (UEFI / Legacy BIOS)
#   5. Confirm and install
#   6. Complete — show IP and access URL
# ─────────────────────────────────────────────────────────────────────────────
set -euo pipefail

DPLANEOS_VERSION=$(cat /etc/dplaneos-install/VERSION 2>/dev/null || echo "7.1.0")
INSTALL_DIR="/etc/dplaneos-install"
LOG_FILE="/tmp/dplaneos-install.log"

BORDER_COLOR="#6366f1"
TITLE_COLOR="#a5b4fc"
SUCCESS_COLOR="#4ade80"
ERROR_COLOR="#f87171"
DIM_COLOR="#6b7280"

INSTALL_DISK=""
ADMIN_PASS=""
BOOT_MODE=""
PRIMARY_IP=""

log() { echo "[$(date '+%H:%M:%S')] $*" | tee -a "$LOG_FILE"; }

die() {
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
            "AGPLv3  •  github.com/4nonX/D-PlaneOS")"

    echo ""
    ACTION=$(gum choose \
        --header "Select an action:" \
        --header.foreground "$TITLE_COLOR" \
        --selected.foreground "$TITLE_COLOR" \
        --cursor.foreground "$BORDER_COLOR" \
        "Install D-PlaneOS" \
        "Shell" \
        "Reboot")

    case "$ACTION" in
        "Install D-PlaneOS") do_install ;;
        "Shell")             exec bash ;;
        "Reboot")            reboot ;;
    esac
}

select_disk() {
    clear
    gum style --foreground "$TITLE_COLOR" --bold --margin "1 0 0 1" \
        "Step 1 of 4 — Select boot disk"
    gum style --foreground "$DIM_COLOR" --margin "0 0 1 1" \
        "This disk will hold the D-PlaneOS operating system (A/B slots + persistent state)." \
        "Your data disks are NOT touched here — ZFS pools are created after install."

    local disk_list=()
    while IFS= read -r line; do
        disk_list+=("$line")
    done < <(lsblk -d -o NAME,SIZE,MODEL,TYPE --noheadings --bytes 2>/dev/null \
        | awk '$4=="disk" {
            size=$2+0
            if (size > 10737418240) {
                if (size >= 1099511627776)      hr=sprintf("%.0f TB", size/1099511627776)
                else if (size >= 1073741824)    hr=sprintf("%.0f GB", size/1073741824)
                else                            hr=sprintf("%.0f MB", size/1048576)
                model=$3
                for(i=4;i<=NF-1;i++) model=model" "$i
                gsub(/[[:space:]]+$/, "", model)
                if (model == "") model = "Unknown"
                printf "/dev/%-8s  %-8s  %s\n", $1, hr, model
            }
        }' | sort)

    if [ ${#disk_list[@]} -eq 0 ]; then
        die "No suitable disks found (minimum 10 GB required).\nCheck hardware connections and retry."
    fi

    local selected
    selected=$(gum choose \
        --header "Available disks — select the OS boot disk:" \
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

set_admin_password() {
    clear
    gum style --foreground "$TITLE_COLOR" --bold --margin "1 0 0 1" \
        "Step 2 of 4 — Administrator password"
    gum style --foreground "$DIM_COLOR" --margin "0 0 1 1" \
        "This password is used to log into the D-PlaneOS web interface." \
        "Minimum 8 characters. Must contain upper, lower, digit, and symbol."

    while true; do
        local pass1 pass2
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

        if [ ${#pass1} -lt 8 ]; then
            gum style --foreground "$ERROR_COLOR" --margin "0 0 0 1" \
                "✗  Password must be at least 8 characters."
            continue
        fi

        if ! echo "$pass1" | grep -qP '(?=.*[A-Z])(?=.*[a-z])(?=.*[0-9])(?=.*[^A-Za-z0-9])'; then
            gum style --foreground "$ERROR_COLOR" --margin "0 0 0 1" \
                "✗  Password must contain uppercase, lowercase, digit, and symbol."
            continue
        fi

        ADMIN_PASS="$pass1"
        break
    done

    log "Admin password configured."
}

select_boot_mode() {
    clear
    gum style --foreground "$TITLE_COLOR" --bold --margin "1 0 0 1" \
        "Step 3 of 4 — Boot mode"

    if [ -d /sys/firmware/efi ]; then
        gum style --foreground "$SUCCESS_COLOR" --margin "0 0 1 1" \
            "✓  UEFI firmware detected."
        BOOT_MODE="UEFI (recommended)"
    else
        gum style --foreground "$DIM_COLOR" --margin "0 0 1 1" \
            "UEFI firmware not detected on this system."
        BOOT_MODE=$(gum choose \
            --header "Select boot mode:" \
            --header.foreground "$TITLE_COLOR" \
            --cursor.foreground "$BORDER_COLOR" \
            "UEFI (recommended)" \
            "Legacy BIOS")
    fi

    log "Boot mode: $BOOT_MODE"
}

confirm_install() {
    clear
    gum style \
        --border rounded \
        --border-foreground "$BORDER_COLOR" \
        --padding "1 3" \
        --margin "1" \
        "$(gum style --foreground "$TITLE_COLOR" --bold "Installation Summary")" \
        "" \
        "  OS disk:    $(gum style --foreground "$TITLE_COLOR" "$INSTALL_DISK")" \
        "  Boot mode:  $(gum style --foreground "$TITLE_COLOR" "$BOOT_MODE")" \
        "  Admin user: $(gum style --foreground "$TITLE_COLOR" "admin")" \
        "  Install:    $(gum style --foreground "$SUCCESS_COLOR" "Offline (no internet required)")" \
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
    gum spin \
        --spinner dot \
        --spinner.foreground "$BORDER_COLOR" \
        --title "  $title" \
        -- bash -c "$cmd >> $LOG_FILE 2>&1" \
        || die "Step failed: $title\n\nSee log for details: $LOG_FILE"
    log "Complete: $title"
}

run_install() {
    clear
    gum style --foreground "$TITLE_COLOR" --bold --margin "1 0 1 1" \
        "Installing D-PlaneOS v${DPLANEOS_VERSION}..."

    local patched_disko="/tmp/disko-install.nix"
    sed "s|device = \"/dev/nvme0n1\"|device = \"${INSTALL_DISK}\"|g" \
        "$INSTALL_DIR/disko.nix" > "$patched_disko"

    progress_step \
        "Partitioning $INSTALL_DISK (ESP + A/B slots + persist)..." \
        "disko --mode disko $patched_disko"

    progress_step \
        "Mounting filesystems..." \
        "disko --mode mount $patched_disko"

    progress_step \
        "Preparing system configuration..." \
        "
        mkdir -p /mnt/etc/nixos
        cp '$patched_disko' /mnt/etc/nixos/disko.nix
        nixos-generate-config --root /mnt --no-filesystems
        "

    local SYSTEM_PATH
    SYSTEM_PATH=$(cat "$INSTALL_DIR/system-path" 2>/dev/null || echo "")

    if [ -n "$SYSTEM_PATH" ] && [ -d "$SYSTEM_PATH" ]; then
        progress_step \
            "Installing system (offline — reading from ISO nix store)..." \
            "nixos-install \
                --root /mnt \
                --system '$SYSTEM_PATH' \
                --no-root-passwd \
                --no-channel-copy \
                --option substitute false \
                --option binary-caches ''"
    else
        log "WARNING: pre-built closure not found, falling back to online install"
        progress_step \
            "Installing system (online — fetching from binary cache)..." \
            "nixos-install \
                --root /mnt \
                --flake 'github:4nonX/D-PlaneOS#dplaneos' \
                --no-root-passwd \
                --no-channel-copy"
    fi

    progress_step \
        "Setting admin credentials..." \
        "
        HASH=\$(python3 -c \"
import bcrypt, sys
pw = sys.stdin.buffer.read().strip()
print(bcrypt.hashpw(pw, bcrypt.gensalt(rounds=12)).decode())
\" <<< '$ADMIN_PASS')
        mkdir -p /mnt/var/lib/dplaneos
        printf '%s' \"\$HASH\" > /mnt/var/lib/dplaneos/.first-boot-password
        chmod 600 /mnt/var/lib/dplaneos/.first-boot-password
        "

    PRIMARY_IP=$(ip route get 1.1.1.1 2>/dev/null \
        | awk '{for(i=1;i<=NF;i++) if ($i=="src") {print $(i+1); exit}}' \
        || echo "")
    [ -z "$PRIMARY_IP" ] && PRIMARY_IP="<your-server-ip>"

    log "Installation complete. Access IP: $PRIMARY_IP"
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
            "✓  D-PlaneOS Installed Successfully")" \
        "" \
        "Open a browser on any device in your network and go to:" \
        "" \
        "$(gum style --foreground "$TITLE_COLOR" --bold \
            "    http://${PRIMARY_IP}")" \
        "" \
        "$(gum style --foreground "$DIM_COLOR" "Username:  admin")" \
        "$(gum style --foreground "$DIM_COLOR" "Password:  (as configured)")" \
        "" \
        "$(gum style --foreground "$DIM_COLOR" \
            "Remove the USB drive, then press Enter to reboot.")"

    echo ""
    read -r _
    reboot
}

do_install() {
    select_disk
    set_admin_password
    select_boot_mode
    confirm_install
    run_install
    show_complete
}

trap '
    echo ""
    if gum confirm "Abort and return to menu?" 2>/dev/null; then
        show_welcome
    fi
' INT

show_welcome
