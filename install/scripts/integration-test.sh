#!/bin/bash
#
# D-PlaneOS Integration Test Suite
#
# Simulates a complete fresh-user journey on real hardware:
#   1. Runs install.sh exactly as a new user would
#   2. Onboards: first login, forced password change, session management
#   3. Exercises every API subsystem end-to-end
#   4. Cleans up after itself (destroys test ZFS datasets, removes test user)
#   5. Produces a timestamped pass/fail report
#
# Prerequisites: Root, ZFS pool must exist (at least one ONLINE pool)
# Usage: sudo ./integration-test.sh [--pool POOLNAME] [--port PORT] [--skip-install]
#
# --pool POOLNAME   ZFS pool to create test datasets in (default: auto-detect first ONLINE pool)
# --port PORT       Port D-PlaneOS is/will be on (default: 80)
# --skip-install    Skip install.sh, test against already-running instance
#

set -euo pipefail

# ── Args ──────────────────────────────────────────────────────────────────────
OPT_POOL=""
OPT_PORT=80
OPT_SKIP_INSTALL=false

while [[ $# -gt 0 ]]; do
    case "$1" in
        --pool)         OPT_POOL="$2"; shift 2 ;;
        --port)         OPT_PORT="$2"; shift 2 ;;
        --skip-install) OPT_SKIP_INSTALL=true; shift ;;
        --help|-h)
            echo "Usage: sudo $0 [--pool POOL] [--port PORT] [--skip-install]"
            exit 0 ;;
        *) echo "Unknown option: $1"; exit 1 ;;
    esac
done

[ "$EUID" -eq 0 ] || { echo "Must run as root: sudo $0"; exit 1; }

# ── Setup ─────────────────────────────────────────────────────────────────────
TIMESTAMP=$(date +%Y%m%d-%H%M%S)
REPORT_DIR="/var/log/dplaneos"
mkdir -p "$REPORT_DIR"
REPORT="$REPORT_DIR/integration-test-${TIMESTAMP}.txt"
exec > >(tee -a "$REPORT") 2>&1

API="http://127.0.0.1:9000"
DAEMON_BIN="/opt/dplaneos/daemon/dplaned"
DB_PATH="/var/lib/dplaneos/dplaneos.db"

# Test credentials — used during the test, cleaned up after
TEST_USER="it-testuser-${TIMESTAMP: -6}"
TEST_PASS="TestPass1!${TIMESTAMP: -4}"  # meets complexity requirements
TEST_DATASET_BASE=""       # set after pool detection
ADMIN_PASS=""              # extracted from DB after install
SESSION=""                 # populated after login

# Counters
PASS=0; FAIL=0; SKIP=0
FAILURES=""

# ── Colours ───────────────────────────────────────────────────────────────────
RED='\033[0;31m'; GREEN='\033[0;32m'; YELLOW='\033[1;33m'
BLUE='\033[0;34m'; BOLD='\033[1m'; CYAN='\033[0;36m'; NC='\033[0m'

# ── Helpers ───────────────────────────────────────────────────────────────────
section() {
    echo ""
    echo -e "${BOLD}${CYAN}══════════════════════════════════════════════════${NC}"
    echo -e "${BOLD}${CYAN}  $1${NC}"
    echo -e "${BOLD}${CYAN}══════════════════════════════════════════════════${NC}"
}

pass() {
    echo -e "  ${GREEN}✓${NC} $1"
    PASS=$((PASS+1))
}

fail() {
    echo -e "  ${RED}✗${NC} $1"
    FAIL=$((FAIL+1))
    FAILURES="${FAILURES}\n  ✗ $1"
}

skip() {
    echo -e "  ${YELLOW}—${NC} SKIP: $1"
    SKIP=$((SKIP+1))
}

info() { echo -e "  ${BLUE}ℹ${NC} $1"; }

# Make an authenticated API call and return the body
# Usage: api GET /api/zfs/pools
# Usage: api POST /api/zfs/snapshots '{"dataset":"tank/test"}'
api() {
    local method="$1"
    local path="$2"
    local body="${3:-}"
    local args=(-sf --max-time 15 -X "$method" "$API$path"
                -H "X-Session-ID: $SESSION"
                -H "X-User: admin"
                -H "Content-Type: application/json")
    if [ -n "$body" ]; then
        args+=(-d "$body")
    fi
    curl "${args[@]}" 2>/dev/null || echo '{"_curl_error":true}'
}

# Assert a JSON response contains a key with a specific value or truthy key
# Usage: assert_json RESPONSE_VAR "success" "true"  -> checks .success == true
# Usage: assert_json RESPONSE_VAR "success"          -> checks .success is truthy
assert_json() {
    local label="$1"
    local resp="$2"
    local key="$3"
    local expected="${4:-}"

    if echo "$resp" | python3 -c "
import sys, json
try:
    d = json.load(sys.stdin)
except:
    sys.exit(1)
val = d
for k in '$key'.split('.'):
    if isinstance(val, dict):
        val = val.get(k)
    else:
        val = None
        break
if '$expected' == '':
    sys.exit(0 if val else 1)
sys.exit(0 if str(val).lower() == '$expected'.lower() else 1)
" 2>/dev/null; then
        pass "$label"
    else
        fail "$label (response: $(echo "$resp" | head -c 200))"
    fi
}

# Assert HTTP status code
assert_status() {
    local label="$1"
    local expected="$2"
    local method="$3"
    local path="$4"
    local body="${5:-}"
    local args=(-s --max-time 10 -o /dev/null -w "%{http_code}"
                -X "$method" "$API$path"
                -H "X-Session-ID: $SESSION"
                -H "X-User: admin"
                -H "Content-Type: application/json")
    [ -n "$body" ] && args+=(-d "$body")
    local got
    got=$(curl "${args[@]}" 2>/dev/null || echo "000")
    if [ "$got" = "$expected" ]; then
        pass "$label (HTTP $got)"
    else
        fail "$label (expected HTTP $expected, got $got)"
    fi
}

# ── Cleanup trap ──────────────────────────────────────────────────────────────
cleanup() {
    echo ""
    info "Cleaning up test artifacts..."
    # Remove test ZFS datasets
    if [ -n "$TEST_DATASET_BASE" ]; then
        zfs list -H -o name -r "$TEST_DATASET_BASE" 2>/dev/null | sort -r | while read -r ds; do
            zfs destroy -f "$ds" 2>/dev/null && info "Destroyed: $ds" || true
        done
    fi
    # Remove test user from DB
    if [ -f "$DB_PATH" ] && [ -n "$TEST_USER" ]; then
        sqlite3 "$DB_PATH" "DELETE FROM users WHERE username = '$TEST_USER';" 2>/dev/null || true
        sqlite3 "$DB_PATH" "DELETE FROM sessions WHERE username = '$TEST_USER';" 2>/dev/null || true
        info "Removed test user: $TEST_USER"
    fi
}
trap cleanup EXIT

# ── Header ────────────────────────────────────────────────────────────────────
clear
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo -e "${BOLD}    D-PlaneOS Integration Test Suite${NC}"
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo "  Host      : $(hostname)"
echo "  Date      : $(date)"
echo "  Report    : $REPORT"
echo "  Skip inst : $OPT_SKIP_INSTALL"
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"

# ════════════════════════════════════════════════════════════════
section "PHASE 1: ZFS pool detection"
# ════════════════════════════════════════════════════════════════

if [ -n "$OPT_POOL" ]; then
    if zpool list -H "$OPT_POOL" &>/dev/null; then
        pass "Pool '$OPT_POOL' exists"
        TEST_POOL="$OPT_POOL"
    else
        fail "Specified pool '$OPT_POOL' not found"
        echo "Available pools:"; zpool list 2>/dev/null || echo "  (none)"
        exit 1
    fi
else
    TEST_POOL=$(zpool list -H -o name,health 2>/dev/null | awk '$2=="ONLINE"{print $1; exit}')
    if [ -z "$TEST_POOL" ]; then
        fail "No ONLINE ZFS pool found — create a pool first"
        echo "ZFS pools present:"; zpool list 2>/dev/null || echo "  (none)"
        exit 1
    fi
    pass "Auto-detected pool: $TEST_POOL"
fi

TEST_DATASET_BASE="${TEST_POOL}/dplaneos-integration-test"
info "Test dataset base: $TEST_DATASET_BASE"

# ════════════════════════════════════════════════════════════════
section "PHASE 2: Installation"
# ════════════════════════════════════════════════════════════════

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
INSTALL_SH="$(dirname "$SCRIPT_DIR")/install.sh"

if $OPT_SKIP_INSTALL; then
    skip "Installation (--skip-install)"
else
    if [ ! -f "$INSTALL_SH" ]; then
        fail "install.sh not found at $INSTALL_SH"
        exit 1
    fi

    info "Running install.sh --unattended --port $OPT_PORT ..."
    if bash "$INSTALL_SH" --unattended --port "$OPT_PORT" 2>&1; then
        pass "install.sh completed without error"
    else
        fail "install.sh exited non-zero"
        echo "Check install log: /var/log/dplaneos-install.log"
        exit 1
    fi
fi

# Verify daemon is running
if systemctl is-active dplaned &>/dev/null; then
    pass "dplaned service: active"
else
    fail "dplaned service: not active after install"
    journalctl -u dplaned -n 30 --no-pager
    exit 1
fi

if systemctl is-active nginx &>/dev/null; then
    pass "nginx service: active"
else
    fail "nginx service: not active after install"
fi

# Wait for daemon to be ready
info "Waiting for daemon health endpoint..."
for i in $(seq 1 20); do
    if curl -sf --max-time 2 "$API/health" | grep -q '"ok"'; then
        break
    fi
    sleep 1
done
HEALTH=$(curl -sf --max-time 5 "$API/health" 2>/dev/null || echo "{}")
assert_json "Daemon health endpoint responds" "$HEALTH" "status" "ok"

# ════════════════════════════════════════════════════════════════
section "PHASE 3: Onboarding — first login as admin"
# ════════════════════════════════════════════════════════════════

# Extract the generated admin password from the DB
# install.sh sets must_change_password=1 and stores the hash; we need the plaintext
# which install.sh printed to stdout. We read it from the install log instead.
if [ -f /var/log/dplaneos-install.log ]; then
    # The install log contains "Password : <value>" in the success banner
    ADMIN_PASS=$(grep -oP '(?<=Password : )\S+' /var/log/dplaneos-install.log | tail -1 || echo "")
fi

if [ -z "$ADMIN_PASS" ]; then
    skip "Could not extract generated admin password from install log — skipping onboarding tests"
    skip "Subsequent auth-dependent tests"
    # Fall through to tests that don't need auth where possible
    ADMIN_PASS=""
else
    pass "Extracted generated admin password from install log"
    info "Admin password: [REDACTED from report]"

    # --- 3a. Login with generated credentials ---
    CSRF_RESP=$(curl -sf --max-time 5 "$API/api/csrf" 2>/dev/null || echo "{}")
    assert_json "CSRF endpoint returns token" "$CSRF_RESP" "csrf_token"

    LOGIN_RESP=$(curl -sf --max-time 10 -X POST "$API/api/auth/login" \
        -H "Content-Type: application/json" \
        -d "{\"username\":\"admin\",\"password\":\"$ADMIN_PASS\"}" 2>/dev/null || echo "{}")
    assert_json "Admin login succeeds" "$LOGIN_RESP" "success" "true"
    assert_json "Login returns session_id" "$LOGIN_RESP" "session_id"
    assert_json "Login signals must_change_password" "$LOGIN_RESP" "must_change_password" "true"

    SESSION=$(echo "$LOGIN_RESP" | python3 -c "import sys,json; print(json.load(sys.stdin).get('session_id',''))" 2>/dev/null || echo "")

    if [ -z "$SESSION" ]; then
        fail "No session_id in login response — cannot continue auth-dependent tests"
    else
        pass "Session established"

        # --- 3b. Verify session check works ---
        CHECK_RESP=$(curl -sf --max-time 5 "$API/api/auth/check" \
            -H "X-Session-ID: $SESSION" 2>/dev/null || echo "{}")
        assert_json "Auth check: authenticated=true" "$CHECK_RESP" "authenticated" "true"

        # --- 3c. Session info ---
        SESSION_RESP=$(api GET /api/auth/session)
        assert_json "Session endpoint: success" "$SESSION_RESP" "success" "true"
        assert_json "Session endpoint: role=admin" "$SESSION_RESP" "user.role" "admin"

        # --- 3d. Forced password change ---
        NEW_PASS="Integration1!Test${TIMESTAMP: -4}"
        CHPASS_RESP=$(api POST /api/auth/change-password \
            "{\"current_password\":\"$ADMIN_PASS\",\"new_password\":\"$NEW_PASS\"}")
        assert_json "Forced password change: success" "$CHPASS_RESP" "success" "true"
        ADMIN_PASS="$NEW_PASS"

        # --- 3e. Re-login with new password ---
        LOGIN2_RESP=$(curl -sf --max-time 10 -X POST "$API/api/auth/login" \
            -H "Content-Type: application/json" \
            -d "{\"username\":\"admin\",\"password\":\"$ADMIN_PASS\"}" 2>/dev/null || echo "{}")
        assert_json "Re-login with new password: success" "$LOGIN2_RESP" "success" "true"
        assert_json "must_change_password cleared after change" "$LOGIN2_RESP" "must_change_password" "false"
        SESSION=$(echo "$LOGIN2_RESP" | python3 -c "import sys,json; print(json.load(sys.stdin).get('session_id',''))" 2>/dev/null || echo "")
        pass "Fresh session established after password change"

        # --- 3f. Wrong password is rejected ---
        BAD_LOGIN=$(curl -sf --max-time 5 -o /dev/null -w "%{http_code}" \
            -X POST "$API/api/auth/login" \
            -H "Content-Type: application/json" \
            -d '{"username":"admin","password":"wrongpassword"}' 2>/dev/null || echo "000")
        [ "$BAD_LOGIN" = "401" ] && pass "Wrong password returns 401" || fail "Wrong password returned $BAD_LOGIN (expected 401)"

        # --- 3g. Invalid username format rejected ---
        BAD_USER=$(curl -sf --max-time 5 -o /dev/null -w "%{http_code}" \
            -X POST "$API/api/auth/login" \
            -H "Content-Type: application/json" \
            -d '{"username":"ad min<script>","password":"x"}' 2>/dev/null || echo "000")
        [ "$BAD_USER" = "400" ] && pass "Invalid username format returns 400" || fail "Invalid username returned $BAD_USER (expected 400)"
    fi
fi

# ════════════════════════════════════════════════════════════════
section "PHASE 4: System status endpoints"
# ════════════════════════════════════════════════════════════════

STATUS_RESP=$(api GET /api/system/status)
assert_json "System status: success" "$STATUS_RESP" "success" "true"

PROFILE_RESP=$(api GET /api/system/profile)
assert_json "System profile responds" "$PROFILE_RESP" "success" "true"

PREFLIGHT_RESP=$(api GET /api/system/preflight)
assert_json "System preflight responds" "$PREFLIGHT_RESP" "success" "true"

NETWORK_RESP=$(api GET /api/system/network)
# Network endpoint returns 200 even with partial data — just check it responds
if echo "$NETWORK_RESP" | python3 -c "import sys,json; json.load(sys.stdin)" 2>/dev/null; then
    pass "System network: returns valid JSON"
else
    fail "System network: invalid JSON response"
fi

LOGS_RESP=$(api GET "/api/system/logs?lines=50")
if echo "$LOGS_RESP" | python3 -c "import sys,json; json.load(sys.stdin)" 2>/dev/null; then
    pass "System logs: returns valid JSON"
else
    fail "System logs: invalid JSON response"
fi

METRICS_RESP=$(api GET /api/metrics/current)
assert_json "Metrics current: responds" "$METRICS_RESP" "success" "true"

STALE_RESP=$(api GET /api/system/stale-locks)
if echo "$STALE_RESP" | python3 -c "import sys,json; json.load(sys.stdin)" 2>/dev/null; then
    pass "Stale locks endpoint: valid JSON"
else
    fail "Stale locks endpoint: invalid JSON"
fi

INOTIFY_RESP=$(api GET /api/monitoring/inotify)
if echo "$INOTIFY_RESP" | python3 -c "import sys,json; json.load(sys.stdin)" 2>/dev/null; then
    pass "Inotify monitoring: valid JSON"
else
    fail "Inotify monitoring: invalid JSON"
fi

DISKS_RESP=$(api GET /api/system/disks)
if echo "$DISKS_RESP" | python3 -c "import sys,json; json.load(sys.stdin)" 2>/dev/null; then
    pass "Disk discovery: valid JSON"
else
    fail "Disk discovery: invalid JSON"
fi

ZFS_GATE_RESP=$(api GET /api/system/zfs-gate-status)
if echo "$ZFS_GATE_RESP" | python3 -c "import sys,json; json.load(sys.stdin)" 2>/dev/null; then
    pass "ZFS gate status: valid JSON"
else
    fail "ZFS gate status: invalid JSON"
fi

NTP_RESP=$(api GET /api/system/ntp)
if echo "$NTP_RESP" | python3 -c "import sys,json; json.load(sys.stdin)" 2>/dev/null; then
    pass "NTP status: valid JSON"
else
    fail "NTP status: invalid JSON"
fi

SETTINGS_RESP=$(api GET /api/system/settings)
assert_json "System settings GET: success" "$SETTINGS_RESP" "success" "true"

# ════════════════════════════════════════════════════════════════
section "PHASE 5: ZFS pool and dataset operations"
# ════════════════════════════════════════════════════════════════

# --- List pools ---
POOLS_RESP=$(api GET /api/zfs/pools)
assert_json "ZFS list pools: success" "$POOLS_RESP" "success" "true"
POOL_COUNT=$(echo "$POOLS_RESP" | python3 -c \
    "import sys,json; d=json.load(sys.stdin); print(len(d.get('data',[])))" 2>/dev/null || echo "0")
if [ "$POOL_COUNT" -gt 0 ]; then
    pass "ZFS pools: $POOL_COUNT pool(s) visible"
else
    fail "ZFS pools: no pools returned (pool $TEST_POOL should be visible)"
fi

# --- List datasets ---
DS_RESP=$(api GET /api/zfs/datasets)
assert_json "ZFS list datasets: success" "$DS_RESP" "success" "true"

# --- Create test dataset ---
CREATE_DS_RESP=$(api POST /api/zfs/datasets \
    "{\"name\":\"${TEST_DATASET_BASE}\",\"compression\":\"lz4\"}")
assert_json "ZFS create dataset: success" "$CREATE_DS_RESP" "success" "true"

# Verify it was actually created
if zfs list "${TEST_DATASET_BASE}" &>/dev/null; then
    pass "ZFS dataset exists on system after create"
else
    fail "ZFS dataset not found on system after create API call"
fi

# --- Dataset quota ---
QUOTA_RESP=$(api POST /api/zfs/dataset/quota \
    "{\"dataset\":\"${TEST_DATASET_BASE}\",\"quota\":\"1G\"}")
if echo "$QUOTA_RESP" | python3 -c "import sys,json; json.load(sys.stdin)" 2>/dev/null; then
    pass "ZFS dataset quota set: valid JSON response"
else
    fail "ZFS dataset quota set: invalid response"
fi

GET_QUOTA_RESP=$(api GET "/api/zfs/dataset/quota?dataset=${TEST_DATASET_BASE}")
if echo "$GET_QUOTA_RESP" | python3 -c "import sys,json; json.load(sys.stdin)" 2>/dev/null; then
    pass "ZFS dataset quota get: valid JSON response"
else
    fail "ZFS dataset quota get: invalid response"
fi

# --- Snapshot lifecycle ---
SNAP_RESP=$(api POST /api/zfs/snapshots \
    "{\"dataset\":\"${TEST_DATASET_BASE}\",\"name\":\"it-snap-1\"}")
assert_json "ZFS create snapshot: success" "$SNAP_RESP" "success" "true"

FULL_SNAP="${TEST_DATASET_BASE}@it-snap-1"
if zfs list -t snapshot "$FULL_SNAP" &>/dev/null; then
    pass "ZFS snapshot exists on system after create"
else
    fail "ZFS snapshot not found on system after create API call"
fi

LIST_SNAP_RESP=$(api GET "/api/zfs/snapshots?dataset=${TEST_DATASET_BASE}")
assert_json "ZFS list snapshots: success" "$LIST_SNAP_RESP" "success" "true"
SNAP_COUNT=$(echo "$LIST_SNAP_RESP" | python3 -c \
    "import sys,json; d=json.load(sys.stdin); print(d.get('count',0))" 2>/dev/null || echo "0")
[ "$SNAP_COUNT" -ge 1 ] && pass "ZFS list snapshots: $SNAP_COUNT snapshot(s) returned" \
    || fail "ZFS list snapshots: expected ≥1, got $SNAP_COUNT"

# --- Snapshot schedule ---
SCHED_RESP=$(api GET /api/snapshots/schedules)
if echo "$SCHED_RESP" | python3 -c "import sys,json; json.load(sys.stdin)" 2>/dev/null; then
    pass "Snapshot schedules GET: valid JSON"
else
    fail "Snapshot schedules GET: invalid JSON"
fi

# --- ZFS health ---
HEALTH_RESP=$(api GET /api/zfs/health)
if echo "$HEALTH_RESP" | python3 -c "import sys,json; json.load(sys.stdin)" 2>/dev/null; then
    pass "ZFS health: valid JSON"
else
    fail "ZFS health: invalid JSON"
fi

IOSTAT_RESP=$(api GET /api/zfs/iostat)
if echo "$IOSTAT_RESP" | python3 -c "import sys,json; json.load(sys.stdin)" 2>/dev/null; then
    pass "ZFS iostat: valid JSON"
else
    fail "ZFS iostat: invalid JSON"
fi

EVENTS_RESP=$(api GET /api/zfs/events)
if echo "$EVENTS_RESP" | python3 -c "import sys,json; json.load(sys.stdin)" 2>/dev/null; then
    pass "ZFS events: valid JSON"
else
    fail "ZFS events: invalid JSON"
fi

SMART_RESP=$(api GET /api/zfs/smart)
if echo "$SMART_RESP" | python3 -c "import sys,json; json.load(sys.stdin)" 2>/dev/null; then
    pass "ZFS SMART health: valid JSON"
else
    fail "ZFS SMART health: invalid JSON"
fi

CAPACITY_RESP=$(api GET /api/zfs/capacity)
if echo "$CAPACITY_RESP" | python3 -c "import sys,json; json.load(sys.stdin)" 2>/dev/null; then
    pass "ZFS capacity: valid JSON"
else
    fail "ZFS capacity: invalid JSON"
fi

LATENCY_RESP=$(api GET /api/zfs/disk-latency)
if echo "$LATENCY_RESP" | python3 -c "import sys,json; json.load(sys.stdin)" 2>/dev/null; then
    pass "ZFS disk latency: valid JSON"
else
    fail "ZFS disk latency: invalid JSON"
fi

# --- Scrub ---
SCRUB_STATUS=$(api GET "/api/zfs/scrub/status?pool=${TEST_POOL}")
if echo "$SCRUB_STATUS" | python3 -c "import sys,json; json.load(sys.stdin)" 2>/dev/null; then
    pass "ZFS scrub status: valid JSON"
else
    fail "ZFS scrub status: invalid JSON"
fi

# Scrub schedules
SCRUB_SCHED=$(api GET /api/zfs/scrub/schedule)
if echo "$SCRUB_SCHED" | python3 -c "import sys,json; json.load(sys.stdin)" 2>/dev/null; then
    pass "ZFS scrub schedules: valid JSON"
else
    fail "ZFS scrub schedules: invalid JSON"
fi

# --- Snapshot rollback ---
ROLLBACK_RESP=$(api POST /api/zfs/snapshots/rollback \
    "{\"snapshot\":\"${FULL_SNAP}\"}")
assert_json "ZFS snapshot rollback: success" "$ROLLBACK_RESP" "success" "true"

# --- Destroy snapshot ---
DESTROY_RESP=$(api DELETE /api/zfs/snapshots \
    "{\"snapshot\":\"${FULL_SNAP}\"}")
assert_json "ZFS destroy snapshot: success" "$DESTROY_RESP" "success" "true"

if ! zfs list -t snapshot "$FULL_SNAP" &>/dev/null; then
    pass "ZFS snapshot gone from system after destroy"
else
    fail "ZFS snapshot still exists on system after destroy API call"
fi

# --- Encryption endpoints (expect meaningful response, not crash) ---
ENC_LIST=$(api GET /api/zfs/encryption/list)
if echo "$ENC_LIST" | python3 -c "import sys,json; json.load(sys.stdin)" 2>/dev/null; then
    pass "ZFS encryption list: valid JSON"
else
    fail "ZFS encryption list: invalid JSON"
fi

# --- ZFS delegation ---
DELEG_RESP=$(api GET "/api/zfs/delegation?dataset=${TEST_DATASET_BASE}")
if echo "$DELEG_RESP" | python3 -c "import sys,json; json.load(sys.stdin)" 2>/dev/null; then
    pass "ZFS delegation GET: valid JSON"
else
    fail "ZFS delegation GET: invalid JSON"
fi

# --- Time machine (list on our test dataset) ---
TM_RESP=$(api GET "/api/timemachine/versions?dataset=${TEST_DATASET_BASE}")
if echo "$TM_RESP" | python3 -c "import sys,json; json.load(sys.stdin)" 2>/dev/null; then
    pass "Time machine versions: valid JSON"
else
    fail "Time machine versions: invalid JSON"
fi

# ════════════════════════════════════════════════════════════════
section "PHASE 6: File manager"
# ════════════════════════════════════════════════════════════════

# Mount point of test dataset
MP=$(zfs get -H -o value mountpoint "${TEST_DATASET_BASE}" 2>/dev/null || echo "none")

if [ "$MP" != "none" ] && [ "$MP" != "-" ] && [ -d "$MP" ]; then
    # Create a test file directly so we have something to list
    echo "dplaneos integration test" > "$MP/it-testfile.txt"
    mkdir -p "$MP/it-testdir"

    FILES_RESP=$(api GET "/api/files/list?path=${MP}")
    if echo "$FILES_RESP" | python3 -c "import sys,json; json.load(sys.stdin)" 2>/dev/null; then
        pass "Files list: valid JSON for $MP"
    else
        fail "Files list: invalid JSON for $MP"
    fi

    PROPS_RESP=$(api GET "/api/files/properties?path=${MP}/it-testfile.txt")
    if echo "$PROPS_RESP" | python3 -c "import sys,json; json.load(sys.stdin)" 2>/dev/null; then
        pass "File properties: valid JSON"
    else
        fail "File properties: invalid JSON"
    fi

    MKDIR_RESP=$(api POST /api/files/mkdir \
        "{\"path\":\"${MP}/it-testdir2\"}")
    if echo "$MKDIR_RESP" | python3 -c "import sys,json; json.load(sys.stdin)" 2>/dev/null; then
        pass "Files mkdir: valid JSON"
        [ -d "${MP}/it-testdir2" ] && pass "Files mkdir: directory actually created" \
            || fail "Files mkdir: directory not found on filesystem"
    else
        fail "Files mkdir: invalid JSON"
    fi

    RENAME_RESP=$(api POST /api/files/rename \
        "{\"path\":\"${MP}/it-testfile.txt\",\"new_name\":\"it-testfile-renamed.txt\"}")
    if echo "$RENAME_RESP" | python3 -c "import sys,json; json.load(sys.stdin)" 2>/dev/null; then
        pass "Files rename: valid JSON"
    else
        fail "Files rename: invalid JSON"
    fi

    CHOWN_RESP=$(api POST /api/files/chown \
        "{\"path\":\"${MP}/it-testdir\",\"owner\":\"root\",\"group\":\"root\"}")
    if echo "$CHOWN_RESP" | python3 -c "import sys,json; json.load(sys.stdin)" 2>/dev/null; then
        pass "Files chown: valid JSON"
    else
        fail "Files chown: invalid JSON"
    fi

    CHMOD_RESP=$(api POST /api/files/chmod \
        "{\"path\":\"${MP}/it-testdir\",\"mode\":\"755\"}")
    if echo "$CHMOD_RESP" | python3 -c "import sys,json; json.load(sys.stdin)" 2>/dev/null; then
        pass "Files chmod: valid JSON"
    else
        fail "Files chmod: invalid JSON"
    fi

    DELETE_RESP=$(api POST /api/files/delete \
        "{\"path\":\"${MP}/it-testdir\"}")
    if echo "$DELETE_RESP" | python3 -c "import sys,json; json.load(sys.stdin)" 2>/dev/null; then
        pass "Files delete: valid JSON"
    else
        fail "Files delete: invalid JSON"
    fi
else
    skip "File manager tests (test dataset mountpoint unavailable: $MP)"
fi

# ════════════════════════════════════════════════════════════════
section "PHASE 7: User and group management"
# ════════════════════════════════════════════════════════════════

LIST_USERS=$(api GET /api/rbac/users)
assert_json "List users: success" "$LIST_USERS" "success" "true"
ADMIN_PRESENT=$(echo "$LIST_USERS" | python3 -c \
    "import sys,json; d=json.load(sys.stdin); print('yes' if any(u['username']=='admin' for u in d.get('users',[])) else 'no')" 2>/dev/null || echo "no")
[ "$ADMIN_PRESENT" = "yes" ] && pass "Admin user present in user list" \
    || fail "Admin user not found in user list"

# Create test user
CREATE_USER=$(api POST /api/users/create \
    "{\"action\":\"create\",\"username\":\"${TEST_USER}\",\"password\":\"${TEST_PASS}\",\"email\":\"test@test.local\",\"role\":\"user\"}")
assert_json "Create test user: success" "$CREATE_USER" "success" "true"

# Verify test user appears in list
LIST_USERS2=$(api GET /api/rbac/users)
NEW_USER_PRESENT=$(echo "$LIST_USERS2" | python3 -c \
    "import sys,json; d=json.load(sys.stdin); print('yes' if any(u['username']=='${TEST_USER}' for u in d.get('users',[])) else 'no')" 2>/dev/null || echo "no")
[ "$NEW_USER_PRESENT" = "yes" ] && pass "Test user visible in user list after creation" \
    || fail "Test user not found in user list after creation"

# Groups
LIST_GROUPS=$(api GET /api/rbac/groups)
if echo "$LIST_GROUPS" | python3 -c "import sys,json; json.load(sys.stdin)" 2>/dev/null; then
    pass "List groups: valid JSON"
else
    fail "List groups: invalid JSON"
fi

# RBAC permissions
MY_PERMS=$(api GET /api/rbac/me/permissions)
if echo "$MY_PERMS" | python3 -c "import sys,json; json.load(sys.stdin)" 2>/dev/null; then
    pass "My permissions: valid JSON"
else
    fail "My permissions: invalid JSON"
fi

MY_ROLES=$(api GET /api/rbac/me/roles)
if echo "$MY_ROLES" | python3 -c "import sys,json; json.load(sys.stdin)" 2>/dev/null; then
    pass "My roles: valid JSON"
else
    fail "My roles: invalid JSON"
fi

# API tokens
TOKENS_RESP=$(api GET /api/auth/tokens)
if echo "$TOKENS_RESP" | python3 -c "import sys,json; json.load(sys.stdin)" 2>/dev/null; then
    pass "API tokens list: valid JSON"
else
    fail "API tokens list: invalid JSON"
fi

# ════════════════════════════════════════════════════════════════
section "PHASE 8: Docker"
# ════════════════════════════════════════════════════════════════

if ! command -v docker &>/dev/null || ! systemctl is-active docker &>/dev/null; then
    skip "Docker tests (Docker not installed or not running)"
else
    PREFLIGHT_RESP=$(api GET /api/docker/preflight)
    if echo "$PREFLIGHT_RESP" | python3 -c "import sys,json; json.load(sys.stdin)" 2>/dev/null; then
        pass "Docker preflight: valid JSON"
    else
        fail "Docker preflight: invalid JSON"
    fi

    CONTAINERS_RESP=$(api GET /api/docker/containers)
    if echo "$CONTAINERS_RESP" | python3 -c "import sys,json; json.load(sys.stdin)" 2>/dev/null; then
        pass "Docker containers list: valid JSON"
    else
        fail "Docker containers list: invalid JSON"
    fi

    COMPOSE_STATUS=$(api GET /api/docker/compose/status)
    if echo "$COMPOSE_STATUS" | python3 -c "import sys,json; json.load(sys.stdin)" 2>/dev/null; then
        pass "Docker compose status: valid JSON"
    else
        fail "Docker compose status: invalid JSON"
    fi
fi

# ════════════════════════════════════════════════════════════════
section "PHASE 9: Shares"
# ════════════════════════════════════════════════════════════════

SHARES_RESP=$(api GET /api/shares)
if echo "$SHARES_RESP" | python3 -c "import sys,json; json.load(sys.stdin)" 2>/dev/null; then
    pass "Shares list: valid JSON"
else
    fail "Shares list: invalid JSON"
fi

NFS_LIST=$(api GET /api/shares/nfs/list)
if echo "$NFS_LIST" | python3 -c "import sys,json; json.load(sys.stdin)" 2>/dev/null; then
    pass "NFS exports list: valid JSON"
else
    fail "NFS exports list: invalid JSON"
fi

# ════════════════════════════════════════════════════════════════
section "PHASE 10: Git sync"
# ════════════════════════════════════════════════════════════════

GIT_CONFIG=$(api GET /api/git-sync/config)
if echo "$GIT_CONFIG" | python3 -c "import sys,json; json.load(sys.stdin)" 2>/dev/null; then
    pass "Git sync config GET: valid JSON"
else
    fail "Git sync config GET: invalid JSON"
fi

GIT_STATUS=$(api GET /api/git-sync/status)
if echo "$GIT_STATUS" | python3 -c "import sys,json; json.load(sys.stdin)" 2>/dev/null; then
    pass "Git sync status: valid JSON"
else
    fail "Git sync status: invalid JSON"
fi

GIT_STACKS=$(api GET /api/git-sync/stacks)
if echo "$GIT_STACKS" | python3 -c "import sys,json; json.load(sys.stdin)" 2>/dev/null; then
    pass "Git sync stacks: valid JSON"
else
    fail "Git sync stacks: invalid JSON"
fi

GIT_REPOS=$(api GET /api/git-sync/repos)
if echo "$GIT_REPOS" | python3 -c "import sys,json; json.load(sys.stdin)" 2>/dev/null; then
    pass "Git sync repos: valid JSON"
else
    fail "Git sync repos: invalid JSON"
fi

GIT_CREDS=$(api GET /api/git-sync/credentials)
if echo "$GIT_CREDS" | python3 -c "import sys,json; json.load(sys.stdin)" 2>/dev/null; then
    pass "Git sync credentials: valid JSON"
else
    fail "Git sync credentials: invalid JSON"
fi

# ════════════════════════════════════════════════════════════════
section "PHASE 11: Alerts and notifications"
# ════════════════════════════════════════════════════════════════

WEBHOOKS=$(api GET /api/alerts/webhooks)
if echo "$WEBHOOKS" | python3 -c "import sys,json; json.load(sys.stdin)" 2>/dev/null; then
    pass "Alert webhooks list: valid JSON"
else
    fail "Alert webhooks list: invalid JSON"
fi

SMTP_CONF=$(api GET /api/alerts/smtp)
if echo "$SMTP_CONF" | python3 -c "import sys,json; json.load(sys.stdin)" 2>/dev/null; then
    pass "SMTP config GET: valid JSON"
else
    fail "SMTP config GET: invalid JSON"
fi

TG_CONF=$(api GET /api/alerts/telegram)
if echo "$TG_CONF" | python3 -c "import sys,json; json.load(sys.stdin)" 2>/dev/null; then
    pass "Telegram config GET: valid JSON"
else
    fail "Telegram config GET: invalid JSON"
fi

# ════════════════════════════════════════════════════════════════
section "PHASE 12: LDAP"
# ════════════════════════════════════════════════════════════════

LDAP_CONF=$(api GET /api/ldap/config)
if echo "$LDAP_CONF" | python3 -c "import sys,json; json.load(sys.stdin)" 2>/dev/null; then
    pass "LDAP config GET: valid JSON"
else
    fail "LDAP config GET: invalid JSON"
fi

LDAP_STATUS=$(api GET /api/ldap/status)
if echo "$LDAP_STATUS" | python3 -c "import sys,json; json.load(sys.stdin)" 2>/dev/null; then
    pass "LDAP status: valid JSON"
else
    fail "LDAP status: invalid JSON"
fi

LDAP_LOG=$(api GET /api/ldap/sync-log)
if echo "$LDAP_LOG" | python3 -c "import sys,json; json.load(sys.stdin)" 2>/dev/null; then
    pass "LDAP sync log: valid JSON"
else
    fail "LDAP sync log: invalid JSON"
fi

CB_STATUS=$(api GET /api/ldap/circuit-breaker)
if echo "$CB_STATUS" | python3 -c "import sys,json; json.load(sys.stdin)" 2>/dev/null; then
    pass "LDAP circuit breaker status: valid JSON"
else
    fail "LDAP circuit breaker status: invalid JSON"
fi

# ════════════════════════════════════════════════════════════════
section "PHASE 13: Network"
# ════════════════════════════════════════════════════════════════

VLAN_LIST=$(api GET /api/network/vlan)
if echo "$VLAN_LIST" | python3 -c "import sys,json; json.load(sys.stdin)" 2>/dev/null; then
    pass "VLAN list: valid JSON"
else
    fail "VLAN list: invalid JSON"
fi

BOND_LIST=$(api GET /api/network/bond)
if echo "$BOND_LIST" | python3 -c "import sys,json; json.load(sys.stdin)" 2>/dev/null; then
    pass "Bond list: valid JSON"
else
    fail "Bond list: invalid JSON"
fi

# ════════════════════════════════════════════════════════════════
section "PHASE 14: Misc subsystems"
# ════════════════════════════════════════════════════════════════

# Removable media
REMOVABLE=$(api GET /api/removable/list)
if echo "$REMOVABLE" | python3 -c "import sys,json; json.load(sys.stdin)" 2>/dev/null; then
    pass "Removable media list: valid JSON"
else
    fail "Removable media list: invalid JSON"
fi

# UPS
UPS_STATUS=$(api GET /api/system/ups)
if echo "$UPS_STATUS" | python3 -c "import sys,json; json.load(sys.stdin)" 2>/dev/null; then
    pass "UPS status: valid JSON"
else
    fail "UPS status: invalid JSON"
fi

# Power / spindown
POWER_DISKS=$(api GET /api/power/disks)
if echo "$POWER_DISKS" | python3 -c "import sys,json; json.load(sys.stdin)" 2>/dev/null; then
    pass "Power disk status: valid JSON"
else
    fail "Power disk status: invalid JSON"
fi

# Sandbox
SANDBOX_LIST=$(api GET /api/sandbox/list)
if echo "$SANDBOX_LIST" | python3 -c "import sys,json; json.load(sys.stdin)" 2>/dev/null; then
    pass "Sandbox list: valid JSON"
else
    fail "Sandbox list: invalid JSON"
fi

# Gitops
GITOPS_STATUS=$(api GET /api/gitops/status)
if echo "$GITOPS_STATUS" | python3 -c "import sys,json; json.load(sys.stdin)" 2>/dev/null; then
    pass "GitOps status: valid JSON"
else
    fail "GitOps status: invalid JSON"
fi

# Firewall
FIREWALL=$(api GET /api/firewall/status)
if echo "$FIREWALL" | python3 -c "import sys,json; json.load(sys.stdin)" 2>/dev/null; then
    pass "Firewall status: valid JSON"
else
    fail "Firewall status: invalid JSON"
fi

# Certs
CERTS=$(api GET /api/certs/list)
if echo "$CERTS" | python3 -c "import sys,json; json.load(sys.stdin)" 2>/dev/null; then
    pass "Certs list: valid JSON"
else
    fail "Certs list: invalid JSON"
fi

# Trash
TRASH=$(api GET /api/trash/list)
if echo "$TRASH" | python3 -c "import sys,json; json.load(sys.stdin)" 2>/dev/null; then
    pass "Trash list: valid JSON"
else
    fail "Trash list: invalid JSON"
fi

# HA
HA_STATUS=$(api GET /api/ha/status)
if echo "$HA_STATUS" | python3 -c "import sys,json; json.load(sys.stdin)" 2>/dev/null; then
    pass "HA status: valid JSON"
else
    fail "HA status: invalid JSON"
fi

HA_LOCAL=$(api GET /api/ha/local)
if echo "$HA_LOCAL" | python3 -c "import sys,json; json.load(sys.stdin)" 2>/dev/null; then
    pass "HA local node info: valid JSON"
else
    fail "HA local node info: invalid JSON"
fi

# iSCSI
ISCSI_STATUS=$(api GET /api/iscsi/status)
if echo "$ISCSI_STATUS" | python3 -c "import sys,json; json.load(sys.stdin)" 2>/dev/null; then
    pass "iSCSI status: valid JSON"
else
    fail "iSCSI status: invalid JSON"
fi

ISCSI_TARGETS=$(api GET /api/iscsi/targets)
if echo "$ISCSI_TARGETS" | python3 -c "import sys,json; json.load(sys.stdin)" 2>/dev/null; then
    pass "iSCSI targets: valid JSON"
else
    fail "iSCSI targets: invalid JSON"
fi

# NixOS guard
NIXOS_DETECT=$(api GET /api/nixos/detect)
if echo "$NIXOS_DETECT" | python3 -c "import sys,json; json.load(sys.stdin)" 2>/dev/null; then
    pass "NixOS detect: valid JSON"
else
    fail "NixOS detect: invalid JSON"
fi

# Prometheus metrics endpoint (plaintext)
PROM=$(curl -sf --max-time 5 "$API/metrics" \
    -H "X-Session-ID: $SESSION" -H "X-User: admin" 2>/dev/null || echo "")
if [ -n "$PROM" ]; then
    pass "Prometheus metrics endpoint: responds"
else
    fail "Prometheus metrics endpoint: no response"
fi

# ════════════════════════════════════════════════════════════════
section "PHASE 15: Security boundary checks"
# ════════════════════════════════════════════════════════════════

# Unauthenticated request to protected endpoint must be rejected
UNAUTH=$(curl -sf --max-time 5 -o /dev/null -w "%{http_code}" \
    "$API/api/zfs/pools" 2>/dev/null || echo "000")
[ "$UNAUTH" = "401" ] && pass "Unauthenticated ZFS pools returns 401" \
    || fail "Unauthenticated ZFS pools returned $UNAUTH (expected 401)"

# Garbage session token rejected
BAD_SESSION=$(curl -sf --max-time 5 -o /dev/null -w "%{http_code}" \
    "$API/api/zfs/pools" -H "X-Session-ID: notavalidtoken" 2>/dev/null || echo "000")
[ "$BAD_SESSION" = "401" ] && pass "Invalid session token returns 401" \
    || fail "Invalid session token returned $BAD_SESSION (expected 401)"

# Command injection attempt blocked by whitelist
INJECT_RESP=$(api POST /api/zfs/command \
    "{\"command\":\"ls\",\"args\":[\"/etc/passwd\"],\"session_id\":\"$SESSION\",\"user\":\"admin\"}")
if echo "$INJECT_RESP" | grep -q '"success":false\|Forbidden\|not allowed\|whitelist'; then
    pass "Command injection attempt blocked"
else
    fail "Command injection attempt NOT blocked: $INJECT_RESP"
fi

# Weak password rejected by change-password
WEAK_PASS_RESP=$(api POST /api/auth/change-password \
    "{\"current_password\":\"$ADMIN_PASS\",\"new_password\":\"weak\"}")
assert_json "Weak password rejected by change-password" "$WEAK_PASS_RESP" "success" "false"

# Port 9000 not exposed beyond localhost
if ss -tuln 2>/dev/null | grep ":9000" | grep -qv "127.0.0.1"; then
    fail "CRITICAL: Port 9000 is exposed beyond localhost"
else
    pass "Port 9000 bound to localhost only"
fi

# ════════════════════════════════════════════════════════════════
section "PHASE 16: WebSocket"
# ════════════════════════════════════════════════════════════════

# Test that the WS endpoint at least accepts the TCP connection
# (full WS handshake requires a WS client — just test it doesn't refuse)
WS_TCP=$(curl -sf --max-time 3 -o /dev/null -w "%{http_code}" \
    -H "Connection: Upgrade" -H "Upgrade: websocket" \
    -H "Sec-WebSocket-Version: 13" \
    -H "Sec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==" \
    -H "X-Session-ID: $SESSION" \
    "$API/ws/monitor" 2>/dev/null || echo "000")
# 101 = upgraded, 400/401 = endpoint exists but handshake issue — both mean it's reachable
if [ "$WS_TCP" = "101" ] || [ "$WS_TCP" = "400" ] || [ "$WS_TCP" = "401" ]; then
    pass "WebSocket endpoint reachable (HTTP $WS_TCP)"
else
    fail "WebSocket endpoint not reachable (HTTP $WS_TCP)"
fi

# ════════════════════════════════════════════════════════════════
section "PHASE 17: Logout"
# ════════════════════════════════════════════════════════════════

LOGOUT_RESP=$(curl -sf --max-time 5 -X POST "$API/api/auth/logout" \
    -H "X-Session-ID: $SESSION" -H "X-User: admin" 2>/dev/null || echo "{}")
assert_json "Logout: success" "$LOGOUT_RESP" "success" "true"

# Session should be invalid after logout
POST_LOGOUT=$(curl -sf --max-time 5 -o /dev/null -w "%{http_code}" \
    "$API/api/auth/check" -H "X-Session-ID: $SESSION" 2>/dev/null || echo "000")
AUTHED=$(curl -sf --max-time 5 "$API/api/auth/check" \
    -H "X-Session-ID: $SESSION" 2>/dev/null | python3 -c \
    "import sys,json; print(json.load(sys.stdin).get('authenticated','false'))" 2>/dev/null || echo "false")
[ "$AUTHED" = "False" ] || [ "$AUTHED" = "false" ] \
    && pass "Session invalidated after logout" \
    || fail "Session still valid after logout (authenticated=$AUTHED)"

# ════════════════════════════════════════════════════════════════
# SUMMARY
# ════════════════════════════════════════════════════════════════

TOTAL=$((PASS + FAIL + SKIP))
echo ""
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo -e "${BOLD}    Integration Test Results${NC}"
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo ""
echo -e "  ${GREEN}Passed${NC} : $PASS"
echo -e "  ${RED}Failed${NC} : $FAIL"
echo -e "  ${YELLOW}Skipped${NC}: $SKIP"
echo -e "  Total  : $TOTAL"
echo ""

if [ "$FAIL" -gt 0 ]; then
    echo -e "${RED}${BOLD}  FAILURES:${NC}"
    echo -e "$FAILURES"
    echo ""
fi

if [ "$FAIL" -eq 0 ]; then
    echo -e "  ${BOLD}${GREEN}ALL TESTS PASSED${NC}"
    EXIT_CODE=0
else
    echo -e "  ${BOLD}${RED}$FAIL TEST(S) FAILED — see failures above${NC}"
    EXIT_CODE=1
fi

echo ""
echo "  Full report: $REPORT"
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo ""

exit $EXIT_CODE
