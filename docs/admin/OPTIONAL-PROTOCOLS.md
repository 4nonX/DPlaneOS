# DPlaneOS Optional Protocols

This document covers protocols that are not enabled by default: iSCSI, NVMe-oF, FTP/FTPS, and MinIO. Each section explains what the protocol is for, how to enable it, and how to manage it.

Standard protocols (SMB, NFS) are covered in [ADMIN-GUIDE.md](ADMIN-GUIDE.md). For GitOps declaration of NVMe-oF, see [GITOPS-REFERENCE.md](../reference/GITOPS-REFERENCE.md#nvme-of-fabrics-optional).

---

## iSCSI (Block Storage over TCP)

iSCSI presents a ZFS zvol as a block device to a network initiator. The initiator sees the zvol as a raw disk and can format it with any filesystem. Typical use cases: booting ESXi, presenting block storage to a Windows server, or providing disks to VMs.

DPlaneOS uses the Linux kernel's `nvmet` subsystem for iSCSI via the configfs interface.

### Enable in NixOS

```nix
# In configuration.nix
services.dplaneos.iscsi.enable = true;
```

Run `nixos-rebuild switch`.

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
# or: apt install open-iscsi    # on the initiator (not the DPlaneOS host)

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

### Enable in NixOS

```nix
# In configuration.nix
services.dplaneos.nvmeof.enable = true;
```

The NixOS module loads `nvmet-tcp` and mounts configfs at `/sys/kernel/config/nvmet`.

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

### Enable in NixOS

```nix
services.dplaneos.ftp.enable = true;
```

### Configure via UI

Shares: FTP: Settings.

Key options:
- **Anonymous access**: disabled by default; enable only for public read-only shares
- **FTPS (TLS)**: enabled by default; requires a TLS certificate
- **Passive port range**: default 40000-40100; adjust if your firewall NAT has a different range
- **Upload directory**: the directory FTP users land in after login
- **Max connections per IP**: throttle per-client connection count

### Passive Mode and Firewall

FTP active mode requires inbound connections from the server to the client, which fails through NAT. Passive mode is always preferred.

Passive mode requires a range of ports to be open:
```yaml
# In state.yaml
system:
  firewall:
    tcp: [21, 990, 40000, 40001, 40002, ..., 40100]
```

For the exact passive port range, check the vsftpd configuration or the FTP settings page. The default range is 40000-40100 (101 ports).

If the NAS is behind NAT, also configure the external IP in vsftpd so passive mode announcements contain the correct address:
```
# /etc/vsftpd.conf (managed by daemon)
pasv_address=PUBLIC_IP
```

This is configurable in the UI: Shares: FTP: Advanced: Passive address.

### FTPS Certificate

The daemon uses the system TLS certificate (configured in Settings: System: HTTPS) for FTPS. No separate certificate configuration is needed.

### FTP Users

FTP uses the same local user accounts as the web UI. Users are chrooted to their home directory (`/mnt/pool/users/<username>`) by default. To allow a user to access a different path, configure it in Shares: FTP: User Paths.

### API

```
GET  /api/ftp/config        # current vsftpd configuration
POST /api/ftp/config        # update configuration
GET  /api/ftp/status        # vsftpd running state + connected clients
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

### Enable in NixOS

```nix
services.dplaneos.minio = {
  enable = true;
  dataDir = "/mnt/tank/minio";
};
```

The data directory must be on a ZFS dataset (not the boot disk).

### Configure via UI

Settings: MinIO: Enable MinIO.

Options:
- **Data directory**: ZFS dataset path for bucket data (must exist)
- **API port**: 9900 (default; do not conflict with the DPlaneOS daemon on 9000)
- **Console port**: 9901

The UI also shows the generated root credentials. Change them after first setup.

### Configure via Environment

The daemon manages MinIO via `/etc/minio.env`. To set custom configuration:

```bash
# /etc/minio.env (managed by daemon, do not edit directly)
MINIO_ROOT_USER=minioadmin
MINIO_ROOT_PASSWORD=<generated>
MINIO_VOLUMES=/mnt/tank/minio
```

### Access MinIO

MinIO is accessible at `http://NAS_IP:9900` (S3 API) and `http://NAS_IP:9901` (web console).

Create an application-specific access key:
```bash
mc alias set local http://NAS_IP:9900 minioadmin PASSWORD

mc mb local/mybucket
mc cp myfile.txt local/mybucket/
mc ls local/mybucket/
```

Or via the MinIO console (port 9901): Access Keys: Create Access Key.

### Rclone with MinIO

```bash
# rclone.conf entry
[minio-local]
type = s3
provider = Minio
access_key_id = YOUR_ACCESS_KEY
secret_access_key = YOUR_SECRET
endpoint = http://NAS_IP:9900
```

Then use `rclone` commands with the `minio-local:` remote.

### Firewall

```yaml
system:
  firewall:
    tcp: [9900, 9901]
```

Port 9900 (S3 API) should be accessible to application servers. Port 9901 (console) can be restricted to management networks.

### API

```
GET  /api/minio/status      # MinIO running state, version
POST /api/minio/enable      # start MinIO
POST /api/minio/disable     # stop MinIO
GET  /api/minio/config      # current configuration
POST /api/minio/config      # update data directory, ports
POST /api/minio/restart     # restart MinIO (applies config changes)
```
