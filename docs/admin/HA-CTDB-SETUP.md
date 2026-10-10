# DPlaneOS CTDB Setup Guide

## Overview

CTDB (Clustered TDB) enables Samba to maintain state across HA failover. Without CTDB, SMB clients disconnect when the primary fails and must reconnect to the secondary. With CTDB, clients remain connected through failover - locks and sessions survive.

**Status:** CTDB support is EXPERIMENTAL in DPlaneOS v14.6.0. Test thoroughly in staging before enabling on production clusters.

---

## When to Use CTDB

**Enable CTDB if:**
- You run SMB/CIFS shares on an HA cluster
- File-locking applications (databases, ERP systems) use SMB shares
- You need zero-downtime failover for file access

**You can skip CTDB if:**
- Your users can tolerate brief SMB reconnects on failover (30-60 seconds)
- You run only NFS shares (NFS has built-in stateless failover)
- You run single-node deployments

---

## Prerequisites

1. **HA must be fully configured** (both nodes healthy, quorum established, witness reachable)
   ```bash
   curl -s http://NODE_IP:5000/api/ha/status | jq '.quorum'
   # Must show: true
   ```

2. **Samba must be enabled**
   ```bash
   systemctl status smbd
   # Must show: active (running)
   ```

3. **Shared storage must be mounted** (Path A) or replicated (Path B)
   ```bash
   zpool list
   # Both nodes must see the same pool
   ```

4. **Sufficient disk space** on the shared/replicated pool for CTDB database
   ```bash
   zfs create tank/ctdb  # At least 1GB recommended
   ```

5. **Network connectivity** between nodes on CTDB port 4379
   ```bash
   nc -zv PEER_IP 5000
   # Must succeed
   ```

---

## Installation Steps

### Step 1: Create CTDB ZFS Dataset

On the primary node, create a ZFS dataset for CTDB's database:

```bash
# Path A (shared storage) or Path B (replicated pool)
zfs create -o mountpoint=/var/lib/ctdb tank/ctdb
chmod 700 /var/lib/ctdb
mkdir -p /var/lib/ctdb/persistent
```

For Path B (replicated), this dataset will be automatically replicated to the secondary node on next sync cycle.

Verify on both nodes:
```bash
zfs list tank/ctdb
ls -la /var/lib/ctdb
# Should show: persistent/ directory
```

### Step 2: Enable CTDB in NixOS Configuration

Edit your flake.nix or configuration.nix:

```nix
services.dplaneos = {
  enable = true;
  samba.enable = true;
  # list every node, identical order on all nodes
  ctdb.nodes = [ "10.0.0.1" "10.0.0.2" ];

  ctdb = {
    enable = true;
    dataPool = "tank";
    dataDataset = "tank/ctdb";
    
    # List of public VIPs that clients connect to (managed by CTDB)
    publicAddresses = [
      "192.168.1.100/24 eth0"  # Client-facing IP managed by CTDB
    ];
    
    # Timing: how long before declaring node dead
    nodeTimeout = 30;      # seconds
    recoveryTimeout = 120; # seconds
    
    logLevel = 2;  # 0=ERROR, 1=WARN, 2=NOTICE, 3=INFO, 4=DEBUG
  };
};
```

**On both nodes**, apply the configuration:
```bash
sudo nixos-rebuild switch
```

### Step 3: Verify CTDB is Running

```bash
# Check CTDB daemon
systemctl status ctdb
# Should show: active (running)

# Check CTDB cluster status
ctdb -x "GET PING" localhost
# Should show responses from both nodes

# Watch cluster nodes
ctdb nodestatus
# Example output:
#   pnn:0 192.168.1.1       OK (THIS NODE)
#   pnn:1 192.168.1.2       OK
```

### Step 4: Check Samba is Using CTDB

```bash
# Samba should be connected to CTDB socket
lsof /var/run/ctdb/ctdbd.socket
# Should show: smbd processes connected

# Verify Samba config
smbclient -L localhost -U%
# Should succeed without Kerberos errors
```

### Step 5: Test SMB Client Connection

From a client machine:

```bash
# macOS
mount_smbfs //username@192.168.1.100/sharename ~/mnt

# Linux
mount -t cifs //192.168.1.100/sharename /mnt -o username=user,password=pass

# Windows
net use * \\192.168.1.100\sharename

# Verify connection
smbclient //192.168.1.100/sharename -U username
```

---

## Testing Failover with CTDB

### Test 1: Verify Byte-Range Locks Survive

```bash
# On client, create a file and lock a byte range
python3 << 'EOF'
import fcntl
f = open('/mnt/sharename/testfile.txt', 'w+')
fcntl.flock(f, fcntl.LOCK_EX | fcntl.LOCK_NB)
print("Lock acquired. Press Enter to release.")
input()
fcntl.flock(f, fcntl.LOCK_UN)
EOF
```

While the lock is held, trigger failover on primary node:
```bash
# On primary
systemctl stop dplaned  # Simulate crash

# Monitor client connection
# Should NOT disconnect during transition
# Lock should still be held after secondary promotes
```

### Test 2: Verify Client Session Survives

```bash
# On client, open SMB connection and keep it open
smbclient //192.168.1.100/sharename -U username

# Inside smbclient prompt, list files
smb: \> ls

# Keep connection open. On primary, trigger failover.
# Failover should complete, and client should reconnect automatically.
# ls command should succeed after 30-60 seconds.
```

### Test 3: Measure Failover Impact

```bash
# Run continuous file operations on share
while true; do
  timestamp=$(date '+%s%N')
  touch /mnt/sharename/$timestamp
  sleep 0.1
done

# Monitor for gaps (failover duration)
# Without CTDB: ~30-60 second gap (client reconnect time)
# With CTDB: <5 second gap (IP migration + Samba restart)
```

---

## Operational Procedures

### Starting CTDB Manually

```bash
systemctl start ctdb
# Wait for ctdb nodestatus to show all nodes "OK"
ctdb nodestatus
```

### Stopping CTDB Safely

```bash
# On secondary, safe to stop immediately
systemctl stop ctdb

# On primary, migrate IPs first
ctdb disable
ctdb recover
sleep 10
systemctl stop ctdb
```

### Monitoring CTDB Health

```bash
# Real-time cluster status
watch -n 1 'ctdb nodestatus'

# Database status
ctdb dbstatus

# Show IP ownership
ctdb ip

# Show process list
ctdb process

# Show recent logs
journalctl -u ctdb -n 100 --no-pager
```

### Troubleshooting: Node Won't Join Cluster

```bash
# Check CTDB logs
journalctl -u ctdb -n 50 --no-pager

# Verify node list is identical on both nodes
diff <(ssh node-a cat /etc/ctdb/nodes) <(ssh node-b cat /etc/ctdb/nodes)

# If nodes list differs, restart CTDB on both
systemctl restart ctdb

# Force node recovery (if one node is stuck)
ctdb recover -n <node_number>
```

### Troubleshooting: IP Migration Slow

CTDB should migrate IPs within 2-3 seconds. If slower:

```bash
# A storage group's floating address must not be the same as a CTDB public address
ip -o addr

# Reduce CTDB timeouts
# In NixOS config:
services.dplaneos.ctdb.nodeTimeout = 15;  # More aggressive

# Restart CTDB
systemctl restart ctdb
```

---

## Failure Scenarios with CTDB

### CTDB Daemon Crashes

**Symptoms:**
- SMB connections suddenly drop
- Clients get "connection reset" errors
- Multiple reconnects within seconds

**Recovery:**
```bash
# Restart CTDB
systemctl restart ctdb

# Monitor recovery
watch 'ctdb nodestatus'

# Samba should reconnect automatically
```

### CTDB Database Corruption

**Symptoms:**
- Repeated errors in ctdb logs: "Error reading database"
- CTDB won't start, or node stays "DISCONNECTED"

**Recovery (Data Loss Risk):**
```bash
# Backup current database
cp -r /var/lib/ctdb /var/lib/ctdb.backup.$(date +%s)

# Rebuild database from peer
ctdb stop
rm -rf /var/lib/ctdb/*
ctdb recover

# If node still won't join, remove from cluster temporarily
# and let replication/failover rebuild it
```

### One Node's CTDB Won't Communicate

**Symptoms:**
- `ctdb nodestatus` shows one node as DISCONNECTED
- Cluster is still operational (other node can take over)

**Recovery:**
```bash
# On healthy node
ctdb disable -n <bad_node>

# Restart CTDB on bad node
ssh bad_node systemctl restart ctdb

# Monitor recovery
ctdb nodestatus

# Re-enable when healthy
ctdb enable -n <bad_node>
```

---

## Performance Considerations

### Overhead

CTDB adds minimal overhead:
- Disk I/O: ~5-10% additional (lock database updates)
- Network I/O: ~100-200 byte/sec per client (heartbeat traffic)
- CPU: <1% (lock coordination)

### Tuning

For high-concurrency workloads (>100 SMB clients):

```nix
services.dplaneos.ctdb = {
  # Reduce recovery time after failures
  recoveryTimeout = 60;      # Default 120
  
  # More aggressive node failure detection
  nodeTimeout = 15;          # Default 30
  
  # Increase log level to see what's happening
  logLevel = 3;              # INFO level
};
```

---

## Disabling CTDB

If CTDB causes issues, disable it:

1. Update NixOS config:
```nix
services.dplaneos.ctdb.enable = false;  # or remove the section
```

2. Apply:
```bash
sudo nixos-rebuild switch
```

3. Clean up:
```bash
systemctl stop ctdb
rm -rf /var/lib/ctdb
zfs destroy tank/ctdb
```

Samba will revert to local TDB locks; failover will work but clients will disconnect briefly.

---

## References

- TrueNAS SCALE CTDB documentation
- Samba CTDB clustering guide: https://wiki.samba.org/index.php/CTDB
- See also: HA-FAILURE-MODES.md (recovery procedures if CTDB fails)
