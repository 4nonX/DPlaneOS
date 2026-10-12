# DPlaneOS - Architecture Diagrams

Visual overview of the codebase. Render in any Mermaid-compatible viewer (GitHub, VS Code with Mermaid extension, or [mermaid.live](https://mermaid.live)).

---

## System Overview

```mermaid
%%{init: {'theme':'base', 'themeVariables': { 'primaryColor':'#b2dfdb', 'primaryTextColor':'#004d40', 'primaryBorderColor':'#00695c', 'lineColor':'#00695c', 'secondaryColor':'#e0f2f1', 'tertiaryColor':'#fff' }}}%%
flowchart LR
    subgraph Client
        Browser["Browser (React app)"]
    end

    subgraph Edge
        nginx["nginx :80 / :443"]
    end

    subgraph Backend["DPlaneOS Daemon"]
        dplaned["dplaned (Go) unix:/run/dplaneos/dplaned.sock"]
    end

    subgraph Data["Data and Runtime"]
        PostgreSQL["PostgreSQL (node-local)\n/var/lib/dplaneos/pgsql/"]
        ZFS["ZFS (kernel)"]
        Docker["Docker (socket)"]
        Services["Samba, NFS, block targets,\nnetwork, firewall"]
        LDAP["LDAP / AD / OIDC"]
    end

    Browser -->|"static + /api/*"| nginx
    nginx -->|"proxy /api/, /ws/"| dplaned
    nginx -->|"static files of the web interface"| Browser
    dplaned --> PostgreSQL
    dplaned --> ZFS
    dplaned --> Docker
    dplaned --> Services
    dplaned --> LDAP
```

---

## Daemon Internal Structure (Go)

```mermaid
%%{init: {'theme':'base', 'themeVariables': { 'primaryColor':'#b2dfdb', 'primaryTextColor':'#004d40', 'primaryBorderColor':'#00695c', 'lineColor':'#00695c', 'secondaryColor':'#e0f2f1' }}}%%
flowchart TB
    subgraph cmd["cmd/"]
        main["dplaned/main.go - flags, database, router, background loops"]
    end

    subgraph api["internal/ - the API"]
        handlers["handlers/ - one file per feature of the interface"]
        middleware["middleware/ - permissions, second factor"]
        security["security/ - sessions, RBAC, command allowlist, client address"]
        audit["audit/ - buffered log, HMAC chain"]
        jobs["jobs/ - long-running operations"]
        websocket["websocket/ - live events for the interface"]
    end

    subgraph storage["internal/ - storage"]
        libzfs["libzfs/ - ZFS operations (zfs and zpool commands)"]
        zfs_pkg["zfs/ - ZED listener, pool heartbeat, status parsing"]
        storageops["storageops/ - one storage operation at a time per pool"]
        hardware["hardware/ - SMART, enclosures, GPUs"]
        uevent["uevent/ - kernel device events (disks added and removed)"]
        iscsit["iscsit/ - iSCSI targets (LIO)"]
        nvmet["nvmet/ - NVMe-oF exports"]
        smbconf["smbconf/ - Samba configuration"]
    end

    subgraph system["internal/ - system"]
        nixwriter["nixwriter/ - system settings (state file)"]
        sysapply["sysapply/ - applies settings: host name, time, firewall, SSH"]
        netconf["netconf/ - network configuration (systemd-networkd)"]
        netlinkx["netlinkx/ - netlink (addresses, links)"]
        cmdutil["cmdutil/ - runs programs through the allowlist"]
        database["database/ - migrations (goose)"]
        secrets["secrets/ - encryption of stored secrets"]
    end

    subgraph cluster["internal/ - GitOps and clusters"]
        gitops["gitops/ - state.yaml parser, plan, apply, drift"]
        configstore["configstore/ - configuration exchange between nodes"]
        quorum["quorum/ - corosync membership and votes"]
        groups["groups/ - storage groups: ownership, moves, failover, replication"]
        diskres["diskres/ + scsipr/ - disk reservations (SCSI-3)"]
        ha["ha/ - watchdog, power fencing"]
    end

    main --> handlers
    main --> groups
    main --> gitops
    handlers --> middleware
    handlers --> security
    handlers --> audit
    handlers --> jobs
    handlers --> libzfs
    handlers --> iscsit
    handlers --> nvmet
    handlers --> smbconf
    handlers --> nixwriter
    handlers --> netconf
    handlers --> gitops
    handlers --> groups
    libzfs --> cmdutil
    nixwriter --> sysapply
    gitops --> libzfs
    gitops --> netconf
    groups --> quorum
    groups --> configstore
    groups --> diskres
    groups --> iscsit
    groups --> nvmet
    groups --> ha
    cmdutil --> security
```

---

## Request Lifecycle

```mermaid
%%{init: {'theme':'base'}}%%
sequenceDiagram
    participant B as Browser
    participant N as nginx
    participant M as Middleware
    participant H as Handler
    participant C as PostgreSQL / allowlisted command / kernel

    B->>N: HTTP request
    N->>M: proxy_pass unix socket
    M->>M: session validation
    M->>M: CSRF check (mutating requests)
    M->>M: rate limit (100 req/min per IP)
    M->>M: RBAC permission check
    M->>H: dispatch to handler
    H->>H: input allowlist validation
    H->>C: PostgreSQL query, allowlisted command, or configuration write
    C-->>H: result
    H->>H: audit log entry
    H-->>B: JSON response
```

---

## Disk Event Flow (Hot-Swap)

```mermaid
%%{init: {'theme':'base'}}%%
flowchart TD
    A["Disk connected"] --> B["Kernel device event (netlink)"]
    B --> C["internal/uevent: the daemon listens itself\n(no udev rule, no helper script)"]
    C --> D["Wait until the disk reports its size\nand has a by-id link (up to 15 s)"]
    D --> E["Describe the device\n(by-id, WWN, size, type)"]
    E --> F["Update the disk registry (PostgreSQL)"]
    F --> G["Broadcast diskAdded to the interface"]
    G --> I{"Belonged to a known pool?"}
    I -->|"pool suspended"| J1["zpool clear: I/O resumes"]
    I -->|"pool not imported"| J2["import the pool"]
    I -->|"no"| K
    J1 --> K["Broadcast pool health"]
    J2 --> K
    K --> L{"Pool has a faulted device?"}
    L -->|Yes| M["Offer the new disk as a replacement"]
    L -->|No| N["Done"]
    M --> N
```

---

## ZED Hook Flow

```mermaid
%%{init: {'theme':'base'}}%%
flowchart TD
    A["ZFS kernel event\n(disk fault, scrub, resilver, TRIM, etc.)"] --> B["ZED: zed.d/all-dplaneos-notify.sh"]
    B --> C["Log to syslog"]
    B --> D{"Daemon socket\n/run/dplaneos/dplaneos.sock"}
    D -->|Connected| E["zed_listener.go goroutine\nparses zfs_event line"]
    E --> F["zedTypedDispatch\n(20+ specific subclasses)"]
    F --> G["WebSocket hub broadcast\n(structured JSON event)"]
    F --> H{"Long-running operation?"}
    H -->|scrub / resilver| I["zedFastProgressPoll\n(2s poll, zpool status)"]
    H -->|trim| J["zedTrimProgressPoll\n(2s poll, trim % / ETA)"]
    I --> G
    J --> G
    F --> K{"Severity >= warning?"}
    K -->|Yes| L["Alert dispatcher\n(SMTP / Webhook / Telegram)"]
    D -->|Not connected| P["Log to syslog only"]
```

---

## Authentication Flow

### Local / LDAP Login

```mermaid
%%{init: {'theme':'base'}}%%
sequenceDiagram
    participant U as User
    participant A as /api/auth/login
    participant T as /api/auth/totp/verify
    participant S as sessionMiddleware

    U->>A: POST {username, password}
    A->>A: bcrypt verify
    alt TOTP enabled
        A-->>U: {session: pending_totp}
        U->>T: POST {code}
        T->>T: TOTP verify (±1 window)
        T-->>U: {session: active, csrf_token}
    else TOTP not enabled
        A-->>U: {session: active, csrf_token}
    end
    U->>S: request with X-Session-ID header
    S->>S: token format + DB lookup + username match
    S->>S: RBAC permission check
    S-->>U: response or 401/403
```

### OIDC SSO Login (Authorization Code + PKCE)

```mermaid
%%{init: {'theme':'base'}}%%
sequenceDiagram
    participant B as Browser (SPA)
    participant D as dplaned
    participant I as IdP

    B->>D: GET /api/auth/oidc/start
    D->>D: generate state, nonce, PKCE verifier; store in oidc_state
    D-->>B: 302 → IdP authorization endpoint
    B->>I: GET /authorize?code_challenge=S256...
    I-->>B: 302 → /api/auth/oidc/callback?code=...&state=...
    B->>D: GET /api/auth/oidc/callback
    D->>D: DELETE oidc_state WHERE state=? (atomic, replay-safe)
    D->>I: POST /token {code, code_verifier, ...}
    I-->>D: {id_token, access_token}
    D->>D: verify ID token (sig, iss, aud, nonce, exp)
    D->>D: resolve or provision user
    D->>D: mint session; store in oidc_handoff (2-min TTL)
    D-->>B: 302 → /login?oidc_handoff=<code>
    B->>D: POST /api/auth/oidc/exchange {handoff_code}
    D->>D: DELETE oidc_handoff WHERE code=? RETURNING session_id
    D-->>B: {session_id, username, expires_at}
    B->>B: storeSession(); validateSession(); navigate to /
```

