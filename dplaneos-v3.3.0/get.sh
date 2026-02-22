#!/bin/bash
#
# D-PlaneOS — Bootstrap Installer
#
# This is the ONE-LINER entry point:
#
#   curl -fsSL https://get.dplaneos.io | sudo bash
#
# Or with options:
#
#   curl -fsSL https://get.dplaneos.io | sudo bash -s -- --port 8080 --unattended
#   curl -fsSL https://get.dplaneos.io | sudo bash -s -- --upgrade
#
# What this script does:
#   1. Detects your OS and architecture
#   2. Downloads the correct D-PlaneOS release tarball
#   3. Extracts it and hands off to install.sh
#
# Supports: Debian 11+, Ubuntu 20.04+, Raspberry Pi OS (arm64/armhf)
#

set -euo pipefail

VERSION="$(cat "$(dirname "$0")/VERSION" 2>/dev/null | tr -d '[:space:]')"
[ -z "$VERSION" ] && { echo "ERROR: Could not read VERSION file" >&2; exit 1; }
RELEASE_BASE="https://github.com/4nonX/dplaneos/releases/download/v${VERSION}"
# Fallback mirror if GitHub is unreachable
MIRROR_BASE="https://releases.dplaneos.io/v${VERSION}"

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
BOLD='\033[1m'
NC='\033[0m'

# Pass-through all arguments to install.sh
INSTALL_ARGS=("$@")

banner() {
    echo ""
    echo -e "${BOLD}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
    echo -e "${BOLD}    D-PlaneOS v${VERSION} — Bootstrap${NC}"
    echo -e "${BOLD}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
    echo ""
}

die() { echo -e "${RED}✗ $1${NC}" >&2; exit 1; }
info() { echo -e "${BLUE}ℹ${NC} $1"; }
ok() { echo -e "${GREEN}✓${NC} $1"; }
warn() { echo -e "${YELLOW}⚠${NC} $1"; }

# ── Root check ────────────────────────────────────────────────────────────────
[ "$EUID" -eq 0 ] || die "Run with sudo: curl -fsSL https://get.dplaneos.io | sudo bash"

banner

# ── OS detection ──────────────────────────────────────────────────────────────
info "Detecting system..."

if [ ! -f /etc/os-release ]; then
    die "Cannot detect OS. Supported: Debian 11+, Ubuntu 20.04+, Raspberry Pi OS"
fi

. /etc/os-release

case "${ID,,}" in
    debian|ubuntu|raspbian|linuxmint|pop)
        PKG_MANAGER="apt"
        ok "OS: $PRETTY_NAME"
        ;;
    *)
        die "Unsupported OS: $PRETTY_NAME. Supported: Debian, Ubuntu, Raspberry Pi OS."
        ;;
esac

# ── Architecture detection ────────────────────────────────────────────────────
ARCH=$(uname -m)
case "$ARCH" in
    x86_64)             ARCH_TAG="linux-amd64"  ;;
    aarch64|arm64)      ARCH_TAG="linux-arm64"  ;;
    armv7l|armhf)       ARCH_TAG="linux-armhf"  ;;
    *)                  die "Unsupported architecture: $ARCH. Supported: x86_64, aarch64, armv7l" ;;
esac
ok "Architecture: $ARCH ($ARCH_TAG)"

# ── Dependency check: curl or wget ───────────────────────────────────────────
if command -v curl &>/dev/null; then
    DOWNLOADER="curl"
elif command -v wget &>/dev/null; then
    DOWNLOADER="wget"
else
    info "Installing curl..."
    apt-get update -qq && apt-get install -y -qq curl
    DOWNLOADER="curl"
fi

# ── Download release ──────────────────────────────────────────────────────────
TARBALL="dplaneos-v${VERSION}-${ARCH_TAG}.tar.gz"
WORK_DIR=$(mktemp -d /tmp/dplaneos-install-XXXXXX)
trap 'rm -rf "$WORK_DIR"' EXIT

info "Downloading D-PlaneOS v${VERSION} (${ARCH_TAG})..."

download_file() {
    local url="$1"
    local dest="$2"
    if [ "$DOWNLOADER" = "curl" ]; then
        curl -fsSL --progress-bar "$url" -o "$dest"
    else
        wget -q --show-progress "$url" -O "$dest"
    fi
}

# Try primary, fall back to mirror
if ! download_file "${RELEASE_BASE}/${TARBALL}" "${WORK_DIR}/${TARBALL}" 2>/dev/null; then
    warn "Primary download failed, trying mirror..."
    download_file "${MIRROR_BASE}/${TARBALL}" "${WORK_DIR}/${TARBALL}" \
        || die "Download failed from both primary and mirror. Check your internet connection."
fi

ok "Downloaded ${TARBALL}"

# ── Verify checksum if available ──────────────────────────────────────────────
CHECKSUM_URL="${RELEASE_BASE}/${TARBALL}.sha256"
CHECKSUM_FILE="${WORK_DIR}/${TARBALL}.sha256"

if download_file "$CHECKSUM_URL" "$CHECKSUM_FILE" 2>/dev/null; then
    info "Verifying checksum..."
    (cd "$WORK_DIR" && sha256sum -c "$CHECKSUM_FILE" --quiet) \
        && ok "Checksum verified" \
        || die "Checksum mismatch — download may be corrupted. Try again."
else
    warn "No checksum file available — skipping verification"
fi

# ── Extract ───────────────────────────────────────────────────────────────────
info "Extracting..."
tar xzf "${WORK_DIR}/${TARBALL}" -C "$WORK_DIR"

# Find the extracted directory (handles both flat and nested tarballs)
EXTRACT_DIR=$(find "$WORK_DIR" -maxdepth 2 -name "install.sh" -exec dirname {} \; | head -1)
[ -n "$EXTRACT_DIR" ] || die "install.sh not found in tarball — release may be corrupt"

ok "Extracted to $EXTRACT_DIR"

# ── Hand off to install.sh ────────────────────────────────────────────────────
echo ""
echo -e "${BOLD}Starting installation...${NC}"
echo ""

exec bash "${EXTRACT_DIR}/install.sh" "${INSTALL_ARGS[@]}"
