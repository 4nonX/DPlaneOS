#!/usr/bin/env bash
# DPlaneOS Installer
# ─────────────────────────────────────────────────────────────────────────────
# TUI installer - mirrors the TrueNAS SCALE installation flow.
# Works fully offline: the ISO contains the complete DPlaneOS closure.
#
# Flow:
#   1. Welcome screen
#   2. Select boot/OS disk
#   3. Set admin password
#   4. Check the firmware (UEFI; the system boots with systemd-boot)
#   5. Confirm and install
#   6. Complete - show IP and access URL
#
# Unattended (automation, the installer's VM test): set DPLANEOS_INSTALL_DISK
# and DPLANEOS_ADMIN_PASSWORD; the installer then installs without asking and
# exits with 0 or 1 instead of rebooting.
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

INSTALL_DISK="${DPLANEOS_INSTALL_DISK:-}"
ADMIN_PASS="${DPLANEOS_ADMIN_PASSWORD:-}"
UNATTENDED=""
[ -n "$INSTALL_DISK" ] && [ -n "$ADMIN_PASS" ] && UNATTENDED=1
BOOT_MODE=""
PRIMARY_IP=""
ROOT_SIZE=""

# The layout (disko.nix): 512 MB ESP, the system partition (Nix store and
# generations; updates are built there), the rest for /persist.
MIN_DISK_BYTES=$((32 * 1024 * 1024 * 1024))

log() { echo "[$(date '+%H:%M:%S')] $*" | tee -a "$LOG_FILE"; }

die() {
    if [ -n "$UNATTENDED" ]; then
        echo "INSTALL FAILED: $*" | tee -a "$LOG_FILE" >&2
        echo "--- last lines of $LOG_FILE:" >&2
        tail -n 40 "$LOG_FILE" >&2
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

# installer_disk prints the disk the installer itself was booted from.
installer_disk() {
    local src
    for m in /iso /run/rootfsbase /nix/.ro-store; do
        src=$(findmnt -n -o SOURCE "$m" 2>/dev/null | head -1) || true
        case "$src" in
            /dev/*) lsblk -n -o PKNAME "$src" 2>/dev/null | head -1; return ;;
        esac
    done
}

# list_disks prints "/dev/NAME SIZEBYTES MODEL" for every disk that can hold
# the system. TYPE comes before MODEL: a model with spaces (most of them) or
# none (virtio) shifted the columns, and those disks were not offered.
list_disks() {
    local skip name type size model
    skip=$(installer_disk)
    while read -r name type size; do
        [ "$type" = "disk" ] || continue
        [ "$name" = "$skip" ] && continue
        case "$name" in zram*|loop*|sr*|fd*) continue ;; esac
        [ "$size" -ge "$MIN_DISK_BYTES" ] || continue
        model=$(lsblk -dn -o MODEL "/dev/$name" 2>/dev/null | sed 's/[[:space:]]*$//')
        echo "/dev/$name $size ${model:-Unknown}"
    done < <(lsblk -dbn -o NAME,TYPE,SIZE 2>/dev/null)
}

human_size() {
    awk -v s="$1" 'BEGIN {
        if (s >= 1099511627776) printf "%.1f TB", s/1099511627776
        else printf "%.0f GB", s/1073741824 }'
}

# root_size: 40 GB on disks of 64 GB and more, 24 GB below (the rest is
# /persist: database, logs, Docker).
root_size() {
    if [ "$1" -ge $((64 * 1024 * 1024 * 1024)) ]; then echo 40G; else echo 24G; fi
}

select_disk() {
    local disks=() line name size model
    while IFS= read -r line; do
        disks+=("$line")
    done < <(list_disks)

    if [ -n "$UNATTENDED" ]; then
        for line in "${disks[@]}"; do
            read -r name size model <<<"$line"
            if [ "$name" = "$INSTALL_DISK" ]; then
                ROOT_SIZE=$(root_size "$size")
                log "Selected disk: $INSTALL_DISK ($(human_size "$size") $model), system partition $ROOT_SIZE"
                return
            fi
        done
        die "$INSTALL_DISK is not a usable disk (at least 32 GB, not the installer medium). Usable: ${disks[*]:-none}"
    fi

    clear
    gum style --foreground "$TITLE_COLOR" --bold --margin "1 0 0 1" \
        "Step 1 of 4 - Select boot disk"
    gum style --foreground "$DIM_COLOR" --margin "0 0 1 1" \
        "This disk will hold the DPlaneOS operating system (system + persistent state)." \
        "Your data disks are NOT touched here - ZFS pools are created after install."

    if [ ${#disks[@]} -eq 0 ]; then
        die "No suitable disks found (at least 32 GB; the installer medium is not offered).\nCheck hardware connections and retry."
    fi

    local choices=()
    for line in "${disks[@]}"; do
        read -r name size model <<<"$line"
        choices+=("$(printf '%-12s  %-8s  %s' "$name" "$(human_size "$size")" "$model")")
    done

    local selected
    selected=$(gum choose \
        --header "Available disks - select the OS boot disk:" \
        --header.foreground "$TITLE_COLOR" \
        --cursor.foreground "$BORDER_COLOR" \
        "${choices[@]}")

    INSTALL_DISK=$(echo "$selected" | awk '{print $1}')
    for line in "${disks[@]}"; do
        read -r name size model <<<"$line"
        [ "$name" = "$INSTALL_DISK" ] && ROOT_SIZE=$(root_size "$size")
    done

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

    log "Selected disk: $INSTALL_DISK ($selected), system partition $ROOT_SIZE"
}

# password_ok: the daemon's policy (8+ characters, upper, lower, digit, symbol).
password_ok() {
    [ ${#1} -ge 8 ] && printf '%s' "$1" | grep -qP '(?=.*[A-Z])(?=.*[a-z])(?=.*[0-9])(?=.*[^A-Za-z0-9])'
}

set_admin_password() {
    if [ -n "$UNATTENDED" ]; then
        password_ok "$ADMIN_PASS" || die "DPLANEOS_ADMIN_PASSWORD does not meet the policy (8+ characters with upper and lower case, a digit and a symbol)."
        return
    fi
    clear
    gum style --foreground "$TITLE_COLOR" --bold --margin "1 0 0 1" \
        "Step 2 of 4 - Administrator password"
    gum style --foreground "$DIM_COLOR" --margin "0 0 1 1" \
        "This password is used to log into the DPlaneOS web interface." \
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
    # The system boots with systemd-boot from the ESP: UEFI only. (A "Legacy
    # BIOS" choice was offered and installed a system that could not boot.)
    if [ ! -d /sys/firmware/efi ]; then
        die "This machine started the installer in legacy BIOS mode.\nDPlaneOS boots with UEFI: enable UEFI in the firmware setup (disable CSM / legacy boot) and start the installer again."
    fi
    BOOT_MODE="UEFI"
    log "Boot mode: UEFI"
}

confirm_install() {
    [ -n "$UNATTENDED" ] && return
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
    if [ -n "$UNATTENDED" ]; then
        bash -c "$cmd >> $LOG_FILE 2>&1" || die "Step failed: $title (see $LOG_FILE)"
        log "Complete: $title"
        return
    fi
    gum spin \
        --spinner dot \
        --spinner.foreground "$BORDER_COLOR" \
        --title "  $title" \
        -- bash -c "$cmd >> $LOG_FILE 2>&1" \
        || die "Step failed: $title\n\nSee log for details: $LOG_FILE"
    log "Complete: $title"
}

run_install() {
    if [ -n "$UNATTENDED" ]; then
        log "Installing DPlaneOS v${DPLANEOS_VERSION}..."
    else
        clear
        gum style --foreground "$TITLE_COLOR" --bold --margin "1 0 1 1" \
            "Installing DPlaneOS v${DPLANEOS_VERSION}..."
    fi

    local patched_disko="/tmp/disko-install.nix"
    sed -e "s|device = \"/dev/nvme0n1\"|device = \"${INSTALL_DISK}\"|g" \
        -e "s|size    = \"40G\";|size    = \"${ROOT_SIZE}\";|" \
        "$INSTALL_DIR/disko.nix" > "$patched_disko"
    if ! grep -q "device = \"${INSTALL_DISK}\"" "$patched_disko" || ! grep -q "size    = \"${ROOT_SIZE}\";" "$patched_disko"; then
        die "Could not prepare the partition layout for $INSTALL_DISK"
    fi

    # The system mounts its partitions by label: a second disk with a DPlaneOS
    # system (an old boot disk) would make them ambiguous at every boot.
    local other
    other=$(lsblk -rno PKNAME,PARTLABEL 2>/dev/null \
        | awk -v d="$(basename "$INSTALL_DISK")" '$1 != d && $2 ~ /^dplaneos-(esp|system|persist)$/ {print "/dev/" $1}' | sort -u | tr '\n' ' ')
    if [ -n "$other" ]; then
        die "Another disk already holds a DPlaneOS system: ${other}\nRemove it or wipe it first (two would make the system's partitions ambiguous at boot)."
    fi

    # The partitioning script was built with the ISO (no Nix evaluation
    # offline); it works on /dev/dplaneos-install-target. It wipes, partitions,
    # formats and mounts the disk at /mnt.
    local disko_script="$INSTALL_DIR/disko-$ROOT_SIZE"
    [ -e "$disko_script" ] || die "No partitioning script for a $ROOT_SIZE system partition ($disko_script)"
    progress_step \
        "Partitioning $INSTALL_DISK (ESP + system + persist)..." \
        "ln -sfn '$INSTALL_DISK' /dev/dplaneos-install-target && bash '$disko_script' --yes-wipe-all-disks"

    progress_step \
        "Checking the mounts..." \
        "mountpoint -q /mnt && mountpoint -q /mnt/boot && mountpoint -q /mnt/persist"

    progress_step \
        "Preparing system configuration..." \
        "
        mkdir -p /mnt/etc/nixos
        cp '$patched_disko' /mnt/etc/nixos/disko.nix
        nixos-generate-config --root /mnt --no-filesystems
        cat > /mnt/etc/nixos/configuration.nix <<'NIXEOF'
# DPlaneOS is built from /etc/nixos/flake.nix (the DPlaneOS release plus
# hardware-configuration.nix and, if present, local.nix), which the daemon
# writes on first start. A rebuild without the flake would install plain NixOS.
throw \"DPlaneOS: rebuild with: nixos-rebuild switch --flake /etc/nixos#dplaneos --impure\"
NIXEOF
        "

    local SYSTEM_PATH
    SYSTEM_PATH=$(cat "$INSTALL_DIR/system-path" 2>/dev/null || echo "")

    if [ -n "$SYSTEM_PATH" ] && [ -d "$SYSTEM_PATH" ]; then
        # nixos-install copies the closure from this (the ISO's) store by
        # using it as a substituter; "--option substitute false" turned that
        # off and the offline install failed ("no substituter can build it").
        # The ISO has no network substituters, so nothing is downloaded.
        progress_step \
            "Installing system (offline - reading from ISO nix store)..." \
            "nixos-install \
                --root /mnt \
                --system '$SYSTEM_PATH' \
                --no-root-passwd \
                --no-channel-copy"
    else
        log "WARNING: pre-built closure not found, falling back to online install"
        progress_step \
            "Installing system (online - fetching from binary cache)..." \
            "nixos-install \
                --root /mnt \
                --flake 'github:4nonX/DPlaneOS#dplaneos' \
                --no-root-passwd \
                --no-channel-copy"
    fi

    _PASS_TMP=$(mktemp)
    chmod 600 "$_PASS_TMP"
    printf '%s' "$ADMIN_PASS" > "$_PASS_TMP"

    progress_step \
        "Setting admin credentials..." \
        "
        set -e
        HASH=\$(python3 -c \"
import bcrypt, sys
with open(sys.argv[1], 'rb') as f:
    pw = f.read()
print(bcrypt.hashpw(pw, bcrypt.gensalt(rounds=12)).decode())
\" $_PASS_TMP)
        case \"\$HASH\" in '\$2'*) ;; *) echo 'password hashing failed' >&2; exit 1 ;; esac
        # On /persist: /var/lib/dplaneos is bind-mounted from there at boot,
        # which hid a file written to the system partition (the admin was
        # created without a password).
        mkdir -p /mnt/persist/var/lib/dplaneos
        printf '%s' \"\$HASH\" > /mnt/persist/var/lib/dplaneos/.first-boot-password
        chmod 600 /mnt/persist/var/lib/dplaneos/.first-boot-password
        "

    rm -f "$_PASS_TMP"

    PRIMARY_IP=$(ip route get 1.1.1.1 2>/dev/null \
        | awk '{for(i=1;i<=NF;i++) if ($i=="src") {print $(i+1); exit}}' \
        || echo "")
    [ -z "$PRIMARY_IP" ] && PRIMARY_IP="<your-server-ip>"

    log "Installation complete. Access IP: $PRIMARY_IP"
}

show_complete() {
    if [ -n "$UNATTENDED" ]; then
        log "Installed. The web interface will be at http://${PRIMARY_IP} after the reboot."
        exit 0
    fi
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

if [ -n "$UNATTENDED" ]; then
    do_install
fi

trap '
    echo ""
    if gum confirm "Abort and return to menu?" 2>/dev/null; then
        show_welcome
    fi
' INT

show_welcome
