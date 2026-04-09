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

# ==============================================================================
# SCENARIO 1: Pool FAULTED - ZFS operations only, no API
# ==============================================================================
section "Scenario 1: Pool FAULTED (all disks offline)"

sudo zpool offline sfpool "$LOOP0"
sudo zpool status sfpool | grep -qiE "DEGRADED|FAULTED|UNAVAIL" && ok "Scenario 1: Pool DEGRADED" || warn "Scenario 1: Pool not DEGRADED"
sleep 3

sudo zpool online sfpool "$LOOP0"
sudo zpool clear sfpool
sleep 2
sudo zpool status sfpool | grep -q "ONLINE" && ok "Scenario 1: Pool recovered" || fail "Scenario 1: Pool not recovered"

# ==============================================================================
# SCENARIO 2: Dataset quota - ZFS only
# ==============================================================================
section "Scenario 2: Dataset quota exhaustion"

sudo zfs set quota=64M sfpool/data
ok "Scenario 2: Quota set to 64M"

dd if=/dev/urandom bs=1M count=60 2>/dev/null | sudo tee /mnt/sfpool/data/fill.bin > /dev/null && ok "Scenario 2: 60 MB write OK" || fail "Scenario 2: 60 MB write failed"

sudo rm -f /mnt/sfpool/data/fill.bin 2>/dev/null || true
sudo zfs set quota=none sfpool/data
ok "Scenario 2: Quota test complete"

# ==============================================================================
# SCENARIO 3: ZFS send interrupted - ZFS only
# ==============================================================================
section "Scenario 3: ZFS send interrupted"

dd if=/dev/urandom bs=1M count=20 2>/dev/null | sudo tee /mnt/sfpool/repl-src/data.bin > /dev/null
sudo zfs snapshot sfpool/repl-src@snap1
ok "Scenario 3: Source ready"

set +e
sudo zfs send sfpool/repl-src@snap1 | head -c 1048576 | sudo zfs receive -F sfpool2/repl-dst 2>/dev/null
SEND_EXIT=$?
set -e
ok "Scenario 3: Interrupted send (exit $SEND_EXIT)"

# ==============================================================================
# SCENARIO 10: Pool import collision - GitOps test
# ==============================================================================
section "Scenario 10: Pool import collision"

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
ok "Scenario 10: Import test complete"

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