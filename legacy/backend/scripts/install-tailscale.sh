#!/bin/bash
#
# D-PlaneOS — Tailscale Installer Helper
# Version: 1.14.0
#
# This is the ONLY script www-data is allowed to run as root for package
# installation (see sudoers). It does nothing else — narrow privilege scope.
#
# Exit codes: 0 = success, 1 = already installed, 2 = install failed

set -e

LOG="/var/log/dplaneos/tailscale-install.log"
echo "[$(date)] Starting Tailscale install" | tee -a "$LOG"

# Already installed?
if command -v tailscale &>/dev/null; then
    echo "[$(date)] Tailscale already installed" | tee -a "$LOG"
    exit 0
fi

# Add Tailscale repo key + source list
curl -fsSL https://tailscale.com/install.sh | sh 2>&1 | tee -a "$LOG"
EXIT_CODE=$?

if [ $EXIT_CODE -ne 0 ]; then
    echo "[$(date)] Tailscale install failed (exit $EXIT_CODE)" | tee -a "$LOG"
    exit 2
fi

echo "[$(date)] Tailscale installed successfully" | tee -a "$LOG"
exit 0
