# DPlaneOS Port Reference

Every port the system listens on, what listens on it, and whether it is configurable. Use this when assigning ports to Docker containers to avoid conflicts.

The internal nginx-to-daemon pipe uses a **Unix domain socket** (`/run/dplaneos/dplaned.sock`), not a TCP port. No OS-internal port is reserved for DPlaneOS plumbing. The only open ports are the protocol ports operators already expect.

---

## Always Active

| Port | Proto | Interface | Service | Configurable |
|------|-------|-----------|---------|-------------|
| 22 | TCP | All | OpenSSH | Yes - Settings > Security > SSH Port |
| 80 | TCP | All | nginx (HTTP) | No |
| 443 | TCP | All | nginx (HTTPS, TLS terminated) | No |
| 5353 | UDP | All | Avahi mDNS (`dplaneos.local` discovery) | No |

---

## File Sharing (active when service is enabled)

Samba and NFS are **enabled by default**. iSCSI and NVMe-oF are off until targets are created.

| Port | Proto | Interface | Service | Configurable |
|------|-------|-----------|---------|-------------|
| 445 | TCP | All | Samba (SMB2/3) | No |
| 139 | TCP | All | Samba (NetBIOS Session Service) | No |
| 137 | UDP | All | Samba (NetBIOS Name Service) | No |
| 138 | UDP | All | Samba (NetBIOS Datagram) | No |
| 2049 | TCP+UDP | All | NFS (nfsd) | No |
| 111 | TCP+UDP | All | rpcbind / portmap (NFS) | No |
| 21 | TCP | All | vsftpd control channel (FTP/FTPS) | Yes - Settings > FTP |
| 40000-40100 | TCP | All | vsftpd passive data range | Yes - Settings > FTP |
| 3260 | TCP | All | iSCSI (kernel LIO via nvmet) | No |
| 4420 | TCP | All | NVMe-oF over TCP (default per-target) | Yes - per-target `listen_port` in state.yaml |

FTP/FTPS is **disabled by default** and must be started via Settings > Sharing > FTP.

---

## S3 Object Storage / MinIO (active when started)

| Port | Proto | Interface | Service | Configurable |
|------|-------|-----------|---------|-------------|
| 9000 | TCP | All | MinIO S3 API | Yes - Settings > S3 |
| 9001 | TCP | All | MinIO web console | Yes - Settings > S3 |

MinIO is **off by default**. Ports only open when MinIO is started via Settings > Sharing > S3.

---

## High Availability (HA cluster nodes only)

On single-node deployments none of these are open.

| Port | Proto | Interface | Service | Configurable |
|------|-------|-----------|---------|-------------|
| 5405 | UDP | Cluster | Corosync (knet) between all cluster members | No |
| 5403 | TCP | Cluster | Third vote (corosync-qnetd), from every node to the QDevice | No |

Cluster ports should be reachable on the cluster network only.

---

## Outbound Only (no listening port on the NAS)

| Destination Port | Proto | Service |
|-----------------|-------|---------|
| 389 | TCP | LDAP (to directory server) |
| 636 | TCP | LDAPS (to directory server) |
| 443 | TCP | OIDC IdP, ACME/Let's Encrypt, rclone cloud targets |
| 25 / 587 / 465 | TCP | SMTP alert delivery |
| 22 | TCP | ZFS replication SSH to remote host |
| 3493 | TCP | NUT upsd (local UPS daemon, loopback only) |

---

## Docker Container Port Safety

Safe ranges for container `-p HOST:CONTAINER` bindings on a default single-node installation:

**Always reserved:** `22`, `80`, `443`, `5353/UDP`

**Reserved when Samba active (default on):** `137-139`, `445`

**Reserved when NFS active (default on):** `111`, `2049`

**Reserved when optional services active:**
`21`, `40000-40100` (FTP), `3260` (iSCSI), `4420` (NVMe-oF), `9000-9001` (MinIO)

**Reserved on HA nodes:** `2379`, `2380`, `5000`, `5432`, `8008`

**Broadly safe for container use on a default single-node install:**
`1024-8079`, `8081-8999`, `9002-40099`, `40101-65535`

(excluding any ports manually opened in Settings > Firewall or declared under `system.firewall` in `state.yaml`)
