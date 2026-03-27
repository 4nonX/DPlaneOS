#!/bin/bash
echo "Uninstalling NFS Server..."
systemctl stop nfs-server
systemctl disable nfs-server
ufw delete allow 2049/tcp
echo "NFS Server uninstalled"
exit 0
