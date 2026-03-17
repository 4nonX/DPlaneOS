# D-PlaneOS Dependencies

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
| `github.com/mattn/go-sqlite3` | SQLite driver (CGO) |
| `github.com/google/uuid` | Job store UUIDs |
| `github.com/go-ldap/ldap/v3` | LDAP / Active Directory integration |
| `github.com/Azure/go-ntlmssp` | NTLM authentication (LDAP) |
| `golang.org/x/crypto` | bcrypt password hashing |

---

## System Dependencies

### Required

Installed automatically by `install.sh` on Debian/Ubuntu.

| Dependency | Purpose |
|------------|---------|
| `nginx` | Reverse proxy and static file server |
| `zfsutils-linux` | ZFS pool and dataset management |
| `sqlite3` | Database CLI (used by installer for schema init) |
| `gcc` / `build-essential` | CGO compilation of SQLite driver |
| `musl-tools` | Optional - enables fully static binary build |
| `smartmontools` | S.M.A.R.T. disk health monitoring |
| `udev` | Device event rules (hot-swap, removable media) |
| `samba` | SMB / CIFS shares and AFP / Time Machine |
| `nfs-kernel-server` | NFS exports |
| `avahi-daemon` | mDNS - makes the NAS visible as `hostname.local` |

### Optional (feature-dependent)

| Dependency | Feature | Install |
|------------|---------|---------|
| `docker` + `docker compose` | Container management | `curl -fsSL https://get.docker.com \| sh` |
| `ipmitool` | IPMI / BMC monitoring | `apt install ipmitool` |
| `rclone` | Cloud sync | `curl https://rclone.org/install.sh \| bash` |
| `targetcli-fb` | iSCSI block targets | `apt install targetcli-fb` |
| `nut` | UPS monitoring | `apt install nut` |

### Build-time Only (not needed on the NAS)

| Dependency | Purpose |
|------------|---------|
| Go 1.22+ | Rebuild the daemon from source |
| Node.js 20+ | Rebuild the frontend from source |
| gcc / CGO | Required when building `go-sqlite3` |

Pre-built binaries are included in the release tarball. Go and Node.js are not needed to install or run D-PlaneOS.

---

## Dependency Check

```bash
#!/bin/bash
echo "=== D-PlaneOS Dependency Check ==="

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
check "sqlite3"      sqlite3
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
go build -mod=vendor -tags "sqlite_fts5" \
  -ldflags "-s -w -X main.Version=$(cat ../VERSION)" \
  -o ../build/dplaned ./cmd/dplaned/

# Frontend (requires Node.js 20+ and npm)
cd app-react
npm install
npm run build
# Output goes to ../app/
```
