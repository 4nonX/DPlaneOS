#!/bin/bash
#
# D-PlaneOS - Installer
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
readonly DPLANEOS_VERSION="$(cat "$SCRIPT_DIR/VERSION" 2>/dev/null | tr -d '[:space:]')"
[ -z "$DPLANEOS_VERSION" ] && { echo "ERROR: Could not read VERSION file" >&2; exit 1; }
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

# ── Trap - rollback on unexpected failure ─────────────────────────────────────
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
            || echo -e "${RED}✗ Rollback also failed - run: sudo dplaneos-recovery${NC}"
    else
        echo "No backup available (fresh install failure)."
        echo "Run: sudo dplaneos-recovery"
    fi
    echo "Log: $LOG_FILE"
    ROLLBACK_DONE=true
}
trap cleanup EXIT

# ── Banner ────────────────────────────────────────────────────────────────────
[ -n "$TERM" ] && clear || true
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo -e "${BOLD}    D-PlaneOS v${DPLANEOS_VERSION} - Installer${NC}"
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo ""
echo "  Architecture : $ARCH ($ARCH_TAG)"
echo "  Port         : $OPT_PORT"
echo "  Mode         : $( $OPT_UPGRADE && echo 'Upgrade' || echo 'Fresh install')"
echo "  Log          : $LOG_FILE"
echo ""

# ────────────────────────────────────────────────────────────────────────────
step "Phase 0/13: Pre-flight checks"
# ────────────────────────────────────────────────────────────────────────────

[ -f /etc/os-release ] || die "Cannot detect OS"
# Safe extraction - avoids Ubuntu's readonly VERSION variable crash
OS_ID=$(grep -E '^ID=' /etc/os-release | cut -d= -f2 | tr -d '"')
OS_PRETTY=$(grep -E '^PRETTY_NAME=' /etc/os-release | cut -d= -f2 | tr -d '"')
OS_VERSION_ID=$(grep -E '^VERSION_ID=' /etc/os-release | cut -d= -f2 | tr -d '"')
case "${OS_ID,,}" in
    debian|ubuntu|raspbian|linuxmint|pop)
        log "OS: ${OS_PRETTY:-$OS_ID}" ;;
    nixos)
        # NixOS detected - warn but do NOT terminate; NixOS users should use nixos/ directory
        warn "NixOS detected. Native package management is handled via nixos/. Proceeding with best-effort install."
        warn "For a fully declarative NixOS setup, see nixos/NIXOS-INSTALL-GUIDE.md instead." ;;
    *)
        die "Unsupported OS: ${OS_PRETTY:-unknown}
  Supported: Debian 12+, Ubuntu 22.04+, Raspberry Pi OS (64-bit, Debian 12 based)
  Other distros: install manually - see docs/manual-install.md" ;;
esac

TOTAL_RAM_MB=$(free -m | awk '/^Mem:/{print $2}')
[ "$TOTAL_RAM_MB" -ge 1024 ] || warn "Low RAM: ${TOTAL_RAM_MB}MB - 2GB+ recommended"
log "RAM: ${TOTAL_RAM_MB}MB"

ROOT_FREE_GB=$(df -BG / | awk 'NR==2{gsub(/G/,"",$4); print $4}')
[ "$ROOT_FREE_GB" -ge 4 ] || die "Insufficient disk: ${ROOT_FREE_GB}GB free (need 4GB+)"
log "Disk free: ${ROOT_FREE_GB}GB"

if ss -tuln 2>/dev/null | grep -q ":${OPT_PORT} "; then
    $OPT_UPGRADE \
        && warn "Port ${OPT_PORT} in use - will reconfigure (expected on upgrade)" \
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
step "Phase 1/13: Backup"
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
# Rollback script - auto-generated $(date)
# Usage: sudo bash ${BACKUP_PATH}/rollback.sh
set -euo pipefail
systemctl stop dplaned nginx 2>/dev/null || true
[ -f "${BACKUP_PATH}/dplaneos.db" ]         && cp "${BACKUP_PATH}/dplaneos.db" "${DB_PATH}" && chmod 600 "${DB_PATH}" && echo "DB restored"
[ -f "${BACKUP_PATH}/nginx-dplaneos.conf" ] && cp "${BACKUP_PATH}/nginx-dplaneos.conf" /etc/nginx/sites-available/dplaneos && echo "nginx config restored"
[ -f "${BACKUP_PATH}/dplaned.service" ]     && cp "${BACKUP_PATH}/dplaned.service" /etc/systemd/system/dplaned.service && systemctl daemon-reload && echo "systemd unit restored"
[ -f "${BACKUP_PATH}/sudoers-dplaneos" ]    && cp "${BACKUP_PATH}/sudoers-dplaneos" /etc/sudoers.d/dplaneos && chmod 440 /etc/sudoers.d/dplaneos && echo "sudoers restored"
systemctl start nginx dplaned 2>/dev/null || true
echo "Rollback complete - access: http://\$(hostname -I | awk '{print \$1}')"
RBSCRIPT
    chmod +x "${BACKUP_PATH}/rollback.sh"
    log "Backup complete ($(du -sh "$BACKUP_PATH" | cut -f1)) - rollback: ${BACKUP_PATH}/rollback.sh"
else
    log "Fresh install - no backup needed"
fi

INSTALL_PHASE=2

# ────────────────────────────────────────────────────────────────────────────
step "Phase 2/13: System dependencies"
# ────────────────────────────────────────────────────────────────────────────

export DEBIAN_FRONTEND=noninteractive

# Enable extra repos required for zfsutils-linux and python3-bcrypt
# Ubuntu: universe   Debian: contrib
case "${OS_ID,,}" in
    ubuntu|pop|linuxmint|raspbian)
        if ! apt-cache show zfsutils-linux &>/dev/null 2>&1; then
            apt-get install -y -qq software-properties-common &>/dev/null
            add-apt-repository -y universe &>/dev/null
            log "Ubuntu universe repository enabled"
        fi
        ;;
    debian)
        if ! grep -qE "contrib" /etc/apt/sources.list /etc/apt/sources.list.d/*.list 2>/dev/null; then
            # Enable contrib for zfsutils-linux on Debian
            sed -i 's/^\(deb .*\)main$/\1main contrib non-free/' /etc/apt/sources.list
            log "Debian contrib/non-free enabled"
        fi
        ;;
esac

apt-get update -qq 2>&1 | tail -1

PACKAGES=(nginx sqlite3 smartmontools lsof udev
          acl ufw hdparm git openssh-client openssl ca-certificates
          iproute2 procps coreutils
          python3-bcrypt apache2-utils
          musl-tools            # enables fully static binary (glibc-independent)
          samba                 # SMB/CIFS file shares
          nfs-kernel-server     # NFS file shares
          avahi-daemon          # mDNS: makes NAS visible as hostname.local
          )

# ZFS needs both the utils AND matching kernel headers (for DKMS module build)
PACKAGES+=(zfsutils-linux "linux-headers-$(uname -r)")

for pkg in "${PACKAGES[@]}"; do
    if dpkg -l "$pkg" 2>/dev/null | grep -q "^ii"; then
        log "  $pkg (already installed)"
    else
        apt-get install -y -qq "$pkg" 2>&1 | tail -1 \
            && log "  $pkg" \
            || warn "  $pkg failed - may be optional"
    fi
done

INSTALL_PHASE=3

# ────────────────────────────────────────────────────────────────────────────
step "Phase 3/13: ZFS setup"
# ────────────────────────────────────────────────────────────────────────────

if ! lsmod | grep -q "^zfs "; then
    modprobe zfs 2>/dev/null \
        && log "ZFS module loaded" \
        || warn "ZFS module failed: try: sudo apt install linux-headers-\$(uname -r) && sudo dpkg-reconfigure zfs-dkms"
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
step "Phase 4/13: Installing files"
# ────────────────────────────────────────────────────────────────────────────

mkdir -p "$INSTALL_DIR" /var/lib/dplaneos/{backups,git-stacks,custom_icons} /var/log/dplaneos /etc/dplaneos

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
# /var/lib/dplaneos: root-owned, root-only access at directory level.
# Subdirectories get their own explicit permissions below.
chmod 700 /var/lib/dplaneos
# custom_icons: root-owned, world-readable so nginx can also serve them
# if configured as a static location. The daemon (running as root) always
# has write access. To upload icons without root, copy files here via SSH
# or use the daemon's /api/assets/custom-icons upload endpoint (if configured).
chown root:root /var/lib/dplaneos/custom_icons
chmod 755 /var/lib/dplaneos/custom_icons
log "Files installed to $INSTALL_DIR"

INSTALL_PHASE=5

# ────────────────────────────────────────────────────────────────────────────
step "Phase 5/13: Daemon binary"
# ────────────────────────────────────────────────────────────────────────────

# try_download_binary: download a pre-built release tarball from GitHub when
# no Go toolchain is present and no pre-built binary was found locally.
try_download_binary() {
    local dl_arch
    case "$(uname -m)" in
        x86_64)        dl_arch="amd64" ;;
        aarch64|arm64) dl_arch="arm64" ;;
        *)
            echo ""
            echo "ERROR: No Go toolchain found and auto-download failed."
            echo "  Download the release tarball from: https://github.com/4nonX/D-PlaneOS/releases/latest"
            echo "  Extract it and run install.sh from the extracted directory."
            exit 1
            ;;
    esac

    local tarball_url="https://github.com/4nonX/D-PlaneOS/releases/latest/download/dplaneos-v${DPLANEOS_VERSION}-linux-${dl_arch}.tar.gz"
    local tmp_tar
    tmp_tar=$(mktemp --suffix=".tar.gz")

    info "No Go toolchain found - attempting binary download for linux-${dl_arch}..."
    info "  URL: ${tarball_url}"

    if curl -fsSL --max-time 120 -o "$tmp_tar" "$tarball_url" 2>/dev/null; then
        mkdir -p "${INSTALL_DIR}/build"
        if tar -xzf "$tmp_tar" -C "${INSTALL_DIR}/build" --wildcards --no-anchored 'dplaned' 2>/dev/null \
            || tar -xzf "$tmp_tar" -O --wildcards --no-anchored 'dplaned' > "${INSTALL_DIR}/build/dplaned" 2>/dev/null; then
            chmod +x "${INSTALL_DIR}/build/dplaned"
            log "Binary downloaded and extracted to ${INSTALL_DIR}/build/dplaned"
        else
            rm -f "$tmp_tar"
            echo ""
            echo "ERROR: No Go toolchain found and auto-download failed."
            echo "  Download the release tarball from: https://github.com/4nonX/D-PlaneOS/releases/latest"
            echo "  Extract it and run install.sh from the extracted directory."
            exit 1
        fi
        rm -f "$tmp_tar"
    else
        rm -f "$tmp_tar"
        echo ""
        echo "ERROR: No Go toolchain found and auto-download failed."
        echo "  Download the release tarball from: https://github.com/4nonX/D-PlaneOS/releases/latest"
        echo "  Extract it and run install.sh from the extracted directory."
        exit 1
    fi
}

BINARY_SRC=""

# Arch to ELF identifier mapping
case "$ARCH_TAG" in
    linux-amd64) EXPECTED_ELF="x86-64"  ;;
    linux-arm64) EXPECTED_ELF="aarch64" ;;
    linux-armhf) EXPECTED_ELF="ARM"     ;;
esac

for candidate in \
    "${INSTALL_DIR}/build/dplaned-${ARCH_TAG}" \
    "${INSTALL_DIR}/build/dplaned" \
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
            warn "Skipping $(basename "$candidate") - arch mismatch ($BIN_ELF vs $EXPECTED_ELF)"
        fi
    fi
done

if [ -n "$BINARY_SRC" ]; then
    [ "$BINARY_SRC" != "${INSTALL_DIR}/daemon/dplaned" ] \
        && cp "$BINARY_SRC" "${INSTALL_DIR}/daemon/dplaned"
    chmod +x "${INSTALL_DIR}/daemon/dplaned"
    log "Binary: $BINARY_SRC ($ARCH_TAG)"
elif command -v go &>/dev/null; then
    info "Building from source (Go detected: ~2 min)..."
    cd "${INSTALL_DIR}/daemon"
    # Use vendor dir if present (airgap/offline install - no network needed)
    if [ -d "vendor" ]; then
        BUILD_MOD="-mod=vendor"
        info "Using vendored dependencies (offline/airgap mode)"
    else
        BUILD_MOD=""
        go mod tidy -e 2>&1 | tail -3
    fi

    # Attempt fully static build via musl (preferred: survives glibc updates)
    if command -v musl-gcc &>/dev/null; then
        info "musl-gcc found - building fully static binary (glibc-independent)..."
        CC=musl-gcc CGO_ENABLED=1 \
            go build $BUILD_MOD \
            -tags "sqlite_fts5" \
            -ldflags="-s -w -X main.Version=${DPLANEOS_VERSION} -linkmode external -extldflags -static" \
            -o "${INSTALL_DIR}/daemon/dplaned" ./cmd/dplaned/ 2>&1 | tail -5
        if ldd "${INSTALL_DIR}/daemon/dplaned" 2>&1 | grep -q "not a dynamic executable"; then
            log "Static binary built successfully - no glibc dependency"
        else
            warn "musl build produced dynamic binary - falling back to glibc build"
            CGO_ENABLED=1 \
                go build $BUILD_MOD -tags "sqlite_fts5" \
                -ldflags="-s -w -X main.Version=${DPLANEOS_VERSION}" \
                -o "${INSTALL_DIR}/daemon/dplaned" ./cmd/dplaned/ 2>&1 | tail -5
        fi
    else
        # Standard glibc build - requires glibc ≥ 2.34 (Ubuntu 22.04+)
        info "Building with glibc (install musl-tools for a glibc-independent binary)..."
        CGO_ENABLED=1 \
            go build $BUILD_MOD -tags "sqlite_fts5" \
            -ldflags="-s -w -X main.Version=${DPLANEOS_VERSION}" \
            -o "${INSTALL_DIR}/daemon/dplaned" ./cmd/dplaned/ 2>&1 | tail -5
    fi
    cd "$SCRIPT_DIR"
    log "Built from source"
else
    try_download_binary
fi

INSTALL_PHASE=6

# ────────────────────────────────────────────────────────────────────────────
step "Phase 6/13: sudoers"
# ────────────────────────────────────────────────────────────────────────────

SUDOERS_TMP=$(mktemp)
cat > "$SUDOERS_TMP" <<'SUDOERS'
# D-PlaneOS daemon permissions - managed by install.sh
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
    warn "sudoers validation failed - skipping (daemon still works as root)"
fi
rm -f "$SUDOERS_TMP"

INSTALL_PHASE=7

# ────────────────────────────────────────────────────────────────────────────
step "Phase 7/13: Database"
# ────────────────────────────────────────────────────────────────────────────

mkdir -p "$(dirname "$DB_PATH")"
GENERATED_ADMIN_PASSWORD=""

if [ ! -f "$DB_PATH" ]; then
    ADMIN_PASSWORD=$(openssl rand -base64 18 | tr -dc 'A-Za-z0-9!@#$' | head -c 16)
    ADMIN_HASH=$(
        python3 -c "import bcrypt; print(bcrypt.hashpw(b'${ADMIN_PASSWORD}', bcrypt.gensalt(12)).decode())" 2>/dev/null \
        || htpasswd -bnBC 12 "" "${ADMIN_PASSWORD}" 2>/dev/null | tr -d ':\n' | sed 's/^://' \
        || die "Cannot generate bcrypt hash - install python3-bcrypt or apache2-utils"
    )

    sqlite3 "$DB_PATH" <<'SCHEMA'
PRAGMA journal_mode = WAL;
PRAGMA foreign_keys = ON;
PRAGMA busy_timeout = 5000;
CREATE TABLE IF NOT EXISTS users (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    username TEXT NOT NULL UNIQUE,
    password_hash TEXT NOT NULL DEFAULT '',
    display_name TEXT NOT NULL DEFAULT '',
    email TEXT NOT NULL DEFAULT '',
    role TEXT NOT NULL DEFAULT 'user',
    active INTEGER NOT NULL DEFAULT 1,
    must_change_password INTEGER NOT NULL DEFAULT 0,
    source TEXT NOT NULL DEFAULT 'local',
    created_at TEXT NOT NULL DEFAULT (datetime('now')),
    updated_at TEXT NOT NULL DEFAULT (datetime('now'))
);
INSERT OR IGNORE INTO users (username, password_hash, display_name, email, role, active, must_change_password, source)
    VALUES ('admin', '__HASH__', 'Administrator', 'admin@localhost', 'admin', 1, 1, 'local');
CREATE TABLE IF NOT EXISTS sessions (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    session_id TEXT NOT NULL UNIQUE,
    username TEXT NOT NULL,
    user_id INTEGER NOT NULL DEFAULT 0,
    ip_address TEXT NOT NULL DEFAULT '',
    user_agent TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL DEFAULT 'active',
    created_at INTEGER NOT NULL DEFAULT (strftime('%s','now')),
    expires_at INTEGER,
    last_activity INTEGER NOT NULL DEFAULT (strftime('%s','now')),
    FOREIGN KEY (username) REFERENCES users(username)
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
    info "Existing database - running migrations..."
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

# ----------------------------------------------------------------------------
step "Phase 8/13: nginx"
# ----------------------------------------------------------------------------

cat > /etc/nginx/sites-available/dplaneos <<NGINX
# D-PlaneOS v${DPLANEOS_VERSION} - do not edit manually (regenerated by install.sh)
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
    location / { try_files \$uri \$uri/ /index.html; }
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
nginx -t 2>&1 || die "nginx config invalid - check /etc/nginx/sites-available/dplaneos"
log "nginx configured (port ${OPT_PORT})"

# ── Download web fonts (Outfit, JetBrains Mono, Material Symbols Rounded) ───
# Fonts are served from /opt/dplaneos/app/assets/fonts/ so the SPA works
# completely offline after install. If the download fails, UI falls back to
# system-ui and monospace - fully functional, just different look.
if bash "${INSTALL_DIR}/install/scripts/download-fonts.sh" "${INSTALL_DIR}/app/assets/fonts" 2>&1; then
    log "Web fonts ready (offline mode)"
else
    warn "Font download failed - UI will use system fonts (no functionality impact)"
fi

INSTALL_PHASE=9

# ────────────────────────────────────────────────────────────────────────────
step "Phase 9/13: Samba configuration"
# ────────────────────────────────────────────────────────────────────────────
# The daemon writes share definitions to /var/lib/dplaneos/smb-shares.conf.
# Samba reads /etc/samba/smb.conf by default. Bridge the two with an include
# directive so shares created in the UI are immediately visible to smbd.

SMB_DAEMON_CONF="/var/lib/dplaneos/smb-shares.conf"
SMB_SYSTEM_CONF="/etc/samba/smb.conf"

# Seed the daemon config file so it exists before smbd first reads it
if [ ! -f "$SMB_DAEMON_CONF" ]; then
    cat > "$SMB_DAEMON_CONF" <<'SMBSEED'
# D-PlaneOS share definitions - managed by the daemon, do not edit manually.
# Regenerated on every share create/update/delete via the web UI.
SMBSEED
    log "Samba daemon config seeded: $SMB_DAEMON_CONF"
fi

# Write /etc/samba/smb.conf with a global section and an include pointing at
# the daemon's file. If a previous config exists we back it up first.
if [ -f "$SMB_SYSTEM_CONF" ] && ! grep -q "dplaneos" "$SMB_SYSTEM_CONF" 2>/dev/null; then
    cp "$SMB_SYSTEM_CONF" "${SMB_SYSTEM_CONF}.pre-dplaneos.bak" 2>/dev/null || true
fi

cat > "$SMB_SYSTEM_CONF" <<SMBCONF
# /etc/samba/smb.conf - managed by D-PlaneOS install.sh
# Global settings are here; per-share definitions are in the include below.
[global]
    workgroup = WORKGROUP
    server string = D-PlaneOS NAS
    security = user
    map to guest = Bad User
    log file = /var/log/samba/log.%m
    max log size = 1000
    dns proxy = no
    # Include the daemon-managed share definitions
    include = ${SMB_DAEMON_CONF}
SMBCONF
log "Samba /etc/samba/smb.conf written (includes daemon share file)"

# Enable and start smbd/nmbd
if command -v systemctl &>/dev/null; then
    systemctl enable smbd nmbd 2>/dev/null || true
    systemctl restart smbd nmbd 2>/dev/null \
        && log "Samba services started (smbd + nmbd)" \
        || warn "Samba services did not start: check: systemctl status smbd"
fi

INSTALL_PHASE=10

# ────────────────────────────────────────────────────────────────────────────
step "Phase 10/13: Kernel tuning"
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

INSTALL_PHASE=11

# ────────────────────────────────────────────────────────────────────────────
step "Phase 11/13: Docker (optional)"
# ────────────────────────────────────────────────────────────────────────────

if command -v docker &>/dev/null; then
    log "Docker already installed"
else
    info "Docker not found - installing via official script..."
    if curl -fsSL https://get.docker.com -o /tmp/install-docker.sh 2>/dev/null; then
        sh /tmp/install-docker.sh --quiet 2>&1 | tail -5 && log "Docker installed" \
            || warn "Docker install script failed - containers unavailable (install manually later)"
        rm -f /tmp/install-docker.sh
    else
        warn "Could not reach get.docker.com - Docker not installed (containers unavailable)"
        warn "Install later: curl -fsSL https://get.docker.com | sh"
    fi
fi

if command -v docker &>/dev/null; then
    mkdir -p /etc/docker
    DOCKER_FS=$(df -T /var/lib/docker 2>/dev/null | awk 'NR==2{print $2}' || echo "overlay2")
    if [ "$DOCKER_FS" = "zfs" ]; then
        cat > /etc/docker/daemon.json \
            <<'DOCKERCFG'
{"storage-driver":"zfs","log-driver":"json-file","log-opts":{"max-size":"10m","max-file":"3"}}
DOCKERCFG
        log "Docker: ZFS native storage driver configured"
    else
        cat > /etc/docker/daemon.json \
            <<'DOCKERCFG'
{"log-driver":"json-file","log-opts":{"max-size":"10m","max-file":"3"}}
DOCKERCFG
        log "Docker: overlay2 driver (default)"
    fi
    systemctl enable docker &>/dev/null && systemctl start docker 2>/dev/null \
        && log "Docker service enabled" \
        || warn "Docker service did not start cleanly"
fi

INSTALL_PHASE=12

# ────────────────────────────────────────────────────────────────────────────
step "Phase 12/13: Services"
# ────────────────────────────────────────────────────────────────────────────

# ZFS mount gate
if [ -f "${INSTALL_DIR}/install/systemd/dplaneos-zfs-mount-wait.service" ]; then
    cp "${INSTALL_DIR}/install/systemd/dplaneos-zfs-mount-wait.service" /etc/systemd/system/
    mkdir -p /etc/systemd/system/docker.service.d
    [ -f "${INSTALL_DIR}/install/systemd/docker.service.d/zfs-dependency.conf" ] \
        && cp "${INSTALL_DIR}/install/systemd/docker.service.d/zfs-dependency.conf" \
              /etc/systemd/system/docker.service.d/
    systemctl enable dplaneos-zfs-mount-wait.service 2>/dev/null || true
    log "ZFS mount gate installed"
else
    warn "ZFS mount gate not found - Docker may race with ZFS on boot"
fi

# DB init service (must start before dplaned and dplaneos-realtime)
if [ -f "${INSTALL_DIR}/install/systemd/dplaneos-init-db.service" ]; then
    cp "${INSTALL_DIR}/install/systemd/dplaneos-init-db.service" /etc/systemd/system/
    systemctl enable dplaneos-init-db.service 2>/dev/null || true
    log "DB init service installed"
else
    warn "dplaneos-init-db.service not found - daemon may race with DB init on first boot"
fi

# Realtime monitor service
if [ -f "${INSTALL_DIR}/install/systemd/dplaneos-realtime.service" ]; then
    cp "${INSTALL_DIR}/install/systemd/dplaneos-realtime.service" /etc/systemd/system/
    systemctl enable dplaneos-realtime.service 2>/dev/null || true
    log "Realtime monitor service installed"
else
    warn "dplaneos-realtime.service not found - realtime monitoring unavailable"
fi

# Main daemon service
if [ -f "${INSTALL_DIR}/install/systemd/dplaned.service" ]; then
    cp "${INSTALL_DIR}/install/systemd/dplaned.service" /etc/systemd/system/dplaned.service
else
    cat > /etc/systemd/system/dplaned.service <<UNIT
[Unit]
Description=D-PlaneOS System Daemon v${DPLANEOS_VERSION}
After=network.target zfs.target dplaneos-zfs-mount-wait.service dplaneos-init-db.service
Requires=dplaneos-zfs-mount-wait.service dplaneos-init-db.service
Wants=zfs.target
[Service]
Type=simple
ExecStartPre=/bin/mkdir -p /var/run/dplaneos /var/lib/dplaneos /var/log/dplaneos /etc/dplaneos
ExecStart=${INSTALL_DIR}/daemon/dplaned -db ${DB_PATH} -listen 127.0.0.1:9000 -smb-conf /var/lib/dplaneos/smb-shares.conf
WorkingDirectory=${INSTALL_DIR}
Restart=always
RestartSec=5
User=root
Group=root
NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=strict
ProtectHome=true
ReadWritePaths=/var/log/dplaneos /var/lib/dplaneos /opt/dplaneos /etc/dplaneos /run/dplaneos /etc/crontab /etc/exports /etc/exports.d /etc/systemd/network /etc/samba /mnt /tank /data /media /tmp /home /etc/modprobe.d /etc/sysctl.d /etc/nginx/sites-available /etc/nginx/sites-enabled /usr/local/bin /etc/iscsi /etc/ssh
AmbientCapabilities=CAP_SYS_ADMIN CAP_NET_ADMIN CAP_DAC_READ_SEARCH CAP_CHOWN CAP_FOWNER
CapabilityBoundingSet=CAP_SYS_ADMIN CAP_NET_ADMIN CAP_DAC_READ_SEARCH CAP_CHOWN CAP_FOWNER
StandardOutput=journal
StandardError=journal
SyslogIdentifier=dplaned
LimitNOFILE=65536
MemoryMax=512M
MemoryHigh=384M
OOMScoreAdjust=-900
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
# Mark all scripts executable BEFORE starting any services
for script in \
    zfs-mount-wait.sh \
    init-database-with-lock.sh \
    validate-db-schema.sh \
    post-install-validation.sh \
    dplaneos-watchdog.sh \
    notify-device-added.sh \
    notify-device-removed.sh \
    notify-disk-added.sh \
    notify-disk-removed.sh; do
    if [ -f "${INSTALL_DIR}/install/scripts/${script}" ]; then
        chmod +x "${INSTALL_DIR}/install/scripts/${script}"
        log "install/scripts/${script} marked executable"
    fi
done

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

systemctl restart nginx || die "nginx failed - check: journalctl -xe -u nginx"

if systemctl restart dplaned; then
    for i in $(seq 1 15); do
        curl -sf http://127.0.0.1:9000/health &>/dev/null && break
        sleep 1
    done
    curl -sf http://127.0.0.1:9000/health &>/dev/null \
        && log "dplaned running and healthy" \
        || die "dplaned started but health check failed - check: journalctl -xe -u dplaned"
else
    die "dplaned did not start - check: journalctl -xe -u dplaned"
fi

# Phase 12.5: GitOps Auto-Apply
GITOPS_STATE="/etc/dplaneos/state.yaml"
if [ -f "$GITOPS_STATE" ]; then
    info "GitOps: /etc/dplaneos/state.yaml found - triggering initial bootstrap apply..."
    # Run a one-off apply to ensure deterministic setup
    if "${INSTALL_DIR}/daemon/dplaned" -apply -db "${DB_PATH}" -gitops-state "$GITOPS_STATE" >> "$LOG_FILE" 2>&1; then
        log "GitOps: Initial apply successful"
    else
        warn "GitOps: Initial apply failed - check $LOG_FILE. You can retry with: sudo dplaned -apply"
    fi
fi

# Recovery CLI
if [ -f "${INSTALL_DIR}/install/scripts/recovery-cli.sh" ]; then
    cp "${INSTALL_DIR}/install/scripts/recovery-cli.sh" /usr/local/bin/dplaneos-recovery
    chmod +x /usr/local/bin/dplaneos-recovery
    log "Recovery CLI: /usr/local/bin/dplaneos-recovery"
fi

# udev rules - removable media + hot-swap pool disks
if [ -f "${INSTALL_DIR}/install/udev/99-dplaneos-removable-media.rules" ]; then
    cp "${INSTALL_DIR}/install/udev/99-dplaneos-removable-media.rules" /etc/udev/rules.d/
    log "udev: removable media rules installed"
else
    warn "install/udev/99-dplaneos-removable-media.rules not found - USB detection unavailable"
fi
if [ -f "${INSTALL_DIR}/install/udev/99-dplaneos-hotswap.rules" ]; then
    cp "${INSTALL_DIR}/install/udev/99-dplaneos-hotswap.rules" /etc/udev/rules.d/
    log "udev: hot-swap rules installed"
else
    warn "install/udev/99-dplaneos-hotswap.rules not found - hot-swap pool disk detection unavailable"
fi
udevadm control --reload-rules 2>/dev/null && log "udev rules reloaded" || warn "udevadm reload failed (rules will apply on reboot)"

# ZED Hook for real-time ZFS events
if [ -f "${INSTALL_DIR}/install/zed/dplaneos-notify.sh" ]; then
    mkdir -p /etc/zfs/zed.d
    cp "${INSTALL_DIR}/install/zed/dplaneos-notify.sh" /etc/zfs/zed.d/
    chmod +x /etc/zfs/zed.d/dplaneos-notify.sh
    log "ZED hook installed (/etc/zfs/zed.d/dplaneos-notify.sh)"
else
    warn "ZED hook not found - ZFS events will not trigger real-time alerts"
fi

# Hot-swap notification scripts
mkdir -p "${INSTALL_DIR}/install/scripts"
for script in \
done

# Watchdog cron
if [ -f "${INSTALL_DIR}/install/scripts/dplaneos-watchdog.sh" ]; then
    cp "${INSTALL_DIR}/install/scripts/dplaneos-watchdog.sh" /usr/local/bin/dplaneos-watchdog
    chmod +x /usr/local/bin/dplaneos-watchdog
    (crontab -l 2>/dev/null | grep -q dplaneos-watchdog) \
        || { crontab -l 2>/dev/null; echo "*/5 * * * * /usr/local/bin/dplaneos-watchdog >/dev/null 2>&1"; } | crontab -
    log "Watchdog: every 5 min via cron"
fi

# v6: dplane CLI symlink for compliance tools
ln -sf "${INSTALL_DIR}/daemon/dplaned" /usr/local/bin/dplane
log "v6: dplane CLI symlink created at /usr/local/bin/dplane"

INSTALL_PHASE=13

# ────────────────────────────────────────────────────────────────────────────
step "Phase 13/13: Validation"
# ────────────────────────────────────────────────────────────────────────────

if [ -f "${INSTALL_DIR}/install/scripts/post-install-validation.sh" ]; then
    bash "${INSTALL_DIR}/install/scripts/post-install-validation.sh" \
        && log "All checks passed" \
        || warn "Some checks failed - run: sudo dplaneos-recovery"
fi

# ── Success ───────────────────────────────────────────────────────────────────
trap - EXIT   # disarm rollback trap

# Task 3: Dynamic IP notification
PRIMARY_IP=$(hostname -I | awk '{print $1}')
MY_IP="${PRIMARY_IP:-your-server-ip}"
PORT_SUFFIX=$( [ "$OPT_PORT" = "80" ] && echo "" || echo ":${OPT_PORT}" )

[ -n "$TERM" ] && clear || true
echo -e "${BOLD}${GREEN}"
echo "╔══════════════════════════════════════════════════════╗"
echo "║       D-PlaneOS v${DPLANEOS_VERSION} - Installation Complete!        ║"
echo "╠══════════════════════════════════════════════════════╣"
echo "║                                                      ║"
printf "║  🌐  Access your dashboard at:                       ║\n"
printf "║      %-48s  ║\n" "http://${MY_IP}${PORT_SUFFIX}"
echo "║                                                      ║"
echo "║  ⚠️   NOTE: The VM screen may remain BLACK after      ║"
echo "║       install. This is normal - use the URL above.   ║"
echo "║                                                      ║"
echo "╚══════════════════════════════════════════════════════╝"
echo -e "${NC}"
echo ""
echo -e "  🌐  Open ${BOLD}http://${MY_IP}${PORT_SUFFIX}${NC} in your browser"
echo ""
echo "  Username : admin"
if [ -n "${GENERATED_ADMIN_PASSWORD}" ]; then
    echo -e "  Password : ${BOLD}${GENERATED_ADMIN_PASSWORD}${NC}"
    echo ""
    echo -e "  ${YELLOW}⚠  Save this password now - it will not be shown again.${NC}"
    echo -e "  ${YELLOW}   You will be required to change it on first login.${NC}"
else
    echo "  Password : (unchanged - this was an upgrade)"
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

