#!/bin/bash
# Monitoring Stack Module - Install Script
set -euo pipefail

echo "Installing Monitoring Stack..."

# Docker Compose deployment handled by the module engine.

# ── Firewall ────────────────────────────────────────────────────────────────
if [ -f /etc/NIXOS ]; then
    echo "NixOS detected: Monitoring firewall ports (9090, 3000) are managed" \
         "by the D-PlaneOS NixWriter bridge. No ufw action taken."
elif command -v ufw &>/dev/null; then
    ufw allow 9090/tcp
    ufw allow 3000/tcp
else
    echo "WARNING: ufw not found and not NixOS. Firewall ports 9090/3000" \
         "must be opened manually." >&2
fi

echo "Monitoring installed - Access Grafana at :3000"
