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

DPlaneOS is not a one-directional deploy system. When you are ready to commit the current state of a node to Git, the Capture workflow reads the complete live system state at that moment, across every category at once, and generates the full `state.yaml` for you. It does not matter how many changes were made, by how many people, or in what order. One Capture call gets all of it.

The operator reviews the output, adjusts if needed, and commits it to Git. Nothing is committed without review. The discipline is deliberate: Git is the record, and every change to that record is an explicit human decision.

During initial setup, this means you can make every change freely through the UI, as fast as you like, with as many people working concurrently as needed. When the system looks right, capture once, review once, commit once as the baseline.

Drift between what is in Git and what is running on the node is detected automatically every five minutes and surfaced immediately in the UI. Drift cannot hide.

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

A full DPlaneOS HA cluster is three nodes: two full NAS nodes (primary and standby) and a lightweight witness (a Raspberry Pi or minimal VM running etcd only, no NAS workloads). Three independent failure domains are protected:

**Database consistency (Patroni + etcd).** PostgreSQL runs in streaming replication from primary to standby. Patroni manages leader election through the three-member etcd cluster. When the primary fails, etcd detects the missing member, Patroni on the standby wins the election, and the standby is promoted. HAProxy runs on each node listening on `127.0.0.1:5000` and health-checks Patroni continuously (`GET /primary` returns 200 on the primary, 503 on the standby). The daemon always connects to `localhost:5000`, never directly to PostgreSQL. It does not need to know which physical node holds the database at any moment.

**Network access (Keepalived VIP).** A floating IP address moves between nodes under Keepalived control. Clients, whether SMB, NFS, iSCSI, or browser sessions, connect to the VIP and never directly to a node. Keepalived polls the daemon API every two seconds. If the daemon stops responding, the VIP migrates. The transition is sub-second for established connections.

**Data integrity (ZFS pool gating).** ZFS pools are imported and mounted only on the current Patroni primary. The standby's ZFS import units are held by systemd until Patroni promotes the node. This prevents both nodes from ever mounting the same pool simultaneously, which is the most dangerous failure mode in any shared-storage HA system.

**Split-brain fencing (STONITH).** A network partition where both nodes lose sight of each other is the scenario HA systems fail on silently. DPlaneOS requires explicit fencing: either IPMI/Redfish (the surviving node powers off the peer via its BMC before promoting) or SBD (each node must periodically renew a ZFS dataset lease; an expired lease means the peer is fenced). Without a confirmed fencing mechanism, the cluster will not promote. This is correct behavior: assuming the other node is dead, without proof, is not a safe basis for importing a ZFS pool.

Automatic failover RTO: 10 to 30 seconds. The witness node ensures quorum even when one main node is unreachable, so a single main-node failure does not block promotion.

See [High Availability Guide](docs/admin/HIGH-AVAILABILITY.md) for setup, fencing configuration, and day-2 operations.

---

## Atomic Updates, Automatic Rollback

The boot disk has two OS slots managed by systemd-boot: Slot A and Slot B. One is active; the other is standby. OTA updates write the new NixOS closure to the standby slot. The active slot is never modified during an update.

Before rebooting into the new slot, the OTA system writes a pending-revert marker to `/persist/ota/`. If the boot fails or the post-boot health check fails, the marker triggers an automatic revert: the bootloader flips back to the previous slot and the system reboots. No operator intervention is required.

The health check fires 90 seconds after boot and verifies four things in sequence:

1. The daemon API responds at `localhost:9000/api/health`
2. All ZFS pools report ONLINE with no faulted devices (`zpool status`)
3. PostgreSQL accepts connections and executes `SELECT 1`
4. Samba responds to `smbcontrol smbd ping`

All four must pass within three retries. On success the marker is cleared and the update is committed. On any failure the revert sequence runs immediately.

The `/persist` partition is never touched by OTA. PostgreSQL data, container images, Samba state, GitOps repository checkout, SSH host keys: all survive every update unchanged, on a dedicated partition separate from both OS slots.

In a HA cluster, OTA is zero-downtime: update the standby, verify it, initiate a controlled failover so the VIP migrates to the freshly-updated node, then update the node that is now standby. Total client impact is one VIP migration.

See [OTA Updates](docs/admin/OTA-UPDATES.md) for the full procedure, verify-only mode, and manual rollback.

---

## No Cloud Dependency

The daemon listens on `127.0.0.1:9000` only. It is not reachable from the network without a reverse proxy. It makes no outbound connections except to:

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

**Authentication and sessions.** Session tokens are 32-byte random values stored hashed in PostgreSQL. They travel in the `X-Session-ID` request header, not a cookie. Browsers cannot auto-submit headers cross-origin, which makes the architecture inherently CSRF-resistant. CSRF tokens (HMAC-SHA256 double-submit) are layered on top for defence in depth. TOTP two-factor authentication is available per user.

**RBAC.** Four roles (viewer, user, operator, admin) with 34 discrete permissions enforced at the handler level. Role assignments support expiry dates. System roles are immutable in the database.

**The audit chain.** Every state-changing operation appends to an audit log. Each row includes an HMAC-SHA256 hash computed over its content plus the previous row's hash, keyed by a 32-byte key stored separately from the database at `/var/lib/dplaneos/audit.key`. Deleting or modifying any row breaks the chain, detectable via `GET /api/system/audit/verify-chain`. An attacker with database write access alone cannot forge the chain without also possessing the key.

**Rate limiting.** 100 requests per minute per source IP enforced in-process before any handler runs.

See [Threat Model](docs/reference/THREAT-MODEL.md) for the full threat analysis, all 13 threat scenarios, known gaps, and residual risks.

---

## Enterprise Identity: LDAP and Active Directory

DPlaneOS integrates natively with any LDAPv3 directory: Active Directory, FreeIPA, OpenLDAP, Azure AD via LDAP proxy, or any standards-compliant directory service.

Directory users authenticate against the configured bind DN directly. On first successful login, the account is JIT-provisioned in DPlaneOS with the configured default role. Directory groups map to DPlaneOS roles: membership in `cn=nas-admins` can automatically grant the `admin` role; membership in `cn=nas-readers` can grant `viewer`. Role changes in the directory propagate on the next sync cycle (default: 1 hour, configurable).

Local accounts and directory accounts coexist. The `admin` account created at install time is always local and is unaffected by LDAP configuration. If the directory is unreachable, local accounts continue to work. There is no hard dependency on directory availability for system operation.

LDAP bind passwords are stored in PostgreSQL and are redacted from all API responses. The `GET /api/ldap/config` endpoint never returns the bind password in any form.

---

## The Full Stack

Every layer is declarative, version-controlled, and rollback-safe:

| Layer | Technology | What it means |
|-------|------------|---------------|
| **OS** | NixOS | Every node boots from the same cryptographically-derived closure declared in the flake. Byte-identical. Guaranteed. |
| **Apps** | Docker Compose | Any `docker-compose.yml`, from any source. Not an approved catalog. Not a Helm chart waiting on a vendor's update cycle. The entire Docker ecosystem, deployed through the same Git workflow as everything else. |
| **Data** | ZFS + built-in backup | Checksums on every block. Snapshots, replication, cloud sync, and cold-tier offload built in. Data integrity and recovery are not configuration options. |
| **Database** | Patroni + etcd | Enterprise-grade PostgreSQL HA, automatic failover, zero-downtime rolling upgrades, built in. |
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
