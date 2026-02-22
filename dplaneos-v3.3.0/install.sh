#!/bin/bash
#
# D-PlaneOS — Installer
#
# ── ONE-LINER INSTALL (nothing to download first) ────────────────────────────
#
#   curl -fsSL https://get.dplaneos.io | sudo bash
#
#   Or with options:
#   curl -fsSL https://get.dplaneos.io | sudo bash -s -- --port 8080
#   curl -fsSL https://get.dplaneos.io | sudo bash -s -- --upgrade
#
# ── OR download and run directly ─────────────────────────────────────────────
#
#   sudo ./install.sh
#
# WITH OPTIONS:
#   sudo ./install.sh --port 8080          # custom port (default 80)
#   sudo ./install.sh --unattended         # no prompts (CI/automation)
#   sudo ./install.sh --upgrade            # upgrade existing install (preserves data)
#   sudo ./install.sh --port 8080 --unattended --upgrade
#

set -euo pipefail

# ── Version ───────────────────────────────────────────────────────────────────
readonly SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
readonly VERSION="$(cat "$SCRIPT_DIR/VERSION" 2>/dev/null | tr -d '[:space:]')"
[ -z "$VERSION" ] && { echo "ERROR: Could not read VERSION file" >&2; exit 1; }
readonly INSTALL_DIR="/opt/dplaneos"
readonly DB_PATH="/var/lib/dplaneos/dplaneos.db"
readonly LOG_FILE="/var/log/dplaneos-install.log"
readonly BACKUP_BASE="/var/lib/dplaneos/backups"

# ── Colors ────────────────────────────────────────────────────────────────────
RED='\033[0;31m'; GREEN='\033[0;32m'; YELLOW='\033[1;33m'
BLUE='\033[0;34m'; BOLD='\033[1m'; NC='\033[0m'

# ── Parse arguments ───────────────────────────────────────────────────────────
OPT_PORT=80
OPT_UNATTENDED=false
OPT_UPGRADE=false

usage() {
    echo "Usage: sudo $0 [OPTIONS]"
    echo ""
    echo "Options:"
    echo "  --port PORT       Web UI port (default: 80)"
    echo "  --unattended      Skip all confirmation prompts"
    echo "  --upgrade         Upgrade existing install (preserves data)"
    echo "  --help            Show this help"
    echo ""
    echo "Examples:"
    echo "  sudo ./install.sh"
    echo "  sudo ./install.sh --port 8080"
    echo "  sudo ./install.sh --upgrade --unattended"
    exit 0
}

while [[ $# -gt 0 ]]; do
    case "$1" in
        --port)       OPT_PORT="$2"; shift 2 ;;
        --unattended) OPT_UNATTENDED=true; shift ;;
        --upgrade)    OPT_UPGRADE=true; shift ;;
        --help|-h)    usage ;;
        *)            echo "Unknown option: $1 (use --help)"; exit 1 ;;
    esac
done

if ! [[ "$OPT_PORT" =~ ^[0-9]+$ ]] || [ "$OPT_PORT" -lt 1 ] || [ "$OPT_PORT" -gt 65535 ]; then
    echo "Invalid port: $OPT_PORT"; exit 1
fi

# ── Logging ───────────────────────────────────────────────────────────────────
mkdir -p /var/log
exec > >(tee -a "$LOG_FILE") 2>&1

log()  { echo -e "${GREEN}✓${NC} $1"; }
warn() { echo -e "${YELLOW}⚠${NC} $1"; }
info() { echo -e "${BLUE}ℹ${NC} $1"; }
step() { echo ""; echo -e "${BOLD}${BLUE}━━━ $1${NC}"; }

die() {
    echo ""
    echo -e "${RED}✗ FATAL: $1${NC}"
    echo ""
    echo "  Log:      $LOG_FILE"
    echo "  Recovery: sudo dplaneos-recovery"
    echo ""
    exit 1
}

confirm() {
    $OPT_UNATTENDED && return 0
    read -r -p "$1 [Y/n] " reply
    [[ "${reply,,}" != "n" ]]
}

# ── Root check ────────────────────────────────────────────────────────────────
[ "$EUID" -eq 0 ] || die "Must run as root: sudo ./install.sh"

# ── Architecture detection ────────────────────────────────────────────────────
ARCH=$(uname -m)
case "$ARCH" in
    x86_64)        ARCH_TAG="linux-amd64" ;;
    aarch64|arm64) ARCH_TAG="linux-arm64" ;;
    armv7l)        ARCH_TAG="linux-armhf" ;;
    *)             die "Unsupported architecture: $ARCH (supported: x86_64, aarch64, armv7l)" ;;
esac

# ── Rollback helper ───────────────────────────────────────────────────────────
_do_rollback() {
    local bp="$1"
    systemctl stop dplaned nginx 2>/dev/null || true
    [ -f "${bp}/dplaneos.db" ]         && cp "${bp}/dplaneos.db" "$DB_PATH" \
                                       && chmod 600 "$DB_PATH" \
                                       && echo "  DB restored"
    [ -f "${bp}/nginx-dplaneos.conf" ] && cp "${bp}/nginx-dplaneos.conf" /etc/nginx/sites-available/dplaneos \
                                       && echo "  nginx config restored"
    [ -f "${bp}/dplaned.service" ]     && cp "${bp}/dplaned.service" /etc/systemd/system/dplaned.service \
                                       && systemctl daemon-reload \
                                       && echo "  systemd unit restored"
    [ -f "${bp}/sudoers-dplaneos" ]    && cp "${bp}/sudoers-dplaneos" /etc/sudoers.d/dplaneos \
                                       && chmod 440 /etc/sudoers.d/dplaneos \
                                       && echo "  sudoers restored"
    systemctl start nginx 2>/dev/null || true
    systemctl start dplaned 2>/dev/null || true
}

# ── Trap — rollback on unexpected failure ─────────────────────────────────────
BACKUP_PATH=""
INSTALL_PHASE=0
ROLLBACK_DONE=false

cleanup() {
    local ec=$?
    [ $ec -eq 0 ] || $ROLLBACK_DONE && return
    echo ""
    echo -e "${RED}━━━ Installation failed at phase $INSTALL_PHASE ━━━${NC}"
    if [ -n "$BACKUP_PATH" ] && [ -d "$BACKUP_PATH" ]; then
        echo "Rolling back to previous installation..."
        _do_rollback "$BACKUP_PATH" \
            && echo -e "${GREEN}✓ Rollback complete${NC}" \
            || echo -e "${RED}✗ Rollback also failed — run: sudo dplaneos-recovery${NC}"
    else
        echo "No backup available (fresh install failure)."
        echo "Run: sudo dplaneos-recovery"
    fi
    echo "Log: $LOG_FILE"
    ROLLBACK_DONE=true
}
trap cleanup EXIT

# ── Banner ────────────────────────────────────────────────────────────────────
clear
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo -e "${BOLD}    D-PlaneOS v${VERSION} — Installer${NC}"
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo ""
echo "  Architecture : $ARCH ($ARCH_TAG)"
echo "  Port         : $OPT_PORT"
echo "  Mode         : $( $OPT_UPGRADE && echo 'Upgrade' || echo 'Fresh install')"
echo "  Log          : $LOG_FILE"
echo ""

# ────────────────────────────────────────────────────────────────────────────
step "Phase 0/12: Pre-flight checks"
# ────────────────────────────────────────────────────────────────────────────

[ -f /etc/os-release ] || die "Cannot detect OS"
. /etc/os-release
case "${ID,,}" in
    debian|ubuntu|raspbian|linuxmint|pop)
        log "OS: $PRETTY_NAME" ;;
    *)
        die "Unsupported OS: ${PRETTY_NAME:-unknown}
  Supported: Debian 11+, Ubuntu 20.04+, Raspberry Pi OS
  Other distros: install manually — see docs/manual-install.md" ;;
esac

TOTAL_RAM_MB=$(free -m | awk '/^Mem:/{print $2}')
[ "$TOTAL_RAM_MB" -ge 1024 ] || warn "Low RAM: ${TOTAL_RAM_MB}MB — 2GB+ recommended"
log "RAM: ${TOTAL_RAM_MB}MB"

ROOT_FREE_GB=$(df -BG / | awk 'NR==2{gsub(/G/,"",$4); print $4}')
[ "$ROOT_FREE_GB" -ge 4 ] || die "Insufficient disk: ${ROOT_FREE_GB}GB free (need 4GB+)"
log "Disk free: ${ROOT_FREE_GB}GB"

if ss -tuln 2>/dev/null | grep -q ":${OPT_PORT} "; then
    $OPT_UPGRADE \
        && warn "Port ${OPT_PORT} in use — will reconfigure (expected on upgrade)" \
        || die "Port ${OPT_PORT} in use. Use --port NNNN or stop the conflicting service."
else
    log "Port ${OPT_PORT} available"
fi

IS_UPGRADE=false
if [ -d "$INSTALL_DIR" ] || [ -f "$DB_PATH" ]; then
    IS_UPGRADE=true
    if ! $OPT_UPGRADE; then
        warn "Existing D-PlaneOS installation detected."
        confirm "Upgrade existing install? (your data will be preserved)" \
            || die "Aborted. To upgrade: sudo ./install.sh --upgrade"
        OPT_UPGRADE=true
    fi
fi
$OPT_UPGRADE && log "Mode: Upgrade (data preserved)" || log "Mode: Fresh install"

INSTALL_PHASE=1

# ────────────────────────────────────────────────────────────────────────────
step "Phase 1/12: Backup"
# ────────────────────────────────────────────────────────────────────────────

if $OPT_UPGRADE; then
    BACKUP_PATH="${BACKUP_BASE}/pre-upgrade-$(date +%Y%m%d-%H%M%S)"
    mkdir -p "$BACKUP_PATH"

    if [ -f "$DB_PATH" ]; then
        sqlite3 "$DB_PATH" ".backup '${BACKUP_PATH}/dplaneos.db'" 2>/dev/null \
            || cp "$DB_PATH" "${BACKUP_PATH}/dplaneos.db"
        log "DB backed up"
    fi
    [ -f /etc/nginx/sites-available/dplaneos ] \
        && cp /etc/nginx/sites-available/dplaneos "${BACKUP_PATH}/nginx-dplaneos.conf" \
        && log "nginx config backed up"
    [ -f /etc/systemd/system/dplaned.service ] \
        && cp /etc/systemd/system/dplaned.service "${BACKUP_PATH}/dplaned.service" \
        && log "systemd unit backed up"
    [ -f /etc/sudoers.d/dplaneos ] \
        && cp /etc/sudoers.d/dplaneos "${BACKUP_PATH}/sudoers-dplaneos" \
        && log "sudoers backed up"

    # Write rollback script into backup dir
    cat > "${BACKUP_PATH}/rollback.sh" <<RBSCRIPT
#!/bin/bash
# Rollback script — auto-generated $(date)
# Usage: sudo bash ${BACKUP_PATH}/rollback.sh
set -euo pipefail
systemctl stop dplaned nginx 2>/dev/null || true
[ -f "${BACKUP_PATH}/dplaneos.db" ]         && cp "${BACKUP_PATH}/dplaneos.db" "${DB_PATH}" && chmod 600 "${DB_PATH}" && echo "DB restored"
[ -f "${BACKUP_PATH}/nginx-dplaneos.conf" ] && cp "${BACKUP_PATH}/nginx-dplaneos.conf" /etc/nginx/sites-available/dplaneos && echo "nginx config restored"
[ -f "${BACKUP_PATH}/dplaned.service" ]     && cp "${BACKUP_PATH}/dplaned.service" /etc/systemd/system/dplaned.service && systemctl daemon-reload && echo "systemd unit restored"
[ -f "${BACKUP_PATH}/sudoers-dplaneos" ]    && cp "${BACKUP_PATH}/sudoers-dplaneos" /etc/sudoers.d/dplaneos && chmod 440 /etc/sudoers.d/dplaneos && echo "sudoers restored"
systemctl start nginx dplaned 2>/dev/null || true
echo "Rollback complete — access: http://\$(hostname -I | awk '{print \$1}')"
RBSCRIPT
    chmod +x "${BACKUP_PATH}/rollback.sh"
    log "Backup complete ($(du -sh "$BACKUP_PATH" | cut -f1)) — rollback: ${BACKUP_PATH}/rollback.sh"
else
    log "Fresh install — no backup needed"
fi

INSTALL_PHASE=2

# ────────────────────────────────────────────────────────────────────────────
step "Phase 2/12: System dependencies"
# ────────────────────────────────────────────────────────────────────────────

export DEBIAN_FRONTEND=noninteractive

# Enable extra repos required for zfsutils-linux and python3-bcrypt
# Ubuntu: universe   Debian: contrib
case "${ID,,}" in
    ubuntu|pop|linuxmint|raspbian)
        if ! apt-cache show zfsutils-linux &>/dev/null 2>&1; then
            apt-get install -y -qq software-properties-common &>/dev/null
            add-apt-repository -y universe &>/dev/null
            log "Ubuntu universe repository enabled"
        fi
        ;;
    debian)
        if ! grep -qE "contrib" /etc/apt/sources.list /etc/apt/sources.list.d/*.list 2>/dev/null; then
            sed -i 's/^\(deb .*\)main$/\1main contrib non-free/' /etc/apt/sources.list
            log "Debian contrib/non-free enabled"
        fi
        ;;
esac

apt-get update -qq 2>&1 | tail -1

PACKAGES=(nginx sqlite3 smartmontools lsof udev zfsutils-linux
          acl ufw hdparm git openssh-client openssl ca-certificates
          iproute2 procps coreutils
          python3-bcrypt apache2-utils
          musl-tools)  # enables fully static binary (glibc-independent)

for pkg in "${PACKAGES[@]}"; do
    if dpkg -l "$pkg" 2>/dev/null | grep -q "^ii"; then
        log "  $pkg (already installed)"
    else
        apt-get install -y -qq "$pkg" 2>&1 | tail -1 \
            && log "  $pkg" \
            || warn "  $pkg failed — may be optional"
    fi
done

INSTALL_PHASE=3

# ────────────────────────────────────────────────────────────────────────────
step "Phase 3/12: ZFS setup"
# ────────────────────────────────────────────────────────────────────────────

if ! lsmod | grep -q "^zfs "; then
    modprobe zfs 2>/dev/null \
        && log "ZFS module loaded" \
        || warn "ZFS module failed — try: sudo apt install linux-headers-\$(uname -r) && sudo dpkg-reconfigure zfs-dkms"
else
    log "ZFS module already loaded"
fi
command -v zpool &>/dev/null || die "ZFS utilities not found after install"

TOTAL_RAM_GB=$((TOTAL_RAM_MB / 1024))
if   [ "$TOTAL_RAM_GB" -le  4 ]; then ARC_MAX_GB=1
elif [ "$TOTAL_RAM_GB" -le  8 ]; then ARC_MAX_GB=2
elif [ "$TOTAL_RAM_GB" -le 16 ]; then ARC_MAX_GB=4
elif [ "$TOTAL_RAM_GB" -le 32 ]; then ARC_MAX_GB=8
elif [ "$TOTAL_RAM_GB" -le 64 ]; then ARC_MAX_GB=16
else                                   ARC_MAX_GB=32
fi
ARC_MAX_BYTES=$((ARC_MAX_GB * 1024 * 1024 * 1024))
echo "options zfs zfs_arc_max=${ARC_MAX_BYTES}" > /etc/modprobe.d/zfs.conf
lsmod | grep -q "^zfs " && echo "$ARC_MAX_BYTES" > /sys/module/zfs/parameters/zfs_arc_max 2>/dev/null || true
log "ZFS ARC: ${ARC_MAX_GB}GB of ${TOTAL_RAM_GB}GB RAM"

INSTALL_PHASE=4

# ────────────────────────────────────────────────────────────────────────────
step "Phase 4/12: Installing files"
# ────────────────────────────────────────────────────────────────────────────

mkdir -p "$INSTALL_DIR" /var/lib/dplaneos/{backups,git-stacks} /var/log/dplaneos /etc/dplaneos

if $OPT_UPGRADE; then
    # rsync: skip DB files, keep user data
    if command -v rsync &>/dev/null; then
        rsync -a --exclude='*.db' --exclude='*.db-wal' --exclude='*.db-shm' \
              "${SCRIPT_DIR}/" "${INSTALL_DIR}/"
    else
        cp -r "${SCRIPT_DIR}"/* "${INSTALL_DIR}/"
    fi
else
    cp -r "${SCRIPT_DIR}"/* "${INSTALL_DIR}/"
fi

chown -R root:root "${INSTALL_DIR}"
chown -R www-data:www-data "${INSTALL_DIR}/app" 2>/dev/null || true
chmod 700 /var/lib/dplaneos
log "Files installed to $INSTALL_DIR"

INSTALL_PHASE=5

# ────────────────────────────────────────────────────────────────────────────
step "Phase 5/12: Daemon binary"
# ────────────────────────────────────────────────────────────────────────────

BINARY_SRC=""

# Arch to ELF identifier mapping
case "$ARCH_TAG" in
    linux-amd64) EXPECTED_ELF="x86-64"  ;;
    linux-arm64) EXPECTED_ELF="aarch64" ;;
    linux-armhf) EXPECTED_ELF="ARM"     ;;
esac

for candidate in \
    "${INSTALL_DIR}/release/dplaned-${ARCH_TAG}" \
    "${INSTALL_DIR}/release/dplaned" \
    "${INSTALL_DIR}/daemon/dplaned-${ARCH_TAG}" \
    "${INSTALL_DIR}/daemon/dplaned"; do
    if [ -f "$candidate" ] && file "$candidate" 2>/dev/null | grep -q "ELF"; then
        BIN_ELF=$(file "$candidate" | grep -oP '(x86-64|aarch64|ARM)' | head -1 || echo "")
        if [ "$BIN_ELF" = "$EXPECTED_ELF" ]; then
            BINARY_SRC="$candidate"
            break
        else
            warn "Skipping $(basename "$candidate") — arch mismatch ($BIN_ELF vs $EXPECTED_ELF)"
        fi
    fi
done

if [ -n "$BINARY_SRC" ]; then
    [ "$BINARY_SRC" != "${INSTALL_DIR}/daemon/dplaned" ] \
        && cp "$BINARY_SRC" "${INSTALL_DIR}/daemon/dplaned"
    chmod +x "${INSTALL_DIR}/daemon/dplaned"
    log "Binary: $BINARY_SRC ($ARCH_TAG)"
elif command -v go &>/dev/null; then
    info "Building from source (Go detected — ~2 min)..."
    cd "${INSTALL_DIR}/daemon"
    go mod tidy -e 2>&1 | tail -3

    # Attempt fully static build via musl (preferred: survives glibc updates)
    if command -v musl-gcc &>/dev/null; then
        info "musl-gcc found — building fully static binary (glibc-independent)..."
        CC=musl-gcc CGO_ENABLED=1 \
            go build \
            -tags "sqlite_fts5" \
            -ldflags="-s -w -X main.Version=${VERSION} -linkmode external -extldflags -static" \
            -o "${INSTALL_DIR}/daemon/dplaned" ./cmd/dplaned/ 2>&1 | tail -5
        if ldd "${INSTALL_DIR}/daemon/dplaned" 2>&1 | grep -q "not a dynamic executable"; then
            log "Static binary built successfully — no glibc dependency"
        else
            warn "musl build produced dynamic binary — falling back to glibc build"
            CGO_ENABLED=1 \
                go build -tags "sqlite_fts5" \
                -ldflags="-s -w -X main.Version=${VERSION}" \
                -o "${INSTALL_DIR}/daemon/dplaned" ./cmd/dplaned/ 2>&1 | tail -5
        fi
    else
        # Standard glibc build — requires glibc ≥ 2.34 (Ubuntu 22.04+)
        info "Building with glibc (install musl-tools for a glibc-independent binary)..."
        CGO_ENABLED=1 \
            go build -tags "sqlite_fts5" \
            -ldflags="-s -w -X main.Version=${VERSION}" \
            -o "${INSTALL_DIR}/daemon/dplaned" ./cmd/dplaned/ 2>&1 | tail -5
    fi
    cd "$SCRIPT_DIR"
    log "Built from source"
else
    die "No pre-built binary for $ARCH_TAG and Go is not installed.
  Option A: Install Go  →  https://go.dev/dl/  then re-run install.sh
  Option B: Download pre-built binary for $ARCH_TAG from:
            https://github.com/4nonX/dplaneos/releases/v${VERSION}"
fi

INSTALL_PHASE=6

# ────────────────────────────────────────────────────────────────────────────
step "Phase 6/12: sudoers"
# ────────────────────────────────────────────────────────────────────────────

SUDOERS_TMP=$(mktemp)
cat > "$SUDOERS_TMP" <<'SUDOERS'
# D-PlaneOS daemon permissions — managed by install.sh
Defaults:www-data !requiretty
www-data ALL=(ALL) NOPASSWD: /sbin/zfs, /sbin/zpool
www-data ALL=(ALL) NOPASSWD: /usr/sbin/zfs, /usr/sbin/zpool
www-data ALL=(ALL) NOPASSWD: /usr/sbin/smartctl
www-data ALL=(ALL) NOPASSWD: /usr/bin/docker ps
www-data ALL=(ALL) NOPASSWD: /usr/bin/docker inspect *
www-data ALL=(ALL) NOPASSWD: /usr/bin/docker stats *
www-data ALL=(ALL) NOPASSWD: /sbin/modprobe -n zfs
www-data ALL=(ALL) NOPASSWD: /bin/systemctl status *
www-data ALL=(ALL) NOPASSWD: /usr/bin/lsblk
www-data ALL=(ALL) NOPASSWD: /usr/bin/lsusb
www-data ALL=(ALL) NOPASSWD: /usr/bin/lspci
SUDOERS

if visudo -c -f "$SUDOERS_TMP" &>/dev/null; then
    cp "$SUDOERS_TMP" /etc/sudoers.d/dplaneos
    chmod 440 /etc/sudoers.d/dplaneos
    log "sudoers configured and validated"
else
    warn "sudoers validation failed — skipping (daemon still works as root)"
fi
rm -f "$SUDOERS_TMP"

INSTALL_PHASE=7

# ────────────────────────────────────────────────────────────────────────────
step "Phase 7/12: Database"
# ────────────────────────────────────────────────────────────────────────────

mkdir -p "$(dirname "$DB_PATH")"
GENERATED_ADMIN_PASSWORD=""

if [ ! -f "$DB_PATH" ]; then
    ADMIN_PASSWORD=$(openssl rand -base64 18 | tr -dc 'A-Za-z0-9!@#$' | head -c 16)
    ADMIN_HASH=$(
        python3 -c "import bcrypt; print(bcrypt.hashpw(b'${ADMIN_PASSWORD}', bcrypt.gensalt(12)).decode())" 2>/dev/null \
        || htpasswd -bnBC 12 "" "${ADMIN_PASSWORD}" 2>/dev/null | tr -d ':\n' | sed 's/^://' \
        || die "Cannot generate bcrypt hash — install python3-bcrypt or apache2-utils"
    )

    sqlite3 "$DB_PATH" <<'SCHEMA'
PRAGMA journal_mode = WAL;
PRAGMA foreign_keys = ON;
PRAGMA busy_timeout = 5000;
CREATE TABLE IF NOT EXISTS users (
    id INTEGER PRIMARY KEY AUTOINCREMENT, username TEXT UNIQUE NOT NULL,
    password_hash TEXT NOT NULL, email TEXT, role TEXT DEFAULT 'user',
    active INTEGER DEFAULT 1, must_change_password INTEGER DEFAULT 0,
    created_at INTEGER DEFAULT (strftime('%s', 'now'))
);
INSERT OR IGNORE INTO users (username, password_hash, email, role, active, must_change_password)
    VALUES ('admin', '__HASH__', 'admin@localhost', 'admin', 1, 1);
CREATE TABLE IF NOT EXISTS sessions (
    id INTEGER PRIMARY KEY AUTOINCREMENT, session_id TEXT UNIQUE NOT NULL,
    user_id INTEGER, username TEXT, expires_at INTEGER,
    created_at INTEGER DEFAULT (strftime('%s', 'now')),
    FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS idx_sessions_token   ON sessions(session_id);
CREATE INDEX IF NOT EXISTS idx_sessions_expires ON sessions(expires_at);
CREATE TABLE IF NOT EXISTS settings (key TEXT PRIMARY KEY, value TEXT, updated_at INTEGER DEFAULT (strftime('%s', 'now')));
CREATE TABLE IF NOT EXISTS audit_logs (
    id INTEGER PRIMARY KEY AUTOINCREMENT, timestamp INTEGER DEFAULT (strftime('%s', 'now')),
    user TEXT, action TEXT, resource TEXT, details TEXT, ip_address TEXT, success INTEGER DEFAULT 1
);
CREATE INDEX IF NOT EXISTS idx_audit_timestamp ON audit_logs(timestamp DESC);
CREATE INDEX IF NOT EXISTS idx_audit_user      ON audit_logs(user);
CREATE TABLE IF NOT EXISTS alerts (
    id INTEGER PRIMARY KEY AUTOINCREMENT, alert_id TEXT UNIQUE NOT NULL,
    category TEXT NOT NULL, priority TEXT NOT NULL, title TEXT NOT NULL,
    message TEXT NOT NULL, details TEXT, count INTEGER DEFAULT 1,
    first_seen INTEGER NOT NULL, last_seen INTEGER NOT NULL,
    acknowledged INTEGER DEFAULT 0, acknowledged_at INTEGER, acknowledged_by TEXT,
    dismissed INTEGER DEFAULT 0, dismissed_at INTEGER, auto_dismiss INTEGER DEFAULT 0, expires_at INTEGER
);
CREATE INDEX IF NOT EXISTS idx_alerts_priority     ON alerts(priority);
CREATE INDEX IF NOT EXISTS idx_alerts_acknowledged ON alerts(acknowledged);
CREATE TABLE IF NOT EXISTS files (
    id INTEGER PRIMARY KEY AUTOINCREMENT, path TEXT UNIQUE NOT NULL, name TEXT NOT NULL,
    parent_id INTEGER, type TEXT, size INTEGER, modified_time INTEGER,
    created_at INTEGER DEFAULT (strftime('%s', 'now'))
);
CREATE INDEX IF NOT EXISTS idx_files_path   ON files(path);
CREATE INDEX IF NOT EXISTS idx_files_parent ON files(parent_id);
CREATE VIRTUAL TABLE IF NOT EXISTS files_fts USING fts5(
    path, name, content=files, content_rowid=id,
    tokenize='porter unicode61 remove_diacritics 1'
);
CREATE TRIGGER IF NOT EXISTS files_fts_insert AFTER INSERT ON files BEGIN
    INSERT INTO files_fts(rowid, path, name) VALUES (new.id, new.path, new.name); END;
CREATE TRIGGER IF NOT EXISTS files_fts_delete AFTER DELETE ON files BEGIN
    DELETE FROM files_fts WHERE rowid = old.id; END;
CREATE TRIGGER IF NOT EXISTS files_fts_update AFTER UPDATE ON files BEGIN
    DELETE FROM files_fts WHERE rowid = old.id;
    INSERT INTO files_fts(rowid, path, name) VALUES (new.id, new.path, new.name); END;
ANALYZE; PRAGMA optimize;
SCHEMA

    sqlite3 "$DB_PATH" "UPDATE users SET password_hash = '${ADMIN_HASH}' WHERE username = 'admin';"
    GENERATED_ADMIN_PASSWORD="$ADMIN_PASSWORD"
    log "Database created (WAL + FTS5)"
else
    info "Existing database — running migrations..."
    sqlite3 "$DB_PATH" "ALTER TABLE users ADD COLUMN must_change_password INTEGER DEFAULT 0;" 2>/dev/null || true
    sqlite3 "$DB_PATH" "CREATE INDEX IF NOT EXISTS idx_audit_timestamp ON audit_logs(timestamp DESC);" 2>/dev/null || true
    sqlite3 "$DB_PATH" <<'FTS5'
CREATE VIRTUAL TABLE IF NOT EXISTS files_fts USING fts5(
    path, name, content=files, content_rowid=id, tokenize='porter unicode61 remove_diacritics 1');
CREATE TRIGGER IF NOT EXISTS files_fts_insert AFTER INSERT ON files BEGIN
    INSERT INTO files_fts(rowid, path, name) VALUES (new.id, new.path, new.name); END;
CREATE TRIGGER IF NOT EXISTS files_fts_delete AFTER DELETE ON files BEGIN
    DELETE FROM files_fts WHERE rowid = old.id; END;
CREATE TRIGGER IF NOT EXISTS files_fts_update AFTER UPDATE ON files BEGIN
    DELETE FROM files_fts WHERE rowid = old.id;
    INSERT INTO files_fts(rowid, path, name) VALUES (new.id, new.path, new.name); END;
FTS5
    log "Database migrated"
fi

# LDAP tables (idempotent)
LDAP_MIGRATION="${INSTALL_DIR}/daemon/internal/database/migrations/009_ldap_integration.sql"
if [ -f "$LDAP_MIGRATION" ]; then
    sqlite3 "$DB_PATH" < "$LDAP_MIGRATION" 2>/dev/null && log "LDAP migration applied"
else
    sqlite3 "$DB_PATH" <<'LDAP'
CREATE TABLE IF NOT EXISTS ldap_config (id INTEGER PRIMARY KEY CHECK (id=1), enabled INTEGER NOT NULL DEFAULT 0,
  server TEXT NOT NULL DEFAULT '', port INTEGER NOT NULL DEFAULT 389, use_tls INTEGER NOT NULL DEFAULT 1,
  bind_dn TEXT NOT NULL DEFAULT '', bind_password TEXT NOT NULL DEFAULT '', base_dn TEXT NOT NULL DEFAULT '',
  user_filter TEXT NOT NULL DEFAULT '(&(objectClass=user)(sAMAccountName={username}))',
  user_id_attr TEXT NOT NULL DEFAULT 'sAMAccountName', user_name_attr TEXT NOT NULL DEFAULT 'displayName',
  user_email_attr TEXT NOT NULL DEFAULT 'mail', group_base_dn TEXT NOT NULL DEFAULT '',
  group_filter TEXT NOT NULL DEFAULT '(&(objectClass=group)(member={user_dn}))',
  group_member_attr TEXT NOT NULL DEFAULT 'member', jit_provisioning INTEGER NOT NULL DEFAULT 1,
  default_role TEXT NOT NULL DEFAULT 'user', sync_interval INTEGER NOT NULL DEFAULT 3600,
  timeout INTEGER NOT NULL DEFAULT 10, last_test_at TEXT, last_test_ok INTEGER DEFAULT 0,
  last_test_msg TEXT DEFAULT '', last_sync_at TEXT, last_sync_ok INTEGER DEFAULT 0,
  last_sync_count INTEGER DEFAULT 0, last_sync_msg TEXT DEFAULT '',
  created_at TEXT NOT NULL DEFAULT (datetime('now')), updated_at TEXT NOT NULL DEFAULT (datetime('now')));
INSERT OR IGNORE INTO ldap_config (id) VALUES (1);
CREATE TABLE IF NOT EXISTS ldap_group_mappings (id INTEGER PRIMARY KEY AUTOINCREMENT,
  ldap_group TEXT NOT NULL, role_name TEXT NOT NULL, role_id INTEGER,
  created_at TEXT NOT NULL DEFAULT (datetime('now')), UNIQUE(ldap_group, role_name));
CREATE TABLE IF NOT EXISTS ldap_sync_log (id INTEGER PRIMARY KEY AUTOINCREMENT,
  sync_type TEXT NOT NULL, success INTEGER NOT NULL DEFAULT 0, users_synced INTEGER NOT NULL DEFAULT 0,
  users_created INTEGER NOT NULL DEFAULT 0, users_updated INTEGER NOT NULL DEFAULT 0,
  users_disabled INTEGER NOT NULL DEFAULT 0, error_msg TEXT DEFAULT '',
  duration_ms INTEGER NOT NULL DEFAULT 0, created_at TEXT NOT NULL DEFAULT (datetime('now')));
LDAP
    log "LDAP tables ready (inline)"
fi

chmod 600 "$DB_PATH"

INSTALL_PHASE=8

# ────────────────────────────────────────────────────────────────────────────
step "Phase 8/12: nginx"
# ────────────────────────────────────────────────────────────────────────────

cat > /etc/nginx/sites-available/dplaneos <<NGINX
# D-PlaneOS v${VERSION} — do not edit manually (regenerated by install.sh)
server {
    listen ${OPT_PORT} default_server;
    listen [::]:${OPT_PORT} default_server;
    server_name _;
    root ${INSTALL_DIR}/app;
    index index.html;
    add_header X-Frame-Options "SAMEORIGIN" always;
    add_header X-Content-Type-Options "nosniff" always;
    add_header X-XSS-Protection "1; mode=block" always;
    add_header Referrer-Policy "strict-origin-when-cross-origin" always;
    add_header Content-Security-Policy "default-src 'self'; script-src 'self' 'unsafe-inline'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; connect-src 'self' ws: wss:; font-src 'self'; frame-ancestors 'self';" always;
    add_header Permissions-Policy "camera=(), microphone=(), geolocation=()" always;
    location / { try_files \$uri \$uri/ /pages/index.html; }
    location ~* \.(js|css|png|jpg|jpeg|gif|ico|svg|woff|woff2|ttf|eot)\$ {
        expires 1y; add_header Cache-Control "public, immutable"; }
    location /api/ {
        proxy_pass http://127.0.0.1:9000;
        proxy_http_version 1.1;
        proxy_set_header Host \$host;
        proxy_set_header X-Real-IP \$remote_addr;
        proxy_set_header X-Forwarded-For \$proxy_add_x_forwarded_for;
        proxy_read_timeout 120s; proxy_connect_timeout 10s; }
    location /metrics {
        proxy_pass http://127.0.0.1:9000; proxy_http_version 1.1;
        proxy_set_header Host \$host; }
    location /ws/ {
        proxy_pass http://127.0.0.1:9000; proxy_http_version 1.1;
        proxy_set_header Upgrade \$http_upgrade; proxy_set_header Connection "upgrade";
        proxy_set_header Host \$host; proxy_read_timeout 86400s; }
    location /health { proxy_pass http://127.0.0.1:9000/health; access_log off; }
    location ~ \.php\$ { deny all; }
    location ~ /\. { deny all; }
}
NGINX

rm -f /etc/nginx/sites-enabled/default
ln -sf /etc/nginx/sites-available/dplaneos /etc/nginx/sites-enabled/
rm -f /var/www/html/index.html /var/www/html/index.nginx-debian.html 2>/dev/null || true
nginx -t 2>&1 || die "nginx config invalid — check /etc/nginx/sites-available/dplaneos"
log "nginx configured (port ${OPT_PORT})"

INSTALL_PHASE=9

# ────────────────────────────────────────────────────────────────────────────
step "Phase 9/12: Kernel tuning"
# ────────────────────────────────────────────────────────────────────────────

cat > /etc/sysctl.d/99-dplaneos.conf <<'SYSCTL'
fs.inotify.max_user_watches   = 524288
fs.inotify.max_user_instances = 512
fs.file-max = 2097152
net.core.rmem_max        = 134217728
net.core.wmem_max        = 134217728
net.ipv4.tcp_rmem        = 4096 87380 67108864
net.ipv4.tcp_wmem        = 4096 65536 67108864
vm.swappiness            = 10
vm.vfs_cache_pressure    = 50
SYSCTL
sysctl -p /etc/sysctl.d/99-dplaneos.conf >/dev/null 2>&1 || warn "sysctl failed (will apply on reboot)"
log "Kernel tuning applied"

INSTALL_PHASE=10

# ────────────────────────────────────────────────────────────────────────────
step "Phase 10/12: Docker (optional)"
# ────────────────────────────────────────────────────────────────────────────

if command -v docker &>/dev/null; then
    DOCKER_FS=$(df -T /var/lib/docker 2>/dev/null | awk 'NR==2{print $2}' || echo "")
    mkdir -p /etc/docker
    if [ "$DOCKER_FS" = "zfs" ]; then
        cat > /etc/docker/daemon.json <<'DOCKERCFG'
{"storage-driver":"zfs","log-driver":"json-file","log-opts":{"max-size":"10m","max-file":"3"}}
DOCKERCFG
        systemctl is-active docker &>/dev/null && systemctl restart docker || true
        log "Docker: ZFS native driver configured"
    else
        log "Docker: keeping default driver ($DOCKER_FS)"
    fi
else
    log "Docker not installed — skipping"
fi

INSTALL_PHASE=11

# ────────────────────────────────────────────────────────────────────────────
step "Phase 11/12: Services"
# ────────────────────────────────────────────────────────────────────────────

# ZFS mount gate
if [ -f "${INSTALL_DIR}/systemd/dplaneos-zfs-mount-wait.service" ]; then
    cp "${INSTALL_DIR}/systemd/dplaneos-zfs-mount-wait.service" /etc/systemd/system/
    mkdir -p /etc/systemd/system/docker.service.d
    [ -f "${INSTALL_DIR}/systemd/docker.service.d/zfs-dependency.conf" ] \
        && cp "${INSTALL_DIR}/systemd/docker.service.d/zfs-dependency.conf" \
              /etc/systemd/system/docker.service.d/
    systemctl enable dplaneos-zfs-mount-wait.service 2>/dev/null || true
    log "ZFS mount gate installed"
else
    warn "ZFS mount gate not found — Docker may race with ZFS on boot"
fi

# Main daemon service
if [ -f "${INSTALL_DIR}/systemd/dplaned.service" ]; then
    cp "${INSTALL_DIR}/systemd/dplaned.service" /etc/systemd/system/dplaned.service
else
    cat > /etc/systemd/system/dplaned.service <<UNIT
[Unit]
Description=D-PlaneOS System Daemon v${VERSION}
After=network.target zfs.target dplaneos-zfs-mount-wait.service
Requires=dplaneos-zfs-mount-wait.service
Wants=zfs.target
[Service]
Type=simple
ExecStart=${INSTALL_DIR}/daemon/dplaned -db ${DB_PATH} -listen 127.0.0.1:9000 -smb-conf /var/lib/dplaneos/smb-shares.conf
WorkingDirectory=${INSTALL_DIR}
Restart=always
RestartSec=5
User=root
StandardOutput=journal
StandardError=journal
LimitNOFILE=65536
StartLimitIntervalSec=60
StartLimitBurst=5
[Install]
WantedBy=multi-user.target
UNIT
fi

# Record ZFS pool list for boot gate
if command -v zpool &>/dev/null; then
    CURRENT_POOLS=$(zpool list -H -o name 2>/dev/null || true)
    if [ -n "$CURRENT_POOLS" ]; then
        { echo "# Auto-generated $(date)"; echo "$CURRENT_POOLS"; } > /etc/dplaneos/expected-pools.conf
        log "ZFS pools recorded: $(echo "$CURRENT_POOLS" | tr '\n' ' ')"
    fi
fi

systemctl daemon-reload
systemctl enable dplaned nginx 2>/dev/null

systemctl restart nginx || die "nginx failed — check: journalctl -xe -u nginx"

if systemctl restart dplaned 2>/dev/null; then
    for i in $(seq 1 15); do
        curl -sf http://127.0.0.1:9000/health &>/dev/null && break
        sleep 1
    done
    curl -sf http://127.0.0.1:9000/health &>/dev/null \
        && log "dplaned running and healthy" \
        || warn "dplaned started but health check timed out (may still be initializing)"
else
    warn "dplaned did not start — check: systemctl status dplaned"
fi

# Recovery CLI
if [ -f "${INSTALL_DIR}/scripts/recovery-cli.sh" ]; then
    cp "${INSTALL_DIR}/scripts/recovery-cli.sh" /usr/local/bin/dplaneos-recovery
    chmod +x /usr/local/bin/dplaneos-recovery
    log "Recovery CLI: /usr/local/bin/dplaneos-recovery"
fi

# Watchdog cron
if [ -f "${INSTALL_DIR}/scripts/dplaneos-watchdog.sh" ]; then
    cp "${INSTALL_DIR}/scripts/dplaneos-watchdog.sh" /usr/local/bin/dplaneos-watchdog
    chmod +x /usr/local/bin/dplaneos-watchdog
    (crontab -l 2>/dev/null | grep -q dplaneos-watchdog) \
        || { crontab -l 2>/dev/null; echo "*/5 * * * * /usr/local/bin/dplaneos-watchdog >/dev/null 2>&1"; } | crontab -
    log "Watchdog: every 5 min via cron"
fi

INSTALL_PHASE=12

# ────────────────────────────────────────────────────────────────────────────
step "Phase 12/12: Validation"
# ────────────────────────────────────────────────────────────────────────────

if [ -f "${INSTALL_DIR}/scripts/post-install-validation.sh" ]; then
    bash "${INSTALL_DIR}/scripts/post-install-validation.sh" \
        && log "All checks passed" \
        || warn "Some checks failed — run: sudo dplaneos-recovery"
fi

# ── Success ───────────────────────────────────────────────────────────────────
trap - EXIT   # disarm rollback trap

MY_IP=$(hostname -I 2>/dev/null | awk '{print $1}' || echo "your-server-ip")
PORT_SUFFIX=$( [ "$OPT_PORT" = "80" ] && echo "" || echo ":${OPT_PORT}" )

clear
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo -e "${BOLD}${GREEN}    D-PlaneOS v${VERSION} — Installation Complete!${NC}"
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo ""
echo -e "  🌐  Open ${BOLD}http://${MY_IP}${PORT_SUFFIX}${NC} in your browser"
echo ""
echo "  Username : admin"
if [ -n "${GENERATED_ADMIN_PASSWORD}" ]; then
    echo -e "  Password : ${BOLD}${GENERATED_ADMIN_PASSWORD}${NC}"
    echo ""
    echo -e "  ${YELLOW}⚠  Save this password now — it will not be shown again.${NC}"
    echo -e "  ${YELLOW}   You will be required to change it on first login.${NC}"
else
    echo "  Password : (unchanged — this was an upgrade)"
fi
echo ""
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo "  If anything is wrong:"
echo ""
echo "    sudo dplaneos-recovery              # interactive menu"
echo "    journalctl -xe -u dplaned           # daemon logs"
echo "    journalctl -xe -u nginx             # web server logs"
if [ -n "$BACKUP_PATH" ]; then
    echo "    sudo bash ${BACKUP_PATH}/rollback.sh"
fi
echo ""
echo "  Full log: $LOG_FILE"
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo ""
