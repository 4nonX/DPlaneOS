#!/bin/bash
set -e

# Helper for colorized output
ok()   { echo -e "  \033[0;32m✓\033[0m $1"; }
fail() { echo -e "  \033[0;31m✗\033[0m $1"; exit 1; }

echo "--- Phase 1: Installation Simulation ---"
sudo truncate -s 512M /tmp/inst0.img /tmp/inst1.img
# Use the fixed installer with PostgreSQL DSN
sudo bash install.sh --unattended --port 9100 --db-dsn "$DATABASE_DSN"

# Verify installation artifacts
[ -f /opt/dplaneos/daemon/dplaned ] && ok "Daemon binary installed" || fail "Binary missing"
[ -f /etc/nginx/sites-enabled/dplaneos ] && ok "Nginx config enabled" || fail "Nginx config missing"
export PGPASSWORD=dplaneos
psql -h localhost -U dplaneos -d dplaneos -c "SELECT 1" >/dev/null && ok "Database initialized" || fail "DB missing or unreachable"

# Health check
for i in $(seq 1 10); do
  curl -sf http://127.0.0.1:9100/health | grep -q '"ok"' && break
  sleep 1
done
curl -sf http://127.0.0.1:9100/health | grep -q '"ok"' && ok "Health check passed" || fail "Health check failed"

# Version check
INST_VER=$(curl -sf http://127.0.0.1:9100/api/system/status | python3 -c "import sys,json; print(json.load(sys.stdin).get('version',''))")
EXPECT_VER=$(cat VERSION | tr -d '[:space:]')
[ "$INST_VER" = "$EXPECT_VER" ] && ok "Version match: $INST_VER" || fail "Version mismatch: $INST_VER vs $EXPECT_VER"

echo "--- Phase 2: Idempotency & Upgrade Simulation ---"
sudo bash install.sh --unattended --upgrade --port 9101
grep -q "listen 9101" /etc/nginx/sites-available/dplaneos && ok "Upgrade port updated" || fail "Upgrade port mismatch"
curl -sf http://127.0.0.1:9101/health | grep -q '"ok"' && ok "Health check after upgrade passed"

echo "--- Phase 3: GitOps Simulation ---"
sudo truncate -s 512M /tmp/gitops0.img /tmp/gitops1.img
LOOP0=$(sudo losetup --find --show /tmp/gitops0.img)
LOOP1=$(sudo losetup --find --show /tmp/gitops1.img)

cat > /tmp/gitops.yaml <<EOF
version: "6"
ignore_extraneous: true
pools:
  - name: gitopspool
    mountpoint: /mnt/gitops
    disks: ["$LOOP0", "$LOOP1"]
datasets:
  - name: gitopspool
    mountpoint: /mnt/gitops
  - name: gitopspool/data
    mountpoint: /mnt/gitops/data
    compression: lz4
EOF

# Apply GitOps
sudo ./dplaned-ci -apply -db-dsn "$DATABASE_DSN" -gitops-state /tmp/gitops.yaml
sudo zfs list gitopspool/data > /dev/null && ok "GitOps dataset created" || fail "GitOps dataset missing"

# Drift correction
sudo zfs set compression=off gitopspool/data
sudo ./dplaned-ci -apply -db /var/lib/dplaneos/dplaneos.db -gitops-state /tmp/gitops.yaml
COMP=$(sudo zfs get -H -o value compression gitopspool/data)
[ "$COMP" = "lz4" ] && ok "Drift corrected (lz4)" || fail "Drift correction failed: $COMP"

# Cleanup Phase 3 pool to avoid interference with declarative Node A/B in Phase 4
# Multi-stage forceful cleanup because of persistent Ubuntu runner unmount failures
sudo zfs unmount -f gitopspool/data || true
sudo zfs unmount -f gitopspool || true
sudo umount -l /mnt/gitops/data || true
sudo umount -l /mnt/gitops || true
sudo zpool destroy -f gitopspool || true
sudo losetup -d "$LOOP0" "$LOOP1" || true

echo "--- Phase 4: Fleet Simulation (Multi-Node) ---"
# Node A
sudo truncate -s 512M /tmp/nodea0.img /tmp/nodea1.img
LA0=$(sudo losetup --find --show /tmp/nodea0.img)
LA1=$(sudo losetup --find --show /tmp/nodea1.img)
sudo mkdir -p /var/lib/dplaneos-node-a
export PGPASSWORD=dplaneos
psql -h localhost -U dplaneos -d postgres -c "DROP DATABASE IF EXISTS dplaneos_node_a;"
psql -h localhost -U dplaneos -d postgres -c "CREATE DATABASE dplaneos_node_a;"
export DATABASE_DSN_A="postgres://dplaneos:dplaneos@localhost:5432/dplaneos_node_a?sslmode=disable"

cat > /tmp/state-node-a.yaml <<EOF
version: "6"
ignore_extraneous: true
pools:
  - name: nodeapool
    mountpoint: /mnt/node-a
    disks: ["$LA0", "$LA1"]
datasets:
  - name: nodeapool
    mountpoint: /mnt/node-a
  - name: nodeapool/data
    mountpoint: /mnt/node-a/data
EOF
sudo ./dplaned-ci -apply -db-dsn "$DATABASE_DSN_A" -gitops-state /tmp/state-node-a.yaml

# Node B
sudo truncate -s 512M /tmp/nodeb0.img /tmp/nodeb1.img
LB0=$(sudo losetup --find --show /tmp/nodeb0.img)
LB1=$(sudo losetup --find --show /tmp/nodeb1.img)
sudo mkdir -p /var/lib/dplaneos-node-b
export PGPASSWORD=dplaneos
psql -h localhost -U dplaneos -d postgres -c "DROP DATABASE IF EXISTS dplaneos_node_b;"
psql -h localhost -U dplaneos -d postgres -c "CREATE DATABASE dplaneos_node_b;"
export DATABASE_DSN_B="postgres://dplaneos:dplaneos@localhost:5432/dplaneos_node_b?sslmode=disable"

cat > /tmp/state-node-b.yaml <<EOF
version: "6"
ignore_extraneous: true
pools:
  - name: nodebpool
    mountpoint: /mnt/node-b
    disks: ["$LB0", "$LB1"]
datasets:
  - name: nodebpool
    mountpoint: /mnt/node-b
  - name: nodebpool/data
    mountpoint: /mnt/node-b/data
EOF
sudo ./dplaned-ci -apply -db-dsn "$DATABASE_DSN_B" -gitops-state /tmp/state-node-b.yaml

# Verify isolation
sudo zfs list nodeapool/data >/dev/null && ok "Node A pool exists" || fail "Node A pool missing"
sudo zfs list nodebpool/data >/dev/null && ok "Node B pool exists" || fail "Node B pool missing"

# Convergence check
CONV_A=$(sudo ./dplaned-ci -convergence-check -db-dsn "$DATABASE_DSN_A" -gitops-state /tmp/state-node-a.yaml 2>&1)
echo "$CONV_A" | grep -q "CONVERGED" && ok "Node A converged" || fail "Node A convergence failed"

CONV_B=$(sudo ./dplaned-ci -convergence-check -db-dsn "$DATABASE_DSN_B" -gitops-state /tmp/state-node-b.yaml 2>&1)
echo "$CONV_B" | grep -q "CONVERGED" && ok "Node B converged" || fail "Node B convergence failed"

echo "--- Fleet Simulation PASSED ---"
