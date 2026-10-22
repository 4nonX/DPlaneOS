# DPlaneOS Administrator Guide

Complete reference for system administration, storage management, sharing protocols, and identity management.

For the system architecture and design philosophy, see the reference section:
- [Design Philosophy](../reference/PHILOSOPHY.md) - why DPlaneOS works the way it does
- [Architecture](../reference/ARCHITECTURE.md) - three-layer model, single-node and HA
- [GitOps Reference](../reference/GITOPS-REFERENCE.md) - state.yaml format and reconciliation

For deeper dives into operational areas, see the dedicated guides:
- [Backup and Replication](BACKUP-REPLICATION.md) - ZFS snapshots, replication, Cloud Sync, rsync, database backup
- [High Availability](HIGH-AVAILABILITY.md) - HA cluster setup, failover, rolling upgrades
- [OTA Updates](OTA-UPDATES.md) - release updates, check after the reboot, automatic way back, manual rollback
- [Optional Protocols](OPTIONAL-PROTOCOLS.md) - iSCSI, NVMe-oF, FTP/FTPS, MinIO
- [Alerts and Authentication](ALERTS.md) - SMTP, webhook, Telegram, TOTP 2FA

---

## Table of Contents

1. [User Management](#user-management)
2. [Role Management](#role-management)
3. [Storage Management](#storage-management)
4. [File Management](#file-management)
5. [Container Management](#container-management)
6. [System Settings](#system-settings)
   - [Change History](#change-history)
   - [Configuration Sync](#configuration-sync)
7. [Monitoring and Alerts](#monitoring-and-alerts)
8. [Backup and Recovery](#backup-and-recovery)
9. [Security Best Practices](#security-best-practices)
10. [Directory Service (LDAP / Active Directory)](#directory-service-ldap--active-directory)
11. [SSO (OIDC)](#sso-oidc)
12. [Custom Container Icons](#custom-container-icons)
13. [Troubleshooting](#troubleshooting)

---

## User Management

### Creating Users

**Via UI:**
1. Navigate to **Settings → Users**
2. Click **Create User**
3. Fill in username (lowercase, no spaces), email, and password (12+ characters)
4. Click **Create**
5. Assign a role (see Role Management)

Every local user is also a file-sharing account: a system account (UID from 3000, no shell, no home) that SMB and NFS use, logging in to SMB with the same password. The Users page shows this per user (*File sharing*). A user whose password was set where only its hash is known (installer, GitOps, an older version) can use SMB after their next login to the web interface or a password change. Usernames must be lower-case letters, digits, `-` and `_` to become system accounts, and a name a system account already has (e.g. `nginx`) is not used for file sharing. DPlaneOS groups become system groups with the same members (GID from 3000), usable as `@group` in a share's valid users.

### Assigning Roles

1. Settings → Users → click on the user
2. Roles section → **Assign Role**
3. Select a role: Admin, Operator, Viewer, or User
4. Optionally set an expiry date
5. Click **Assign**

Role changes take effect immediately. The permission cache is invalidated on each assignment or removal. The 5-minute TTL is only a fallback for changes made directly to the database outside the daemon.

### Resetting a User's Password

Use this when a user is locked out or needs a forced credential rotation.

1. Settings → Users → click the **lock_reset icon** next to the user
2. Enter a temporary password (full strength requirements apply)
3. Click **Set Temporary Password**

The user's existing sessions are immediately revoked. On their next login they are blocked at the system level until they change the temporary password - only `/api/auth/change-password`, `/api/auth/logout`, and `/api/auth/session` are accessible until the flag is cleared. LDAP accounts cannot have their password reset here; direct users to their directory server.

**AAL2 required:** Resetting another user's password requires the admin session to be authenticated at AAL2 (Authentication Assurance Level 2), meaning the admin must have verified TOTP during login. A password-only session (AAL1) is rejected with HTTP 403 and `action: "enable_totp"`. This prevents an account compromise from triggering unauthorized password resets.

### Removing Users

Users cannot be deleted while they have active sessions. User ID 1 (the initial admin) cannot be deleted.

1. Settings → Users → click on the user
2. Click **Delete User** and confirm

On deletion: the account, role assignments, and active sessions are all removed. An audit log entry is created.

---

## Role Management

### Built-in Roles

**Admin** - All 31 permissions. Can manage users, roles, and system settings. Cannot be deleted (system role).

**Operator** - Storage, Docker, and file management. Cannot create users or assign roles.

**Viewer** - Read-only access to all data. Can download files and view logs. Cannot modify anything.

**User** - Upload and download files, view own storage usage, read system status.

### Creating Custom Roles

1. Settings → Roles and Permissions → **Create Role**
2. Enter a name, display name, and description
3. Save, then click on the new role
4. Select the permissions it should have
5. Click **Save Permissions**

### Permission Reference

Permissions are `resource:action` pairs. Not all resources expose all actions.

| Resource | Actions | What it covers |
|----------|---------|----------------|
| `storage` | read, write, delete, admin | Pools, datasets, quotas, encryption, replication |
| `snapshots` | read, write | Snapshot schedules and management |
| `shares` | read, write, admin | SMB/NFS share configuration and reload |
| `files` | read, write | File Explorer: browse/download and upload/modify/delete |
| `docker` | read, write, delete, admin | Containers, images, compose, prune |
| `network` | read, write | Network interfaces and routing |
| `firewall` | read, write | Firewall rules |
| `users` | read, write, admin | User accounts, groups, and sessions |
| `roles` | read, write | Role and permission management |
| `system` | read, write, admin | System settings, reboot, poweroff, audit log rotation |
| `monitoring` | read | Metrics and health dashboard |
| `audit` | read | Audit log chain view and verification |
| `certificates` | read, write | TLS certificate management |

---

## Storage Management

### Creating Pools

| Disks | Configuration | Usable Space | Failure Tolerance |
|-------|---------------|--------------|-------------------|
| 2 | Mirror | 50% | 1 disk |
| 3 | RAID-Z1 | 67% | 1 disk |
| 4 | RAID-Z2 | 50% | 2 disks |
| 6 | RAID-Z2 | 67% | 2 disks |
| 6 | RAID-Z3 | 50% | 3 disks |

**Steps:** Storage → Pools → **Create Pool** → select disks → choose RAID level → enter pool name → Create.

### Managing Datasets

**Create:** Storage → Datasets → **Create Dataset**. Recommended settings: compression `lz4`, set quotas per user.

**Best practices:**
- Use hierarchical names: `pool/data/users/alice`
- Enable compression (saves ~30% on typical data)
- Set quotas for user datasets
- Take regular snapshots (see Backup section)

### Scrubbing Pools

Scrubs verify on-disk data integrity and auto-repair from parity. Recommended monthly.

**Via UI:** Storage → Pools → select pool → **Start Scrub**.

### Pool Maintenance (Clear/Online)

For troubleshooting pool errors or managing hot-swap replacements:

- **Clear Errors**: Storage → Pools → select pool → **Clear**. Resets the pool's error counters (useful after a known cable issue or transient fault).
- **Online Device**: If a device was previously detached or disconnected, use the **Online** action in the disk management view to attempt to bring it back into the pool.

**Via cron:**
```bash
sudo crontab -e
# Add:
0 2 1 * * /usr/sbin/zpool scrub tank
```

### Pool and Dataset Constraints

**Pool destroy dependency check:** A pool cannot be destroyed (without force) if any dataset within it is actively mounted, or if any NFS export or SMB share references a path within the pool. The operation returns a list of blocking dependencies. Remove or disable the shares first, then retry. The Force option bypasses this check: if a share entry is corrupt and cannot be removed, force allows recovery. Force does not protect against connected clients seeing an abrupt loss - pool destroy is final regardless. The destroy confirmation (which requires typing the pool name) runs before force has any effect. Note: pool export with force behaves differently and still validates dependencies, because a forced export with open handles causes client-side data corruption whereas a forced destroy is terminal either way.

**Snapshot clone detection:** If a snapshot has dependent clones, destroy is rejected with a message naming the clone. Promote or destroy the clone first.

**Quota guard:** Setting a refquota below the dataset's current referenced usage is rejected. The error message includes the current usage so you can choose an appropriate quota value.

## File Management

### File Explorer

DPlaneOS includes a web-based file explorer accessible via the **Files** navigation item.

- **Navigation**: Browse datasets and directories in real-time.
- **Uploads**: Supports large, chunked multi-gigabyte uploads directly to the server.
- **Operations**: Rename, Copy, Move, and Delete files/directories.

### ACL Management (POSIX ACLs)

For granular access control beyond standard owner/group permissions, the File Explorer supports POSIX ACLs.

1. Navigate to **Files**.
2. Right-click any file or directory.
3. Select **Manage Permissions (ACL)**.
4. From this dialog, you can:
   - View current ACL entries.
   - Add new entries for specific users or groups.
   - Set permissions (Read, Write, Execute).
   - Apply changes recursively to directory contents.

> [!IMPORTANT]
> To use ACLs, the underlying ZFS dataset must have `acltype=posix` set. Datasets created on the Datasets page have it; a pool's root dataset does not (OpenZFS on Linux defaults to `off`). Giving a share's users access to its folder (below) turns it on for that dataset, with `xattr=sa`.

## File Sharing (SMB / NFS)

Users log in to SMB shares with their DPlaneOS user name and password (see User Management); NFS clients see files owned by the same UIDs.

**SMB shares** (Shares page):
- **Valid users**: users and `@groups` allowed in; empty means every user.
- **Give these users access to the folder** (on by default for a new share): a new folder usually belongs to root, so users could open the share but not write. This sets an inherited POSIX ACL on the folder for the valid users (or the group `users`, which every account is in): read and write, or read for a read-only share. Existing contents keep their permissions; use Files → Manage Permissions for those.
- **Guest access**: while a share allows guests, users the server does not know are let in as guest (`map to guest = Bad User`). Windows 11 refuses guest logins unless its policy allows them.
- **Previous Versions**: ZFS snapshots of the share's dataset, including those of child datasets inside it and of schedules with their own prefix (`<prefix>-YYYYMMDD-HHMM`).
- The server shows up under **Network** in Windows Explorer (WS-Discovery) and in the Finder (Bonjour).

**NFS exports** (NFS page): served as NFSv4.0, 4.1 and 4.2 by default (`services.dplaneos.nfs.minVersion`; macOS mounts only 4.0). NFSv3 (Windows' NFS client, older ESXi) is off; `services.dplaneos.nfs.enableNFSv3 = true` turns it on with fixed helper ports opened in the firewall. New exports use `rw,sync,no_subtree_check,root_squash,crossmnt` (`crossmnt`: child datasets are visible; without it they appear empty). Unknown options are refused before they are saved.

---

## Container Management

Requires permission: `docker:write`.

### Deploying Containers

Containers → Docker → **Pull Image** tab to pull an image, or **Compose Stacks** tab to deploy a `docker-compose.yml`. Standalone containers can be deployed from the Containers tab via **Deploy Container**.

### View Modes

The Containers tab offers three layouts: **Grid** (icon card per container, default), **By Stack** (grouped by compose project), and **List** (table with resource usage). The selected layout persists across sessions.

### Editing a Container

Click the tune icon on any container card to open the edit modal. You can change:
- **Icon** - Material Symbol name, `.svg`/`.png` filename, or a URL
- **Restart policy** - no / always / unless-stopped / on-failure
- **Ports**, **Volumes**, and **Environment variables**

Changes are applied by stopping and recreating the container. Compose-managed containers should be edited via their compose file in the Compose Stacks tab.

### Container Icons

Each container displays an icon resolved in this order:

1. **`dplaneos.icon` label** on the container (set in `docker-compose.yaml`):
   - A Material Symbol name (e.g. `database`) - renders as a vector icon
   - A filename ending in `.svg`, `.png`, or `.webp` - served from `/var/lib/dplaneos/custom_icons/`
   - A full URL starting with `http` or `/` - loaded as an image
2. **Built-in image-name mapping** - covers 80+ well-known images (Jellyfin, Plex, Nextcloud, Grafana, etc.)
3. **Fallback** - generic `deployed_code` icon

**Adding a custom icon:**
1. Copy your image file to `/var/lib/dplaneos/custom_icons/myapp.svg`
2. In your `docker-compose.yaml` or container config, add:
   ```yaml
   labels:
     dplaneos.icon: myapp.svg
   ```

Custom icons are served via `GET /api/assets/custom-icons/<filename>`. The full icon list is available at `GET /api/assets/custom-icons/list`.

### Managing Containers

- **Start/Stop:** click on container → Start or Stop
- **Logs:** click on container → Logs tab
- **Exec:** click on container → Exec tab → enter `bash` or any command
- **Remove:** click on container → Delete → confirm

### Compose Stacks

Each stack is a folder in `/var/lib/dplaneos/stacks/<name>/` with its `docker-compose.yml` and `.env`, run as compose project `<name>`. One operation runs per stack at a time; a second one is refused with "busy". An edit is checked with `docker compose config` before it replaces the working file, and if deploying it fails, the previous version is put back and deployed again. A stack's status counts exited services ("partial" when some are down). Actions keep running on the server when the browser tab is closed.

### Updating Stacks

**Update** pulls the stack's images and recreates the services whose image changed, then waits until every service is up: healthy when it has a health check, otherwise running without a restart for 10 seconds. If one is not, the stack goes back to the images it ran before. Before the update the ZFS datasets the stack bind-mounts are snapshotted (`<dataset>@pre-update-<stack>-<time>`, the last 3 kept); with **Roll data back on failure** the data goes back to that snapshot too (what the app wrote since is lost). The result of the last update is shown with the stack.

**Update detection:** every 6 hours (or **Check now**) DPlaneOS asks the registries, through Docker and its registry logins, which image each tag points to now, without pulling, and marks stacks with new images. Images pinned by digest and locally built images are not checked. **Auto-update** (per stack, off by default) updates a stack when new images are found; an update that fails is rolled back and that image is not tried again until the registry has a newer one. Alerts report automatic updates and failures.

### Git-backed Stacks (Git Sync)

A repository's compose file runs as compose project `git-<name>`. Auto-sync fetches the branch and moves the clone to it; a clone with local changes or commits that are not pushed is left alone, with an error, until they are pushed or discarded. A new commit is deployed; the same commit is not deployed again, so a stack stopped on purpose stays stopped. Deploy errors are shown with the repository and raise an alert.

**Deploy on push:** **Webhook** creates a secret (shown once) and the payload URL `/api/git-sync/repos/webhook/<id>`. Add them in GitHub, Gitea/Forgejo (content type `application/json`, push events) or GitLab (secret token); a push to the tracked branch starts a sync and deploys the new commit.

### Sandboxes

Docker → Sandbox clones a dataset (`<pool>/sandboxes/<name>` from a `@sandbox-base-*` snapshot) to try something on a copy, mounted as a volume in a test container. **Destroy** removes the clone and its snapshot; **Cleanup** removes base snapshots whose clone was deleted by other means.

---

## System Settings

### Network Configuration

Network → configure interface, IP address, gateway, DNS, VLANs, and bonding → **Apply**.

**Every change is confirmed.** A change applies at once; a banner then asks to **Keep** it. Without that within 90 seconds it is reverted, so a change that cuts off the browser undoes itself. One change waits at a time. When you change the address you are connected to, open the interface on the new address (log in again) and confirm there. A change that was not confirmed is also reverted when the daemon restarts or the machine reboots.

**How it is stored.** DPlaneOS writes systemd-networkd files `/etc/systemd/network/05-dplane-<interface>.{network,netdev}` (one per interface it configures) and `/etc/systemd/resolved.conf.d/50-dplane.conf` for DNS; the state is `/var/lib/dplaneos/network-state.json`. They come before the files NixOS generates, so they take precedence; interfaces DPlaneOS does not configure keep the system configuration (DHCP on every Ethernet port by default). No rebuild is needed. The GitOps `system.networking` and `dns_servers` settings use the same files.

- **VLANs** go on an interface or a bond; the parent keeps its address. A VLAN can have an address, DHCP, or none.
- **Bonds** take their members' place: the members lose their own addresses. A bond created without an address takes over the configuration of its first member that has one. Link monitoring (100 ms) is on, so a failed member is noticed. Note that the bond has its own MAC address, so DHCP may give it a new address. Deleting a bond gives its members back to the system configuration.
- **DNS** servers and search domains apply to all interfaces.

### Firewall

The **Firewall** page is in the Security group. On NixOS the firewall is part of the system configuration: it opens the ports of the enabled services (listed on the page) and refuses everything else. Rules made on the page apply at once on top of it, in order; the first rule that matches decides:

- **Allow** opens a port, optionally only for a source address or network.
- **Deny** blocks a port, also one the system opens. To allow SMB only from the office network: *allow 445/tcp from 192.168.10.0/24*, then *deny 445/tcp*.

Like network changes, every rule change is reverted unless confirmed. The rules live in the nftables table `inet dplaneos_fw` (`/var/lib/dplaneos/firewall.nft`, loaded at boot by `dplaneos-firewall.service`). They apply to connections to the server itself; ports published by Docker containers are not affected.

### Change History

System → **Change History** lists every change to datasets, SMB shares, NFS exports, users, groups, Docker stacks, replication jobs, SMART tasks and system settings, newest first:

- Changes made in the web UI appear right away (**Web UI**). Changes made elsewhere (shell, other tools, a GitOps apply) are found by a check every 5 minutes (**Detected**). **Record now** runs that check immediately.
- Expand a resource to see each setting before and after the change. Secrets (LDAP bind password, ACME DNS tokens, certificate keys) are never stored; the history only shows that they changed.
- **Undo this change** sets one resource back to how it was before that change (later changes to the same resource are undone too). The usual safety rules apply: a rollback that would delete a dataset containing data, or a share with open connections, is refused. A deleted dataset is recreated with its settings but without its data; a deleted user cannot be restored from the history (passwords are not stored). Pools, LDAP, ACME and certificates are shown but cannot be rolled back here.
- **Export state.yaml** downloads the current configuration in the GitOps format (without secrets and certificates), with or without a Git repository.

In an HA pair the history is recorded by the active node.

### Configuration Sync

System → **Configuration Sync** keeps datasets, SMB shares, NFS exports, users, groups, replication jobs and shared settings (time zone, DNS, NTP, firewall ports, Samba global settings, SSH) in step between nodes that each have their own database. Each node keeps working on its own when the network fails.

**Pairing.** On the first node, **Create join code** (valid once, for 15 minutes). On the second node, **Join another node**: enter the first node's address, the code and this node's own address as the first node reaches it, then **Preview the merge**. The preview lists what pairing would change on this node (added, updated, deleted), what goes to the other node, and what differs on both sides. Nothing changes until you click **Pair**. The other node's certificate fingerprint is shown in the preview and checked on every later connection.

**Normal operation.** Nodes exchange changes every 30 seconds and right after a change. A change made on one node is applied on the others through the same engine as GitOps, with the same safety rules: a deletion that would destroy data or interrupt open connections is refused and shown under **Received but not applied yet**. Changes for a pool that is not imported on a node wait until it is.

**Network problems.** A node that cannot reach a paired node shows a banner on every page: *Operating independently since 14:02 — 3 changes waiting to sync*. Everything keeps working. When the connection is back, changes to different things merge automatically.

**Conflicts.** If the same thing was changed differently on two nodes, neither change is overwritten. **Needs your decision** shows both versions side by side: keep this node's version, take the other node's, or edit a combined version. The decision is applied on both nodes.

**Detach** makes a node independent for good: it keeps its whole configuration and stops exchanging changes. **Remove** on a paired node stops the exchange with that one node. Both nodes can be paired again later; the preview shows what would change.

Not exchanged: hostname and network interfaces and SMART tasks (they belong to each node); Docker stacks that use a pool and NVMe-oF exports (they must run only on the node that owns the pool, which comes with storage groups in a later release); LDAP, ACME and certificates (their secrets are not synchronised yet); pools. User passwords are not synchronised: a user created on another node needs a password set on this node. Nodes of an HA pair on a shared database already share their configuration and cannot be paired.

### Notifications

Settings → System → Notifications - configure SMTP for email alerts. The ZED hook also supports direct Telegram alerts for critical ZFS events (pool degraded, disk faulted). See `install/zed/dplaneos-notify.sh` for configuration details (reads `telegram_config` from the database).

---

## Monitoring and Alerts

### Dashboard Metrics

CPU, RAM, disk I/O, network traffic, pool health, and container status are displayed on the dashboard in real time via WebSocket.

### Alert Thresholds

Monitoring → Settings - configure thresholds for CPU, RAM, disk capacity, and I/O wait. Alerts route to email, webhook, Telegram, and the dashboard.

### Viewing Logs

- System logs: System → Logs
- Audit log: Security → Audit Log (HMAC-chained, tamper-evident)
- Container logs: Containers → Docker → select container → Logs

---

## Backup and Recovery

### ZFS Snapshots

```bash
# Manual snapshot
zfs snapshot tank/data@backup-$(date +%Y%m%d)

# Automatic snapshots - configure via services.zfs.autoSnapshot in configuration.nix
# services.zfs.autoSnapshot.enable = true;
```

### Configuration Backup

Settings → Maintenance downloads the whole configuration (database and settings files) and restores it; a copy is written daily to the first data pool. See [Backup and Replication](BACKUP-REPLICATION.md#configuration-backup).

### Pool Export/Import

```bash
# Export (for moving to another system)
sudo zpool export tank

# Import
sudo zpool import        # list importable pools
sudo zpool import tank   # import by name
```

---

## Security Best Practices

**Hardened Execution Whitelist (v6.1.0):** The daemon uses a strict, "sentence-based" allowlist for all system commands (`zfs`, `zpool`, `ufw`, etc.). This means only predefined, safe command structures are allowed. Modification of critical ZFS properties (like `mountpoint`, `quota`, `atime`) and firewall rules is restricted to validated patterns to prevent accidental or malicious system disruption. In v6.1.0, disk operations like `zpool attach` and `zpool replace` are strictly validated against `by-id` paths and pool membership.

**Path Normalization:** DPlaneOS is fully path-agnostic. It does not rely on hardcoded absolute paths (`/usr/bin/`, `/bin/`) for key binaries, instead using the system's `PATH` for resolution. On NixOS this is essential as binaries live under `/nix/store`.

**Allowed Base Paths:** File operations (create, delete, rename, chown, chmod) are restricted to a defined set of "safe" base paths:
- `/mnt/*` (Main storage pools)
- `/home/*` (User directories)
- `/tank/*`, `/data/*`, `/media/*`, `/opt/*`, `/srv/*` (Common storage mountpoints)
- `/tmp/*` (Temporary files)
- `/var/lib/dplaneos/` (Application data)

**Passwords:** Minimum 8 characters with uppercase, lowercase, digit, and special character (12+ recommended). On first install the setup wizard creates the admin account - no pre-generated password is printed; you set it during wizard Step 1. The forced-change flag (`must_change_password`) is server-enforced: all API routes except `/api/auth/change-password`, `/api/auth/logout`, and `/api/auth/session` return HTTP 403 until the flag is cleared. When changing a password, all other active sessions are revoked automatically.

To reset a forgotten or compromised password, an admin uses **Settings → Users → lock_reset icon** to set a temporary password (see User Management above). There is no self-service password reset - this is intentional for a NAS with no email integration.

**HTTPS:** Set up a TLS certificate via certbot or your reverse proxy. The nginx config ships with appropriate security headers.

**Firewall:**
```bash
sudo ufw allow 80/tcp
sudo ufw allow 443/tcp
sudo ufw allow 22/tcp
sudo ufw enable
```

**Fail2ban:** Install `fail2ban` and configure it to monitor `/var/log/dplaneos/access.log` (maxretry 5, bantime 3600).

**Audit logs:** Review monthly via Settings → Audit Log. Look for failed logins, unexpected role changes, and out-of-hours access.

**Least privilege:** Use Operator or Viewer roles for daily-use accounts; reserve Admin for administrative tasks only. Review role assignments quarterly.

**Database access for direct queries:**
```bash
sudo -u postgres psql dplaneos -c "SELECT r.name FROM roles r \
  JOIN user_roles ur ON r.id = ur.role_id WHERE ur.user_id = X;"
```

---

## Directory Service (LDAP / Active Directory)

### Quick Setup

1. Navigate to **Identity → Directory Service**
2. Select a preset: **Active Directory**, **OpenLDAP**, **FreeIPA**, or **Custom**
3. Enter server address, Bind DN, Bind Password, and Base DN
4. Click **Test Connection**
5. Click **Save Configuration** and enable the toggle

### Group to Role Mapping

| AD Group | DPlaneOS Role | Access Level |
|----------|----------------|--------------|
| `IT_Admins` | Administrator | Full system access |
| `Storage_Team` | Operator | Storage, Docker, Shares |
| `Domain Users` | User | Files, Dashboard |
| `Auditors` | Viewer | Read-only |

To add a mapping: click **Add Mapping**, enter the LDAP group name, select the role, click **Add Mapping**.

### Authentication Model

DPlaneOS uses **directory-sourced user provisioning with local authentication**. This is intentionally different from live LDAP auth:

- Users are **synced from the directory** into the local database (`POST /api/ldap/sync`), with group-to-role mappings applied at sync time.
- At login, synced users authenticate via **LDAP bind** - the daemon connects to the directory server and verifies credentials in real time.
- Local accounts (including the system administrator) always authenticate via bcrypt regardless of LDAP state.
- If the LDAP server is unreachable, **all local accounts continue to work** - the UI is never fully locked out.

This model gives you directory-controlled access without making the management UI dependent on directory availability.

> **Note:** Unlike TrueNAS Scale and Unraid, which only use LDAP/AD for SMB share authentication, DPlaneOS uses LDAP credentials to authenticate web UI logins for directory-sourced accounts.

### Sync vs Live Auth

| Account type | How it authenticates |
|---|---|
| `source=local` | bcrypt against local `password_hash` |
| `source=ldap` | Real-time LDAP bind against the configured server |
| User ID 1 (admin) | Always local bcrypt, even if `source=ldap` |

### Security Notes

- The system administrator (user ID 1) always uses local authentication, even when LDAP is enabled, preventing lockout if the directory server goes down.
- TLS is enforced by default (TLS 1.2+).
- The bind password is stored in PostgreSQL - use a read-only service account.
- If the LDAP server is unreachable, local accounts continue to work. Directory-sourced accounts will fail login until the server is reachable again.

### LDAP API

```
GET    /api/ldap/config
POST   /api/ldap/config
POST   /api/ldap/test
GET    /api/ldap/status
POST   /api/ldap/sync
POST   /api/ldap/search-user
GET    /api/ldap/mappings
POST   /api/ldap/mappings
DELETE /api/ldap/mappings?id=N
GET    /api/ldap/sync-log
```

### Troubleshooting LDAP

| Issue | Solution |
|-------|----------|
| Connection failed | Verify server address, port, and firewall rules |
| Bind failed | Verify Bind DN and password; check the service account is not locked |
| User not found | Check User Filter: AD uses `sAMAccountName`, OpenLDAP uses `uid` |
| No groups mapped | Verify Group Base DN and Group Filter |
| Admin locked out | User ID 1 always uses local auth - log in with local credentials |

---

## SSO (OIDC)

DPlaneOS supports single sign-on via any OpenID Connect (OIDC) Authorization Code + PKCE provider: Keycloak, Authentik, Dex, Auth0, Microsoft Entra ID, Google Workspace, and others.

### Quick Setup

1. Navigate to **Settings → SSO / OIDC**
2. Enter the **Issuer URL** (the base URL of your provider, e.g. `https://auth.example.com/realms/myrealm`)
3. Enter your **Client ID** and **Client Secret** from the IdP application registration
4. Set a **Button Label** (displayed on the login page, e.g. "Sign in with Keycloak")
5. Toggle **Enable SSO** and click **Save Configuration**

DPlaneOS auto-discovers the provider's endpoints from `{issuer}/.well-known/openid-configuration`. No manual endpoint configuration is required.

### IdP Application Registration

Register DPlaneOS as a confidential client with:

- **Redirect URI:** `https://your-dplaneos-hostname/api/auth/oidc/callback`
- **Grant type:** Authorization Code
- **PKCE:** S256 (required; if your IdP allows disabling PKCE, do not)
- **Scopes:** `openid email profile groups` (adjust if your IdP uses a different claim for group membership)

### Group to Role Mapping

OIDC group names are matched against DPlaneOS role names at each login: an IdP group named exactly like a role grants that role. The claim that carries group membership is configurable (default: `groups`); it may be a list or a space- or comma-separated string.

| IdP group | DPlaneOS role | Access level |
|-----------|---------------|--------------|
| `admin` | Administrator | Full system access |
| `operator` | Operator | Storage, Docker, Shares |
| `user` | User | Files, Dashboard |
| `viewer` | Viewer | Read-only |

Groups that do not match a role name are ignored. Roles granted this way (`granted_by = 'oidc-provider'`) follow the IdP: a role whose group the user no longer has is revoked at the next login. Roles granted locally and the default role of auto-provisioned accounts are never revoked by the IdP. When the token has no groups claim at all (the scope was not granted), roles are left unchanged.

### User Provisioning

DPlaneOS resolves users in priority order:

1. **Existing OIDC identity link** - the `(issuer, subject)` pair from the IdP is the stable, authoritative key. Matched immediately on subsequent logins regardless of email changes.
2. **Email-based link** - if no identity link exists and the IdP marks the email as verified (`email_verified: true`), DPlaneOS links the local account with the same email address. An unverified email never links (anyone able to set an email at the IdP could otherwise take over that account). Subsequent logins use the identity link.
3. **Auto-provision** - if no match and **Auto-provision accounts** is enabled, a new local account is created with `source = 'oidc'` and the configured Default Role assigned.

If **Auto-provision** is disabled and neither of the first two paths match, login is rejected with a "not authorized" error on the login page.

Auto-provisioned usernames are derived from the IdP's `preferred_username` claim, falling back to the local part of the email address, then the subject. Characters outside `[a-zA-Z0-9_-]` are replaced with `_`. Duplicate names get a numeric suffix.

### Account Sources

| `source` | How it authenticates |
|----------|----------------------|
| `local` | bcrypt against local `password_hash` |
| `ldap` | Real-time LDAP bind against the configured directory server |
| `oidc` | OIDC flow; no local password; password-change UI is disabled for these accounts |
| User ID 1 (admin) | Always local bcrypt regardless of source |

### Login Flow

1. User clicks the SSO button on the login page.
2. Browser is redirected to the IdP authorization endpoint with a PKCE S256 challenge.
3. After the user authenticates, the IdP redirects to `/api/auth/oidc/callback`.
4. The daemon validates state, exchanges the code for tokens, verifies the ID token, resolves the user, and stores a one-time 2-minute handoff code.
5. Browser is redirected to `/login?oidc_handoff=<code>`.
6. The login page detects the parameter, exchanges it via `POST /api/auth/oidc/exchange`, and stores the session - identical in shape to a normal login response.

The handoff code is necessary because the session token cannot safely travel in a redirect URL (browser history and access logs would expose it). The exchange call is a normal XHR that the SPA makes directly, so the token stays out of the URL entirely.

### OIDC API

```
GET    /api/auth/oidc/info        public - login page queries this for SSO button
GET    /api/auth/oidc/start       public - initiates IdP redirect
GET    /api/auth/oidc/callback    public - receives IdP redirect (internal, not for direct use)
POST   /api/auth/oidc/exchange    public - SPA exchanges handoff code for session
GET    /api/auth/oidc/config      system:admin - read OIDC configuration (client secret omitted)
POST   /api/auth/oidc/config      system:admin - write OIDC configuration
```

### Troubleshooting OIDC

| Issue | Solution |
|-------|----------|
| SSO button does not appear | Enable SSO in Settings and confirm the provider discovery URL is reachable from the server |
| "SSO session expired - please try again" | State row expired (10-minute TTL). Browser clock may be far ahead of server. |
| "Invalid identity token from SSO provider" | Check **Allowed Algorithms** matches what your IdP signs with (default: `RS256`); check clock skew |
| "Your account is not authorized to log in" | Auto-provision is off and no local account matches the email; create a local account or enable auto-provision |
| "SSO provider is temporarily unavailable" | The daemon could not reach the IdP discovery URL; check network connectivity from the server |
| Redirect URI mismatch | The registered redirect URI must exactly match `https://your-dplaneos-hostname/api/auth/oidc/callback` - note the trailing path |

---

## Custom Container Icons

The daemon serves custom icons from `/var/lib/dplaneos/custom_icons/`.

Supported formats: `.svg`, `.png`, `.webp`

**API endpoints:**
- `GET /api/assets/custom-icons/<filename>` - serve a single icon file
- `GET /api/assets/custom-icons/list` - JSON list of available filenames
- `GET /api/docker/icon-map` - built-in image-name to Material Symbol mapping (80+ entries)

**Usage:** Drop an image file into `/var/lib/dplaneos/custom_icons/`, then reference it via the `dplaneos.icon` container label. No daemon restart is required.

---

## Troubleshooting

See [TROUBLESHOOTING.md](TROUBLESHOOTING.md) for comprehensive troubleshooting steps.

### Common Issues

**Permission Denied (403):**
```bash
# Check user's roles
sudo -u postgres psql dplaneos -c \
  "SELECT r.name FROM roles r JOIN user_roles ur ON r.id = ur.role_id WHERE ur.user_id = X;"
sudo systemctl restart dplaned  # clears permission cache
```

**Pool Will Not Mount:**
```bash
sudo zpool status
sudo zpool import -f tank
sudo zpool status -v  # check for errors
```

**High Memory Usage:**
```bash
arc_summary  # ZFS ARC breakdown
# To limit ARC (in /etc/modprobe.d/zfs.conf):
# options zfs zfs_arc_max=17179869184  (16 GB in bytes)
```

**Slow Web Interface:**
```bash
htop      # CPU
iotop     # disk I/O
sudo systemctl restart dplaned
```
