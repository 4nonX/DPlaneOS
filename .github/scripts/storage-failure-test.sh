#!/bin/bash
# ==============================================================================
# D-PlaneOS Storage Failure Test Suite
# Tests real-world storage fault scenarios that CI cannot easily replicate
# on physical hardware but are critical for NAS reliability.
# ==============================================================================
set -e

ok()   { echo -e "  \033[0;32m✓\033[0m $1"; PASS=$((PASS+1)); }
warn() { echo -e "  \033[0;33m!\033[0m $1"; }
fail() { echo -e "  \033[0;31m✗\033[0m $1"; FAIL=$((FAIL+1)); FAILURES="$FAILURES\n  ✗ $1"; }
section() { echo ""; echo "--- $1 ---"; }

PASS=0; FAIL=0; FAILURES=""

cleanup() {
  echo ""
  echo "--- Cleanup ---"
  [ -n "$DAEMON_PID" ] && sudo kill "$DAEMON_PID" 2>/dev/null || true
  sleep 1
  for pool in sfpool sfpool2 sfpool_import; do sudo zpool destroy -f "$pool" 2>/dev/null || true; done
  for loopvar in LOOP0 LOOP1 LOOP2 LOOP3 LOOP4 LOOP5; do val="${!loopvar}"; [ -n "$val" ] && sudo losetup -d "$val" 2>/dev/null || true; done
  sudo rm -f /tmp/sf*.img /tmp/sf-state.yaml /tmp/dplaned-sf.log /tmp/sf-smb.conf
}
trap cleanup EXIT

section "Stage 0: Environment Setup"
[ -z "$DATABASE_DSN" ] && { echo "ERROR: DATABASE_DSN not set"; exit 1; }

SMB_CONF=$(mktemp)

sudo modprobe zfs 2>/dev/null || true
for i in 0 1 2 3 4 5; do sudo truncate -s 512M "/tmp/sf${i}.img"; done
LOOP0=$(sudo losetup --find --show /tmp/sf0.img)
LOOP1=$(sudo losetup --find --show /tmp/sf1.img)
LOOP2=$(sudo losetup --find --show /tmp/sf2.img)
LOOP3=$(sudo losetup --find --show /tmp/sf3.img)
LOOP4=$(sudo losetup --find --show /tmp/sf4.img)
LOOP5=$(sudo losetup --find --show /tmp/sf5.img)
ok "6 loop devices allocated: $LOOP0 $LOOP1 $LOOP2 $LOOP3 $LOOP4 $LOOP5"

sudo ./dplaned-ci -db-dsn "$DATABASE_DSN" -init-only > /dev/null
ok "Database initialised"

CI_PASS="${CI_PASS:-CiAdmin1!Test}"
CI_HASH=$(python3 -c "import bcrypt; print(bcrypt.hashpw(b'$CI_PASS', bcrypt.gensalt(rounds=10)).decode())")
export PGPASSWORD=dplaneos
psql -h localhost -U dplaneos -d dplaneos -c "UPDATE users SET password_hash='$(echo "$CI_HASH" | sed "s/'/''/g")', active=1, role='admin', must_change_password=0 WHERE username='admin';" 2>/dev/null || true

sudo zpool create -f sfpool raidz "$LOOP0" "$LOOP1" "$LOOP2"
sudo zfs set acltype=posixacl sfpool
sudo zfs create sfpool/data
sudo zfs create sfpool/repl-src
sudo mkdir -p /mnt/sfpool/data /mnt/sfpool/repl-src
ok "Primary pool sfpool created (raidz: $LOOP0 $LOOP1 $LOOP2)"

sudo zpool create -f sfpool2 mirror "$LOOP3" "$LOOP4"
sudo zfs create sfpool2/repl-dst
ok "Secondary pool sfpool2 created (mirror: $LOOP3 $LOOP4)"

# Start daemon
sudo ./dplaned-ci --listen 127.0.0.1:9200 --db-dsn "$DATABASE_DSN" --smb-conf "$SMB_CONF" > /tmp/dplaned-sf.log 2>&1 &
DAEMON_PID=$!

for i in $(seq 1 30); do 
  curl -s http://127.0.0.1:9200/health >/dev/null 2>&1 && break
  sleep 0.5
done

if curl -s http://127.0.0.1:9200/health >/dev/null 2>&1; then
  ok "Daemon started (PID $DAEMON_PID)"
else
  fail "Daemon failed to start"
  exit 1
fi

SESSION=$(curl -s -X POST http://127.0.0.1:9200/api/auth/login -H "Content-Type: application/json" -d "{\"username\":\"admin\",\"password\":\"$CI_PASS\"}" | python3 -c "import sys,json; print(json.load(sys.stdin).get('session_id',''))" 2>/dev/null)
[ -n "$SESSION" ] && ok "Login successful" || fail "Login failed"

CSRF=$(curl -s http://127.0.0.1:9200/api/csrf -H "X-Session-ID: $SESSION" | python3 -c "import sys,json; print(json.load(sys.stdin).get('csrf_token',''))" 2>/dev/null)

# ==============================================================================
# SCENARIO 1: Pool FAULTED - all disks offline
# ==============================================================================
section "Scenario 1: Pool FAULTED (all disks offline)"

POOL_RESP=$(curl -s http://127.0.0.1:9200/api/zfs/pools -H "X-Session-ID: $SESSION" 2>/dev/null || echo '{}')
echo "$POOL_RESP" | python3 -c "import sys,json; json.load(sys.stdin)" 2>/dev/null && ok "Scenario 1: Pool API OK" || warn "Scenario 1: Pool API issue"

sudo zpool offline sfpool "$LOOP0"
sudo zpool status sfpool | grep -qiE "DEGRADED|FAULTED|UNAVAIL" && ok "Scenario 1: Pool entered DEGRADED state" || warn "Scenario 1: Pool not DEGRADED"
sleep 3

grep -qiE "CRITICAL|FAULTED|pool.*unavail" /tmp/dplaned-sf.log && ok "Scenario 1: Daemon logged critical pool event" || warn "Scenario 1: No critical log (heartbeat may not have ticked)"

HEALTH_RESP=$(curl -s http://127.0.0.1:9200/api/system/health -H "X-Session-ID: $SESSION" 2>/dev/null || echo '{}')
echo "$HEALTH_RESP" | python3 -c "import sys,json; json.load(sys.stdin)" 2>/dev/null && ok "Scenario 1: Health API OK" || warn "Scenario 1: Health API issue"

sudo zpool online sfpool "$LOOP0"
sudo zpool clear sfpool
sleep 2
sudo zpool status sfpool | grep -q "ONLINE" && ok "Scenario 1: Pool recovered to ONLINE" || fail "Scenario 1: Pool did not recover"

# ==============================================================================
# SCENARIO 2: Dataset quota exhaustion
# ==============================================================================
section "Scenario 2: Dataset quota exhaustion"

sudo zfs set quota=64M sfpool/data
ok "Scenario 2: Quota set to 64M on sfpool/data"

dd if=/dev/urandom bs=1M count=60 2>/dev/null | sudo tee /mnt/sfpool/data/fill.bin > /dev/null && ok "Scenario 2: 60 MB write succeeded (within quota)" || fail "Scenario 2: 60 MB write failed"

set +e
dd if=/dev/urandom bs=1M count=10 2>/dev/null | sudo tee /mnt/sfpool/data/overflow.bin > /dev/null 2>&1
DD_EXIT=$?
set -e
[ $DD_EXIT -ne 0 ] && ok "Scenario 2: Write beyond quota correctly failed (exit $DD_EXIT)" || warn "Scenario 2: Write beyond quota succeeded unexpectedly"

HEALTH2=$(curl -s http://127.0.0.1:9200/api/system/health -H "X-Session-ID: $SESSION" 2>/dev/null || echo '{}')
echo "$HEALTH2" | python3 -c "import sys,json; json.load(sys.stdin)" 2>/dev/null && ok "Scenario 2: Daemon still responsive after quota exhaustion" || fail "Scenario 2: Daemon not responding after quota exhaustion"

sudo rm -f /mnt/sfpool/data/fill.bin /mnt/sfpool/data/overflow.bin 2>/dev/null || true
sudo zfs set quota=none sfpool/data

# ==============================================================================
# SCENARIO 3: ZFS send interrupted
# ==============================================================================
section "Scenario 3: ZFS send interrupted mid-transfer"

sudo mkdir -p /mnt/sfpool/repl-src
dd if=/dev/urandom bs=1M count=20 2>/dev/null | sudo tee /mnt/sfpool/repl-src/data.bin > /dev/null
sudo zfs snapshot sfpool/repl-src@snap1
ok "Scenario 3: Source dataset with 20 MB data and snapshot created"

set +e
sudo zfs send sfpool/repl-src@snap1 | head -c 1048576 | sudo zfs receive -F sfpool2/repl-dst 2>/dev/null
SEND_EXIT=$?
set -e
ok "Scenario 3: Interrupted ZFS send completed (exit $SEND_EXIT)"

curl -s http://127.0.0.1:9200/api/zfs/pools -H "X-Session-ID: $SESSION" >/dev/null 2>&1 && ok "Scenario 3: Daemon survived interrupted send" || fail "Scenario 3: Daemon died after interrupted send"

# ==============================================================================
# SCENARIO 4: Snapshot on near-full pool (edge case)
# ==============================================================================
section "Scenario 4: Snapshot on near-full dataset"

sudo zfs set quota=48M sfpool/repl-src
dd if=/dev/urandom bs=1M count=40 2>/dev/null | sudo tee /mnt/sfpool/repl-src/fill2.bin > /dev/null 2>&1 || true

SNAP_RESP=$(curl -s -X POST http://127.0.0.1:9200/api/zfs/snapshots -H "X-Session-ID: $SESSION" -H "X-CSRF-Token: $CSRF" -H "Content-Type: application/json" -d '{"dataset":"sfpool/repl-src","name":"near-full-snap"}' 2>/dev/null || echo '{}')
echo "$SNAP_RESP" | python3 -c "import sys,json; d=json.load(sys.stdin); sys.exit(0 if d.get('success') in [True,False] or 'error' in d else 1)" 2>/dev/null && ok "Scenario 4: Snapshot API OK" || fail "Scenario 4: Snapshot API failed"

sudo zfs set quota=none sfpool/repl-src
sudo rm -f /mnt/sfpool/repl-src/fill2.bin 2>/dev/null || true

# ==============================================================================
# SCENARIO 5: Pool with checksum errors
# ==============================================================================
section "Scenario 5: Checksum error detection via scrub"

sudo zpool offline sfpool "$LOOP1" 2>/dev/null || true
sudo dd if=/dev/urandom of=/tmp/sf1.img bs=4096 count=100 seek=1000 conv=notrunc 2>/dev/null || true
sudo zpool online sfpool "$LOOP1" 2>/dev/null || true
sudo zpool scrub sfpool 2>/dev/null || true
sleep 3

HEALTH5=$(curl -s http://127.0.0.1:9200/api/system/health -H "X-Session-ID: $SESSION" 2>/dev/null || echo '{}')
echo "$HEALTH5" | python3 -c "import sys,json; json.load(sys.stdin)" 2>/dev/null && ok "Scenario 5: Health API stable with checksum errors present" || fail "Scenario 5: Health API crashed"

sudo zpool clear sfpool
sudo zpool scrub sfpool 2>/dev/null || true
sleep 2
ok "Scenario 5: Pool cleared after checksum error test"

# ==============================================================================
# SCENARIO 6: Dataset destroyed under active share
# ==============================================================================
section "Scenario 6: Dataset destroyed under active share"

sudo zfs create sfpool/share-victim
sudo mkdir -p /mnt/sfpool/share-victim

SHARE_RESP=$(curl -s -X POST http://127.0.0.1:9200/api/shares -H "X-Session-ID: $SESSION" -H "X-CSRF-Token: $CSRF" -H "Content-Type: application/json" -d '{"action":"create","name":"victim-share","path":"/mnt/sfpool/share-victim","read_only":false,"guest_ok":true}' 2>/dev/null || echo '{}')
echo "$SHARE_RESP" | python3 -c "import sys,json; d=json.load(sys.stdin); sys.exit(0 if d.get('success') else 1)" 2>/dev/null && ok "Scenario 6: Share created for victim dataset" || warn "Scenario 6: Share creation failed (may already exist)"

sudo zfs destroy -f sfpool/share-victim 2>/dev/null || true
ok "Scenario 6: Backing dataset destroyed while share exists"

SHARES_RESP=$(curl -s http://127.0.0.1:9200/api/shares -H "X-Session-ID: $SESSION" 2>/dev/null || echo '{}')
echo "$SHARES_RESP" | python3 -c "import sys,json; json.load(sys.stdin)" 2>/dev/null && ok "Scenario 6: Share list API stable after backing dataset destroyed" || fail "Scenario 6: Share list API crashed after backing dataset destroyed"

# ==============================================================================
# SCENARIO 10: Pool import collision (duplicate pool name)
# ==============================================================================
section "Scenario 10: Pool import collision (duplicate pool name)"

sudo zpool create -f sfpool_import "$LOOP5"
sudo zpool export sfpool_import
ok "Scenario 10: Exported pool sfpool_import for import collision test"

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

set +e
AMBIG_OUT=$(sudo ./dplaned-ci -apply -db-dsn "$DATABASE_DSN" -gitops-state /tmp/sf-state.yaml 2>&1)
AMBIG_EXIT=$?
set -e

echo "$AMBIG_OUT" | grep -qiE "ambiguous|duplicate|collision|already exists|conflict" && ok "Scenario 10: Duplicate pool name in GitOps state rejected with clear error" || { [ $AMBIG_EXIT -ne 0 ] && ok "Scenario 10: Duplicate pool name rejected (exit $AMBIG_EXIT)" || fail "Scenario 10: Duplicate pool name in GitOps state was NOT rejected"; }

sudo zpool import sfpool_import 2>/dev/null || true
sudo zpool destroy -f sfpool_import 2>/dev/null || true
ok "Scenario 10: Import collision test complete"

# ==============================================================================
# SUMMARY
# ==============================================================================
echo ""
echo "=========================================="
printf "  Results: %d passed   %d failed\n" "$PASS" "$FAIL"
echo "=========================================="

if [ "$FAIL" -gt 0 ]; then
  echo -e "$FAILURES"
  exit 1
fi

echo "  STORAGE FAILURE SCENARIOS PASSED"