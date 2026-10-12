# DPlaneOS Live Image

The live image starts DPlaneOS from a USB stick, without installing anything: to try it on your hardware, or to get at the pools of a machine whose system does not start.

**It runs from memory.** Users, shares and settings made in the live system are gone at shutdown. What is written to the pools on the machine's disks stays. To keep a configuration, install DPlaneOS ([Installation Guide](INSTALLATION-GUIDE.md)).

---

## Start it

1. Download `dplaneos-v<version>-live-x86_64.iso` (or `-arm64`) from the [latest release](https://github.com/4nonX/DPlaneOS/releases/latest) and write it to a USB stick of 8 GB or more, as for the installer.
2. Start the machine from the stick (UEFI). It needs 8 GB of memory or more: the system itself lives there.
3. The console logs in by itself and shows the address of the web interface and a **setup code**.
4. Open `https://<address>/` in a browser. The first visit creates the administrator: choose a password and enter the setup code from the console.

The setup code is there because the live system has no administrator yet when it comes up, with your pools attached: only someone who can read the machine's console may create one. It changes at every start; `cat /run/dplaneos/setup-code` shows it again.

## Pools

- Pools that are free are imported at start: pools that were exported, or last used by this same live system.
- Pools that were last used by **another system** and not exported there (the installed DPlaneOS of this machine, another NAS the disks came from) are not imported by themselves: the other system might still be using them, as on a shelf shared by two nodes. Import them under **Storage › ZFS Pools › Import**, which asks before it forces the import. Do that only when no other system has the pool.
- At shutdown the live system exports the pools again, so that the installed system finds them free at its next start. If the live system is switched off without a shutdown, the installed system has to import the pool with *force* once.

Do not start the live image on a node of a running cluster to "have a look" at shared disks.

## What works

Everything the installed system does that does not need a restart to survive: pools, datasets, snapshots, shares, users, containers, replication. System updates do not apply (there is nothing installed to update).

At the console you are root; `zpool`, `zfs` and `docker` are there. SSH is running, but root has no password and nobody can sign in over SSH until you set one at the console (`passwd`), or add a key for the administrator in the web interface (Identity › SSH Keys).

## Rescue

To get data off a machine whose system no longer starts:

1. Start the live image, create the administrator.
2. Import the pool (with force, if it was not exported: the installed system did not shut down cleanly, and you know it is not running).
3. Share the dataset over SMB, or replicate it to another system, or copy it with the file explorer.
4. Shut down properly, then reinstall or repair.

To reset a lost administrator password or reinstall, use the installer image instead ([Recovery](RECOVERY.md)).

## If something does not work

| | |
|---|---|
| The web interface does not answer | wait a minute after the console appears; `systemctl status dplaned nginx postgresql` |
| No address | no DHCP on this network: `ip addr add 192.168.1.50/24 dev <interface>` at the console, for this session |
| A pool is missing | `zpool import` at the console lists what is found and why it is not imported; `journalctl -u dplane-zfs-auto-import` shows what happened at start |
| Out of memory | the live system and its database live in memory; containers and large uploads to places outside the pools use it up |
