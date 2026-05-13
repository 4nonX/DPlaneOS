# DPlaneOS Design Philosophy

This document explains the design decisions behind DPlaneOS: why it is built the way it is, what problems it is solving, and what trade-offs were made intentionally. Read this before the [Architecture Reference](ARCHITECTURE.md) if you want to understand the reasoning before the mechanics.

---

## The Problem

Traditional NAS software accumulates state over time. Packages are installed imperatively. Configuration files are hand-edited. Services are enabled one at a time. After six months, no one can fully describe what the running system is - and recreating it from scratch is a multi-day exercise.

This is tolerable for a general-purpose workstation. It is not tolerable for a storage appliance. A NAS is infrastructure. When it fails, data is at risk. The operator needs to be able to rebuild it exactly - not approximately - from known-good state.

The secondary problem is correctness. Traditional NAS management tools let you configure things through a web UI and then silently write config files. The UI and the config can drift apart. You can make a change via SSH that the UI does not know about. There is no authoritative source of truth.

DPlaneOS is built around a single answer to both problems: **the entire system state is always derivable from two text files**.

---

## The Four Principles

### 1. Everything is Declared

There is no imperative installation of DPlaneOS. You do not run `apt install`, configure systemd units by hand, or manage nginx with chef recipes. You write a declaration. The system applies it.

Two declarations cover the entire system:

- **`configuration.nix` / `flake.nix`:** The OS-level declaration. Kernel version, ZFS package, systemd units, firewall baseline, package set, HA cluster membership. Managed by NixOS. Changes here require a rebuild.
- **`state.yaml`:** The runtime declaration. ZFS pools and datasets, SMB/NFS shares, Docker stacks, users and groups, replication schedules, network config. Managed by the GitOps engine. Changes here are applied without a rebuild.

Everything that the system does is derivable from these two files. Given the files and a blank machine, you can produce the exact running system.

### 2. The System Can Always Reproduce Itself

The flake lockfile pins every input: nixpkgs revision, kernel version, ZFS package version. Two builds from the same lockfile produce byte-for-byte identical systems. The ISO you download was built from the same inputs you can build locally.

This matters because upgrade paths are predictable. You can see exactly what is changing before applying it. Rolling back is not "undo" - it is booting the previous immutable system closure, which is still present in the inactive boot slot.

The corollary: if you have `state.yaml` in Git and can boot the ISO, you can fully recover the system. No installation procedures, no "I think I had this setting", no partial states.

### 3. Execution is Constrained

The daemon makes changes by calling system tools (`zfs`, `zpool`, `docker`, `samba`, `exportfs`, `nvmetcli`). It does not execute arbitrary shell commands. Every `exec.Command` call goes through a strict allowlist (`internal/security/whitelist.go`) that validates the binary name and each argument against predefined patterns.

This has two effects:

**Security:** A compromised API request cannot leverage the daemon to execute arbitrary commands on the host. The worst a compromised request can do is trigger one of the predefined, validated operations.

**Auditability:** Because every system-level action is a named, validated operation, the audit log can describe exactly what was done and why. There is no "ran shell script" entry - there are specific named operations with specific named parameters.

### 4. Drift is Made Visible

The GitOps engine continuously compares the live system state against `state.yaml`. Any divergence - a dataset created via `zfs create` directly on the shell, a share added via the UI without updating `state.yaml` - is detected and surfaced to the operator within five minutes.

This is intentional. The UI can make immediate changes, and the daemon can apply them instantly. But long-term truth lives in `state.yaml`. The drift detector keeps operators honest: changes that are not committed to Git are not permanent.

The alternative (enforcing that changes can only happen through `state.yaml`) would eliminate the useful property of immediate effect. The chosen model allows both: instant changes when needed, declarative source of truth always visible.

---

## The Three Layers

These four principles map onto three distinct layers of the system:

```
WHAT IT SHOULD BE  →  configuration.nix / state.yaml     (Declaration)
HOW TO GET THERE   →  nixos-rebuild / GitOps apply engine  (Reconciliation)
MAKING IT HAPPEN   →  dplaned daemon / NixOS activation   (Execution)
```

Each layer has a clear owner and a clear scope:

- **NixOS owns the platform.** The kernel, ZFS module, systemd units, nginx, PostgreSQL, and the daemon binary all come from the NixOS system closure. NixOS guarantees they are present, correctly versioned, and activated.
- **The daemon owns the workload.** ZFS datasets, SMB shares, NFS exports, Docker stacks, users - these are the things that change during normal NAS operation. The daemon manages them against `state.yaml`.
- **Neither layer reaches into the other.** The daemon does not modify the NixOS system. NixOS does not manage dataset quotas.

This separation is why OTA updates are safe: the OS slot can be updated atomically without touching the workload state. The workload state can be changed without rebooting. They are genuinely independent.

---

## The Data Safety Model

Data on ZFS pools is the most valuable thing the system holds. The architecture reflects this:

**ZFS pools are independent of the OS.** Data disks are entirely separate from the boot disk. A complete boot disk failure - including reinstalling DPlaneOS from scratch - does not affect pool data. After reinstall, `zpool import -a`.

**Impermanence protects the OS layer.** The root filesystem (`/`) is ephemeral and replaced on every OTA update. Only what is explicitly listed in `impermanence.nix` survives across reboots. This prevents the accumulation of unmanaged state that would make the system non-reproducible.

**Execution is transactional.** The GitOps apply engine halts on the first failure. Already-applied items are safe by design (ZFS operations are idempotent). A partially-applied plan leaves the system in a known state, not an unknown one.

**Destructive changes require explicit approval.** The diff engine classifies operations as `BLOCKED` when they are potentially destructive (deleting a dataset, removing a disk from a pool). BLOCKED items do not execute without explicit operator approval, even when `state.yaml` says to do it.

---

## The Operational Trust Model

DPlaneOS is designed for a specific trust model: an operator with physical access to the hardware who needs to be able to fully audit and reproduce everything the system does.

This means:

- **The audit log is tamper-evident.** Entries are HMAC-chained. You cannot remove an entry without breaking the chain. The audit log is a forensic record, not an operational convenience.
- **All user-facing credentials are bcrypt-hashed.** Passwords are never stored in reversible form. The GitOps capture workflow never exports password hashes.
- **Session management is explicit.** Sessions have a fixed lifetime. There are no "remember me" tokens that persist indefinitely.
- **RBAC is fine-grained.** 34 discrete permissions. Role assignments have optional expiry. Changes are audited.

What is not in scope:

- **Multi-tenant isolation.** DPlaneOS assumes a single operator or a small team. Users share a system; they do not have isolated namespaces. ZFS dataset quotas and SMB share permissions provide data separation, not process isolation.
- **Air-gapped operation after install.** The system can be installed from the ISO without internet access. Ongoing OTA updates require Nix cache access. `state.yaml` Git operations require Git remote access.

---

## Design Decisions That May Surprise You

**Why a custom YAML parser?** The daemon has zero external dependencies at runtime. The standard library covers everything the parser needs. Importing a third-party YAML library would add an attack surface and a dependency to maintain. The parser is deliberately minimal: it supports exactly the YAML subset that `state.yaml` uses. Unknown constructs are parse errors, not silent coercions.

**Why PostgreSQL and not SQLite?** HA mode requires replication. Patroni can replicate PostgreSQL with streaming replication to a hot standby. SQLite has no equivalent. Using PostgreSQL in single-node mode means the same daemon binary works in both modes with only a connection string change.

**Why a daemon in Go?** The daemon is a single statically-linked binary with no runtime dependencies. It can be copied to any Linux system and run. Static linking eliminates the "which libc version" problem. Go's concurrency model matches the workload: many independent subsystems (ZFS, Docker, Samba, NFS, WebSocket hub) running concurrently without shared mutable state.

**Why NixOS exclusively?** Every capability that distinguishes DPlaneOS from "just another web UI for ZFS" - atomic upgrades, reproducible builds, impermanence, A/B slots, pinned kernel and ZFS versions - depends on NixOS primitives. These are not optional features. They are the architecture. A port to another Linux distribution would need to reimplement all of them, at which point you have built your own NixOS subset. See [PORTING-GUIDE.md](PORTING-GUIDE.md) for what that actually entails.

---

## Where to Go Next

| Question | Document |
|----------|----------|
| How does the architecture actually work? | [ARCHITECTURE.md](ARCHITECTURE.md) |
| What goes in state.yaml and how does apply work? | [GITOPS-REFERENCE.md](GITOPS-REFERENCE.md) |
| How does NixOS make this possible? | [NIXOS-RATIONALE.md](NIXOS-RATIONALE.md) |
| How do I run this on hardware? | [INSTALLATION-GUIDE.md](../admin/INSTALLATION-GUIDE.md) |
| What are the security boundaries? | [THREAT-MODEL.md](THREAT-MODEL.md) |
