# DPlaneOS OTA Updates

DPlaneOS updates are atomic and automatically self-reverting. An update either fully succeeds or the system rolls back to the previous version - there is no partially-upgraded state.

---

## How A/B Slot Updates Work

The boot disk has two OS slots: Slot A and Slot B. At any time, one slot is active (booted) and the other is standby. OTA updates write to the standby slot; the active slot is never touched during an update.

```
Boot disk:
├── ESP (/boot)           systemd-boot, slot A/B boot entries
├── Slot A (ext4)         NixOS system closure (may be active or standby)
├── Slot B (ext4)         NixOS system closure (may be active or standby)
├── swap
└── /persist (ext4)       All durable state - never touched by OTA
```

The update sequence:

```
1. Fetch new NixOS closure (from GitHub + Nix cache)
          │
2. Write closure to inactive slot (Slot B if A is active)
          │
3. Update systemd-boot to boot into the new slot first
          │
4. Write pending-revert marker to /persist/ota/
          │
5. Reboot into new slot
          │
6. 90 seconds after boot: OTA health check fires
          │
     ┌────┴────┐
     │         │
  PASS       FAIL
     │         │
Clear marker  Revert bootloader to previous slot
              │
              Reboot into old slot
              │
              Clear marker
```

If the health check passes, the system is on the new version. If it fails, the system is automatically back on the old version without operator intervention.

---

## What the Health Check Verifies

The post-boot health check (`dplaneos-ota-health`) verifies four things:

1. **Daemon API** - `GET http://localhost:9000/api/health` returns 200 within 10 seconds
2. **ZFS pool health** - `zpool status` reports all pools as ONLINE with no faulted devices
3. **PostgreSQL** - `psql` can connect and execute `SELECT 1`
4. **Samba** - `smbcontrol smbd ping` receives a response from the Samba process

If all four pass, the pending-revert marker is cleared and the update is committed. If any check fails after 3 retries, the revert sequence starts.

The check fires 90 seconds after boot by default. This delay gives all services time to start fully. Adjust in `configuration.nix`:
```nix
services.dplaneos.ota.healthCheckDelay = "120s";   # increase if services are slow to start
```

---

## Triggering an Update

### Via the UI

Settings: System: Updates: Check for Updates. If an update is available, click **Install Update**.

The UI shows:
- Current version
- Available version
- Changelog
- Estimated install time

Click **Install and Reboot** to start the process. The system reboots automatically. The UI reconnects once the daemon is responsive on the new slot.

### Via the API

```
POST /api/ota/check
# Returns: {"available": true, "version": "10.1.0", "current": "10.0.0"}

POST /api/ota/update
# Triggers the update; streams progress via WebSocket
```

### Via the CLI (on the node)

```bash
# Check for available update
dplaneos-ota-update --check

# Run the update
sudo dplaneos-ota-update

# Verify-only: build the closure without installing
sudo dplaneos-ota-update --verify-only

# Force revert to previous slot (manual rollback)
sudo dplaneos-ota-update --revert
```

---

## Update Sources

Updates are distributed via GitHub. The OTA script runs:
```bash
git -C /var/lib/dplaneos/gitops/dplaneos-src pull origin main
nixos-rebuild switch --flake .#dplaneos
```

The Nix build system handles fetching required store paths from the Nix binary cache. The flake's content-addressed store and lockfile pin provide integrity at the package level.

**Airgap environments:** The ISO contains the full system closure for the version it ships with. Installing from the ISO does not require internet access. OTA updates, however, require Nix cache access to fetch new store paths. Airgap OTA requires a local Nix binary cache or pre-fetched closures.

---

## Manual Revert

If the automatic health check fails, the system reverts automatically. If you need to revert manually after a successful update (e.g., discovered a regression after the health check passed):

```bash
# From the running system
sudo dplaneos-ota-update --revert
# This flips the bootloader to the other slot and reboots

# From the bootloader (if the system won't boot at all)
# At the systemd-boot menu, select the previous NixOS generation
# Press 'd' to set it as default, then Enter to boot it
```

The previous slot's closure is always preserved until the next OTA update overwrites it. This means you always have exactly one version of rollback available.

---

## HA Rolling Upgrade

In a HA cluster, update both nodes without downtime by updating one at a time:

```
1. Place node B (standby) in maintenance mode
   POST /api/ha/maintenance {"enable": true, "duration_minutes": 120}

2. Trigger OTA on node B
   # SSH into node B:
   sudo dplaneos-ota-update

3. Node B reboots, health check runs, update commits

4. Verify node B is healthy
   GET /api/ha/status  # from node B

5. Initiate a controlled failover (node B becomes primary)
   POST /api/ha/failover {"dry_run": false}

6. Verify clients are connecting via node B (VIP has moved)

7. Place node A in maintenance mode
   POST /api/ha/maintenance {"enable": true, "duration_minutes": 120}

8. Trigger OTA on node A
   sudo dplaneos-ota-update

9. Node A reboots, becomes standby on new version

10. Release maintenance mode on both nodes
    POST /api/ha/maintenance {"enable": false}
```

Total downtime: zero. The VIP moves during the failover step (step 5) but the transition is sub-second for established connections and ~2 seconds for new connections.

---

## Verify-Only Mode

Run the update process through fetching and building the new closure without rebooting:

```bash
sudo dplaneos-ota-update --verify-only
```

This validates that:
- The new version is fetchable from GitHub
- All required Nix store paths are available in the cache
- The closure builds cleanly
- The new system configuration passes `nixos-rebuild dry-run` checks

Use verify-only before scheduling an update on a production system to confirm the update will succeed.

---

## Update Log

OTA update history is stored at `/persist/ota/update.log`. View via:

```bash
cat /persist/ota/update.log
```

Or via the UI: Settings: System: Update History.

---

## Preventing Unintended Updates

To pin a system to a specific version and prevent automatic updates:

```nix
# In configuration.nix
services.dplaneos.ota.enable = false;
```

When `ota.enable = false`, the health check timer does not run. You can still trigger updates manually via `dplaneos-ota-update` but automatic post-boot revert checking is disabled. Use this only if you fully manage the update lifecycle externally.

---

## Troubleshooting

| Symptom | Cause | Resolution |
|---------|-------|------------|
| System booted into old version unexpectedly | Health check failed; auto-revert triggered | Check `/persist/ota/update.log`; fix the underlying issue; retry update |
| Health check passes but services are broken | Check evaluated services that the health check does not test (e.g., NFS, iSCSI) | Run `sudo dplaneos-ota-update --revert` manually; file a bug |
| Update fails at build step | Nix cache unreachable or store path unavailable | Check internet access; try again; check if the target version was published |
| Boot hangs after update | Kernel incompatibility with hardware | Boot previous generation from systemd-boot menu; report with hardware details |
| `dplaneos-ota-update` not found | OTA module not in system closure | Check `services.dplaneos.ota.enable = true` in configuration.nix |
| `/persist` not mounted at boot | disko or fstab misconfiguration | The persist-health-check service will have failed; check `journalctl -u persist-health-check` |
