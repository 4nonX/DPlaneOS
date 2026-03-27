#!/bin/bash
# SMB Server Module - Uninstall Script
set -euo pipefail

echo "Uninstalling SMB/CIFS Server..."

# ── Service management ──────────────────────────────────────────────────────
systemctl stop smbd  || true  # allow failure if already stopped
systemctl stop nmbd  || true
systemctl disable smbd || true
systemctl disable nmbd || true

# ── Firewall ────────────────────────────────────────────────────────────────
if [ -f /etc/NIXOS ]; then
    echo "NixOS detected: SMB firewall rules are managed by the D-PlaneOS" \
         "NixWriter bridge. No ufw action taken."
elif command -v ufw &>/dev/null; then
    ufw delete allow 139/tcp
    ufw delete allow 445/tcp
else
    echo "WARNING: ufw not found and not NixOS. Firewall ports 139/445" \
         "must be closed manually." >&2
fi

echo "SMB Server uninstalled successfully"
