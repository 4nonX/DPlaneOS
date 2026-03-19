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
# Use /mnt/testpool to stay within daemon's allowedBasePaths
sudo mkdir -p /mnt/testpool
sudo zpool create -f -m /mnt/testpool testpool mirror $LOOP0 $LOOP1
sudo zfs set acltype=posixacl testpool

# daemon expects this for audit key
sudo mkdir -p /var/lib/dplaneos

# USE LOCAL DB FOR CI RELIABILITY
DB_PATH="$(pwd)/ci-integration.db"
rm -f "$DB_PATH"*
echo "--- Using Database: $DB_PATH ---"

echo "--- v6: Deterministic Bootstrap (-apply) ---"
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
    mountpoint: /mnt/testpool
  - name: testpool/ci-enforcement
    mountpoint: /mnt/testpool/ci-enforcement
EOF

sudo ./dplaned-ci --db "$DB_PATH" --gitops-state /tmp/state.yaml --smb-conf /tmp/smb.conf --apply

echo "--- v6: Serialization Round-trip ---"
sudo ./dplaned-ci --db "$DB_PATH" --gitops-state /tmp/state.yaml --test-serialization

echo "--- v6: Deterministic Idempotency ---"
sudo ./dplaned-ci --db "$DB_PATH" --gitops-state /tmp/state.yaml --smb-conf /tmp/smb.conf --test-idempotency

echo "--- Starting Daemon ---"
sudo ./dplaned-ci --listen 127.0.0.1:9000 --db "$DB_PATH" --gitops-state /tmp/state.yaml --smb-conf /tmp/smb.conf > /tmp/dplaned.log 2>&1 &
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
sudo sqlite3 "$DB_PATH" "UPDATE users SET password_hash='$(echo "$CI_HASH" | sed "s/'/''/g")', active=1, role='admin', must_change_password=0 WHERE username='admin';"

# --- TEST UTILITIES ---
BASE="http://127.0.0.1:9000"
PASS=0; FAIL=0; FAILURES=""
SESSION=""

ok()   { echo "  ✓ $1"; PASS=$((PASS+1)); }
fail() { echo "  ✗ $1"; FAIL=$((FAIL+1)); FAILURES="${FAILURES}\n  ✗ $1"; }

# api: Helper that handles auth and returns full response (even on error)
api() {
  local method="$1" path="$2" body="${3:-}"
  local args=(-s --max-time 15 -X "$method" "$BASE$path" -H "X-Session-ID: $SESSION" -H "X-User: admin" -H "Content-Type: application/json")
  [ -n "$body" ] && args+=(-d "$body")
  
  local resp=$(curl "${args[@]}" 2>/dev/null)
  if [ -z "$resp" ]; then
    echo '{"_err":"empty response"}'
  else
    echo "$resp"
  fi
}

# Python-based assertion engine (much cleaner than nested shell/sed)
python_assert() {
  local script="$1"
  python3 -c "$script" 2>/dev/null
}

assert_json() {
  local label="$1" resp="$2" key="$3" expected="${4:-}"
  export ASSERT_RESP="$resp"
  export ASSERT_KEY="$key"
  export ASSERT_EXPECTED="$expected"
  if python3 -c "
import sys, json, os
try:
    d = json.loads(os.environ['ASSERT_RESP'])
    key = os.environ['ASSERT_KEY']
    expected = os.environ['ASSERT_EXPECTED']
    val = d
    for k in key.split('.'):
        val = val.get(k) if isinstance(val, dict) else None
    
    if expected:
        success = str(val).lower() == expected.lower()
    else:
        success = bool(val)
    sys.exit(0 if success else 1)
except Exception as e:
    sys.exit(1)
"; then ok "$label"; else fail "$label (got: ${resp:0:100})"; fi
}

assert_shape() {
  local label="$1" resp="$2" arr_key="$3"; shift 3
  local required_keys=("$@")
  export ASSERT_RESP="$resp"
  export ASSERT_ARR_KEY="$arr_key"
  export ASSERT_KEYS=$(printf "%s," "${required_keys[@]}" | sed 's/,$//')
  
  if python3 -c "
import sys, json, os
try:
    d = json.loads(os.environ['ASSERT_RESP'])
    arr_key = os.environ['ASSERT_ARR_KEY']
    req_keys = [k.strip() for k in os.environ['ASSERT_KEYS'].split(',') if k.strip()]
    
    if not d.get('success'): 
        print(f'Error: success is {d.get(\"success\")}'); sys.exit(1)
    arr = d.get(arr_key)
    if not isinstance(arr, list): 
        print(f'Error: {arr_key} is not a list'); sys.exit(1)
    if len(arr) == 0: 
        sys.exit(0)
    
    first = arr[0]
    missing = [k for k in req_keys if k not in first]
    if missing:
        print(f'Error: Missing keys {missing} in first element keys: {list(first.keys())}')
        sys.exit(1)
    sys.exit(0)
except Exception as e:
    print(f'Error: Exception {e}')
    sys.exit(1)
"; then ok "$label"; else fail "$label (shape mismatch - check log)"; fi
}

assert_array() {
  local label="$1" resp="$2" key="$3"
  export ASSERT_RESP="$resp"
  export ASSERT_KEY="$key"
  if python3 -c "
import sys, json, os
try:
    d = json.loads(os.environ['ASSERT_RESP'])
    key = os.environ['ASSERT_KEY']
    val = d
    for k in key.split('.'):
        val = val.get(k) if isinstance(val, dict) else None
    sys.exit(0 if isinstance(val, list) else 1)
except:
    sys.exit(1)
"; then ok "$label"; else fail "$label (not an array)"; fi
}

valid_json() {
  echo "$1" | python3 -c "import sys,json; json.load(sys.stdin)" >/dev/null 2>&1
}

echo "--- Starting API Integration Suite ---"

# 1. PRE-AUTH
CODE=$(curl -s -o /dev/null -w "%{http_code}" "$BASE/api/system/status")
[ "$CODE" = "200" ] && ok "GET /api/system/status" || fail "GET /api/system/status (HTTP $CODE)"

# Setup admin
CODE=$(curl -s -o /dev/null -w "%{http_code}" -X POST "$BASE/api/system/setup-admin" -H "Content-Type: application/json" -d "{\"username\":\"admin\",\"password\":\"$CI_PASS\"}")
[ "$CODE" = "200" ] || [ "$CODE" = "403" ] && ok "POST /api/system/setup-admin (HTTP $CODE)" || fail "POST /api/system/setup-admin (HTTP $CODE)"

# Heartbeat
CODE=$(curl -s -o /dev/null -w "%{http_code}" -X POST "$BASE/api/ha/heartbeat" -H "Content-Type: application/json" -d '{"node_id":"ci-peer","status":"healthy"}')
[ "$CODE" = "200" ] && ok "POST /api/ha/heartbeat" || fail "POST /api/ha/heartbeat (HTTP $CODE)"

# 2. AUTH
LOGIN_JSON=$(curl -s -X POST $BASE/api/auth/login -H "Content-Type: application/json" -d "{\"username\":\"admin\",\"password\":\"$CI_PASS\"}")
assert_json "Login succeeds" "$LOGIN_JSON" "success" "true"
SESSION=$(echo "$LOGIN_JSON" | python3 -c "import sys,json; print(json.load(sys.stdin).get('session_id',''))" 2>/dev/null)

# 3. ZFS
sleep 2
POOLS=$(api GET /api/zfs/pools)
echo "DEBUG POOLS: $POOLS"
assert_shape "ZFS pool shape" "$POOLS" "data" "name" "health" "capacity"

assert_json "Create dataset" "$(api POST /api/zfs/datasets '{"name":"testpool/ci-test","compression":"lz4"}')" "success" "true"
sudo zfs list testpool/ci-test >/dev/null && ok "Dataset on disk" || fail "Dataset missing"

# 4. ACL / FM
# Mountpoint is /mnt/testpool/ci-test
assert_json "ACL set" "$(api POST /api/acl/set '{"path":"/mnt/testpool/ci-test","entry":"u:nobody:rwx"}')" "success" "true"
api GET "/api/acl/get?path=/mnt/testpool/ci-test" | grep -q "nobody" && ok "ACL readback verified" || fail "ACL readback failed"

sudo mkdir -p /mnt/testpool/fm-test
assert_json "FM: write" "$(api POST /api/files/write '{"path":"/mnt/testpool/fm-test/hello.txt","content":"hello ci"}')" "success" "true"
assert_json "FM: read" "$(api GET '/api/files/read?path=/mnt/testpool/fm-test/hello.txt')" "content" "hello ci"
assert_json "FM: chmod" "$(api POST /api/files/chmod '{"path":"/mnt/testpool/fm-test/hello.txt","mode":"0600"}')" "success" "true"
[ "$(sudo stat -c %a /mnt/testpool/fm-test/hello.txt)" = "600" ] && ok "FM: chmod verified" || fail "FM: chmod failed"

# 5. GITOPS
assert_json "GitOps status" "$(api GET /api/gitops/status)" "success" "true"
assert_json "GitOps plan" "$(api GET /api/gitops/plan)" "success" "true"

# 6. SECURITY
INJECT=$(api POST /api/zfs/command "{\"command\":\"ls\",\"args\":[\"/etc/passwd\"],\"session_id\":\"$SESSION\",\"user\":\"admin\"}")
# Check if it was blocked by middleware (403/Forbidden) or by the handler explicitly
echo "$INJECT" | grep -qiE "Forbidden|not allowed|success\":false" && ok "Security: Injection blocked" || fail "Security: Injection NOT blocked (got: $INJECT)"

# 7. AUDIT
# Since we did ZFS operations, there should be an audit chain
AUDIT=$(api GET /api/system/audit/verify-chain)
assert_json "Audit chain verified" "$AUDIT" "valid" "true"

# --- SUMMARY ---
echo ""
echo "=========================================="
printf "  Results: %d passed   %d failed\n" "$PASS" "$FAIL"
echo "=========================================="

if [ "$FAIL" -gt 0 ]; then
  # Use printf with cat to avoid format character issues
  echo "Failures:"
  echo -e "$FAILURES"
  exit 1
fi
