# D-PlaneOS Codebase — Architecture Diagrams

Visual overview of the D-PlaneOS v3.2.1 codebase. Render this file in any Mermaid-compatible viewer (e.g. GitHub, VS Code with Mermaid extension, or [mermaid.live](https://mermaid.live)).

---


**Mermaid version:**

```mermaid
%%{init: {'theme':'base', 'themeVariables': { 'primaryColor':'#b2dfdb', 'primaryTextColor':'#004d40', 'primaryBorderColor':'#00695c', 'lineColor':'#00695c', 'secondaryColor':'#e0f2f1', 'tertiaryColor':'#fff' }}}%%
flowchart LR
    subgraph Client
        Browser["Browser (Material Design 3)"]
    end

    subgraph Edge
        nginx["nginx reverse proxy :80"]
    end

    subgraph Backend["D-PlaneOS Daemon"]
        dplaned["dplaned (Go) :9000 171 API routes"]
    end

    subgraph Data["Data & Runtime"]
        SQLite["SQLite (WAL, /var/lib/dplaneos)"]
        ZFS["ZFS (kernel)"]
        Docker["Docker (socket)"]
        LDAP["LDAP/AD"]
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

## 1. Daemon Internal Structure (Go)

```mermaid
%%{init: {'theme':'base', 'themeVariables': { 'primaryColor':'#b2dfdb', 'primaryTextColor':'#004d40', 'primaryBorderColor':'#00695c', 'lineColor':'#00695c', 'secondaryColor':'#e0f2f1' }}}%%
flowchart TB
    subgraph cmd["cmd/dplaned"]
        main["main.go flags, DB init, router setup"]
        schema["schema.go SQLite schema"]
    end

    subgraph internal["internal/"]
        handlers["handlers/ ~50 handler files"]
        middleware["middleware/ logging, session, rate limit"]
        audit["audit/ logger, buffered, chain, HMAC"]
        security["security/ session, RBAC, whitelist"]
        ha["ha/ cluster manager"]
        zfs["zfs/ pool heartbeat"]
        dockerclient["dockerclient/"]
        ldap["ldap/"]
        gitops["gitops/ state, apply, drift, diff"]
        nixwriter["nixwriter/ NixOS fragment writer"]
        networkdwriter["networkdwriter/ systemd-networkd files"]
        reconciler["reconciler/ network state sync"]
        websocket["websocket/ monitor hub"]
        monitoring["monitoring/ background, inotify"]
        alerts["alerts/ Telegram"]
        hardware["hardware/ detection"]
        storage["storage/ mount guard"]
        indexing["indexing/ hybrid strategy"]
        cmdutil["cmdutil/ command whitelist"]
        netlinkx["netlinkx/"]
    end

    main --> handlers
    main --> middleware
    main --> audit
    main --> security
    main --> ha
    main --> zfs
    main --> dockerclient
    main --> ldap
    main --> gitops
    main --> nixwriter
    main --> networkdwriter
    main --> reconciler
    main --> websocket
    main --> monitoring
    main --> alerts
    handlers --> zfs
    handlers --> dockerclient
    handlers --> ldap
    handlers --> gitops
    handlers --> security
    handlers --> audit
```

---

## 2. API Domains (Handlers → Routes)

Handlers are grouped by API prefix; all go through **middleware** (logging, session, rate limit) and many use **RBAC** (`permRoute`).

```mermaid
%%{init: {'theme':'base', 'themeVariables': { 'primaryColor':'#b2dfdb', 'primaryTextColor':'#004d40', 'primaryBorderColor':'#00695c', 'lineColor':'#00695c', 'secondaryColor':'#e0f2f1' }}}%%
flowchart LR
    subgraph Auth["Auth"]
        auth["/api/auth/* login, TOTP, tokens"]
    end

    subgraph Storage["Storage / ZFS"]
        zfs_api["/api/zfs/* pools, datasets, encryption"]
        snap["/api/zfs/snapshots /api/timemachine /api/sandbox"]
        health["/api/zfs/health /api/zfs/capacity /api/zfs/scrub"]
        replication["/api/replication/*"]
    end

    subgraph Compute["Compute"]
        docker["/api/docker/* containers, compose, update"]
    end

    subgraph Identity["Identity"]
        rbac["/api/rbac/* users, groups, roles"]
        ldap["/api/ldap/* config, sync, mappings"]
    end

    subgraph Network["Network"]
        net["/api/network/* apply, VLAN, bond"]
    end

    subgraph Shares["Shares & Files"]
        shares["/api/shares/* /api/files/* /api/acl/*"]
    end

    subgraph System["System"]
        system["/api/system/* status, settings, audit, UPS"]
        nixos["/api/nixos/* validate, apply, rollback"]
        gitops["/api/gitops/*"]
        git_sync["/api/git-sync/*"]
    end

    subgraph Other["Other"]
        alerts["/api/alerts/* webhooks, SMTP, Telegram"]
        firewall["/api/firewall/*"]
        certs["/api/certs/*"]
        iscsi["/api/iscsi/*"]
        metrics["/metrics Prometheus"]
    end

    Router["Gorilla Mux Router"] --> Auth
    Router --> Storage
    Router --> Compute
    Router --> Identity
    Router --> Network
    Router --> Shares
    Router --> System
    Router --> Other
```

---

## 3. Frontend (app/) Structure

```mermaid
%%{init: {'theme':'base', 'themeVariables': { 'primaryColor':'#b2dfdb', 'primaryTextColor':'#004d40', 'primaryBorderColor':'#00695c', 'lineColor':'#00695c', 'secondaryColor':'#e0f2f1' }}}%%
flowchart TB
    subgraph app["app/"]
        index["index.html entry"]
        pages["pages/ ~45 HTML pages"]
        assets["assets/"]
        modules["modules/available/"]
        daemons["daemons/ Python: realtime, alerting"]
    end

    subgraph pages_detail["Pages (samples)"]
        p1["login, setup-wizard index, dashboard"]
        p2["pools, shares docker, replication"]
        p3["users, security directory-service, rbac"]
        p4["network, settings audit, alerts, gitops"]
        p5["files, iscsi certificates, firewall"]
    end

    subgraph assets_detail["assets/"]
        js["js/ core, nav, api, websocket form-validator, theme-engine"]
        css["css/ Material theme, M3, design tokens"]
    end

    index --> pages
    index --> assets
    pages --> pages_detail
    assets --> assets_detail
```

---

## 4. Deployment & Runtime

```mermaid
%%{init: {'theme':'base', 'themeVariables': { 'primaryColor':'#b2dfdb', 'primaryTextColor':'#004d40', 'primaryBorderColor':'#00695c', 'lineColor':'#00695c', 'secondaryColor':'#e0f2f1' }}}%%
flowchart TB
    subgraph Deploy["Deployment"]
        make["Makefile build, install"]
        install_sh["install.sh / get.sh"]
        nix["nixos/ flake.nix, configuration.nix disko.nix, setup-nixos.sh"]
    end

    subgraph Runtime["Runtime (after install)"]
        systemd["systemd dplaned.service"]
        nginx_svc["nginx (serves app + proxy)"]
        zed_hook["ZED hook /etc/zfs/zed.d/"]
        udev_rules["udev 99-dplaneos-removable-media"]
    end

    subgraph DataDirs["Paths"]
        opt["/opt/dplaneos/ daemon + app"]
        var_lib["/var/lib/dplaneos/ DB, audit, config"]
        var_log["/var/log/dplaneos/"]
        etc["/etc/dplaneos/"]
    end

    make --> systemd
    make --> zed_hook
    make --> udev_rules
    nix --> systemd
    systemd --> opt
    systemd --> var_lib
```

---

## 5. Request Flow (Simplified)

```mermaid
%%{init: {'theme':'base', 'themeVariables': { 'primaryColor':'#b2dfdb', 'primaryTextColor':'#004d40', 'primaryBorderColor':'#00695c', 'lineColor':'#00695c' }}}%%
sequenceDiagram
    participant U as User/Browser
    participant N as nginx
    participant M as Middleware
    participant H as Handler
    participant D as DB / ZFS / Docker

    U->>N: GET /api/zfs/pools or static file
    alt Static (html,css,js)
        N->>U: serve from /opt/dplaneos/app
    else API
        N->>M: proxy to :9000
        M->>M: log, session, rate limit
        M->>H: route to handler
        H->>D: query / exec
        D-->>H: result
        H-->>M: JSON response
        M-->>N: response
        N-->>U: response
    end
```

---

## 6. Key Dependencies (Daemon)

| Layer      | Dependency / Component |
|-----------|--------------------------|
| HTTP      | gorilla/mux             |
| DB        | mattn/go-sqlite3 (CGO)  |
| Auth      | internal security + session |
| Config    | networkdwriter, nixwriter, reconciler |
| External  | ZFS CLI, Docker socket, LDAP, systemd-networkd |

---

*Generated for D-PlaneOS v3.2.1. Update this doc when adding major modules or routes.*
