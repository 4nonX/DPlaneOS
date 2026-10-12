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

Drift between what is in Git and what is running on the node is detected automatically and shown in the UI. A node can also apply new commits by itself (off by default): only changes that add or modify, never ones that destroy data or touch a pool, and each commit is tried once.

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

**The OS layer:** NixOS. The kernel, ZFS, every service and their configuration are built from the flake in the project's Git at a tagged commit into one image; a node runs exactly that image, and an update replaces it as a whole. What differs between nodes (host name, network, firewall, time) is runtime state, and part of `state.yaml`.

**The state layer:** DPlaneOS configuration. Pools, datasets and their properties, shares, users and groups, stacks, replication, NVMe-oF exports, system and network settings: reconciled from `state.yaml` in Git. The daemon reads it, computes a diff against the live state, and applies only what has changed; what would destroy data waits for an approval.

**The app layer:** Docker Compose. Every container a node runs, its configuration, its dependencies, its version: in Git. Not in a proprietary catalog. In a plain text file in your repository, the same as every other piece of your infrastructure.

From kernel to container, the state of a node is text in Git: the release it runs, and its `state.yaml`. Together they are enough to set the node up again on new hardware; the data comes back with the pools or from replication.

**The one layer Git does not touch is the data layer, and that is intentional.** Raw data does not belong in Git. It belongs in ZFS, which provides the equivalent guarantee at the data level: checksums on every block, point-in-time snapshots, and native encryption at rest. For backup and replication, DPlaneOS ships every option built in:

- **ZFS snapshots:** scheduled per dataset (hourly, daily, weekly, monthly), kept by number or by age and expired without manual intervention
- **ZFS Send/Receive:** efficient incremental replication to any other ZFS system, local or remote, over SSH
- **Cloud sync:** rclone-backed sync to any S3-compatible store, Backblaze B2, Google Drive, or any of the 40+ providers rclone supports
- **Remote storage as a folder:** an rclone remote mounted into the file tree, for data that may live elsewhere
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

## Updates That Take Themselves Back

A node is an image. An update is a signed file with the next image: the node verifies the signature against the release key it was installed with, installs the new version next to the running one, and starts it once on trial. Before the update it noted what worked: the services, the pools, the persistent state. If all of that works again after the trial start, the new version becomes the default. If not, or if the new version does not start at all, the node is back on the previous version after the next start, without anyone at the console.

Data and settings are not part of the image: both versions use the same pools and the same persistent partition.

Update files can be carried to nodes without internet access. In a cluster, move the storage groups away, update, move them back.

See [System Updates](docs/admin/OTA-UPDATES.md).

---

## No Cloud Dependency

A node makes outbound connections only for what you set up: the Git repositories you configure, the cloud targets you configure, your mail server or webhook for alerts, your directory or identity provider, an ACME certificate authority if you ask for certificates, and the project's release page when you look for updates (update files can be uploaded instead).

There is no telemetry. No license server. No activation. No subscription required to operate. The web interface loads nothing from other hosts. The system continues to run on the installed version indefinitely without any external contact.

The ISO contains the complete system. Installation requires no internet connection at any point.

---

## Security Architecture

**One process, no shell.** The daemon manages the system by running programs, and every program it may run, with its sub-commands and the shape of each argument, is on an allowlist (`internal/security/whitelist.go`). Arguments are passed as a list, never as a line for a shell. Names from requests are validated before they reach a command. The daemon itself runs as root, as a NAS manager has to; what limits it is inside it.

**Authentication and sessions.** Session tokens are 32-byte random values that travel in a request header, not a cookie, so a browser does not attach them to requests another site triggers; changing requests carry a CSRF token as well. TOTP two-factor sign-in; the operations with the largest blast radius (destroying a pool, fencing) require it. Failed sign-ins are throttled per client address.

**RBAC.** Four built-in roles (viewer, user, operator, admin) and custom roles over 31 permissions, checked on every route. Role assignments support expiry dates.

**The audit chain.** Changes are appended to an audit log in which each entry carries an HMAC over its content and the entry before it, with a key kept outside the database. Removing or changing an entry breaks the chain, and the check says where.

**Signed all the way.** Release images are built by CI from the tagged source, signed, and shipped with an SBOM. A node installs only updates signed with the release key in its own image.

See [Threat Model](docs/reference/THREAT-MODEL.md) for what is defended, and what is not.

---

## Identity: LDAP, Active Directory, OIDC

**LDAP sign-in.** Directory users sign in to the web interface with their directory password; the account is created at the first sign-in, and directory groups map to roles. Works with OpenLDAP, FreeIPA, Active Directory over LDAP.

**Active Directory membership.** The NAS joins the domain: domain users and groups open SMB shares with their domain credentials, and appear in permissions and ACLs.

**OIDC single sign-on.** Authorization Code with PKCE against any standard provider; roles follow the groups in the token.

Local accounts and directory accounts coexist. The `admin` account created at install time is always local: if the directory is unreachable, it still signs in.

Bind passwords and client secrets are stored encrypted and are never returned by the API.

---

## The Full Stack

Every layer is declarative, version-controlled, and rollback-safe:

| Layer | Technology | What it means |
|-------|------------|---------------|
| **OS** | NixOS | Every node runs the image built from one tagged commit. The same bytes on every node of a release. |
| **Apps** | Docker Compose | Any `docker-compose.yml`, from any source. Not an approved catalog. Not a Helm chart waiting on a vendor's update cycle. The entire Docker ecosystem, deployed through the same Git workflow as everything else. |
| **Data** | ZFS + built-in backup | Checksums on every block. Snapshots, replication and cloud sync built in. |
| **Database** | PostgreSQL per node | Each node keeps working on its own; configuration merges per resource with conflict review. |
| **Identity** | LDAP, Active Directory, OIDC | Directory sign-in with group-to-role mapping, domain membership for SMB, single sign-on, and a local administrator that always works. |
| **Architecture** | x86_64 + ARM64 | Images for both are built for every release. The automated tests run on x86_64. |

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

**Open source. AGPLv3.** What is stable, what is beta and what is experimental is listed, feature by feature, in the [README](README.md#project-status).

[Get started](docs/admin/INSTALLATION-GUIDE.md) | [Architecture](docs/reference/ARCHITECTURE.md) | [GitOps Reference](docs/reference/GITOPS-REFERENCE.md) | [Design Philosophy](docs/reference/PHILOSOPHY.md) | [Threat Model](docs/reference/THREAT-MODEL.md) | [High Availability](docs/admin/HIGH-AVAILABILITY.md)
