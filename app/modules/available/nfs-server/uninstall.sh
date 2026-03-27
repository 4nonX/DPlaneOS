#!/bin/bash
# NFS Server Module - Uninstall Script
set -euo pipefail

echo "Uninstalling NFS Server..."

# ── Service management ──────────────────────────────────────────────────────
systemctl stop nfs-server    || true
systemctl disable nfs-server || true

# ── Firewall ────────────────────────────────────────────────────────────────
if [ -f /etc/NIXOS ]; then
    echo "NixOS detected: NFS firewall rule is managed by the D-PlaneOS" \
         "NixWriter bridge. No ufw action taken."
elif command -v ufw &>/dev/null; then
    ufw delete allow 2049/tcp
else
    echo "WARNING: ufw not found and not NixOS. Firewall port 2049" \
         "must be closed manually." >&2
fi

echo "NFS Server uninstalled"
