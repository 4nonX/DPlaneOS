#!/bin/bash
#
# D-PlaneOS UPS Management Setup
# 
# Installs and configures NUT (Network UPS Tools) for:
# - APC, CyberPower, Eaton, Tripp Lite UPS devices
# - USB and serial connections
# - Auto-detection of connected UPS
# - Clean shutdown on power loss
#
# Usage: Called by install.sh or standalone
#

set -e

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m'

log() { echo -e "${GREEN}✓${NC} $1"; }
warn() { echo -e "${YELLOW}⚠${NC} $1"; }
error() { echo -e "${RED}✗${NC} $1"; }
info() { echo -e "${BLUE}ℹ${NC} $1"; }

echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo "  D-PlaneOS UPS Management Setup"
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo ""

# ============================================================
# STEP 1: Install NUT
# ============================================================

info "Installing Network UPS Tools (NUT)..."

DEBIAN_FRONTEND=noninteractive apt-get install -y \
    nut nut-client nut-server \
    >> /var/log/dplaneos-ups-setup.log 2>&1

if [ $? -eq 0 ]; then
    log "NUT installed successfully"
else
    error "Failed to install NUT"
    exit 1
fi

# ============================================================
# STEP 2: Detect UPS
# ============================================================

info "Detecting connected UPS devices..."

# Scan for USB UPS devices
UPS_DETECTED=$(nut-scanner -U 2>/dev/null || echo "")

if [ -z "$UPS_DETECTED" ]; then
    warn "No UPS detected"
    info "If you have a UPS:"
    info "  1. Connect it via USB"
    info "  2. Run: sudo /opt/dplaneos/scripts/ups-setup.sh"
    echo ""
    info "UPS support installed but not configured"
    exit 0
fi

log "UPS detected!"
echo "$UPS_DETECTED"
echo ""

# ============================================================
# STEP 3: Configure NUT
# ============================================================

info "Configuring NUT..."

# Parse detected UPS info
UPS_DRIVER=$(echo "$UPS_DETECTED" | grep driver | cut -d'"' -f2 | head -1)
UPS_PORT=$(echo "$UPS_DETECTED" | grep port | cut -d'"' -f2 | head -1)
UPS_DESC=$(echo "$UPS_DETECTED" | grep desc | cut -d'"' -f2 | head -1)

if [ -z "$UPS_DRIVER" ] || [ -z "$UPS_PORT" ]; then
    error "Could not parse UPS configuration"
    exit 1
fi

log "Driver: $UPS_DRIVER"
log "Port: $UPS_PORT"
log "Description: $UPS_DESC"

# Configure ups.conf
cat > /etc/nut/ups.conf <<EOUPSC
# D-PlaneOS UPS Configuration
# Auto-detected configuration

[dplaneos-ups]
    driver = $UPS_DRIVER
    port = $UPS_PORT
    desc = "$UPS_DESC"
    # Poll interval (seconds)
    pollinterval = 2
EOUPSC

log "ups.conf configured"

# Configure upsd.conf
cat > /etc/nut/upsd.conf <<EOUPSDC
# D-PlaneOS UPSD Configuration

LISTEN 127.0.0.1 3493
LISTEN ::1 3493

MAXAGE 15
EOUPSDC

log "upsd.conf configured"

# Configure upsd.users
cat > /etc/nut/upsd.users <<EOUSERS
# D-PlaneOS UPS Users

[dplaneos]
    password = $(openssl rand -base64 32)
    upsmon master
    
[monuser]
    password = $(openssl rand -base64 32)
    upsmon slave
EOUSERS

chmod 640 /etc/nut/upsd.users
chown root:nut /etc/nut/upsd.users

log "upsd.users configured"

# Configure upsmon.conf
MONITOR_PASSWORD=$(grep "password =" /etc/nut/upsd.users | head -1 | awk '{print $3}')

cat > /etc/nut/upsmon.conf <<EOUPSMON
# D-PlaneOS UPS Monitor Configuration

# Monitor the local UPS
MONITOR dplaneos-ups@localhost 1 dplaneos $MONITOR_PASSWORD master

# Action on power failure
SHUTDOWNCMD "/sbin/shutdown -h +0"

# Notify command
NOTIFYCMD /opt/dplaneos/scripts/ups-notify.sh

# Notification flags
NOTIFYFLAG ONBATT   SYSLOG+WALL+EXEC
NOTIFYFLAG LOWBATT  SYSLOG+WALL+EXEC
NOTIFYFLAG ONLINE   SYSLOG+EXEC
NOTIFYFLAG REPLBATT SYSLOG+EXEC
NOTIFYFLAG FSD      SYSLOG+WALL+EXEC

# Polling interval
POLLFREQ 5
POLLFREQALERT 2

# Power failure actions
MINSUPPLIES 1
FINALDELAY 5

# Low battery action
HOSTSYNC 15
EOUPSMON

chmod 640 /etc/nut/upsmon.conf
chown root:nut /etc/nut/upsmon.conf

log "upsmon.conf configured"

# Configure nut.conf
cat > /etc/nut/nut.conf <<EONUTC
# D-PlaneOS NUT Mode Configuration
MODE=standalone
EONUTC

log "nut.conf configured"

# ============================================================
# STEP 4: Create Notification Script
# ============================================================

info "Creating notification script..."

mkdir -p /opt/dplaneos/scripts

cat > /opt/dplaneos/scripts/ups-notify.sh <<'EONOTIFY'
#!/bin/bash
#
# UPS Event Notification Script
#

NOTIFYTYPE="$1"
UPS="$2"

case "$NOTIFYTYPE" in
    ONBATT)
        # On battery power
        /opt/dplaneos/scripts/create-alert.sh \
            "ups_battery" \
            "warning" \
            "UPS: On Battery Power" \
            "System running on battery power. AC power lost." \
            "UPS: $UPS"
        ;;
    LOWBATT)
        # Low battery
        /opt/dplaneos/scripts/create-alert.sh \
            "ups_low_battery" \
            "critical" \
            "UPS: Low Battery!" \
            "Battery critically low. System will shut down soon." \
            "UPS: $UPS"
        ;;
    ONLINE)
        # Back on AC power
        /opt/dplaneos/scripts/create-alert.sh \
            "ups_online" \
            "info" \
            "UPS: Power Restored" \
            "AC power restored. System running normally." \
            "UPS: $UPS"
        ;;
    REPLBATT)
        # Replace battery
        /opt/dplaneos/scripts/create-alert.sh \
            "ups_replace_battery" \
            "warning" \
            "UPS: Replace Battery" \
            "UPS battery needs replacement." \
            "UPS: $UPS"
        ;;
    FSD)
        # Forced shutdown
        logger -t "UPS" "Forced shutdown initiated by UPS"
        ;;
esac
EONOTIFY

chmod +x /opt/dplaneos/scripts/ups-notify.sh

log "Notification script created"

# ============================================================
# STEP 5: Start Services
# ============================================================

info "Starting NUT services..."

# Start driver
upsdrvctl start >> /var/log/dplaneos-ups-setup.log 2>&1

if [ $? -eq 0 ]; then
    log "UPS driver started"
else
    error "Failed to start UPS driver"
    info "Check: tail -f /var/log/dplaneos-ups-setup.log"
    exit 1
fi

# Start upsd
systemctl restart nut-server

if systemctl is-active nut-server >/dev/null 2>&1; then
    log "nut-server started"
else
    error "Failed to start nut-server"
    exit 1
fi

# Start upsmon
systemctl restart nut-monitor

if systemctl is-active nut-monitor >/dev/null 2>&1; then
    log "nut-monitor started"
else
    error "Failed to start nut-monitor"
    exit 1
fi

# Enable on boot
systemctl enable nut-server nut-monitor

log "NUT services enabled on boot"

# ============================================================
# STEP 6: Test Configuration
# ============================================================

info "Testing UPS connection..."

sleep 2

UPS_STATUS=$(upsc dplaneos-ups 2>/dev/null | head -5)

if [ -n "$UPS_STATUS" ]; then
    log "UPS connection successful!"
    echo ""
    echo "$UPS_STATUS"
    echo ""
else
    warn "Could not query UPS status"
    info "Check: upsc dplaneos-ups"
fi

# ============================================================
# SUMMARY
# ============================================================

echo ""
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo "  UPS Management Setup Complete!"
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo ""
log "NUT installed and configured"
log "UPS auto-detected and connected"
log "Clean shutdown on power loss enabled"
log "Notifications enabled"
echo ""
info "Check UPS status:"
echo "  upsc dplaneos-ups"
echo ""
info "Test shutdown (BE CAREFUL!):"
echo "  upsmon -c fsd"
echo ""
info "View in Dashboard:"
echo "  → Hardware → UPS Status"
echo ""
