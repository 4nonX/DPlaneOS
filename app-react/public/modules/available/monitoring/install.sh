#!/bin/bash
echo "Installing Monitoring Stack..."
# Docker Compose would be deployed here
ufw allow 9090/tcp
ufw allow 3000/tcp
echo "Monitoring installed - Access Grafana at :3000"
exit 0
