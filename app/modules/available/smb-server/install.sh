#!/bin/bash
# SMB Server Module - Install Script

echo "Installing SMB/CIFS Server..."

# Start and enable Samba
systemctl start smbd
systemctl enable smbd
systemctl start nmbd
systemctl enable nmbd

# Create default config if not exists
if [ ! -f /etc/samba/smb.conf.original ]; then
    cp /etc/samba/smb.conf /etc/samba/smb.conf.original
fi

# Allow SMB through firewall
ufw allow 139/tcp
ufw allow 445/tcp

echo "SMB Server installed successfully"
exit 0
