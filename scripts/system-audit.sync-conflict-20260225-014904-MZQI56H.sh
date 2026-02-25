#!/bin/bash
#
# D-PlaneOS System Audit
#
# Full system audit and report for installed D-PlaneOS instances.
# Audits hardware, OS, services, daemon, ZFS, Docker, security,
# database, network, and performance. Produces a timestamped report.
#
# Usage:
#   sudo ./system-audit.sh                  # full audit, report to stdout + file
#   sudo ./system-audit.sh --out /tmp/      # custom output directory
#   sudo ./system-audit.sh --quick          # skip slow checks (S.M.A.R.T, scrub status)
#
# Output: /var/log/dplaneos/audit-YYYYMMDD-HHMMSS.txt
#

set -euo pipefail

# ── Arguments ─────────────────────────────────────────────────────────────────
OUTDIR="/var/log/dplaneos"
QUICK=false

while [[ $# -gt 0 ]]; do
    case "$1" in
        --out)      OUTDIR="$2"; shift 2 ;;
        --quick)    QUICK=true; shift ;;
        --help|-h)
            echo "Usage: sudo $0 [--out DIR] [--quick]"
            exit 0 ;;
        *) echo "Unknown option: $1"; exit 1 ;;
    esac
done

[ "$EUID" -eq 0 ] || { echo "Must run as root: sudo $0"; exit 1; }

# ── Setup ─────────────────────────────────────────────────────────────────────
TIMESTAMP=$(date +%Y%m%d-%H%M%S)
mkdir -p "$OUTDIR"
REPORT="$OUTDIR/audit-${TIMESTAMP}.txt"

# Tee everything to file and stdout
exec > >(tee -a "$REPORT") 2>&1

# ── Colours ───────────────────────────────────────────────────────────────────
RED='\033[0;31m'; GREEN='\033[0;32m'; YELLOW='\033[1;33m'
BLUE='\033[0;34m'; BOLD='\033[1m'; CYAN='\033[0;36m'; NC='\033[0m'

PASS=0; WARN=0; FAIL=0

pass() { echo -e "  ${GREEN}✓${NC} $1"; PASS=$((PASS+1)); }
warn() { echo -e "  ${YELLOW}⚠${NC} $1"; WARN=$((WARN+1)); }
fail() { echo -e "  ${RED}✗${NC} $1"; FAIL=$((FAIL+1)); }
info() { echo -e "  ${BLUE}ℹ${NC} $1"; }
section() {
    echo ""
    echo -e "${BOLD}${CYAN}━━━ $1 ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
}

# ── Header ────────────────────────────────────────────────────────────────────
clear
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo -e "${BOLD}    D-PlaneOS System Audit${NC}"
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo "    Host     : $(hostname -f 2>/dev/null || hostname)"
echo "    Date     : $(date)"
echo "    Kernel   : $(uname -r)"
echo "    Uptime   : $(uptime -p 2>/dev/null || uptime)"
echo "    Report   : $REPORT"
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"

# ════════════════════════════════════════════════════════════════
section "1. HARDWARE"
# ════════════════════════════════════════════════════════════════

# CPU
CPU_MODEL=$(grep -m1 "model name" /proc/cpuinfo | cut -d: -f2 | xargs || echo "unknown")
CPU_CORES=$(nproc)
CPU_THREADS=$(grep -c "^processor" /proc/cpuinfo)
info "CPU      : $CPU_MODEL"
info "Cores    : $CPU_CORES physical / $CPU_THREADS logical"

# CPU frequency scaling
if [ -f /sys/devices/system/cpu/cpu0/cpufreq/scaling_governor ]; then
    GOV=$(cat /sys/devices/system/cpu/cpu0/cpufreq/scaling_governor)
    if [ "$GOV" = "performance" ] || [ "$GOV" = "ondemand" ]; then
        pass "CPU governor: $GOV"
    else
        warn "CPU governor: $GOV (consider 'performance' or 'ondemand' for NAS workloads)"
    fi
fi

# RAM
TOTAL_RAM_MB=$(free -m | awk '/^Mem:/{print $2}')
AVAIL_RAM_MB=$(free -m | awk '/^Mem:/{print $7}')
USED_RAM_MB=$((TOTAL_RAM_MB - AVAIL_RAM_MB))
RAM_PCT=$((USED_RAM_MB * 100 / TOTAL_RAM_MB))
info "RAM total: ${TOTAL_RAM_MB}MB"
info "RAM used : ${USED_RAM_MB}MB (${RAM_PCT}%)"

if [ "$TOTAL_RAM_MB" -ge 8192 ]; then
    pass "RAM: ${TOTAL_RAM_MB}MB (sufficient for ZFS ARC)"
elif [ "$TOTAL_RAM_MB" -ge 4096 ]; then
    warn "RAM: ${TOTAL_RAM_MB}MB (minimum; 8GB+ recommended for large ZFS datasets)"
else
    fail "RAM: ${TOTAL_RAM_MB}MB (insufficient; ZFS may behave erratically under 4GB)"
fi

if [ "$RAM_PCT" -ge 90 ]; then
    fail  "RAM usage at ${RAM_PCT}% — system under memory pressure"
elif [ "$RAM_PCT" -ge 75 ]; then
    warn "RAM usage at ${RAM_PCT}%"
else
    pass "RAM usage: ${RAM_PCT}%"
fi

# ECC
if dmidecode -t memory 2>/dev/null | grep -qi "error correction.*ecc\|error correction.*single"; then
    pass "ECC RAM detected"
elif dmidecode -t memory 2>/dev/null | grep -qi "error correction.*none"; then
    warn "Non-ECC RAM — ZFS bit-rot detection works but silent corruption is possible on hardware faults"
else
    info "ECC status: could not determine (dmidecode unavailable or no SMBIOS data)"
fi

# Swap
SWAP_TOTAL=$(free -m | awk '/^Swap:/{print $2}')
SWAP_USED=$(free -m | awk '/^Swap:/{print $3}')
if [ "$SWAP_TOTAL" -eq 0 ]; then
    warn "No swap configured — OOM killer will target processes directly"
elif [ "$SWAP_USED" -gt 0 ]; then
    warn "Swap in use: ${SWAP_USED}MB — system is under memory pressure"
else
    pass "Swap: ${SWAP_TOTAL}MB configured, not in use"
fi

# Disk inventory
section "1b. BLOCK DEVICES"
lsblk -o NAME,SIZE,TYPE,ROTA,TRAN,MODEL --noheadings 2>/dev/null | while IFS= read -r line; do
    info "$line"
done

# NVMe
NVME_COUNT=$(find /dev -name "nvme*n*" -not -name "*p*" 2>/dev/null | wc -l)
HDD_COUNT=$(lsblk -d -o ROTA --noheadings 2>/dev/null | grep -c "^1" || echo 0)
SSD_COUNT=$(lsblk -d -o ROTA,TRAN --noheadings 2>/dev/null | grep "^0" | grep -vc "nvme" || echo 0)
info "NVMe: $NVME_COUNT  SSD: $SSD_COUNT  HDD: $HDD_COUNT"

# S.M.A.R.T. status (skip in quick mode — can be slow)
if ! $QUICK && command -v smartctl &>/dev/null; then
    section "1c. S.M.A.R.T. HEALTH"
    SMART_FAIL=0
    for dev in $(lsblk -d -o NAME --noheadings | grep -E "^(sd|nvme)" | sed 's/^/\/dev\//'); do
        SMART_OUT=$(smartctl -H "$dev" 2>/dev/null | grep -E "overall-health|SMART Health" || echo "FAILED")
        if echo "$SMART_OUT" | grep -q "PASSED\|OK"; then
            pass "$(basename $dev): SMART PASSED"
        elif echo "$SMART_OUT" | grep -q "FAILED"; then
            fail "$(basename $dev): SMART FAILED — drive may be failing"
            SMART_FAIL=$((SMART_FAIL+1))
        else
            warn "$(basename $dev): SMART status unknown"
        fi
    done
else
    $QUICK && info "S.M.A.R.T. skipped (--quick mode)"
fi

# ════════════════════════════════════════════════════════════════
section "2. OPERATING SYSTEM"
# ════════════════════════════════════════════════════════════════

. /etc/os-release 2>/dev/null || true
info "OS       : ${PRETTY_NAME:-unknown}"
info "Kernel   : $(uname -r)"
info "Arch     : $(uname -m)"

# OS support check
case "${ID,,}" in
    ubuntu)
        VER_MAJOR="${VERSION_ID%%.*}"
        if [ "$VER_MAJOR" -ge 22 ]; then
            pass "Ubuntu ${VERSION_ID} — supported"
        else
            warn "Ubuntu ${VERSION_ID} — minimum is 20.04, 22.04+ recommended"
        fi ;;
    debian)
        if [ "${VERSION_ID:-0}" -ge 11 ]; then
            pass "Debian ${VERSION_ID} — supported"
        else
            warn "Debian ${VERSION_ID} — minimum is 11 (Bullseye)"
        fi ;;
    nixos)
        pass "NixOS — supported" ;;
    *)
        warn "OS '${ID}' is not in the supported list (Debian/Ubuntu/NixOS)" ;;
esac

# Pending updates
PENDING=$(apt-get -s upgrade 2>/dev/null | grep -c "^Inst" || echo 0)
if [ "$PENDING" -eq 0 ]; then
    pass "System packages up to date"
elif [ "$PENDING" -le 10 ]; then
    warn "$PENDING pending updates"
else
    warn "$PENDING pending updates — consider running: apt-get upgrade"
fi

# Security updates specifically
SEC_PENDING=$(apt-get -s upgrade 2>/dev/null | grep -c "^Inst.*security" || echo 0)
if [ "$SEC_PENDING" -gt 0 ]; then
    fail "$SEC_PENDING pending SECURITY updates — apply immediately"
fi

# Kernel tuning (applied by install.sh)
INOTIFY=$(sysctl -n fs.inotify.max_user_watches 2>/dev/null || echo 0)
if [ "$INOTIFY" -ge 524288 ]; then
    pass "inotify.max_user_watches: $INOTIFY"
else
    warn "inotify.max_user_watches: $INOTIFY (install.sh should have set 524288)"
fi

SWAPPINESS=$(sysctl -n vm.swappiness 2>/dev/null || echo 60)
if [ "$SWAPPINESS" -le 10 ]; then
    pass "vm.swappiness: $SWAPPINESS (NAS-optimised)"
else
    warn "vm.swappiness: $SWAPPINESS (recommend ≤10 for ZFS workloads)"
fi

# ════════════════════════════════════════════════════════════════
section "3. D-PLANEOS INSTALLATION"
# ════════════════════════════════════════════════════════════════

INSTALL_DIR="/opt/dplaneos"
DB_PATH="/var/lib/dplaneos/dplaneos.db"

# Install dir
if [ -d "$INSTALL_DIR" ]; then
    pass "Install directory: $INSTALL_DIR"
else
    fail "Install directory missing: $INSTALL_DIR"
fi

# Version
if [ -f "$INSTALL_DIR/VERSION" ]; then
    INSTALLED_VER=$(cat "$INSTALL_DIR/VERSION")
    pass "Version: $INSTALLED_VER"
else
    warn "VERSION file not found"
    INSTALLED_VER="unknown"
fi

# Binary
DAEMON_BIN="$INSTALL_DIR/daemon/dplaned"
if [ -f "$DAEMON_BIN" ]; then
    BIN_SIZE=$(du -sh "$DAEMON_BIN" | cut -f1)
    pass "Daemon binary present ($BIN_SIZE)"
    if [ -x "$DAEMON_BIN" ]; then
        pass "Daemon binary executable"
    else
        fail "Daemon binary not executable"
    fi
else
    fail "Daemon binary missing: $DAEMON_BIN"
fi

# App directory
if [ -d "$INSTALL_DIR/app" ]; then
    APP_FILES=$(find "$INSTALL_DIR/app" -type f | wc -l)
    pass "App directory: $APP_FILES files"
else
    fail "App directory missing"
fi

# Ownership
APP_OWNER=$(stat -c '%U' "$INSTALL_DIR/app" 2>/dev/null || echo "unknown")
if [ "$APP_OWNER" = "www-data" ]; then
    pass "App ownership: www-data"
else
    warn "App ownership: $APP_OWNER (expected www-data)"
fi

# sudoers
if [ -f /etc/sudoers.d/dplaneos ]; then
    SUDOERS_PERMS=$(stat -c '%a' /etc/sudoers.d/dplaneos)
    if [ "$SUDOERS_PERMS" = "440" ]; then
        pass "sudoers: /etc/sudoers.d/dplaneos (440)"
    else
        fail "sudoers permissions: $SUDOERS_PERMS (must be 440)"
    fi
    visudo -c -f /etc/sudoers.d/dplaneos &>/dev/null \
        && pass "sudoers syntax valid" \
        || fail "sudoers syntax invalid"
else
    warn "sudoers file not found — some ZFS operations may require manual sudo"
fi

# ════════════════════════════════════════════════════════════════
section "4. SERVICES"
# ════════════════════════════════════════════════════════════════

check_service() {
    local svc="$1"
    local label="${2:-$1}"
    if systemctl is-active "$svc" &>/dev/null; then
        ENABLED=$(systemctl is-enabled "$svc" 2>/dev/null || echo "unknown")
        pass "$label: active (enabled: $ENABLED)"
        return 0
    else
        STATUS=$(systemctl is-active "$svc" 2>/dev/null || echo "unknown")
        fail "$label: $STATUS"
        info "  diagnose: journalctl -xe -u $svc"
        return 1
    fi
}

check_service nginx        "nginx (reverse proxy)"
check_service dplaned      "dplaned (Go daemon)"

# ZFS gate service
if systemctl list-units --full --all 2>/dev/null | grep -q "dplaneos-zfs-mount-wait"; then
    check_service dplaneos-zfs-mount-wait "ZFS mount gate"
else
    warn "ZFS mount-wait gate not installed (Docker/ZFS race condition possible on boot)"
fi

# Docker (optional)
if systemctl list-units --full --all 2>/dev/null | grep -q "docker.service"; then
    check_service docker "Docker"
fi

# Daemon memory usage
DPLANED_PID=$(systemctl show dplaned --property=MainPID --value 2>/dev/null || echo "")
if [ -n "$DPLANED_PID" ] && [ "$DPLANED_PID" != "0" ] && [ -f "/proc/$DPLANED_PID/status" ]; then
    DAEMON_RSS_KB=$(grep VmRSS /proc/$DPLANED_PID/status | awk '{print $2}')
    DAEMON_RSS_MB=$((DAEMON_RSS_KB / 1024))
    if [ "$DAEMON_RSS_MB" -lt 256 ]; then
        pass "Daemon memory: ${DAEMON_RSS_MB}MB"
    elif [ "$DAEMON_RSS_MB" -lt 400 ]; then
        warn "Daemon memory: ${DAEMON_RSS_MB}MB (approaching MemoryHigh=384M)"
    else
        fail "Daemon memory: ${DAEMON_RSS_MB}MB (exceeds MemoryHigh threshold)"
    fi
fi

# ════════════════════════════════════════════════════════════════
section "5. DAEMON API"
# ════════════════════════════════════════════════════════════════

API="http://127.0.0.1:9000"

# Health
HEALTH=$(curl -sf --max-time 5 "$API/health" 2>/dev/null || echo "")
if echo "$HEALTH" | grep -q '"ok"'; then
    DAEMON_VER=$(echo "$HEALTH" | python3 -c "import sys,json; d=json.load(sys.stdin); print(d.get('version','unknown'))" 2>/dev/null || echo "unknown")
    pass "Health endpoint: OK (version: $DAEMON_VER)"
    if [ "$DAEMON_VER" != "$INSTALLED_VER" ] && [ "$INSTALLED_VER" != "unknown" ]; then
        warn "Version mismatch: binary reports $DAEMON_VER, VERSION file says $INSTALLED_VER"
    fi
else
    fail "Health endpoint not responding — is dplaned running?"
    info "  check: systemctl status dplaned"
    info "  logs:  journalctl -xe -u dplaned"
fi

# CSRF
CSRF_RESP=$(curl -sf --max-time 5 "$API/api/csrf" 2>/dev/null || echo "")
if echo "$CSRF_RESP" | grep -q "csrf_token"; then
    pass "CSRF endpoint: OK"
    CSRF_TOKEN=$(echo "$CSRF_RESP" | python3 -c "import sys,json; print(json.load(sys.stdin)['csrf_token'])" 2>/dev/null || echo "")
else
    fail "CSRF endpoint not responding"
    CSRF_TOKEN=""
fi

# Auth check (unauthenticated — expect 401)
AUTH_CODE=$(curl -sf --max-time 5 -o /dev/null -w "%{http_code}" \
    "$API/api/auth/check" 2>/dev/null || echo "000")
if [ "$AUTH_CODE" = "401" ]; then
    pass "Auth middleware: returning 401 on unauthenticated request"
else
    warn "Auth middleware: expected 401, got $AUTH_CODE"
fi

# WebSocket port reachable (TCP only, no full WS handshake)
if curl -sf --max-time 3 -o /dev/null "$API/health" &>/dev/null; then
    pass "Daemon port 9000: reachable from localhost"
fi

# Port 9000 NOT exposed externally (should only be localhost)
if ss -tuln 2>/dev/null | grep ":9000" | grep -q "0\.0\.0\.0\|::"; then
    fail "Port 9000 is bound to 0.0.0.0 or :: — daemon is directly internet-exposed"
    info "  It should only listen on 127.0.0.1:9000 (nginx proxies it)"
elif ss -tuln 2>/dev/null | grep -q "127.0.0.1:9000"; then
    pass "Port 9000: bound to 127.0.0.1 only (correct)"
fi

# ════════════════════════════════════════════════════════════════
section "6. DATABASE"
# ════════════════════════════════════════════════════════════════

if [ ! -f "$DB_PATH" ]; then
    fail "Database not found: $DB_PATH"
else
    DB_SIZE=$(du -sh "$DB_PATH" | cut -f1)
    pass "Database: $DB_PATH ($DB_SIZE)"

    DB_PERMS=$(stat -c '%a' "$DB_PATH")
    if [ "$DB_PERMS" = "600" ]; then
        pass "Database permissions: 600"
    else
        warn "Database permissions: $DB_PERMS (should be 600)"
    fi

    WAL=$(sqlite3 "$DB_PATH" "PRAGMA journal_mode;" 2>/dev/null || echo "error")
    if [ "$WAL" = "wal" ]; then
        pass "WAL mode enabled"
    else
        fail "WAL mode: $WAL (should be wal)"
    fi

    FK=$(sqlite3 "$DB_PATH" "PRAGMA foreign_keys;" 2>/dev/null || echo "0")
    [ "$FK" = "1" ] && pass "Foreign keys: ON" || warn "Foreign keys: OFF in current connection (set in daemon)"

    FTS5=$(sqlite3 "$DB_PATH" \
        "SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='files_fts';" 2>/dev/null || echo "0")
    [ "$FTS5" = "1" ] && pass "FTS5 search table: present" || warn "FTS5 search table: missing"

    # Audit chain
    AUDIT_ROWS=$(sqlite3 "$DB_PATH" "SELECT COUNT(*) FROM audit_logs;" 2>/dev/null || echo "0")
    info "Audit log rows: $AUDIT_ROWS"

    # Sessions table — check for expired sessions piling up
    ACTIVE_SESSIONS=$(sqlite3 "$DB_PATH" \
        "SELECT COUNT(*) FROM sessions WHERE expires_at > strftime('%s','now');" 2>/dev/null || echo "0")
    EXPIRED_SESSIONS=$(sqlite3 "$DB_PATH" \
        "SELECT COUNT(*) FROM sessions WHERE expires_at <= strftime('%s','now');" 2>/dev/null || echo "0")
    pass "Active sessions: $ACTIVE_SESSIONS"
    if [ "$EXPIRED_SESSIONS" -gt 1000 ]; then
        warn "Expired sessions not cleaned up: $EXPIRED_SESSIONS rows (daemon should prune these)"
    else
        info "Expired sessions: $EXPIRED_SESSIONS"
    fi

    # Integrity check
    INTEGRITY=$(sqlite3 "$DB_PATH" "PRAGMA integrity_check;" 2>/dev/null | head -1)
    if [ "$INTEGRITY" = "ok" ]; then
        pass "Database integrity: OK"
    else
        fail "Database integrity check FAILED: $INTEGRITY"
    fi

    # Disk space for DB growth
    DB_DIR_FREE=$(df -BM "$DB_PATH" | awk 'NR==2{gsub(/M/,"",$4); print $4}')
    if [ "$DB_DIR_FREE" -ge 1024 ]; then
        pass "DB filesystem free: ${DB_DIR_FREE}MB"
    else
        warn "DB filesystem free: ${DB_DIR_FREE}MB — monitor closely"
    fi
fi

# ════════════════════════════════════════════════════════════════
section "7. ZFS"
# ════════════════════════════════════════════════════════════════

if ! command -v zpool &>/dev/null; then
    fail "ZFS utilities not installed"
else
    # Module
    if lsmod | grep -q "^zfs "; then
        pass "ZFS kernel module loaded"
    else
        fail "ZFS kernel module not loaded"
        info "  try: modprobe zfs"
    fi

    # ARC
    if [ -f /sys/module/zfs/parameters/zfs_arc_max ]; then
        ARC_MAX_BYTES=$(cat /sys/module/zfs/parameters/zfs_arc_max)
        ARC_MAX_GB=$((ARC_MAX_BYTES / 1024 / 1024 / 1024))
        ARC_CURRENT_KB=$(awk '/^size/{print $3}' /proc/spl/kstat/zfs/arcstats 2>/dev/null || echo 0)
        ARC_CURRENT_MB=$((ARC_CURRENT_KB / 1024))
        pass "ZFS ARC max: ${ARC_MAX_GB}GB | current: ${ARC_CURRENT_MB}MB"
    fi

    # ARC hit ratio
    if [ -f /proc/spl/kstat/zfs/arcstats ]; then
        ARC_HITS=$(awk '/^hits/{print $3}' /proc/spl/kstat/zfs/arcstats 2>/dev/null || echo 0)
        ARC_MISS=$(awk '/^misses/{print $3}' /proc/spl/kstat/zfs/arcstats 2>/dev/null || echo 0)
        ARC_TOTAL=$((ARC_HITS + ARC_MISS))
        if [ "$ARC_TOTAL" -gt 0 ]; then
            ARC_HIT_PCT=$((ARC_HITS * 100 / ARC_TOTAL))
            if [ "$ARC_HIT_PCT" -ge 80 ]; then
                pass "ARC hit ratio: ${ARC_HIT_PCT}%"
            elif [ "$ARC_HIT_PCT" -ge 60 ]; then
                warn "ARC hit ratio: ${ARC_HIT_PCT}% (consider more RAM)"
            else
                warn "ARC hit ratio: ${ARC_HIT_PCT}% (low — system may be under-RAM'd for workload)"
            fi
        fi
    fi

    # Pools
    POOL_COUNT=$(zpool list -H 2>/dev/null | wc -l)
    if [ "$POOL_COUNT" -eq 0 ]; then
        warn "No ZFS pools configured"
    else
        info "ZFS pools: $POOL_COUNT"
        echo ""
        while IFS=$'\t' read -r name size alloc free frag cap dedup health altroot; do
            echo -e "  Pool: ${BOLD}$name${NC}"
            info "  Size: $size  Alloc: $alloc  Free: $free  Frag: $frag  Cap: $cap"

            if [ "$health" = "ONLINE" ]; then
                pass "  Health: $health"
            elif [ "$health" = "DEGRADED" ]; then
                fail "  Health: DEGRADED — pool has failed/missing vdevs"
                info "  run: zpool status $name"
            else
                fail "  Health: $health"
            fi

            # Capacity warning
            CAP_NUM=${cap/\%/}
            if [ "${CAP_NUM:-0}" -ge 80 ]; then
                fail "  Capacity at ${cap} — ZFS performance degrades >80%"
            elif [ "${CAP_NUM:-0}" -ge 70 ]; then
                warn "  Capacity at ${cap} — plan expansion"
            fi

            # Scrub status (skip in quick mode)
            if ! $QUICK; then
                SCRUB=$(zpool status "$name" 2>/dev/null | grep "scan:" | head -1 | xargs)
                if echo "$SCRUB" | grep -q "scrub repaired"; then
                    pass "  Last scrub: completed"
                    if echo "$SCRUB" | grep -qv "with 0 errors"; then
                        warn "  Scrub found errors — check: zpool status $name"
                    fi
                elif echo "$SCRUB" | grep -q "none requested"; then
                    warn "  No scrub has ever been run on this pool"
                else
                    info "  Scrub: $SCRUB"
                fi
            fi

            echo ""
        done < <(zpool list -H -o name,size,alloc,free,frag,cap,dedup,health,altroot 2>/dev/null)
    fi
fi

# ════════════════════════════════════════════════════════════════
section "8. NGINX"
# ════════════════════════════════════════════════════════════════

if nginx -t 2>/dev/null; then
    pass "nginx config syntax: OK"
else
    fail "nginx config syntax error"
fi

# Check D-PlaneOS site config exists and is enabled
if [ -f /etc/nginx/sites-available/dplaneos ]; then
    pass "D-PlaneOS nginx site config: present"
else
    fail "D-PlaneOS nginx site config: missing"
fi

if [ -L /etc/nginx/sites-enabled/dplaneos ] || [ -f /etc/nginx/sites-enabled/dplaneos ]; then
    pass "D-PlaneOS nginx site: enabled"
else
    fail "D-PlaneOS nginx site: not enabled in sites-enabled/"
fi

# Check required security headers present
NGINX_CONF="/etc/nginx/sites-available/dplaneos"
if [ -f "$NGINX_CONF" ]; then
    for header in "X-Frame-Options" "X-Content-Type-Options" "Content-Security-Policy" "Referrer-Policy"; do
        if grep -q "$header" "$NGINX_CONF"; then
            pass "Security header present: $header"
        else
            warn "Security header missing in nginx config: $header"
        fi
    done

    # CSP unsafe-inline note
    if grep -q "unsafe-inline" "$NGINX_CONF"; then
        warn "CSP contains 'unsafe-inline' — required for current frontend but limits XSS protection"
    fi
fi

# Web UI reachability
HTTP_CODE=$(curl -sf --max-time 5 -o /dev/null -w "%{http_code}" http://localhost/ 2>/dev/null || echo "000")
if [ "$HTTP_CODE" = "200" ] || [ "$HTTP_CODE" = "302" ]; then
    pass "Web UI: reachable (HTTP $HTTP_CODE)"
else
    fail "Web UI: not reachable (HTTP $HTTP_CODE)"
fi

# ════════════════════════════════════════════════════════════════
section "9. SECURITY"
# ════════════════════════════════════════════════════════════════

# Port 9000 exposure (already checked above, flag again in security context)
if ss -tuln 2>/dev/null | grep ":9000" | grep -qv "127.0.0.1"; then
    fail "Daemon port 9000 exposed beyond localhost"
fi

# SSH
if ss -tuln 2>/dev/null | grep -q ":22 "; then
    info "SSH port 22 is open"
    # Check PasswordAuthentication
    if grep -qE "^PasswordAuthentication\s+no" /etc/ssh/sshd_config 2>/dev/null; then
        pass "SSH: PasswordAuthentication disabled (key-only)"
    else
        warn "SSH: PasswordAuthentication may be enabled — consider key-only auth"
    fi
    if grep -qE "^PermitRootLogin\s+(no|prohibit-password)" /etc/ssh/sshd_config 2>/dev/null; then
        pass "SSH: PermitRootLogin restricted"
    else
        warn "SSH: PermitRootLogin not explicitly restricted"
    fi
fi

# UFW / firewall
if command -v ufw &>/dev/null; then
    UFW_STATUS=$(ufw status 2>/dev/null | head -1)
    if echo "$UFW_STATUS" | grep -q "active"; then
        pass "UFW firewall: active"
    else
        warn "UFW installed but not active"
    fi
else
    warn "UFW not installed — no host-level firewall detected"
fi

# fail2ban
if systemctl is-active fail2ban &>/dev/null; then
    pass "fail2ban: running"
else
    warn "fail2ban: not running — no brute-force protection"
fi

# Unattended-upgrades
if systemctl is-active unattended-upgrades &>/dev/null; then
    pass "unattended-upgrades: active"
elif dpkg -l unattended-upgrades 2>/dev/null | grep -q "^ii"; then
    warn "unattended-upgrades: installed but not active"
else
    warn "unattended-upgrades: not installed — security patches not automatic"
fi

# sudoers dplaneos scope check — ensure it's not overly broad
if [ -f /etc/sudoers.d/dplaneos ]; then
    if grep -q "ALL.*ALL.*NOPASSWD.*ALL$" /etc/sudoers.d/dplaneos 2>/dev/null; then
        fail "sudoers: overly broad NOPASSWD ALL rule detected"
    else
        pass "sudoers: rules are scoped (not blanket NOPASSWD ALL)"
    fi
fi

# DB file not world-readable
if [ -f "$DB_PATH" ]; then
    DB_WORLD=$(stat -c '%a' "$DB_PATH" | cut -c3)
    if [ "$DB_WORLD" = "0" ]; then
        pass "Database: not world-readable"
    else
        fail "Database: world-readable (permissions: $(stat -c '%a' "$DB_PATH"))"
    fi
fi

# systemd hardening active
if systemctl show dplaned --property=ProtectSystem --value 2>/dev/null | grep -q "strict"; then
    pass "systemd ProtectSystem=strict active"
else
    warn "systemd ProtectSystem=strict not detected"
fi

if systemctl show dplaned --property=NoNewPrivileges --value 2>/dev/null | grep -q "yes"; then
    pass "systemd NoNewPrivileges active"
else
    warn "systemd NoNewPrivileges not detected"
fi

# ════════════════════════════════════════════════════════════════
section "10. NETWORK"
# ════════════════════════════════════════════════════════════════

# Interfaces
ip -br addr 2>/dev/null | while IFS= read -r line; do info "$line"; done

# DNS
if [ -f /etc/resolv.conf ]; then
    DNS=$(grep "^nameserver" /etc/resolv.conf | awk '{print $2}' | tr '\n' ' ')
    info "DNS servers: $DNS"
fi

# networkd
if systemctl is-active systemd-networkd &>/dev/null; then
    pass "systemd-networkd: running"
    # Check for dplaneos-managed configs
    DPLANE_NETS=$(find /etc/systemd/network -name "50-dplane-*" 2>/dev/null | wc -l)
    info "D-PlaneOS network configs: $DPLANE_NETS file(s) in /etc/systemd/network/"
else
    info "systemd-networkd: not active (using NetworkManager or other)"
fi

# Internet reachability
if curl -sf --max-time 5 https://github.com &>/dev/null; then
    pass "Internet reachable (HTTPS)"
else
    warn "Internet not reachable — OTA updates and Docker pulls will fail"
fi

# ════════════════════════════════════════════════════════════════
section "11. DOCKER"
# ════════════════════════════════════════════════════════════════

if ! command -v docker &>/dev/null; then
    info "Docker: not installed (optional feature)"
else
    DOCKER_VER=$(docker version --format '{{.Server.Version}}' 2>/dev/null || echo "unknown")
    pass "Docker version: $DOCKER_VER"

    # Storage driver
    DOCKER_DRIVER=$(docker info --format '{{.Driver}}' 2>/dev/null || echo "unknown")
    info "Storage driver: $DOCKER_DRIVER"
    if [ "$DOCKER_DRIVER" = "zfs" ]; then
        pass "Docker using ZFS native storage driver"
    elif [ "$DOCKER_DRIVER" = "overlay2" ]; then
        info "Docker using overlay2 (ZFS native would be more efficient on this system)"
    fi

    # Running containers
    RUNNING=$(docker ps -q 2>/dev/null | wc -l)
    TOTAL_CTR=$(docker ps -aq 2>/dev/null | wc -l)
    info "Containers: $RUNNING running / $TOTAL_CTR total"

    # Docker socket permissions
    SOCK_PERMS=$(stat -c '%a' /var/run/docker.sock 2>/dev/null || echo "unknown")
    if [ "$SOCK_PERMS" = "660" ]; then
        pass "Docker socket permissions: 660"
    else
        warn "Docker socket permissions: $SOCK_PERMS (expected 660)"
    fi

    # Docker bridge network DOCKER-USER chain
    if iptables -L DOCKER-USER &>/dev/null 2>&1; then
        DOCKER_USER_RULES=$(iptables -L DOCKER-USER --line-numbers 2>/dev/null | grep -c "^[0-9]" || echo 0)
        if [ "$DOCKER_USER_RULES" -gt 1 ]; then
            pass "DOCKER-USER iptables chain: custom rules present"
        else
            warn "DOCKER-USER iptables chain: default only — Docker may bypass UFW"
        fi
    fi
fi

# ════════════════════════════════════════════════════════════════
section "12. LOGS"
# ════════════════════════════════════════════════════════════════

# Recent daemon errors
DAEMON_ERRORS=$(journalctl -u dplaned --since "24 hours ago" -p err..crit --no-pager -q 2>/dev/null | wc -l)
if [ "$DAEMON_ERRORS" -eq 0 ]; then
    pass "Daemon: 0 errors in last 24h"
else
    fail "Daemon: $DAEMON_ERRORS error-level log entries in last 24h"
    info "  review: journalctl -u dplaned --since '24 hours ago' -p err"
fi

# nginx errors
NGINX_ERRORS=$(journalctl -u nginx --since "24 hours ago" -p err..crit --no-pager -q 2>/dev/null | wc -l)
if [ "$NGINX_ERRORS" -eq 0 ]; then
    pass "nginx: 0 errors in last 24h"
else
    warn "nginx: $NGINX_ERRORS error-level log entries in last 24h"
fi

# Watchdog cron
if crontab -l 2>/dev/null | grep -q "dplaneos-watchdog"; then
    pass "Watchdog cron: installed"
else
    warn "Watchdog cron: not found — daemon won't auto-restart if it dies"
fi

# Log directory disk usage
LOG_SIZE=$(du -sh /var/log/dplaneos 2>/dev/null | cut -f1 || echo "0")
info "Log directory: $LOG_SIZE in /var/log/dplaneos/"

# ════════════════════════════════════════════════════════════════
section "13. PERFORMANCE SNAPSHOT"
# ════════════════════════════════════════════════════════════════

# Load average
LOAD=$(uptime | grep -oP 'load average: \K.*')
LOAD_1=$(echo "$LOAD" | cut -d, -f1 | xargs)
info "Load average: $LOAD"
if (( $(echo "$LOAD_1 > $CPU_CORES" | bc -l 2>/dev/null || echo 0) )); then
    warn "1-min load ($LOAD_1) exceeds CPU core count ($CPU_CORES)"
fi

# Disk I/O wait
IOWAIT=$(iostat -c 1 2 2>/dev/null | awk 'NR==7{print $4}' || echo "0")
info "I/O wait: ${IOWAIT}%"
if (( $(echo "${IOWAIT:-0} > 20" | bc -l 2>/dev/null || echo 0) )); then
    warn "High I/O wait: ${IOWAIT}% — storage may be a bottleneck"
fi

# Open file handles
OPEN_FILES=$(cat /proc/sys/fs/file-nr 2>/dev/null | awk '{print $1}' || echo "unknown")
FILE_MAX=$(cat /proc/sys/fs/file-max 2>/dev/null || echo "unknown")
info "Open file handles: $OPEN_FILES / $FILE_MAX"

# Daemon open files
if [ -n "${DPLANED_PID:-}" ] && [ "${DPLANED_PID:-0}" != "0" ]; then
    DAEMON_FD=$(ls /proc/$DPLANED_PID/fd 2>/dev/null | wc -l || echo "unknown")
    info "Daemon open FDs: $DAEMON_FD"
fi

# ════════════════════════════════════════════════════════════════
# SUMMARY
# ════════════════════════════════════════════════════════════════

TOTAL=$((PASS + WARN + FAIL))
HEALTH_PCT=$(( PASS * 100 / (TOTAL > 0 ? TOTAL : 1) ))

echo ""
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo -e "${BOLD}    Audit Complete — $(date)${NC}"
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo ""
echo -e "  ${GREEN}Passed${NC}  : $PASS"
echo -e "  ${YELLOW}Warnings${NC}: $WARN"
echo -e "  ${RED}Failed${NC}  : $FAIL"
echo -e "  Health  : ${HEALTH_PCT}%"
echo ""

if [ "$FAIL" -eq 0 ] && [ "$WARN" -eq 0 ]; then
    echo -e "  ${BOLD}${GREEN}ALL CHECKS PASSED — System healthy${NC}"
    EXIT_CODE=0
elif [ "$FAIL" -eq 0 ]; then
    echo -e "  ${BOLD}${YELLOW}$WARN warning(s) — System functional, review warnings above${NC}"
    EXIT_CODE=0
else
    echo -e "  ${BOLD}${RED}$FAIL failure(s) — Attention required${NC}"
    EXIT_CODE=1
fi

echo ""
echo "  Full report saved: $REPORT"
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo ""

exit $EXIT_CODE
