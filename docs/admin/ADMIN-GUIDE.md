# DPlaneOS Administrator Guide

Day-to-day administration, by area of the web interface. Each area's deeper guide is linked where there is one.

| | |
|---|---|
| Installing, first steps | [Installation Guide](INSTALLATION-GUIDE.md) |
| Snapshots, replication, cloud sync, rsync | [Backup and Replication](BACKUP-REPLICATION.md) |
| iSCSI, NVMe-oF, FTP, S3 | [Optional Protocols](OPTIONAL-PROTOCOLS.md) |
| Alerts and their channels, two-factor sign-in | [Alerts and Authentication](ALERTS.md) |
| Updates | [System Updates](OTA-UPDATES.md) |
| Clusters | [High Availability](HIGH-AVAILABILITY.md) |
| Configuration in Git | [Git-Driven NAS](GITOPS-DRIVEN-NAS.md) |
| When something is wrong | [Troubleshooting](TROUBLESHOOTING.md), [Recovery](RECOVERY.md) |

---

## Users, groups and roles

**Identity › Users & Groups.**

### Users

A user of the web interface is also the Unix, SMB and FTP account of the same name, with the same password: files a user writes over a share belong to that user, and group membership decides access to folders.

- **Create**: name, password (at least 8 characters, with upper and lower case, a digit and a special character), optionally an e-mail address. Then give the user a role.
- **Reset a password**: sets a temporary password. The user's sessions end, and at the next sign-in the user can do nothing but choose a new one. Resetting another user's password requires that your own account signs in with a second factor (Security Settings › 2FA): without one the request is refused.
- **Delete**: removes the account, its role assignments and its sessions. The last active administrator cannot be deleted. Files the user owned stay, owned by a number without a name.
- Users from a directory or from single sign-on have no password here; it is changed where the account lives.

### Groups

Groups are Unix groups: use them for the permissions of shared folders (owner group of a dataset, entries in ACLs, *valid users* of an SMB share).

### Roles

A role is a set of permissions for the web interface and the API.

| Role | |
|---|---|
| **Admin** | everything, including users, roles and system settings. An administrator is root on the machine (terminal, containers, `sudo` over SSH) |
| **Operator** | storage, shares, containers, files; not users, roles or system settings |
| **Viewer** | reads everything, changes nothing |
| **User** | the file explorer and their own account |

The built-in roles cannot be changed. **Roles › Create role** makes your own from the permission list (`resource:action`, for example `storage:write`, `shares:admin`, `docker:read`). An assignment can have an end date, after which it lapses by itself. Changes take effect at once.

### Sessions

The **Sessions** tab lists who is signed in from where, and ends sessions. A session ends by itself after 24 hours, or after 30 minutes without use.

### Your own account

**Security › Security Settings**: change your password, enable two-factor sign-in, create API tokens (with a scope: read, write or admin), end your other sessions, and check the audit log's integrity.

---

## Storage

### Pools

**Storage › ZFS Pools › Create Pool**: choose the disks and the layout.

| Layout | Disks | Survives |
|---|---|---|
| Mirror | 2 or more | all but one disk of the mirror |
| RAID-Z1 | 3 or more | one disk |
| RAID-Z2 | 4 or more | two disks |
| RAID-Z3 | 5 or more | three disks |

Disks are used by their stable names (`/dev/disk/by-id`), so a pool does not depend on the order in which the system finds them. Pools from another system appear under *Import*.

The pool's page has the rest: **Scrub Schedules** (a scrub reads everything and repairs what does not match its checksum; monthly is usual), TRIM, replacing and adding disks, clearing error counters, checkpoints, encryption, and for clusters Multihost.

**A failed disk**: the pool is shown as degraded and an alert is sent. Replace the disk (hot, where the hardware allows), then use *Replace* on the failed device and choose the new disk; the pool rebuilds itself while it stays in use.

**Destroying a pool** asks for its name, needs an account with a second factor, and is refused while shares, exports, targets or stacks still use it.

### Datasets

**Storage › Datasets**: one dataset per kind of data (per share, per application), so that each can have its own snapshots, quota, compression and encryption. Compression (`lz4`) is on by default and costs nothing noticeable. A quota below what the dataset already holds is refused.

### Volumes

**Storage › Block Volumes**: ZFS volumes, the disks behind iSCSI targets and NVMe-oF exports.

### Quotas, permissions

**Storage › Quotas** limits users and groups within a dataset. **Storage › ACL Manager** shows and changes the POSIX ACL of a folder: entries for users and groups, applied to the folder alone or to everything in it.

---

## Sharing

**Sharing › SMB Shares**: a folder, who may use it (*valid users*: users and groups, also of a domain the system is a member of), read-only or not, and per share: Previous Versions (snapshots shown to Windows clients), recycle bin, Time Machine with a size limit, allowed and denied hosts.

**Sharing › NFS Exports**: a folder and the client networks that may mount it, read-only or read-write.

**Sharing › File Share Links**: a link to one file or folder, for someone without an account, with an expiry.

The configuration of Samba and NFS is written by the system; a configuration Samba does not accept is not applied, and the previous one stays.

---

## Files

**File Explorer** browses the pools: upload (large files in parts), download, rename, copy, move, delete, and the permissions of a file or folder. It works within the pools only.

---

## Containers

**Containers › Docker.**

- **Compose** deploys a stack from a compose file, and edits and redeploys it. **Git Sync** keeps a stack in step with a compose file in a Git repository.
- **Containers** shows what runs, by container, by stack or as a list, with logs, a shell in the container, start, stop, restart and removal. Removing containers and images asks for confirmation.
- **Images** and **Pull Image**; **Stats** for what each container uses; **GPU / hardware** for what can be passed to containers.

Put a stack's data on a pool (`/mnt/<pool>/...`): it is then covered by snapshots and replication, and in a cluster the stack follows its storage group.

Containers get what their compose file gives them, including host paths and host networking, and the ports they publish are reachable whatever the Firewall page says. Deploy only what you would run as root.

**Icons.** A container shows the icon named by its label `dplaneos.icon` (the name of a Material Symbol, the file name of an image uploaded on the Docker page, or a URL), otherwise one chosen by the image name.

---

## Network and system

**Network**: addresses (DHCP or static), VLANs, bonds, DNS servers and time servers. A change takes effect at once and is taken back by itself unless you confirm it within 30 seconds: one that cuts your connection undoes itself.

**Security › Firewall**: the ports the system opened for its own services, and your rules. The firewall is on from the start.

**Security › Certificates**: the certificate of the web interface: self-signed at first, one you upload, or one from an ACME certificate authority. *Let's Encrypt* requests one for a name under which the system can be reached from the internet on port 80 (the proof is delivered there); try the staging service first, its certificate is for testing and not trusted by browsers. *Another ACME certificate authority* takes the directory address of an internal one (step-ca, for example) and its CA certificate. A certificate is used once you **activate** it. Certificates from an ACME authority are renewed by the system 30 days before they expire (checked twice a day), and the web server then serves the renewed one.

**Identity › SSH Keys**: the keys that may sign in over SSH (administrators only; no passwords and no root unless you change that), and the SSH port.

**System › Settings**: host name, time zone, tuning (the ZFS cache limit).

**System › Power Management**, **UPS**, **Hardware**, **IPMI**: disk spin-down, shutting down on battery, disks and their SMART values, the machine's BMC.

### Change History

System → **Change History** lists every change to datasets, SMB shares, NFS exports, users, groups, Docker stacks, replication jobs, SMART tasks and system settings, newest first:

- Changes made in the web UI appear right away (**Web UI**). Changes made elsewhere (shell, other tools, a GitOps apply) are found by a check every 5 minutes (**Detected**). **Record now** runs that check immediately.
- Expand a resource to see each setting before and after the change. Secrets (LDAP bind password, ACME DNS tokens, certificate keys) are never stored; the history only shows that they changed.
- Each change says where it applies: *This node only* (host name, interfaces: never exchanged), *On all nodes*, *From another node*, or *Only here so far* (made on this node, in effect here, and not fetched yet by the nodes it names, for example while the node was cut off).
- **Undo this change** sets one resource back to how it was before that change (later changes to the same resource are undone too). The usual safety rules apply: a rollback that would delete a dataset containing data, or a share with open connections, is refused. A deleted dataset is recreated with its settings but without its data; a deleted user cannot be restored from the history (passwords are not stored). Pools, LDAP, ACME and certificates are shown but cannot be rolled back here.
- **Export state.yaml** downloads the current configuration in the GitOps format (without secrets and certificates), with or without a Git repository.

In a cluster every node records what changes on it.

### Configuration Sync

System → **Configuration Sync** keeps datasets, SMB shares, NFS exports, users, groups, replication jobs and shared settings (time zone, DNS, NTP, firewall ports, Samba global settings, SSH) in step between nodes that each have their own database. Each node keeps working on its own when the network fails.

**Pairing.** On the first node, **Create join code** (valid once, for 15 minutes). On the second node, **Join another node**: enter the first node's address, the code and this node's own address as the first node reaches it, then **Preview the merge**. The preview lists what pairing would change on this node (added, updated, deleted), what goes to the other node, and what differs on both sides. Nothing changes until you click **Pair**. The other node's certificate fingerprint is shown in the preview and checked on every later connection.

**Normal operation.** Nodes exchange changes every 30 seconds and right after a change. A change made on one node is applied on the others through the same engine as GitOps, with the same safety rules: a deletion that would destroy data or interrupt open connections is refused and shown under **Received but not applied yet**. Changes for a pool that is not imported on a node wait until it is.

**Network problems.** A node that cannot reach a paired node shows a banner on every page: *Operating independently since 14:02 — 3 changes waiting to sync*. Everything keeps working. When the connection is back, changes to different things merge automatically.

**Conflicts.** If the same thing was changed differently on two nodes, neither change is overwritten. **Needs your decision** shows both versions side by side: keep this node's version, take the other node's, or edit a combined version. The decision is applied on both nodes.

**Detach** makes a node independent for good: it keeps its whole configuration and stops exchanging changes. **Remove** on a paired node stops the exchange with that one node. Both nodes can be paired again later; the preview shows what would change.

Not exchanged: hostname and network interfaces and SMART tasks (they belong to each node); Docker stacks that use a pool, iSCSI targets and NVMe-oF exports (they run only on the node that owns the pool; in a cluster they follow their storage group, see [High Availability](HIGH-AVAILABILITY.md)); LDAP, ACME and certificates (their secrets are not synchronised yet); pools. User passwords are not synchronised: a user created on another node needs a password set on this node. Nodes of an HA pair on a shared database already share their configuration and cannot be paired.

---

## Monitoring

**Dashboard**: load, memory, network, pool health and containers, live.

**System › Alerts**: where alerts go (e-mail, webhooks, Telegram) and which events each webhook gets. Set this up before you need it: a degraded pool that nobody hears of is the usual way data is lost. See [Alerts](ALERTS.md).

**System › Reporting**: history of the system's metrics. For your own monitoring, `/metrics` serves them in Prometheus format ([Integration Guide](INTEGRATION-GUIDE.md)).

**System › Logs**: the daemon's and the system's logs. **Security › Audit Log**: who changed what, from where; the log is chained so that a removed or altered entry is detected.

---

## Directory users (LDAP)

**Identity › Directory Service**: users of an LDAP directory (OpenLDAP, FreeIPA, Active Directory over LDAP) sign in to the web interface with their directory password.

1. Choose a preset, enter the server, the account the system reads the directory with (bind DN and password; a read-only account), and the base DN. **Test Connection**.
2. Map directory groups to roles (**Add Mapping**).
3. Save and enable.

- A directory user's password is checked against the directory at every sign-in; it is never stored here.
- Accounts are created by **Sync**, on its schedule, or at the first sign-in (if that is enabled).
- Roles follow the groups: a user who leaves a mapped group loses the role at the next sync or sign-in. Roles you assign by hand stay.
- Local accounts do not depend on the directory: if it is unreachable, `admin` still signs in.
- Connections use TLS (LDAPS or StartTLS) unless you switch that off.

| Problem | Look at |
|---|---|
| Connection failed | address, port, the directory's certificate, a firewall in between |
| Bind failed | the bind DN and password; a locked service account |
| User not found | the user filter: Active Directory uses `sAMAccountName`, OpenLDAP `uid` |
| No role after sign-in | the group mappings, the group base DN and filter |

## Active Directory membership

On the same page, **Active Directory**: the system joins a domain, and domain users and groups then open SMB shares with their domain accounts and appear in permissions and ACLs. Joining needs a domain account that may add computers; its password is used once and not stored. The page shows whether the domain still accepts the system's account, and an alert is raised when it stops. A system is a member of one domain. See the [Integration Guide](INTEGRATION-GUIDE.md).

## Single sign-on (OIDC)

**System › Settings › SSO / OIDC**: sign-in through an OpenID Connect provider (Keycloak, Authentik, Entra ID and others), Authorization Code with PKCE.

1. At the provider, register a confidential client with the redirect URI `https://<address of this system>/api/auth/oidc/callback` and the scopes `openid email profile` (and the one that carries groups).
2. Here: issuer URL, client ID, client secret, the label of the button on the sign-in page. Enable and save. The provider's endpoints are found from the issuer URL.

- **Who gets in.** A user who signed in through the provider before is recognised by the provider's identifier. Otherwise an existing account is linked by e-mail address, but only if the provider says the address is verified. Otherwise, with *Auto-provision* on, an account is created with the default role; with it off, the sign-in is refused. An account that is disabled here stays out.
- **Roles follow groups.** Groups from the token (claim `groups`, or the one you name) whose names match role names give those roles, and a role given this way is taken away when the group is no longer in the token. Roles you assign by hand stay.
- Local sign-in keeps working next to it.

| Message | Cause |
|---|---|
| The button does not appear | SSO is not enabled, or the issuer URL cannot be reached from this system |
| "SSO session expired" | more than ten minutes between starting and finishing the sign-in, or clocks that differ |
| "Invalid identity token" | the token's signature algorithm is not among those allowed here; clocks that differ |
| "Your account is not authorized" | no linked account, and auto-provision is off; or the account is disabled |
| Redirect URI mismatch at the provider | the registered URI must be exactly the one above |

---

## Good practice

- Give every person an account of their own, with the smallest role that does the job; keep `admin` for administration.
- Enable two-factor sign-in for administrators. Some operations require it.
- Install a certificate the browsers trust, and reach the web interface from a network you control or over a VPN, not from the internet.
- Set up alerts and test them.
- Snapshots are not a backup: replicate or sync to another system ([Backup and Replication](BACKUP-REPLICATION.md)).
- Keep a copy of the configuration: `state.yaml` in Git, or a copy of the database ([Recovery](RECOVERY.md#6-the-database-and-the-configuration)).
- Read the audit log now and then: failed sign-ins, role changes, sessions from addresses you do not know.
