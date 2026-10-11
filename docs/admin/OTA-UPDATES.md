# DPlaneOS System Updates

A system installed from the installer image updates itself from the web interface. A new version is installed next to the running one, started once on trial, and kept only if the system is healthy afterwards. Otherwise the system returns to the version before by itself.

(A system that was set up from the DPlaneOS flake with its own NixOS configuration is updated the NixOS way instead: update the flake input and run `nixos-rebuild`.)

---

## How an update works

```
1. Check        the Updates page asks the release server for the latest release
2. Download     the update file of that release for this kind of machine
3. Verify       its signature is checked against the release key in the system
4. Install      the new system is added to the Nix store next to the running one,
                and a boot entry for it is written
5. Restart      you choose when. The boot loader starts the new version ONCE;
                the default entry is still the old version
6. Check        a few minutes after the start: everything that worked before
                the update must work again
        ┌───────┴────────┐
     healthy          not healthy
        │                │
   the new version    the old version is restored,
   becomes the        the system restarts into it
   default
```

If the new version does not start at all (it hangs, the machine is power cycled), the next start is the old version again, because the boot loader was only told to start the new one once. The system then notes that the update did not start and removes it.

Nothing of your data or settings is part of this: they live on the persistent partition and in your pools, and both versions use the same ones.

### What "healthy" means

Before the update is installed, the system notes what works. After the trial start, for up to five minutes, it waits for:

- the DPlaneOS service to answer
- every service that was running before to be running again (web server, database, Samba, NFS, Docker, SSH, FTP, UPS monitoring)
- every pool that was online before to be online again
- the persistent partition to be mounted

If any of these is still missing when the time is up, the update is removed and the reason is shown on the Updates page.

### What stays installed

The running version and the one before it. Older versions are removed after a successful update.

---

## Installing an update

**Updates page (System, Updates).** *Check for updates* shows the latest release and its notes; *Install* downloads and installs it. The page then offers *Restart now*. Until you restart, nothing has changed for the running system, and *Cancel the update* removes the installed version again.

**From a file.** *Install from file* takes the update file of a release (`dplaneos-v<version>-<arch>.update`, on the release page) from your computer. Use it when the system cannot reach the release server.

**Return to the previous version.** *Return to previous version* on the same page restarts the system into the version before.

On the system itself the same is available as a command:

```bash
dplaneos-update status                 # versions, update on trial, last result (JSON)
dplaneos-update verify  <file.update>  # check a file's signature, show what it is
dplaneos-update install <file.update>  # install; the new version starts at the next restart
dplaneos-update revert                 # take an update on trial back, or return to the previous version
```

The log of all of this is `/var/lib/dplaneos/updates/update.log`.

---

## What an update file is, and what it needs

An update file contains the parts of the system that DPlaneOS builds itself (the daemon, the web interface, the system configuration), as a Nix binary cache, signed with the release key (Ed25519). The file is a few hundred megabytes at most.

Everything else a new version may bring (a newer kernel, ZFS, Samba, ...) is fetched from the public NixOS cache, `https://cache.nixos.org`, when the update is installed. **The system therefore needs internet access to install an update**, also when the update file itself is brought by hand. Nothing is changed if the download cannot be completed.

A site without internet access can run a mirror of the NixOS cache and point the system at it. This is a build-time option of the system (`services.dplaneos.ota.upstreamCache`), not a setting.

A file that is not signed with the release key, or was changed after signing, is refused. The public key is part of the system itself (read-only, with the system files).

---

## High availability: updating both nodes

Update one node at a time, the standby first:

1. Put the standby node into maintenance mode (HA page).
2. Install the update on it and restart it. Wait until the Updates page shows the update as kept.
3. End maintenance mode; move the storage groups to the updated node.
4. Repeat with the other node.

---

## Troubleshooting

| What you see | Why | What to do |
|---|---|---|
| "The update to X was not kept", with a reason | The health check found something that worked before and did not after | The reason names it. The system is on the old version; report the problem with the log |
| "The new version did not start" | The trial start did not get as far as the check | The system is on the old version. If it happens again, report it with the hardware details |
| "the signature ... is not valid" | The file is not a release of this project, or is damaged | Download it again from the release page |
| "could not be fetched completely" | `cache.nixos.org` (or your mirror) was not reachable | Check the system's internet access and DNS, then install again |
| "only N MB free on the system disk" | The system partition is full | `nix-store --gc` on the system frees what old versions left behind |
| *Check for updates* says a version has no update file for this kind of machine | The release was published without one for this architecture | Nothing to do on the system; the release is incomplete |
