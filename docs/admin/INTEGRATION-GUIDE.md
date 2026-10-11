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

There are two separate things a domain can do for DPlaneOS:

- **Sign-in to the web interface** with domain accounts: Directory › Directory Setup (the domain is used as an LDAP directory; no membership needed).
- **SMB shares for domain users**: the system becomes a **member of the domain**. Domain users then open the shares with their domain password (Kerberos or NTLM), own the files they create, and show up by name in `ls -l`, in share permissions and in ACLs. This section is about that.

A system is a member of **one** domain. Users of other domains come in through trusts between the domains.

### Before joining

- **DNS:** the system must use the domain's DNS server (Network › DNS). A member finds its domain controllers through DNS; the join refuses to start when it cannot.
- **Time:** the clocks of the system and the domain controllers must agree to within five minutes. Use the domain controller as the time server (System › Settings).
- **Host name:** at most 15 characters (System › Settings). It becomes the name of the computer account.
- An account that may join computers to the domain.

### Joining

Directory › **Domain Membership** › *Add domain*: the short (NetBIOS) name of the domain, e.g. `CORP`; the Kerberos realm, e.g. `CORP.EXAMPLE.COM`; optionally a domain controller (found through DNS when empty); and the range of user IDs the domain's users get on this system. Then **Join**, with the account's user name (without the domain) and password. The password is used once, passed to Samba on a pipe, and not stored.

What happens: the domain controllers are looked up, Samba's configuration is written for the domain, the system joins (`net ads join`), Samba is restarted and the new computer account is checked against the domain. If any step fails, the configuration goes back to what it was and the reason is shown.

```
POST /api/ldap/domains
{ "name": "CORP", "realm": "CORP.EXAMPLE.COM", "server": "",
  "idmap_backend": "rid", "idmap_low": 100000, "idmap_high": 999999 }

POST /api/ldap/domains/CORP/join          → { "job_id": "..." }
{ "username": "Administrator", "password": "...", "ou": "Servers/NAS" }

GET  /api/jobs/{job_id}                   the steps, and the result
GET  /api/directory/status                member of which domain; is the account accepted
GET  /api/ldap/domains/CORP/status        winbind, trust check, sample users and groups
```

### After joining

- Domain users sign in to shares as `CORP\alice` (or just `alice` from a domain computer). On the system they are `alice`; a local account of the same name takes precedence, so avoid giving local accounts the names of domain users.
- **Local accounts keep working**, but from other computers they must now be given with the system's name: `NASNAME\localuser`.
- Limit a share to a domain group with *Valid users*: `@"domain users"`, `@nas-team`.
- Files belong to the domain user and to the group `users`, like files of local users.
- Every 15 minutes the system checks that the domain still accepts its account. If not (the computer account was deleted or reset, or no controller is reachable), the alert `directory.trust_lost` is raised: domain users cannot open shares until that is repaired.

On the system:

```bash
wbinfo -t              # is the computer account accepted
wbinfo -u | head       # domain users
id alice               # user and group IDs of a domain user
```

### ID ranges and trusted domains

Each domain's users are mapped to a range of Unix user IDs. `rid` (the default) derives the ID from the user's number in the domain: the same on every DPlaneOS system that uses the same range. `ad` takes the IDs from the Unix attributes in the directory. Local users have IDs below 3000; ranges for domains start above that.

For a domain that the joined domain trusts, add it under Domain Membership **without joining it**: that gives its users a range of their own. Ranges must not overlap.

### Leaving

**Leave** on the domain, with an account that may remove the computer account. The system goes back to its local accounts. If the domain no longer exists or cannot be reached, tick *Leave even if the domain does not answer*; the computer account then stays behind in the domain.

### Limits

- **Failover:** Samba's session and lock state is not shared between nodes. SMB clients reconnect after a failover; byte-range locks are lost.
- The membership belongs to one node. In a cluster, join each node (each has its own computer account).
- No home directories are created for domain users.
- Tested against a Samba domain controller (automated, for every release: `nixos/tests/domain.nix`). Not tested by the project against Windows Server; the commands and protocols are the same, but report what you find.

---

## SMB / Samba

SMB shares work for local users and, when the system is a member of a domain, for domain users (see above). Users of an LDAP directory that is only used for sign-in to the web interface have no SMB access.

- **Failover:** in clusters, SMB clients disconnect and reconnect on failover. Byte-range locks are not preserved.
- **Reconnect time:** typically 5-30 seconds, depending on the client's retry settings.

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
