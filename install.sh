#!/usr/bin/env bash
# DPlaneOS Linux installer
#
# Deploys the daemon binary, web UI, and nginx config on a Linux host.
# Used by CI (fleet-integration-test.sh) and by users installing outside NixOS.
# For the NixOS appliance installer, see nixos/install.sh.
#
# Usage:
#   sudo bash install.sh [--unattended] [--port PORT] [--db-dsn DSN] [--upgrade]
#
# Flags:
#   --port PORT       nginx listen port (default: 80)
#   --db-dsn DSN      PostgreSQL connection string (required)
#   --upgrade         update an existing installation in place
#   --unattended      skip confirmation prompts

set -euo pipefail

# ---------- defaults ----------------------------------------------------------
PORT=80
DB_DSN="${DATABASE_DSN:-}"
UPGRADE=false
UNATTENDED=false
DAEMON_INTERNAL_PORT=9000

# ---------- argument parsing --------------------------------------------------
while [[ $# -gt 0 ]]; do
    case "$1" in
        --port)       PORT="$2"; shift 2 ;;
        --db-dsn)     DB_DSN="$2"; shift 2 ;;
        --upgrade)    UPGRADE=true; shift ;;
        --unattended) UNATTENDED=true; shift ;;
        *) echo "Unknown argument: $1" >&2; exit 1 ;;
    esac
done

if [[ -z "$DB_DSN" ]]; then
    echo "ERROR: --db-dsn is required (or set DATABASE_DSN)" >&2
    exit 1
fi

# ---------- paths -------------------------------------------------------------
INSTALL_ROOT="/opt/dplaneos"
DAEMON_BIN="$INSTALL_ROOT/daemon/dplaned"
APP_DIR="$INSTALL_ROOT/app"
VERSION_FILE="$INSTALL_ROOT/VERSION"
LOG_DIR="/var/log/dplaneos"
STATE_DIR="/var/lib/dplaneos"
RUN_DIR="/run/dplaneos"
PID_FILE="$RUN_DIR/dplaned.pid"
SMB_CONF="$STATE_DIR/smb-shares.conf"
NGINX_AVAIL="/etc/nginx/sites-available/dplaneos"
NGINX_ENABLED="/etc/nginx/sites-enabled/dplaneos"

# ---------- helpers -----------------------------------------------------------
log()  { echo "[install] $*"; }
die()  { echo "ERROR: $*" >&2; exit 1; }

write_nginx_conf() {
    local port="$1"
    mkdir -p /etc/nginx/sites-available /etc/nginx/sites-enabled
    cat > "$NGINX_AVAIL" <<EOF
server {
    listen ${port} default_server;
    listen [::]:${port} default_server;
    server_name _;

    root ${APP_DIR};
    index index.html;

    add_header X-Frame-Options "SAMEORIGIN" always;
    add_header X-Content-Type-Options "nosniff" always;

    location / {
        try_files \$uri \$uri/ /index.html;
    }

    location /api/ {
        proxy_pass http://127.0.0.1:${DAEMON_INTERNAL_PORT};
        proxy_http_version 1.1;
        proxy_set_header Host \$host;
        proxy_set_header X-Real-IP \$remote_addr;
        proxy_set_header X-Forwarded-For \$proxy_add_x_forwarded_for;
        proxy_read_timeout 300s;
    }

    location /ws/ {
        proxy_pass http://127.0.0.1:${DAEMON_INTERNAL_PORT};
        proxy_http_version 1.1;
        proxy_set_header Upgrade \$http_upgrade;
        proxy_set_header Connection "upgrade";
        proxy_read_timeout 7d;
    }

    location /health {
        proxy_pass http://127.0.0.1:${DAEMON_INTERNAL_PORT}/health;
    }
}
EOF
    ln -sf "$NGINX_AVAIL" "$NGINX_ENABLED"
    rm -f /etc/nginx/sites-enabled/default 2>/dev/null || true
}

start_daemon() {
    mkdir -p "$RUN_DIR" "$LOG_DIR"
    touch "$SMB_CONF"
    DATABASE_DSN="$DB_DSN" nohup "$DAEMON_BIN" \
        -db-dsn "$DB_DSN" \
        -listen "127.0.0.1:${DAEMON_INTERNAL_PORT}" \
        -smb-conf "$SMB_CONF" \
        >> "$LOG_DIR/dplaned.log" 2>&1 &
    echo $! > "$PID_FILE"
    log "Daemon started (PID $!)"
}

stop_daemon() {
    if [[ -f "$PID_FILE" ]]; then
        local pid
        pid=$(cat "$PID_FILE")
        kill "$pid" 2>/dev/null || true
        rm -f "$PID_FILE"
    fi
    pkill -f "dplaned.*-listen" 2>/dev/null || true
    sleep 1
}

reload_nginx() {
    if systemctl is-active nginx &>/dev/null; then
        nginx -t && systemctl reload nginx
    elif [[ -f /run/nginx.pid ]]; then
        nginx -t && nginx -s reload
    else
        nginx -t && nginx
    fi
}

# ---------- upgrade path ------------------------------------------------------
if $UPGRADE; then
    log "Upgrading: switching nginx to port $PORT"
    write_nginx_conf "$PORT"
    reload_nginx
    stop_daemon
    start_daemon
    log "Upgrade complete"
    exit 0
fi

# ---------- fresh install -----------------------------------------------------
log "Installing DPlaneOS (port $PORT)..."

# Binary
mkdir -p "$INSTALL_ROOT/daemon"
if [[ -f "./dplaned-ci" ]]; then
    cp ./dplaned-ci "$DAEMON_BIN"
elif [[ -f "./build/dplaned-linux-amd64" ]]; then
    cp ./build/dplaned-linux-amd64 "$DAEMON_BIN"
elif [[ -f "./build/dplaned-linux-arm64" ]]; then
    cp ./build/dplaned-linux-arm64 "$DAEMON_BIN"
else
    die "daemon binary not found (expected ./dplaned-ci or ./build/dplaned-linux-*)"
fi
chmod +x "$DAEMON_BIN"
log "Daemon binary installed: $DAEMON_BIN"

# Web UI
mkdir -p "$APP_DIR"
if [[ -d "./app" ]]; then
    cp -r ./app/. "$APP_DIR/"
    log "Web UI installed: $APP_DIR"
fi

# Version file
if [[ -f "./VERSION" ]]; then
    cp ./VERSION "$VERSION_FILE"
fi

# State dirs
mkdir -p "$LOG_DIR" "$STATE_DIR" "$RUN_DIR"
touch "$SMB_CONF"

# nginx
write_nginx_conf "$PORT"
reload_nginx
log "nginx configured (port $PORT)"

# Daemon
start_daemon

log "Installation complete"
