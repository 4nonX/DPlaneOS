#!/usr/bin/env bash
# D-PlaneOS — NixOS First-Boot Setup
# ─────────────────────────────────────────────────────────────────────────────
# Run once after initial nixos-install and reboot:
#   bash /root/setup-nixos.sh
#
# What this script does:
#   1. Generates a unique ZFS host ID and writes it to configuration.nix
#   2. Sets the correct timezone
#   3. Detects UEFI vs BIOS and adjusts the boot loader config
#   4. Verifies the ZFS pool configuration
#   5. Triggers a nixos-rebuild switch
# ─────────────────────────────────────────────────────────────────────────────
set -euo pipefail

CONFIG="/etc/nixos/configuration.nix"
BOLD="\033[1m"
GREEN="\033[32m"
YELLOW="\033[33m"
RED="\033[31m"
NC="\033[0m"

log()   { echo -e "${GREEN}✓${NC} $*"; }
warn()  { echo -e "${YELLOW}⚠${NC}  $*"; }
error() { echo -e "${RED}✗${NC} $*" >&2; }
step()  { echo -e "\n${BOLD}$*${NC}"; }

# ─── Root check ──────────────────────────────────────────────────────────────
if [ "$(id -u)" != "0" ]; then
  error "This script must be run as root."
  exit 1
fi

echo -e "${BOLD}"
echo "    D-PlaneOS v$(cat "$(dirname "$0")/../VERSION" 2>/dev/null | tr -d "[:space:]" || echo "?") — NixOS Setup"
echo "────────────────────────────────────────"
echo -e "${NC}"

# ─── Step 1: Generate unique ZFS host ID ─────────────────────────────────────
step "Step 1/5: ZFS Host ID"

# ZFS requires a unique hostId per machine to prevent pool import conflicts.
# We derive it from /etc/machine-id (generated during nixos-install).
if [ -f /etc/machine-id ]; then
  HOSTID=$(head -c 8 /etc/machine-id)
else
  HOSTID=$(dd if=/dev/urandom bs=1 count=4 2>/dev/null | xxd -p)
fi

# Validate: must be 8 hex characters
if ! echo "$HOSTID" | grep -qE '^[0-9a-f]{8}$'; then
  HOSTID=$(od -A n -t x4 -N 4 /dev/urandom | tr -d ' \n')
fi

log "Generated hostId: $HOSTID"

# Write hostId into configuration.nix
if grep -q 'networking.hostId' "$CONFIG"; then
  sed -i "s/networking.hostId = \"[^\"]*\"/networking.hostId = \"$HOSTID\"/" "$CONFIG"
  log "Updated hostId in $CONFIG"
else
  warn "networking.hostId not found in $CONFIG — add it manually:"
  echo "  networking.hostId = \"$HOSTID\";"
fi

# ─── Step 2: Timezone ─────────────────────────────────────────────────────────
step "Step 2/5: Timezone"

# Detect current timezone
CURRENT_TZ=$(timedatectl show --property=Timezone --value 2>/dev/null || echo "UTC")
echo "Current timezone: $CURRENT_TZ"
echo ""
echo "Press Enter to keep '$CURRENT_TZ', or type a new timezone"
echo "(full list: timedatectl list-timezones)"
echo -n "Timezone [${CURRENT_TZ}]: "
read -r INPUT_TZ
TZ="${INPUT_TZ:-$CURRENT_TZ}"

# Validate timezone
if ! timedatectl list-timezones 2>/dev/null | grep -qx "$TZ"; then
  warn "Unknown timezone '$TZ' — keeping '$CURRENT_TZ'"
  TZ="$CURRENT_TZ"
fi

# Write timezone into configuration.nix
if grep -q 'time.timeZone' "$CONFIG"; then
  sed -i "s|time.timeZone = \"[^\"]*\"|time.timeZone = \"$TZ\"|" "$CONFIG"
  log "Set timezone to: $TZ"
else
  warn "time.timeZone not found in $CONFIG — add it manually:"
  echo "  time.timeZone = \"$TZ\";"
fi

# ─── Step 3: Boot loader detection ───────────────────────────────────────────
step "Step 3/5: Boot Loader Detection"

if [ -d /sys/firmware/efi ]; then
  log "UEFI boot detected — using systemd-boot (already configured)"
else
  warn "BIOS/MBR boot detected"
  echo ""
  echo "Your configuration.nix currently has UEFI (systemd-boot) configured."
  echo "For BIOS/MBR, you need to change it. Edit $CONFIG and:"
  echo "  - Comment out:  boot.loader.systemd-boot.enable = true;"
  echo "  - Comment out:  boot.loader.efi.canTouchEfiVariables = true;"
  echo "  - Uncomment:    boot.loader.grub.enable = true;"
  echo "  - Uncomment:    boot.loader.grub.device = \"/dev/sda\";  # your boot disk"
  echo ""
  echo -n "Press Enter after editing configuration.nix, or Ctrl+C to abort: "
  read -r _
fi

# ─── Step 4: ZFS pool verification ───────────────────────────────────────────
step "Step 4/5: ZFS Pool Check"

if zpool list &>/dev/null && zpool list | grep -q ONLINE; then
  POOLS=$(zpool list -H -o name 2>/dev/null | tr '\n' ' ')
  log "ZFS pools detected: $POOLS"
else
  warn "No ZFS pools found (or ZFS not loaded yet)."
  echo ""
  echo "If you have existing data disks, import them after this script:"
  echo "  zpool import               # list importable pools"
  echo "  zpool import <poolname>    # import your pool"
  echo ""
  echo "To create a new pool:"
  echo "  zpool create tank mirror /dev/sdb /dev/sdc     # 2-disk mirror"
  echo "  zpool create tank raidz1 /dev/sdb /dev/sdc /dev/sdd  # RAIDZ1"
fi

# ─── Step 5: Network interface detection ─────────────────────────────────────
step "Step 5/5: Network Interface"

PRIMARY_IFACE=$(ip route show default 2>/dev/null | awk '/default/ {print $5}' | head -1)

if [ -n "$PRIMARY_IFACE" ] && [ "$PRIMARY_IFACE" != "eth0" ]; then
  warn "Primary interface is '$PRIMARY_IFACE', not 'eth0'"
  echo ""
  echo "Update $CONFIG — find this line:"
  echo "  matchConfig.Name = \"eth0\";"
  echo "Change it to:"
  echo "  matchConfig.Name = \"$PRIMARY_IFACE\";"
  echo ""
  # Attempt automatic fix
  if grep -q 'matchConfig.Name = "eth0"' "$CONFIG"; then
    sed -i "s/matchConfig.Name = \"eth0\"/matchConfig.Name = \"$PRIMARY_IFACE\"/" "$CONFIG"
    log "Auto-updated interface to: $PRIMARY_IFACE"
  fi
else
  log "Interface: ${PRIMARY_IFACE:-eth0}"
fi

# ─── Step 5b: Create dplane-generated.nix stub ───────────────────────────────
# The D-PlaneOS daemon writes network config (VLANs, bonds, static IPs, DNS,
# NTP, firewall) into this file so changes survive nixos-rebuild.
# We create an empty stub now so the import in configuration.nix resolves.
GENERATED="/etc/nixos/dplane-generated.nix"
if [ ! -f "$GENERATED" ]; then
  cat > "$GENERATED" << 'NIXEOF'
# dplane-generated.nix
# AUTO-GENERATED by D-PlaneOS. DO NOT EDIT BY HAND.
# This file is written by the web UI to persist network configuration.
# It will be populated automatically when you configure networking via the UI.
{ config, lib, pkgs, ... }:
{
  # (empty — populated by D-PlaneOS daemon on first network change)
}
NIXEOF
  log "Created $GENERATED (stub — will be populated by web UI)"
else
  log "$GENERATED already exists"
fi

# ─── Summary ─────────────────────────────────────────────────────────────────
echo ""
echo -e "${BOLD}Setup complete. Configuration written to $CONFIG${NC}"
echo ""
echo "  hostId:    $HOSTID"
echo "  timezone:  $TZ"
echo "  interface: ${PRIMARY_IFACE:-eth0}"
echo ""
echo -e "${BOLD}Next step: build the system${NC}"
echo ""
echo "  sudo nixos-rebuild switch"
echo ""
echo "The first build will fail with a hash mismatch error — this is expected."
echo "See INSTALLATION-GUIDE-NIXOS.md Step 2.3 for how to resolve it (< 5 min)."
echo ""
