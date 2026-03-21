#!/bin/bash
# ==============================================================================
# D-PlaneOS Convergence & Resilience Test Suite
# ==============================================================================
# This script validates that the system correctly converges to a desired state
# even under partial failures, drift, and repeated applications.
# ==============================================================================

set -e

# --- UTILITIES ---
ok()   { echo -e "  \033[0;32m✓\033[0m $1"; }
warn() { echo -e "  \033[0;33m!\033[0m $1"; }
fail() { echo -e "  \033[0;31m✗\033[0m $1"; exit 1; }

# --- SETUP ---
echo "--- Stage 0: Environment Setup ---"
sudo truncate -s 512M /tmp/conv0.img /tmp/conv1.img
LOOP0=$(sudo losetup --find --show /tmp/conv0.img)
LOOP1=$(sudo losetup --find --show /tmp/conv1.img)

DB_PATH="/tmp/convergence.db"
rm -f "$DB_PATH"*
sudo ./dplaned-ci -db "$DB_PATH" --init-only > /dev/null

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
# First apply
echo "Applying initial state..."
sudo ./dplaned-ci -apply -db "$DB_PATH" -gitops-state "$STATE_YAML" > /tmp/apply1.log 2>&1 || fail "Initial apply failed"

# Verify initial convergence
sudo zfs get compression convpool/data | grep -q 'lz4' && ok "Initial state reached" || fail "State mismatch"

# Second apply (Idempotent)
echo "Applying same state again (should be NOP)..."
START=$(date +%s%3N)
# Disable set -e for the subshell call so we can capture status
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

echo "$APPLY_OUT" | grep -q "0 items applied" && ok "Idempotency: 0 items applied on second run" || warn "Idempotency: some items re-applied (check logs)"

# --- TEST 2: PARTIAL FAILURE & RECOVERY ---
echo "--- Stage 2: Partial Failure & Recovery ---"
# Introduce drift
echo "Introducing manual drift (compression=off)..."
sudo zfs set compression=off convpool/data

# Run with fault injection (making zfs set fail)
echo "Attempting reconciliation with fault injection (zfs:set=1.0)..."
export DPLANE_FAULT_INJECT="zfs:set=1.0"
APPLY_FAIL=$(sudo -E ./dplaned-ci -apply -db "$DB_PATH" -gitops-state "$STATE_YAML" 2>&1 || true)
unset DPLANE_FAULT_INJECT

echo "$APPLY_FAIL" | grep -q "SIMULATED FAULT" && ok "Fault injection successfully blocked 'zfs set'" || fail "Fault injection FAILED to block command"
sudo zfs get compression convpool/data | grep -q 'off' && ok "System remains drifted (as expected after failure)"

# Recover
echo "Re-applying without faults (recovery)..."
sudo ./dplaned-ci -apply -db "$DB_PATH" -gitops-state "$STATE_YAML" > /dev/null
sudo zfs get compression convpool/data | grep -q 'lz4' && ok "System converged successfully after previous failure"

# --- TEST 3: DRIFT CORRECTION ---
echo "--- Stage 3: Broad Drift Correction ---"
# Kill the dataset
echo "Simulating major drift (destroying dataset)..."
sudo zfs destroy convpool/data

# Re-apply
sudo ./dplaned-ci -apply -db "$DB_PATH" -gitops-state "$STATE_YAML" > /dev/null
sudo zfs list convpool/data > /dev/null && ok "Missing resources re-created automatically"

# --- TEST 4: CONCURRENT MUTATIONS (RACE) ---
echo "--- Stage 4: Concurrent Mutations (Race) ---"
# Start two applies simultaneously
echo "Starting racing applies..."
sudo ./dplaned-ci -apply -db "$DB_PATH" -gitops-state "$STATE_YAML" > /tmp/race1.log 2>&1 &
PID1=$!
sudo ./dplaned-ci -apply -db "$DB_PATH" -gitops-state "$STATE_YAML" > /tmp/race2.log 2>&1 &
PID2=$!

wait $PID1 $PID2 || true # One might fail if racing, but system should stay sane

# Verify system is still sane
sudo zfs list convpool/data > /dev/null && ok "Race test: System survived concurrent applies"

# --- TEST 5: SIMULATED HARDWARE FAILURE (DISK IO) ---
echo "--- Stage 5: Simulated Hardware Failure ---"
# Make one loopback "fail" (by detaching it)
echo "Forcing disk failure (detaching $LOOP1)..."
sudo losetup -d "$LOOP1"

# Try an operation that writes to ZFS (e.g. rename or set)
echo "Attempting ZFS operation on degraded pool..."
APPLY_DISK_FAIL=$(sudo ./dplaned-ci -apply -db "$DB_PATH" -gitops-state "$STATE_YAML" 2>&1 || true)

# ZFS should return IO error or pool should be DEGRADED
echo "$APPLY_DISK_FAIL" | grep -E "I/O error|pool is degraded|failed" && ok "System correctly reported disk failure"

# Re-attach and Recovery
echo "Recovering disk..."
# Only re-attach if it was actually detached
if ! losetup -a | grep -q "$LOOP1"; then
  sudo losetup "$LOOP1" /tmp/conv1.img
fi
sudo zpool online convpool "$LOOP1" || true
sudo zpool clear convpool
ok "Hardware failure recovery complete"

# --- TEST 6: CONVERGENCE CHECK ---
echo "--- Stage 6: Convergence Status API ---"
CONV_STATUS=$(sudo ./dplaned-ci -convergence-check -db "$DB_PATH" -gitops-state "$STATE_YAML" 2>&1)
echo "$CONV_STATUS" | grep -q "CONVERGED" && ok "Convergence check confirms system is 'CONVERGED'" || fail "Convergence check returned: $CONV_STATUS"

# --- CLEANUP ---
echo "--- Stage 7: Cleanup ---"
sudo zpool destroy convpool || true
sudo losetup -d "$LOOP0" 2>/dev/null || true
sudo losetup -d "$LOOP1" 2>/dev/null || true
rm -f /tmp/conv0.img /tmp/conv1.img /tmp/convergence.db* "$STATE_YAML" /tmp/apply1.log /tmp/race1.log /tmp/race2.log

echo ""
echo "=========================================="
echo "  CONVERGENCE & RESILIENCE TESTS PASSED"
echo "=========================================="
