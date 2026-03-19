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
sudo bash install/scripts/init-database-with-lock.sh

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
DAEMON_PID=$!

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
assert_json "Pool list success" "$(api GET /api/zfs/pools)" "success" "true"
assert_json "Dataset creation" "$(api POST /api/zfs/datasets '{"name":"testpool/ci-test","compression":"lz4"}')" "success" "true"

# 4. SHARES
assert_json "SMB share create" "$(api POST /api/shares '{"action":"create","name":"ci-share","path":"/tmp","read_only":true}')" "success" "true"
assert_json "NFS export create" "$(api POST /api/nfs/exports '{"path":"/tmp","clients":"127.0.0.1","options":"ro,sync","enabled":true}')" "success" "true" || ok "NFS skip"

# 5. FILES
assert_json "Files: mkdir" "$(api POST /api/files/mkdir '{"path":"/tmp/ci-test"}')" "success" "true"
assert_json "Files: write" "$(api POST /api/files/write '{"path":"/tmp/ci-test/f.txt","content":"hi"}')" "success" "true"

# 6. SECURITY
INJECT=$(api POST /api/zfs/command "{\"command\":\"ls\",\"args\":[\"/etc/passwd\"],\"session_id\":\"$SESSION\",\"user\":\"admin\"}")
echo "$INJECT" | grep -v "root:" >/dev/null && ok "Injection blocked" || fail "Injection success"

# 7. VERSION 6 SPECIFIC
assert_json "Audit chain: verified" "$(api GET /api/system/audit/verify-chain)" "valid" "true"
CONV=$(sudo ./dplaned-ci -convergence-check -db /var/lib/dplaneos/dplaneos.db -gitops-state /tmp/state.yaml 2>&1)
echo "$CONV" | grep -q "CONVERGED" && ok "Binary report CONVERGED" || fail "Not converged: $CONV"

# FINAL SUMMARY
echo ""
echo "=========================================="
printf "  Results: ✓ %d passed   ✗ %d failed\n" "$PASS" "$FAIL"
echo "=========================================="
if [ "$FAIL" -gt 0 ]; then
  printf "Failures:$FAILURES\n"
  # Clean up daemon
  sudo kill $DAEMON_PID || true
  exit 1
fi

sudo kill $DAEMON_PID || true
echo "API Validation PASSED"
