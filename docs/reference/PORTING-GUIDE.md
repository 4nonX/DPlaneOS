# Porting DPlaneOS to Other Linux Distributions

DPlaneOS is a NixOS appliance. This document is for contributors or organisations that want to fork the project and run the DPlaneOS daemon on a different Linux distribution. It describes what is portable, what requires rebuilding from scratch, and what you will lose compared to the NixOS appliance.

This is not a supported path: no packages and no support exist for other distributions. `install.sh` at the repository root installs the daemon, the web UI and an nginx configuration on a Linux host; CI uses it for the fleet test (`.github/scripts/fleet-integration-test.sh`). It is the starting point for a port, not a product.

For the rationale behind the NixOS-only decision, see [NixOS Rationale](NIXOS-RATIONALE.md).

---

## What Is Already Portable

### The Daemon

`daemon/cmd/dplaned` is a standard Go program. Built with `CGO_ENABLED=0` it is a static binary with no runtime dependency on NixOS or a package manager; ZFS operations then go through the `zfs` and `zpool` commands instead of libzfs. It runs on any Linux kernel that supports ZFS and has the required system tools in `PATH`.

The daemon calls system tools via a strict exec allowlist - `zpool`, `zfs`, `docker`, `smartctl`, `ipmitool`, and others. As long as these binaries are present in `PATH`, the daemon functions.

```bash
# Build a portable static binary (Go version: daemon/go.mod)
cd daemon
CGO_ENABLED=0 go build -mod=vendor \
  -ldflags="-s -w -X main.Version=$(cat ../VERSION)" \
  -o ../build/dplaned ./cmd/dplaned/
```

### The Frontend

`app/` is a pre-built React SPA. It is served as static files by nginx. No changes are needed for non-NixOS targets.

### The PostgreSQL Schema

The daemon migrates its own schema on first connect. PostgreSQL 15 or later works (CI tests 15; the appliance runs 17). `pg_dump` must be the server's major version: configuration backups use it.

---

## What You Must Build

If you want DPlaneOS on Debian, Ubuntu, RHEL, or any other distribution, you are responsible for everything the NixOS appliance provides declaratively. That is roughly the following.

### 1. System Package Installation

The NixOS module (`nixos/module.nix`) declares all system dependencies. You must install equivalent packages through your distribution's package manager. The full list is in [DEPENDENCIES.md](DEPENDENCIES.md).

### 2. systemd Service Unit

A service unit for `dplaned` must be created and installed. The required capabilities are documented in `nixos/module.nix` under `serviceConfig`:

- `CapabilityBoundingSet` and `AmbientCapabilities`: `CAP_SYS_ADMIN`, `CAP_NET_ADMIN`, `CAP_DAC_READ_SEARCH`, `CAP_CHOWN`, `CAP_FOWNER`
- `NoNewPrivileges=true`, `MemoryMax=1G`, `OOMPolicy=continue`, `Restart=on-failure`
- No `ProtectSystem` or `PrivateTmp`: a mount namespace would hide the ZFS mounts the daemon makes from Samba, NFS and the host

`install.sh` writes such a unit.

### 3. ZFS Kernel Module

Your distribution must provide a ZFS kernel module compatible with OpenZFS 2.3+ (the appliance uses 2.4) and your kernel version. On Debian/Ubuntu this is `zfsutils-linux` from the OpenZFS PPA. On RHEL/Rocky it requires the ELRepo kernel or DKMS build.

ZFS DKMS builds (rebuilding the module on every kernel update) are a common source of boot failures. The NixOS appliance avoids this by pinning the kernel and ZFS versions together as tested pairs.

### 4. nginx Configuration

nginx proxies `/api/` and `/ws` to `dplaned` via Unix socket (`http://unix:/run/dplaneos/dplaned.sock:/`) and serves the web UI (`app/`) as static files. WebSocket proxying requires `proxy_read_timeout 300s`. `install/nginx-dplaneos.conf` is a working configuration; `nixos/module.nix` has the appliance's.

### 5. PostgreSQL Initialisation

The daemon expects a PostgreSQL instance with a `dplaneos` database and a `dplaneos` user. The daemon initialises the schema on first connect.

### 6. An Upgrade Path

This is the hardest part. The NixOS appliance provides atomic upgrades with automatic rollback via the generation system. On a traditional distribution:

- There is no equivalent of NixOS generations
- Upgrades are `systemctl stop dplaned && replace binary && systemctl start dplaned`
- Rollback means keeping the previous binary and a restore procedure for any schema migrations
- A partially applied upgrade that fails mid-way leaves the system in an undefined state

You must design and test your own upgrade and rollback procedure.

### 7. State on Its Own Partition (Recommended)

The NixOS appliance keeps all state on `/persist` and binds the listed paths into place at boot, so the system can be replaced without touching the state. On a traditional distribution you decide yourself where state lives and what a reinstall keeps.

---

## What You Lose

| NixOS Appliance | Traditional Linux Fork |
|-----------------|----------------------|
| Atomic OS upgrades with automatic health-check rollback | Manual binary swap; rollback is a restore procedure |
| Entire system state in git | Packages and config diverge from declared state over time |
| Reproducible builds (ISO = local `nix build`) | Install script installs whatever versions are current on the mirror |
| State separated from the system | State spread across the file system |
| Declarative disk layout | Manual partitioning |
| NixOS module options with type checking | Config files and environment variables |
| Kernel/ZFS version assertions at build time | Compatibility issues discovered at runtime |
| Pre-baked airgap ISO | Online installer requires internet access |

---

## Licensing

DPlaneOS is licensed under the [GNU Affero General Public License v3.0](../../LICENSE) (AGPLv3). Any fork that operates the modified software as a network service - even internally - must make the modified source available to users of that service. This applies to the daemon, the frontend, and any NixOS modules.

Forks must:

- Retain all copyright notices
- Provide complete corresponding source (including any modifications to `nixos/`, `daemon/`, and `app-react/`)
- Not remove or obscure the AGPLv3 licence

---

## Starting Point

The starting point for a non-NixOS port is `install.sh` (daemon, web UI, nginx, systemd unit; tested by CI's fleet test) together with `install/` (nginx configuration, udev rules and their scripts, the ZED hook). The NixOS module is the reference for everything else the appliance sets up.

For any questions about porting, open a discussion on the [GitHub repository](https://github.com/4nonX/DPlaneOS). Note that porting assistance is best-effort and not part of the project's core support scope.
