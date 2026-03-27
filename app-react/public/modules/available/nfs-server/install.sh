#!/bin/bash
echo "Installing NFS Server..."
systemctl start nfs-server
systemctl enable nfs-server
ufw allow 2049/tcp
echo "NFS Server installed"
exit 0
