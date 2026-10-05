# ADR-0009: Watchdog self-fencing as the baseline; stronger fencing is optional; inform, do not block

| | |
|---|---|
| **Status** | Proposed |
| **Date** | 2026-10-05 |
| **Design** | [Design 0001, section 5.3.1](../0001-distributed-state-gitops-ha.md) |

## Decision

Every HA topology (shared storage and replicated, with any drive type) uses the same baseline: **quorum from a third vote plus watchdog self-fencing**, with the survivor waiting out the watchdog timeout before it takes over. SCSI-3 reservations, ZFS multihost and IPMI/PDU power fencing are **optional layers** that add protection where the hardware allows. The GUI states the protection level and the hardware's limitations in plain language and does not refuse a configuration because stronger hardware is missing. The only hard rule is the one physics imposes: **automatic failover needs a third vote**; without one, failover is a confirmed manual takeover.

## Context

Two-node HA must be accessible to homelab users with consumer hardware (SATA drives, no BMC, no managed PDU) and still be safe. The earlier draft of this design required power fencing for shared storage without SCSI-3 reservations, which would exclude most SATA setups.

Proxmox VE solves the same problem without external fencing hardware ([High Availability](https://pve.proxmox.com/wiki/High_Availability), [Cluster Manager](https://pve.proxmox.com/wiki/Cluster_Manager)):

- Fencing is the node's own watchdog: a hardware watchdog if configured, otherwise the Linux `softdog` ("still reliable" but "lower reliability than a hardware watchdog").
- A node that loses quorum cannot reset its watchdog while it runs services, and is rebooted after 60 seconds.
- The survivor recovers services only after it acquires the failed node's lock, which is after the watchdog has fired; typical detection-to-recovery time is about two minutes.
- External power fencing is optional.
- HA needs reliable quorum: three nodes, or two nodes plus a QDevice as third vote. Without it there is no automatic recovery.

DPlaneOS already implements the same mechanism (`internal/ha/watchdog*.go`, `softdog` fallback in `nixos/ha.nix`, survivor waits watchdog timeout plus margin) and uses it for the replicated topology with any drives. It is a sound baseline for shared storage as well: the protection comes from the node that lost quorum resetting itself, not from the disks.

## Details

Layers, from baseline to strongest; the GUI shows which are active and what each adds:

| Layer | Needs | Adds | Limitation shown to the user |
|---|---|---|---|
| Third vote (QDevice / witness) | any small always-on device: Raspberry Pi, VM, another D-PlaneOS node, cloud instance | automatic failover | without it: no automatic failover; "Take over" is a manual action after confirming the other node is off |
| Watchdog self-fence (baseline) | `/dev/watchdog`; `softdog` when there is no hardware watchdog | an isolated node resets itself before the survivor takes over | softdog: "depends on the kernel still running; a hard hang can stop it — a hardware watchdog is more reliable" |
| ZFS multihost (MMP) | shared storage, any drive type | refuses to import a pool that another node is still writing | adds a few seconds to imports; recommended on for shared storage |
| SCSI-3 persistent reservations | disks that pass the write probe (SAS, some enterprise SATA) | the disks themselves reject writes from the fenced node | most SATA drives cannot; shown as "not supported by these disks" |
| IPMI / PDU power fencing | BMC or switched PDU reachable from the peer | the survivor switches the failed node off | needs the management network to be reachable |

- Failover order: lose quorum → isolated node stops resetting its watchdog → survivor waits watchdog timeout plus margin (and, where configured, preempts reservations / powers the node off) → survivor imports (multihost check applies) → epoch increments.
- Two-node clusters without a third vote run Corosync `two_node` with `wait_for_all` for membership but **do not fail over automatically**: on a network split both halves stay quorate, so an automatic takeover could run the pool on both nodes. The GUI explains this and offers the manual takeover.
- Hardware findings are warnings with an explanation, never blockers: softdog only, no BMC, disks without reservations, SATA without interposers in a dual-expander JBOD (single path), witness on the same switch as both nodes.

## Consequences

- Shared storage with SATA disks becomes a supported two-node topology instead of being redirected to replication.
- The setup wizard needs a protection summary ("Your pair is protected by: third vote ✓, watchdog (softdog) ⚠, multihost ✓, reservations ✗, power fencing ✗") with an explanation per line.
- Failover takes the watchdog timeout plus margin (about one to two minutes), as in Proxmox; the GUI states the expected failover time.

## Alternatives considered

- **Mandatory power fencing for shared storage without reservations:** safest, but excludes most homelab hardware.
- **Pacemaker-style fence race in two-node clusters without a third vote:** requires working power fencing on both sides; without it, both nodes would take over on a split.
- **Refusing HA without a hardware watchdog:** softdog is what Proxmox uses as its fallback; refusing would exclude many boards for a modest gain.
