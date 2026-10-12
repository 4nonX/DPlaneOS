# DPlaneOS Integration Guide

This document covers integrating DPlaneOS with external systems. Each section is honest about what works, what has known limitations, and what to use instead when the native path is incomplete.

## Monitoring (Prometheus / Grafana / Datadog)

DPlaneOS serves its metrics in the Prometheus/OpenMetrics text format at `GET /metrics`, on the web interface's address, to a request that carries an API token.

1. **Security › API Tokens**: create a token with the scope *read* for the monitoring system. It is shown once.
2. Point the collector at `https://<system>/metrics` with that token.

### Prometheus scrape config example

```yaml
scrape_configs:
  - job_name: dplaneos
    scheme: https
    metrics_path: /metrics
    authorization:
      type: Bearer
      credentials: dpl_...            # or credentials_file
    tls_config:
      insecure_skip_verify: true      # with the system's self-signed certificate; better: ca_file, or a trusted certificate
    static_configs:
      - targets: ['nas.example.com']
```

Without a token the answer is `401`. In a cluster, scrape every node: each reports what it sees and serves.

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
| `dplaneos_zfs_arc_size_bytes`, `dplaneos_zfs_arc_max_bytes` | ZFS ARC current and maximum size |
| `dplaneos_ha_enabled`, `dplaneos_ha_quorum` | In a cluster; in the part that has quorum |
| `dplaneos_ha_expected_votes`, `dplaneos_ha_online_nodes` | Votes expected; members this node sees |
| `dplaneos_ha_auto_failover`, `dplaneos_ha_self_fence_armed` | Automatic failover possible; this node would shut itself out when it loses quorum |
| `dplaneos_group_serving{group}`, `dplaneos_group_owner_here{group}` | The storage group is served; by this node |
| `dplaneos_group_epoch{group}`, `dplaneos_group_problems{group}` | Ownership changes so far; open problems of the group |
| `dplaneos_replication_enabled{schedule,dataset}` | 1 if the schedule is enabled |
| `dplaneos_replication_last_run_timestamp_seconds{schedule,dataset}` | Last run |
| `dplaneos_replication_last_success{schedule,dataset}` | 1 if the last run succeeded |
| `dplaneos_bmc_scrape_ok`, `dplaneos_bmc_chassis_power` | The BMC answered; chassis power is on |
| `dplaneos_bmc_sensor_value{...}`, `dplaneos_bmc_sensor_ok{...}` | BMC sensor readings and their state (when a BMC is configured) |
| `dplaneos_memory_total_bytes`, `dplaneos_memory_available_bytes`, `dplaneos_memory_used_bytes` | Memory |
| `dplaneos_cpu_iowait_ratio` | CPU I/O wait fraction (0-1) |
| `dplaneos_load_1` / `_5` / `_15` | Load averages |
| `dplaneos_inotify_active_instances`, `dplaneos_inotify_max_watches` | File watch use and limit |
| `dplaneos_build_info{version}`, `dplaneos_scrape_timestamp_ms` | Version; time of the scrape |

Alert rules for a cluster are in [`prometheus/ha-alerts.yml`](../../prometheus/ha-alerts.yml).

### SNMP

There is no SNMP agent. Where SNMP is required, convert from Prometheus on the monitoring side.

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

## iSCSI and NVMe-oF clients

Setting up targets and exports is described in [Optional Protocols](OPTIONAL-PROTOCOLS.md). What a client should know:

**The disk keeps its identity.** The serial number of an iSCSI LUN, and the UUID and NGUID of an NVMe namespace, are derived from the name of the target or export. An initiator that reconnects after a restart of the NAS, or after the target moved to another node, finds the same disk and carries on; file systems on it stay mounted.

**Failover in a cluster.** A target or export of a volume in a storage group is served by the node that owns the group. On a move the old owner stops serving it, the pools move, and the new owner serves it again, on the group's floating address. For the initiator this is a connection that drops and comes back after the failover time; I/O waits meanwhile and continues. Set the initiator's timeouts above the failover time of the cluster (HA page), for example:

```bash
# iSCSI (open-iscsi), in /etc/iscsi/iscsid.conf, before logging in
node.session.timeo.replacement_timeout = 180

# NVMe over TCP
nvme connect -t tcp -a <floating address> -s 4420 -n <subsystem NQN> --ctrl-loss-tmo=600 --reconnect-delay=5
```

Point initiators at the floating address of the group, not at a node. Both nodes never serve the same volume at once, so there is no second path to configure: multipath (ALUA, ANA) is not used.

**NVMe-oF in state.yaml:**

```yaml
fabrics:
  nvme:
    - subsystem_nqn: nqn.2026-01.lan.nas:my-volume
      zvol: tank/nvme/vol0
      listen_addr: 0.0.0.0
      listen_port: 4420
      allow_any_host: false
      host_nqns:
        - nqn.2014-08.org.nvmexpress:uuid:YOUR-HOST-UUID
```

iSCSI targets are set up in the web interface or through the API; they are not part of `state.yaml`.

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

## Failover timing

An automatic takeover starts the watchdog timeout plus 15 seconds after the owner left the quorate partition: the time the old owner needs to have reset itself. Set the watchdog timeout under HA › Watchdog and power fencing. Client timeouts (iSCSI, NVMe, NFS) should be longer than that plus the time the pools need to import.

## Containers and GPUs

**Intel and AMD graphics (VA-API, Quick Sync).** The kernel drivers are part of the system; the render device appears as `/dev/dri/renderD128`. Pass it to the container, which brings the user-space driver itself (the Jellyfin, Plex and similar images do):

```yaml
# compose file (Jellyfin as an example)
services:
  jellyfin:
    image: jellyfin/jellyfin
    devices:
      - /dev/dri/renderD128:/dev/dri/renderD128
    group_add:
      - "GID"        # the numeric group that owns the device on the host: ls -ln /dev/dri
```

Containers › Docker › *GPU / hardware* shows the devices the system has, and a stack that asks for one that is not there is refused with a message instead of failing at start.

**NVIDIA.** The proprietary driver and the NVIDIA container toolkit are not part of the released images, so NVIDIA cards cannot be used by containers on a system installed from them. An image you build yourself can include them (`services.dplaneos.docker.enableNvidia`, plus the NVIDIA driver in your configuration; see [the NixOS side](../../nixos/README.md)).

DPlaneOS does not run virtual machines; there is no GPU passthrough to VMs.
