# Porting DPlaneOS to Other Linux Distributions

DPlaneOS is a NixOS appliance. This document is for contributors or organisations that want to fork the project and run the DPlaneOS daemon on a different Linux distribution. It describes what is portable, what requires rebuilding from scratch, and what you will lose compared to the NixOS appliance.

This is not a supported path. No official packages, installer scripts, or support exist for non-NixOS platforms. This document exists to give forks a realistic starting point.

For the rationale behind the NixOS-only decision, see [NixOS Rationale](NIXOS-RATIONALE.md).

---

## What Is Already Portable

### The Daemon

`daemon/cmd/dplaned` is a standard Go program. It compiles to a static binary with `CGO_ENABLED=0` and has no runtime dependency on NixOS, systemd, or any specific package manager. It runs on any Linux kernel that supports ZFS and has the required system tools available in `PATH`.

The daemon calls system tools via a strict exec allowlist - `zpool`, `zfs`, `docker`, `zfs`, `smartctl`, `ipmitool`, and others. As long as these binaries are present in `PATH`, the daemon functions.

```bash
# Build a portable static binary on any Linux with Go 1.22+
cd daemon
CGO_ENABLED=0 go build \
  -ldflags="-s -w -X main.Version=$(cat ../VERSION)" \
  -o ../build/dplaned ./cmd/dplaned/
```

### The Frontend

`app/` is a pre-built React SPA. It is served as static files by nginx. No changes are needed for non-NixOS targets.

### The PostgreSQL Schema

The daemon initialises its own schema on first connect. Any PostgreSQL 15+ instance (including managed cloud databases) works.

---

## What You Must Build

If you want DPlaneOS on Debian, Ubuntu, RHEL, or any other distribution, you are responsible for everything the NixOS appliance provides declaratively. That is roughly the following.

### 1. System Package Installation

The NixOS module (`nixos/module.nix`) declares all system dependencies. You must install equivalent packages through your distribution's package manager. The full list is in [DEPENDENCIES.md](DEPENDENCIES.md).

### 2. systemd Service Unit

A service unit for `dplaned` must be created and installed. The required capabilities are documented in `nixos/module.nix` under `serviceConfig`:

- `CAP_SYS_ADMIN`, `CAP_NET_ADMIN`, `CAP_DAC_READ_SEARCH`, `CAP_CHOWN`, `CAP_FOWNER`
- `ProtectSystem=strict` with explicit `ReadWritePaths`
- `PrivateTmp=true`, `NoNewPrivileges=true`

The existing `install/scripts/` directory contains scripts and unit files from an earlier pre-NixOS port that can serve as a starting point, with the caveat that they are not tested or maintained.

### 3. ZFS Kernel Module

Your distribution must provide a ZFS kernel module compatible with OpenZFS 2.3+ and your kernel version. On Debian/Ubuntu this is `zfsutils-linux` from the OpenZFS PPA. On RHEL/Rocky it requires the ELRepo kernel or DKMS build.

ZFS DKMS builds (rebuilding the module on every kernel update) are a common source of boot failures. The NixOS appliance avoids this by pinning the kernel and ZFS versions together as tested pairs.

### 4. nginx Configuration

nginx proxies `/api/` and `/ws` to `dplaned` on `127.0.0.1:9000` and serves `/opt/dplaneos/app/` as static files. WebSocket proxying requires `proxy_read_timeout 300s`. A working nginx configuration can be extracted from `nixos/module.nix` lines 202-225.

### 5. PostgreSQL Initialisation

The daemon expects a PostgreSQL instance with a `dplaneos` database and a `dplaneos` user. The daemon initialises the schema on first connect. Patroni for HA is optional but recommended for production.

### 6. An Upgrade Path

This is the hardest part. The NixOS appliance provides atomic upgrades with automatic rollback via the generation system. On a traditional distribution:

- There is no equivalent of NixOS generations
- Upgrades are `systemctl stop dplaned && replace binary && systemctl start dplaned`
- Rollback means keeping the previous binary and a restore procedure for any schema migrations
- A partially applied upgrade that fails mid-way leaves the system in an undefined state

You must design and test your own upgrade and rollback procedure.

### 7. Impermanence (Optional but Strongly Recommended)

The NixOS appliance mounts `/` as tmpfs and persists only declared paths. On a traditional distribution, accumulated state (stale config, rotated credentials, leftover temp files) builds up over time and creates support surface. There is no standard equivalent to NixOS impermanence on traditional distributions.

---

## What You Lose

| NixOS Appliance | Traditional Linux Fork |
|-----------------|----------------------|
| Atomic OS upgrades with automatic health-check rollback | Manual binary swap; rollback is a restore procedure |
| Entire system state in git | Packages and config diverge from declared state over time |
| Reproducible builds (ISO = local `nix build`) | Install script installs whatever versions are current on the mirror |
| Impermanent root - no boot-to-boot drift | State accumulates; drift between instances |
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

The closest starting point for a non-NixOS port is the existing `install/scripts/` directory, which contains scripts from an earlier pre-NixOS installation path. These scripts are not maintained or tested and may have significant gaps relative to the current daemon feature set, but they reflect the general shape of what a manual install requires.

For any questions about porting, open a discussion on the [GitHub repository](https://github.com/4nonX/DPlaneOS). Note that porting assistance is best-effort and not part of the project's core support scope.
