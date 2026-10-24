# DPlaneOS Optional Protocols

This document covers protocols that are not enabled by default: iSCSI, NVMe-oF, FTP/FTPS, and MinIO. Each section explains what the protocol is for, how to enable it, and how to manage it.

Standard protocols (SMB, NFS) are covered in [ADMIN-GUIDE.md](ADMIN-GUIDE.md). For GitOps declaration of NVMe-oF, see [GITOPS-REFERENCE.md](../reference/GITOPS-REFERENCE.md#nvme-of-fabrics-optional).

---

## iSCSI (Block Storage over TCP)

iSCSI presents a ZFS zvol as a block device to a network initiator. The initiator sees the zvol as a raw disk and can format it with any filesystem. Typical use cases: booting ESXi, presenting block storage to a Windows server, or providing disks to VMs.

DPlaneOS uses the Linux LIO target stack (targetcli-fb) for iSCSI. `targetcli-fb` is included in the NixOS module's system packages and requires no additional NixOS configuration.

### Create a zvol for iSCSI

iSCSI requires a ZFS zvol, not a dataset. Create one:

```bash
# 100 GB zvol on the tank pool
zfs create -V 100G -b 4K tank/iscsi-target

# Or via the UI: Storage: Datasets: Create zvol
```

### Configure via UI

Shares: iSCSI: Add Target.

Required fields:
- **IQN** (iSCSI Qualified Name): follows the format `iqn.YYYY-MM.reverse.domain:identifier`
  - Example: `iqn.2026-01.io.dplaneos:nas01-esxi`
- **zvol path**: the ZFS zvol to back this target (e.g., `tank/iscsi-target`)
- **Authentication** (optional): CHAP username and secret for initiator authentication

### Configure via API

```
POST /api/iscsi/targets
{
  "iqn": "iqn.2026-01.io.dplaneos:nas01-esxi",
  "zvol": "tank/iscsi-target",
  "auth": {
    "chap_username": "esxi-host",
    "chap_secret": "STRONG_SECRET_MIN_12"
  }
}
```

### Initiator Setup (Linux)

```bash
# Install iscsiadm
nix-env -iA nixpkgs.openiscsi   # on NixOS
# Debian/Ubuntu initiator: apt install open-iscsi

# Discover targets
iscsiadm -m discovery -t sendtargets -p NAS_IP

# Connect to target
iscsiadm -m node -T iqn.2026-01.io.dplaneos:nas01-esxi -p NAS_IP --login

# The device appears as /dev/sdX
lsblk
```

### Initiator Setup (Windows / ESXi)

On Windows Server: iSCSI Initiator control panel: Discovery tab: Target Portal: enter NAS IP. Connect tab: select the IQN.

On ESXi: Storage: Add Storage: iSCSI (Software): enter NAS IP, rescan.

### Firewall

iSCSI uses TCP port 3260. Allow it:
```yaml
# In state.yaml
system:
  firewall:
    tcp: [3260]
```

---

## NVMe-oF (NVMe over Fabrics / NVMe/TCP)

NVMe-oF over TCP presents ZFS zvols as NVMe namespaces to initiators. It provides lower latency and higher throughput than iSCSI for workloads that saturate the NIC. DPlaneOS uses the kernel `nvmet` subsystem over TCP transport.

NVMe-oF is not enabled by default. It requires a kernel with NVMe/TCP support (included in the Linux 6.6 LTS kernel that DPlaneOS pins).

The `nvmet` and `nvmet-tcp` kernel modules are loaded at boot by the NixOS module. No additional NixOS configuration is required.

### Create a zvol for NVMe-oF

Same as iSCSI - NVMe-oF requires a zvol:

```bash
zfs create -V 500G -b 4K tank/nvme-target
```

### Configure via UI

Shares: NVMe-oF: Add Subsystem.

Required fields:
- **Subsystem NQN**: follows the format `nqn.YYYY-MM.reverse.domain:identifier`
  - Example: `nqn.2026-01.io.dplaneos:tank-data`
- **zvol**: the ZFS zvol to back this namespace
- **Listen address**: `0.0.0.0` to listen on all interfaces
- **Listen port**: 4420 (default)
- **Host NQNs**: list of allowed initiator NQNs (leave empty with `allow_any_host: true` for testing only)

### Configure via state.yaml

NVMe-oF targets can be declared in `state.yaml` and managed by the GitOps reconciler:

```yaml
fabrics:
  nvme:
    - subsystem_nqn: nqn.2026-01.io.dplaneos:tank-data
      zvol: tank/nvme-target
      transport: tcp
      listen_addr: 0.0.0.0
      listen_port: 4420
      namespace_id: 1
      allow_any_host: false
      host_nqns:
        - nqn.2026-01.io.initiator:my-server
```

### Initiator Setup (Linux)

```bash
# Load NVMe/TCP module on the initiator
modprobe nvme-tcp

# Discover subsystems
nvme discover -t tcp -a NAS_IP -s 4420

# Connect
nvme connect -t tcp -n nqn.2026-01.io.dplaneos:tank-data -a NAS_IP -s 4420

# List NVMe devices
nvme list
# Device appears as /dev/nvme0n1
```

### Firewall

NVMe-oF uses TCP port 4420:
```yaml
system:
  firewall:
    tcp: [4420]
```

---

## FTP / FTPS

FTP and FTPS are for file transfer to clients that cannot use SMB or NFS - legacy applications, network printers, or mass file upload workflows. DPlaneOS manages vsftpd.

**Note:** Plain FTP transmits credentials unencrypted. Use FTPS (FTP over TLS, explicit mode) wherever possible.

vsftpd is part of the system (`services.dplaneos.ftp.enable`, on by default). The daemon writes its configuration to `/var/lib/dplaneos/ftp/` and runs it as the unit `dplaneos-vsftpd` while FTP is enabled on the page.

### Configure via UI

Sharing › FTP / FTPS.

Options:
- **Mode**: FTP or FTPS (TLS)
- **Port** (default 21) and **passive port range** (default 40000-40100)
- **Local users**: allow the DPlaneOS users to log in; **allowed users** limits it to a list
- **Write access** and **chroot** (users stay in their home directory)
- **Anonymous access**: off by default; enable only for public read-only shares
- **Max clients** and **max per IP**, **banner**
- **TLS certificate and key**: empty uses a self-signed certificate the daemon creates in `/var/lib/dplaneos/ftp/`

### Passive Mode and Firewall

FTP active mode requires inbound connections from the server to the client, which fails through NAT. Passive mode is always preferred; it needs the passive port range open.

Open TCP 21 and the passive range with `services.dplaneos.ftp.openFirewall = true;` in `/etc/nixos/local.nix` (then rebuild), or with allow rules on the Firewall page (Security › Firewall).

### API

```
GET  /api/ftp/status        # vsftpd running state
GET  /api/ftp/config        # current configuration
PUT  /api/ftp/config        # update configuration
POST /api/ftp/start         # start vsftpd
POST /api/ftp/stop          # stop vsftpd
POST /api/ftp/restart       # restart vsftpd (applies config changes)
```

---

## MinIO (S3-Compatible Object Store)

MinIO turns a ZFS dataset into an S3-compatible object store accessible from the local network. Applications that use the AWS S3 SDK can read and write objects without modification.

Use MinIO when you need:
- S3-compatible access for applications (Nextcloud external storage, Rclone S3 targets, Proxmox backup repository)
- An on-premises object store that does not involve cloud egress costs
- Structured object access with access keys and bucket policies

MinIO is distinct from Cloud Sync - Cloud Sync uploads data to external providers. MinIO makes your NAS act as the S3 provider.

MinIO is not installed by default (it is large). Enable it with `services.dplaneos.s3.enable = true;` in `/etc/nixos/local.nix` and rebuild (`sudo nixos-rebuild switch --flake /etc/nixos#dplaneos --impure`). The daemon then runs it as the unit `dplaneos-minio`, with its settings in `/var/lib/dplaneos/minio/`, and starts it again at boot while it is enabled.

### Configure via UI

Sharing › S3 Object Storage.

Options:
- **Data directory**: the ZFS dataset path for bucket data (default `/tank/minio`; it must exist)
- **API port**: 9000
- **Console port**: 9001
- **Root user and password**: set a password before starting

### Access MinIO

MinIO is accessible at `http://NAS_IP:9000` (S3 API) and `http://NAS_IP:9001` (web console).

Create an application-specific access key:
```bash
mc alias set local http://NAS_IP:9000 minioadmin PASSWORD

mc mb local/mybucket
mc cp myfile.txt local/mybucket/
mc ls local/mybucket/
```

Or via the MinIO console (port 9001): Access Keys → Create Access Key.

### Rclone with MinIO

```bash
# rclone.conf entry
[minio-local]
type = s3
provider = Minio
access_key_id = YOUR_ACCESS_KEY
secret_access_key = YOUR_SECRET
endpoint = http://NAS_IP:9000
```

Then use `rclone` commands with the `minio-local:` remote.

### Firewall

Open the ports with allow rules on the Firewall page (Security › Firewall): 9000 (S3 API) for application servers, 9001 (console) for management networks only.

### API

```
GET  /api/s3/status         # MinIO running state
GET  /api/s3/config         # current configuration
PUT  /api/s3/config         # update data directory, ports, credentials
POST /api/s3/start          # start MinIO
POST /api/s3/stop           # stop MinIO
POST /api/s3/restart        # restart MinIO (applies config changes)
```
