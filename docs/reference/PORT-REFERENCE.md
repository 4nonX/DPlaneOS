# DPlaneOS Port Reference

Every port the system listens on, what listens there, and when. The firewall is on from the first start and lets in only what is listed here plus the rules you add under Security › Firewall. The Firewall page shows the ports that are open at the moment ("opened by the system").

nginx talks to the daemon over a Unix socket (`/run/dplaneos/dplaned.sock`) and the daemon to PostgreSQL over another: neither has a TCP port.

---

## Always

| Port | Proto | Service | Changeable |
|------|-------|---------|------------|
| 22 | TCP | SSH (keys only unless you allow passwords; root does not sign in) | Yes, Identity › SSH Keys |
| 80 | TCP | Web interface (HTTP) | No |
| 443 | TCP | Web interface and API (HTTPS) | No |
| 445, 139 | TCP | SMB (Samba) | No |
| 137, 138 | UDP | NetBIOS name service and datagrams (Samba) | No |
| 2049, 111 | TCP+UDP | NFS, rpcbind | No |
| 5353 | UDP | mDNS: the system is found by name, Time Machine shares are announced | No |

## When switched on

| Port | Proto | Service | Opens when | Changeable |
|------|-------|---------|-----------|------------|
| 21 | TCP | FTP / FTPS control | FTP is enabled | Yes, Sharing › FTP / FTPS |
| 40000-40100 | TCP | FTP passive data | FTP is enabled | Yes, there |
| 3260 | TCP | iSCSI | the first target exists | Per target |
| 4420 | TCP | NVMe over TCP | the first export exists | Per export |
| 9000 | TCP | S3 (Versity Gateway) | S3 is started | Yes, Sharing › S3 Object Storage |

These ports close again when the service is switched off or its last target is removed.

## Cluster nodes

| Port | Proto | Service |
|------|-------|---------|
| 5405 | UDP | Corosync, between all members |
| 5403 | TCP | The third vote (corosync-qnetd), from every node to the machine that gives it |
| 443 | TCP | Exchange of configuration between the nodes (HTTPS, with pinned certificates) |
| 22 | TCP | Replication of replicated storage groups (ZFS streams over SSH) |

Keep 5405 and 5403 to the cluster network.

## Outgoing only

| Port | Proto | For |
|------|-------|-----|
| 389, 636 | TCP | LDAP / LDAPS directory |
| 88, 389, 445, 464 | TCP+UDP | Active Directory domain controllers (a member only) |
| 443 | TCP | OIDC provider, ACME, cloud targets, looking for updates |
| 25, 465, 587 | TCP | E-mail alerts |
| 22 | TCP | Replication to another system |
| 123 | UDP | Time servers |
| 3493 | TCP | A UPS server on another machine (the local one listens on loopback only) |

## Ports for containers

Containers publish their ports through Docker, which does its own packet filtering: a published port is reachable whatever the Firewall page says. Avoid the ports above; everything else is free, for example `1024-2048`, `2050-3259`, `4421-5352`, `5406-8999`, `9001-39999`, `40101-65535`.
