# DPlaneOS: When Something Fails

What the daemon does when a part of the system fails, and where to look. For step-by-step fixes see the [Troubleshooting Guide](../docs/admin/TROUBLESHOOTING.md) and the [Recovery Guide](../docs/admin/RECOVERY.md); the internals are in [ARCHITECTURE.md](ARCHITECTURE.md).

---

## Where to look

| What | Where |
|---|---|
| Subsystem health (ZFS, Docker, PostgreSQL, resources, external services, bonds) | `GET /api/health`, `/api/health/subsystem`, `/api/health/live`, `/api/health/ready` |
| Daemon log | `journalctl -u dplaned` |
| Alerts (pools, capacity, S.M.A.R.T., UPS, replication, updates, backups) | System › Alerts and the dashboard; see [ALERTS.md](../docs/admin/ALERTS.md) |
| Changes and who made them | Security › Audit Log, System › Change History |
| Prometheus metrics | `GET /metrics` |

---

## A pool fails or is degraded

- DEGRADED: alert `pool.degraded`, the pool keeps serving.
- FAULTED, UNAVAIL, SUSPENDED or REMOVED: alert `pool.critical`, and Docker is stopped so that containers do not write into the empty mountpoint folders on the system disk (if `systemctl stop docker` fails, the daemon tries again at every check; `docker.stopped` is sent once it is stopped). If the health of the pools cannot be read, the alert says so and Docker keeps running. Bring the pool back (`zpool status`, replace the disk), then start Docker again (`systemctl start docker`).
- A disk that returns (hot-swap) is brought back online, or its exported pool is imported again when the disk was last seen in that pool on this machine.

## The system disk fills up

The daemon logs disk warnings for `/persist` at 90, 95 and 98 % and shows them in `/api/health` (subsystem `resources`); it keeps accepting requests. Free space on `/persist` (old logs, Docker images on the system disk: `docker system prune`). Pools have their own capacity alerts at 80, 90 and 95 %.

## The daemon runs out of memory

systemd stops the daemon at its memory limit (`services.dplaneos.memoryMax`, 1 GB by default; `OOMPolicy=continue`) and starts it again. Running shares, containers and pools are not affected; they do not run inside the daemon.

## A directory server (LDAP / AD) is down

Local accounts log in as always; directory accounts cannot log in until the server answers again. The LDAP circuit breaker (`/api/ldap/circuit-breaker`) stops repeated slow attempts. File sharing for already connected clients keeps working.

## Power loss or a crash during a change

Settings are written atomically and synced, so a file is either the old or the new version. Every change is in the change history (System › Change History) and can be undone per resource. A stack update that was interrupted can be run again; one operation per stack runs at a time.

## An update fails

The system checks itself after the update's reboot and goes back to the previous generation when the check fails (alert `upgrade.rolled_back`). See [OTA-UPDATES.md](../docs/admin/OTA-UPDATES.md).

## A cluster node fails

See [HIGH-AVAILABILITY.md](../docs/admin/HIGH-AVAILABILITY.md) and [HA-FAILURE-MODES.md](../docs/admin/HA-FAILURE-MODES.md): quorum, the third vote, watchdog self-fencing and the takeover of storage groups.

---

## Reporting a problem

Include the output of `dplaned -version`, `journalctl -u dplaned -n 200`, `GET /api/health` and the alerts from the time of the failure, or a support bundle (System › Support).
