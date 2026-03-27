#!/bin/bash
# SMB Server Module - Install Script
set -euo pipefail

echo "Installing SMB/CIFS Server..."

# ── Service management ──────────────────────────────────────────────────────
# On NixOS, services are managed declaratively. systemctl start/enable
# still works for units that are already enabled via configuration.nix.
systemctl start smbd
systemctl enable smbd
systemctl start nmbd
systemctl enable nmbd

# ── Default config backup ───────────────────────────────────────────────────
if [ ! -f /etc/samba/smb.conf.original ]; then
    cp /etc/samba/smb.conf /etc/samba/smb.conf.original
fi

# ── Firewall ────────────────────────────────────────────────────────────────
# On NixOS, the firewall is managed declaratively by NixWriter
# (daemon writes dplane-state.json → nixos-rebuild applies it).
# ufw is not present on NixOS; calling it here would silently fail
# and return exit 0, giving the illusion of success. We skip it.
if [ -f /etc/NIXOS ]; then
    echo "NixOS detected: SMB firewall ports (139, 445) are managed by" \
         "the D-PlaneOS NixWriter bridge. No ufw action taken."
elif command -v ufw &>/dev/null; then
    ufw allow 139/tcp
    ufw allow 445/tcp
else
    echo "WARNING: ufw not found and not NixOS. Firewall ports 139/445" \
         "must be opened manually." >&2
fi

echo "SMB Server installed successfully"
