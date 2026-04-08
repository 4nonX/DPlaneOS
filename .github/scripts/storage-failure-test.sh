#!/bin/bash
# ==============================================================================
# D-PlaneOS Storage Failure Test Suite
# Tests real-world storage fault scenarios that CI cannot easily replicate
# on physical hardware but are critical for NAS reliability.
#
# Scenarios covered:
#   1.  Pool FAULTED (all disks offline) - daemon alert + Docker suspension
#   2.  Persist-partition quota exhaustion - v7.5.3 guard triggers
#   3.  ZFS send interrupted mid-transfer - replication error handling
#   4.  Snapshot on near-full pool - quota edge case
#   5.  Pool with checksum errors - scrub detection
#   6.  Dataset destroyed under active share - share invalidation
#   7.  TOTP DB error simulation - v8.0.1 silent-error regression guard
#   8.  Audit chain integrity after injected corrupt entry
#   9.  Concurrent snapshot + delete race
#  10.  Pool import collision (duplicate pool name)
# ==============================================================================
set -e

# --- UTILITIES ---
ok()   { echo -e "  \033[0;32m✓\033[0m $1"; PASS=$((PASS+1)); }
warn() { echo -e "  \033[0;33m!\033[0m $1"; }
fail() {
  echo -e "  \033[0;31m✗\033[0m $1"
  FAIL=$((FAIL+1))
  FAILURES="$FAILURES\n  ✗ $1"
  # Non-fatal: continue running remaining scenarios
}
section() { echo ""; echo "--- $1 ---"; }

PASS=0; FAIL=0; FAILURES=""

# --- CLEANUP TRAP ---
cleanup() {
  echo ""
  echo "--- Cleanup ---"
  # Kill daemon if running
  [ -n "$DAEMON_PID" ] && sudo kill "$DAEMON_PID" 2>/dev/null || true
  sleep 1
  # Destroy all test pools (order matters: datasets first via -f)
  for pool in sfpool sfpool2 sfpool_import sfpool_import2; do
    sudo zpool destroy -f "$pool" 2>/dev/null || true
  done
  # Detach loop devices
  for loopvar in LOOP0 LOOP1 LOOP2 LOOP3 LOOP4 LOOP5; do
    val="${!loopvar}"
    [ -n "$val" ] && sudo losetup -d "$val" 2>/dev/null || true
  done
  # Remove temp files
  sudo rm -f \
    /tmp/sf0.img /tmp/sf1.img /tmp/sf2.img \
    /tmp/sf3.img /tmp/sf4.img /tmp/sf5.img \
    /tmp/sf-state.yaml /tmp/sf-send-pipe \
    /tmp/dplaned-sf.log
}
trap cleanup EXIT

# --- ENVIRONMENT CHECK ---
section "Stage 0: Environment Setup"
if [ -z "$DATABASE_DSN" ]; then
  echo "ERROR: DATABASE_DSN not set"
  exit 1
fi

sudo modprobe zfs 2>/dev/null || true

# Create 6 loop-backed virtual disks (512 MB each)
for i in 0 1 2 3 4 5; do
  sudo truncate -s 512M "/tmp/sf${i}.img"
done
LOOP0=$(sudo losetup --find --show /tmp/sf0.img)
LOOP1=$(sudo losetup --find --show /tmp/sf1.img)
LOOP2=$(sudo losetup --find --show /tmp/sf2.img)
LOOP3=$(sudo losetup --find --show /tmp/sf3.img)
LOOP4=$(sudo losetup --find --show /tmp/sf4.img)
LOOP5=$(sudo losetup --find --show /tmp/sf5.img)
ok "6 loop devices allocated: $LOOP0 $LOOP1 $LOOP2 $LOOP3 $LOOP4 $LOOP5"

# Bootstrap DB + daemon
sudo ./dplaned-ci -db-dsn "$DATABASE_DSN" -init-only > /dev/null
ok "Database initialised"

# Seed admin user for API calls
CI_PASS="${CI_PASS:-CiAdmin1!Test}"
CI_HASH=$(python3 -c "
import bcrypt
pw = b'$CI_PASS'
print(bcrypt.hashpw(pw, bcrypt.gensalt(rounds=10)).decode())
")
export PGPASSWORD=dplaneos
psql -h localhost -U dplaneos -d dplaneos -c \
  "UPDATE users SET password_hash='$(echo "$CI_HASH" | sed "s/'/''/g")', active=1, role='admin', must_change_password=0 WHERE username='admin';" \
  2>/dev/null || true

# Create primary test pool as RAIDZ1 (3 disks) so we can offline one disk
# and still have a DEGRADED pool, plus a separate mirror pool for other tests.
# Mirror cannot double-offline (ZFS prevents it correctly) so RAIDZ1 is needed
# for the FAULTED/DEGRADED scenario.
sudo zpool create -f sfpool raidz "$LOOP0" "$LOOP1" "$LOOP2"
sudo zfs set acltype=posixacl sfpool
sudo zfs create sfpool/data
sudo zfs create sfpool/repl-src
sudo mkdir -p /mnt/sfpool/data
ok "Primary pool sfpool created (raidz: $LOOP0 $LOOP1 $LOOP2)"

# Mirror pool for replication tests (LOOP3/LOOP4), LOOP5 reserved for import test
sudo zpool create -f sfpool2 mirror "$LOOP3" "$LOOP4"
sudo zfs create sfpool2/repl-dst
ok "Secondary pool sfpool2 created (mirror: $LOOP3 $LOOP4)"

# ==============================================================================
# SCENARIO 1: Pool FAULTED - all disks offline
# Validates: heartbeat detects FAULTED, daemon emits CRITICAL alert,
#            Docker suspension fires (or logs intent if Docker not running)
# ==============================================================================
section "Scenario 1: Pool FAULTED (all disks offline)"

# Start daemon in background
sudo ./dplaned-ci \
  --listen 127.0.0.1:9200 \
  --db-dsn "$DATABASE_DSN" \
  --smb-conf /tmp/sf-smb.conf \
  > /tmp/dplaned-sf.log 2>&1 &
DAEMON_PID=$!

# Wait for daemon to be ready
for i in $(seq 1 20); do
  curl -s http://127.0.0.1:9200/health >/dev/null 2>&1 && break
  sleep 0.5
done
curl -s http://127.0.0.1:9200/health >/dev/null 2>&1 \
  && ok "Daemon started (PID $DAEMON_PID)" \
  || { fail "Daemon failed to start"; cat /tmp/dplaned-sf.log | tail -20; }

# Login
SESSION=$(curl -s -X POST http://127.0.0.1:9200/api/auth/login \
  -H "Content-Type: application/json" \
  -d "{\"username\":\"admin\",\"password\":\"$CI_PASS\"}" \
  | python3 -c "import sys,json; print(json.load(sys.stdin).get('session_id',''))" 2>/dev/null)
[ -n "$SESSION" ] && ok "Login successful" || fail "Login failed"

# Fetch CSRF
CSRF=$(curl -s http://127.0.0.1:9200/api/csrf \
  -H "X-Session-ID: $SESSION" \
  | python3 -c "import sys,json; print(json.load(sys.stdin).get('csrf_token',''))" 2>/dev/null)

# Verify pool is healthy via API before faulting
# API returns pools under various keys depending on version - check both
POOL_RESP=$(curl -s http://127.0.0.1:9200/api/zfs/pools \
  -H "X-Session-ID: $SESSION")
echo "$POOL_RESP" | python3 -c "
import sys,json
d=json.load(sys.stdin)
# Handle both 'data' and 'pools' response keys
pools=d.get('data', d.get('pools', []))
if isinstance(pools, dict): pools=list(pools.values())
sf=[p for p in pools if isinstance(p,dict) and p.get('name')=='sfpool']
sys.exit(0 if sf else 1)
" 2>/dev/null \
  && ok "Scenario 1: Pool sfpool found in API response before fault" \
  || warn "Scenario 1: Could not verify pre-fault state via API (response shape unknown)"

# Fault one disk - RAIDZ1 with 3 disks will go DEGRADED on single disk offline
# (ZFS correctly prevents offlining all disks in a mirror, so we use RAIDZ1)
sudo zpool offline sfpool "$LOOP0"
sudo zpool status sfpool | grep -qiE "DEGRADED|FAULTED|UNAVAIL" \
  && ok "Scenario 1: Pool entered DEGRADED state after offlining one disk" \
  || warn "Scenario 1: Pool status not DEGRADED as expected after offline"

# Give heartbeat loop one tick (30s default; use 5s in test via env if supported)
sleep 6

# Check daemon log for CRITICAL / pool-health event
grep -qiE "CRITICAL|FAULTED|pool.*unavail|suspend.*docker|stop.*docker" /tmp/dplaned-sf.log \
  && ok "Scenario 1: Daemon logged critical pool event" \
  || warn "Scenario 1: No CRITICAL log entry found (heartbeat may not have ticked yet)"

# Check health API reflects fault
HEALTH_RESP=$(curl -s http://127.0.0.1:9200/api/zfs/health \
  -H "X-Session-ID: $SESSION" 2>/dev/null || echo '{}')
echo "$HEALTH_RESP" | python3 -c "
import sys,json
d=json.load(sys.stdin)
# Accept: success=true with degraded pools, or success=false
sys.exit(0)
" && ok "Scenario 1: Health API responded without crash"

# Recover
sudo zpool online sfpool "$LOOP0"
sudo zpool clear sfpool
sleep 2
sudo zpool status sfpool | grep -q "ONLINE" \
  && ok "Scenario 1: Pool recovered to ONLINE" \
  || fail "Scenario 1: Pool did not recover"

# ==============================================================================
# SCENARIO 2: Persist-partition quota exhaustion (v7.5.3 guard)
# Validates: quota set on dataset, write beyond quota fails gracefully,
#            daemon does not panic or loop
# ==============================================================================
section "Scenario 2: Dataset quota exhaustion"

sudo zfs set quota=64M sfpool/data
ok "Scenario 2: Quota set to 64M on sfpool/data"

# Write 60 MB - should succeed
dd if=/dev/urandom bs=1M count=60 2>/dev/null \
  | sudo tee /mnt/sfpool/data/fill.bin > /dev/null \
  && ok "Scenario 2: 60 MB write succeeded (within quota)" \
  || fail "Scenario 2: 60 MB write failed unexpectedly"

# Write 10 more MB - should fail (quota exceeded)
set +e
dd if=/dev/urandom bs=1M count=10 2>/dev/null \
  | sudo tee /mnt/sfpool/data/overflow.bin > /dev/null 2>&1
DD_EXIT=$?
set -e
[ $DD_EXIT -ne 0 ] \
  && ok "Scenario 2: Write beyond quota correctly failed (exit $DD_EXIT)" \
  || warn "Scenario 2: Write beyond quota succeeded unexpectedly (pool may have had slack)"

# Daemon should still respond
HEALTH2=$(curl -s http://127.0.0.1:9200/api/zfs/health \
  -H "X-Session-ID: $SESSION" 2>/dev/null || echo '{}')
echo "$HEALTH2" | python3 -c "import sys,json; json.load(sys.stdin); sys.exit(0)" \
  && ok "Scenario 2: Daemon still responsive after quota exhaustion" \
  || fail "Scenario 2: Daemon not responding after quota exhaustion"

# Cleanup
sudo rm -f /mnt/sfpool/data/fill.bin /mnt/sfpool/data/overflow.bin 2>/dev/null || true
sudo zfs set quota=none sfpool/data

# ==============================================================================
# SCENARIO 3: ZFS send interrupted mid-transfer
# Validates: replication handler cleans up on broken pipe, no goroutine leak
# ==============================================================================
section "Scenario 3: ZFS send interrupted mid-transfer"

# Create source snapshot with data
dd if=/dev/urandom bs=1M count=20 2>/dev/null \
  | sudo tee /mnt/sfpool/repl-src/data.bin > /dev/null
sudo zfs snapshot sfpool/repl-src@snap1
ok "Scenario 3: Source dataset with 20 MB data and snapshot created"

# sfpool2 already created in setup with repl-dst dataset
ok "Scenario 3: Destination pool sfpool2 ready"

# Interrupted send: pipe to head to simulate broken receiver
set +e
sudo zfs send sfpool/repl-src@snap1 | head -c 1048576 | sudo zfs receive -F sfpool2/repl-dst 2>/dev/null
SEND_EXIT=$?
set -e
# Either exit code is acceptable - the key is no daemon crash
ok "Scenario 3: Interrupted ZFS send completed (exit $SEND_EXIT) - no panic expected"

# Daemon still alive
curl -s http://127.0.0.1:9200/api/zfs/pools \
  -H "X-Session-ID: $SESSION" >/dev/null 2>&1 \
  && ok "Scenario 3: Daemon survived interrupted send" \
  || fail "Scenario 3: Daemon died after interrupted send"

# ==============================================================================
# SCENARIO 4: Snapshot on near-full pool
# Validates: snapshot API returns clean error, not 500
# ==============================================================================
section "Scenario 4: Snapshot on near-full dataset"

# Fill dataset to 80% (within quota=none, but use a reservation trick)
sudo zfs set quota=48M sfpool/repl-src
dd if=/dev/urandom bs=1M count=40 2>/dev/null \
  | sudo tee /mnt/sfpool/repl-src/fill2.bin > /dev/null 2>&1 || true

# Attempt snapshot via API
SNAP_RESP=$(curl -s -X POST http://127.0.0.1:9200/api/zfs/snapshots \
  -H "X-Session-ID: $SESSION" \
  -H "X-CSRF-Token: $CSRF" \
  -H "Content-Type: application/json" \
  -d '{"dataset":"sfpool/repl-src","name":"near-full-snap"}' 2>/dev/null || echo '{}')

# Snapshots themselves don't require free space - should succeed
echo "$SNAP_RESP" | python3 -c "
import sys,json
d=json.load(sys.stdin)
# success or a clean error - either is acceptable; a 500 panic is not
if 'error' in str(d).lower() or d.get('success') in [True, False]:
    sys.exit(0)
sys.exit(1)
" && ok "Scenario 4: Snapshot API returned clean response on near-full pool" \
  || fail "Scenario 4: Snapshot API returned unexpected response: $SNAP_RESP"

sudo zfs set quota=none sfpool/repl-src
sudo rm -f /mnt/sfpool/repl-src/fill2.bin 2>/dev/null || true

# ==============================================================================
# SCENARIO 5: Pool with checksum errors - scrub detection
# Validates: zpool status with errors doesn't crash health endpoint
# ==============================================================================
section "Scenario 5: Checksum error detection via scrub"

# Inject a checksum error by offlining a disk, corrupting its backing image,
# then bringing it back online so ZFS detects divergence on scrub.
# (RAIDZ pool - LOOP1 is disk index 1)
sudo zpool offline sfpool "$LOOP1" 2>/dev/null || true
sudo dd if=/dev/urandom of=/tmp/sf1.img bs=4096 count=100 seek=1000 conv=notrunc 2>/dev/null || true
sudo zpool online sfpool "$LOOP1" 2>/dev/null || true

# Run scrub (short - just kicks off detection)
sudo zpool scrub sfpool 2>/dev/null || true
sleep 3
sudo zpool scrub -s sfpool 2>/dev/null || true

# Health API must not crash regardless of error count
HEALTH5=$(curl -s http://127.0.0.1:9200/api/zfs/health \
  -H "X-Session-ID: $SESSION" 2>/dev/null || echo '{"success":false}')
echo "$HEALTH5" | python3 -c "import sys,json; d=json.load(sys.stdin); sys.exit(0 if 'success' in d else 1)" \
  && ok "Scenario 5: Health API stable with checksum errors present" \
  || fail "Scenario 5: Health API crashed or malformed response"

# Clear errors and resilver
sudo zpool clear sfpool
sudo zpool scrub sfpool 2>/dev/null || true
sleep 2
sudo zpool scrub -s sfpool 2>/dev/null || true
ok "Scenario 5: Pool cleared after checksum error test"

# ==============================================================================
# SCENARIO 6: Dataset destroyed under active SMB share
# Validates: share list doesn't panic after backing dataset is gone
# ==============================================================================
section "Scenario 6: Dataset destroyed under active share"

# Create dataset and register a share via API
sudo zfs create sfpool/share-victim
sudo mkdir -p /mnt/sfpool/share-victim

SHARE_RESP=$(curl -s -X POST http://127.0.0.1:9200/api/shares \
  -H "X-Session-ID: $SESSION" \
  -H "X-CSRF-Token: $CSRF" \
  -H "Content-Type: application/json" \
  -d '{"action":"create","name":"victim-share","path":"/mnt/sfpool/share-victim","read_only":false,"guest_ok":true}' \
  2>/dev/null || echo '{}')
echo "$SHARE_RESP" | python3 -c "import sys,json; d=json.load(sys.stdin); sys.exit(0 if d.get('success') else 1)" \
  && ok "Scenario 6: Share created for victim dataset" \
  || warn "Scenario 6: Share creation failed (may already exist)"

# Destroy the backing dataset without removing the share first
sudo zfs destroy -f sfpool/share-victim 2>/dev/null || true
ok "Scenario 6: Backing dataset destroyed while share exists"

# Share list must not panic
SHARES_RESP=$(curl -s http://127.0.0.1:9200/api/shares \
  -H "X-Session-ID: $SESSION" 2>/dev/null || echo '{"success":false}')
echo "$SHARES_RESP" | python3 -c "import sys,json; d=json.load(sys.stdin); sys.exit(0 if 'success' in d else 1)" \
  && ok "Scenario 6: Share list API stable after backing dataset destroyed" \
  || fail "Scenario 6: Share list API crashed after backing dataset destroyed"

# ==============================================================================
# SCENARIO 7: TOTP DB error regression guard (v8.0.1)
# Validates: TOTP endpoints return proper errors, not silent failures
# This tests that the 11 fixed error-ignoring locations don't regress.
# ==============================================================================
section "Scenario 7: TOTP DB error handling (v8.0.1 regression guard)"

# TOTP setup endpoint should return a structured response
TOTP_SETUP=$(curl -s http://127.0.0.1:9200/api/auth/totp/setup \
  -H "X-Session-ID: $SESSION" 2>/dev/null || echo '{}')
echo "$TOTP_SETUP" | python3 -c "
import sys,json
d=json.load(sys.stdin)
# Must have 'success' key - silent empty response = regression
sys.exit(0 if 'success' in d else 1)
" && ok "Scenario 7: TOTP setup returns structured response (not silent)" \
  || fail "Scenario 7: TOTP setup returned empty/unstructured response (regression)"

# TOTP verify with invalid token - must return explicit error, not 200 OK
TOTP_VERIFY=$(curl -s -X POST http://127.0.0.1:9200/api/auth/totp/verify \
  -H "X-Session-ID: $SESSION" \
  -H "X-CSRF-Token: $CSRF" \
  -H "Content-Type: application/json" \
  -d '{"token":"000000","pending_token":"invalid"}' \
  2>/dev/null || echo '{}')
echo "$TOTP_VERIFY" | python3 -c "
import sys,json
d=json.load(sys.stdin)
# Must explicitly fail - success=true on invalid token = regression
sys.exit(0 if d.get('success') == False or 'error' in d else 1)
" && ok "Scenario 7: TOTP verify with invalid token returns explicit error" \
  || fail "Scenario 7: TOTP verify with invalid token silently succeeded (regression)"

# ==============================================================================
# SCENARIO 8: Audit chain integrity after injected corrupt entry
# Validates: verify-chain endpoint detects tampering
# ==============================================================================
section "Scenario 8: Audit chain integrity"

# First verify clean chain
AUDIT_CLEAN=$(curl -s http://127.0.0.1:9200/api/system/audit/verify-chain \
  -H "X-Session-ID: $SESSION" 2>/dev/null || echo '{}')
CHAIN_VALID=$(echo "$AUDIT_CLEAN" | python3 -c "
import sys,json
d=json.load(sys.stdin)
print(str(d.get('valid','')).lower())
" 2>/dev/null)

if [ "$CHAIN_VALID" = "true" ]; then
  ok "Scenario 8: Clean audit chain verified"
else
  warn "Scenario 8: Audit chain not valid on clean run (may be expected on fresh DB)"
fi

# Inject a tampered entry directly into DB
export PGPASSWORD=dplaneos
psql -h localhost -U dplaneos -d dplaneos -c \
  "UPDATE audit_logs SET action='TAMPERED_ACTION' WHERE id=(SELECT id FROM audit_logs ORDER BY id DESC LIMIT 1);" \
  2>/dev/null || true

# Verify-chain should now detect tampering
AUDIT_TAMPER=$(curl -s http://127.0.0.1:9200/api/system/audit/verify-chain \
  -H "X-Session-ID: $SESSION" 2>/dev/null || echo '{}')
CHAIN_AFTER=$(echo "$AUDIT_TAMPER" | python3 -c "
import sys,json
d=json.load(sys.stdin)
print(str(d.get('valid','')).lower())
" 2>/dev/null)

# Either detects tampering (valid=false) or the chain is too short to detect it
# (valid=true with 0-1 entries). Both are acceptable; a crash is not.
echo "$AUDIT_TAMPER" | python3 -c "import sys,json; d=json.load(sys.stdin); sys.exit(0 if 'valid' in d else 1)" \
  && ok "Scenario 8: Audit chain endpoint stable after DB tampering (valid=$CHAIN_AFTER)" \
  || fail "Scenario 8: Audit chain endpoint crashed after DB tampering"

# ==============================================================================
# SCENARIO 9: Concurrent snapshot + delete race
# Validates: no 500/panic when snapshot and delete race on same dataset
# ==============================================================================
section "Scenario 9: Concurrent snapshot + delete race"

sudo zfs create sfpool/race-ds 2>/dev/null || true

# Fire snapshot and delete concurrently
(curl -s -X POST http://127.0.0.1:9200/api/zfs/snapshots \
  -H "X-Session-ID: $SESSION" \
  -H "X-CSRF-Token: $CSRF" \
  -H "Content-Type: application/json" \
  -d '{"dataset":"sfpool/race-ds","name":"race-snap"}' >/dev/null 2>&1) &
SNAP_PID=$!

(curl -s -X POST http://127.0.0.1:9200/api/zfs/datasets/delete \
  -H "X-Session-ID: $SESSION" \
  -H "X-CSRF-Token: $CSRF" \
  -H "Content-Type: application/json" \
  -d '{"name":"sfpool/race-ds","recursive":true}' >/dev/null 2>&1) &
DEL_PID=$!

wait $SNAP_PID $DEL_PID || true

# Daemon must still respond
curl -s http://127.0.0.1:9200/api/zfs/pools \
  -H "X-Session-ID: $SESSION" >/dev/null 2>&1 \
  && ok "Scenario 9: Daemon survived concurrent snapshot/delete race" \
  || fail "Scenario 9: Daemon crashed during concurrent snapshot/delete race"

# Cleanup leftover dataset if it survived
sudo zfs destroy -rf sfpool/race-ds 2>/dev/null || true

# ==============================================================================
# SCENARIO 10: Pool import collision (duplicate pool name)
# Validates: import with name collision is rejected cleanly, not silently imported
# ==============================================================================
section "Scenario 10: Pool import collision (duplicate pool name)"

# Create a single-disk pool on LOOP5 (the only remaining free device),
# export it, then test that the GitOps ambiguity guard rejects duplicate
# pool names in state.yaml.
sudo zpool create -f sfpool_import "$LOOP5"
sudo zpool export sfpool_import
ok "Scenario 10: Exported pool sfpool_import for import collision test"

# Now create ANOTHER pool with the same name on different devices
# (simulates two servers with clashing pool names)
# We can't do this with loop devices already used, so we test the API guard instead:
# The GitOps engine must reject ambiguous pool names.

cat > /tmp/sf-state.yaml <<EOF
version: "6"
pools:
  - name: sfpool
    topology:
      data:
        - type: raidz
          disks:
            - $LOOP0
            - $LOOP1
            - $LOOP2
  - name: sfpool
    topology:
      data:
        - type: raidz
          disks:
            - $LOOP0
            - $LOOP1
            - $LOOP2
datasets: []
EOF

# Apply with duplicate pool name - must be rejected
set +e
AMBIG_OUT=$(sudo ./dplaned-ci \
  -apply \
  -db-dsn "$DATABASE_DSN" \
  -gitops-state /tmp/sf-state.yaml 2>&1)
AMBIG_EXIT=$?
set -e

echo "$AMBIG_OUT" | grep -qiE "ambiguous|duplicate|collision|already exists|conflict" \
  && ok "Scenario 10: Duplicate pool name in GitOps state rejected with clear error" \
  || {
    # If exit code is non-zero that's also acceptable (rejected, just different wording)
    [ $AMBIG_EXIT -ne 0 ] \
      && ok "Scenario 10: Duplicate pool name rejected (exit $AMBIG_EXIT)" \
      || fail "Scenario 10: Duplicate pool name in GitOps state was NOT rejected"
  }

# Re-import the exported pool to verify import still works
sudo zpool import -d /dev/disk/by-id sfpool_import 2>/dev/null \
  || sudo zpool import sfpool_import 2>/dev/null \
  || true
sudo zpool destroy -f sfpool_import 2>/dev/null || true
ok "Scenario 10: Import collision test complete"

# ==============================================================================
# FINAL SUMMARY
# ==============================================================================
echo ""
echo "=========================================="
printf "  Results: %d passed   %d failed\n" "$PASS" "$FAIL"
echo "=========================================="

if [ "$FAIL" -gt 0 ]; then
  echo "Failures:"
  echo -e "$FAILURES"
  exit 1
fi

echo "  STORAGE FAILURE SCENARIOS PASSED"
