#!/bin/bash
set -e

# Fresh Install Test
sudo truncate -s 512M /tmp/inst0.img /tmp/inst1.img
L0=$(sudo losetup --find --show /tmp/inst0.img); L1=$(sudo losetup --find --show /tmp/inst1.img)
sudo zpool create -f installpool mirror "$L0" "$L1"

mkdir -p build/linux-amd64
cp build/dplaned-linux-amd64 build/linux-amd64/dplaned

sudo bash install.sh --unattended --port 9300
systemctl is-active dplaned || (journalctl -xe -u dplaned --no-pager | tail -50; exit 1)

# Fleet Simulation
sudo truncate -s 512M /tmp/a0.img /tmp/a1.img
LA0=$(sudo losetup --find --show /tmp/a0.img); LA1=$(sudo losetup --find --show /tmp/a1.img)
sudo zpool create -f nodeapool mirror "$LA0" "$LA1"
GUID_A=$(sudo zpool list -H -o guid nodeapool)

sudo mkdir -p /var/lib/dplaneos-node-a
sudo bash install/scripts/init-database-with-lock.sh --db /var/lib/dplaneos-node-a/dplaneos.db 2>/dev/null \
  || sudo DPLANEOS_DB=/var/lib/dplaneos-node-a/dplaneos.db bash install/scripts/init-database-with-lock.sh

# Apply state Node A
cat > /tmp/state-node-a.yaml <<EOF
version: "1"
pools: [{name: nodeapool, guid: "$GUID_A", disks: ["$LA0", "$LA1"]}]
datasets: [{name: nodeapool/data, mountpoint: /mnt/node-a/data}]
shares: [{name: node-a-data, path: /mnt/node-a/data}]
EOF

chmod +x build/dplaned-linux-amd64
sudo ./build/dplaned-linux-amd64 -apply -db /var/lib/dplaneos-node-a/dplaneos.db -gitops-state /tmp/state-node-a.yaml -smb-conf /tmp/smb-node-a.conf

# Verify Convergence
CONV=$(sudo ./build/dplaned-linux-amd64 -convergence-check -db /var/lib/dplaneos-node-a/dplaneos.db -gitops-state /tmp/state-node-a.yaml 2>&1)
echo "Fleet Convergence: $CONV"
echo "$CONV" | grep -q "CONVERGED" || (echo "FAILED: $CONV"; exit 1)
echo "✓ Fleet Simulation Passed"
