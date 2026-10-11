# DPlaneOS Recovery Guide

For an installed DPlaneOS system (the installer ISO). What to do when you cannot sign in, the system does not start, the web interface does not answer, or a pool does not come up.

Your data is in ZFS pools on the data disks. Nothing in this guide touches them unless it says so, and nearly every problem below is a problem of the system disk or of the configuration, not of the data.

## Quick reference

| Problem | Way out |
|---|---|
| Lost the administrator password, or its second factor | [Reset it from the installation medium](#1-lost-administrator-password) |
| The system does not start after an update | It returns to the previous version by itself; otherwise [choose it in the boot menu](#2-the-system-does-not-start) |
| The web interface does not answer | [Check the services](#3-the-web-interface-does-not-answer) |
| No network after a change, or no address at all | [Network](#4-no-network) |
| A pool is missing, degraded or locked | [ZFS](#5-zfs) |
| The system disk died | [Reinstall; import the pools](#7-the-system-disk-is-gone) |

Signing in at the console or over SSH: administrators of the web interface sign in with their user name and password (`sudo` asks for the same password). There is no `root` password.

---

## 1. Lost administrator password

You need the machine in front of you and the installation medium (the installer ISO of any DPlaneOS version).

1. Start the machine from the installation medium.
2. In the menu choose **Reset the administrator password of an installed system**.
3. Enter the new password twice.
4. Remove the medium and restart.

At its next start the system gives the administrator account (`admin`; if there is none, the oldest local administrator; if there is no administrator at all, a new `admin`) the new password. For that account, two-factor sign-in is switched off and all sessions are ended, and it is active again if it was disabled. Other users, settings and data are not touched. The reset is used once and recorded in the audit log.

If only another user is locked out, an administrator resets that user's password or second factor on the Users page.

---

## 2. The system does not start

**After an update.** A new version is started once on trial. If it does not come up healthy within a few minutes (the web interface answers, the pools are back, the persistent state is mounted), the system restarts into the previous version by itself. If it hangs before that point, switch the machine off and on: the trial start is used up and the previous version starts.

**Choosing a version by hand.** The boot menu (hold the space bar while the machine starts) lists the installed versions. Choose an older one. When the system is up, **System › Updates › Return to the previous version** makes that permanent.

**"Waiting for /persist" or emergency mode.** The system keeps its state (database, settings, keys) on the partition labelled `persist` of the system disk. If that partition cannot be mounted the system stops rather than run with an empty state. Start from the installation medium, choose *Shell*, and check the file system: `fsck.ext4 -f /dev/disk/by-partlabel/persist`.

---

## 3. The web interface does not answer

Sign in at the console or over SSH.

```bash
systemctl status dplaned nginx postgresql      # the daemon, the web server, the database
journalctl -u dplaned -b --no-pager | tail -n 100
sudo systemctl restart dplaned

# Does the daemon answer at all (without the web server)?
curl --unix-socket /run/dplaneos/dplaned.sock http://localhost/health
```

- `dplaned` restarts in a loop: the last lines of its log say why. The usual causes are a full system disk (`df -h / /persist`) and a database that does not start (`journalctl -u postgresql -b`).
- The browser refuses the certificate after you activated a broken one: remove `/var/lib/dplaneos/tls/web.crt` and `web.key` and restart the system; it makes a self-signed certificate again.
- Too many failed sign-ins make an address wait (30 seconds at most). Waiting, or signing in from another machine, gets around it.

---

## 4. No network

**After a change on the Network page.** A change to an interface is on trial: if you do not confirm it in the web interface within the time shown, the previous setting comes back by itself. Wait for that before doing anything else.

**No address at all** (no DHCP server on this network): at the console, sign in and run

```bash
sudo dplaneos-console-net
```

It asks for the interface, an address and the gateway and sets them until the next restart. Then open the web interface and enter the same values under **Network**, where they are kept.

Interfaces that are not configured use DHCP; what is configured on the Network page is kept in the database and applied at every start.

---

## 5. ZFS

```bash
zpool status              # what is imported, and how it is
zpool import              # pools that could be imported
sudo zpool import tank    # import one
sudo zpool import -f tank # ... that was last used by another system
```

- **Pool not imported after a start.** The Pools page offers the import. A pool of a shared storage group is imported by the node that serves the group; see [High Availability](HIGH-AVAILABILITY.md).
- **Degraded.** A disk failed or is missing. The Pools page shows which and offers the replacement; from the shell: `sudo zpool replace tank <old> <new>`. A returned disk is taken back by itself.
- **Suspended.** All paths to the data were lost (a shelf or cable). When the disks are back: `sudo zpool clear tank`. On a node of a cluster, a suspended pool together with the alert "this node has been shut out" means another node took the group over: restart this node.
- **Encrypted dataset locked.** `sudo zfs load-key tank/secure && sudo zfs mount tank/secure`, or unlock it on the Datasets page.
- **Deleted or changed files.** Snapshots: the Snapshots page, or `ls <folder of the dataset>/.zfs/snapshot/`.

Do not use `zpool import -F`, `zpool labelclear` or `zpool destroy` on a pool you want to keep without understanding what they discard.

---

## 6. The database and the configuration

The database is PostgreSQL in `/var/lib/dplaneos/pgsql` (on the `persist` partition).

```bash
# A copy of everything the system knows about itself
sudo -u postgres pg_dump dplaneos > /mnt/tank/backup/dplaneos-$(date +%F).sql

# Back from a copy (all current settings are replaced)
sudo systemctl stop dplaned
sudo -u postgres psql -c 'DROP DATABASE dplaneos' -c 'CREATE DATABASE dplaneos OWNER dplaneos'
sudo -u postgres psql dplaneos < /mnt/tank/backup/dplaneos-2026-01-31.sql
sudo systemctl start dplaned
```

Passwords of other systems that are stored in the database (directory, cloud accounts, mail) are encrypted with `/var/lib/dplaneos/secrets.key`. A copy of the database is only complete together with that file.

**Earlier settings.** System › Configuration History records every change to shares, users, exports and settings, shows the difference and restores single items.

**Starting over.** Dropping the database as above without restoring a copy gives a system with no users and no settings; the setup dialog appears again. Pools, datasets and their files are untouched; shares and users have to be set up again.

---

## 7. The system disk is gone

1. Replace the disk and install DPlaneOS from the installation medium. The installer only writes to the disk you choose.
2. Sign in, open **Storage › Pools** and import the pools. Datasets, snapshots and files are as they were.
3. Shares, users, schedules and settings were on the system disk. Restore the database copy (section 6) together with `secrets.key`, or apply your `state.yaml` if you keep the configuration in Git, or set them up again.

The same applies to moving the data disks to another machine.

---

## 8. Collecting information

**System › Support › Download bundle** packs the daemon's log, the state of pools, disks and the network, and the last entries of the audit log into one file. It contains no passwords and no keys; it does contain user names and addresses. For a cluster, add the output of `corosync-quorumtool -s` from each node.
