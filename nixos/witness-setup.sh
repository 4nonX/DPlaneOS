#!/usr/bin/env bash
# DPlaneOS Witness Node Setup
# Interactive installer for the Patroni/etcd quorum witness node.
# Launched from the combined installer ISO ("Install Witness Node") or
# from the standalone witness ISO auto-login.
set -euo pipefail

PATRONI_NIX=/etc/dplaneos-witness/patroni-witness.nix
LOG_FILE="/tmp/dplaneos-witness-install.log"

BORDER_COLOR="#6366f1"
TITLE_COLOR="#a5b4fc"
SUCCESS_COLOR="#4ade80"
ERROR_COLOR="#f87171"
DIM_COLOR="#6b7280"

LOCAL_IP=""
NODE_A_IP=""
NODE_B_IP=""
SSH_KEY=""
TARGET_DISK=""

log() { echo "[$(date '+%H:%M:%S')] $*" | tee -a "$LOG_FILE"; }

die() {
    clear
    gum style \
        --border double --border-foreground "$ERROR_COLOR" \
        --padding "1 2" --margin "1" \
        "$(gum style --foreground "$ERROR_COLOR" --bold "Witness Setup Failed")" \
        "" \
        "$*" \
        "" \
        "$(gum style --foreground "$DIM_COLOR" "Full log: $LOG_FILE")"
    exit 1
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
        || die "Step failed: $title\n\nSee: $LOG_FILE"
    log "Complete: $title"
}

# ── Welcome ───────────────────────────────────────────────────────────────────

clear
gum style \
    --border double \
    --border-foreground "$BORDER_COLOR" \
    --padding "1 4" --margin "1" --align center \
    "$(gum style --foreground "$TITLE_COLOR" --bold "DPlaneOS Witness Node Setup")" \
    "" \
    "$(gum style --foreground "$DIM_COLOR" "Patroni/etcd Quorum Node Installer")"

gum style --margin "0 2" \
    "This installs a minimal NixOS system running etcd." \
    "The witness participates only in quorum voting:" \
    "no ZFS, no DPlaneOS daemon, no storage pools." \
    "" \
    "Requirements:" \
    "  - 512 MB RAM, 4 GB disk" \
    "  - Static IP, reachable by both DPlaneOS nodes on ports 2379/2380" \
    "  - Internet access (packages fetched from cache.nixos.org)"

echo ""
gum confirm "Ready to proceed?" || exit 0

# ── IP Configuration ──────────────────────────────────────────────────────────

clear
gum style --foreground "$TITLE_COLOR" --bold --margin "1 0 0 1" \
    "Step 1 of 4 - Network Addresses"
gum style --foreground "$DIM_COLOR" --margin "0 0 1 1" \
    "Enter the permanent LAN IPs for all three cluster members." \
    "All three must be statically assigned before setup."

LOCAL_IP=$(gum input \
    --placeholder "e.g. 192.168.1.30" \
    --prompt "  Witness node IP  › " \
    --prompt.foreground "$BORDER_COLOR")

NODE_A_IP=$(gum input \
    --placeholder "e.g. 192.168.1.10" \
    --prompt "  DPlaneOS Node A  › " \
    --prompt.foreground "$BORDER_COLOR")

NODE_B_IP=$(gum input \
    --placeholder "e.g. 192.168.1.20" \
    --prompt "  DPlaneOS Node B  › " \
    --prompt.foreground "$BORDER_COLOR")

echo ""
gum style \
    --border rounded --border-foreground "$BORDER_COLOR" \
    --padding "0 2" --margin "0 1" \
    "  Witness : $(gum style --foreground "$TITLE_COLOR" "$LOCAL_IP")" \
    "  Node A  : $(gum style --foreground "$TITLE_COLOR" "$NODE_A_IP")" \
    "  Node B  : $(gum style --foreground "$TITLE_COLOR" "$NODE_B_IP")"
echo ""
gum confirm "Are these addresses correct?" || exit 0

# ── SSH Key ───────────────────────────────────────────────────────────────────

clear
gum style --foreground "$TITLE_COLOR" --bold --margin "1 0 0 1" \
    "Step 2 of 4 - SSH Access"
gum style --foreground "$DIM_COLOR" --margin "0 0 1 1" \
    "Provide the SSH public key for administrative access." \
    "Password authentication will be disabled on the installed system."

SSH_KEY=$(gum input \
    --placeholder "ssh-ed25519 AAAA... user@host" \
    --prompt "  SSH public key  › " \
    --prompt.foreground "$BORDER_COLOR" \
    --width 80)

if [ -z "$SSH_KEY" ]; then
    gum style --foreground "$ERROR_COLOR" --margin "0 0 0 1" \
        "No SSH key provided. You will not be able to log in after reboot."
    gum confirm "Continue without SSH key?" || exit 0
fi

log "SSH key configured: ${SSH_KEY:0:40}..."

# ── Disk Selection ────────────────────────────────────────────────────────────

clear
gum style --foreground "$TITLE_COLOR" --bold --margin "1 0 0 1" \
    "Step 3 of 4 - Target Disk"
gum style --foreground "$DIM_COLOR" --margin "0 0 1 1" \
    "The selected disk will be completely erased." \
    "A small disk is fine: the witness only needs 4 GB."

echo ""
lsblk -d -o NAME,SIZE,MODEL --exclude 7 | grep -v "^loop"
echo ""

TARGET_DISK=$(gum input \
    --placeholder "/dev/sda" \
    --prompt "  Install to disk  › " \
    --prompt.foreground "$BORDER_COLOR")

if [ ! -b "$TARGET_DISK" ]; then
    die "'$TARGET_DISK' is not a block device."
fi

DISK_INFO=$(lsblk -d -o SIZE,MODEL "$TARGET_DISK" | tail -1 | xargs)
echo ""
gum confirm \
    --prompt.foreground "$ERROR_COLOR" \
    --default=false \
    "ERASE $TARGET_DISK ($DISK_INFO) and install?" \
    || exit 0

log "Target disk: $TARGET_DISK ($DISK_INFO)"

# ── Summary ───────────────────────────────────────────────────────────────────

clear
gum style --foreground "$TITLE_COLOR" --bold --margin "1 0 0 1" \
    "Step 4 of 4 - Confirm"
gum style \
    --border rounded --border-foreground "$BORDER_COLOR" \
    --padding "1 3" --margin "1" \
    "$(gum style --foreground "$TITLE_COLOR" --bold "Witness Node Installation")" \
    "" \
    "  Witness IP : $(gum style --foreground "$TITLE_COLOR" "$LOCAL_IP")" \
    "  Node A     : $(gum style --foreground "$TITLE_COLOR" "$NODE_A_IP")" \
    "  Node B     : $(gum style --foreground "$TITLE_COLOR" "$NODE_B_IP")" \
    "  Install to : $(gum style --foreground "$TITLE_COLOR" "$TARGET_DISK")" \
    "" \
    "$(gum style --foreground "$ERROR_COLOR" --bold "  ALL DATA ON $TARGET_DISK WILL BE DESTROYED")"

echo ""
gum confirm \
    --prompt.foreground "$ERROR_COLOR" \
    --default=false \
    "Begin installation?" \
    || exit 0

# ── Partition and Format ──────────────────────────────────────────────────────

clear
gum style --foreground "$TITLE_COLOR" --bold --margin "1 0 1 1" \
    "Installing DPlaneOS Witness Node..."

progress_step "Partitioning $TARGET_DISK (EFI + root)..." "
    sgdisk --zap-all '$TARGET_DISK'
    parted -s '$TARGET_DISK' -- \
        mklabel gpt \
        mkpart ESP fat32 1MiB 512MiB \
        set 1 esp on \
        mkpart root ext4 512MiB 100%
    sleep 2
    partprobe '$TARGET_DISK' 2>/dev/null || true
    sleep 1
"

if [[ "$TARGET_DISK" == *nvme* ]] || [[ "$TARGET_DISK" == *mmcblk* ]]; then
    BOOT_PART="${TARGET_DISK}p1"
    ROOT_PART="${TARGET_DISK}p2"
else
    BOOT_PART="${TARGET_DISK}1"
    ROOT_PART="${TARGET_DISK}2"
fi

progress_step "Formatting partitions..." "
    mkfs.fat -F32 -n WITNESS_BOOT '$BOOT_PART'
    mkfs.ext4 -L witness-root -F '$ROOT_PART'
"

progress_step "Mounting filesystems..." "
    mount '$ROOT_PART' /mnt
    mkdir -p /mnt/boot
    mount '$BOOT_PART' /mnt/boot
"

# ── Generate NixOS Configuration ──────────────────────────────────────────────

progress_step "Writing NixOS configuration..." "
    mkdir -p /mnt/etc/nixos
    cp '$PATRONI_NIX' /mnt/etc/nixos/patroni-witness.nix
"

SSH_KEYS_LINE=""
if [ -n "$SSH_KEY" ]; then
    SSH_KEYS_LINE="  users.users.root.openssh.authorizedKeys.keys = [ \"$SSH_KEY\" ];"
fi

case "$(uname -m)" in
  aarch64) NIX_SYSTEM="aarch64-linux" ;;
  *)       NIX_SYSTEM="x86_64-linux"  ;;
esac

cat > /mnt/etc/nixos/flake.nix << FLAKEEOF
{
  inputs.nixpkgs.url = "github:NixOS/nixpkgs/nixos-25.11";
  outputs = { nixpkgs, ... }: {
    nixosConfigurations.witness = nixpkgs.lib.nixosSystem {
      system = "$NIX_SYSTEM";
      modules = [ ./configuration.nix ];
    };
  };
}
FLAKEEOF

cat > /mnt/etc/nixos/configuration.nix << NIXEOF
{ config, pkgs, lib, ... }:
{
  imports = [ ./patroni-witness.nix ];

  services.dplaneos.ha.witness = {
    enable       = true;
    localAddress = "$LOCAL_IP";
    nodeAAddress = "$NODE_A_IP";
    nodeBAddress = "$NODE_B_IP";
  };

  networking.hostName = "dplaneos-witness";
  time.timeZone       = "UTC";

  boot.loader.systemd-boot.enable      = true;
  boot.loader.efi.canTouchEfiVariables = true;

  nix.settings.experimental-features = [ "nix-command" "flakes" ];

$SSH_KEYS_LINE

  system.stateVersion = "25.11";
}
NIXEOF

log "Configuration written to /mnt/etc/nixos/"

# ── Install NixOS ─────────────────────────────────────────────────────────────

progress_step "Installing NixOS from binary cache (5-15 min)..." "
    nixos-install \
        --root /mnt \
        --flake /mnt/etc/nixos#witness \
        --no-root-passwd \
        --option substituters 'https://cache.nixos.org' \
        --option trusted-public-keys 'cache.nixos.org-1:6NCHdD59X431o0gWypbMrAURkbJ16ZPMQFGspcDShjY='
"

# ── Complete ──────────────────────────────────────────────────────────────────

clear
gum style \
    --border double --border-foreground "$SUCCESS_COLOR" \
    --padding "2 6" --margin "1" --align center \
    "$(gum style --foreground "$SUCCESS_COLOR" --bold "Witness Node Installed")" \
    "" \
    "$(gum style --foreground "$DIM_COLOR" "Witness : $LOCAL_IP")" \
    "$(gum style --foreground "$DIM_COLOR" "Node A  : $NODE_A_IP")" \
    "$(gum style --foreground "$DIM_COLOR" "Node B  : $NODE_B_IP")"

gum style --margin "0 2" \
    "" \
    "Next steps:" \
    "  1. On both DPlaneOS nodes, confirm 'services.dplaneos.ha.witnessAddress'" \
    "     is set to $LOCAL_IP and apply: sudo nixos-rebuild switch" \
    "  2. Verify ports 2379 and 2380 are open between all three nodes." \
    "  3. Remove the USB drive and reboot:" \
    "" \
    "       reboot" \
    "" \
    "After reboot, verify from any node:" \
    "  etcdctl endpoint health \\" \
    "    --endpoints=http://$LOCAL_IP:2379,http://$NODE_A_IP:2379,http://$NODE_B_IP:2379"

echo ""
read -rp "Press Enter to return to the main menu..." _
