# DPlaneOS Updates

An update moves an installed system to a newer DPlaneOS release. It builds the release as a new NixOS generation, reboots into it and checks the system. If the check fails, the system goes back to the previous generation by itself. Every older generation also stays selectable in the boot menu.

Live systems are not updated; boot a newer live image instead.

---

## Where the system comes from

Each system records the release it was built from in `/etc/dplaneos-source.json`, which holds the flake, the git revision and the version. On an installed system the daemon writes `/etc/nixos/flake.nix` from that record when it starts:

```nix
# Managed by DPlaneOS: ...
{
  inputs.dplaneos.url = "github:4nonX/DPlaneOS/<revision or release tag>";
  outputs = { dplaneos, ... }: {
    nixosConfigurations.dplaneos = dplaneos.nixosConfigurations.dplaneos.extendModules {
      modules = [ ./hardware-configuration.nix ]
        ++ (if builtins.pathExists ./local.nix then [ ./local.nix ] else [ ]);
    };
  };
}
```

All rebuilds go through this flake: applying settings, the watchdog apply and updates. If the flake is missing, a rebuild is refused. NixOS's stock `configuration.nix` would otherwise replace DPlaneOS with plain NixOS, so the installer replaces it with one that stops a rebuild without the flake.

Local additions go in `/etc/nixos/local.nix`, which is an ordinary NixOS module. If you replace `flake.nix` with your own (one without the "Managed by DPlaneOS" header and input line), the Updates page leaves it alone, and you update it yourself.

Settings made in the web UI are stored in `/var/lib/dplaneos/dplane-state.json`. That file is read at evaluation time, so a manual rebuild needs `--impure`:

```bash
sudo nixos-rebuild switch --flake /etc/nixos#dplaneos --impure
```

---

## Updating

On the **Updates** page:

1. Click **Check for updates**. The page asks GitHub for the latest release.
2. Click **Update to vX.Y.Z**, then **Update and reboot**.

The update runs these steps:

```
1. Record the running system, the flake, the imported pools and the number
   of SMB shares                              (/var/lib/dplaneos/update-pending.json)
2. Point /etc/nixos/flake.nix at the release tag
3. nixos-rebuild boot --flake /etc/nixos#dplaneos --impure --recreate-lock-file
      fails → the flake is restored and nothing changes
4. Reboot (10 seconds after the build)
5. After the boot, dplaneos-ota-health checks the system (see below)
      passes → the update is kept
      fails  → the previous generation becomes the boot default again, the
               flake is restored, and the machine reboots into it
6. The daemon sends an alert with the result: upgrade.applied (info) or
   upgrade.rolled_back (critical)
```

The same update through the API:

```bash
curl -X POST .../api/system/updates/nixos/apply -d '{"version":"v16.0.0"}'   # empty: latest
curl .../api/system/updates/nixos?check=1
```

---

## The check after the reboot

`dplaneos-ota-health.timer` starts the check 60 seconds after boot. The check then retries every 15 seconds for up to 5 minutes while services start. It passes when all of the following are true:

- the system booted is the new one, not the previous one;
- the daemon answers on its socket;
- nginx is running, and PostgreSQL is running if the database is local;
- every pool that was imported before the update is imported;
- Samba is running if SMB shares were enabled.

If the new generation does not boot at all, choose the previous entry in the boot menu. The check then sees the old system and records the update as rolled back.

The result is in `/var/lib/dplaneos/update-result.json`, and the log is in `journalctl -u dplaneos-ota-health`.

---

## Going back by hand

If you find a problem after the check has passed:

```bash
sudo nixos-rebuild switch --rollback       # previous generation, now and at boot
sudo sed -i 's|DPlaneOS/v[0-9.]*"|DPlaneOS/v15.1.0"|' /etc/nixos/flake.nix   # the release you went back to
```

Without the second line, the next rebuild would build the newer release again. If the system does not boot, pick an older generation in the systemd-boot menu.

systemd-boot keeps the two newest generations in the menu (`boot.loader.systemd-boot.configurationLimit`). Garbage collection removes generations older than 14 days.

---

## HA clusters: one node at a time

```
1. Maintenance mode on the standby node (B)
   POST /api/ha/maintenance {"enable": true, "duration_minutes": 120}
2. Update B (its Updates page, or POST /api/system/updates/nixos/apply on B)
3. B reboots and passes its check (GET /api/system/updates/nixos on B: last_result ok)
4. Controlled failover to B
   POST /api/ha/failover {"dry_run": false}
5. Maintenance mode on A, update A the same way
6. End maintenance mode on both nodes
   POST /api/ha/maintenance {"enable": false}
```

Clients see one move of the virtual IP, in step 4.

---

## Configuration

```nix
services.dplaneos.ota = {
  enable = true;            # the check after the reboot (off on live systems)
  healthCheckDelay = "60s"; # when the check starts after boot
  checkSeconds = 300;       # how long it waits for the system to become healthy
};
```

---

## Changes from the A/B slot scheme

Earlier documentation described A/B partition slots, `dplaneos-ota-update` and signed update bundles. That scheme never worked:

- it needed boot entries named "DPlaneOS (Slot A/B)" that nothing created;
- the root filesystem was always `system-a`;
- nothing built the bundles.

NixOS generations provide the same atomic switch and rollback. The `system-b` partition created by installers before 16.0 is unused (new installs give its space to the system partition). `services.dplaneos.ota.signingKey` has been removed.

---

## Installs from before 16.0

A system installed before 16.0 has no `/etc/dplaneos-source.json` and no system flake, and its Updates page used the A/B scheme. Move it to 16.0 once by hand. From then on the Updates page works as described above.

```bash
sudo tee /etc/nixos/flake.nix >/dev/null <<'EOF'
# Managed by DPlaneOS: this machine is built from the DPlaneOS release
# below plus its hardware configuration (and local.nix, if present).
{
  inputs.dplaneos.url = "github:4nonX/DPlaneOS/v16.0.0";

  outputs = { dplaneos, ... }: {
    nixosConfigurations.dplaneos = dplaneos.nixosConfigurations.dplaneos.extendModules {
      modules = [ ./hardware-configuration.nix ]
        ++ (if builtins.pathExists ./local.nix then [ ./local.nix ] else [ ]);
    };
  };
}
EOF
sudo nixos-rebuild boot --flake /etc/nixos#dplaneos --impure
sudo reboot
```

On arm64, use `dplaneos.nixosConfigurations.dplaneos-arm`. `/etc/nixos/hardware-configuration.nix` was written by the installer.

These systems' partitions are labelled `ESP`, `system-a` and `persist`; installs from 16.0 use `dplaneos-esp`, `dplaneos-system` and `dplaneos-persist`. The rebuild runs on the machine with `--impure`, sees the old labels and mounts by them (`services.dplaneos.legacyPartitionLabels`). If you build the system on another machine, set `services.dplaneos.legacyPartitionLabels = true;` in `/etc/nixos/local.nix`. Without it, the new generation cannot find its disk at boot. In that case, pick the previous generation in the boot menu.

The persisted `/etc/systemd/network` of these systems is copied into place once at the first boot (`dplaneos-networkd-migrate`).

---

## Troubleshooting

| Symptom | Cause | What to do |
|---|---|---|
| "The system flake is missing" | System built from an unpublished tree, so the source record has no revision | Write `/etc/nixos/flake.nix` yourself (see above) |
| Build fails | Release unreachable, or a `local.nix` that does not evaluate | The page shows the build output. Nothing was changed. |
| Back on the old version after an update | The check failed | Read `update-result.json` for the reason and `journalctl -u dplaneos-ota-health` |
| Updates page says the flake is not managed | `flake.nix` was replaced by hand | Update with `nix flake update` and `nixos-rebuild` |
