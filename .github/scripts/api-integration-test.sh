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
sudo mkdir -p /mnt/testpool
sudo zpool create -f -m /mnt/testpool testpool raidz1 "$LOOP0" "$LOOP1" "$LOOP2"
sudo zfs set acltype=posixacl testpool

echo "--- Initializing Database ---"
sudo mkdir -p /var/lib/dplaneos
# Using the fixed script
sudo bash install/scripts/init-database-with-lock.sh --db /var/lib/dplaneos/dplaneos.db

echo "--- v6: Deterministic Bootstrap (-apply) ---"
cat > /tmp/state.yaml <<EOF
version: "6"
pools:
  - name: testpool
    mountpoint: /mnt/testpool
    disks:
      - $LOOP0
      - $LOOP1
      - $LOOP2
datasets:
  - name: testpool
    mountpoint: /mnt/testpool
  - name: testpool/ci-enforcement
    mountpoint: /mnt/testpool/ci-enforcement
    compression: lz4
EOF

# Using ./dplaned-ci as mapped in the yaml
sudo ./dplaned-ci -apply \
  -db /var/lib/dplaneos/dplaneos.db \
  -gitops-state /tmp/state.yaml \
  -smb-conf /tmp/smb-ci.conf

# Verify the dataset was actually created by the bootstrap
sudo zfs list testpool/ci-enforcement > /dev/null

echo "--- v6: Serialization Round-trip ---"
sudo ./dplaned-ci -test-serialization -gitops-state /tmp/state.yaml

echo "--- v6: Deterministic Idempotency ---"
sudo ./dplaned-ci -test-idempotency \
  -db /var/lib/dplaneos/dplaneos.db \
  -gitops-state /tmp/state.yaml \
  -smb-conf /tmp/smb-ci.conf

echo "--- Starting Daemon ---"
sudo mkdir -p /opt/dplaneos /var/log/dplaneos /etc/dplaneos
sudo cp -r app /opt/dplaneos/app
sudo ./dplaned-ci \
  -db /var/lib/dplaneos/dplaneos.db \
  -listen 127.0.0.1:9000 \
  -smb-conf /var/lib/dplaneos/smb-shares.conf \
  &>/tmp/dplaned.log &
echo $! > /tmp/dplaned.pid

for i in $(seq 1 20); do
  curl -sf http://127.0.0.1:9000/health | grep -q '"ok"' && break
  sleep 1
done
curl -sf http://127.0.0.1:9000/health | grep -q '"ok"'

echo "--- Seeding CI User ---"
CI_PASS="CiAdmin1!Test"
CI_HASH=$(python3 -c "
import bcrypt, os
pw = b'CiAdmin1!Test'
print(bcrypt.hashpw(pw, bcrypt.gensalt(rounds=10)).decode())
")
sudo sqlite3 /var/lib/dplaneos/dplaneos.db \
  "UPDATE users SET password_hash='$(echo "$CI_HASH" | sed "s/'/''/g")', active=1, role='admin', must_change_password=0 WHERE username='admin';"

echo "--- Validating ReadWritePaths ---"
REQUIRED_PATHS=(
  "/mnt" "/tank" "/data" "/media"
  "/etc/samba" "/etc/exports" "/etc/iscsi" "/etc/ssh"
  "/tmp" "/home"
  "/var/lib/dplaneos" "/var/log/dplaneos"
)
SERVICE="install/systemd/dplaned.service"
for p in "${REQUIRED_PATHS[@]}"; do
  grep -q "$p" "$SERVICE" || (echo "ERROR: $p missing from ReadWritePaths"; exit 1)
done

# --- API INTEGRATION TESTS ---
echo "--- Starting API Integration Suite ---"
BASE="http://127.0.0.1:9000"
PASS=0; FAIL=0; FAILURES=""

# Helper functions
ok()   { echo "  ✓ $1"; PASS=$((PASS+1)); }
fail() { echo "  ✗ $1"; FAIL=$((FAIL+1)); FAILURES="${FAILURES}\n  ✗ $1"; }

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
  print('$arr_key is not a list, got: ' + type(arr).__name__); sys.exit(1)
if len(arr) == 0:
  sys.exit(0)
first = arr[0]
target_keys = [$(printf "'%s'," "${required_keys[@]}" | sed 's/,$//')]
missing = [k for k in target_keys if k not in first]
if missing:
  print('first element missing keys: ' + str(missing)); sys.exit(1)
" 2>/dev/null \
  && ok "$label" \
  || fail "$label  (got: $(echo "$resp" | head -c 200))"
}

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

# 1. PRE-AUTH
CODE=$(curl -s -o /dev/null -w "%{http_code}" "$BASE/api/system/status")
[ "$CODE" = "200" ] && ok "GET /api/system/status 200" || fail "/api/system/status $CODE"

CODE=$(curl -s -o /dev/null -w "%{http_code}" -X POST "$BASE/api/system/setup-admin" -H "Content-Type: application/json" -d "{\"username\":\"admin\",\"password\":\"$CI_PASS\"}")
[ "$CODE" = "200" ] || [ "$CODE" = "403" ] && ok "POST /api/system/setup-admin $CODE" || fail "/api/system/setup-admin $CODE"

CODE=$(curl -s -o /dev/null -w "%{http_code}" -X POST "$BASE/api/system/setup-complete" -H "Content-Type: application/json" -d '{}')
[ "$CODE" = "200" ] || [ "$CODE" = "403" ] && ok "POST /api/system/setup-complete $CODE" || fail "/api/system/setup-complete $CODE"

CODE=$(curl -s -o /dev/null -w "%{http_code}" -X POST "$BASE/api/ha/heartbeat" -H "Content-Type: application/json" -d '{"node_id":"ci-peer","status":"healthy"}')
[ "$CODE" != "401" ] && ok "POST /api/ha/heartbeat 200/403" || fail "/api/ha/heartbeat 401"

assert_json "/health ok" "$(curl -sf $BASE/health)" "status" "ok"
assert_json "/api/csrf returns token" "$(curl -sf $BASE/api/csrf)" "csrf_token"

# 2. AUTH
LOGIN_HTTP=$(curl -s -w "\n%{http_code}" -X POST $BASE/api/auth/login -H "Content-Type: application/json" -d "{\"username\":\"admin\",\"password\":\"$CI_PASS\"}")
LOGIN=$(echo "$LOGIN_HTTP" | sed '$d')
SESSION=$(echo "$LOGIN" | python3 -c "import sys,json; print(json.load(sys.stdin).get('session_id',''))" 2>/dev/null)
[ -n "$SESSION" ] && ok "Login successful" || fail "Login failed"

api() {
  local method="$1" path="$2" body="${3:-}"
  local args=(-sf --max-time 15 -X "$method" "$BASE$path" -H "X-Session-ID: $SESSION" -H "X-User: admin" -H "Content-Type: application/json")
  [ -n "$body" ] && args+=(-d "$body")
  curl "${args[@]}" 2>/dev/null || echo '{"_err":true}'
}

# 3. ZFS
POOLS=$(api GET /api/zfs/pools)
assert_array "ZFS pools: data is array" "$POOLS" "data"
assert_json "Dataset creation" "$(api POST /api/zfs/datasets '{"name":"testpool/ci-test","compression":"lz4"}')" "success" "true"
sudo zfs list testpool/ci-test > /dev/null && ok "Dataset on disk" || fail "Dataset missing"

assert_json "Create snapshot" "$(api POST /api/zfs/snapshots '{"dataset":"testpool/ci-test","name":"ci-snap-1"}')" "success" "true"
sudo zfs list -t snapshot testpool/ci-test@ci-snap-1 > /dev/null && ok "Snapshot on disk" || fail "Snapshot missing"

assert_json "Rollback snapshot" "$(api POST /api/zfs/snapshots/rollback '{"snapshot":"testpool/ci-test@ci-snap-1"}')" "success" "true"
assert_json "Destroy snapshot" "$(api DELETE /api/zfs/snapshots '{"snapshot":"testpool/ci-test@ci-snap-1"}')" "success" "true"

# ACL
assert_json "ACL set success" "$(api POST /api/acl/set '{"path":"/mnt/testpool","entry":"u:nobody:rwx"}')" "success" "true"
api GET "/api/acl/get?path=/mnt/testpool" | grep -q "nobody" && ok "ACL verified" || fail "ACL missing nobody"

# 4. SHARES
assert_json "Create SMB share" "$(api POST /api/shares '{"action":"create","name":"ci-share","path":"/tmp","read_only":true}')" "success" "true"
SHARES=$(api GET /api/shares)
echo "$SHARES" | python3 -c "import sys,json; d=json.load(sys.stdin); s=next(x for x in d['shares'] if x['name']=='ci-share'); assert s['read_only'] is True" 2>/dev/null && ok "SMB read_only persistence verified" || fail "SMB read_only mismatch"

# 5. NFS
CREATE_NFS=$(api POST /api/nfs/exports '{"path":"/tmp","clients":"127.0.0.1","options":"ro,sync","enabled":true}')
assert_json "Create NFS export" "$CREATE_NFS" "success" "true"

# 6. DOCKER
CTRS=$(api GET /api/docker/containers)
assert_shape "Docker container shape" "$CTRS" "containers" "id" "name" "image" "state"

# 7. FILE MANAGER
sudo mkdir -p /tmp/ci-files-test
assert_json "Files: mkdir" "$(api POST /api/files/mkdir '{"path":"/tmp/ci-files-test/subdir"}')" "success" "true"
assert_json "Files: write" "$(api POST /api/files/write '{"path":"/tmp/ci-files-test/h.txt","content":"hi"}')" "success" "true"
READ=$(api GET '/api/files/read?path=/tmp/ci-files-test/h.txt')
echo "$READ" | grep -q "hi" && ok "File content verified" || fail "File content mismatch"
assert_json "Files: rename" "$(api POST /api/files/rename '{"old_path":"/tmp/ci-files-test/h.txt","new_name":"r.txt"}')" "success" "true"
assert_json "Files: chmod" "$(api POST /api/files/chmod '{"path":"/tmp/ci-files-test/r.txt","mode":"0600"}')" "success" "true"
[ "$(sudo stat -c %a /tmp/ci-files-test/r.txt)" = "600" ] && ok "Chmod verified" || fail "Chmod failed"

# 8. FIREWALL
FW=$(api GET /api/firewall/status)
assert_array "Firewall rules is array" "$FW" "rules"

# 9. USERS
assert_json "Create user" "$(api POST /api/rbac/users '{"username":"ci-tester","role":"user","password":"Tester1!Password"}')" "success" "true"
api GET /api/rbac/users | grep -q "ci-tester" && ok "User listed" || fail "User missing from list"

# 10. REMAINING SUBSYSTEMS
assert_json "Git sync config"   "$(api GET /api/git-sync/config)"     "success" "true"
assert_json "Webhooks"          "$(api GET /api/alerts/webhooks)"      "success" "true"
assert_json "SMTP config"       "$(api GET /api/alerts/smtp)"          "success" "true"
assert_json "LDAP config"       "$(api GET /api/ldap/config)"          "success" "true"
assert_json "HA status"         "$(api GET /api/ha/status)"            "success" "true"
assert_json "My permissions"    "$(api GET /api/rbac/me/permissions)"  "success" "true"
assert_json "API tokens"        "$(api GET /api/auth/tokens)"          "success" "true"

valid_json "$(api GET /api/git-sync/repos)"  && ok "Git sync repos"  || fail "Git sync repos invalid JSON"
valid_json "$(api GET /api/ldap/status)"     && ok "LDAP status"     || fail "LDAP status invalid JSON"
valid_json "$(api GET /api/iscsi/status)"    && ok "iSCSI status"    || fail "iSCSI status invalid JSON"
valid_json "$(api GET /api/sandbox/list)"    && ok "Sandbox list"    || fail "Sandbox list invalid JSON"
valid_json "$(api GET /api/trash/list)"      && ok "Trash list"      || fail "Trash list invalid JSON"
valid_json "$(api GET /api/removable/list)"  && ok "Removable media" || fail "Removable media invalid JSON"
valid_json "$(api GET /api/power/disks)"     && ok "Power/spindown"  || fail "Power/spindown invalid JSON"
valid_json "$(api GET /api/certs/list)"      && ok "Certs"           || fail "Certs invalid JSON"

# 11. SECURITY
INJECT=$(api POST /api/zfs/command "{\"command\":\"ls\",\"args\":[\"/etc/passwd\"],\"session_id\":\"$SESSION\",\"user\":\"admin\"}")
echo "$INJECT" | grep -v "root:" >/dev/null && ok "Injection blocked" || fail "Injection success"

# 12. DATA READINESS (v6)
sudo zfs unmount testpool/ci-enforcement
api GET /api/system/preflight | grep -q "unmounted\|not mounted" && ok "Unmount detected" || fail "Unmount missed"
sudo zfs mount testpool/ci-enforcement

# 13. AUDIT INTEGRITY (v6)
assert_json "Audit chain: verified" "$(api GET /api/system/audit/verify-chain)" "valid" "true"

# 14. SYSTEM CONVERGENCE
CONV=$(sudo ./dplaned-ci -convergence-check -db /var/lib/dplaneos/dplaneos.db -gitops-state /tmp/state.yaml 2>&1)
echo "$CONV" | grep -q "CONVERGED" && ok "System reported CONVERGED" || fail "Not converged: $CONV"

# FINAL SUMMARY
echo ""
echo "=========================================="
printf "  Results: ✓ %d passed   ✗ %d failed\n" "$PASS" "$FAIL"
echo "=========================================="
if [ "$FAIL" -gt 0 ]; then
  printf "Failures:$FAILURES\n"
  [ -f /tmp/dplaned.pid ] && sudo kill $(cat /tmp/dplaned.pid) || true
  exit 1
fi

[ -f /tmp/dplaned.pid ] && sudo kill $(cat /tmp/dplaned.pid) || true
echo "API Validation PASSED"
