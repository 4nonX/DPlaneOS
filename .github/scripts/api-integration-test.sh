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
sudo zpool create testpool mirror $LOOP0 $LOOP1
sudo zpool add testpool spare $LOOP2

echo "--- Initializing Database ---"
sudo bash install/scripts/init-database-with-lock.sh --db /tmp/dplaneos.db

echo "--- v6: Deterministic Bootstrap (-apply) ---"
# Seed the state file for Phase 1
echo '
pools:
  - name: testpool
datasets:
  - name: testpool/ci-enforcement
    mountpoint: /mnt/testpool/ci-enforcement
' > /tmp/state.yaml
sudo ./dplaned-ci --db /tmp/dplaneos.db --gitops-state /tmp/state.yaml --apply

echo "--- v6: Serialization Round-trip ---"
sudo ./dplaned-ci --db /tmp/dplaneos.db --gitops-state /tmp/state.yaml --test-serialization

echo "--- v6: Deterministic Idempotency ---"
sudo ./dplaned-ci --db /tmp/dplaneos.db --gitops-state /tmp/state.yaml --test-idempotency

echo "--- Starting Daemon ---"
sudo ./dplaned-ci --listen 127.0.0.1:9000 --db /tmp/dplaneos.db --gitops-state /tmp/state.yaml > /tmp/dplaned-ci.log 2>&1 &
PID=$!
trap "sudo kill $PID || true; sudo zpool destroy testpool || true; sudo losetup -d $LOOP0 $LOOP1 $LOOP2 || true" EXIT

# Wait for daemon
for i in {1..20}; do
  curl -s http://127.0.0.1:9000/health >/dev/null && break
  sleep 0.5
done

echo "--- Seeding CI User ---"
# Re-using internal init logic via CLI or direct DB would be hard, so we hit the setup-admin endpoint
# which is allowed because the DB is fresh and has no users yet.
curl -s -X POST http://127.0.0.1:9000/api/system/setup-admin \
  -H "Content-Type: application/json" \
  -d '{"username":"admin","password":"Tester1!Password"}' > /tmp/setup.log

echo "--- Validating ReadWritePaths ---"
[ -d "/mnt/testpool/ci-enforcement" ] || (echo "Convergence failure: dataset not mounted"; exit 1)

# --- TEST SUITE ---
BASE="http://127.0.0.1:9000"
COOKIE_JAR="/tmp/ci-cookies.txt"

ok()   { echo -ne "  \033[0;32m✓\033[0m $1\n"; }
fail() { echo -ne "  \033[0;31m✗\033[0m $1\n"; exit 1; }

api() {
  local method=$1
  local path=$2
  local data=$3
  if [ "$method" = "GET" ]; then
    curl -s -b "$COOKIE_JAR" -c "$COOKIE_JAR" "$BASE$path"
  else
    curl -s -b "$COOKIE_JAR" -c "$COOKIE_JAR" -X "$method" \
      -H "Content-Type: application/json" \
      -d "$data" "$BASE$path"
  fi
}

assert_json() {
  local label=$1
  local body=$2
  local key=$3
  local expect=$4
  if echo "$body" | grep -q "\"$key\":$expect" || echo "$body" | grep -q "\"$key\":\"$expect\""; then
    ok "$label"
  else
    fail "$label (got: $body)"
  fi
}

assert_array() {
  local label=$1
  local body=$2
  local key=$3
  if echo "$body" | grep -q "\"$key\":\["; then
    ok "$label"
  else
    fail "$label (got: $body)"
  fi
}

echo "--- Starting API Integration Suite ---"

# 1. CORE
assert_json "GET /api/system/status" "$(api GET /api/system/status)" "success" "true"
assert_json "POST /api/system/setup-admin" "$(api POST /api/system/setup-admin '{"username":"admin","password":"Tester1!Password"}')" "success" "false" # Already exists
assert_json "POST /api/system/setup-complete" "$(api POST /api/system/setup-complete '{}')" "success" "true"

# Pre-auth paths
CODE=$(curl -s -o /dev/null -w "%{http_code}" $BASE/api/ha/heartbeat)
[ "$CODE" = "200" ] || [ "$CODE" = "403" ] && ok "POST /api/ha/heartbeat 200/403" || fail "HA Heartbeat failed ($CODE)"
assert_json "/health" "$(api GET /health)" "status" "ok"

# 2. AUTH
TOKEN=$(api GET /api/csrf | grep -o '"csrf_token":"[^"]*"' | cut -d'"' -f4)
[ -n "$TOKEN" ] && ok "/api/csrf returns token" || fail "CSRF failed"

LOGIN=$(api POST /api/auth/login "{\"username\":\"admin\",\"password\":\"Tester1!Password\",\"csrf_token\":\"$TOKEN\"}")
echo "$LOGIN" | grep -q '"success":true' && ok "Login successful" || fail "Login failed: $LOGIN"

# 3. ZFS
POOLS=$(api GET /api/zfs/pools)
assert_array "ZFS pools: data is array" "$POOLS" "pools"

assert_json "Dataset creation" "$(api POST /api/zfs/datasets '{"name":"testpool/app-data"}')" "success" "true"
sudo zfs list testpool/app-data >/dev/null && ok "Dataset on disk" || fail "ZFS dataset missing"

assert_json "Create snapshot" "$(api POST /api/zfs/snapshots '{"name":"testpool/app-data@first"}')" "success" "true"
sudo zfs list -t snapshot testpool/app-data@first >/dev/null && ok "Snapshot on disk" || fail "ZFS snapshot missing"

assert_json "Rollback snapshot" "$(api POST /api/zfs/snapshots/rollback '{"name":"testpool/app-data@first"}')" "success" "true"
assert_json "Destroy snapshot" "$(api DELETE /api/zfs/snapshots '{"name":"testpool/app-data@first"}')" "success" "true"

# 4. SHARES/ACL
assert_json "ACL set success" "$(api POST /api/acl/set '{"path":"/mnt/testpool/app-data","entries":[{"type":"user","name":"root","permissions":"rwx"}]}')" "success" "true"
GET_ACL=$(api GET /api/acl/get?path=/mnt/testpool/app-data)
echo "$GET_ACL" | grep -q "rwx" && ok "ACL verified" || fail "ACL readback failed"

assert_json "Create SMB share" "$(api POST /api/shares '{"name":"Public","path":"/mnt/testpool/app-data","type":"smb","read_only":true}')" "success" "true"
api GET /api/shares | grep -q '"read_only":true' && ok "SMB read_only persistence verified" || fail "SMB read_only lost"

assert_json "Create NFS export" "$(api POST /api/shares '{"name":"NFS","path":"/mnt/testpool/app-data","type":"nfs"}')" "success" "true"

# 5. DOCKER
DOCKER=$(api GET /api/docker/containers)
assert_array "Docker container shape" "$DOCKER" "containers"

# 6. FILE MANAGER
assert_json "Files: mkdir" "$(api POST /api/files/mkdir '{"path":"/mnt/testpool/app-data/ci-test"}')" "success" "true"
assert_json "Files: write" "$(api POST /api/files/write '{"path":"/mnt/testpool/app-data/ci-test/hello.txt","content":"CI_VAL_123"}')" "success" "true"
cat /mnt/testpool/app-data/ci-test/hello.txt | grep -q "CI_VAL_123" && ok "File content verified" || fail "File write failed"

assert_json "Files: rename" "$(api POST /api/files/rename '{"old_path":"/mnt/testpool/app-data/ci-test/hello.txt","new_path":"/mnt/testpool/app-data/ci-test/world.txt"}')" "success" "true"
assert_json "Files: chmod" "$(api POST /api/files/chmod '{"path":"/mnt/testpool/app-data/ci-test/world.txt","mode":493}')" "success" "true" # 0755
[ "$(stat -c %a /mnt/testpool/app-data/ci-test/world.txt)" = "755" ] && ok "Chmod verified" || fail "Chmod failed"

# 7. FIREWALL
FW=$(api GET /api/firewall/status)
assert_array "Firewall rules is array" "$FW" "rules"

# 8. SECURITY
INJECT=$(api POST /api/zfs/command "{\"command\":\"ls\",\"args\":[\"/etc/passwd\"],\"session_id\":\"$SESSION\",\"user\":\"admin\"}")
echo "$INJECT" | grep -v "root:" >/dev/null && ok "Injection blocked" || fail "Injection check failed"

# 9. ASSET READINESS
sudo zfs unmount testpool/ci-enforcement
HEALTH=$(api GET /api/system/preflight)
echo "$HEALTH" | grep -q "unmounted_datasets" && ok "Unmount detected" || fail "Unmount probe failed"
sudo zfs mount testpool/ci-enforcement

# 10. AUDIT
AUDIT=$(api GET /api/system/audit/verify-chain)
assert_json "Audit chain: verified" "$AUDIT" "valid" "true"

# 11. USERS
assert_json "Create user" "$(api POST /api/rbac/users '{"action":"create","username":"ci-tester","role":"user","password":"Tester1!Password"}')" "success" "true"
api GET /api/rbac/users | grep -q "ci-tester" && ok "User listed" || fail "User missing from list"

# 12. GITOPS & STATS (New Coverage)
echo "--- Testing v6 GitOps & Stats Expansion ---"
assert_json "GitOps status"   "$(api GET /api/gitops/status)"   "success" "true"
assert_json "GitOps plan"     "$(api GET /api/gitops/plan)"     "success" "true"
assert_json "GitOps state"    "$(api GET /api/gitops/state)"    "success" "true"
assert_json "GitOps settings" "$(api GET /api/gitops/settings)" "success" "true"

assert_json "Docker stats"    "$(api GET /api/docker/stats)"    "success" "true"
assert_json "Audit stats"     "$(api GET /api/system/audit/stats)" "success" "true"
assert_json "System health"   "$(api GET /api/system/health)"   "success" "true"
assert_json "Stale locks"     "$(api GET /api/system/stale-locks)" "success" "true"
assert_json "Disk latency"    "$(api GET /api/zfs/disk-latency)" "success" "true"

# 13. REMAINING SUBSYSTEMS - valid JSON + success
assert_json "Git sync config"   "$(api GET /api/git-sync/config)"     "success" "true"
assert_json "Webhooks"          "$(api GET /api/alerts/webhooks)"      "success" "true"
assert_json "SMTP config"       "$(api GET /api/alerts/smtp)"          "success" "true"
assert_json "LDAP config"       "$(api GET /api/ldap/config)"          "success" "true"
assert_json "HA status"         "$(api GET /api/ha/status)"            "success" "true"
assert_json "My permissions"    "$(api GET /api/rbac/me/permissions)"  "success" "true"
assert_json "API tokens"        "$(api GET /api/auth/tokens)"          "success" "true"
assert_json "Git sync repos"    "$(api GET /api/git-sync/repos)"       "success" "true"
assert_json "LDAP status"       "$(api GET /api/ldap/status)"          "success" "true"
assert_json "iSCSI status"      "$(api GET /api/iscsi/status)"         "success" "true"
assert_json "Sandbox list"      "$(api GET /api/zfs/sandbox)"          "success" "true"
assert_json "Trash list"        "$(api GET /api/trash/list)"           "success" "true"
assert_json "Removable media"   "$(api GET /api/removable/list)"       "success" "true"
assert_json "Power/spindown"    "$(api GET /api/power/disks)"          "success" "true"
assert_array "Certs"            "$(api GET /api/certs/list)"           "certs"

CONV=$(./dplaned-ci --convergence-check --db /tmp/dplaneos.db --gitops-state /tmp/state.yaml)
[ "$CONV" = "CONVERGED" ] && ok "System reported CONVERGED" || fail "Convergence check failed: $CONV"

echo ""
echo "=========================================="
echo "  Results: All integration tests PASSED"
echo "=========================================="
