# D-PlaneOS — Dependencies

All internal dependencies are bundled in the release tarball. Only system-level tools need to be installed separately.

---

## Bundled (no installation required)

### Frontend — `app/`

The React SPA is pre-built. No Node.js or npm required at runtime.

| Asset | Description |
|-------|-------------|
| `app/index.html` | SPA entry point |
| `app/assets/index-*.js` | Application bundle (React 19, TanStack Router, TanStack Query, Zustand) |
| `app/assets/react-vendor-*.js` | React runtime |
| `app/assets/tanstack-*.js` | Router + Query split chunks |
| `app/assets/zustand-*.js` | State management |
| `app/assets/index-*.css` | Compiled styles |
| `app/assets/fonts/outfit.woff2` | UI font (variable, 100–900) |
| `app/assets/fonts/jetbrains-mono.woff2` | Monospace font (variable, 100–900) |
| `app/assets/fonts/MaterialSymbolsRounded.woff2` | Icon font (5MB variable) |
| `app/assets/fonts/fonts.css` | Font-face declarations |

**Zero external requests at runtime** — all fonts and assets are local. No CDN dependencies.

### Daemon Go vendor — `daemon/vendor/`

All Go dependencies are vendored. No internet access needed to build.

| Package | Purpose |
|---------|---------|
| `github.com/gorilla/mux` | HTTP router |
| `github.com/gorilla/websocket` | WebSocket (real-time monitor) |
| `github.com/mattn/go-sqlite3` | SQLite driver (CGO) |
| `github.com/google/uuid` | Job store UUIDs |
| `github.com/go-ldap/ldap/v3` | LDAP/AD integration |
| `github.com/Azure/go-ntlmssp` | NTLM auth (LDAP) |
| `golang.org/x/crypto` | bcrypt password hashing |

---

## System Dependencies

### Required

These must be present on the target system. `install.sh` installs them automatically on Debian/Ubuntu and RHEL-family systems.

| Dependency | Purpose | Debian/Ubuntu | RHEL/Rocky |
|------------|---------|---------------|------------|
| `nginx` | Reverse proxy / static file server | `apt install nginx` | `dnf install nginx` |
| `zfsutils-linux` | ZFS pool and dataset management | `apt install zfsutils-linux` | `dnf install zfs` |
| `gcc` / `build-essential` | CGO build of SQLite | `apt install build-essential` | `dnf install gcc` |

### Optional (feature-dependent)

| Dependency | Feature | Install |
|------------|---------|---------|
| `docker` + `docker compose` | Container management | `curl -fsSL https://get.docker.com \| sh` |
| `ipmitool` | IPMI / BMC monitoring | `apt install ipmitool` |
| `rclone` | Cloud sync | `curl https://rclone.org/install.sh \| bash` |
| `samba` | SMB/CIFS shares | `apt install samba` |
| `nfs-kernel-server` | NFS exports | `apt install nfs-kernel-server` |
| `avahi-daemon` | mDNS / Bonjour discovery | `apt install avahi-daemon` |
| `nut` | UPS monitoring | `apt install nut` |

### Build-time only (not needed on the NAS)

| Dependency | Purpose |
|------------|---------|
| Go 1.22+ | Rebuild the daemon from source |
| Node.js 20+ + npm | Rebuild the frontend from source |
| gcc / CGO | Required when building `go-sqlite3` |

Pre-built binaries are included in the release tarball — you do not need Go or Node.js to install or run D-PlaneOS.

---

## Dependency Check

```bash
#!/bin/bash
# Quick check of runtime dependencies

echo "=== D-PlaneOS Dependency Check ==="

check() {
    local name=$1; local cmd=$2
    printf "%-20s" "$name:"
    if command -v $cmd &>/dev/null; then echo "✓"; else echo "✗ missing"; fi
}

check_service() {
    local name=$1; local svc=$2
    printf "%-20s" "$name:"
    systemctl is-active --quiet $svc 2>/dev/null && echo "✓ running" || echo "⚠ not running"
}

check "nginx"         nginx
check "zpool (ZFS)"   zpool
check "docker"        docker
check "ipmitool"      ipmitool
check "rclone"        rclone
check "samba"         smbd
check "nfs"           exportfs

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
