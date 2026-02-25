# D-PlaneOS v3.2.0 — networkd Persistence

**Release date:** 2026-02-21  
**Upgrade from:** v3.1.0 or v3.0.0  
**Status:** Current stable release ✅

---

## What's new

### Architecture: systemd-networkd file writer (networkdwriter)

The central change in this release eliminates the last remaining "flicker" between the web UI and the host OS. Network changes made in the UI now survive reboots **and** `nixos-rebuild switch` with zero extra steps.

**How it works:**  
D-PlaneOS writes `/etc/systemd/network/50-dplane-*.{network,netdev}` files directly.  
`networkctl reload` applies changes live in < 1 second with zero downtime.  
NixOS activation only manages files it created (prefixes `10-`, `20-`). D-PlaneOS files at prefix `50-` are never touched by `nixos-rebuild`.

This approach works on every systemd-based distribution: NixOS, Debian, Ubuntu, Arch.

**Scope of nixwriter reduced** — nixwriter is now only called for NixOS-specific settings that have no systemd equivalent:
- Firewall ports (`networking.firewall`)
- Samba globals (`services.samba`)

hostname, timezone, and NTP were already persistent via OS tools (`hostnamectl`, `timedatectl`, `timesyncd.conf`) — no action needed.

### Completeness fixes

- All 12 `nixwriter` methods fully wired; all handler stanzas covered
- New `POST /api/firewall/sync` endpoint for explicit firewall port persistence
- DNS: `SetGlobalDNS` writes `/etc/systemd/resolved.conf.d/50-dplane.conf`; POST handler added
- `HandleSettings` runtime POST wires hostname + timezone persist calls
- `/etc/systemd/network` and `/etc/systemd/resolved.conf.d` added to `ReadWritePaths` in `module.nix`

### Documentation

- Version references corrected throughout all documentation files (CHANGELOG, DEPENDENCIES, INSTALLATION-GUIDE, TROUBLESHOOTING, and others contained stale v4.x/v5.x references — all fixed)
- `CHANGELOG.md` now covers the full history from v1.2.0 through v3.2.0

---

## Upgrade notes

**From v3.1.0:** drop-in upgrade. `sudo ./install.sh --upgrade` on Debian/Ubuntu; `nixos-rebuild switch` on NixOS. All data and configuration preserved.

**From v3.0.0:** same as above. The nixwriter intermediary (v3.1.0) is not required.

**NixOS users:** `/etc/systemd/network` must be in `ReadWritePaths` for the daemon service. This is included in `module.nix` — no manual action needed if you use the provided NixOS module.

---

## Downloads

| File | Description |
|------|-------------|
| `dplaneos-v3.2.0.tar.gz` | Full source + pre-built amd64 binary |
| `dplaned-v3.2.0-linux-amd64` | Pre-built binary only |

SHA-256 checksums are listed below each asset.
