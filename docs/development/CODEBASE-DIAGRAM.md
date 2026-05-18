# DPlaneOS - Architecture Diagrams

Visual overview of the codebase. Render in any Mermaid-compatible viewer (GitHub, VS Code with Mermaid extension, or [mermaid.live](https://mermaid.live)).

---

## System Overview

```mermaid
%%{init: {'theme':'base', 'themeVariables': { 'primaryColor':'#b2dfdb', 'primaryTextColor':'#004d40', 'primaryBorderColor':'#00695c', 'lineColor':'#00695c', 'secondaryColor':'#e0f2f1', 'tertiaryColor':'#fff' }}}%%
flowchart LR
    subgraph Client
        Browser["Browser (Material Design 3)"]
    end

    subgraph Edge
        nginx["nginx :80 / :443"]
    end

    subgraph Backend["DPlaneOS Daemon"]
        dplaned["dplaned (Go) :9000"]
    end

    subgraph Data["Data and Runtime"]
        PostgreSQL["PostgreSQL / Patroni\n/var/lib/dplaneos/pgsql/"]
        ZFS["ZFS (kernel)"]
        Docker["Docker (socket)"]
        LDAP["LDAP / AD"]
    end

    Browser -->|"static + /api/*"| nginx
    nginx -->|"proxy /api/, /ws/"| dplaned
    nginx -->|"serve /opt/dplaneos/app"| Browser
    dplaned --> PostgreSQL
    dplaned --> ZFS
    dplaned --> Docker
    dplaned --> LDAP
```

---

## Daemon Internal Structure (Go)

```mermaid
%%{init: {'theme':'base', 'themeVariables': { 'primaryColor':'#b2dfdb', 'primaryTextColor':'#004d40', 'primaryBorderColor':'#00695c', 'lineColor':'#00695c', 'secondaryColor':'#e0f2f1' }}}%%
flowchart TB
    subgraph cmd["cmd/"]
        main["dplaned/main.go - flags, DB init, router setup"]
        fenced["dplane-fenced/main.go - SCSI-3 PR reservation manager"]
    end

    subgraph internal["internal/"]
        handlers["handlers/ - ~55 handler files (one per feature)"]
        middleware["middleware/ - logging, session, rate limit"]
        audit["audit/ - buffered logger, HMAC chain"]
        security["security/ - session validation, RBAC, command whitelist"]
        ha["ha/ - cluster manager, fenced client, promote/demote"]
        zfs_pkg["zfs/ - pool heartbeat, ZED listener, status parsing"]
        libzfs["libzfs/ - cgo libzfs bindings (+ subprocess fallback)"]
        storageops["storageops/ - transactional storage op guard"]
        jobs["jobs/ - async job store (in-memory)"]
        cmdutil["cmdutil/ - safe exec.Command wrappers"]
        netlinkx["netlinkx/ - netlink syscalls (no CGO)"]
        database["database/migrations/ - SQL migration files"]
        gitops["gitops/ - state.yaml parser, diff engine, apply engine"]
        hardware["hardware/ - SES enclosure, SMART timer management"]
        acl["acl/ - NFSv4 ACL parsing and validation"]
        scsipr["scsipr/ - SCSI-3 persistent reservation primitives"]
        nvmet["nvmet/ - NVMe-oF target configuration"]
        nixwriter["nixwriter/ - NixOS config file writer"]
        composegpu["composegpu/ - Docker Compose + GPU validation"]
    end

    main --> handlers
    main --> middleware
    main --> gitops
    handlers --> audit
    handlers --> security
    handlers --> cmdutil
    handlers --> libzfs
    handlers --> storageops
    handlers --> jobs
    handlers --> netlinkx
    handlers --> database
    handlers --> ha
    handlers --> zfs_pkg
    handlers --> hardware
    handlers --> acl
    handlers --> nvmet
    handlers --> composegpu
    gitops --> libzfs
    gitops --> nixwriter
    ha --> scsipr
    fenced --> scsipr
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
    participant C as libzfs / exec.Command / PostgreSQL

    B->>N: HTTP request
    N->>M: proxy_pass :9000
    M->>M: session validation
    M->>M: CSRF check (mutating requests)
    M->>M: rate limit (100 req/min per IP)
    M->>M: RBAC permission check
    M->>H: dispatch to handler
    H->>H: input allowlist validation
    H->>C: PostgreSQL query, libzfs call, or exec.Command
    C-->>H: result
    H->>H: audit.LogAction()
    H-->>B: JSON response
```

---

## Disk Event Flow (Hot-Swap)

```mermaid
%%{init: {'theme':'base'}}%%
flowchart TD
    A["Physical disk connected"] --> B["udev: 99-dplaneos-hotswap.rules"]
    B --> C["install/scripts/notify-disk-added.sh"]
    C --> D["POST /api/internal/disk-event\n(localhost only)"]
    D --> E["Enrich device\n(by-id, WWN, size, type, temp)"]
    E --> F["Upsert disk registry (PostgreSQL)"]
    F --> G["Broadcast diskAdded WS event"]
    G --> H["2 s settle delay"]
    H --> I{"Pool importable?"}
    I -->|Yes| J["libzfs.PoolImportAll(/dev/disk/by-id)"]
    J --> K["Broadcast poolHealthChanged WS event"]
    I -->|No| K
    K --> L{"Faulted vdevs exist?"}
    L -->|Yes| M["Broadcast diskReplacementAvailable\nPre-populate Replace modal in UI"]
    L -->|No| N["Done"]
    M --> N
```

---

## ZED Hook Flow

```mermaid
%%{init: {'theme':'base'}}%%
flowchart TD
    A["ZFS kernel event\n(disk fault, scrub, resilver, TRIM, etc.)"] --> B["ZED: zed.d/dplaneos-notify.sh"]
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
    B --> M{"Severity = critical\nand daemon down?"}
    M -->|Yes| N["Read telegram_config\nfrom PostgreSQL directly"]
    N --> O["POST to Telegram Bot API\n(bypass daemon)"]
```

---

## Authentication Flow

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

