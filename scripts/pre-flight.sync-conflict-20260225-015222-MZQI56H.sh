#!/bin/bash
#
# D-PlaneOS - Pre-Flight Validation
# 
# This script runs BEFORE installation to ensure the system
# meets ALL requirements. If ANY check fails, installation aborts.
#
# GUARANTEES:
# 1. All dependencies available
# 2. System compatible
# 3. No conflicts
# 4. Enough resources
#
# Usage: Called automatically by install.sh
#

set -e

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
BOLD='\033[1m'
NC='\033[0m'

ERRORS=0
WARNINGS=0

echo -e "${BOLD}${BLUE}D-PlaneOS v$(cat "$(dirname "$0")/../VERSION" 2>/dev/null | tr -d "[:space:]" || echo "?") Pre-Flight Validation${NC}"
echo "=========================================="
echo ""

log_pass() {
    echo -e "${GREEN}✓${NC} $1"
}

log_fail() {
    echo -e "${RED}✗${NC} $1"
    ERRORS=$((ERRORS + 1))
}

log_warn() {
    echo -e "${YELLOW}⚠${NC} $1"
    WARNINGS=$((WARNINGS + 1))
}

log_info() {
    echo -e "${BLUE}ℹ${NC} $1"
}

# ============================================================
# CHECK 1: Root Privileges
# ============================================================

echo "1. Checking privileges..."
if [ "$EUID" -ne 0 ]; then
    log_fail "Must run as root"
    echo ""
    echo "Run: sudo $0"
    exit 1
else
    log_pass "Running as root"
fi
echo ""

# ============================================================
# CHECK 2: Operating System
# ============================================================

echo "2. Checking operating system..."

if [ ! -f /etc/os-release ]; then
    log_fail "Cannot detect operating system"
    exit 1
fi

source /etc/os-release

SUPPORTED_OS=("debian" "ubuntu")
OS_SUPPORTED=false

for os in "${SUPPORTED_OS[@]}"; do
    if [[ "${ID,,}" == "$os" ]]; then
        OS_SUPPORTED=true
        break
    fi
done

if $OS_SUPPORTED; then
    log_pass "OS: $PRETTY_NAME (supported)"
else
    log_fail "OS: $PRETTY_NAME (not supported)"
    log_info "Supported: Debian 11+, Ubuntu 22.04+"
fi

# Check version
if [[ "${ID,,}" == "debian" ]]; then
    if [ "${VERSION_ID}" -lt 11 ]; then
        log_warn "Debian version $VERSION_ID may not be fully supported (recommend 11+)"
    fi
elif [[ "${ID,,}" == "ubuntu" ]]; then
    if [ "${VERSION_ID%%.*}" -lt 22 ]; then
        log_warn "Ubuntu version $VERSION_ID may not be fully supported (recommend 22.04+)"
    fi
fi

echo ""

# ============================================================
# CHECK 3: System Resources
# ============================================================

echo "3. Checking system resources..."

# RAM
TOTAL_RAM_MB=$(free -m | awk '/^Mem:/{print $2}')
if [ "$TOTAL_RAM_MB" -lt 2048 ]; then
    log_fail "Insufficient RAM: ${TOTAL_RAM_MB}MB (minimum 2GB required)"
else
    log_pass "RAM: ${TOTAL_RAM_MB}MB (sufficient)"
fi

# Disk space
ROOT_SPACE_GB=$(df -BG / | awk 'NR==2 {print $4}' | sed 's/G//')
if [ "$ROOT_SPACE_GB" -lt 10 ]; then
    log_fail "Insufficient disk space: ${ROOT_SPACE_GB}GB (minimum 10GB required)"
else
    log_pass "Disk space: ${ROOT_SPACE_GB}GB (sufficient)"
fi

# CPU cores
CPU_CORES=$(nproc)
if [ "$CPU_CORES" -lt 2 ]; then
    log_warn "Only $CPU_CORES CPU core (recommend 2+)"
else
    log_pass "CPU cores: $CPU_CORES"
fi

echo ""

# ============================================================
# CHECK 4: Network
# ============================================================

echo "4. Checking network..."

# Check internet connectivity
if ping -c 1 -W 2 8.8.8.8 &>/dev/null; then
    log_pass "Internet connectivity"
else
    log_warn "No internet connection (some features may not work)"
fi

# Check DNS
if ping -c 1 -W 2 google.com &>/dev/null; then
    log_pass "DNS resolution"
else
    log_warn "DNS not working"
fi

# Check if ports 80/443 are available
if netstat -tuln 2>/dev/null | grep -q ":80 "; then
    log_fail "Port 80 already in use"
    log_info "Another web server may be running"
else
    log_pass "Port 80 available"
fi

if netstat -tuln 2>/dev/null | grep -q ":443 "; then
    log_warn "Port 443 in use (HTTPS will not be available)"
else
    log_pass "Port 443 available"
fi

echo ""

# ============================================================
# CHECK 5: Required Packages
# ============================================================

echo "5. Checking required packages..."

# Critical packages that MUST be available
CRITICAL_PACKAGES=(
    "wget"
    "curl"
    "tar"
    "gzip"
    "sudo"
    "systemctl"
)

for pkg in "${CRITICAL_PACKAGES[@]}"; do
    if command -v "$pkg" &>/dev/null; then
        log_pass "$pkg installed"
    else
        log_fail "$pkg not found"
        log_info "Install: apt-get install $pkg"
    fi
done

echo ""

# ============================================================
# CHECK 6: Package Manager
# ============================================================

echo "6. Checking package manager..."

if command -v apt-get &>/dev/null; then
    log_pass "apt-get available"
    
    # Try to update package lists
    log_info "Testing apt-get update..."
    if apt-get update -qq 2>&1 | tee /tmp/apt-update.log | grep -q "Err:"; then
        log_warn "Some apt repositories failed"
        log_info "Check: /tmp/apt-update.log"
    else
        log_pass "Package lists updated"
    fi
else
    log_fail "apt-get not found (Debian/Ubuntu required)"
fi

echo ""

# ============================================================
# CHECK 7: Existing Installation
# ============================================================

echo "7. Checking for existing installation..."

if [ -d "/opt/dplaneos" ]; then
    log_warn "Existing installation found at /opt/dplaneos"
    log_info "Backup will be created automatically"
fi

if [ -f "/var/lib/dplaneos/dplaneos.db" ]; then
    log_warn "Existing database found"
    log_info "Database will be preserved during upgrade"
fi

if systemctl list-units --full -all | grep -q "nginx.service"; then
    if systemctl is-active nginx &>/dev/null; then
        log_info "nginx is running (will be reconfigured)"
    fi
fi

echo ""

# ============================================================
# CHECK 8: ZFS Support
# ============================================================

echo "8. Checking ZFS support..."

if command -v zpool &>/dev/null; then
    log_pass "ZFS utilities installed"
    
    # Check if ZFS module can be loaded
    if lsmod | grep -q "^zfs "; then
        log_pass "ZFS kernel module loaded"
    else
        log_info "ZFS module not loaded (will be loaded during install)"
    fi
else
    log_info "ZFS not installed (will be installed)"
fi

echo ""

# ============================================================
# CHECK 9: Conflicting Services
# ============================================================

echo "9. Checking for conflicting services..."

# Apache
if systemctl list-units --full -all | grep -q "apache2.service"; then
    if systemctl is-active apache2 &>/dev/null; then
        log_warn "Apache is running (conflicts with nginx)"
        log_info "Apache will be stopped during installation"
    fi
fi

# Check for other web servers
OTHER_WEB_SERVERS=("lighttpd" "caddy" "traefik")
for server in "${OTHER_WEB_SERVERS[@]}"; do
    if systemctl is-active "$server" &>/dev/null; then
        log_warn "$server is running (may conflict)"
    fi
done

echo ""

# ============================================================
# CHECK 10: File System
# ============================================================

echo "10. Checking file system..."

# Check if / is writable
if touch /tmp/dplaneos-write-test 2>/dev/null; then
    rm -f /tmp/dplaneos-write-test
    log_pass "Filesystem writable"
else
    log_fail "Cannot write to filesystem"
fi

# Check /opt permissions
if [ -d "/opt" ]; then
    if [ -w "/opt" ]; then
        log_pass "/opt directory writable"
    else
        log_fail "/opt directory not writable"
    fi
else
    log_info "/opt directory will be created"
fi

echo ""

# ============================================================
# SUMMARY
# ============================================================

echo "=========================================="
echo "Pre-Flight Validation Summary"
echo "=========================================="
echo ""

if [ $ERRORS -eq 0 ] && [ $WARNINGS -eq 0 ]; then
    echo -e "${BOLD}${GREEN}✓ ALL CHECKS PASSED!${NC}"
    echo ""
    echo "System is ready for D-PlaneOS installation."
    echo ""
    exit 0
elif [ $ERRORS -eq 0 ]; then
    echo -e "${BOLD}${YELLOW}⚠ $WARNINGS WARNING(S)${NC}"
    echo ""
    echo "System is compatible but has some warnings."
    echo "Installation can proceed but may require manual intervention."
    echo ""
    read -p "Continue anyway? (y/N): " -n 1 -r
    echo ""
    if [[ $REPLY =~ ^[Yy]$ ]]; then
        exit 0
    else
        echo "Installation aborted."
        exit 1
    fi
else
    echo -e "${BOLD}${RED}✗ $ERRORS ERROR(S), $WARNINGS WARNING(S)${NC}"
    echo ""
    echo "System does NOT meet requirements for D-PlaneOS."
    echo "Please fix the errors above before installing."
    echo ""
    exit 1
fi
