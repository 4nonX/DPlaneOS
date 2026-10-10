# DPlaneOS Integration Guide

This document covers integrating DPlaneOS with external systems. Each section is honest about what works, what has known limitations, and what to use instead when the native path is incomplete.

## Monitoring (Prometheus / Grafana / Datadog)

DPlaneOS exposes a Prometheus/OpenMetrics endpoint at `GET /metrics` (no authentication required). Scrape it with any standard collector.

### Metrics exposed

| Metric | Description |
|--------|-------------|
| `dplaneos_zfs_pool_size_bytes{pool}` | Total pool capacity |
| `dplaneos_zfs_pool_alloc_bytes{pool}` | Allocated bytes |
| `dplaneos_zfs_pool_free_bytes{pool}` | Free bytes |
| `dplaneos_zfs_pool_healthy{pool}` | Health: 1=ONLINE 0.75=DEGRADED 0.5=FAULTED 0=UNAVAIL |
| `dplaneos_zfs_pool_scan_percent{pool}` | Scrub/resilver progress (-1 if no active scan) |
| `dplaneos_zfs_dataset_used_bytes{dataset}` | Dataset used bytes (compressed) |
| `dplaneos_zfs_dataset_available_bytes{dataset}` | Dataset available bytes |
| `dplaneos_zfs_dataset_quota_bytes{dataset}` | Dataset quota (0 = no quota) |
| `dplaneos_zfs_arc_size_bytes` | ZFS ARC current size |
| `dplaneos_zfs_arc_max_bytes` | ZFS ARC maximum size |
| `dplaneos_ha_enabled` | 1 if HA is configured |
| `dplaneos_ha_peer_count` | Number of cluster peers |
| `dplaneos_ha_quorum` | 1 if cluster has quorum |
| `dplaneos_ha_last_failover_timestamp_seconds` | Unix timestamp of last failover |
| `dplaneos_ha_peer_health{peer,role}` | Per-peer health: 1=healthy 0.5=degraded 0=unreachable |
| `dplaneos_replication_enabled{schedule,dataset}` | 1 if schedule is enabled |
| `dplaneos_replication_last_run_timestamp_seconds{schedule,dataset}` | Last run timestamp |
| `dplaneos_replication_last_success{schedule,dataset}` | 1 if last run succeeded |
| `dplaneos_memory_total_bytes` | Total physical memory |
| `dplaneos_memory_used_bytes` | Used memory |
| `dplaneos_cpu_iowait_ratio` | CPU IO wait fraction (0-1) |
| `dplaneos_load_1` / `_5` / `_15` | Load averages |
| `dplaneos_build_info{version}` | Build metadata |

### Prometheus scrape config example

```yaml
scrape_configs:
  - job_name: dplaneos
    static_configs:
      - targets: ['nas.example.com:80']
    metrics_path: /metrics
    scheme: http  # or https if TLS is configured
```

### SNMP

There is no native SNMP MIB. For environments that require SNMP, use `snmp_exporter` with a custom module that scrapes `/metrics` and re-exposes via SNMP, or use Prometheus federation.

---

## Active Directory / Windows Domain

DPlaneOS supports full Active Directory domain membership with winbind, Kerberos, and NSS integration. The implementation covers:

- Samba in ADS security mode with machine account
- Kerberos TGT acquisition and background ticket renewal
- Winbind for SID-to-UID/GID mapping (multi-forest with configurable IDMAP backends)
- NSS integration so `ls -l`, `id`, `stat`, and SMB ACL display resolve AD names
- Per-forest IDMAP range configuration (RID, AD, TDB, or autorid backends)

### Joining a Domain

**Via UI:** Directory Services -> Active Directory -> Add Domain -> Join

1. Register the domain: provide the realm (FQDN, uppercase, e.g. `CORP.EXAMPLE.COM`), NetBIOS workgroup, and a domain controller address.
2. Click Join. Provide an AD admin username and password. The join flow:
   - Validates DNS SRV records (`_kerberos._tcp.REALM`, `_ldap._tcp.REALM`) to confirm the DC is reachable
   - Runs `kinit <admin>@REALM` via stdin (password never appears in process args)
   - Runs `net ads join -k` (Kerberos-authenticated)
   - Updates the NixOS config to enable ADS security mode, configure IDMAP, and activate winbind + NSS
   - Starts background Kerberos ticket renewal (every 15 minutes)

**Via API:**
```
POST /api/ldap/domains
{ "name": "CORP", "realm": "CORP.EXAMPLE.COM", "server": "dc1.corp.example.com",
  "idmap_backend": "rid", "idmap_low": 10000, "idmap_high": 999999 }

POST /api/ldap/domains/CORP/join
{ "username": "Administrator", "password": "..." }
```

### Verifying the Join

```
GET /api/ldap/domains/CORP/status
```

Returns: winbind daemon ping, machine account trust check (`wbinfo -t`), DC online status, and sample AD users and groups. If trust_ok is true and sample_users is populated, NSS resolution is working.

**On the NAS via SSH:**
```bash
wbinfo -t                    # machine account trust check
wbinfo -u | head -10         # list AD users
id CORP\\username             # verify UID/GID resolution
getent passwd CORP\\username  # verify NSS lookup
```

### IDMAP Configuration

Each AD forest needs an IDMAP backend and UID/GID range. The RID backend is the recommended default for single-forest setups (maps RIDs to UIDs predictably across reboots). For multi-forest or when RID-based mapping is not suitable, use the AD backend (requires RFC2307 attributes in AD) or autorid.

| Backend | Use case |
|---------|----------|
| `rid` | Single forest, most deployments |
| `ad` | Forest with RFC2307 Unix attributes in AD |
| `autorid` | Multiple forests, non-overlapping ranges managed automatically |
| `tdb` | Catch-all / fallback for unmapped SIDs |

### SMB with AD ACLs

Once the domain is joined and winbind is running, Samba shares use AD SIDs for file ACLs. Windows clients see domain users and groups in the ACL editor. NFSv4 clients on Linux see resolved usernames in `ls -l` output via the NSS winbind integration.

### Known limitations

- **CTDB**: In HA configurations, Samba state is not distributed. SMB clients disconnect and reconnect on failover. Byte-range lock state is lost. This is the same behavior as TrueNAS active-passive HA.
- **Backup DC**: Only one domain controller is stored per domain. Add a secondary DC to your DNS SRV records for automatic failover at the DNS level.
- **Home directory creation**: No automatic home directory creation on first login (`pam_mkhomedir`). Users need home directories created manually or via the Dataset UI.

### Leaving a Domain

```
POST /api/ldap/domains/CORP/leave
{ "username": "Administrator", "password": "..." }
```

Runs `net ads leave -k` and clears the join state. The NixOS config reverts Samba to `user` security mode on next rebuild.

---

## SMB / Samba

SMB shares work for local and LDAP-authenticated users. Limitations:

- **No CTDB**: In HA configurations, SMB clients disconnect and reconnect on failover. Byte-range locks are not preserved.
- **No Winbind**: AD SID-to-UID mapping is not available.
- **Reconnect time**: Typically 5-30 seconds depending on client retry settings.

Applications that depend on lock continuity (Outlook PST files, Access databases, certain CAD tools) will experience errors on failover. For these workloads, pre-schedule failovers during maintenance windows and notify connected users.

---

## NVMe-oF

NVMe-oF over TCP is fully supported. ANA (Asymmetric Namespace Access) is supported for multi-path configurations.

### Setup in state.yaml

```yaml
fabrics:
  nvme:
    - subsystem_nqn: nqn.2024-01.io.dplane:my-volume
      zvol: tank/nvme/vol0
      transport: tcp
      listen_addr: 0.0.0.0
      listen_port: 4420
      allow_any_host: false
      host_nqns:
        - nqn.2014-08.org.nvmexpress:uuid:YOUR-HOST-UUID
      ana_enabled: true
      ana_groups:
        - group_id: 1
          namespace_id: 1
          state: optimized
```

### Connecting from a Linux initiator

```bash
modprobe nvme-tcp
nvme discover -t tcp -a <nas-ip> -s 4420
nvme connect -t tcp -a <nas-ip> -s 4420 -n nqn.2024-01.io.dplane:my-volume
```

---

## iSCSI

iSCSI with CHAP and ACL-based access control is supported. ALUA (Asymmetric Logical Unit Access) is supported for HA path management.

### HA failover with ALUA

On a planned move, a storage group's NVMe-oF exports are removed on the old owner before its pools are released and created on the new owner once they are imported; clients using multipath reconnect through the floating address.

For single-path iSCSI, clients will disconnect and reconnect on failover. This is equivalent to SMB failover behavior.

---

## GitOps / Automation

DPlaneOS manages configuration through `state.yaml`. This can be committed to a Git repository and pushed to trigger reconciliation.

### Integrate with CI/CD

The daemon accepts `-apply` to apply a state file and exit non-zero on failure. This allows GitOps pipelines to drive configuration changes:

```bash
# In CI:
scp state.yaml nas:/var/lib/dplaneos/gitops/state.yaml
ssh nas 'dplaned -apply -gitops-state /var/lib/dplaneos/gitops/state.yaml'
```

### Direct API

All operations available in the UI are available via the REST API. The API token system supports resource-level allowlists so CI/CD tokens can be scoped to only the endpoints they need.

---

## HA Timing Tuning

The failover threshold and hysteresis window are now runtime-configurable (changed in v14.0.0):

```
GET  /api/ha/timing  - read current values
POST /api/ha/timing  - update values (requires AAL2)
```

Failover timing follows the watchdog: the takeover starts the watchdog timeout plus 15 seconds after the owner left the quorate partition (set the timeout under HA › Watchdog and power fencing).

## Docker and Hardware Video Transcoding

DPlaneOS ships Docker and docker-compose in the running system. Containers have full access to the host network and can mount ZFS datasets as volumes.

### Intel hardware transcoding (Jellyfin, Plex, Handbrake)

Intel VA-API hardware video transcoding is supported via `intel-media-driver` (iHD, for Gen 9-12 including Raptor Lake) and `intel-compute-runtime` (OpenCL). These are included in the running system by default:

```nix
hardware.graphics = {
  enable        = true;
  extraPackages = [ pkgs.intel-media-driver pkgs.intel-compute-runtime ];
};
```

To use hardware transcoding in a Docker container, pass the render device:

```yaml
# docker-compose.yml (Jellyfin example)
devices:
  - /dev/dri/renderD128:/dev/dri/renderD128
environment:
  - LIBVA_DRIVER_NAME=iHD
```

**Verify VA-API is working on the host before configuring containers:**

```bash
nix-shell -p libva-utils --run "vainfo"
# Should show iHD driver and VAEntrypointEncSlice / VAEntrypointVLD profiles
```

### Offline installs and hardware transcoding

The installer ISO embeds the core DPlaneOS system for airgapped installation but excludes `intel-media-driver` and `intel-compute-runtime` to keep the ISO within a manageable size (~2 GB). These packages grew substantially in nixpkgs 26.05 (~400 MB combined).

**On a system installed from ISO without internet access:**

Hardware transcoding will not be available immediately after installation. To enable it once the system is on the network:

```bash
nixos-rebuild switch
```

This fetches the packages from `cache.nixos.org` and activates VA-API. All other NAS functionality (ZFS, SMB, NFS, iSCSI, NVMe-oF, HA, Docker) works immediately from an offline install.

**On a system installed from ISO with internet access**, or on any system that was deployed via GitOps (not from ISO), the packages are present from the first boot.

### GPU passthrough to VMs

GPU passthrough (VFIO) to virtual machines or containers does not require `intel-media-driver` or `intel-compute-runtime` on the host. The host needs:

- `kvm-intel` kernel module (included)
- IOMMU enabled: add `intel_iommu=on iommu=pt` to `boot.kernelParams` in your NixOS configuration
- The GPU bound to `vfio-pci` before the host driver claims it

The guest VM installs its own GPU drivers. No special host-side GPU software is required beyond the VFIO kernel modules.
