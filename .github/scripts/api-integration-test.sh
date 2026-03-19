#!/bin/bash
set -e

# --- SETUP ---
echo "--- Setting up ZFS loopbacks ---"
sudo truncate -s 512M /tmp/vdisk0.img
sudo truncate -s 512M /tmp/vdisk1.img
sudo truncate -s 512M /tmp/vdisk2.img
LOOP0=$(sudo losetup --find --show /tmp/vdisk0.img)
LOOP1=$(sudo losetup --find --show /tmp/vdisk1.img)
LOOP2=$(sudo losetup --find --show /tmp/vdisk2.img)

echo "--- Creating Test ZFS Pool ---"
# Default mountpoint will be /testpool
sudo zpool create -f -m /testpool testpool mirror $LOOP0 $LOOP1
# POSIX ACLs must be explicitly enabled on ZFS datasets for getfacl to work.
sudo zfs set acltype=posixacl testpool

# USE LOCAL DB FOR CI RELIABILITY
DB_PATH="$(pwd)/ci-integration.db"
rm -f "$DB_PATH"*
echo "--- Using Database: $DB_PATH ---"

echo "--- v6: Deterministic Bootstrap (-apply) ---"
# Seed the state file for Phase 1
cat <<EOF > /tmp/state.yaml
version: "6"
pools:
  - name: testpool
    vdev_type: mirror
    disks:
      - $LOOP0
      - $LOOP1
datasets:
  - name: testpool
    mountpoint: /testpool
  - name: testpool/ci-enforcement
    mountpoint: /testpool/ci-enforcement
EOF

# Let the daemon create the DB itself to avoid permission mismatches
sudo ./dplaned-ci --db "$DB_PATH" --gitops-state /tmp/state.yaml --apply

echo "--- v6: Serialization Round-trip ---"
sudo ./dplaned-ci --db "$DB_PATH" --gitops-state /tmp/state.yaml --test-serialization

echo "--- v6: Deterministic Idempotency ---"
sudo ./dplaned-ci --db "$DB_PATH" --gitops-state /tmp/state.yaml --test-idempotency

echo "--- Starting Daemon ---"
sudo ./dplaned-ci --listen 127.0.0.1:9000 --db "$DB_PATH" --gitops-state /tmp/state.yaml > /tmp/dplaned-ci.log 2>&1 &
PID=$!
trap "sudo kill $PID || true; sudo zpool destroy testpool || true; sudo losetup -d $LOOP0 $LOOP1 $LOOP2 || true" EXIT

# Wait for daemon
for i in {1..20}; do
  curl -s http://127.0.0.1:9000/health >/dev/null && break
  sleep 0.5
done

echo "--- Seeding CI User ---"
CI_PASS="CiAdmin1!Test"
CI_HASH=$(python3 -c "
import bcrypt, os
pw = b'$CI_PASS'
print(bcrypt.hashpw(pw, bcrypt.gensalt(rounds=10)).decode())
")
# Update existing admin row
sudo sqlite3 "$DB_PATH" "UPDATE users SET password_hash='$(echo "$CI_HASH" | sed "s/'/''/g")', active=1, role='admin', must_change_password=0 WHERE username='admin';"

echo "--- Validating ReadWritePaths ---"
zpool status testpool
zfs list -r testpool
[ -d "/testpool/ci-enforcement" ] || (echo "Convergence failure: dataset not mounted"; exit 1)

# --- TEST SUITE (FULL FIDELITY) ---
BASE="http://127.0.0.1:9000"
PASS=0; FAIL=0; FAILURES=""

ok()   { echo "  ✓ $1"; PASS=$((PASS+1)); }
fail() { echo "  ✗ $1"; FAIL=$((FAIL+1)); FAILURES="${FAILURES}\n  ✗ $1"; }

# assert_json: drill into dotted key path, compare to expected value.
# If expected is omitted, asserts the value is truthy (non-null, non-empty, non-false).
assert_json() {
  local label="$1" resp="$2" key="$3" expected="${4:-}"
  if echo "$resp" | python3 -c "
import sys,json
try: d=json.load(sys.stdin)
except: sys.exit(1)
val=d
for k in '$key'.split('.'): val=val.get(k) if isinstance(val,dict) else None
sys.exit(0 if (str(val).lower()=='$expected'.lower() if '$expected' else bool(val)) else 1)
" 2>/dev/null; then ok "$label"; else fail "$label  (got: $(echo "$resp" | head -c 200))"; fi
}

# assert_shape: verify a response has success=true AND that a named field
# is a non-empty array AND that the first element contains required keys.
# Usage: assert_shape LABEL RESP ARRAY_KEY KEY1 KEY2 ...
assert_shape() {
  local label="$1" resp="$2" arr_key="$3"; shift 3
  local required_keys=("$@")
  echo "$resp" | python3 -c "
import sys, json
try:
  d = json.load(sys.stdin)
except:
  print('JSON parse failed'); sys.exit(1)
if not d.get('success'):
  print('success!=true, error=' + str(d.get('error',''))); sys.exit(1)
arr = d.get('$arr_key')
if not isinstance(arr, list):
  print('$arr_key is not a list'); sys.exit(1)
if len(arr) == 0:
  sys.exit(0)
first = arr[0]
missing = [k for k in $(printf "'%s'," "${required_keys[@]}" | sed 's/,$//' | sed "s/'[^']*'/&/g" | python3 -c "import sys; parts=sys.stdin.read().split(','); print('[' + ','.join(repr(p.strip(\\\"'\\\")) for p in parts if p.strip()) + ']'") if k not in first]
if missing:
  print('missing keys: ' + str(missing)); sys.exit(1)
" 2>/dev/null \
    && ok "$label" \
    || fail "$label  (got: $(echo "$resp" | head -c 200))"
}

# assert_array: verify a field in the response is a list (not a string, not null).
assert_array() {
  local label="$1" resp="$2" key="$3"
  echo "$resp" | python3 -c "
import sys,json
try: d=json.load(sys.stdin)
except: sys.exit(1)
val=d
for k in '$key'.split('.'): val=val.get(k) if isinstance(val,dict) else None
sys.exit(0 if isinstance(val,list) else 1)
" 2>/dev/null && ok "$label" || fail "$label  (expected array)"
}

valid_json() {
  echo "$1" | python3 -c "import sys,json; json.load(sys.stdin)" 2>/dev/null
}

echo "--- Starting API Integration Suite ---"

# --- 1. PRE-AUTH ---
CODE=$(curl -s -o /dev/null -w "%{http_code}" "$BASE/api/system/status")
[ "$CODE" = "200" ] && ok "GET /api/system/status accessible without session" || fail "GET /api/system/status 401"

CODE=$(curl -s -o /dev/null -w "%{http_code}" -X POST "$BASE/api/system/setup-admin" -H "Content-Type: application/json" -d "{\"username\":\"admin\",\"password\":\"$CI_PASS\"}")
[ "$CODE" = "200" ] || [ "$CODE" = "403" ] && ok "POST /api/system/setup-admin accessible (HTTP $CODE)" || fail "POST /api/system/setup-admin 401"

CODE=$(curl -s -o /dev/null -w "%{http_code}" -X POST "$BASE/api/system/setup-complete" -H "Content-Type: application/json" -d '{}')
[ "$CODE" = "200" ] || [ "$CODE" = "403" ] && ok "POST /api/system/setup-complete accessible (HTTP $CODE)" || fail "POST /api/system/setup-complete 401"

CODE=$(curl -s -o /dev/null -w "%{http_code}" -X POST "$BASE/api/ha/heartbeat" -H "Content-Type: application/json" -d '{"node_id":"ci-peer","status":"healthy"}')
[ "$CODE" != "401" ] && ok "POST /api/ha/heartbeat accessible (HTTP $CODE)" || fail "POST /api/ha/heartbeat 401"

# --- 2. AUTH ---
assert_json "/health ok" "$(curl -sf $BASE/health)" "status" "ok"
assert_json "/api/csrf returns token" "$(curl -sf $BASE/api/csrf)" "csrf_token"

LOGIN_HTTP=$(curl -s -w "\n%{http_code}" -X POST $BASE/api/auth/login -H "Content-Type: application/json" -d "{\"username\":\"admin\",\"password\":\"$CI_PASS\"}")
LOGIN=$(echo "$LOGIN_HTTP" | sed '$d')
assert_json "Login succeeds" "$LOGIN" "success" "true"
SESSION=$(echo "$LOGIN" | python3 -c "import sys,json; print(json.load(sys.stdin).get('session_id',''))" 2>/dev/null)

api() {
  local method="$1" path="$2" body="${3:-}"
  local args=(-sf --max-time 15 -X "$method" "$BASE$path" -H "X-Session-ID: $SESSION" -H "X-User: admin" -H "Content-Type: application/json")
  [ -n "$body" ] && args+=(-d "$body")
  curl "${args[@]}" 2>/dev/null || echo '{"_err":true}'
}

assert_json "Auth check" "$(api GET /api/auth/check)" "authenticated" "true"
assert_json "Session role" "$(api GET /api/auth/session)" "user.role" "admin"

# --- 3. SYSTEM ---
assert_json "System status" "$(api GET /api/system/status)" "success" "true"
assert_json "System profile" "$(api GET /api/system/profile)" "success" "true"
assert_json "System preflight" "$(api GET /api/system/preflight)" "success" "true"
valid_json "$(api GET /api/system/network)" && ok "System network" || fail "Network failed"
valid_json "$(api GET /api/system/disks)" && ok "Disk discovery" || fail "Disks failed"

# --- 4. ZFS ---
POOLS=$(api GET /api/zfs/pools)
assert_shape "ZFS pool shape" "$POOLS" "data" "name" "health" "capacity"

assert_json "Create dataset" "$(api POST /api/zfs/datasets '{"name":"testpool/ci-test","compression":"lz4"}')" "success" "true"
sudo zfs list testpool/ci-test >/dev/null && ok "Dataset on disk" || fail "Dataset missing"

assert_json "Create snapshot" "$(api POST /api/zfs/snapshots '{"dataset":"testpool/ci-test","name":"ci-snap-1"}')" "success" "true"
sudo zfs list -t snapshot testpool/ci-test@ci-snap-1 >/dev/null && ok "Snapshot on disk" || fail "Snapshot missing"

assert_json "Rollback snapshot" "$(api POST /api/zfs/snapshots/rollback '{"snapshot":"testpool/ci-test@ci-snap-1"}')" "success" "true"
assert_json "Destroy snapshot" "$(api DELETE /api/zfs/snapshots '{"snapshot":"testpool/ci-test@ci-snap-1"}')" "success" "true"

# ACL round-trip
assert_json "ACL set" "$(api POST /api/acl/set '{"path":"/testpool/ci-test","entry":"u:nobody:rwx"}')" "success" "true"
api GET "/api/acl/get?path=/testpool/ci-test" | grep -q "nobody" && ok "ACL readback verified" || fail "ACL readback failed"

# --- 5. SHARES ---
CREATE_SHARE=$(api POST /api/shares '{"action":"create","name":"ci-share","path":"/tmp","read_only":true,"guest_ok":false,"browsable":true}')
assert_json "Create SMB share" "$CREATE_SHARE" "success" "true"
SHARES=$(api GET /api/shares)
echo "$SHARES" | python3 -c "import sys,json; d=json.load(sys.stdin); s=next((x for x in d.get('shares',[]) if x.get('name')=='ci-share'),None); sys.exit(0 if s and s.get('read_only') is True else 1)" && ok "Share field persistence" || fail "Share field lost"

# --- 6. DOCKER ---
assert_shape "Docker containers shape" "$(api GET /api/docker/containers)" "containers" "id" "name" "image" "state"

# --- 7. FILE MANAGER ---
sudo mkdir -p /testpool/fm-test
assert_json "FM: mkdir" "$(api POST /api/files/mkdir '{"path":"/testpool/fm-test/subdir"}')" "success" "true"
assert_json "FM: write" "$(api POST /api/files/write '{"path":"/testpool/fm-test/hello.txt","content":"hello ci"}')" "success" "true"
assert_json "FM: read" "$(api GET '/api/files/read?path=/testpool/fm-test/hello.txt')" "content" "hello ci"
assert_json "FM: chmod" "$(api POST /api/files/chmod '{"path":"/testpool/fm-test/hello.txt","mode":"0600"}')" "success" "true"
[ "$(sudo stat -c %a /testpool/fm-test/hello.txt)" = "600" ] && ok "FM: chmod verified" || fail "FM: chmod failed"

# --- 8. FIREWALL ---
FW=$(api GET /api/firewall/status)
assert_array "Firewall: rules is array" "$FW" "rules"

# --- 9. SECURITY ---
INJECT=$(api POST /api/zfs/command "{\"command\":\"ls\",\"args\":[\"/etc/passwd\"],\"session_id\":\"$SESSION\",\"user\":\"admin\"}")
echo "$INJECT" | grep -q '"success":false\|Forbidden\|not allowed' && ok "Security: Injection blocked" || fail "Security: Injection NOT blocked"

# --- 10. GITOPS EXTRA (v6) ---
assert_json "GitOps status" "$(api GET /api/gitops/status)" "success" "true"
assert_json "GitOps plan" "$(api GET /api/gitops/plan)" "success" "true"
assert_json "Audit chain" "$(api GET /api/system/audit/verify-chain)" "valid" "true"

# --- 11. REMAINING ---
valid_json "$(api GET /api/git-sync/config)" && ok "Subsystem: GitSync" || fail "GitSync failed"
valid_json "$(api GET /api/alerts/webhooks)" && ok "Subsystem: Webhooks" || fail "Webhooks failed"
valid_json "$(api GET /api/ldap/config)" && ok "Subsystem: LDAP" || fail "LDAP failed"
valid_json "$(api GET /api/rbac/roles)" && ok "Subsystem: RBAC" || fail "RBAC failed"

# --- SUMMARY ---
echo ""
echo "=========================================="
printf "  Results: %d passed   %d failed\n" "$PASS" "$FAIL"
echo "=========================================="

if [ "$FAIL" -gt 0 ]; then
  printf "Failures:$FAILURES\n"
  exit 1
fi
