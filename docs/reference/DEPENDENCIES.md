# DPlaneOS Dependencies

All Go and frontend dependencies are bundled in the release tarball. Only system-level tools need to be installed separately.

---

## Bundled (no installation required)

### Frontend - `app/`

The React SPA is pre-built. No Node.js or npm is required at runtime.

| Asset | Description |
|-------|-------------|
| `app/index.html` | SPA entry point |
| `app/assets/index-*.js` | Application bundle (React 19, TanStack Router, TanStack Query, Zustand) |
| `app/assets/tanstack-*.js` | Router and Query split chunks |
| `app/assets/zustand-*.js` | State management |
| `app/assets/index-*.css` | Compiled styles |
| `app/assets/fonts/outfit.woff2` | UI font (variable, 100–900 weight) |
| `app/assets/fonts/jetbrains-mono.woff2` | Monospace font (variable, 100–900 weight) |
| `app/assets/fonts/MaterialSymbolsRounded.woff2` | Icon font (variable) |
| `app/assets/fonts/fonts.css` | Font-face declarations |

Zero external requests at runtime. All fonts and assets are local - no CDN dependencies.

### Go Vendor - `daemon/vendor/`

All Go dependencies are vendored. No internet access is needed to build.

| Package | Purpose |
|---------|---------|
| `github.com/gorilla/mux` | HTTP router |
| `github.com/gorilla/websocket` | WebSocket (real-time monitor) |
| `github.com/jackc/pgx/v5` | PostgreSQL driver (pure Go) |
| `github.com/google/uuid` | Job store UUIDs |
| `github.com/go-ldap/ldap/v3` | LDAP / Active Directory integration |
| `github.com/Azure/go-ntlmssp` | NTLM authentication (LDAP) |
| `golang.org/x/crypto` | bcrypt password hashing |

---

## System Dependencies

All system-level dependencies are declared in the NixOS module (`nixos/module.nix`) and provisioned automatically at build time. No manual package installation is required or supported.

### Provided by the NixOS module

| Dependency | Purpose |
|------------|---------|
| `nginx` | Reverse proxy and static file server |
| `zfs` | ZFS pool and dataset management (kernel module + CLI) |
| `postgresql` | Database and CLI (`psql`) |
| `patroni`, `etcd` | HA cluster management |
| `smartmontools` | S.M.A.R.T. disk health monitoring |
| `udev` (systemd) | Device event rules (hot-swap, removable media) |
| `samba` | SMB / CIFS shares and AFP / Time Machine |
| `nfs-utils` | NFS exports (`exportfs`) |
| `nfs4-acl-tools` | NFSv4 ACL management (`nfs4_getfacl`, `nfs4_setfacl`) |
| `docker` + `docker-compose` | Container management |
| `ipmitool` | IPMI / BMC monitoring |
| `rclone` + `fuse3` | Cloud sync and Cold Tier FUSE mounts |
| `targetcli-fb` | iSCSI block targets |
| `openssh` | Remote access |
| `git` | GitOps repository sync |
| `sg3_utils` | SCSI-3 diagnostic tools (optional; `sg_persist` for reservation verification) |

All packages are pinned via the flake lockfile. Versions are reproducible across every build.

### Build-time Only (not needed on the NAS)

| Dependency | Purpose |
|------------|---------|
| Go 1.22+ | Rebuild the daemon from source |
| Node.js 20+ | Rebuild the frontend from source |
| gcc / CGO | Required for the cgo (`libzfs`) build path |
| `libzfs.h` + `pkg-config` + `pkgs.zfs` | Required for `dplaneos-daemon-cgo` build target only |

Pre-built binaries are included in the release tarball. Go and Node.js are not needed to install or run DPlaneOS.

### Two build variants

The standard production build (`dplaneos-daemon`) uses a static musl binary with CGO disabled. ZFS operations use subprocess calls through the exec allowlist. This binary has no runtime dependency on `libzfs.so`.

The cgo build (`dplaneos-daemon-cgo`) links against `libzfs.so` at runtime and calls ZFS operations natively via `libzfs.h`. It requires `gcc`, `pkg-config`, and `pkgs.zfs` at build time and produces a glibc-linked binary. Enable with:

```bash
nix build .#dplaneos-daemon-cgo
```

Both variants expose identical APIs. The build tag `libzfs` controls which path is compiled:

```bash
# Explicit cgo build
go build -tags "linux cgo libzfs" -mod=vendor ./cmd/dplaned/

# Standard static build (default)
CGO_ENABLED=0 go build -mod=vendor ./cmd/dplaned/
```

---

## Dependency Check

```bash
#!/bin/bash
echo "=== DPlaneOS Dependency Check ==="

check() {
    printf "%-20s" "$1:"
    command -v $2 &>/dev/null && echo "ok" || echo "missing"
}

check_service() {
    printf "%-20s" "$1:"
    systemctl is-active --quiet $2 2>/dev/null && echo "running" || echo "not running"
}

check "nginx"        nginx
check "zpool (ZFS)"  zpool
check "psql (Postgres)" psql
check "docker"       docker
check "smartctl"     smartctl
check "ipmitool"     ipmitool
check "rclone"       rclone
check "samba"        smbd
check "nfs"          exportfs

echo ""
check_service "dplaned"  dplaned
check_service "nginx"    nginx
check_service "docker"   docker
```

---

## Building from Source

Only needed if you want to modify the daemon or frontend.

```bash
# Daemon (requires Go 1.22+ and gcc)
cd daemon
go build -mod=vendor \
  -ldflags "-s -w -X main.Version=$(cat ../VERSION)" \
  -o ../build/dplaned ./cmd/dplaned/

# Frontend (requires Node.js 20+ and npm)
cd app-react
npm install
npm run build
# Output goes to ../app/
```
