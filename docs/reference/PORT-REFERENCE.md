# DPlaneOS Port Reference

Every port the system listens on, what listens on it, and whether it is configurable. Use this when assigning ports to Docker containers to avoid conflicts.

nginx talks to the daemon over a **Unix domain socket** (`/run/dplaneos/dplaned.sock`); the daemon binds no TCP port. The firewall is on: only the ports below are open, plus allow rules from Security › Firewall.

---

## Always Active

| Port | Proto | Interface | Service | Configurable |
|------|-------|-----------|---------|-------------|
| 22 | TCP | All | OpenSSH (keys only) | Yes - Identity › SSH Keys → Daemon tab |
| 80 | TCP | All | nginx (HTTP: web interface and API) | No |
| 443 | TCP | All | nginx (HTTPS, once a certificate is activated) | No |
| 5353 | UDP | All | Avahi mDNS (`<hostname>.local`) | No |
| 5403 | TCP | All | corosync-qnetd (a cluster's third vote, when this node serves it) | No |
| 5405 | UDP | All | Corosync (knet) between cluster members | No |

The cluster ports are open on every node, also before it joins a cluster. Restrict them with firewall rules (Security › Firewall) to the cluster network if you want.

---

## File Sharing

Samba and NFS are **enabled by default**. iSCSI and NVMe-oF listen once targets exist.

| Port | Proto | Interface | Service | Configurable |
|------|-------|-----------|---------|-------------|
| 445 | TCP | All | Samba (SMB2/3) | No |
| 139 | TCP | All | Samba (NetBIOS Session Service) | No |
| 137, 138 | UDP | All | Samba (NetBIOS name and datagram service) | No |
| 5357 | TCP | All | WS-Discovery (Windows network discovery) | No |
| 3702 | UDP | All | WS-Discovery multicast | No |
| 2049 | TCP+UDP | All | NFS (nfsd) | No |
| 111, 20048, 32765, 32768 | TCP+UDP | All | rpcbind, mountd, statd, lockd - only with NFSv3 (`services.dplaneos.nfs.enableNFSv3`) | No |
| 3260 | TCP | All | iSCSI (kernel LIO, `targetcli`) | No |
| 4420 | TCP | All | NVMe-oF over TCP (kernel `nvmet`) | Per export |
| 21 | TCP | All | vsftpd control channel (FTP/FTPS) | Yes - Sharing › FTP / FTPS |
| 40000-40100 | TCP | All | vsftpd passive data range | Yes - Sharing › FTP / FTPS |

FTP/FTPS runs only while enabled on Sharing › FTP / FTPS; its ports are opened with `services.dplaneos.ftp.openFirewall = true;` or firewall rules. iSCSI, NVMe-oF and MinIO ports likewise need allow rules on the Firewall page to be reachable from other machines.

---

## S3 Object Storage / MinIO (when installed and started)

| Port | Proto | Interface | Service | Configurable |
|------|-------|-----------|---------|-------------|
| 9000 | TCP | All | MinIO S3 API | Yes - Sharing › S3 Object Storage |
| 9001 | TCP | All | MinIO web console | Yes - Sharing › S3 Object Storage |

MinIO needs `services.dplaneos.s3.enable = true;` and runs only while started on its page.

---

## Local Only

| Port | Proto | Interface | Service |
|------|-------|-----------|---------|
| 8080 | TCP | 127.0.0.1 | ACME HTTP-01 challenge server, only during a certificate request (nginx forwards `/.well-known/acme-challenge/`) |
| 5432 | TCP | 127.0.0.1 | PostgreSQL (the daemon uses the Unix socket) |
| 3493 | TCP | 127.0.0.1 | NUT upsd (with a UPS configured) |

---

## Outbound Only (no listening port on the NAS)

| Destination Port | Proto | Service |
|-----------------|-------|---------|
| 80 / 443 | TCP | Paired nodes' peer API, fleet manager, Git servers, OIDC IdP, ACME/Let's Encrypt, rclone cloud targets, GitHub (update check) |
| 389 / 636 | TCP | LDAP / LDAPS (to the directory server) |
| 25 / 587 / 465 | TCP | SMTP alert delivery |
| 22 | TCP | ZFS replication over SSH; Git over SSH |
| 623 | UDP | IPMI (BMC management and power fencing) |

Outgoing connections are not filtered by the firewall.

---

## Docker Container Port Safety

Safe ranges for container `-p HOST:CONTAINER` bindings on a default installation:

**Always reserved:** `22`, `80`, `443`, `5353/UDP`, `5403`, `5405/UDP`, `8080` (127.0.0.1, during certificate requests)

**Reserved while Samba is active (default on):** `137-139`, `445`, `5357`, `3702/UDP`

**Reserved while NFS is active (default on):** `2049` (and `111`, `20048`, `32765`, `32768` with NFSv3)

**Reserved when optional services are active:**
`21`, `40000-40100` (FTP), `3260` (iSCSI), `4420` (NVMe-oF), `9000-9001` (MinIO)

**Broadly safe for container use on a default install:**
`1024-2048`, `2050-3259`, `3261-3701`, `3703-4419`, `4421-5352`, `5358-5402`, `5406-7999`, `8081-8999`, `9002-39999`, `40101-65535`

Ports a container publishes (`-p`, `ports:`) are reachable from other machines without a firewall rule: Docker forwards them itself, before the host firewall, and Firewall page rules do not restrict them. Publish on `127.0.0.1:` for a port only the NAS should reach.
