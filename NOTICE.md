# NOTICE

DPlaneOS
Copyright (C) 2024-2026 Dan Dressen

This product is licensed under the GNU Affero General Public License v3.0. See [LICENSE](LICENSE) for the full text.

This product includes, depends on, or distributes alongside the following third-party software. All components are listed with their upstream project and license. License texts for vendored Go dependencies are preserved in `daemon/vendor/<module>/LICENSE`. Frontend dependency license texts are preserved in the lockfile manifests and reproduced by the build process.

If you redistribute DPlaneOS, in whole or in part, you must preserve this notice.

---

## System services (provided by NixOS, not linked into the daemon)

These projects are installed as separate processes by the NixOS module and communicated with over their respective interfaces (sockets, files, command-line). DPlaneOS does not statically or dynamically link against any of them in its default build.

| Project | License | Upstream |
|---------|---------|----------|
| NixOS / Nixpkgs | MIT | https://github.com/NixOS/nixpkgs |
| OpenZFS | CDDL-1.0 (with file-level exceptions; see `THIRDPARTYLICENSE.*` upstream) | https://github.com/openzfs/zfs |
| PostgreSQL | PostgreSQL License (BSD-style) | https://www.postgresql.org |
| nginx | BSD-2-Clause | https://nginx.org |
| HAProxy | GPL-2.0-or-later (exportable headers under LGPL-2.1-or-later) | https://www.haproxy.org |
| Patroni | MIT | https://github.com/patroni/patroni |
| etcd | Apache-2.0 | https://github.com/etcd-io/etcd |
| Keepalived | GPL-2.0-or-later | https://www.keepalived.org |
| Samba | GPL-3.0-or-later | https://www.samba.org |
| Docker (moby) | Apache-2.0 | https://github.com/moby/moby |
| nfs-utils | Mixed: GPL-2.0-only AND GPL-2.0-or-later AND BSD-3-Clause AND BSD-2-Clause AND others (per Fedora package metadata) | https://git.kernel.org/pub/scm/utils/nfs-utils/nfs-utils.git |
| nfs4-acl-tools | Mixed: GPL-2.0 / BSD / LGPL-2.1-or-later (varies by file; see upstream COPYING) | https://git.linux-nfs.org/?p=steved/nfs4-acl-tools.git |
| rclone | MIT | https://github.com/rclone/rclone |
| Avahi | LGPL-2.1-or-later | https://www.avahi.org |
| smartmontools | GPL-2.0-or-later | https://www.smartmontools.org |
| ipmitool | BSD-3-Clause | https://github.com/ipmitool/ipmitool |
| targetcli-fb | Apache-2.0 | https://github.com/open-iscsi/targetcli-fb |
| sg3_utils | Mixed: BSD-2-Clause (libsgutils) / GPL-2.0-or-later (older utilities) | https://sg.danny.cz/sg/sg3_utils.html |
| OpenSSH | BSD-style (with some additional permissive components) | https://www.openssh.com |
| Git | GPL-2.0-only (explicitly not "or later") | https://git-scm.com |

Optional NUT (Network UPS Tools, primarily GPL-2.0-or-later with some GPL-3.0-or-later and Artistic-1.0 components, https://networkupstools.org) integration is supported but disabled in the default `configuration.nix`.

### A note on OpenZFS

The default DPlaneOS daemon build invokes the OpenZFS CLI tools (`zfs`, `zpool`) as separate processes and parses their output. It does not link against `libzfs`. This avoids the well-known CDDL/GPL linking question.

An optional CGO build target (`mkDaemonCGO` in `flake.nix`) dynamically links against `libzfs` for performance-sensitive paths. This build is intended for local installation by end users on their own systems, where the binary is compiled against the system-provided `libzfs`. It is not distributed as a pre-built binary.

---

## Go dependencies (vendored into `daemon/vendor/`)

### Direct

| Module | License | Upstream |
|--------|---------|----------|
| github.com/gorilla/mux | BSD-3-Clause | https://github.com/gorilla/mux |
| github.com/gorilla/websocket | BSD-3-Clause | https://github.com/gorilla/websocket |
| github.com/jackc/pgx/v5 | MIT | https://github.com/jackc/pgx |
| github.com/go-ldap/ldap/v3 | MIT | https://github.com/go-ldap/ldap |
| github.com/go-acme/lego/v4 | MIT | https://github.com/go-acme/lego |
| github.com/creack/pty | MIT | https://github.com/creack/pty |
| github.com/google/uuid | BSD-3-Clause | https://github.com/google/uuid |
| golang.org/x/crypto | BSD-3-Clause | https://cs.opensource.google/go/x/crypto |

### Indirect

| Module | License | Upstream |
|--------|---------|----------|
| github.com/Azure/go-ntlmssp | MIT | https://github.com/Azure/go-ntlmssp |
| github.com/cenkalti/backoff/v5 | MIT | https://github.com/cenkalti/backoff |
| github.com/go-asn1-ber/asn1-ber | MIT | https://github.com/go-asn1-ber/asn1-ber |
| github.com/go-jose/go-jose/v4 | Apache-2.0 | https://github.com/go-jose/go-jose |
| github.com/jackc/pgpassfile | MIT | https://github.com/jackc/pgpassfile |
| github.com/jackc/pgservicefile | MIT | https://github.com/jackc/pgservicefile |
| github.com/jackc/puddle/v2 | MIT | https://github.com/jackc/puddle |
| github.com/mfridman/interpolate | MIT | https://github.com/mfridman/interpolate |
| github.com/miekg/dns | BSD-3-Clause | https://github.com/miekg/dns |
| github.com/pressly/goose/v3 | MIT | https://github.com/pressly/goose |
| github.com/sethvargo/go-retry | Apache-2.0 | https://github.com/sethvargo/go-retry |
| go.uber.org/multierr | MIT | https://github.com/uber-go/multierr |
| golang.org/x/mod | BSD-3-Clause | https://cs.opensource.google/go/x/mod |
| golang.org/x/net | BSD-3-Clause | https://cs.opensource.google/go/x/net |
| golang.org/x/sync | BSD-3-Clause | https://cs.opensource.google/go/x/sync |
| golang.org/x/sys | BSD-3-Clause | https://cs.opensource.google/go/x/sys |
| golang.org/x/text | BSD-3-Clause | https://cs.opensource.google/go/x/text |
| golang.org/x/tools | BSD-3-Clause | https://cs.opensource.google/go/x/tools |

Apache-2.0 NOTICE preservation: the Apache-2.0-licensed dependencies above (`go-jose/v4` and `sethvargo/go-retry`) do not ship `NOTICE` files in their upstream repositories. If any future Apache-2.0 dependency includes a `NOTICE` file, it will be preserved in `daemon/vendor/<module>/` per Apache-2.0 § 4.

---

## Frontend dependencies

### Runtime

| Package | License | Upstream |
|---------|---------|----------|
| react | MIT | https://github.com/facebook/react |
| react-dom | MIT | https://github.com/facebook/react |
| @tanstack/react-router | MIT | https://github.com/TanStack/router |
| @tanstack/react-query | MIT | https://github.com/TanStack/query |
| zustand | MIT | https://github.com/pmndrs/zustand |
| @xterm/xterm | MIT | https://github.com/xtermjs/xterm.js |
| @xterm/addon-fit | MIT | https://github.com/xtermjs/xterm.js |
| @xterm/addon-web-links | MIT | https://github.com/xtermjs/xterm.js |

### Build tooling

| Package | License | Upstream |
|---------|---------|----------|
| vite | MIT | https://github.com/vitejs/vite |
| @vitejs/plugin-react | MIT | https://github.com/vitejs/vite-plugin-react |
| typescript | Apache-2.0 | https://github.com/microsoft/TypeScript |

### Fonts and icons

| Asset | License | Upstream |
|-------|---------|----------|
| Outfit (variable) | SIL Open Font License 1.1 | https://github.com/Outfitio/Outfit-Fonts |
| JetBrains Mono (variable) | SIL Open Font License 1.1 | https://github.com/JetBrains/JetBrainsMono |
| Material Symbols Rounded | Apache-2.0 | https://github.com/google/material-design-icons |
| @fontsource-variable/outfit (packaging) | OFL-1.1 | https://github.com/fontsource/fontsource |
| @fontsource-variable/jetbrains-mono (packaging) | OFL-1.1 | https://github.com/fontsource/fontsource |

---

## License compatibility note

All third-party components listed above are licensed under terms compatible with the AGPLv3 distribution of DPlaneOS. Permissive licenses (MIT, BSD, Apache-2.0, OFL-1.1) flow into AGPL without restriction. GPL-2.0-or-later and GPL-3.0 system services run as independent processes and are not subject to combined-work analysis. The CDDL-licensed OpenZFS is invoked via separate-process CLI in the default build.

If you believe a component is incorrectly attributed or licensed, please open an issue at https://github.com/4nonX/DPlaneOS/issues.
