#!/usr/bin/env bash
# DPlaneOS system updates
# ─────────────────────────────────────────────────────────────────────────────
# An update is a new generation of the system, installed next to the running
# one and booted once on trial:
#
#   install   check the update file's signature, fetch the new system into
#             the Nix store (its own parts from the update file, everything
#             else from the public NixOS cache), add a boot entry for it and
#             tell the boot loader to start it ONCE. The default boot entry
#             stays the running system.
#   (reboot)  the new system starts. If it does not come up at all, the next
#             start (power cycle, watchdog) is the old system again.
#   confirm   run a few minutes after every boot. With an update on trial:
#             everything that worked before must work again (the daemon
#             answers, the services that ran are running, the pools that
#             were online are online). Then the new system becomes the
#             default; otherwise the old one is restored and started.
#   revert    go back to the previous generation by hand.
#   status    what is installed, what is on trial, how the last update went.
#
# Usage:
#   dplaneos-update verify  <file.update>
#   dplaneos-update install <file.update>
#   dplaneos-update confirm
#   dplaneos-update revert
#   dplaneos-update status
#
# An update file (dplaneos-v<version>-<arch>.update, one per release) is a
# tar archive of three members:
#   update.tar             metadata.json and cache/, a Nix binary cache with
#                          the parts of the system that DPlaneOS builds itself
#   update.tar.sha256      its checksum
#   update.tar.sha256.sig  Ed25519 signature of the checksum file, made with
#                          the release key. The public key is part of the
#                          system: /etc/dplaneos/update-signing.pub
# ─────────────────────────────────────────────────────────────────────────────

set -euo pipefail

STATE_DIR="${DPLANEOS_UPDATE_STATE:-/var/lib/dplaneos/updates}"
PENDING="${STATE_DIR}/pending"          # an update is on trial
RESULT="${STATE_DIR}/last-result.json"  # how the last update went
LOG="${STATE_DIR}/update.log"
PUBKEY="${DPLANEOS_UPDATE_PUBKEY:-/etc/dplaneos/update-signing.pub}"
PROFILE=/nix/var/nix/profiles/system
UPSTREAM_CACHE="${DPLANEOS_UPDATE_UPSTREAM:-https://cache.nixos.org/}"
SOCKET=/run/dplaneos/dplaned.sock
# How long "confirm" waits for the system to become healthy.
HEALTH_TIMEOUT="${DPLANEOS_UPDATE_HEALTH_TIMEOUT:-300}"
# Free space on the system disk an update needs.
MIN_FREE_MB="${DPLANEOS_UPDATE_MIN_FREE_MB:-3000}"
# Units whose state before the update is compared with their state after it.
WATCHED_UNITS="dplaned nginx postgresql samba-smbd nfs-server docker sshd dplaneos-vsftpd dplaneos-upsmon"

mkdir -p "${STATE_DIR}"

log() {
    local line
    line="[$(date -u +"%Y-%m-%dT%H:%M:%SZ")] $*"
    echo "${line}" >&2
    echo "${line}" >> "${LOG}" 2>/dev/null || true
}

die() {
    log "ERROR: $*"
    exit 1
}

json_string() {
    # Encode $1 as a JSON string.
    python3 -c 'import json,sys; print(json.dumps(sys.argv[1]))' "$1"
}

write_result() {
    # $1 outcome (installed|reverted|failed)  $2 version  $3 message
    local tmp="${RESULT}.tmp"
    printf '{"outcome":%s,"version":%s,"message":%s,"at":%s}\n' \
        "$(json_string "$1")" "$(json_string "$2")" "$(json_string "$3")" \
        "$(json_string "$(date -u +"%Y-%m-%dT%H:%M:%SZ")")" > "${tmp}"
    mv "${tmp}" "${RESULT}"
}

version_of() {
    # The DPlaneOS version of a system closure.
    cat "$1/dplaneos-version" 2>/dev/null || echo unknown
}

generation_of() {
    # The generation number a profile link ("system-12-link") stands for.
    local link
    link=$(basename "$(readlink "$1")")
    link=${link#system-}
    echo "${link%-link}"
}

# ── Signature ────────────────────────────────────────────────────────────────

unpack_and_verify() {
    # $1 update file, $2 directory to unpack into. Leaves $2/update.tar,
    # checked against the release key, or exits.
    local file="$1" dir="$2"
    [ -f "${file}" ] || die "update file not found: ${file}"
    [ -s "${PUBKEY}" ] || die "this system has no update signing key (${PUBKEY}): updates cannot be verified"
    rm -rf "${dir}"
    mkdir -p "${dir}"
    # Only the three expected members, as plain files: nothing in the file
    # is trusted before its signature is checked.
    if ! tar -xf "${file}" -C "${dir}" --no-same-owner --no-same-permissions \
            update.tar update.tar.sha256 update.tar.sha256.sig 2>>"${LOG}"; then
        rm -rf "${dir}"
        die "$(basename "${file}") is not a DPlaneOS update file"
    fi
    local m
    for m in update.tar update.tar.sha256 update.tar.sha256.sig; do
        if [ -L "${dir}/${m}" ] || [ ! -f "${dir}/${m}" ]; then
            rm -rf "${dir}"
            die "$(basename "${file}") is not a DPlaneOS update file"
        fi
    done

    # The signature covers the checksum file; the checksum covers the update.
    if ! openssl pkeyutl -verify -pubin -inkey "${PUBKEY}" -rawin \
            -in "${dir}/update.tar.sha256" -sigfile "${dir}/update.tar.sha256.sig" >/dev/null 2>&1; then
        rm -rf "${dir}"
        die "the signature of $(basename "${file}") is not valid: the file does not come from the DPlaneOS release process, or was changed"
    fi
    local want have
    want=$(awk '{print $1; exit}' "${dir}/update.tar.sha256")
    have=$(sha256sum "${dir}/update.tar" | awk '{print $1}')
    if [ -z "${want}" ] || [ "${want}" != "${have}" ]; then
        rm -rf "${dir}"
        die "$(basename "${file}") is damaged (checksum mismatch)"
    fi
    log "signature and checksum of $(basename "${file}") are valid"
}

metadata_value() {
    # $1 directory with metadata.json, $2 key
    python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))[sys.argv[2]])' "$1/metadata.json" "$2"
}

cmd_verify() {
    local dir="${STATE_DIR}/verify"
    unpack_and_verify "$1" "${dir}"
    tar -xf "${dir}/update.tar" -C "${dir}" metadata.json
    # What the file is, for whoever asked.
    python3 -c 'import json,sys; print(json.dumps(json.load(open(sys.argv[1]))))' "${dir}/metadata.json"
    rm -rf "${dir}"
}

# ── Install ──────────────────────────────────────────────────────────────────

cmd_install() {
    local file="$1"
    [ ! -e "${PENDING}" ] || die "an update is already on trial; restart the system (or run: dplaneos-update revert)"

    local incoming="${STATE_DIR}/incoming" work="${STATE_DIR}/unpacked"
    unpack_and_verify "${file}" "${incoming}"
    rm -rf "${work}"
    mkdir -p "${work}"
    tar -xf "${incoming}/update.tar" -C "${work}" --no-same-owner
    rm -rf "${incoming}"
    [ -f "${work}/metadata.json" ] || die "update file has no metadata.json"
    [ -d "${work}/cache" ] || die "update file has no cache directory"

    local version system arch
    local version system arch
    version=$(metadata_value "${work}" version)
    system=$(metadata_value "${work}" system)
    arch=$(metadata_value "${work}" arch)
    case "${system}" in
        /nix/store/*-nixos-system-*) ;;
        *) die "update file names an unexpected system path: ${system}" ;;
    esac
    [ "${arch}" = "$(uname -m)" ] || die "this update is for ${arch}, this machine is $(uname -m)"

    local current
    current=$(readlink -f /run/current-system)
    [ "${system}" != "${current}" ] || die "version ${version} is what is running already"

    # Room for the new system (its own parts are in the file; the rest is
    # downloaded): refuse early rather than fill the disk half way.
    local free_kb
    free_kb=$(df -Pk /nix/store | awk 'NR==2 {print $4}')
    [ "${free_kb}" -ge $((MIN_FREE_MB * 1024)) ]         || die "only $((free_kb / 1024)) MB free on the system disk; an update needs ${MIN_FREE_MB} MB"

    log "fetching DPlaneOS ${version} (${system})"
    if ! nix-store --realise "${system}" \
            --option substituters "file://${work}/cache?trusted=true ${UPSTREAM_CACHE}" \
            --option require-sigs true \
            --option fallback false \
            --option builders "" \
            --option max-jobs 0 >> "${LOG}" 2>&1; then
        rm -rf "${work}"
        die "the new system could not be fetched completely (see ${LOG}); nothing was changed. Is ${UPSTREAM_CACHE} reachable?"
    fi
    rm -rf "${work}"
    [ -x "${system}/bin/switch-to-configuration" ] || die "${system} is not a system"

    local old_system old_gen new_gen
    old_system=$(readlink -f "${PROFILE}")
    old_gen=$(generation_of "${PROFILE}")

    # What works now must work after the update.
    local units="" pools=""
    for u in ${WATCHED_UNITS}; do
        if systemctl is-active --quiet "${u}.service"; then units="${units} ${u}"; fi
    done
    pools=$(zpool list -H -o name,health 2>/dev/null | awk '$2 == "ONLINE" {printf "%s ", $1}' || true)

    log "installing boot entry for ${version}"
    nix-env -p "${PROFILE}" --set "${system}"
    new_gen=$(generation_of "${PROFILE}")
    if ! "${system}/bin/switch-to-configuration" boot >> "${LOG}" 2>&1; then
        nix-env -p "${PROFILE}" --switch-generation "${old_gen}" || true
        "${old_system}/bin/switch-to-configuration" boot >> "${LOG}" 2>&1 || true
        die "the boot entry for the new system could not be written; nothing was changed"
    fi

    # Trial boot: the running system stays the default, the new one is
    # started once.
    if ! bootctl set-default "nixos-generation-${old_gen}.conf" >> "${LOG}" 2>&1 \
       || ! bootctl set-oneshot "nixos-generation-${new_gen}.conf" >> "${LOG}" 2>&1; then
        bootctl set-default "" >> "${LOG}" 2>&1 || true
        bootctl set-oneshot "" >> "${LOG}" 2>&1 || true
        nix-env -p "${PROFILE}" --switch-generation "${old_gen}" || true
        "${old_system}/bin/switch-to-configuration" boot >> "${LOG}" 2>&1 || true
        die "the boot loader could not be told to start the new system once; nothing was changed"
    fi

    local tmp="${PENDING}.tmp"
    {
        echo "version=${version}"
        echo "new_system=${system}"
        echo "new_gen=${new_gen}"
        echo "old_system=${old_system}"
        echo "old_gen=${old_gen}"
        echo "old_version=$(version_of "${old_system}")"
        echo "units=${units# }"
        echo "pools=${pools% }"
        echo "persist=$(grep -q ' /persist ' /proc/mounts && echo 1 || echo 0)"
        echo "installed_at=$(date -u +"%Y-%m-%dT%H:%M:%SZ")"
    } > "${tmp}"
    mv "${tmp}" "${PENDING}"
    sync
    log "DPlaneOS ${version} is installed and will be started at the next restart"
}

pending_value() {
    sed -n "s/^$1=//p" "${PENDING}" | head -n 1
}

# ── Going back ───────────────────────────────────────────────────────────────

restore_old() {
    # Make the system that ran before the update the installed one again.
    local old_gen old_system
    old_gen=$(pending_value old_gen)
    old_system=$(pending_value old_system)
    nix-env -p "${PROFILE}" --switch-generation "${old_gen}" >> "${LOG}" 2>&1 \
        || nix-env -p "${PROFILE}" --set "${old_system}" >> "${LOG}" 2>&1 || true
    # The generation that was on trial goes away, and its boot entry with it.
    local new_gen
    new_gen=$(pending_value new_gen)
    if [ -n "${new_gen}" ] && [ "${new_gen}" != "$(generation_of "${PROFILE}")" ]; then
        nix-env -p "${PROFILE}" --delete-generations "${new_gen}" >> "${LOG}" 2>&1 || true
    fi
    "${old_system}/bin/switch-to-configuration" boot >> "${LOG}" 2>&1 || true
    bootctl set-oneshot "" >> "${LOG}" 2>&1 || true
    bootctl set-default "" >> "${LOG}" 2>&1 || true
}

# ── Confirm (after every boot) ───────────────────────────────────────────────

health_problems() {
    # Prints one line per problem; nothing when the system is healthy.
    if ! curl -sf --max-time 10 --unix-socket "${SOCKET}" http://localhost/api/system/health >/dev/null 2>&1; then
        echo "the DPlaneOS service does not answer"
    fi
    local u
    for u in $(pending_value units); do
        if ! systemctl is-active --quiet "${u}.service"; then
            echo "${u} ran before the update and does not run now"
        fi
    done
    local p health
    for p in $(pending_value pools); do
        health=$(zpool list -H -o health "${p}" 2>/dev/null || echo "missing")
        if [ "${health}" != "ONLINE" ]; then
            echo "pool ${p} was online before the update and is ${health} now"
        fi
    done
    if [ "$(pending_value persist)" = 1 ] && ! grep -q ' /persist ' /proc/mounts; then
        echo "/persist was mounted before the update and is not mounted now"
    fi
}

cmd_confirm() {
    if [ ! -e "${PENDING}" ]; then
        exit 0
    fi
    local version new_system booted
    version=$(pending_value version)
    new_system=$(pending_value new_system)
    booted=$(readlink -f /run/booted-system)

    if [ "${booted}" != "${new_system}" ]; then
        # The trial start did not happen or did not get this far, and the
        # boot loader started the old system again.
        log "DPlaneOS ${version} did not start; staying on $(pending_value old_version)"
        restore_old
        write_result reverted "${version}" "The new version did not start. The system is running $(pending_value old_version) as before."
        rm -f "${PENDING}"
        exit 0
    fi

    log "DPlaneOS ${version} is running on trial; checking it"
    local deadline=$(( $(date +%s) + HEALTH_TIMEOUT )) problems=""
    while :; do
        problems=$(health_problems)
        [ -n "${problems}" ] || break
        [ "$(date +%s)" -lt "${deadline}" ] || break
        sleep 10
    done

    if [ -z "${problems}" ]; then
        bootctl set-default "" >> "${LOG}" 2>&1 || true
        write_result installed "${version}" "DPlaneOS ${version} is installed."
        rm -f "${PENDING}"
        log "DPlaneOS ${version} is healthy and now the installed version"
        # Keep the previous generation to go back to; drop older ones.
        nix-env -p "${PROFILE}" --delete-generations +2 >> "${LOG}" 2>&1 || true
        "${new_system}/bin/switch-to-configuration" boot >> "${LOG}" 2>&1 || true
        nix-store --gc >> "${LOG}" 2>&1 || true
        exit 0
    fi

    log "DPlaneOS ${version} is not healthy: ${problems//$'\n'/; }"
    restore_old
    write_result reverted "${version}" "The new version was removed again because: ${problems//$'\n'/; }. The system is back on $(pending_value old_version)."
    rm -f "${PENDING}"
    sync
    log "restarting into $(version_of "$(readlink -f "${PROFILE}")")"
    systemctl reboot
}

# ── Manual revert ────────────────────────────────────────────────────────────

cmd_revert() {
    if [ -e "${PENDING}" ]; then
        local version
        version=$(pending_value version)
        restore_old
        write_result reverted "${version}" "The update to ${version} was taken back by hand."
        rm -f "${PENDING}"
        sync
        if [ "$(readlink -f /run/booted-system)" != "$(readlink -f "${PROFILE}")" ]; then
            log "restarting into the previous version"
            systemctl reboot
        fi
        exit 0
    fi
    local current_gen prev_gen
    current_gen=$(generation_of "${PROFILE}")
    prev_gen=$(nix-env -p "${PROFILE}" --list-generations | awk -v cur="${current_gen}" '$1 < cur {p=$1} END {print p}')
    [ -n "${prev_gen}" ] || die "there is no previous version to go back to"
    local from to
    from=$(version_of "$(readlink -f "${PROFILE}")")
    nix-env -p "${PROFILE}" --switch-generation "${prev_gen}"
    to=$(version_of "$(readlink -f "${PROFILE}")")
    "$(readlink -f "${PROFILE}")/bin/switch-to-configuration" boot >> "${LOG}" 2>&1 \
        || die "the boot entry could not be written"
    bootctl set-oneshot "" >> "${LOG}" 2>&1 || true
    bootctl set-default "" >> "${LOG}" 2>&1 || true
    write_result reverted "${from}" "Went back from ${from} to ${to} by hand."
    sync
    log "restarting into ${to}"
    systemctl reboot
}

# ── Status ───────────────────────────────────────────────────────────────────

cmd_status() {
    python3 - "${PENDING}" "${RESULT}" "${PROFILE}" <<'PY'
import json, os, re, sys
pending_file, result_file, profile = sys.argv[1:4]

def version_of(system):
    try:
        return open(os.path.join(system, "dplaneos-version")).read().strip()
    except OSError:
        return "unknown"

out = {
    "running_version": version_of(os.path.realpath("/run/booted-system")),
    "installed_version": version_of(os.path.realpath(profile)),
    "pending": None,
    "last_result": None,
    "generations": [],
    "can_verify": os.path.exists(os.environ.get("DPLANEOS_UPDATE_PUBKEY", "/etc/dplaneos/update-signing.pub")),
}
if os.path.exists(pending_file):
    p = dict(l.rstrip("\n").split("=", 1) for l in open(pending_file) if "=" in l)
    out["pending"] = {"version": p.get("version"), "installed_at": p.get("installed_at"),
                      "running": os.path.realpath("/run/booted-system") == p.get("new_system")}
try:
    out["last_result"] = json.load(open(result_file))
except (OSError, ValueError):
    pass
directory = os.path.dirname(profile)
current = os.path.realpath(profile)
booted = os.path.realpath("/run/booted-system")
for name in sorted(os.listdir(directory), key=lambda n: int(re.sub(r"\D", "", n) or 0)):
    m = re.fullmatch(r"system-(\d+)-link", name)
    if not m:
        continue
    target = os.path.realpath(os.path.join(directory, name))
    out["generations"].append({
        "generation": int(m.group(1)), "version": version_of(target),
        "installed": target == current, "running": target == booted,
        "date": int(os.lstat(os.path.join(directory, name)).st_mtime),
    })
print(json.dumps(out))
PY
}

case "${1:-}" in
    verify)  cmd_verify "${2:?usage: dplaneos-update verify <file.update>}" ;;
    install) cmd_install "${2:?usage: dplaneos-update install <file.update>}" ;;
    confirm) cmd_confirm ;;
    revert)  cmd_revert ;;
    status)  cmd_status ;;
    *) echo "usage: dplaneos-update verify|install <file.update> | confirm | revert | status" >&2; exit 2 ;;
esac
