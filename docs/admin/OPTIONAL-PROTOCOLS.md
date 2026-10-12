# DPlaneOS Optional Protocols

Services that are off until you set them up: iSCSI, NVMe over TCP, FTP/FTPS and S3. Each opens its ports in the firewall when it is switched on and closes them when it is switched off; nothing has to be done on the Firewall page. What is set up here is there again after a restart.

All four are part of every release's automated test: they are set up through the API and then used from a second machine.

---

## iSCSI

A block device for another machine: a ZFS volume (zvol) offered as a disk over the network.

**Sharing › iSCSI Targets**

| Field | |
|---|---|
| Target name (IQN) | e.g. `iqn.2026-01.lan.nas:vm-disk` |
| Volume | the zvol to offer (create it under Storage › Block Volumes) |
| Portal address and port | where the target listens; all addresses, port 3260, unless you say otherwise |
| Require CHAP | on (the default): initiators sign in with the user and secret set with their access entry |

Then allow the initiators that may use it, each by its IQN (tab **ACLs / Initiators**), with CHAP credentials if CHAP is required. Nobody can connect to a target until its initiators are listed. A target without CHAP relies on the initiator names alone, which a machine on the network can claim: use it only on a storage network you control.

The page shows for each target whether this node serves it and how many of its initiators are connected. Changing a target's access list does not disturb the initiators that stay; changing its volume, address or CHAP mode rebuilds it, and its initiators reconnect.

The disk keeps its serial number across restarts, and reservations that cluster software on the initiators places on it (SCSI-3 persistent reservations) are kept too.

```
POST   /api/iscsi/targets          { "iqn": "...", "zvol": "tank/vm-disk", "portal_ip": "0.0.0.0", "portal_port": 3260, "require_chap": true }
POST   /api/iscsi/targets/update   the same fields
POST   /api/iscsi/acls             { "target_iqn": "...", "initiator_iqn": "...", "chap_user": "...", "chap_pass": "..." }
DELETE /api/iscsi/acls             { "target_iqn": "...", "initiator_iqn": "..." }
GET    /api/iscsi/targets          GET /api/iscsi/acls?target=...    GET /api/iscsi/status
DELETE /api/iscsi/targets/{iqn}
```

**On the initiator (Linux, open-iscsi):**

```bash
iscsiadm -m discovery -t sendtargets -p NAS_ADDRESS
iscsiadm -m node -T iqn.2026-01.lan.nas:vm-disk -p NAS_ADDRESS --login
# the disk appears as /dev/sdX
```

Windows: *iSCSI Initiator* › Discovery › the address of the NAS. ESXi: add a software iSCSI adapter and a dynamic discovery address.

In a cluster, a target on a volume of a storage group moves with the group; give the group a floating address and let the initiators use that. See [Integration Guide](INTEGRATION-GUIDE.md#iscsi-and-nvme-of-clients) for the initiators' timeouts.

---

## NVMe over TCP

The same idea with the NVMe protocol: lower overhead than iSCSI, for initiators that have an NVMe/TCP driver (Linux 5.0+, ESXi 7+).

**Sharing › NVMe-oF**

| Field | |
|---|---|
| Subsystem name (NQN) | e.g. `nqn.2026-01.lan.nas:fast0` |
| Volume | the zvol to offer |
| Port | 4420 unless you say otherwise |
| Hosts | the host NQNs that may connect, or any host |

"Any host" means any machine that reaches the port: NVMe over TCP as used here has no passwords. Changing the list of hosts does not disturb the initiators that stay. The namespace keeps its UUID across restarts, and an export of a storage group's volume moves with the group.

```
POST /api/nvmet/targets   { "subsystem_nqn": "...", "zvol": "tank/fast0", "allow_any_host": false, "host_nqns": ["nqn...."] }
GET  /api/nvmet/targets   PUT /api/nvmet/targets   DELETE /api/nvmet/targets?subsystem_nqn=...
```

**On the initiator (Linux, nvme-cli):**

```bash
modprobe nvme-tcp
nvme connect -t tcp -a NAS_ADDRESS -s 4420 -n nqn.2026-01.lan.nas:fast0
nvme list            # the disk appears as /dev/nvmeXnY
cat /etc/nvme/hostnqn   # this machine's host NQN, for the list of allowed hosts
```

---

## FTP / FTPS

For devices that speak nothing else (scanners, cameras, old backup software). Prefer SMB, NFS or SFTP where you have the choice.

**Sharing › FTP / FTPS**

| Field | |
|---|---|
| Folder | the folder FTP users see; they cannot leave it |
| Mode | FTP, or FTPS (TLS; a certificate is made on first use) |
| Port, passive ports | 21 and 40000-40100 unless you say otherwise |
| Allowed users | which users of the web interface may sign in; they use the same password |
| Writing | whether uploads are allowed |

Plain FTP sends passwords unencrypted: use FTPS outside a network you trust. Files uploaded belong to the user who uploaded them, as with SMB.

```
GET /api/ftp/config   PUT /api/ftp/config   GET /api/ftp/status
POST /api/ftp/start   /api/ftp/stop   /api/ftp/restart
```

---

## S3

A folder of the NAS served over the S3 protocol, for backup software and applications that store their data in buckets. The directories in the folder are the buckets, the files in them the objects: what is written over S3 can be read as files, snapshotted and replicated like everything else.

The service is [Versity Gateway](https://github.com/versity/versitygw). (Earlier versions used MinIO, which is no longer maintained as open-source software.)

**Sharing › S3 Object Storage**

| Field | |
|---|---|
| Folder | a folder in a pool; its directories are the buckets |
| Access key and secret key | the one account of the service |
| Port | 9000 unless you say otherwise |

There is one account, with access to everything in the folder; there are no further users, policies or versioning. The service speaks HTTP: put it on a network you trust, or behind a TLS proxy.

```
GET  /api/s3/config   PUT /api/s3/config   { "root_user": "...", "root_password": "...", "volume_path": "/mnt/tank/s3", "api_port": 9000 }
GET  /api/s3/status
POST /api/s3/start   /api/s3/stop   /api/s3/restart
```

**With rclone:**

```ini
[nas]
type = s3
provider = Other
endpoint = http://NAS_ADDRESS:9000
access_key_id = ...
secret_access_key = ...
force_path_style = true
```

```bash
rclone mkdir nas:backups
rclone copy ./files nas:backups/files
```

With the AWS CLI: `aws --endpoint-url http://NAS_ADDRESS:9000 s3 ls`.
