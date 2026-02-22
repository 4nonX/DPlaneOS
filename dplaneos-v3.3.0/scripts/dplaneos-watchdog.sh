#!/bin/bash
#
# D-PlaneOS - Daemon Watchdog & Health Monitor
# 
# Purpose: Monitor Go daemon, auto-restart on failure, alert on issues
# Install: /usr/local/bin/dplaneos-watchdog.sh
# Cron: */5 * * * * /usr/local/bin/dplaneos-watchdog.sh
#
# Features:
# - Health checks (daemon responsive, socket exists, memory usage)
# - Auto-restart on failure
# - Email/webhook alerts
# - Performance metrics
# - Resource limits enforcement
#

set -euo pipefail

# Configuration
DAEMON_NAME="dplaned"
SOCKET_PATH="/var/run/dplaneos.sock"
MAX_MEMORY_MB=200  # Alert if daemon uses >200MB
MAX_RESTART_PER_HOUR=3
LOG_FILE="/var/log/dplaneos-watchdog.log"
ALERT_EMAIL="${DPLANEOS_ALERT_EMAIL:-}"
ALERT_WEBHOOK="${DPLANEOS_ALERT_WEBHOOK:-}"

# State file
STATE_FILE="/var/lib/dplaneos/watchdog-state"
mkdir -p "$(dirname "$STATE_FILE")"

# Colors for console output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m'

# Logging function
log() {
    echo "[$(date '+%Y-%m-%d %H:%M:%S')] $1" | tee -a "$LOG_FILE"
}

# Alert function
alert() {
    local severity=$1
    local message=$2
    
    log "$severity: $message"
    
    # Email alert
    if [ -n "$ALERT_EMAIL" ]; then
        echo "$message" | mail -s "[D-PlaneOS] $severity: Daemon Issue" "$ALERT_EMAIL" 2>/dev/null || true
    fi
    
    # Webhook alert (Slack/Discord/etc)
    if [ -n "$ALERT_WEBHOOK" ]; then
        curl -X POST "$ALERT_WEBHOOK" \
            -H "Content-Type: application/json" \
            -d "{\"text\":\"$severity: $message\"}" \
            2>/dev/null || true
    fi
}

# Get restart count in last hour
get_restart_count() {
    if [ ! -f "$STATE_FILE" ]; then
        echo "0"
        return
    fi
    
    local count=0
    local one_hour_ago=$(date -d "1 hour ago" +%s)
    
    while IFS='|' read -r timestamp; do
        if [ "$timestamp" -gt "$one_hour_ago" ]; then
            ((count++))
        fi
    done < "$STATE_FILE"
    
    echo "$count"
}

# Record restart
record_restart() {
    echo "$(date +%s)|restart" >> "$STATE_FILE"
    
    # Clean old entries (older than 24h)
    local day_ago=$(date -d "1 day ago" +%s)
    grep -v "^[0-9]*|" "$STATE_FILE" > "${STATE_FILE}.tmp" 2>/dev/null || true
    awk -F'|' -v cutoff="$day_ago" '$1 > cutoff' "$STATE_FILE" > "${STATE_FILE}.tmp" 2>/dev/null || true
    mv "${STATE_FILE}.tmp" "$STATE_FILE" 2>/dev/null || true
}

# Check 1: Is daemon process running?
check_process() {
    if systemctl is-active --quiet "$DAEMON_NAME"; then
        return 0
    else
        return 1
    fi
}

# Check 2: Does socket exist and is accessible?
check_socket() {
    if [ -S "$SOCKET_PATH" ]; then
        # Try to connect to socket
        if timeout 2 bash -c "echo '' | socat - UNIX-CONNECT:$SOCKET_PATH" 2>/dev/null; then
            return 0
        fi
    fi
    return 1
}

# Check 3: Memory usage reasonable?
check_memory() {
    local pid=$(pgrep -x "$DAEMON_NAME" | head -1)
    if [ -z "$pid" ]; then
        return 1
    fi
    
    # Get memory usage in MB
    local mem_mb=$(ps -p "$pid" -o rss= | awk '{print int($1/1024)}')
    
    if [ "$mem_mb" -gt "$MAX_MEMORY_MB" ]; then
        alert "WARNING" "Daemon using ${mem_mb}MB (limit: ${MAX_MEMORY_MB}MB)"
        return 2  # Warning, but not fatal
    fi
    
    return 0
}

# Check 4: Can daemon execute test command?
check_responsiveness() {
    # Try to get daemon version or simple command
    local response=$(echo '{"action":"version","args":[]}' | \
        socat - UNIX-CONNECT:"$SOCKET_PATH" 2>/dev/null || echo "")
    
    if [ -z "$response" ]; then
        return 1
    fi
    
    # Check if response is valid JSON
    if echo "$response" | jq empty 2>/dev/null; then
        return 0
    else
        return 1
    fi
}

# Restart daemon
restart_daemon() {
    local restart_count=$(get_restart_count)
    
    if [ "$restart_count" -ge "$MAX_RESTART_PER_HOUR" ]; then
        alert "CRITICAL" "Daemon restart limit reached ($restart_count/$MAX_RESTART_PER_HOUR per hour). Manual intervention required."
        log "ERROR: Daemon crashlooping, not restarting automatically"
        return 1
    fi
    
    log "Attempting to restart daemon (restart count: $restart_count/$MAX_RESTART_PER_HOUR)"
    
    # Stop daemon
    systemctl stop "$DAEMON_NAME" 2>/dev/null || true
    
    # Wait for socket cleanup
    sleep 2
    
    # Remove stale socket
    rm -f "$SOCKET_PATH"
    
    # Start daemon
    if systemctl start "$DAEMON_NAME"; then
        record_restart
        log "Daemon restarted successfully"
        alert "INFO" "Daemon auto-restarted (count: $((restart_count + 1))/$MAX_RESTART_PER_HOUR)"
        
        # Wait for socket to appear
        local retries=10
        while [ $retries -gt 0 ]; do
            if [ -S "$SOCKET_PATH" ]; then
                log "Socket available after restart"
                return 0
            fi
            sleep 1
            ((retries--))
        done
        
        alert "WARNING" "Daemon started but socket not available"
        return 1
    else
        alert "CRITICAL" "Failed to restart daemon"
        return 1
    fi
}

# Collect performance metrics
collect_metrics() {
    local pid=$(pgrep -x "$DAEMON_NAME" | head -1)
    if [ -z "$pid" ]; then
        return
    fi
    
    # Get metrics
    local cpu=$(ps -p "$pid" -o %cpu= | tr -d ' ')
    local mem_mb=$(ps -p "$pid" -o rss= | awk '{print int($1/1024)}')
    local uptime=$(ps -p "$pid" -o etimes= | tr -d ' ')
    
    # Log metrics
    log "METRICS: CPU=${cpu}% MEM=${mem_mb}MB UPTIME=${uptime}s"
    
    # Store metrics for trending (last 24 hours)
    echo "$(date +%s)|${cpu}|${mem_mb}|${uptime}" >> /var/lib/dplaneos/metrics.log
    
    # Keep only last 24 hours of metrics
    local day_ago=$(date -d "1 day ago" +%s)
    awk -F'|' -v cutoff="$day_ago" '$1 > cutoff' /var/lib/dplaneos/metrics.log > /var/lib/dplaneos/metrics.log.tmp 2>/dev/null || true
    mv /var/lib/dplaneos/metrics.log.tmp /var/lib/dplaneos/metrics.log 2>/dev/null || true
}

# Main health check
main() {
    log "Starting health check"
    
    local issues=0
    
    # Check 1: Process running?
    if ! check_process; then
        log "ERROR: Daemon process not running"
        ((issues++))
        restart_daemon || exit 1
    fi
    
    # Check 2: Socket exists?
    if ! check_socket; then
        log "ERROR: Daemon socket not responding"
        ((issues++))
        restart_daemon || exit 1
    fi
    
    # Check 3: Memory usage OK?
    local mem_result=0
    check_memory || mem_result=$?
    if [ $mem_result -eq 1 ]; then
        log "ERROR: Cannot get memory usage"
        ((issues++))
    elif [ $mem_result -eq 2 ]; then
        # Warning logged by check_memory, but not fatal
        :
    fi
    
    # Check 4: Daemon responsive?
    if ! check_responsiveness; then
        log "ERROR: Daemon not responding to commands"
        ((issues++))
        restart_daemon || exit 1
    fi
    
    # Collect metrics if daemon is healthy
    if [ $issues -eq 0 ]; then
        collect_metrics
        log "Health check PASSED"
    else
        log "Health check FAILED ($issues issues)"
    fi
}

# Run main check
main

exit 0
