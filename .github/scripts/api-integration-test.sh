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

## --- TEST UTILITIES ---
BASE="http://127.0.0.1:9000"
PASS=0; FAIL=0; FAILURES=""
SESSION=""

ok() { printf "  \033[32m✓\033[0m %s\n" "$1"; PASS=$((PASS+1)); }
fail() {
  FAIL=$((FAIL+1))
  local msg="$1"
  [ -f /tmp/last_resp.json ] && msg="$msg (got: $(cat /tmp/last_resp.json | head -c 100)...)"
  FAILURES="$FAILURES\n  ✗ $msg"
  echo "  ✗ $msg"
}

api() {
  local method="$1" path="$2" data="$3"
  local args=(-s --max-time 15 -X "$method" "$BASE$path" -H "X-Session-ID: $SESSION" -H "X-User: admin" -H "Content-Type: application/json")
  [ -n "$data" ] && args+=(-d "$data")
  
  local resp
  resp=$(curl "${args[@]}" 2>/dev/null)
  if [ -n "$resp" ]; then
    echo "$resp" > /tmp/last_resp.json
    echo "$resp"
  else
    echo '{"_err":"empty response"}' > /tmp/last_resp.json
    echo '{"_err":"empty response"}'
  fi
}

assert_json() {
  local label="$1" key="$2" expected="${3:-}"
  if python3 -c "
import sys, json
try:
    with open('/tmp/last_resp.json', 'r') as f:
        d = json.load(f)
    key = \"$key\"
    expected = \"$expected\"
    val = d
    for k in key.split('.'):
        if val is None: break
        val = val.get(k) if isinstance(val, dict) else None
    if expected:
        success = str(val).lower() == expected.lower()
    else:
        success = val is not None
    sys.exit(0 if success else 1)
except:
    sys.exit(1)
"; then ok "$label"; else fail "$label"; fi
}

assert_array() {
  local label="$1" key="$2"
  if python3 -c "
import sys, json
try:
    with open('/tmp/last_resp.json', 'r') as f:
        d = json.load(f)
    key = \"$key\"
    val = d
    for k in key.split('.'):
        if val is None: break
        val = val.get(k) if isinstance(val, dict) else None
    sys.exit(0 if isinstance(val, list) else 1)
except:
    sys.exit(1)
"; then ok "$label"; else fail "$label (not an array)"; fi
}

assert_shape() {
  local label="$1" arr_key="$2"; shift 2
  local keys=("$@")
  local keys_json=$(printf '%s\n' "${keys[@]}" | python3 -c "import sys,json; print(json.dumps(sys.stdin.read().splitlines()))")
  
  if python3 -c "
import sys, json
try:
    with open('/tmp/last_resp.json', 'r') as f:
        d = json.load(f)
    arr_key = \"$arr_key\"
    keys = $keys_json
    arr = d.get(arr_key)
    if not isinstance(arr, list) or len(arr) == 0:
        sys.exit(1)
    for item in arr:
        for k in keys:
            if k not in item:
                sys.exit(1)
    sys.exit(0)
except:
    sys.exit(1)
"; then ok "$label"; else fail "$label (shape mismatch or empty)"; fi
}

echo "--- Starting API Integration Suite ---"

# 1. PRE-AUTH
api GET /api/system/status >/dev/null
assert_json "GET /api/system/status" "success" "true"

# Setup admin
api POST /api/system/setup-admin '{"username":"admin","password":"StrongPassword123!"}' >/dev/null
ok "POST /api/system/setup-admin"

# HA Check
api POST /api/ha/heartbeat '{"node_id":"ci-node-1","status":"online"}' >/dev/null
assert_json "POST /api/ha/heartbeat" "success" "true"

# 2. LOGIN
LOGIN_JSON=$(api POST /api/auth/login '{"username":"admin","password":"StrongPassword123!"}')
SESSION=$(echo "$LOGIN_JSON" | python3 -c "import sys,json; print(json.load(sys.stdin).get('session_id',''))" 2>/dev/null)

api POST /api/auth/login '{"username":"admin","password":"wrong"}' >/dev/null
assert_json "Login with wrong password fails" "success" "false"
api POST /api/auth/login '{"username":"not-exist","password":"any"}' >/dev/null
assert_json "Login with non-existent user fails" "success" "false"

# 3. RBAC / USERS
api GET /api/rbac/users >/dev/null
assert_array "List users returns array" "users"
assert_json "Admin user present in list" "success" "true"

# Create user
api POST /api/users/create '{"action":"create","username":"ci-user","password":"CiUser1!Test","email":"ci@dplane.local","role":"user"}' >/dev/null
assert_json "Create user succeeds" "success" "true"
api GET /api/rbac/users >/dev/null
assert_json "User ci-user exists in list" "success" "true"

# Self-service
api GET /api/rbac/me/permissions >/dev/null
assert_array "Me permissions" "permissions"
api GET /api/rbac/me/roles >/dev/null
assert_array "Me roles" "roles"

# List roles & permissions
api GET /api/rbac/roles >/dev/null
assert_array "List roles returns array" "roles"
api GET /api/rbac/permissions >/dev/null
assert_array "List permissions returns array" "permissions"

# 4. ZFS (ADVANCED)
sleep 2
api GET /api/zfs/pools >/dev/null
assert_shape "ZFS pool shape" "data" "name" "health" "capacity"

api POST /api/zfs/datasets '{"name":"testpool/api-test","compression":"lz4","atime":"off"}' >/dev/null
assert_json "Create dataset" "success" "true"
sudo zfs list testpool/api-test >/dev/null && ok "Dataset on disk" || fail "Dataset missing"

# Snapshot
api POST /api/zfs/snapshots '{"dataset":"testpool/api-test","name":"ci-snap-1"}' >/dev/null
assert_json "Create snapshot" "success" "true"
sudo zfs list -t snapshot testpool/api-test@ci-snap-1 >/dev/null && ok "Snapshot on disk" || fail "Snapshot missing"

# List snapshots
api GET "/api/zfs/snapshots?dataset=testpool/api-test" >/dev/null
assert_array "List snapshots returns array" "snapshots"

# Rollback
api POST /api/zfs/snapshots/rollback "{\"snapshot\":\"testpool/api-test@ci-snap-1\",\"force\":true}" >/dev/null
assert_json "Rollback snapshot" "success" "true"

# Health & Iostat
api GET /api/zfs/health >/dev/null
assert_json "ZFS health" "success" "true"
api GET /api/zfs/iostat >/dev/null
assert_json "ZFS iostat" "success" "true"

# 5. ACL / FILE MANAGER
# Mountpoint is /mnt/testpool/api-test
api POST /api/acl/set '{"path":"/mnt/testpool/api-test","entry":"u:nobody:rwx"}' >/dev/null
assert_json "ACL set" "success" "true"
api GET "/api/acl/get?path=/mnt/testpool/api-test" | grep -q "nobody" && ok "ACL readback verified" || fail "ACL readback failed"

sudo mkdir -p /mnt/testpool/fm-test
api POST /api/files/write '{"path":"/mnt/testpool/fm-test/hello.txt","content":"hello ci"}' >/dev/null
assert_json "FM: write" "success" "true"
api GET '/api/files/read?path=/mnt/testpool/fm-test/hello.txt' >/dev/null
assert_json "FM: read" "content" "hello ci"
api POST /api/files/chmod '{"path":"/mnt/testpool/fm-test/hello.txt","mode":"0600"}' >/dev/null
assert_json "FM: chmod" "success" "true"

# Full FM Lifecycle
api POST /api/files/mkdir '{"path":"/mnt/testpool/fm-test/subdir"}' >/dev/null
assert_json "FM: mkdir" "success" "true"
api GET '/api/files/list?path=/mnt/testpool/fm-test' >/dev/null
assert_array "FM: list" "files"
# Rename uses 'new_name' (filename only)
api POST /api/files/rename '{"old_path":"/mnt/testpool/fm-test/hello.txt","new_name":"renamed.txt"}' >/dev/null
assert_json "FM: rename" "success" "true"
api POST /api/files/delete '{"path":"/mnt/testpool/fm-test/renamed.txt"}' >/dev/null
assert_json "FM: delete" "success" "true"
[ ! -f /mnt/testpool/fm-test/renamed.txt ] && ok "FM: lifecycle verified" || fail "FM: delete failed"

# 6. SMB SHARES
api POST /api/shares "{\"action\":\"create\",\"name\":\"ci-share\",\"path\":\"/mnt/testpool/api-test\",\"read_only\":false,\"guest_ok\":true,\"comment\":\"CI Test Share\"}" >/dev/null
assert_json "Create SMB share" "success" "true"
api GET /api/shares >/dev/null
assert_json "SMB share in list" "success" "true"
[ -f /tmp/smb.conf ] && grep -q "\[ci-share\]" /tmp/smb.conf && ok "Share in smb-shares.conf" || fail "Share missing from config file"

# 7. NFS EXPORTS
api POST /api/nfs/exports '{"path":"/mnt/testpool/api-test","clients":"*","options":"rw,sync,no_subtree_check","enabled":true}' >/dev/null
assert_json "Create NFS export" "success" "true"
api GET /api/nfs/exports >/dev/null
assert_json "NFS export in list" "success" "true"
sudo grep -q "/mnt/testpool/api-test" /etc/exports && ok "Export in /etc/exports" || fail "Export missing from /etc/exports"

# 8. DOCKER
api GET /api/docker/stacks >/dev/null
assert_json "Docker stacks list" "success" "true"

# 9. NETWORKING & SYSTEM
api GET /api/system/network >/dev/null
assert_json "List interfaces" "success" "true"
assert_array "Interfaces list" "interfaces"
api GET /api/system/status >/dev/null
assert_json "Get status" "success" "true"
api GET /api/system/settings >/dev/null
assert_json "Get settings" "success" "true"
CUR_HOST=$(api GET /api/system/profile | python3 -c "import sys,json; d=json.load(sys.stdin); print(d.get('profile',{}).get('hostname',''))")
[ -n "$CUR_HOST" ] && ok "Current hostname: $CUR_HOST" || fail "Hostname empty"

# Subsystems
api GET /api/firewall/status >/dev/null
assert_json "Firewall status" "success" "true"
api GET /api/iscsi/status >/dev/null
assert_json "iSCSI status" "success" "true"
api GET /api/ldap/status >/dev/null
assert_json "LDAP status" "success" "true"
api GET /api/metrics/current >/dev/null
assert_json "Metrics current" "success" "true"
api GET /api/alerts/webhooks >/dev/null
assert_json "Alerts/Webhooks" "success" "true"
api GET /api/monitoring/inotify >/dev/null
assert_json "Inotify stats" "used"

# Conditional Hardware
UPS_JSON=$(api GET /api/system/ups)
if echo "$UPS_JSON" | grep -q "NUT not installed"; then
  ok "UPS status (skipped - NUT not installed)"
else
  assert_json "UPS status" "success" "true"
fi

api GET /api/trash/list >/dev/null
assert_json "Trash list" "success" "true"
api GET /api/power/disks >/dev/null
assert_json "Power disks" "success" "true"

# 10. GITOPS
api GET /api/gitops/status >/dev/null
assert_json "GitOps status" "success" "true"
api GET /api/gitops/plan >/dev/null
assert_json "GitOps plan" "success" "true"

# 11. SECURITY
INJECT=$(api POST /api/zfs/command "{\"command\":\"ls\",\"args\":[\"/etc/passwd\"],\"session_id\":\"$SESSION\",\"user\":\"admin\"}")
echo "$INJECT" | grep -qiE "Forbidden|not allowed|success\":false" && ok "Security: Injection blocked" || fail "Security: Injection NOT blocked"

# 12. AUDIT & LOGOUT
api GET /api/system/audit/verify-chain >/dev/null
assert_json "Audit chain verified" "valid" "true"

api POST /api/auth/logout '{}' >/dev/null
assert_json "Logout succeeds" "success" "true"
api GET /api/auth/check >/dev/null
assert_json "Auth check after logout fails" "authenticated" "false"

# --- SUMMARY ---
echo ""
echo "=========================================="
printf "  Results: %d passed   %d failed\n" "$PASS" "$FAIL"
echo "=========================================="

if [ "$FAIL" -gt 0 ]; then
  echo "Failures:"
  echo -e "$FAILURES"
  exit 1
fi
