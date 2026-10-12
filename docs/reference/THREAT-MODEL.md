# DPlaneOS Threat Model

What the system defends, against whom, by what means, and what it does not defend. Statements here are about the installed system (the image); where something is checked by the automated tests of every release, that is said.

## The system

One machine: the kernel with ZFS, and the services of a NAS (Samba, NFS, the iSCSI and NVMe targets, Docker, SSH). They are managed by one program, `dplaned`, which runs as root. The web interface is static files served by nginx; nginx passes API requests to the daemon over a Unix socket. The daemon keeps its state in a PostgreSQL database on the same machine, reached over another Unix socket.

```
 untrusted                                  trusted (root on the machine)
 ───────────                                ──────────────────────────────
 browser ── HTTPS ──▶ nginx ── socket ──▶ dplaned ──▶ PostgreSQL (socket)
 SMB / NFS / iSCSI / NVMe / FTP / S3 clients ──▶ the services themselves
 other nodes of a cluster ── HTTPS, pinned certificates ──▶ dplaned
```

**The trust boundary is the daemon's API.** Whoever is root on the machine, or can write to the database, has everything; nothing below tries to defend against that.

## What is worth protecting

| | Where |
|---|---|
| The data | ZFS pools |
| Accounts: password hashes, TOTP secrets, API tokens (hashes), sessions | database |
| Secrets of integrations: LDAP bind password, OIDC client secret, SMTP password, Telegram token, Git credentials, the cluster's key | database, encrypted with a key kept in a file next to it (`/var/lib/dplaneos`) |
| CHAP secrets of iSCSI initiators | a root-only file and the database, not encrypted (the kernel holds them in clear as well) |
| The audit log | database, each entry chained to the one before with an HMAC; the key is a file |
| The release key's public half | part of the system image |

## Who attacks

| | Can | Wants |
|---|---|---|
| Anyone on the network, not signed in | reach the web interface and the file services | data, a foothold |
| A signed-in user with few rights | call the API as themselves | more rights, other users' data |
| A website the administrator visits | make the browser send requests | actions in the administrator's name |
| Someone with the hardware | take disks, start another system | data |
| A container | whatever its compose file gave it | the host |
| A node of the cluster that is cut off, or a stale one | still write to shared disks | (no intent: corruption) |

---

## Sign-in and sessions

- Passwords are stored as bcrypt hashes. The password rules (length, character classes) are enforced by the server, in the installer too.
- **Failed sign-ins are throttled per client address**: after each failure the address waits longer (up to 30 seconds per attempt); other addresses are not affected. A burst of failures raises an alert. (Tested: appliance.)
- A session is a random 32-byte token sent in a request header, not a cookie: a browser does not attach it to requests another site triggers. Changing requests also need a CSRF token bound to the session.
- Sessions end after 24 hours, and after 30 minutes without use. Changing a password ends the user's other sessions; an administrator's reset ends all of them.
- **Two-factor sign-in (TOTP)**: with it enabled, the password alone gives a session that can do nothing but present the code. Secrets are stored encrypted; backup codes as hashes. The QR code for the authenticator app is drawn in the browser; the secret is sent nowhere.
- **Operations with a large blast radius need a session that has presented a second factor**: destroying a pool, resetting another user's password, configuring fencing and the watchdog, enrolling and using a BMC. Without TOTP enabled on the account these are refused.
- Destroying or exporting a pool, destroying a volume and removing containers and images need a confirmation token: single use, valid 60 seconds, bound to the user, the operation and its target.
- **API tokens** are stored as SHA-256 hashes, carry a scope (read, write, admin), and can be limited to paths.
- **Directory users (LDAP)** sign in against the directory; their roles follow their groups. **OIDC**: Authorization Code with PKCE; the ID token's signature, issuer, audience, expiry and nonce are checked; an existing account is linked only by an e-mail address the provider marks as verified; a disabled account stays out. (Tested against a simulated provider.)
- A lost administrator password is reset from the installation medium, which needs the machine itself.

*Not defended:* session tokens are stored in the database as issued, not hashed: whoever can read the database can use the sessions that are active at that moment.

## Rights

Every route of the API carries the permission it needs (`resource:action`); four built-in roles and custom roles map users to permissions; assignments can have an end date. Roles given by a directory or an identity provider are taken away when the group membership ends.

*Not defended:* an administrator of the web interface is root: the terminal, containers with host mounts, and SSH (administrators may sign in with their key and use `sudo`) are part of the role.

## Commands

The daemon manages the system by running programs. It never runs a shell with text it was given:

- every program it may run, with the sub-commands and the shape of each argument, is on an allowlist (`internal/security/whitelist.go`); arguments are passed as a list, not as a line;
- names from requests (pools, datasets, snapshots, shares, users, interfaces) are validated before they reach a command, and may not begin with `-`;
- paths from requests are cleaned and must lie under the allowed roots (pools, the stacks directory); symbolic links out of them are refused where files are read or written for a user.

`targetcli` (iSCSI) is not on the general allowlist; the package that runs it checks its arguments itself.

## The web interface

- nginx sends a Content-Security-Policy that allows scripts from the system itself only, forbids framing by other sites, and sends `X-Content-Type-Options: nosniff` and `Referrer-Policy: no-referrer`. (Tested: appliance.) The interface is a React application: text from the server is inserted as text, not as HTML.
- The pages load nothing from other hosts: fonts and icons are part of the system. A container icon given as a URL is the one exception, and the administrator's choice.
- HTTPS from the first start, with a self-signed certificate until one is installed (your own, or from an ACME certificate authority). HTTP stays available. No `Strict-Transport-Security` header is sent: with a self-signed certificate it would lock the browser out.

*Not defended:* until a trusted certificate is installed, the browser cannot tell the system from someone in the middle; the first warning has to be accepted on trust. Styles are inline (`style-src 'unsafe-inline'`).

## The network

- The firewall is on from the first start and lets in the web interface, SSH, SMB, NFS and mDNS; every other service opens its port when it is switched on and closes it again. Published container ports bypass it (Docker filters for itself).
- SSH: keys only, no root sign-in, unless changed in the web interface.
- The API is limited to 100 requests per minute per client address.
- SMB: users of the web interface with their own password; NFS: by client network, as NFS is; FTP: off, plain or TLS; S3: off, one account, HTTP; iSCSI: per initiator, CHAP on by default; NVMe over TCP: per host name only (no secrets).

*Not defended:* NFS exports, NVMe-oF exports for "any host", iSCSI targets without CHAP and plain FTP trust the network they are offered on. A signed-in user can start expensive operations (scrubs, image pulls) within the request limit.

## Data at rest

ZFS native encryption per dataset, with lock, unlock and change of key in the web interface. A dataset that is locked is unreadable until its key is given again.

*Not defended:* datasets that are not encrypted are readable from the disks. The system disk is not encrypted: the database (with the secrets above and the file that holds their key) is readable by whoever has that disk.

## The system itself, and updates

- The system is an image: its programs live in a read-only store; a restart returns everything outside the persistent state to what the image says.
- An update is a file signed with the release key (Ed25519). The system verifies the signature against the public key in its own image before anything of it is used, and refuses a file for another machine type. A new version runs once on trial and is removed if the system is not healthy with it. (Tested: update.)
- Release images are built by CI from the tagged source, signed with cosign, and accompanied by an SBOM; `govulncheck` runs for every build.
- The installer needs no network and starts no SSH unless asked at its console; it refuses weak administrator passwords.

*Not defended:* the daemon is one root process with all capabilities. It is not sandboxed by systemd (it imports pools, mounts datasets, configures the network and the firewall): a flaw that gives code execution in it gives the machine.

## Containers

DPlaneOS deploys what a compose file says. A container that is given a host path, the Docker socket, `privileged` or host networking has what it was given.

*Not defended:* containers are not confined beyond Docker's defaults, and their published ports are not subject to the firewall page.

## The audit log

Sign-ins and the changes made through the API are recorded with the user and the client address. Each entry carries an HMAC over itself and the one before it; the check on the Security Settings page (`GET /api/system/audit/verify-chain`) finds entries that were changed or removed.

*Not defended:* whoever has both the database and the key file can forge a consistent log. Send alerts or logs off the machine if that matters to you.

## Clusters

- Nodes are paired with a one-time code; afterwards they recognise each other by pinned certificates and exchange configuration, including secrets, over HTTPS.
- **Two nodes must never write to the same pool.** In layers: only a node of the quorate partition takes a group over; a storage owner that loses quorum stops feeding its watchdog and is reset before anyone takes over; pools of shared groups carry ZFS multihost, which refuses an import while another host is writing; where the disks support SCSI-3 persistent reservations, the new owner takes the disks first, and the old owner's writes are then refused by the disks themselves; power fencing (IPMI, a PDU) switches the old owner off where configured. Two nodes without a third vote never fail over by themselves. (Tested: quorum, failover, reservations, on virtual hardware.)
- Every change of owner raises the group's epoch; a node that returns with an older one follows.

*Not defended:* a cluster without a hardware watchdog, without multihost and without reservations has only quorum between it and a double import; the HA page says which layers are active. Reservations and failover are tested on virtual disks and iSCSI LUNs, not on physical SAS shelves by the project ([HA Hardware Matrix](HA-HARDWARE-MATRIX.md)).

---

## Recommended

- Install a trusted certificate, and reach the web interface from a management network or a VPN; do not put it on the internet.
- Enable two-factor sign-in for every administrator.
- Encrypt datasets whose disks could leave the building.
- Put iSCSI, NVMe-oF and NFS on a storage network.
- Keep alerts going somewhere outside the machine.

## Reporting a vulnerability

See [Security Policy](../../SECURITY.md).
