# DPlaneOS

## The storage operating system where Git is the control plane.

---

Every serious fleet has the same infrastructure tax. A control plane to run. A management console to license. A proprietary dashboard to secure, back up, and keep online. Another vendor. Another thing that breaks at 3am. Another access control system that does not quite match the rest of your stack.

DPlaneOS eliminates it.

---

## Git Is the Control Plane

In DPlaneOS, the Git repository is the pane of glass into each node. Every node's declared state lives in it. Every change flows through it. Every node reconciles against it.

Want to update a node's configuration? Commit to its state file and apply.

Want to roll back a bad change? Revert the commit and apply.

Want to see the declared state of any node right now? Open the repo.

Want to know what changed on a node at 14:32 on March 3rd, who approved it, and what it looked like before? `git log`.

There is no separate control plane. Git is the control plane.

---

## The Loop Is Closed in Both Directions

DPlaneOS is not a one-directional deploy system. A change made in the web interface becomes a commit (or a pull request) within seconds, listing each resource, who changed it and when. A change pushed to the repository comes back to the node: it waits for approval on the Git page, or is applied at once if you switch automatic apply on. A change on both sides is a conflict you decide, with both versions side by side. Nothing is overwritten silently.

The first time the repository is connected, the node writes its whole configuration to it: the baseline. From then on the repository and the node stay in step in both directions, on every node of a cluster, each scope written by exactly one node.

A change made outside the web interface (a shell, another tool) is found by the configuration history within five minutes and committed like any other change, or, with self-heal on, replaced by the repository's version. Drift cannot hide.

---

## Access Control Is Already Solved

Access control to each node's configuration is access control to its Git repository. Your existing branch protection rules become change approval gates. Required reviewers become mandatory peer review for every infrastructure change. Your current Git security posture is your configuration security posture. Nothing new to design, implement, or audit.

---

## Git Is Your Infrastructure's History

Every change to every node's declared state is a commit. Who made it. When. What it looked like before. What it looks like after. Not because you configured a logging system. Not because you pay for an observability platform. Because that is what Git does, and Git is the control plane.

When something breaks, you know exactly what changed and when. When you need to understand the evolution of a node's configuration over months or years, you read the log. The history of your infrastructure is the same history you already know how to navigate.

---

## Everything Is in Git

This is not a partial claim. Every layer of the stack has a native, integrated mechanism for declaring its state in Git. Not bolted on. Not optional. Not "most things."

**The OS layer:** NixOS. The kernel version, kernel modules, system services, firewall rules, users, network configuration: all declared in the flake. Checked in. In Git. Changing the kernel is a one-line commit. Rolling it back is a revert.

**The state layer:** DPlaneOS configuration. Storage pools, datasets, shares, container definitions, network topology: reconciled from `state.yaml` in Git. The daemon reads it, computes a diff against live state, and applies only what has changed.

**The app layer:** Docker Compose. Every container a node runs, its configuration, its dependencies, its version: in Git. Not in a proprietary catalog. In a plain text file in your repository, the same as every other piece of your infrastructure.

From kernel to container, the entire system has one source of truth. A single Git repository that a new engineer can clone, read, and understand completely. A repository that, on its own, is sufficient to reproduce the node from scratch.

**The one layer Git does not touch is the data layer, and that is intentional.** Raw data does not belong in Git. It belongs in ZFS, which provides the equivalent guarantee at the data level: checksums on every block, point-in-time snapshots, and native encryption at rest. For backup and replication, DPlaneOS ships every option built in:

- **ZFS snapshots:** automatic, tiered schedules (every 15 minutes, hourly, daily, weekly, monthly), retained and expired without manual intervention
- **ZFS Send/Receive:** efficient incremental replication to any other ZFS system, local or remote, over SSH
- **Cloud sync:** rclone-backed sync to any S3-compatible store, Backblaze B2, Google Drive, or any of the 40+ providers rclone supports
- **Cold-tier offload:** move aged data to lower-cost storage automatically, keeping hot data local
- **rsync:** for replication to non-ZFS targets when needed

Git versions your infrastructure. ZFS versions your data. Each tool doing exactly what it was built for.

---

## High Availability

Every node keeps its own database and keeps working on its own if it is cut off; paired nodes exchange configuration, including passwords and other secrets, encrypted for the pair. Nodes form a Corosync cluster (the engine Proxmox VE uses), and pools live in **storage groups** that move between nodes with their shares, exports, apps and a floating address.

**Shared storage** (SAS or SATA shelf cabled to both nodes, SAN): the next node imports the same pools, no data loss. **Replicated** (any disks, two boxes, two rooms or sites): the owner streams ZFS snapshots to the others, at most one replication interval is lost.

**The third vote from anything.** Two nodes run fine on their own, with manual takeover. One small extra vote turns on automatic failover: a Raspberry Pi or VM as QDevice (one pasted command), a Pi or mini PC as a full corosync voter, another DPlaneOS system, or a third node.

**Protection without mandatory hardware.** A storage owner that loses quorum resets itself through the watchdog before anyone takes over; ZFS multihost refuses double imports; SCSI-3 reservations and IPMI/PDU power fencing are added when the hardware has them. The protection card says what each missing layer would add instead of refusing to run.

Automatic failover: the watchdog timeout plus 15 seconds, then the import (about 30 seconds for shared pools).

See [High Availability Guide](docs/admin/HIGH-AVAILABILITY.md) for setup, fencing configuration, and day-2 operations.

---

## Atomic Updates, Automatic Rollback

An update builds the new DPlaneOS release as a new NixOS generation (a new boot entry) while the running system is untouched, then reboots into it. The previous generation stays on disk and in the boot menu.

After the boot, a check runs for up to five minutes: the daemon answers, nginx and PostgreSQL run, every pool imported before the update is imported again, and Samba runs if shares are enabled. If anything fails, the system makes the previous generation the boot default again, restores its flake and reboots into it. No operator intervention is required, and an alert reports the result either way.

The lab proof is a NixOS VM test (`checks.update`) that boots through systemd-boot, applies a working update (kept) and a broken one (rolled back by itself).

The `/persist` partition is never touched by an update. PostgreSQL data, container images, Samba state, the Git clone, SSH host keys: all survive every update unchanged, on a dedicated partition separate from the system.

In a cluster, nodes are updated one at a time: maintenance mode moves a node's storage groups to another node, the node is updated and checked, then the next one. Clients see each group's address move once.

See [OTA Updates](docs/admin/OTA-UPDATES.md) for the full procedure and manual rollback.

---

## No Cloud Dependency

The daemon communicates via a Unix socket only. It is not reachable from the network at all without a reverse proxy. It makes no outbound connections except to:

- The Git repository you configure, which can be self-hosted on Gitea, Forgejo, GitLab, or any bare Git server
- rclone targets you explicitly configure, which are opt-in and operator-controlled
- Nix binary caches during OTA updates, which can be self-hosted for air-gap environments

There is no telemetry. No license server. No activation. No subscription required to operate. The system continues to run on the installed version indefinitely without any external contact.

The ISO contains the complete NixOS closure for its shipped version. Installation from ISO requires no internet connection at any point.

The GitOps deploy key is generated by the operator, stored on the node, and under the operator's full control. Rotate or revoke it at any time without contacting a vendor.

---

## Security Architecture

The daemon runs as root but constrains itself at the systemd unit level: `ProtectSystem=strict`, `NoNewPrivileges=true`, and a 1 GB `MemoryMax`. It cannot modify the NixOS system closure beneath it.

**The exec allowlist.** Every call to a system tool passes through `internal/security/whitelist.go` before execution. The allowlist validates the binary name, the subcommand, and each argument individually against strict predefined patterns. Arguments reach `exec.Command` as a string slice, never as a shell string. There is no `bash -c`, no shell expansion, no string interpolation. A request that passes all API-level validation but attempts a command not in the allowlist is rejected before any process is spawned.

Input is validated before it reaches the allowlist at all. Pool names, dataset names, device paths, and share names each pass through dedicated validators (`ValidatePoolName`, `ValidateDatasetName`, `ValidateDevicePath`) that reject shell metacharacters with HTTP 400 before any command is constructed. Device paths must be stable `/dev/disk/by-id/` paths; short `/dev/sdX` paths are rejected at `state.yaml` parse time, long before execution.

**Authentication and sessions.** Session tokens are 32-byte random values; PostgreSQL stores only their SHA-256. They travel in the `X-Session-ID` request header, not a cookie. Browsers cannot auto-submit headers cross-origin, which makes the architecture inherently CSRF-resistant. CSRF tokens (HMAC-SHA256 double-submit) are layered on top for defence in depth. TOTP two-factor authentication is available per user.

**RBAC.** Four roles (viewer, user, operator, admin) with 31 discrete permissions enforced per route. Role assignments support expiry dates. System roles are immutable in the database.

**The audit chain.** Every state-changing operation appends to an audit log. Each row includes an HMAC-SHA256 hash computed over its content plus the previous row's hash, keyed by a 32-byte key stored separately from the database at `/var/lib/dplaneos/audit.key`. Deleting or modifying any row breaks the chain, detectable via `GET /api/system/audit/verify-chain`. An attacker with database write access alone cannot forge the chain without also possessing the key.

**Rate limiting.** 100 requests per minute per source IP enforced in-process before any handler runs.

See [Threat Model](docs/reference/THREAT-MODEL.md) for the full threat analysis, all 13 threat scenarios, known gaps, and residual risks.

---

## Enterprise Identity: LDAP and Active Directory

DPlaneOS integrates natively with any LDAPv3 directory: Active Directory, FreeIPA, OpenLDAP, Azure AD via LDAP proxy, or any standards-compliant directory service.

Directory users authenticate against the configured bind DN directly. On first successful login, the account is JIT-provisioned in DPlaneOS with the configured default role. Directory groups map to DPlaneOS roles: membership in `cn=nas-admins` can automatically grant the `admin` role; membership in `cn=nas-readers` can grant `viewer`. Role changes in the directory propagate on the next sync cycle (default: 1 hour, configurable).

Local accounts and directory accounts coexist. The `admin` account created at install time is always local and is unaffected by LDAP configuration. If the directory is unreachable, local accounts continue to work. There is no hard dependency on directory availability for system operation.

LDAP bind passwords are stored sealed in PostgreSQL and are redacted from all API responses. The `GET /api/ldap/config` endpoint never returns the bind password in any form.

---

## The Full Stack

Every layer is declarative, version-controlled, and rollback-safe:

| Layer | Technology | What it means |
|-------|------------|---------------|
| **OS** | NixOS | Every node boots from the same cryptographically-derived closure declared in the flake. Byte-identical. Guaranteed. |
| **Apps** | Docker Compose | Any `docker-compose.yml`, from any source. Not an approved catalog. Not a Helm chart waiting on a vendor's update cycle. The entire Docker ecosystem, deployed through the same Git workflow as everything else. |
| **Data** | ZFS + built-in backup | Checksums on every block. Snapshots, replication, cloud sync, and cold-tier offload built in. Data integrity and recovery are not configuration options. |
| **Database** | PostgreSQL per node | Each node keeps working on its own; configuration merges per resource with conflict review. |
| **Identity** | LDAP / Active Directory | Native LDAPv3 integration with JIT provisioning, group-to-role mapping, and local account fallback. |
| **Architecture** | x86_64 + ARM64 | Graviton and Ampere nodes supported from day one. |

---

## What You Get Rid Of

- Control plane infrastructure to run and scale
- Proprietary fleet management consoles
- App store catalogs and their version lag
- Drift that hides until it causes an incident
- Separate access control systems for infrastructure configuration
- Gaps between what you intended and what actually happened
- Cloud dependencies and vendor lock-in
- Partially-upgraded systems after a failed update

---

## What Replaces All of It

A Git repository. Which has been battle-tested for 20 years. Which runs everywhere. Which costs nothing. Which every engineer on the planet already knows.

Your storage infrastructure, managed with the same tools and discipline as your application layer. Finally.

---

**Open source. AGPLv3. Production today.**

[Get started](docs/admin/INSTALLATION-GUIDE.md) | [Architecture](docs/reference/ARCHITECTURE.md) | [GitOps Reference](docs/reference/GITOPS-REFERENCE.md) | [Design Philosophy](docs/reference/PHILOSOPHY.md) | [Threat Model](docs/reference/THREAT-MODEL.md) | [High Availability](docs/admin/HIGH-AVAILABILITY.md)
