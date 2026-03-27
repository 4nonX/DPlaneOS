#!/bin/bash
# SMB Server Module - Uninstall Script

echo "Uninstalling SMB/CIFS Server..."

# Stop services
systemctl stop smbd
systemctl stop nmbd
systemctl disable smbd
systemctl disable nmbd

# Remove firewall rules
ufw delete allow 139/tcp
ufw delete allow 445/tcp

echo "SMB Server uninstalled successfully"
exit 0
