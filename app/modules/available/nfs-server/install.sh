#!/bin/bash
# NFS Server Module - Install Script
set -euo pipefail

echo "Installing NFS Server..."

# ── Service management ──────────────────────────────────────────────────────
systemctl start nfs-server
systemctl enable nfs-server

# ── Firewall ────────────────────────────────────────────────────────────────
if [ -f /etc/NIXOS ]; then
    echo "NixOS detected: NFS firewall port (2049) is managed by the" \
         "D-PlaneOS NixWriter bridge. No ufw action taken."
elif command -v ufw &>/dev/null; then
    ufw allow 2049/tcp
else
    echo "WARNING: ufw not found and not NixOS. Firewall port 2049" \
         "must be opened manually." >&2
fi

echo "NFS Server installed"
