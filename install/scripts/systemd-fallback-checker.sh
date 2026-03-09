#!/bin/bash
#
# D-PlaneOS Systemd Fallback Checker
#
# SOLVES: "Ohne systemd bleibt Dashboard schwarz"
#
# This script detects if systemd is available and provides
# fallback mechanisms for Docker/non-systemd environments
#

set -e

DPLANEOS_DIR="/opt/dplaneos"
LOG_FILE="/var/log/dplaneos/startup.log"

# Colors
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

log() {
    echo -e "${GREEN}[INFO]${NC} $1" | tee -a "$LOG_FILE"
}

warn() {
    echo -e "${YELLOW}[WARN]${NC} $1" | tee -a "$LOG_FILE"
}

error() {
    echo -e "${RED}[ERROR]${NC} $1" | tee -a "$LOG_FILE"
}

# Check if systemd is available
check_systemd() {
    if command -v systemctl &> /dev/null && pidof systemd &> /dev/null; then
        return 0
    else
        return 1
    fi
}

# Start daemon with systemd
start_with_systemd() {
    log "Starting D-PlaneOS with systemd..."
    
    # Enable and start main daemon
    systemctl enable dplaneos-daemon.service
    systemctl start dplaneos-daemon.service
    
    # Enable and start realtime service (if exists)
    if [ -f /etc/systemd/system/dplaneos-realtime.service ]; then
        systemctl enable dplaneos-realtime.service
        systemctl start dplaneos-realtime.service
    fi
    
    log "Services started via systemd"
}

# Start daemon without systemd (fallback)
start_without_systemd() {
    warn "Systemd not detected - using fallback startup method"
    
    # Create PID directory
    mkdir -p /var/run/dplaneos
    
    # Start main daemon
    if [ -f "$DPLANEOS_DIR/daemon/dplaneos-daemon" ]; then
        log "Starting D-PlaneOS daemon..."
        nohup "$DPLANEOS_DIR/daemon/dplaneos-daemon" \
            >> /var/log/dplaneos/daemon.log 2>&1 &
        echo $! > /var/run/dplaneos/daemon.pid
        log "Daemon started with PID $(cat /var/run/dplaneos/daemon.pid)"
    else
        error "Daemon binary not found at $DPLANEOS_DIR/daemon/dplaneos-daemon"
        exit 1
    fi
    
    # Start realtime service (if exists)
    if [ -f "$DPLANEOS_DIR/daemon/dplaneos-realtime" ]; then
        log "Starting realtime service..."
        nohup "$DPLANEOS_DIR/daemon/dplaneos-realtime" \
            >> /var/log/dplaneos/realtime.log 2>&1 &
        echo $! > /var/run/dplaneos/realtime.pid
        log "Realtime service started with PID $(cat /var/run/dplaneos/realtime.pid)"
    fi
    
    # Create systemd compatibility script
    create_init_script
}

# Create sysvinit-style init script
create_init_script() {
    log "Creating init.d script..."
    
    cat > /etc/init.d/dplaneos << 'EOF'
#!/bin/bash
### BEGIN INIT INFO
# Provides:          dplaneos
# Required-Start:    $network $remote_fs
# Required-Stop:     $network $remote_fs
# Default-Start:     2 3 4 5
# Default-Stop:      0 1 6
# Short-Description: D-PlaneOS NAS Services
### END INIT INFO

DAEMON_PATH="/opt/dplaneos/daemon/dplaneos-daemon"
REALTIME_PATH="/opt/dplaneos/daemon/dplaneos-realtime"
PID_DIR="/var/run/dplaneos"
DAEMON_PID="$PID_DIR/daemon.pid"
REALTIME_PID="$PID_DIR/realtime.pid"

start() {
    echo "Starting D-PlaneOS services..."
    mkdir -p "$PID_DIR"
    
    # Start daemon
    if [ -f "$DAEMON_PATH" ]; then
        nohup "$DAEMON_PATH" >> /var/log/dplaneos/daemon.log 2>&1 &
        echo $! > "$DAEMON_PID"
        echo "Daemon started (PID $(cat $DAEMON_PID))"
    fi
    
    # Start realtime
    if [ -f "$REALTIME_PATH" ]; then
        nohup "$REALTIME_PATH" >> /var/log/dplaneos/realtime.log 2>&1 &
        echo $! > "$REALTIME_PID"
        echo "Realtime service started (PID $(cat $REALTIME_PID))"
    fi
}

stop() {
    echo "Stopping D-PlaneOS services..."
    
    # Stop daemon
    if [ -f "$DAEMON_PID" ]; then
        kill $(cat "$DAEMON_PID") 2>/dev/null || true
        rm -f "$DAEMON_PID"
    fi
    
    # Stop realtime
    if [ -f "$REALTIME_PID" ]; then
        kill $(cat "$REALTIME_PID") 2>/dev/null || true
        rm -f "$REALTIME_PID"
    fi
    
    echo "Services stopped"
}

status() {
    echo "D-PlaneOS Service Status:"
    
    if [ -f "$DAEMON_PID" ] && kill -0 $(cat "$DAEMON_PID") 2>/dev/null; then
        echo "  Daemon: Running (PID $(cat $DAEMON_PID))"
    else
        echo "  Daemon: Stopped"
    fi
    
    if [ -f "$REALTIME_PID" ] && kill -0 $(cat "$REALTIME_PID") 2>/dev/null; then
        echo "  Realtime: Running (PID $(cat $REALTIME_PID))"
    else
        echo "  Realtime: Stopped"
    fi
}

case "$1" in
    start)
        start
        ;;
    stop)
        stop
        ;;
    restart)
        stop
        sleep 2
        start
        ;;
    status)
        status
        ;;
    *)
        echo "Usage: $0 {start|stop|restart|status}"
        exit 1
        ;;
esac

exit 0
EOF

    chmod +x /etc/init.d/dplaneos
    
    # Add to startup (if update-rc.d available)
    if command -v update-rc.d &> /dev/null; then
        update-rc.d dplaneos defaults
        log "Init script installed"
    elif command -v chkconfig &> /dev/null; then
        chkconfig --add dplaneos
        log "Init script installed (chkconfig)"
    fi
}

# Check service ports
check_ports() {
    log "Checking service ports..."
    
    # Main daemon port (default 8080)
    if netstat -tuln 2>/dev/null | grep -q ":8080 "; then
        warn "Port 8080 is already in use"
        warn "D-PlaneOS daemon may fail to start"
        return 1
    fi
    
    # Realtime service port (default 8081)
    if netstat -tuln 2>/dev/null | grep -q ":8081 "; then
        warn "Port 8081 is already in use"
        warn "Realtime service may fail to start"
        return 1
    fi
    
    log "Ports available"
    return 0
}

# Verify services are running
verify_services() {
    log "Verifying services..."
    
    sleep 3
    
    # Check daemon
    if curl -s http://localhost:8080/api/health &> /dev/null; then
        log "Daemon is responding"
    else
        error "Daemon not responding on port 8080"
        return 1
    fi
    
    # Check realtime (if exists)
    if [ -f "$DPLANEOS_DIR/daemon/dplaneos-realtime" ]; then
        if curl -s http://localhost:8081/health &> /dev/null; then
            log "Realtime service is responding"
        else
            warn "Realtime service not responding on port 8081"
            warn "Dashboard graphs may not update"
        fi
    fi
    
    return 0
}

# Main execution
main() {
    log "D-PlaneOS Startup - Checking environment..."
    
    # Check ports first
    if ! check_ports; then
        error "Port conflicts detected!"
        error "Please free ports 8080 and 8081, then retry"
        exit 1
    fi
    
    # Check systemd
    if check_systemd; then
        log "Systemd detected - using systemd units"
        start_with_systemd
    else
        warn "Systemd not available - using fallback method"
        start_without_systemd
    fi
    
    # Verify
    if verify_services; then
        log "All services started successfully"
        log "Access D-PlaneOS at: http://$(hostname -I | awk '{print $1}'):8080"
    else
        error "Service verification failed"
        exit 1
    fi
}

# Run main
main "$@"
