#!/bin/bash
set -e

# --- 1. INSTALL.SH TESTS ---
echo "--- JOB 1: install.sh (fresh + idempotent + upgrade) ---"
sudo truncate -s 512M /tmp/inst0.img /tmp/inst1.img
L0=$(sudo losetup --find --show /tmp/inst0.img); L1=$(sudo losetup --find --show /tmp/inst1.img)
sudo zpool create -f installpool mirror "$L0" "$L1"

# Fresh install (--unattended)
sudo bash install.sh --unattended --port 9100
VERSION="$(cat VERSION | tr -d '[:space:]')"

# Verify health
for i in $(seq 1 20); do
  curl -sf http://127.0.0.1:9100/health | grep -q '"ok"' && break
  sleep 1
done
curl -sf http://127.0.0.1:9100/health | grep -q "$VERSION"

# Idempotent re-run
sudo bash install.sh --unattended --port 9100
curl -sf http://127.0.0.1:9100/health | grep -q '"ok"'

# Cleanup Job 1
sudo systemctl stop dplaned 2>/dev/null || true
sudo zpool destroy installpool 2>/dev/null || true
sudo losetup -d "$L0" "$L1" 2>/dev/null || true

# --- 2. GITOPS TESTS ---
echo "--- JOB 2: GitOps apply pipeline ---"
sudo truncate -s 512M /tmp/g0.img /tmp/g1.img
L0=$(sudo losetup --find --show /tmp/g0.img); L1=$(sudo losetup --find --show /tmp/g1.img)
sudo zpool create -f gitopspool mirror "$L0" "$L1"
POOL_GUID=$(sudo zpool list -H -o guid gitopspool)

sudo mkdir -p /var/lib/dplaneos
sudo bash install/scripts/init-database-with-lock.sh

cat > /tmp/gitops-state.yaml <<EOF
version: "1"
pools:
  - name: gitopspool
    guid: "${POOL_GUID}"
    disks: [${L0}, ${L1}]
datasets:
  - name: gitopspool/media
    mountpoint: /mnt/media
    compression: lz4
EOF

# Apply 1: fresh
sudo ./dplaned-ci -apply -db /var/lib/dplaneos/dplaneos.db -gitops-state /tmp/gitops-state.yaml -smb-conf /tmp/smb-ci.conf
sudo zfs list gitopspool/media

# Apply 2: idempotency
sudo ./dplaned-ci -apply -db /var/lib/dplaneos/dplaneos.db -gitops-state /tmp/gitops-state.yaml -smb-conf /tmp/smb-ci.conf

# Convergence check
CONV=$(sudo ./dplaned-ci -convergence-check -db /var/lib/dplaneos/dplaneos.db -gitops-state /tmp/gitops-state.yaml 2>&1)
echo "$CONV" | grep -q "CONVERGED"

# Cleanup Job 2
sudo zpool destroy gitopspool 2>/dev/null || true
sudo losetup -d "$L0" "$L1" 2>/dev/null || true

# --- 3. FLEET SIMULATION ---
echo "--- JOB 3: Fleet simulation (2 nodes) ---"
sudo truncate -s 512M /tmp/a0.img /tmp/a1.img /tmp/b0.img /tmp/b1.img
LA0=$(sudo losetup --find --show /tmp/a0.img); LA1=$(sudo losetup --find --show /tmp/a1.img)
LB0=$(sudo losetup --find --show /tmp/b0.img); LB1=$(sudo losetup --find --show /tmp/b1.img)
sudo zpool create -f nodeapool mirror "$LA0" "$LA1"
GUID_A=$(sudo zpool list -H -o guid nodeapool)
sudo zpool create -f nodebpool mirror "$LB0" "$LB1"
GUID_B=$(sudo zpool list -H -o guid nodebpool)

sudo mkdir -p /var/lib/dplaneos-node-a /var/lib/dplaneos-node-b
cat > /tmp/state-node-a.yaml <<EOF
version: "1"
pools: [{ name: nodeapool, guid: "${GUID_A}", disks: [${LA0}, ${LA1}] }]
datasets: [{ name: nodeapool/data, mountpoint: /mnt/node-a/data }]
EOF
cat > /tmp/state-node-b.yaml <<EOF
version: "1"
pools: [{ name: nodebpool, guid: "${GUID_B}", disks: [${LB0}, ${LB1}] }]
datasets: [{ name: nodebpool/data, mountpoint: /mnt/node-b/data }]
EOF

# Initialise databases (using my fixed script with --db)
sudo bash install/scripts/init-database-with-lock.sh --db /var/lib/dplaneos-node-a/dplaneos.db
sudo bash install/scripts/init-database-with-lock.sh --db /var/lib/dplaneos-node-b/dplaneos.db

# Apply state
sudo ./dplaned-ci -apply -db /var/lib/dplaneos-node-a/dplaneos.db -gitops-state /tmp/state-node-a.yaml -smb-conf /tmp/smb-node-a.conf
sudo ./dplaned-ci -apply -db /var/lib/dplaneos-node-b/dplaneos.db -gitops-state /tmp/state-node-b.yaml -smb-conf /tmp/smb-node-b.conf

# Verify Convergence
CONV_A=$(sudo ./dplaned-ci -convergence-check -db /var/lib/dplaneos-node-a/dplaneos.db -gitops-state /tmp/state-node-a.yaml 2>&1)
echo "$CONV_A" | grep -q "CONVERGED"
CONV_B=$(sudo ./dplaned-ci -convergence-check -db /var/lib/dplaneos-node-b/dplaneos.db -gitops-state /tmp/state-node-b.yaml 2>&1)
echo "$CONV_B" | grep -q "CONVERGED"

# Cleanup Job 3
sudo zpool destroy nodeapool 2>/dev/null || true
sudo zpool destroy nodebpool 2>/dev/null || true
sudo losetup -d "$LA0" "$LA1" "$LB0" "$LB1" 2>/dev/null || true

echo "Fleet Simulation PASSED"
