# DPlaneOS Daemon: Startup, Health and Resilience

This document describes what `dplaned` starts besides its HTTP API, and how it behaves when something is missing or failing. The overall system (nginx, PostgreSQL, ZFS, the NixOS appliance) is described in [docs/reference/ARCHITECTURE.md](../docs/reference/ARCHITECTURE.md).

---

## Startup

`daemon/internal/bootstrap/phases.go` initialises these parts in order, after the database is migrated:

| Part | Package | What it does |
|---|---|---|
| Resource watcher | `internal/resource` | Every 5 seconds: disk use of `/persist`, the daemon's memory, open file descriptors |
| Hardware profile | `internal/hardware` | Detected once at startup and logged |
| Feature flags | `internal/features` | Loaded from the `feature_flags` table; listed and switched under Settings › Features |
| Circuit breakers | `internal/resilience` | A pool of breakers for external services |
| Operation journal | `internal/gitops` (`StateMachine`) | The `operation_journal` table; unfinished operations are looked at during startup |
| Health checker | `internal/monitoring` | Periodic subsystem checks, served at `/api/health` |
| Event log | `internal/audit` (`EventLogger`) | The HMAC-chained `audit_events` table |
| Rollback manager | `internal/rollback` | Startup recovery of the operation journal |

They monitor and log. If the bootstrap fails, the daemon logs `hardening phases not started` and starts without them: the API keeps working, the `/api/health` routes are not served and Settings › Features stays empty.

---

## Resource watcher

| Resource | Warning | Degraded | Critical |
|---|---|---|---|
| Disk (`/persist`) | 90 % | 95 % | 98 % |
| Daemon memory | | 85 % | 95 % |
| File descriptors | | | 90 % |

Crossing a level is logged, a critical level is also written to the event log (`resource_critical`), and the status appears in `/api/health` (subsystem `resources`). The watcher does not refuse requests. The daemon's hard memory limit is systemd's `MemoryMax` (`services.dplaneos.memoryMax`, 1 GB by default); pool capacity has its own alerts at 80, 90 and 95 % (see [ALERTS.md](../docs/admin/ALERTS.md)).

---

## Health checks

`/api/health` (overall), `/api/health/subsystem`, `/api/health/live` and `/api/health/ready` report these checks:

| Subsystem | Check |
|---|---|
| `resources` | the resource watcher's status |
| `external_services` | the circuit breakers (open breakers degrade the status) |
| `bonding` | bond members up (when bonds exist) |
| `zfs` | `zpool status -x` names no pool with problems |
| `docker` | the Docker daemon answers (skipped when Docker is not installed) |
| `postgres` | the database answers |

`/health` and `/api/system/health` (no login) only say that the daemon runs; the update check after a reboot calls `/api/system/health` on the daemon socket.

---

## Failure handling where it matters

The protections that keep data and configuration safe are not in this framework but where the work is done:

- **Directory outages:** the LDAP client has its own circuit breaker (`/api/ldap/circuit-breaker`); local accounts always log in.
- **Power loss:** state files are written atomically and synced (`internal/fsutil.WriteFileDurable`); configuration changes are recorded in the change history (System › Change History) and can be undone per resource.
- **Stacks:** one operation per stack (`internal/stacklock`); a stack update rolls back to the previous images when a service does not come up.
- **Updates:** a new NixOS generation, checked after the reboot and rolled back when the check fails (`nixos/ota-module.nix`).
- **Pools at boot:** a pool that is not ONLINE keeps the daemon waiting (`dplaneos-zfs-gate`, up to 120 s); a pool that fails later stops Docker, so containers never write into empty mountpoints.
- **Clusters:** quorum, epochs, watchdog self-fencing, ZFS multihost, SCSI-3 PR and power fencing (see [HIGH-AVAILABILITY.md](../docs/admin/HIGH-AVAILABILITY.md)).

---

## Not active

- The operation journal is in place, but no operation writes to it yet, so the startup recovery finds nothing to resume.
- Feature flags are stored and shown, but no API route or job checks them: the features they name work whether the flag is on or off.
- `handlers.ResourceGuardMiddleware` (refuse writes when disk or memory is critical) exists but is not applied to the router.

---

## Tests

Each package has unit tests (`go test ./internal/resource ./internal/monitoring ./internal/rollback ./internal/resilience ./internal/audit`); the NixOS VM tests (`nix build .#checks.x86_64-linux.<name>`) cover the behaviour above on a running system: power loss (`hardening-*`), updates (`update`), pool import at boot and the installer (`installer`), failover (`failover`, `groups`, `replicated`).
