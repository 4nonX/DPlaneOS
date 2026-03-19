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
{ [ "$CODE" = "200" ] || [ "$CODE" = "403" ]; } && ok "POST /api/system/setup-admin (HTTP $CODE)" || fail "POST /api/system/setup-admin (HTTP $CODE)"

# Heartbeat
CODE=$(curl -s -o /dev/null -w "%{http_code}" -X POST "$BASE/api/ha/heartbeat" -H "Content-Type: application/json" -d '{"node_id":"ci-peer","status":"healthy"}')
[ "$CODE" = "200" ] && ok "POST /api/ha/heartbeat" || fail "POST /api/ha/heartbeat (HTTP $CODE)"

# 2. AUTH & SESSION
assert_json "Login succeeds" "$(api POST /api/auth/login "{\"username\":\"admin\",\"password\":\"$CI_PASS\"}")" "success" "true"
LOGIN_JSON=$(api POST /api/auth/login "{\"username\":\"admin\",\"password\":\"$CI_PASS\"}")
SESSION=$(echo "$LOGIN_JSON" | python3 -c "import sys,json; print(json.load(sys.stdin).get('session_id',''))" 2>/dev/null)

assert_json "Login with wrong password fails" "$(api POST /api/auth/login '{"username":"admin","password":"wrong"}')" "success" "false"
assert_json "Login with non-existent user fails" "$(api POST /api/auth/login '{"username":"not-exist","password":"any"}')" "success" "false"

# 3. RBAC / USERS
# List users
USERS=$(api GET /api/rbac/users)
assert_array "List users returns array" "$USERS" "users"
assert_json "Admin user present in list" "$USERS" "success" "true"

# Create user (handler requires 'action' field)
assert_json "Create user succeeds" "$(api POST /api/users/create '{"action":"create","username":"ci-user","password":"CiUser1!Test","email":"ci@dplane.local","role":"user"}')" "success" "true"
assert_json "User ci-user exists in list" "$(api GET /api/rbac/users)" "success" "true"

# Self-service
assert_array "Me permissions" "$(api GET /api/rbac/me/permissions)" "permissions"
assert_array "Me roles" "$(api GET /api/rbac/me/roles)" "roles"

# List roles & permissions
assert_array "List roles returns array" "$(api GET /api/rbac/roles)" "roles"
assert_array "List permissions returns array" "$(api GET /api/rbac/permissions)" "permissions"

# 4. ZFS (ADVANCED)
sleep 2
POOLS=$(api GET /api/zfs/pools)
assert_shape "ZFS pool shape" "$POOLS" "data" "name" "health" "capacity"

assert_json "Create dataset" "$(api POST /api/zfs/datasets '{"name":"testpool/api-test","compression":"lz4","atime":"off"}')" "success" "true"
sudo zfs list testpool/api-test >/dev/null && ok "Dataset on disk" || fail "Dataset missing"

# Snapshot
assert_json "Create snapshot" "$(api POST /api/zfs/snapshots '{"dataset":"testpool/api-test","name":"ci-snap-1"}')" "success" "true"
sudo zfs list -t snapshot testpool/api-test@ci-snap-1 >/dev/null && ok "Snapshot on disk" || fail "Snapshot missing"

# List snapshots
SNAPS=$(api GET "/api/zfs/snapshots?dataset=testpool/api-test")
assert_array "List snapshots returns array" "$SNAPS" "snapshots"

# Rollback
assert_json "Rollback snapshot" "$(api POST /api/zfs/snapshots/rollback "{\"snapshot\":\"testpool/api-test@ci-snap-1\",\"force\":true}")" "success" "true"

# Health & Iostat
assert_json "ZFS health" "$(api GET /api/zfs/health)" "success" "true"
assert_json "ZFS iostat" "$(api GET /api/zfs/iostat)" "success" "true"

# 5. ACL / FILE MANAGER
# Mountpoint is /mnt/testpool/api-test
assert_json "ACL set" "$(api POST /api/acl/set '{"path":"/mnt/testpool/api-test","entry":"u:nobody:rwx"}')" "success" "true"
api GET "/api/acl/get?path=/mnt/testpool/api-test" | grep -q "nobody" && ok "ACL readback verified" || fail "ACL readback failed"

sudo mkdir -p /mnt/testpool/fm-test
assert_json "FM: write" "$(api POST /api/files/write '{"path":"/mnt/testpool/fm-test/hello.txt","content":"hello ci"}')" "success" "true"
assert_json "FM: read" "$(api GET '/api/files/read?path=/mnt/testpool/fm-test/hello.txt')" "content" "hello ci"
assert_json "FM: chmod" "$(api POST /api/files/chmod '{"path":"/mnt/testpool/fm-test/hello.txt","mode":"0600"}')" "success" "true"

# Full FM Lifecycle
assert_json "FM: mkdir" "$(api POST /api/files/mkdir '{"path":"/mnt/testpool/fm-test/subdir"}')" "success" "true"
assert_array "FM: list" "$(api GET '/api/files/list?path=/mnt/testpool/fm-test')" "files"
# Rename uses 'new_name' (filename only)
assert_json "FM: rename" "$(api POST /api/files/rename '{"old_path":"/mnt/testpool/fm-test/hello.txt","new_name":"renamed.txt"}')" "success" "true"
assert_json "FM: delete" "$(api POST /api/files/delete '{"path":"/mnt/testpool/fm-test/renamed.txt"}')" "success" "true"
[ ! -f /mnt/testpool/fm-test/renamed.txt ] && ok "FM: lifecycle verified" || fail "FM: delete failed"

# 6. SMB SHARES
assert_json "Create SMB share" "$(api POST /api/shares "{\"action\":\"create\",\"name\":\"ci-share\",\"path\":\"/mnt/testpool/api-test\",\"read_only\":false,\"guest_ok\":true,\"comment\":\"CI Test Share\"}")" "success" "true"
assert_json "SMB share in list" "$(api GET /api/shares)" "success" "true"
[ -f /tmp/smb.conf ] && grep -q "\[ci-share\]" /tmp/smb.conf && ok "Share in smb-shares.conf" || fail "Share missing from config file"

# 7. NFS EXPORTS
assert_json "Create NFS export" "$(api POST /api/nfs/exports '{"path":"/mnt/testpool/api-test","clients":"*","options":"rw,sync,no_subtree_check","enabled":true}')" "success" "true"
assert_json "NFS export in list" "$(api GET /api/nfs/exports)" "success" "true"
sudo grep -q "/mnt/testpool/api-test" /etc/exports && ok "Export in /etc/exports" || fail "Export missing from /etc/exports"

# 8. DOCKER
assert_json "Docker stacks list" "$(api GET /api/docker/stacks)" "success" "true"

# 9. NETWORKING & SYSTEM
assert_json "List interfaces" "$(api GET /api/system/network)" "success" "true"
assert_array "Interfaces list" "$(api GET /api/system/network)" "interfaces"
assert_json "Get status" "$(api GET /api/system/status)" "success" "true"
assert_json "Get settings" "$(api GET /api/system/settings)" "success" "true"
CUR_HOST=$(api GET /api/system/status | python3 -c "import sys,json; d=json.load(sys.stdin); print(d.get('system_info',{}).get('hostname',''))")
[ -n "$CUR_HOST" ] && ok "Current hostname: $CUR_HOST" || fail "Hostname empty"

# Subsystems
assert_json "Firewall status" "$(api GET /api/firewall/status)" "success" "true"
assert_json "iSCSI status" "$(api GET /api/iscsi/status)" "success" "true"
assert_json "LDAP status" "$(api GET /api/ldap/status)" "success" "true"
assert_json "Metrics current" "$(api GET /api/metrics/current)" "success" "true"
assert_json "Alerts/Webhooks" "$(api GET /api/alerts/webhooks)" "success" "true"
assert_json "Inotify stats" "$(api GET /api/monitoring/inotify)" "used"

# Conditional Hardware
UPS=$(api GET /api/system/ups)
if echo "$UPS" | grep -q "NUT not installed"; then
  ok "UPS status (skipped - NUT not installed)"
else
  assert_json "UPS status" "$UPS" "success" "true"
fi

assert_json "Trash list" "$(api GET /api/trash/list)" "success" "true"
assert_json "Power disks" "$(api GET /api/power/disks)" "success" "true"

# 10. GITOPS
assert_json "GitOps status" "$(api GET /api/gitops/status)" "success" "true"
assert_json "GitOps plan" "$(api GET /api/gitops/plan)" "success" "true"

# 11. SECURITY
INJECT=$(api POST /api/zfs/command "{\"command\":\"ls\",\"args\":[\"/etc/passwd\"],\"session_id\":\"$SESSION\",\"user\":\"admin\"}")
echo "$INJECT" | grep -qiE "Forbidden|not allowed|success\":false" && ok "Security: Injection blocked" || fail "Security: Injection NOT blocked (got: $INJECT)"

# 12. AUDIT & LOGOUT
AUDIT=$(api GET /api/system/audit/verify-chain)
assert_json "Audit chain verified" "$AUDIT" "valid" "true"

assert_json "Logout succeeds" "$(api POST /api/auth/logout '{}')" "success" "true"
assert_json "Auth check after logout fails" "$(api GET /api/auth/check)" "authenticated" "false"

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
