# DPlaneOS Dependencies

What DPlaneOS is made of. On an installed system there is nothing to install: everything below is part of the image, at the versions pinned in `flake.lock`.

---

## The daemon (Go)

Eleven direct dependencies, vendored in `daemon/vendor/` (the daemon builds without network access):

| Package | For |
|---|---|
| `github.com/gorilla/mux` | HTTP routing |
| `github.com/gorilla/websocket` | live events, terminal |
| `github.com/jackc/pgx/v5` | PostgreSQL |
| `github.com/pressly/goose/v3` | database migrations |
| `github.com/go-ldap/ldap/v3` | LDAP sign-in and directory sync |
| `github.com/go-jose/go-jose/v4` | OIDC: verifying ID tokens |
| `github.com/go-acme/lego/v4` | ACME certificates |
| `github.com/creack/pty` | the terminal |
| `github.com/google/uuid` | identifiers |
| `golang.org/x/crypto` | bcrypt, SSH |
| `golang.org/x/sys` | system calls (SCSI, netlink) |

The rest of `go.mod` are their dependencies. `govulncheck` runs against the whole set in CI.

The daemon is built with `CGO_ENABLED=0`: one static binary. (A build that links `libzfs` exists as an experiment, `dplaneos-daemon-cgo` in the flake; no image installs it. See the [Architecture Reference](ARCHITECTURE.md#zfs).)

## The web interface

Built ahead of time into `app/` (committed, and copied into the image as it is): static files, served by nginx.

| Package | For |
|---|---|
| `react`, `react-dom` | the interface |
| `@tanstack/react-router`, `@tanstack/react-query` | navigation, server state |
| `zustand` | client state |
| `@xterm/xterm` and add-ons | the terminal |
| `qrcode-generator` | the QR code for two-factor setup, drawn in the browser |
| `@fontsource-variable/*`, Material Symbols | fonts and icons, served by the system itself |

The interface loads nothing from other hosts.

## The system

From the pinned NixOS release, put together by `nixos/module.nix` and the modules next to it:

| | |
|---|---|
| Linux kernel (6.12 or newer), OpenZFS (2.3 or newer) | the build fails if the pair is not what is expected |
| PostgreSQL | state |
| nginx | web interface, TLS, proxy to the daemon |
| Samba (built with Active Directory support), nfs-utils | file sharing |
| the kernel's LIO and NVMe targets, `targetcli` | iSCSI, NVMe-oF |
| vsftpd, Versity Gateway | FTP, S3 |
| Docker, Docker Compose | containers |
| Corosync, corosync-qdevice | cluster membership and quorum |
| rclone, rsync, OpenSSH, pv | cloud sync, backups, replication |
| smartmontools, hdparm, nvme-cli, sg3_utils, lsscsi | disks |
| ipmitool, dmidecode, lm_sensors, NUT | BMC, hardware identity, sensors, UPS |
| git | configuration repositories |
| systemd (networkd, resolved, timesyncd, boot) | network, time, boot loader |

## Building

Everything is built through the flake ([the NixOS side](../../nixos/README.md)): `nix build .#iso`. A development shell with the pinned Go and Node.js: `nix develop`.

By hand:

```bash
# Daemon (the Go version named in daemon/go.mod)
cd daemon && CGO_ENABLED=0 go build -mod=vendor -o ../build/dplaned ./cmd/dplaned/

# Web interface (Node.js 20 or newer); the result goes to ../app/
cd app-react && npm ci && npm run build
```

`app/` is committed: a change to `app-react/` is not in an image until the interface has been rebuilt and the result committed with it. CI fails when the two do not match.
