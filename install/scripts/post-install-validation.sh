#!/bin/bash
#
# D-PlaneOS — Post-Install Validation (Debian/Ubuntu only)
# Called automatically at the end of install.sh
#

set -euo pipefail

RED='\033[0;31m'; GREEN='\033[0;32m'; YELLOW='\033[1;33m'
BLUE='\033[0;34m'; BOLD='\033[1m'; NC='\033[0m'

ERRORS=0
WARNINGS=0

VERSION="$(cat "$(dirname "$0")/../VERSION" 2>/dev/null | tr -d '[:space:]' || echo "?")"
echo ""
echo -e "${BOLD}${BLUE}D-PlaneOS v${VERSION} — Post-Install Validation${NC}"
echo "==========================================="
echo ""

pass() { echo -e "${GREEN}✓${NC} $1"; }
fail() { echo -e "${RED}✗${NC} $1"; ERRORS=$((ERRORS+1)); }
warn() { echo -e "${YELLOW}⚠${NC} $1"; WARNINGS=$((WARNINGS+1)); }
info() { echo -e "${BLUE}ℹ${NC} $1"; }

# ── 1. Services ───────────────────────────────────────────────────────────────
echo "1. Checking services..."

systemctl is-active nginx &>/dev/null \
    && pass "nginx running" \
    || fail "nginx not running — try: systemctl start nginx"

systemctl is-active dplaned &>/dev/null \
    && pass "dplaned running" \
    || fail "dplaned not running — check: journalctl -xe -u dplaned"

echo ""

# ── 2. Database ───────────────────────────────────────────────────────────────
echo "2. Checking database..."

DB_PATH="/var/lib/dplaneos/dplaneos.db"
if [ -f "$DB_PATH" ]; then
    pass "Database file exists"
    [ -r "$DB_PATH" ] && [ -w "$DB_PATH" ] \
        && pass "Database readable/writable" \
        || fail "Database permissions incorrect — try: chmod 600 $DB_PATH"

    WAL=$(sqlite3 "$DB_PATH" "PRAGMA journal_mode;" 2>/dev/null || echo "error")
    [ "$WAL" = "wal" ] && pass "WAL mode enabled" || warn "WAL mode not active (got: $WAL)"

    FTS=$(sqlite3 "$DB_PATH" "SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='files_fts';" 2>/dev/null || echo "0")
    [ "$FTS" -eq 1 ] && pass "FTS5 search table present" || warn "FTS5 table not found"

    USERS=$(sqlite3 "$DB_PATH" "SELECT COUNT(*) FROM users;" 2>/dev/null || echo "0")
    [ "$USERS" -ge 1 ] && pass "Admin user exists" || fail "No users in database"
else
    fail "Database file not found at $DB_PATH"
fi
echo ""

# ── 3. Web UI ─────────────────────────────────────────────────────────────────
echo "3. Checking web UI..."

HTTP=$(curl -s -o /dev/null -w "%{http_code}" http://localhost/ 2>/dev/null || echo "000")
case "$HTTP" in
    200|301|302) pass "Web UI accessible (HTTP $HTTP)" ;;
    000) fail "Web UI not reachable — nginx may not be running" ;;
    *)   warn "Web UI returned HTTP $HTTP (expected 200)" ;;
esac
echo ""

# ── 4. Daemon API ─────────────────────────────────────────────────────────────
echo "4. Checking daemon API..."

HEALTH=$(curl -sf http://127.0.0.1:9000/health 2>/dev/null || echo "fail")
if echo "$HEALTH" | grep -qi "ok\|healthy\|running\|alive"; then
    pass "Daemon health endpoint OK"
else
    # Might just not have /health — try another safe endpoint
    INFO=$(curl -sf http://127.0.0.1:9000/api/system/info 2>/dev/null || echo "fail")
    if [ "$INFO" != "fail" ]; then
        pass "Daemon API responding"
    else
        fail "Daemon not responding on :9000 — check: journalctl -xe -u dplaned"
    fi
fi

DB_TEST=$(sqlite3 "$DB_PATH" "SELECT 1;" 2>/dev/null || echo "fail")
[ "$DB_TEST" = "1" ] && pass "SQLite accessible" || fail "SQLite not accessible"
echo ""

# ── 5. File permissions ───────────────────────────────────────────────────────
echo "5. Checking permissions..."

[ -d "/opt/dplaneos" ] && pass "Install dir exists" || fail "Install dir /opt/dplaneos missing"

OWNER=$(stat -c '%U' /opt/dplaneos/app 2>/dev/null || echo "unknown")
[ "$OWNER" = "www-data" ] \
    && pass "app/ owned by www-data" \
    || warn "app/ owned by $OWNER (should be www-data)"

DB_DIR_PERM=$(stat -c '%a' /var/lib/dplaneos 2>/dev/null || echo "000")
[[ "$DB_DIR_PERM" =~ ^7 ]] \
    && pass "DB directory permissions ($DB_DIR_PERM)" \
    || warn "DB directory permissions unexpected: $DB_DIR_PERM"
echo ""

# ── 6. sudoers ────────────────────────────────────────────────────────────────
echo "6. Checking sudoers..."

if [ -f /etc/sudoers.d/dplaneos ]; then
    pass "sudoers file exists"
    SPERM=$(stat -c '%a' /etc/sudoers.d/dplaneos)
    [ "$SPERM" = "440" ] && pass "sudoers permissions (440)" || fail "sudoers permissions: $SPERM (should be 440)"
    visudo -c -f /etc/sudoers.d/dplaneos &>/dev/null && pass "sudoers syntax valid" || fail "sudoers syntax invalid"
else
    warn "sudoers file not found — daemon still works as root"
fi
echo ""

# ── 7. ZFS ────────────────────────────────────────────────────────────────────
echo "7. Checking ZFS..."

if command -v zpool &>/dev/null; then
    pass "ZFS utilities present"
    if zpool list &>/dev/null; then
        pass "zpool command works"
        NPOOLS=$(zpool list -H 2>/dev/null | wc -l)
        [ "$NPOOLS" -gt 0 ] \
            && info "$NPOOLS ZFS pool(s) found" \
            || info "No ZFS pools yet — create them via the UI after login"
    else
        warn "zpool command failed (module may not be loaded yet)"
        info "Try: sudo modprobe zfs"
    fi
else
    warn "ZFS not installed"
fi
echo ""

# ── 8. SMB / NFS (optional services) ─────────────────────────────────────────
echo "8. Checking file sharing services..."

if command -v smbd &>/dev/null; then
    systemctl is-active smbd &>/dev/null \
        && pass "Samba running" \
        || info "Samba installed but not running (starts automatically when shares are created)"
else
    warn "Samba not installed — SMB shares unavailable"
    info "Install: apt install samba"
fi

if systemctl list-units --type=service 2>/dev/null | grep -q "nfs-server"; then
    systemctl is-active nfs-server &>/dev/null \
        && pass "NFS server running" \
        || info "NFS installed but not running (starts on first NFS share)"
else
    warn "NFS server not installed — NFS shares unavailable"
    info "Install: apt install nfs-kernel-server"
fi
echo ""

# ── 9. Kernel tuning ─────────────────────────────────────────────────────────
echo "9. Checking kernel tuning..."

WATCHES=$(sysctl -n fs.inotify.max_user_watches 2>/dev/null || echo "0")
[ "$WATCHES" -ge 524288 ] \
    && pass "inotify watches: $WATCHES" \
    || warn "inotify watches: $WATCHES (expected 524288 — will apply on next reboot)"
echo ""

# ── 10. Recovery CLI ──────────────────────────────────────────────────────────
echo "10. Checking recovery tools..."

[ -x /usr/local/bin/dplaneos-recovery ] \
    && pass "Recovery CLI installed" \
    || warn "Recovery CLI not found at /usr/local/bin/dplaneos-recovery"
echo ""

# ── Summary ───────────────────────────────────────────────────────────────────
echo "==========================================="
echo "Summary"
echo "==========================================="
echo ""

if [ $ERRORS -eq 0 ] && [ $WARNINGS -eq 0 ]; then
    echo -e "${BOLD}${GREEN}✓ ALL CHECKS PASSED${NC}"
    echo ""
    echo "  Access: http://$(hostname -I | awk '{print $1}')"
    echo "  Login:  admin  (password shown above — change on first login)"
    echo ""
    exit 0
elif [ $ERRORS -eq 0 ]; then
    echo -e "${BOLD}${YELLOW}⚠ $WARNINGS warning(s) — installation successful${NC}"
    echo ""
    echo "  Warnings are non-critical. D-PlaneOS is fully operational."
    echo "  Access: http://$(hostname -I | awk '{print $1}')"
    echo ""
    exit 0
else
    echo -e "${BOLD}${RED}✗ $ERRORS error(s), $WARNINGS warning(s)${NC}"
    echo ""
    echo "  Installation has problems. Run: sudo dplaneos-recovery"
    echo ""
    exit 1
fi
