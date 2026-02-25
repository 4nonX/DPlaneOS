#!/usr/bin/env bash
# D-PlaneOS OTA Update Script — A/B Slot Switcher (Task 4.2)
# ─────────────────────────────────────────────────────────────────────────────
# Usage:
#   dplaneos-ota-update <update-bundle.tar.gz>
#   dplaneos-ota-update --verify-only <update-bundle.tar.gz>
#   dplaneos-ota-update --health-check          (run by systemd after reboot)
#   dplaneos-ota-update --revert                (manual rollback to last slot)
#
# Update bundle format:
#   update.tar.gz
#   ├── system.tar.gz       — NixOS system closure tarball
#   ├── system.tar.gz.sig   — Ed25519 signature over system.tar.gz
#   ├── metadata.json       — { version, slot_target, min_version, sha256 }
#   └── metadata.json.sig   — Ed25519 signature over metadata.json
#
# A/B flow:
#   1. Verify Ed25519 signature on both files (fail closed — no signature, no apply)
#   2. Detect active slot (A or B) from /proc/cmdline boot label
#   3. Mount inactive slot at /mnt/system-b (or /mnt/system-a)
#   4. Extract new system closure into inactive slot
#   5. Update systemd-boot entry to boot inactive slot next time
#   6. Write revert marker to /persist/ota/pending-revert
#   7. Reboot
#
# Auto-revert (runs 90s after boot via systemd timer):
#   - If /persist/ota/pending-revert exists AND health check passes → remove marker (commit)
#   - If /persist/ota/pending-revert exists AND health check fails → revert to old slot
# ─────────────────────────────────────────────────────────────────────────────

set -euo pipefail

# ── Constants ──────────────────────────────────────────────────────────────

PERSIST_OTA="/persist/ota"
REVERT_MARKER="${PERSIST_OTA}/pending-revert"
SLOT_MARKER="${PERSIST_OTA}/active-slot"
UPDATE_LOG="${PERSIST_OTA}/ota.log"

# Public key for update signature verification (Ed25519, base64-encoded).
# This key is baked in at build time. The corresponding private key is held
# exclusively by the D-PlaneOS release signing infrastructure.
#
# REPLACE THIS WITH YOUR ACTUAL PUBLIC KEY before shipping.
# Generate with: openssl genpkey -algorithm ed25519 | tee private.pem | \
#                openssl pkey -pubout | base64 -w0
OTA_PUBLIC_KEY="${OTA_PUBLIC_KEY:-REPLACE_WITH_BASE64_ED25519_PUBLIC_KEY}"

# Boot label used in systemd-boot entries for each slot
LABEL_A="D-PlaneOS (Slot A)"
LABEL_B="D-PlaneOS (Slot B)"

# Partition labels (must match disko.nix)
PART_LABEL_A="system-a"
PART_LABEL_B="system-b"

# ── Logging ────────────────────────────────────────────────────────────────

mkdir -p "${PERSIST_OTA}"

log() {
    local ts
    ts=$(date -u +"%Y-%m-%dT%H:%M:%SZ")
    echo "[${ts}] $*" | tee -a "${UPDATE_LOG}"
}

die() {
    log "ERROR: $*"
    exit 1
}

# ── Signature verification ─────────────────────────────────────────────────

verify_signature() {
    local file="$1"
    local sigfile="${file}.sig"

    log "Verifying signature on $(basename "${file}")..."

    if [ ! -f "${sigfile}" ]; then
        die "Signature file ${sigfile} not found — refusing to apply unsigned update"
    fi

    # Write public key to temp file for openssl
    local pubkey_file
    pubkey_file=$(mktemp /tmp/dplaneos-ota-pubkey-XXXXXX.pem)
    # shellcheck disable=SC2064
    trap "rm -f '${pubkey_file}'" RETURN

    # Decode base64 public key and wrap in PEM header
    {
        echo "-----BEGIN PUBLIC KEY-----"
        echo "${OTA_PUBLIC_KEY}" | fold -w 64
        echo "-----END PUBLIC KEY-----"
    } > "${pubkey_file}"

    if ! openssl pkeyutl \
            -verify \
            -pubin \
            -inkey "${pubkey_file}" \
            -sigfile "${sigfile}" \
            -in "${file}" \
            2>/dev/null; then
        die "Signature verification FAILED for $(basename "${file}"). Update rejected."
    fi

    log "Signature OK: $(basename "${file}")"
}

# ── Slot detection ─────────────────────────────────────────────────────────

detect_active_slot() {
    # Read the boot label from /proc/cmdline.
    # systemd-boot passes the loader entry label as systemd.machine_id or
    # more reliably as the PARTLABEL of the root device.
    local root_part
    root_part=$(findmnt -n -o SOURCE /)
    local root_label
    root_label=$(lsblk -no PARTLABEL "${root_part}" 2>/dev/null || echo "")

    case "${root_label}" in
        system-a) echo "a" ;;
        system-b) echo "b" ;;
        *)
            # Fallback: read from our own slot marker
            if [ -f "${SLOT_MARKER}" ]; then
                cat "${SLOT_MARKER}"
            else
                # Assume A if we cannot determine (first boot after install)
                echo "a"
            fi
            ;;
    esac
}

inactive_slot_of() {
    case "$1" in
        a) echo "b" ;;
        b) echo "a" ;;
        *) die "Unknown slot: $1" ;;
    esac
}

inactive_part_label_of() {
    case "$1" in
        a) echo "${PART_LABEL_B}" ;;
        b) echo "${PART_LABEL_A}" ;;
    esac
}

inactive_boot_label_of() {
    case "$1" in
        a) echo "${LABEL_B}" ;;
        b) echo "${LABEL_A}" ;;
    esac
}

# ── Boot entry management ──────────────────────────────────────────────────

# Find the systemd-boot entry .conf file for a given slot label.
find_boot_entry() {
    local label="$1"
    grep -rl "title ${label}" /boot/loader/entries/ 2>/dev/null | head -1
}

# Set the default boot entry to boot a specific slot next time.
# This uses bootctl to write /boot/loader/loader.conf default=<entry>.
set_default_boot_entry() {
    local target_label="$1"
    local entry_file
    entry_file=$(find_boot_entry "${target_label}")

    if [ -z "${entry_file}" ]; then
        die "No boot entry found for label '${target_label}'. Is this a systemd-boot system?"
    fi

    local entry_name
    entry_name=$(basename "${entry_file}")
    log "Setting default boot entry to: ${entry_name} (${target_label})"

    bootctl set-default "${entry_name}"
}

# ── Update application ────────────────────────────────────────────────────

cmd_apply() {
    local bundle="$1"

    [ -f "${bundle}" ] || die "Bundle not found: ${bundle}"

    log "=== D-PlaneOS OTA Update Starting ==="
    log "Bundle: ${bundle}"

    # 1. Extract bundle to temp dir
    local workdir
    workdir=$(mktemp -d /tmp/dplaneos-ota-XXXXXX)
    # shellcheck disable=SC2064
    trap "rm -rf '${workdir}'" EXIT

    log "Extracting bundle..."
    tar -xzf "${bundle}" -C "${workdir}"

    local system_tar="${workdir}/system.tar.gz"
    local metadata_json="${workdir}/metadata.json"

    [ -f "${system_tar}" ]    || die "Bundle missing system.tar.gz"
    [ -f "${metadata_json}" ] || die "Bundle missing metadata.json"

    # 2. Verify signatures — fail closed
    verify_signature "${metadata_json}"
    verify_signature "${system_tar}"

    # 3. Parse metadata
    local new_version
    new_version=$(python3 -c "import json,sys; d=json.load(open('${metadata_json}')); print(d['version'])")
    log "Update version: ${new_version}"

    # 4. Detect active/inactive slots
    local active_slot
    active_slot=$(detect_active_slot)
    local inactive_slot
    inactive_slot=$(inactive_slot_of "${active_slot}")
    local inactive_part_label
    inactive_part_label=$(inactive_part_label_of "${active_slot}")

    log "Active slot: ${active_slot} | Target slot: ${inactive_slot}"

    # 5. Resolve inactive slot device by PARTLABEL
    local inactive_dev
    inactive_dev=$(blkid -L "${inactive_part_label}" 2>/dev/null || \
                   lsblk -no PATH $(lsblk -no PATH,PARTLABEL | awk -v lbl="${inactive_part_label}" '$2==lbl{print $1}' | head -1) 2>/dev/null || \
                   echo "")
    [ -n "${inactive_dev}" ] || die "Cannot find device for partition label '${inactive_part_label}'"
    log "Writing to: ${inactive_dev} (${inactive_part_label})"

    # 6. Mount inactive slot
    local mnt="/mnt/system-${inactive_slot}"
    mkdir -p "${mnt}"
    if mountpoint -q "${mnt}"; then
        log "Unmounting stale mount at ${mnt}"
        umount "${mnt}"
    fi
    mount "${inactive_dev}" "${mnt}"
    # shellcheck disable=SC2064
    trap "umount '${mnt}' 2>/dev/null || true; rm -rf '${workdir}'" EXIT

    # 7. Wipe inactive slot and extract new system
    log "Wiping inactive slot..."
    find "${mnt}" -mindepth 1 -maxdepth 1 ! -name 'lost+found' -exec rm -rf {} +

    log "Extracting new system closure to ${mnt}..."
    tar -xzf "${system_tar}" -C "${mnt}"

    # 8. Update boot entry to target inactive slot
    local target_boot_label
    target_boot_label=$(inactive_boot_label_of "${active_slot}")
    set_default_boot_entry "${target_boot_label}"

    # 9. Write revert marker BEFORE rebooting
    # This tells the health check service what to revert to if the new system fails.
    mkdir -p "${PERSIST_OTA}"
    cat > "${REVERT_MARKER}" <<EOF
revert_to_slot=${active_slot}
updated_at=$(date -u +"%Y-%m-%dT%H:%M:%SZ")
new_version=${new_version}
EOF
    echo "${inactive_slot}" > "${SLOT_MARKER}"

    log "Revert marker written. Will auto-revert to slot '${active_slot}' if health check fails."
    log "=== Update applied. Rebooting in 5 seconds... ==="
    sleep 5
    systemctl reboot
}

# ── Health check (runs after reboot, via systemd timer) ────────────────────

cmd_health_check() {
    log "=== OTA Health Check ==="

    if [ ! -f "${REVERT_MARKER}" ]; then
        log "No pending revert — system is stable. Nothing to do."
        exit 0
    fi

    log "Pending revert marker found. Running health checks..."

    local checks_passed=0
    local checks_failed=0

    # Check 1: daemon is up and responding
    if curl -sf --max-time 10 http://127.0.0.1:9000/api/system/info >/dev/null 2>&1; then
        log "PASS: daemon API is responding"
        checks_passed=$((checks_passed + 1))
    else
        log "FAIL: daemon API not responding on port 9000"
        checks_failed=$((checks_failed + 1))
    fi

    # Check 2: ZFS pools are ONLINE
    local degraded_pools
    degraded_pools=$(zpool list -H -o name,health 2>/dev/null | awk '$2 != "ONLINE" {print $1}' || echo "")
    if [ -z "${degraded_pools}" ]; then
        log "PASS: all ZFS pools ONLINE"
        checks_passed=$((checks_passed + 1))
    else
        log "FAIL: degraded/faulted pools: ${degraded_pools}"
        checks_failed=$((checks_failed + 1))
    fi

    # Check 3: /persist is mounted
    if mountpoint -q /persist; then
        log "PASS: /persist is mounted"
        checks_passed=$((checks_passed + 1))
    else
        log "FAIL: /persist is NOT mounted — state will be lost on next reboot"
        checks_failed=$((checks_failed + 1))
    fi

    # Check 4: Samba is running (if smb_shares exist in DB)
    local share_count
    share_count=$(sqlite3 /var/lib/dplaneos/dplaneos.db \
        "SELECT COUNT(*) FROM smb_shares WHERE enabled=1" 2>/dev/null || echo "0")
    if [ "${share_count}" -gt 0 ]; then
        if systemctl is-active --quiet smbd; then
            log "PASS: smbd running (${share_count} active shares)"
            checks_passed=$((checks_passed + 1))
        else
            log "FAIL: smbd not running but ${share_count} active shares configured"
            checks_failed=$((checks_failed + 1))
        fi
    else
        log "SKIP: no active shares configured — skipping smbd check"
    fi

    log "Health check result: ${checks_passed} passed, ${checks_failed} failed"

    if [ "${checks_failed}" -gt 0 ]; then
        log "=== HEALTH CHECK FAILED — initiating auto-revert ==="
        cmd_revert
    else
        # All checks passed — commit the update
        log "=== Health check PASSED — committing update ==="
        local revert_to
        revert_to=$(grep revert_to_slot "${REVERT_MARKER}" | cut -d= -f2)
        rm -f "${REVERT_MARKER}"
        log "Update committed. Previous slot (${revert_to}) remains available for manual revert."
    fi
}

# ── Revert ─────────────────────────────────────────────────────────────────

cmd_revert() {
    local revert_to

    if [ -f "${REVERT_MARKER}" ]; then
        revert_to=$(grep revert_to_slot "${REVERT_MARKER}" | cut -d= -f2)
    else
        # Manual revert: swap to whichever slot we're NOT on
        local active_slot
        active_slot=$(detect_active_slot)
        revert_to=$(inactive_slot_of "${active_slot}")
    fi

    log "Reverting to slot: ${revert_to}"

    case "${revert_to}" in
        a) set_default_boot_entry "${LABEL_A}" ;;
        b) set_default_boot_entry "${LABEL_B}" ;;
        *) die "Invalid revert target slot: ${revert_to}" ;;
    esac

    echo "${revert_to}" > "${SLOT_MARKER}"
    rm -f "${REVERT_MARKER}"

    log "Revert boot entry set. Rebooting in 5 seconds..."
    sleep 5
    systemctl reboot
}

# ── Verify-only mode ───────────────────────────────────────────────────────

cmd_verify_only() {
    local bundle="$1"
    [ -f "${bundle}" ] || die "Bundle not found: ${bundle}"

    local workdir
    workdir=$(mktemp -d /tmp/dplaneos-ota-verify-XXXXXX)
    trap "rm -rf '${workdir}'" EXIT

    tar -xzf "${bundle}" -C "${workdir}"
    verify_signature "${workdir}/metadata.json"
    verify_signature "${workdir}/system.tar.gz"

    log "Bundle verified successfully. Signatures are valid."
    python3 -c "
import json, sys
d = json.load(open('${workdir}/metadata.json'))
print(f'  Version:     {d.get(\"version\", \"unknown\")}')
print(f'  Min version: {d.get(\"min_version\", \"any\")}')
"
}

# ── Entrypoint ─────────────────────────────────────────────────────────────

case "${1:-}" in
    --verify-only)
        cmd_verify_only "${2:?Usage: $0 --verify-only <bundle>}"
        ;;
    --health-check)
        cmd_health_check
        ;;
    --revert)
        cmd_revert
        ;;
    --help|-h)
        grep "^# " "$0" | head -20 | sed 's/^# //'
        ;;
    "")
        die "No command given. Usage: $0 <bundle.tar.gz> | --verify-only | --health-check | --revert"
        ;;
    *)
        cmd_apply "$1"
        ;;
esac
