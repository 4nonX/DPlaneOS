# DPlaneOS

## The storage operating system where Git is the control plane.

---

Every serious fleet has the same infrastructure tax. A control plane to run. A management console to license. A proprietary dashboard to secure, back up, and keep online. Another vendor. Another thing that breaks at 3am. Another access control system that does not quite match the rest of your stack.

DPlaneOS eliminates it.

---

## Git Is the Control Plane

In DPlaneOS, the Git repository is the pane of glass into the entire fleet. Every node's state is in it. Every change flows through it. Every node reconciles against it, continuously and automatically.

Want to update a configuration across 10,000 storage nodes? Commit.

Want to roll back a bad change fleet-wide? Revert.

Want to see the exact state of every node in your fleet right now? Open the repo.

Want to know what changed on node 4,847 at 14:32 on March 3rd, who approved it, and what it looked like before? `git log`.

There is no separate control plane. Git is the control plane.

---

## The Loop Is Closed in Both Directions

This is not a one-directional deploy system. Changes made through the web UI on any node are captured and pushed back to the repo automatically. The repository does not record intended state. It mirrors actual state, from both directions, continuously.

Drift is structurally impossible. The repo and the fleet are the same thing.

---

## Access Control Is Already Solved

Access control to the fleet is access control to the repo. Your existing branch protection rules become change approval gates. Required reviewers become mandatory peer review for every infrastructure change. Your current Git security posture is your fleet security posture. Nothing new to design, implement, or audit.

---

## Git Is Your Fleet's History

Every change to every node is a commit. Who made it. When. What it looked like before. What it looks like after. Not because you configured a logging system. Not because you pay for an observability platform. Because that is what Git does, and Git is the control plane.

When something breaks, you know exactly what changed, on which node, and when. When you need to understand the evolution of your fleet over months or years, you read the log. The history of your infrastructure is the same history you already know how to navigate.

---

## The Full Stack

Every layer is declarative, version-controlled, and rollback-safe:

| Layer | Technology | What it means |
|-------|------------|---------------|
| **OS** | NixOS | Every node is cryptographically identical to every other node in its class. Not "we tried to keep them consistent." Byte-identical. Guaranteed. The system boots from the same derivation every time, on every node. |
| **Apps** | Docker Compose | Any `docker-compose.yml`, from any source. Not an approved catalog. Not a Helm chart waiting on a vendor's update cycle. The entire Docker ecosystem, deployed through the same Git workflow as everything else, immediately, without gatekeeping. |
| **Data** | ZFS | Checksums on every block. Snapshots at every layer. Compression and encryption by default. Data integrity is not a configuration option. |
| **Database** | Patroni + etcd | Enterprise-grade PostgreSQL HA, automatic failover, built in. |
| **Architecture** | x86_64 + ARM64 | Graviton and Ampere fleets supported from day one. |

---

## What You Get Rid Of

- Control plane infrastructure to run and scale
- Proprietary fleet management consoles
- App store catalogs and their version lag
- Drift between declared and actual state
- Separate access control systems for fleet management
- Gaps between what you intended and what actually happened

---

## What Replaces All of It

A Git repository. Which has been battle-tested for 20 years. Which runs everywhere. Which costs nothing. Which every engineer on the planet already knows.

Your storage fleet, managed with the same tools and discipline as your application layer. Finally.

---

**Open source. AGPLv3. Production today.**

[Get started](docs/admin/INSTALLATION-GUIDE.md) - [Architecture](docs/reference/ARCHITECTURE.md) - [GitOps Reference](docs/reference/GITOPS-REFERENCE.md) - [Design Philosophy](docs/reference/PHILOSOPHY.md)
