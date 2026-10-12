# DPlaneOS Design Philosophy

Why the system is built the way it is, and which trade-offs were made on purpose. For how it works, see the [Architecture Reference](ARCHITECTURE.md).

---

## The problem

A NAS is infrastructure: when it fails, data is at risk, and whoever runs it must be able to say what it is doing and to set it up again. Ordinary NAS software makes both hard over time. Packages are installed one by one, configuration files are edited by tools and by hand, and after a year nobody can describe the running system completely.

DPlaneOS answers with two descriptions that together are the system:

- **the release**: one image with the kernel, ZFS, every service and their configuration, built from a tagged commit;
- **the configuration**: pools, datasets, shares, users, schedules, network and system settings, held in the system's database and, if you want, as `state.yaml` in Git.

A machine with the release installed and the configuration applied is the NAS. The data lives in ZFS pools on disks of their own and comes back by importing them.

---

## Principles

### 1. The system is replaced, not modified

Nothing is installed or upgraded piece by piece on a running system. An update is the next image, placed next to the running one, started once on trial, and kept only if the system is healthy with it. Returning to the previous version means starting it: it is still there, complete.

### 2. Configuration can be declared, and does not have to be

Changes made in the web interface take effect at once. The same configuration can be written down as `state.yaml`: the system compares the file with what is running, shows the difference as a plan, and applies only that difference. *Capture* writes the file from the running system. Both ways lead to the same state, and the system shows when they have drifted apart.

Requiring every change to go through Git would lose the immediate change that an operator sometimes needs; allowing only the interface would lose review, history and reproducibility. Both are there, and the difference between them is always visible.

### 3. What destroys data waits for a person

Applying a `state.yaml` never deletes a dataset that holds data, removes a pool or takes a share away from connected clients on its own: such steps are marked as blocked in the plan and need an explicit approval, whatever the file says. Automatic apply of new commits (off by default) only ever adds and modifies. In the web interface, destroying a pool or a volume needs a confirmation that names it; destroying a pool also needs an account with a second factor.

### 4. The daemon runs programs, never text

The daemon changes the system by running programs with their arguments as a list, never by handing text to a shell. Where arguments come from a request, the program, its sub-command and the shape of each argument are fixed by an allowlist. A request cannot make it run something else. The audit log records what was changed, by whom, from where, and is chained so that a removed or altered entry is detected.

### 5. Say what is not there

The interface and the documentation state what a feature does and where it stops: which protection layers of a cluster are active and which are missing, which hardware the project has tested and which it has not, what a status has actually been verified against. A feature that does not work end to end is a defect to fix or to remove, not a line to keep in a list.

---

## The data comes first

**Pools are independent of the system.** The installer and every update touch only the system disk. A lost system disk costs the configuration (unless it is in Git or in a copy of the database), never the pools.

**Two nodes never write to one pool.** In a cluster, a node takes a storage group over only with quorum; an owner that loses quorum resets itself; ZFS multihost and, where the disks support them, disk reservations stop a second writer at the pool and at the disks. Where these layers are missing, the interface says so, and automatic failover without a third vote does not exist.

**A change the system does not take is taken back.** Where a setting is written and then applied (network, firewall, shares, block targets), a failed apply restores the previous state, and the error says why. The saved configuration does not claim more than the system does.

---

## Decisions that may surprise

**One daemon, running as root.** A NAS manager imports pools, mounts file systems, configures the network and the firewall. Splitting that into privilege-separated parts would be a larger system with more to go wrong; a reduced capability set that still contains what is needed is root in all but name. What limits the daemon is inside it: sessions, permissions on every route, the command allowlist, path checks. The [Threat Model](THREAT-MODEL.md) says plainly what follows from that.

**Its own YAML parser.** `state.yaml` uses a small part of YAML. The parser accepts exactly that part and treats everything else as an error, instead of the surprises a full YAML implementation allows (anchors, type guessing, multiple documents).

**Few dependencies.** Eleven direct Go dependencies, vendored in the repository; a static binary. Less to audit, less to break on an update.

**PostgreSQL, on every node.** Many parts of the daemon write at the same time, and PostgreSQL handles that, migrations and JSON well. Each node of a cluster has its own database and keeps working when it is cut off; the nodes exchange configuration and merge it per item, instead of depending on one shared database being reachable.

**Mature software underneath.** ZFS for the data, Samba, the kernel's NFS and block targets, Corosync for quorum, Docker for containers. DPlaneOS is what ties them together and keeps their configuration; it does not reimplement them.

**NixOS, exclusively.** See [Why DPlaneOS is built on NixOS](NIXOS-RATIONALE.md).

---

## What it is not

- **Not multi-tenant.** One operator or a small team; users are separated by file permissions and quotas, not from each other as tenants.
- **Not a general-purpose server.** There is no package manager to add software with; applications run as containers.
- **Not finished.** Which parts are stable, which are beta and which are experimental is listed in the [README](../../README.md#project-status).
