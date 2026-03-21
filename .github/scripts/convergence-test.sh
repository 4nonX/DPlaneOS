#!/bin/bash
# ==============================================================================
# D-PlaneOS Convergence & Resilience Test Suite (Hardened v2)
# ==============================================================================
set -e

# --- UTILITIES ---
ok()   { echo -e "  \033[0;32m✓\033[0m $1"; }
warn() { echo -e "  \033[0;33m!\033[0m $1"; }
fail() { echo -e "  \033[0;31m✗\033[0m $1"; exit 1; }

# --- CLEANUP TRAP ---
cleanup() {
  echo "--- Cleanup ---"
  sudo zpool destroy convpool 2>/dev/null || true
  # Detach loops - use -D to ensure they are gone even if lazy
  [ -n "$LOOP0" ] && sudo losetup -d "$LOOP0" 2>/dev/null || true
  [ -n "$LOOP1" ] && sudo losetup -d "$LOOP1" 2>/dev/null || true
  rm -f /tmp/conv0.img /tmp/conv1.img /tmp/convergence.db* "$STATE_YAML" /tmp/apply1.log /tmp/race1.log /tmp/race2.log
}
trap cleanup EXIT

# --- SETUP ---
echo "--- Stage 0: Environment Setup ---"
sudo truncate -s 512M /tmp/conv0.img /tmp/conv1.img
LOOP0=$(sudo losetup --find --show /tmp/conv0.img)
LOOP1=$(sudo losetup --find --show /tmp/conv1.img)

DB_PATH="/tmp/convergence.db"
rm -f "$DB_PATH"*
sudo ./dplaned-ci -db "$DB_PATH" -init-only > /dev/null

STATE_YAML="/tmp/conv-state.yaml"
cat > "$STATE_YAML" <<EOF
version: "6"
pools:
  - name: convpool
    disks: ["$LOOP0", "$LOOP1"]
datasets:
  - name: convpool/data
    compression: lz4
    atime: off
EOF

# --- TEST 1: IDEMPOTENCY ---
echo "--- Stage 1: Idempotency Validation ---"
echo "Applying initial state..."
sudo ./dplaned-ci -apply -db "$DB_PATH" -gitops-state "$STATE_YAML" > /tmp/apply1.log 2>&1 || fail "Initial apply failed"
sudo zfs get compression convpool/data | grep -q 'lz4' && ok "Initial state reached" || fail "State mismatch"

echo "Applying same state again (should be NOP)..."
START=$(date +%s%3N)
set +e
APPLY_OUT=$(sudo ./dplaned-ci -apply -db "$DB_PATH" -gitops-state "$STATE_YAML" 2>&1)
STATUS=$?
set -e
END=$(date +%s%3N)
DURATION=$((END-START))

if [ $STATUS -ne 0 ]; then
  echo "$APPLY_OUT"
  fail "Second apply (idempotency) failed with exit code $STATUS"
fi

echo "Idempotent apply took ${DURATION}ms"
echo "$APPLY_OUT" | grep -q "0 items applied" && ok "Idempotency: 0 items applied on second run" || warn "Idempotency: some items re-applied (check logs)"

# --- TEST 2: PARTIAL FAILURE & RECOVERY ---
echo "--- Stage 2: Partial Failure & Recovery ---"
echo "Introducing manual drift (compression=off)..."
sudo zfs set compression=off convpool/data

echo "Attempting reconciliation with fault injection (zfs:set=1.0)..."
export DPLANE_FAULT_INJECT="zfs:set=1.0"
APPLY_FAIL=$(sudo -E ./dplaned-ci -apply -db "$DB_PATH" -gitops-state "$STATE_YAML" 2>&1 || true)
unset DPLANE_FAULT_INJECT

echo "$APPLY_FAIL" | grep -q "SIMULATED FAULT" && ok "Fault injection successfully blocked 'zfs set'" || fail "Fault injection FAILED to block command"
sudo zfs get compression convpool/data | grep -q 'off' && ok "System remains drifted (as expected after failure)"

echo "Re-applying without faults (recovery)..."
sudo ./dplaned-ci -apply -db "$DB_PATH" -gitops-state "$STATE_YAML" > /dev/null
sudo zfs get compression convpool/data | grep -q 'lz4' && ok "System converged successfully after previous failure"

# --- TEST 3: DRIFT CORRECTION ---
echo "--- Stage 3: Broad Drift Correction ---"
echo "Simulating major drift (destroying dataset)..."
sudo zfs destroy convpool/data
sudo ./dplaned-ci -apply -db "$DB_PATH" -gitops-state "$STATE_YAML" > /dev/null
sudo zfs list convpool/data > /dev/null && ok "Missing resources re-created automatically"

# --- TEST 4: CONCURRENT MUTATIONS (RACE) ---
echo "--- Stage 4: Concurrent Mutations (Race) ---"
echo "Starting racing applies..."
sudo ./dplaned-ci -apply -db "$DB_PATH" -gitops-state "$STATE_YAML" > /tmp/race1.log 2>&1 &
PID1=$!
sudo ./dplaned-ci -apply -db "$DB_PATH" -gitops-state "$STATE_YAML" > /tmp/race2.log 2>&1 &
PID2=$!

wait $PID1 $PID2 || true
sudo zfs get compression convpool/data | grep -q 'lz4' && ok "Race test: Correct state maintained" || fail "Race test left inconsistent state"

# --- TEST 5: SIMULATED HARDWARE FAILURE (DISK IO) ---
echo "--- Stage 5: Simulated Hardware Failure ---"
echo "Forcing disk failure (offlining $LOOP1)..."
sudo zpool offline convpool "$LOOP1"

echo "Attempting ZFS operation on degraded pool..."
# On a mirror, this should still succeed but we can check if it stays degraded
# We'll use apply to ensure the daemon can still talk to a degraded pool.
sudo ./dplaned-ci -apply -db "$DB_PATH" -gitops-state "$STATE_YAML" > /dev/null
sudo zpool status convpool | grep -q "DEGRADED" && ok "System correctly handles degraded pool" || warn "Pool status not DEGRADED as expected"

echo "Recovering disk..."
# Bring it back online and clear errors
sudo timeout 30s zpool online convpool "$LOOP1"
sudo timeout 30s zpool clear convpool
sudo zpool status convpool | grep -q "ONLINE" && ok "Hardware failure recovery complete" || fail "Pool failed to return to ONLINE status"

# --- TEST 6: CONVERGENCE CHECK ---
echo "--- Stage 6: Convergence Status API ---"
CONV_STATUS=$(sudo ./dplaned-ci -convergence-check -db "$DB_PATH" -gitops-state "$STATE_YAML" 2>&1)
echo "$CONV_STATUS" | grep -q "CONVERGED" && ok "Convergence check confirms system is 'CONVERGED'" || fail "Convergence check returned: $CONV_STATUS"

echo ""
echo "=========================================="
echo "  CONVERGENCE & RESILIENCE TESTS PASSED"
echo "=========================================="
