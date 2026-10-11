# DPlaneOS Troubleshooting

Where to look first, by symptom. For a system that does not start, a lost password, no network, or a lost system disk, see the [Recovery Guide](RECOVERY.md).

An installed system is an image: there is nothing to rebuild, reinstall or repair in the system itself. If a service of the system is broken after an update, the answer is to return to the previous version (System › System Updates, or the boot menu); if it is broken without an update, it is a defect, and a support bundle (below) is what a report needs.

---

## First steps

1. **System › Logs** shows the daemon's log and the system's; **Security › Audit Log** shows who changed what.
2. At the console or over SSH:

   ```bash
   systemctl status dplaned nginx postgresql
   journalctl -u dplaned -n 100
   curl --unix-socket /run/dplaneos/dplaned.sock http://localhost/health
   zpool status
   ```
3. **System › Support › Download bundle** packs logs and state into one file (no passwords, no keys) for a report at <https://github.com/4nonX/DPlaneOS/issues>.

---

## The web interface

| Symptom | Cause and what to do |
|---|---|
| Blank page or broken layout right after an update | The browser still holds the previous version's files. Reload without cache (Ctrl+Shift+R, on a Mac Cmd+Shift+R) |
| "Too many failed attempts" at sign-in | Each wrong password from an address makes that address wait longer before the next try (2, 4, 8, 16, then 30 seconds); other addresses are not affected. The count is forgotten after 15 minutes without a failure |
| The browser warns about the certificate | The certificate is self-signed until you install one: Security › Certificates |
| The page loads, every action fails with 502 | The daemon is not running: `systemctl status dplaned`, then the log. [Recovery, section 3](RECOVERY.md#3-the-web-interface-does-not-answer) |

## Storage

| Symptom | Cause and what to do |
|---|---|
| No pools after the first start | None has been created or imported yet: Storage › ZFS Pools. Pools from another system appear under *Import* |
| A pool is missing after a restart | Its disks were not there in time, or not all of them. `zpool import` at the console shows what ZFS sees and why it does not import. When the disks appear, the pool is imported by itself |
| A new disk does not appear under System › Hardware | The daemon learns of disks from the kernel. `lsblk` at the console: if the disk is not there either, it is the cable, the bay or the controller (`dmesg \| tail`) |
| A disk was pulled and put back, the pool stays degraded | `zpool status` says what ZFS thinks of the disk. If it is marked faulted or removed, bring it back with *Online*, or use *Replace*, on the pool's page |
| A pool is suspended | All paths to its data were lost (an enclosure without power, a cable). When the disks return, I/O is resumed by itself; otherwise `zpool clear <pool>` |
| Replacing a disk makes no progress | `zpool status` shows the resilver and its speed. A resilver of a large, full disk takes many hours |
| "pool is busy" on export | Something uses it: a share with open files, a container, a shell standing in it |

## Sharing

| Symptom | Cause and what to do |
|---|---|
| A user cannot sign in to an SMB share | Users of the web interface are the SMB accounts, with the same password. For domain users: Identity › Directory Service shows whether the trust with the domain is intact |
| Windows shows the share but refuses access | The share's *valid users*, then the permissions of the folder itself (Storage › ACL Manager) |
| NFS mount is refused | The client's address is not within the export's network (Sharing › NFS Exports) |
| iSCSI or NVMe initiator finds nothing | The target has no access entry for this initiator's name, or the initiator uses an address the target does not listen on |

## Containers

| Symptom | Cause and what to do |
|---|---|
| Deploying a stack ends without containers | The job's output, shown in the dialog, has Docker's own message. Usually an image could not be pulled, or a port is in use ([Port Reference](../reference/PORT-REFERENCE.md)) |
| Images cannot be pulled | Docker has to reach the registry directly. An HTTP proxy for Docker cannot be configured in this version; on a network without direct access, use a registry mirror inside the network and name it in the image reference |
| A stack does not start after a restart | Stacks with volumes on a pool run only while that pool is imported |
| A container shows the generic icon | Give it the label `dplaneos.icon` in the compose file |

## Alerts

| Symptom | Cause and what to do |
|---|---|
| No alert arrives | System › Alerts: each channel can be tested there, and the answer shown is the mail server's or the web service's own. A webhook only receives the events selected for it |
| No SMART values for a disk | Disks behind some USB bridges and RAID controllers do not pass SMART through. `smartctl -a /dev/sdX` at the console says so |

## Snapshots and replication

| Symptom | Cause and what to do |
|---|---|
| A schedule did not run | Data Protection › Snapshot Scheduler and › Replication show the last run and its message. A schedule does not run while its pool is not imported |
| Replication fails with "permission denied", or asks for a password | The key of this system is not in place at the target, or the user there may not run `zfs`. *Test* on the target's entry says which |
| Replication fails after the target was changed by hand | The target must not be written to. Roll it back to the last common snapshot there, or start again with a full copy |

## Updates

| Symptom | Cause and what to do |
|---|---|
| No update is found although a new release exists | The system looks at the project's releases; without internet access, upload the update file instead |
| The update file is refused | Its signature does not match the release key of this installation, or the file is damaged or for the other architecture. The message says which |
| After the restart the old version runs | The new version did not come up healthy and the system returned by itself. System › System Updates says so |

More: [System Updates](OTA-UPDATES.md).

## Clusters

See [HA Failure Modes](HA-FAILURE-MODES.md): what the cluster does in each case, and what is yours to do.

## Building from source

Everything is built through the flake: `nix build .#iso` for the installer image, `nix develop` for a shell with the pinned Go and Node; see [the NixOS side](../../nixos/README.md). The Go dependencies are vendored (`daemon/vendor`), so the daemon also builds without network access: `cd daemon && CGO_ENABLED=0 go build -mod=vendor ./cmd/dplaned/`.
