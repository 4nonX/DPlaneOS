-- +goose Up

-- ── Core user management ──
CREATE TABLE IF NOT EXISTS users (
    id               BIGSERIAL PRIMARY KEY,
    username         TEXT NOT NULL UNIQUE,
    password_hash    TEXT NOT NULL DEFAULT '',
    display_name     TEXT NOT NULL DEFAULT '',
    email            TEXT NOT NULL DEFAULT '',
    role             TEXT NOT NULL DEFAULT 'user',
    active           INTEGER NOT NULL DEFAULT 1,
    must_change_password INTEGER NOT NULL DEFAULT 0,
    source           TEXT NOT NULL DEFAULT 'local',
    totp_enabled     INTEGER NOT NULL DEFAULT 0,
    last_login       TIMESTAMPTZ,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- ── Session management ──
CREATE TABLE IF NOT EXISTS sessions (
    id            BIGSERIAL PRIMARY KEY,
    session_id    TEXT NOT NULL UNIQUE,
    user_id       BIGINT,
    username      TEXT NOT NULL,
    ip_address    TEXT NOT NULL DEFAULT '',
    user_agent    TEXT NOT NULL DEFAULT '',
    created_at    BIGINT NOT NULL DEFAULT EXTRACT(EPOCH FROM NOW())::BIGINT,
    expires_at    BIGINT,
    last_activity BIGINT NOT NULL DEFAULT EXTRACT(EPOCH FROM NOW())::BIGINT,
    status        TEXT NOT NULL DEFAULT 'active',
    csrf_token    TEXT NOT NULL DEFAULT '',
    FOREIGN KEY (username) REFERENCES users(username)
);

CREATE INDEX IF NOT EXISTS idx_sessions_session_id ON sessions(session_id);
CREATE INDEX IF NOT EXISTS idx_sessions_expires     ON sessions(expires_at);
CREATE INDEX IF NOT EXISTS idx_sessions_csrf        ON sessions(csrf_token);

-- ── RBAC: Roles ──
CREATE TABLE IF NOT EXISTS roles (
    id           BIGSERIAL PRIMARY KEY,
    name         TEXT NOT NULL UNIQUE,
    display_name TEXT NOT NULL DEFAULT '',
    description  TEXT NOT NULL DEFAULT '',
    is_system    INTEGER NOT NULL DEFAULT 0,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- ── RBAC: Permissions ──
CREATE TABLE IF NOT EXISTS permissions (
    id           BIGSERIAL PRIMARY KEY,
    resource     TEXT NOT NULL,
    action       TEXT NOT NULL,
    display_name TEXT NOT NULL DEFAULT '',
    description  TEXT NOT NULL DEFAULT '',
    category     TEXT NOT NULL DEFAULT 'general',
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE(resource, action)
);

-- ── RBAC: Role-Permission mapping ──
CREATE TABLE IF NOT EXISTS role_permissions (
    role_id       BIGINT NOT NULL,
    permission_id BIGINT NOT NULL,
    PRIMARY KEY (role_id, permission_id),
    FOREIGN KEY (role_id)       REFERENCES roles(id)       ON DELETE CASCADE,
    FOREIGN KEY (permission_id) REFERENCES permissions(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_role_permissions_role ON role_permissions(role_id);

-- ── RBAC: User-Role mapping ──
CREATE TABLE IF NOT EXISTS user_roles (
    user_id    BIGINT NOT NULL,
    role_id    BIGINT NOT NULL,
    granted_by TEXT NOT NULL DEFAULT 'system',
    expires_at TIMESTAMPTZ,
    PRIMARY KEY (user_id, role_id),
    FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE,
    FOREIGN KEY (role_id) REFERENCES roles(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_user_roles_user ON user_roles(user_id);
CREATE INDEX IF NOT EXISTS idx_user_roles_role ON user_roles(role_id);

-- ── Audit logging (HMAC chain) ──
CREATE TABLE IF NOT EXISTS audit_logs (
    id         BIGSERIAL PRIMARY KEY,
    timestamp  BIGINT NOT NULL DEFAULT EXTRACT(EPOCH FROM NOW())::BIGINT,
    actor      TEXT NOT NULL DEFAULT '',
    action     TEXT NOT NULL DEFAULT '',
    resource   TEXT NOT NULL DEFAULT '',
    details    TEXT NOT NULL DEFAULT '',
    ip_address TEXT NOT NULL DEFAULT '',
    success    INTEGER NOT NULL DEFAULT 1,
    prev_hash  TEXT NOT NULL DEFAULT '',
    row_hash   TEXT NOT NULL DEFAULT ''
);

CREATE INDEX IF NOT EXISTS idx_audit_timestamp ON audit_logs(timestamp);
CREATE INDEX IF NOT EXISTS idx_audit_user       ON audit_logs(actor);

-- ── Telegram notifications ──
CREATE TABLE IF NOT EXISTS telegram_config (
    id         BIGINT PRIMARY KEY,
    bot_token  TEXT NOT NULL DEFAULT '',
    chat_id    TEXT NOT NULL DEFAULT '',
    enabled    INTEGER NOT NULL DEFAULT 0,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- ── LDAP/AD configuration (includes v7.3.0 enterprise directory columns) ──
CREATE TABLE IF NOT EXISTS ldap_config (
    id               BIGINT PRIMARY KEY DEFAULT 1,
    enabled          INTEGER NOT NULL DEFAULT 0,
    server           TEXT NOT NULL DEFAULT '',
    port             INTEGER NOT NULL DEFAULT 389,
    use_tls          INTEGER NOT NULL DEFAULT 0,
    bind_dn          TEXT NOT NULL DEFAULT '',
    bind_password    TEXT NOT NULL DEFAULT '',
    base_dn          TEXT NOT NULL DEFAULT '',
    user_filter      TEXT NOT NULL DEFAULT '(&(objectClass=user)(sAMAccountName={username}))',
    user_id_attr     TEXT NOT NULL DEFAULT 'sAMAccountName',
    user_name_attr   TEXT NOT NULL DEFAULT 'displayName',
    user_email_attr  TEXT NOT NULL DEFAULT 'mail',
    group_base_dn    TEXT NOT NULL DEFAULT '',
    group_filter     TEXT NOT NULL DEFAULT '(&(objectClass=group)(member={user_dn}))',
    group_member_attr TEXT NOT NULL DEFAULT 'member',
    jit_provisioning INTEGER NOT NULL DEFAULT 0,
    default_role     TEXT NOT NULL DEFAULT 'user',
    sync_interval    INTEGER NOT NULL DEFAULT 3600,
    timeout          INTEGER NOT NULL DEFAULT 10,
    last_test_at     TIMESTAMPTZ,
    last_test_ok     INTEGER NOT NULL DEFAULT 0,
    last_test_msg    TEXT,
    last_sync_at     TIMESTAMPTZ,
    last_sync_ok     INTEGER NOT NULL DEFAULT 0,
    last_sync_count  INTEGER NOT NULL DEFAULT 0,
    last_sync_msg    TEXT,
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    provider_type    TEXT NOT NULL DEFAULT 'openldap',
    realm            TEXT NOT NULL DEFAULT '',
    domain_joined    BOOLEAN NOT NULL DEFAULT false,
    domain_joined_at TIMESTAMPTZ
);

-- ── LDAP group-to-role mappings ──
CREATE TABLE IF NOT EXISTS ldap_group_mappings (
    id         BIGSERIAL PRIMARY KEY,
    ldap_group TEXT NOT NULL,
    role_name  TEXT NOT NULL DEFAULT '',
    role_id    BIGINT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE(ldap_group)
);

-- ── LDAP sync history ──
CREATE TABLE IF NOT EXISTS ldap_sync_log (
    id             BIGSERIAL PRIMARY KEY,
    sync_type      TEXT NOT NULL DEFAULT 'manual',
    success        INTEGER NOT NULL DEFAULT 0,
    users_synced   INTEGER NOT NULL DEFAULT 0,
    users_created  INTEGER NOT NULL DEFAULT 0,
    users_updated  INTEGER NOT NULL DEFAULT 0,
    users_disabled INTEGER NOT NULL DEFAULT 0,
    error_msg      TEXT NOT NULL DEFAULT '',
    duration_ms    BIGINT NOT NULL DEFAULT 0,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- ── Git sync config (legacy singleton) ──
CREATE TABLE IF NOT EXISTS git_sync_config (
    id           BIGINT PRIMARY KEY CHECK (id = 1),
    repo_url     TEXT NOT NULL DEFAULT '',
    branch       TEXT NOT NULL DEFAULT 'main',
    local_path   TEXT NOT NULL DEFAULT '/var/lib/dplaneos/git-stacks',
    sync_interval INTEGER NOT NULL DEFAULT 0,
    auto_deploy  INTEGER NOT NULL DEFAULT 0,
    auth_type    TEXT NOT NULL DEFAULT 'none',
    auth_token   TEXT NOT NULL DEFAULT '',
    ssh_key_path TEXT NOT NULL DEFAULT '',
    host_key_mode TEXT NOT NULL DEFAULT 'accept',
    commit_name  TEXT NOT NULL DEFAULT 'DPlaneOS',
    commit_email TEXT NOT NULL DEFAULT 'dplaneos@localhost',
    last_sync_at TIMESTAMPTZ,
    last_commit  TEXT,
    last_error   TEXT
);

-- ── ACME configuration ──
CREATE TABLE IF NOT EXISTS acme_config (
    id         BIGINT PRIMARY KEY CHECK (id = 1),
    email      TEXT NOT NULL DEFAULT '',
    server     TEXT NOT NULL DEFAULT 'https://acme-v02.api.letsencrypt.org/directory',
    resolver   TEXT NOT NULL DEFAULT 'http',
    dns_config TEXT NOT NULL DEFAULT '{}',
    domains    TEXT NOT NULL DEFAULT '[]',
    enabled    INTEGER NOT NULL DEFAULT 0,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- ── Certificate store ──
CREATE TABLE IF NOT EXISTS certificates (
    id         BIGSERIAL PRIMARY KEY,
    name       TEXT NOT NULL UNIQUE,
    cert_pem   TEXT NOT NULL,
    key_pem    TEXT NOT NULL,
    is_managed INTEGER NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- ── SMART monitoring schedules ──
CREATE TABLE IF NOT EXISTS smart_schedules (
    id         BIGSERIAL PRIMARY KEY,
    device     TEXT NOT NULL,
    test_type  TEXT NOT NULL,
    schedule   TEXT NOT NULL,
    enabled    INTEGER NOT NULL DEFAULT 1,
    last_run   TIMESTAMPTZ,
    next_run   TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE(device, test_type)
);

-- ── Git sync repos (multi-repo v2.1.1) ──
CREATE TABLE IF NOT EXISTS git_sync_repos (
    id            BIGSERIAL PRIMARY KEY,
    name          TEXT NOT NULL UNIQUE,
    repo_url      TEXT NOT NULL DEFAULT '',
    branch        TEXT NOT NULL DEFAULT 'main',
    local_path    TEXT NOT NULL DEFAULT '',
    compose_path  TEXT NOT NULL DEFAULT 'docker-compose.yml',
    auto_sync     INTEGER NOT NULL DEFAULT 0,
    sync_interval INTEGER NOT NULL DEFAULT 5,
    auth_type     TEXT NOT NULL DEFAULT 'none',
    auth_token    TEXT NOT NULL DEFAULT '',
    ssh_key_path  TEXT NOT NULL DEFAULT '',
    host_key_mode TEXT NOT NULL DEFAULT 'accept',
    commit_name   TEXT NOT NULL DEFAULT 'DPlaneOS',
    commit_email  TEXT NOT NULL DEFAULT 'dplaneos@localhost',
    last_sync_at  TIMESTAMPTZ,
    last_commit   TEXT,
    last_error    TEXT,
    enabled       INTEGER NOT NULL DEFAULT 1,
    created_at    TIMESTAMPTZ DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_git_repos_name ON git_sync_repos(name);

-- ── GitHub credential store ──
CREATE TABLE IF NOT EXISTS git_credentials (
    id         BIGSERIAL PRIMARY KEY,
    name       TEXT NOT NULL UNIQUE,
    host       TEXT NOT NULL DEFAULT 'github.com',
    auth_type  TEXT NOT NULL DEFAULT 'token',
    token      TEXT NOT NULL DEFAULT '',
    ssh_key    TEXT NOT NULL DEFAULT '',
    notes      TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ DEFAULT NOW()
);

-- ── API tokens (long-lived bearer tokens for automation) ──
CREATE TABLE IF NOT EXISTS api_tokens (
    id           BIGSERIAL PRIMARY KEY,
    user_id      BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name         TEXT NOT NULL,
    token_hash   TEXT NOT NULL UNIQUE,
    token_prefix TEXT NOT NULL,
    scopes       TEXT NOT NULL DEFAULT 'read',
    last_used    TIMESTAMPTZ,
    expires_at   TIMESTAMPTZ,
    created_at   TIMESTAMPTZ DEFAULT NOW(),
    UNIQUE(user_id, name)
);

-- ── TOTP 2FA secrets ──
CREATE TABLE IF NOT EXISTS totp_secrets (
    user_id      BIGINT PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    secret       TEXT NOT NULL,
    enabled      INTEGER NOT NULL DEFAULT 0,
    backup_codes TEXT NOT NULL DEFAULT '',
    created_at   TIMESTAMPTZ DEFAULT NOW(),
    verified_at  TIMESTAMPTZ
);

-- ── Pre-upgrade ZFS snapshots ──
CREATE TABLE IF NOT EXISTS pre_upgrade_snapshots (
    id          BIGSERIAL PRIMARY KEY,
    snapshot    TEXT NOT NULL,
    pool        TEXT NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    nixos_apply TEXT NOT NULL DEFAULT '',
    success     INTEGER NOT NULL DEFAULT 1,
    error       TEXT NOT NULL DEFAULT ''
);

-- ── Webhook alerting ──
CREATE TABLE IF NOT EXISTS webhook_configs (
    id             BIGSERIAL PRIMARY KEY,
    name           TEXT NOT NULL UNIQUE,
    url            TEXT NOT NULL,
    secret_header  TEXT NOT NULL DEFAULT '',
    secret_value   TEXT NOT NULL DEFAULT '',
    content_type   TEXT NOT NULL DEFAULT 'application/json',
    body_template  TEXT NOT NULL DEFAULT '',
    enabled        INTEGER NOT NULL DEFAULT 1,
    events         TEXT NOT NULL DEFAULT '[]',
    created_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- ── GitOps approvals ──
CREATE TABLE IF NOT EXISTS gitops_approvals (
    kind        TEXT NOT NULL,
    name        TEXT NOT NULL,
    reason      TEXT NOT NULL DEFAULT '',
    approved_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (kind, name)
);

-- ── GitOps configuration ──
CREATE TABLE IF NOT EXISTS gitops_config (
    id               BIGINT PRIMARY KEY CHECK (id = 1),
    enabled          INTEGER NOT NULL DEFAULT 0,
    repo_id          BIGINT,
    state_path       TEXT NOT NULL DEFAULT 'state.yaml',
    sync_storage     INTEGER NOT NULL DEFAULT 1,
    sync_access      INTEGER NOT NULL DEFAULT 1,
    sync_app         INTEGER NOT NULL DEFAULT 1,
    sync_identity    INTEGER NOT NULL DEFAULT 1,
    sync_protection  INTEGER NOT NULL DEFAULT 1,
    sync_system      INTEGER NOT NULL DEFAULT 1,
    nixos_repo_id    BIGINT,
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    FOREIGN KEY (repo_id)       REFERENCES git_sync_repos(id),
    FOREIGN KEY (nixos_repo_id) REFERENCES git_sync_repos(id)
);

-- ── Disk lifecycle registry ──
CREATE TABLE IF NOT EXISTS disk_registry (
    id         BIGSERIAL PRIMARY KEY,
    dev_name   TEXT NOT NULL,
    by_id_path TEXT UNIQUE,
    serial     TEXT,
    wwn        TEXT,
    model      TEXT,
    size_bytes BIGINT,
    disk_type  TEXT,
    pool_name  TEXT,
    health     TEXT NOT NULL DEFAULT 'UNKNOWN',
    last_seen  TIMESTAMPTZ NOT NULL,
    first_seen TIMESTAMPTZ NOT NULL,
    removed_at TIMESTAMPTZ,
    temp_c     INTEGER NOT NULL DEFAULT 0,
    UNIQUE(dev_name, by_id_path)
);

CREATE INDEX IF NOT EXISTS idx_disk_registry_by_id  ON disk_registry(by_id_path);
CREATE INDEX IF NOT EXISTS idx_disk_registry_serial ON disk_registry(serial);

-- ── System-level key-value configuration ──
CREATE TABLE IF NOT EXISTS system_config (
    key        TEXT PRIMARY KEY,
    value      TEXT NOT NULL,
    updated_at TIMESTAMPTZ DEFAULT NOW()
);

-- ── General settings store ──
CREATE TABLE IF NOT EXISTS settings (
    key        TEXT PRIMARY KEY,
    value      TEXT NOT NULL,
    updated_at TIMESTAMPTZ DEFAULT NOW()
);

-- ── SMB shares ──
CREATE TABLE IF NOT EXISTS smb_shares (
    id             BIGSERIAL PRIMARY KEY,
    name           TEXT NOT NULL UNIQUE,
    path           TEXT NOT NULL,
    comment        TEXT DEFAULT '',
    browsable      INTEGER DEFAULT 1,
    read_only      INTEGER DEFAULT 0,
    guest_ok       INTEGER DEFAULT 0,
    valid_users    TEXT DEFAULT '',
    write_list     TEXT DEFAULT '',
    create_mask    TEXT DEFAULT '0664',
    directory_mask TEXT DEFAULT '0775',
    enabled        INTEGER DEFAULT 1,
    created_at     TIMESTAMPTZ DEFAULT NOW(),
    updated_at     TIMESTAMPTZ DEFAULT NOW()
);

-- ── NFS exports ──
CREATE TABLE IF NOT EXISTS nfs_exports (
    id         BIGSERIAL PRIMARY KEY,
    path       TEXT NOT NULL,
    clients    TEXT NOT NULL DEFAULT '*',
    options    TEXT NOT NULL DEFAULT 'rw,sync,no_subtree_check,no_root_squash',
    enabled    INTEGER NOT NULL DEFAULT 1,
    created_at TIMESTAMPTZ DEFAULT NOW(),
    updated_at TIMESTAMPTZ DEFAULT NOW()
);

-- ── Groups and members ──
CREATE TABLE IF NOT EXISTS groups (
    id          BIGSERIAL PRIMARY KEY,
    name        TEXT NOT NULL UNIQUE,
    description TEXT DEFAULT '',
    gid         INTEGER,
    created_at  TIMESTAMPTZ DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS group_members (
    group_name TEXT,
    username   TEXT,
    PRIMARY KEY (group_name, username),
    FOREIGN KEY (group_name) REFERENCES groups(name) ON DELETE CASCADE,
    FOREIGN KEY (username)   REFERENCES users(username) ON DELETE CASCADE
);

-- ── Cold tier (rclone FUSE mounts) ──
CREATE TABLE IF NOT EXISTS cold_tier_mounts (
    id             BIGSERIAL PRIMARY KEY,
    name           TEXT NOT NULL UNIQUE,
    remote         TEXT NOT NULL,
    remote_path    TEXT NOT NULL DEFAULT '',
    mount_point    TEXT NOT NULL UNIQUE,
    vfs_cache_mode TEXT NOT NULL DEFAULT 'writes',
    mounted        INTEGER NOT NULL DEFAULT 0,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_mount_at  TIMESTAMPTZ
);

-- +goose Down

DROP TABLE IF EXISTS cold_tier_mounts;
DROP TABLE IF EXISTS group_members;
DROP TABLE IF EXISTS groups;
DROP TABLE IF EXISTS nfs_exports;
DROP TABLE IF EXISTS smb_shares;
DROP TABLE IF EXISTS settings;
DROP TABLE IF EXISTS system_config;
DROP TABLE IF EXISTS disk_registry;
DROP TABLE IF EXISTS gitops_config;
DROP TABLE IF EXISTS gitops_approvals;
DROP TABLE IF EXISTS webhook_configs;
DROP TABLE IF EXISTS pre_upgrade_snapshots;
DROP TABLE IF EXISTS totp_secrets;
DROP TABLE IF EXISTS api_tokens;
DROP TABLE IF EXISTS git_credentials;
DROP TABLE IF EXISTS git_sync_repos;
DROP TABLE IF EXISTS smart_schedules;
DROP TABLE IF EXISTS certificates;
DROP TABLE IF EXISTS acme_config;
DROP TABLE IF EXISTS git_sync_config;
DROP TABLE IF EXISTS ldap_sync_log;
DROP TABLE IF EXISTS ldap_group_mappings;
DROP TABLE IF EXISTS ldap_config;
DROP TABLE IF EXISTS telegram_config;
DROP TABLE IF EXISTS audit_logs;
DROP TABLE IF EXISTS user_roles;
DROP TABLE IF EXISTS role_permissions;
DROP TABLE IF EXISTS permissions;
DROP TABLE IF EXISTS roles;
DROP TABLE IF EXISTS sessions;
DROP TABLE IF EXISTS users;
