# DPlaneOS: Limits, and What Has Been Tested

What the system does not do, where its limits are, and what the claims in this repository rest on. If something you need is on this page as a limit, it is a limit.

---

## What "tested" means here

Every release is built by a pipeline that starts real systems in virtual machines and uses them the way the web interface does. A release is not published while one of these fails.

| Test | What it starts | What it shows |
|---|---|---|
| `appliance` | the installed system and a second machine | first boot, pools, SMB/NFS/FTP shares with the users' passwords, directory sign-in (OpenLDAP), system settings taking effect, iSCSI / NVMe-oF / S3 from the other machine, UPS shutdown, SSH, replication, firewall, network changes with rollback, everything again after a restart, a degraded pool |
| `install` | the installer on an empty disk, then that disk on its own | partitioning, the admin password given to the installer, the installed system booting through its boot loader |
| `update` | an installed system and a release server | a signed update installed next to the running version, started on trial, kept; an unsigned one refused; one that breaks a service removed again by itself; manual return |
| `domain` | a Samba domain controller, the appliance, a client | joining an Active Directory domain, domain users on shares, refusals, leaving |
| `reservations` | two nodes on shared iSCSI disks | disk reservations: the owner holds the disks, a takeover shuts out an owner that is cut off but still writing, the pool is intact |
| `quorum`, `votes`, `witness-appliance` | clusters of two and three nodes, third votes of every kind | forming and changing a cluster, quorum |
| `groups`, `failover`, `replicated` | two nodes on a shared disk, or with a pool each | storage groups: planned moves, automatic failover with a third vote and the watchdog, manual takeover, replication between nodes |
| `live-boot` | the live image | the system running from the installation medium |

Besides these: unit tests of the daemon (more than 300 test functions), among them one that checks every SQL statement in the daemon against the database schema, and a check that every request the web interface makes exists in the API with the fields it sends and reads.

**What this does not cover:** physical hardware. Nothing above runs on a real SAS shelf, HBA, BMC, UPS, or against Windows Server. Where a feature depends on hardware, the page that describes it says what was tested and how to check it on yours ([disk reservations](HA-HARDWARE-MATRIX.md), [hardware compatibility](../hardware/HARDWARE-COMPATIBILITY.md)).

---

## Limits

### High availability

- Two nodes without a third vote never fail over automatically; failover is *Take over*, by hand. That is deliberate: after a network split both would consider themselves in charge.
- Replicated storage groups lose the changes since the last replication on a failover. Automatic failover is therefore off for them by default.
- Without a hardware watchdog the kernel's software watchdog is used. It does not reset a node whose kernel hangs; power fencing or disk reservations cover that case.
- Disk reservations need disks that have them (SAS normally, most SATA not). Without them, a takeover from an owner that is still running is refused by ZFS multihost rather than made safe.
- One owner per storage group. There is no active/active access to one pool from two nodes.
- SMB clients reconnect after a failover; locks are lost.
- Clusters have been tested in virtual machines, not under production load.

Time to recover: the watchdog timeout plus 15 seconds before the takeover starts, then the import (about 30 seconds for a shared pool, because ZFS checks that nobody else uses it).

### Updates

- Updates replace the whole system, by an update file signed with the project's release key. A system that does not come up healthy on the new version returns to the previous one by itself; **Return to the previous version** does the same on request, and the boot menu lists the installed versions if the system does not start at all.
- UEFI only (the boot loader is systemd-boot).
- The first installation of a release needs the installation medium; updates need about 3 GB of free space on the system partition.

### Identity

- Active Directory: membership in one domain. Tested against a Samba domain controller, not against Windows Server.
- Directory users who only sign in to the web interface (LDAP) have no SMB access; that takes domain membership.
- OIDC sign-in is tested against a simulated identity provider (the whole code flow, in the daemon's tests), not against a real product such as Keycloak, Entra ID or Authentik.

### Storage and services

- The S3 service is [Versity Gateway](https://github.com/versity/versitygw) serving a folder: one access key, no bucket policies, no versioning.
- NVMe disks have no disk reservations (see above).
- Replication to a system that is not DPlaneOS needs an account there that may run `zfs` (root, or sudo without a password).

### Git as the source of configuration

- In a cluster, only the node that serves every storage group works with the repository.
- Network settings from the repository are applied at once, without the trial period the Network page uses.
- Secrets are never read from or written to the repository.

### The compliance engine

The optional License/Compliance Engine is a separate, closed program that is not part of this repository. Nothing here tests it.

---

## Trusting the binaries

The system image and the daemon are built by the public pipeline from this repository; the inputs are pinned (`flake.lock`). You can build the same thing yourself:

```bash
nix build .#iso                      # the installation image
nix build .#checks.x86_64-linux.appliance -L    # and test it
```

Update files are signed (Ed25519); an installed system refuses an update that is not signed with the key it was installed with (`nixos/update-signing.pub`). The private key is not in the repository: the maintainer holds it, and the release pipeline has it as a secret to sign the update files it builds.

Builds are reproducible in the sense that the same inputs are used every time; the project does not currently verify that two builds are identical bit for bit.
