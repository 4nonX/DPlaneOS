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

DPlaneOS is not a one-directional deploy system. When you make a change through the web UI on a node, the Capture workflow generates the corresponding `state.yaml` update for you to review and commit. The operator reviews the output, adjusts if needed, and commits it to Git. Nothing is committed without review.

This means the Git repository can always reflect actual system state, not just intended state. The discipline is deliberate: Git is the record, and every change to that record is an explicit human decision.

Drift between what is in Git and what is running on the node is detected automatically, every five minutes, and surfaced immediately in the UI. Drift cannot hide.

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

## The Full Stack

Every layer is declarative, version-controlled, and rollback-safe:

| Layer | Technology | What it means |
|-------|------------|---------------|
| **OS** | NixOS | Every node boots from the same cryptographically-derived closure declared in the flake. Byte-identical. Guaranteed. |
| **Apps** | Docker Compose | Any `docker-compose.yml`, from any source. Not an approved catalog. Not a Helm chart waiting on a vendor's update cycle. The entire Docker ecosystem, deployed through the same Git workflow as everything else. |
| **Data** | ZFS + built-in backup | Checksums on every block. Snapshots, replication, cloud sync, and cold-tier offload built in. Data integrity and recovery are not configuration options. |
| **Database** | Patroni + etcd | Enterprise-grade PostgreSQL HA, automatic failover, built in. |
| **Architecture** | x86_64 + ARM64 | Graviton and Ampere nodes supported from day one. |

---

## What You Get Rid Of

- Proprietary fleet management consoles
- App store catalogs and their version lag
- Drift that hides until it causes an incident
- Separate access control systems for infrastructure configuration
- Gaps between what you intended and what actually happened

---

## What Replaces All of It

A Git repository. Which has been battle-tested for 20 years. Which runs everywhere. Which costs nothing. Which every engineer on the planet already knows.

Your storage infrastructure, managed with the same tools and discipline as your application layer. Finally.

---

**Open source. AGPLv3. Production today.**

[Get started](docs/admin/INSTALLATION-GUIDE.md) | [Architecture](docs/reference/ARCHITECTURE.md) | [GitOps Reference](docs/reference/GITOPS-REFERENCE.md) | [Design Philosophy](docs/reference/PHILOSOPHY.md)
