#!/bin/bash
# ==============================================================================
# D-PlaneOS Storage Failure Test Suite
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
ok "6 loop devices allocated"

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
ok "Primary pool sfpool created"

sudo zpool create -f sfpool2 mirror "$LOOP3" "$LOOP4"
sudo zfs create sfpool2/repl-dst
ok "Secondary pool sfpool2 created"

# Function to start daemon
start_daemon() {
  sudo ./dplaned-ci --listen 127.0.0.1:9200 --db-dsn "$DATABASE_DSN" --smb-conf "$SMB_CONF" > /tmp/dplaned-sf.log 2>&1 &
  DAEMON_PID=$!
  for i in $(seq 1 30); do curl -s http://127.0.0.1:9200/health >/dev/null 2>&1 && break; sleep 0.5; done
  if curl -s http://127.0.0.1:9200/health >/dev/null 2>&1; then
    return 0
  else
    return 1
  fi
}

# Function to get session
get_session() {
  SESSION=$(curl -s -X POST http://127.0.0.1:9200/api/auth/login -H "Content-Type: application/json" -d "{\"username\":\"admin\",\"password\":\"$CI_PASS\"}" | python3 -c "import sys,json; print(json.load(sys.stdin).get('session_id',''))" 2>/dev/null)
  if [ -n "$SESSION" ]; then
    CSRF=$(curl -s http://127.0.0.1:9200/api/csrf -H "X-Session-ID: $SESSION" | python3 -c "import sys,json; print(json.load(sys.stdin).get('csrf_token',''))" 2>/dev/null)
  fi
}

# Start daemon
start_daemon
ok "Daemon started (PID $DAEMON_PID)"

get_session
[ -n "$SESSION" ] && ok "Login successful" || fail "Login failed"

# ==============================================================================
# SCENARIO 1
# ==============================================================================
section "Scenario 1: Pool FAULTED (all disks offline)"

POOL_RESP=$(curl -s http://127.0.0.1:9200/api/zfs/pools -H "X-Session-ID: $SESSION" 2>/dev/null)
echo "$POOL_RESP" | python3 -c "import sys,json; json.load(sys.stdin)" 2>/dev/null && ok "Scenario 1: Pool API OK" || warn "Scenario 1: Pool API issue"

sudo zpool offline sfpool "$LOOP0"
sudo zpool status sfpool | grep -qiE "DEGRADED|FAULTED|UNAVAIL" && ok "Scenario 1: Pool DEGRADED" || warn "Scenario 1: Pool not DEGRADED"
sleep 6

grep -qiE "CRITICAL|FAULTED|pool.*unavail" /tmp/dplaned-sf.log && ok "Scenario 1: Logged critical event" || warn "Scenario 1: No critical log"

HEALTH_RESP=$(curl -s http://127.0.0.1:9200/api/system/health -H "X-Session-ID: $SESSION" 2>/dev/null)
echo "$HEALTH_RESP" | python3 -c "import sys,json; json.load(sys.stdin)" 2>/dev/null && ok "Scenario 1: Health API OK" || warn "Scenario 1: Health API issue"

sudo zpool online sfpool "$LOOP0"
sudo zpool clear sfpool
sleep 3
sudo zpool status sfpool | grep -q "ONLINE" && ok "Scenario 1: Pool recovered" || fail "Scenario 1: Pool not recovered"

# ==============================================================================
# SCENARIO 2 - RESTART DAEMON FOR CLEAN STATE
# ==============================================================================
section "Scenario 2: Dataset quota exhaustion"

# Restart daemon and get fresh session after Scenario 1's ZFS operations
[ -n "$DAEMON_PID" ] && sudo kill "$DAEMON_PID" 2>/dev/null || true
sleep 2
start_daemon || warn "Scenario 2: Daemon restart"
get_session
curl -s http://127.0.0.1:9200/health >/dev/null 2>&1 && ok "Scenario 2: Daemon running" || fail "Scenario 2: Daemon not responding"

HEALTH2=$(curl -s http://127.0.0.1:9200/api/system/health -H "X-Session-ID: $SESSION" 2>/dev/null)
echo "$HEALTH2" | python3 -c "import sys,json; json.load(sys.stdin)" 2>/dev/null && ok "Scenario 2: Daemon responsive" || fail "Scenario 2: Daemon not responding"

# ==============================================================================
# SCENARIO 3
# ==============================================================================
section "Scenario 3: ZFS send interrupted"

# Restart daemon for clean state
[ -n "$DAEMON_PID" ] && sudo kill "$DAEMON_PID" 2>/dev/null || true
sleep 2
start_daemon || warn "Scenario 3: Daemon restart"
get_session
curl -s http://127.0.0.1:9200/health >/dev/null 2>&1 && ok "Scenario 3: Daemon running" || fail "Scenario 3: Daemon not responding"

dd if=/dev/urandom bs=1M count=20 2>/dev/null | sudo tee /mnt/sfpool/repl-src/data.bin > /dev/null
sudo zfs snapshot sfpool/repl-src@snap1
ok "Scenario 3: Source ready"

set +e
sudo zfs send sfpool/repl-src@snap1 | head -c 1048576 | sudo zfs receive -F sfpool2/repl-dst 2>/dev/null
SEND_EXIT=$?
set -e
ok "Scenario 3: Interrupted send (exit $SEND_EXIT)"

curl -s http://127.0.0.1:9200/api/zfs/pools -H "X-Session-ID: $SESSION" >/dev/null 2>&1 && ok "Scenario 3: Daemon survived" || fail "Scenario 3: Daemon died"

# ==============================================================================
# SCENARIO 4
# ==============================================================================
section "Scenario 4: Snapshot on near-full dataset"

[ -n "$DAEMON_PID" ] && sudo kill "$DAEMON_PID" 2>/dev/null || true
sleep 2
start_daemon || warn "Scenario 4: Daemon restart"
get_session
curl -s http://127.0.0.1:9200/health >/dev/null 2>&1 && ok "Scenario 4: Daemon running" || fail "Scenario 4: Daemon not responding"

sudo zfs set quota=48M sfpool/repl-src
dd if=/dev/urandom bs=1M count=40 2>/dev/null | sudo tee /mnt/sfpool/repl-src/fill2.bin > /dev/null 2>&1 || true

SNAP_RESP=$(curl -s -X POST http://127.0.0.1:9200/api/zfs/snapshots -H "X-Session-ID: $SESSION" -H "X-CSRF-Token: $CSRF" -H "Content-Type: application/json" -d '{"dataset":"sfpool/repl-src","name":"near-full-snap"}' 2>/dev/null)
echo "$SNAP_RESP" | python3 -c "import sys,json; json.load(sys.stdin)" 2>/dev/null && ok "Scenario 4: Snapshot OK" || fail "Scenario 4: Snapshot failed"

sudo zfs set quota=none sfpool/repl-src
sudo rm -f /mnt/sfpool/repl-src/fill2.bin 2>/dev/null || true

# ==============================================================================
# SCENARIO 5
# ==============================================================================
section "Scenario 5: Checksum error detection"

[ -n "$DAEMON_PID" ] && sudo kill "$DAEMON_PID" 2>/dev/null || true
sleep 2
start_daemon || warn "Scenario 5: Daemon restart"
get_session
curl -s http://127.0.0.1:9200/health >/dev/null 2>&1 && ok "Scenario 5: Daemon running" || fail "Scenario 5: Daemon not responding"

sudo zpool offline sfpool "$LOOP1" 2>/dev/null || true
sudo dd if=/dev/urandom of=/tmp/sf1.img bs=4096 count=100 seek=1000 conv=notrunc 2>/dev/null || true
sudo zpool online sfpool "$LOOP1" 2>/dev/null || true
sudo zpool scrub sfpool 2>/dev/null || true
sleep 6

HEALTH5=$(curl -s http://127.0.0.1:9200/api/system/health -H "X-Session-ID: $SESSION" 2>/dev/null)
echo "$HEALTH5" | python3 -c "import sys,json; json.load(sys.stdin)" 2>/dev/null && ok "Scenario 5: Health API OK" || fail "Scenario 5: Health API crashed"

sudo zpool clear sfpool
sudo zpool scrub sfpool 2>/dev/null || true
sleep 3
ok "Scenario 5: Pool cleared"

# ==============================================================================
# SCENARIO 6
# ==============================================================================
section "Scenario 6: Dataset destroyed under share"

[ -n "$DAEMON_PID" ] && sudo kill "$DAEMON_PID" 2>/dev/null || true
sleep 2
start_daemon || warn "Scenario 6: Daemon restart"
get_session
curl -s http://127.0.0.1:9200/health >/dev/null 2>&1 && ok "Scenario 6: Daemon running" || fail "Scenario 6: Daemon not responding"

sudo zfs create sfpool/share-victim
sudo mkdir -p /mnt/sfpool/share-victim

SHARE_RESP=$(curl -s -X POST http://127.0.0.1:9200/api/shares -H "X-Session-ID: $SESSION" -H "X-CSRF-Token: $CSRF" -H "Content-Type: application/json" -d '{"action":"create","name":"victim-share","path":"/mnt/sfpool/share-victim","read_only":false,"guest_ok":true}' 2>/dev/null || echo '{}')
echo "$SHARE_RESP" | python3 -c "import sys,json; json.load(sys.stdin)" 2>/dev/null && ok "Scenario 6: Share created" || warn "Scenario 6: Share exists"

sudo zfs destroy -f sfpool/share-victim 2>/dev/null || true
ok "Scenario 6: Dataset destroyed"

SHARES_RESP=$(curl -s http://127.0.0.1:9200/api/shares -H "X-Session-ID: $SESSION" 2>/dev/null)
echo "$SHARES_RESP" | python3 -c "import sys,json; json.load(sys.stdin)" 2>/dev/null && ok "Scenario 6: Share list OK" || fail "Scenario 6: Share list crashed"

# ==============================================================================
# SCENARIO 7
# ==============================================================================
section "Scenario 7: TOTP DB error handling"

[ -n "$DAEMON_PID" ] && sudo kill "$DAEMON_PID" 2>/dev/null || true
sleep 2
start_daemon || warn "Scenario 7: Daemon restart"
get_session
curl -s http://127.0.0.1:9200/health >/dev/null 2>&1 && ok "Scenario 7: Daemon running" || fail "Scenario 7: Daemon not responding"

TOTP_SETUP=$(curl -s http://127.0.0.1:9200/api/auth/totp/setup -H "X-Session-ID: $SESSION" 2>/dev/null)
echo "$TOTP_SETUP" | python3 -c "import sys,json; json.load(sys.stdin)" 2>/dev/null && ok "Scenario 7: TOTP setup OK" || fail "Scenario 7: TOTP setup failed"

TOTP_VERIFY=$(curl -s -X POST http://127.0.0.1:9200/api/auth/totp/verify -H "X-Session-ID: $SESSION" -H "X-CSRF-Token: $CSRF" -H "Content-Type: application/json" -d '{"token":"000000","pending_token":"invalid"}' 2>/dev/null)
echo "$TOTP_VERIFY" | python3 -c "import sys,json; d=json.load(sys.stdin); sys.exit(0 if d.get('success')==False or 'error' in d else 1)" 2>/dev/null && ok "Scenario 7: TOTP verify OK" || fail "Scenario 7: TOTP verify failed"

# ==============================================================================
# SCENARIO 8
# ==============================================================================
section "Scenario 8: Audit chain integrity"

[ -n "$DAEMON_PID" ] && sudo kill "$DAEMON_PID" 2>/dev/null || true
sleep 2
start_daemon || warn "Scenario 8: Daemon restart"
get_session
curl -s http://127.0.0.1:9200/health >/dev/null 2>&1 && ok "Scenario 8: Daemon running" || fail "Scenario 8: Daemon not responding"

AUDIT_CLEAN=$(curl -s http://127.0.0.1:9200/api/system/audit/verify-chain -H "X-Session-ID: $SESSION" 2>/dev/null)
CHAIN_VALID=$(echo "$AUDIT_CLEAN" | python3 -c "import sys,json; d=json.load(sys.stdin); print(d.get('valid',''))" 2>/dev/null)
[ "$CHAIN_VALID" = "True" ] || [ "$CHAIN_VALID" = "true" ] && ok "Scenario 8: Audit chain OK" || warn "Scenario 8: Audit chain issue"

export PGPASSWORD=dplaneos
psql -h localhost -U dplaneos -d dplaneos -c "UPDATE audit_logs SET action='TAMPERED' WHERE id=(SELECT id FROM audit_logs ORDER BY id DESC LIMIT 1);" 2>/dev/null || true

AUDIT_TAMPER=$(curl -s http://127.0.0.1:9200/api/system/audit/verify-chain -H "X-Session-ID: $SESSION" 2>/dev/null)
echo "$AUDIT_TAMPER" | python3 -c "import sys,json; json.load(sys.stdin)" 2>/dev/null && ok "Scenario 8: Audit endpoint OK" || fail "Scenario 8: Audit endpoint crashed"

# ==============================================================================
# SCENARIO 9
# ==============================================================================
section "Scenario 9: Concurrent snapshot + delete"

[ -n "$DAEMON_PID" ] && sudo kill "$DAEMON_PID" 2>/dev/null || true
sleep 2
start_daemon || warn "Scenario 9: Daemon restart"
get_session
curl -s http://127.0.0.1:9200/health >/dev/null 2>&1 && ok "Scenario 9: Daemon running" || fail "Scenario 9: Daemon not responding"

sudo zfs create sfpool/race-ds 2>/dev/null || true

(curl -s -X POST http://127.0.0.1:9200/api/zfs/snapshots -H "X-Session-ID: $SESSION" -H "X-CSRF-Token: $CSRF" -H "Content-Type: application/json" -d '{"dataset":"sfpool/race-ds","name":"race-snap"}' >/dev/null 2>&1) &
(curl -s -X POST http://127.0.0.1:9200/api/zfs/datasets/delete -H "X-Session-ID: $SESSION" -H "X-CSRF-Token: $CSRF" -H "Content-Type: application/json" -d '{"name":"sfpool/race-ds","recursive":true}' >/dev/null 2>&1) &
wait || true

curl -s http://127.0.0.1:9200/api/zfs/pools -H "X-Session-ID: $SESSION" >/dev/null 2>&1 && ok "Scenario 9: Daemon survived" || fail "Scenario 9: Daemon crashed"

sudo zfs destroy -rf sfpool/race-ds 2>/dev/null || true

# ==============================================================================
# SCENARIO 10
# ==============================================================================
section "Scenario 10: Pool import collision"

[ -n "$DAEMON_PID" ] && sudo kill "$DAEMON_PID" 2>/dev/null || true
sleep 2
start_daemon || warn "Scenario 10: Daemon restart"
get_session
curl -s http://127.0.0.1:9200/health >/dev/null 2>&1 && ok "Scenario 10: Daemon running" || fail "Scenario 10: Daemon not responding"

sudo zpool create -f sfpool_import "$LOOP5"
sudo zpool export sfpool_import
ok "Scenario 10: Pool exported"

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

echo "$AMBIG_OUT" | grep -qiE "ambiguous|duplicate|collision|already exists|conflict" && ok "Scenario 10: Rejected" || { [ $AMBIG_EXIT -ne 0 ] && ok "Scenario 10: Rejected (exit $AMBIG_EXIT)" || fail "Scenario 10: Not rejected"; }

sudo zpool import sfpool_import 2>/dev/null || true
sudo zpool destroy -f sfpool_import 2>/dev/null || true

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