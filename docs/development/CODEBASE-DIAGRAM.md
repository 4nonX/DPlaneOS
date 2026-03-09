# D-PlaneOS — Architecture Diagrams

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

    subgraph Backend["D-PlaneOS Daemon"]
        dplaned["dplaned (Go) :9000"]
    end

    subgraph Data["Data and Runtime"]
        SQLite["SQLite WAL\n/var/lib/dplaneos/"]
        ZFS["ZFS (kernel)"]
        Docker["Docker (socket)"]
        LDAP["LDAP / AD"]
    end

    Browser -->|"static + /api/*"| nginx
    nginx -->|"proxy /api/, /ws/"| dplaned
    nginx -->|"serve /opt/dplaneos/app"| Browser
    dplaned --> SQLite
    dplaned --> ZFS
    dplaned --> Docker
    dplaned --> LDAP
```

---

## Daemon Internal Structure (Go)

```mermaid
%%{init: {'theme':'base', 'themeVariables': { 'primaryColor':'#b2dfdb', 'primaryTextColor':'#004d40', 'primaryBorderColor':'#00695c', 'lineColor':'#00695c', 'secondaryColor':'#e0f2f1' }}}%%
flowchart TB
    subgraph cmd["cmd/dplaned"]
        main["main.go — flags, DB init, router setup"]
        schema["schema.go — SQLite schema migrations"]
    end

    subgraph internal["internal/"]
        handlers["handlers/ — ~50 handler files (one per feature)"]
        middleware["middleware/ — logging, session, rate limit"]
        audit["audit/ — buffered logger, HMAC chain"]
        security["security/ — session validation, RBAC, command whitelist"]
        ha["ha/ — cluster manager (active/standby)"]
        zfs_pkg["zfs/ — pool heartbeat"]
        jobs["jobs/ — async job store (in-memory)"]
        cmdutil["cmdutil/ — safe exec.Command wrappers"]
        netlinkx["netlinkx/ — netlink syscalls (no CGO)"]
        database["database/migrations/ — SQL migration files"]
    end

    main --> handlers
    main --> middleware
    handlers --> audit
    handlers --> security
    handlers --> cmdutil
    handlers --> jobs
    handlers --> netlinkx
    handlers --> database
    handlers --> ha
    handlers --> zfs_pkg
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
    participant C as exec.Command / SQLite / ZFS

    B->>N: HTTP request
    N->>M: proxy_pass :9000
    M->>M: session validation
    M->>M: CSRF check (mutating requests)
    M->>M: rate limit (100 req/min per IP)
    M->>M: RBAC permission check
    M->>H: dispatch to handler
    H->>H: input allowlist validation
    H->>C: SQLite query or exec.Command
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
    E --> F["Upsert disk registry (SQLite)"]
    F --> G["Broadcast diskAdded WS event"]
    G --> H["2 s settle delay"]
    H --> I{"Pool importable?"}
    I -->|Yes| J["zpool import -d /dev/disk/by-id <pool>"]
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
    A["ZFS kernel event\n(disk fault, scrub, resilver, etc.)"] --> B["ZED: zed.d/dplaneos-notify.sh"]
    B --> C["Write JSON event file\n/var/lib/dplaneos/notifications/"]
    B --> D["Log to syslog"]
    B --> E{"Daemon socket\n/run/dplaneos/dplaneos.sock"}
    E -->|Connected| F["Send zfs_event message\n(non-blocking, 2 s timeout)"]
    B --> G{"Severity = critical?"}
    G -->|Yes| H["Read telegram_config from SQLite"]
    H --> I["POST to Telegram Bot API"]
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
