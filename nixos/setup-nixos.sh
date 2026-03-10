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

# ─── Step 5b: Install dplane-generated.nix (static JSON-to-Nix bridge) ────────
# v5.0 architecture: the daemon writes /var/lib/dplaneos/dplane-state.json
# (pure JSON via encoding/json). dplane-generated.nix is a STATIC file that
# reads the JSON at eval time via builtins.fromJSON — zero dynamic Nix syntax,
# zero risk of a daemon write breaking nixos-rebuild.
#
# This file is installed once here and never modified by the daemon again.
GENERATED="/etc/nixos/dplane-generated.nix"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SOURCE_NIX="${SCRIPT_DIR}/dplane-generated.nix"

if [ -f "$SOURCE_NIX" ]; then
  cp "$SOURCE_NIX" "$GENERATED"
  log "Installed static JSON-to-Nix bridge: $GENERATED"
else
  # Fallback: write the bridge inline if the source file is missing
  cat > "$GENERATED" << 'NIXEOF'
{ config, lib, pkgs, ... }:
let
  stateFile = /var/lib/dplaneos/dplane-state.json;
  s = if builtins.pathExists stateFile
        then builtins.fromJSON (builtins.readFile stateFile)
        else {};
in {
  networking.hostName = s.hostname or config.networking.hostName;
  time.timeZone       = s.timezone or config.time.timeZone;
  networking.nameservers     = s.dns_servers or [];
  services.timesyncd.servers = s.ntp_servers or [];
  networking.firewall.allowedTCPPorts = lib.mkIf (s ? firewall_tcp) s.firewall_tcp;
  networking.firewall.allowedUDPPorts = lib.mkIf (s ? firewall_udp) s.firewall_udp;
  systemd.network.enable = true;
}
NIXEOF
  warn "dplane-generated.nix source not found — wrote minimal fallback to $GENERATED"
fi

# Seed an empty state file so dplane-generated.nix can always resolve on first boot
STATE_FILE="/var/lib/dplaneos/dplane-state.json"
mkdir -p "$(dirname "$STATE_FILE")"
if [ ! -f "$STATE_FILE" ]; then
  echo '{}' > "$STATE_FILE"
  log "Created empty state file: $STATE_FILE"
fi

# Ensure dplane-generated.nix is imported by configuration.nix
if ! grep -q "dplane-generated.nix" "$CONFIG" 2>/dev/null; then
  # Insert the import after the first 'imports = [' line
  if grep -q 'imports = \[' "$CONFIG"; then
    sed -i 's|imports = \[|imports = [\n    ./dplane-generated.nix  # D-PlaneOS JSON-to-Nix bridge (v5.0)|' "$CONFIG"
    log "Added dplane-generated.nix import to $CONFIG"
  else
    warn "Could not auto-add import — add this to $CONFIG manually:"
    echo "  imports = [ ./dplane-generated.nix ];"
  fi
else
  log "dplane-generated.nix already imported in $CONFIG"
fi

# ─── Summary ─────────────────────────────────────────────────────────────────
echo ""
echo -e "${BOLD}Setup complete. Configuration written to $CONFIG${NC}"
echo ""
echo "  hostId:    $HOSTID"
echo "  timezone:  $TZ"
echo "  interface: ${PRIMARY_IFACE:-eth0}"
echo ""
echo -e "${BOLD}Next steps${NC}"
echo ""
echo "  1. Edit configuration-standalone.nix:"
echo "       - Set networking.hostName"
echo "       - Add your SSH public key to services.dplaneos.sshKeys"
echo "       - Replace Pangolin NEWT_ID / NEWT_SECRET (or remove that block)"
echo ""
echo "  2. Get the correct vendorHash for the Go daemon:"
echo "       nix build .#dplaneos-daemon 2>&1 | grep 'got:'"
echo "     Copy the 'got: sha256-...' value into flake.nix → vendorHash"
echo ""
echo "  3. Build the system:"
echo "       sudo nixos-rebuild switch --flake .#dplaneos"
echo ""
echo "  4. Open http://${PRIMARY_IFACE:-your-nas-ip} or http://\$(hostname).local"
echo "     Login: admin / (generated on first start — check: journalctl -u dplaned | grep -i password)"
echo ""
