#!/usr/bin/env bash
# DPlaneOS update check after the reboot (dplaneos-ota-health.service).
#
# An update (daemon: POST /api/system/updates/nixos/apply) points the system
# flake at the new release, builds it as the next boot entry, writes
# /var/lib/dplaneos/update-pending.json and reboots. This script runs after
# the boot: when the new system works it keeps it, otherwise it makes the
# generation that ran before the boot default again, restores the flake and
# reboots into it. The daemon reports the result (update-result.json).
set -u

PENDING="${PENDING:-/var/lib/dplaneos/update-pending.json}"
RESULT="${RESULT:-/var/lib/dplaneos/update-result.json}"
SOCKET="${DAEMON_SOCKET:-/run/dplaneos/dplaned.sock}"
FLAKE_DIR="${FLAKE_DIR:-/etc/nixos}"
CHECK_SECONDS="${CHECK_SECONDS:-300}"
# Units that must run (ota-module.nix: nginx, and PostgreSQL when local).
CHECK_UNITS="${CHECK_UNITS:-nginx.service}"
PROFILE="${SYSTEM_PROFILE:-/nix/var/nix/profiles/system}"

log() { echo "dplaneos-update-health: $*"; }

[ -f "$PENDING" ] || { log "no update waiting for its check"; exit 0; }

prev=$(jq -r '.previous_system' "$PENDING")
from=$(jq -r '.from_version // ""' "$PENDING")
to=$(jq -r '.to_version // ""' "$PENDING")
shares=$(jq -r '.smb_shares // 0' "$PENDING")
mapfile -t pools < <(jq -r '.pools[]?' "$PENDING")

result() { # status reason
  jq -n --arg s "$1" --arg r "$2" --arg f "$from" --arg t "$to" --arg at "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
    '{status: $s, from_version: $f, to_version: $t, reason: $r, at: $at}' > "$RESULT.tmp" && mv "$RESULT.tmp" "$RESULT"
}

failures=()
check() {
  failures=()
  local current
  current=$(readlink -f /run/current-system)
  if [ "$current" = "$prev" ]; then
    failures+=("the new system did not boot")
    return
  fi
  curl -s -o /dev/null --max-time 10 --unix-socket "$SOCKET" http://localhost/api/system/health \
    || failures+=("the daemon does not answer")
  local u
  for u in $CHECK_UNITS; do
    systemctl is-active -q "$u" || failures+=("$u is not running")
  done
  for p in "${pools[@]}"; do
    [ -n "$p" ] || continue
    zpool list -H -o name "$p" >/dev/null 2>&1 || failures+=("pool $p is not imported")
  done
  if [ "$shares" -gt 0 ] 2>/dev/null; then
    systemctl is-active -q samba-smbd.service || failures+=("samba-smbd.service is not running ($shares shares)")
  fi
}

rollback() { # reason
  log "going back to $prev: $1"
  if ! nix-env --profile "$PROFILE" --set "$prev" || ! "$prev/bin/switch-to-configuration" boot; then
    log "ERROR: could not make $prev the boot default; choose the previous entry in the boot menu"
    result rolled_back "$1 (the previous system could not be made the boot default: choose it in the boot menu)"
    rm -f "$PENDING"
    exit 1
  fi
  jq -r '.previous_flake' "$PENDING" > "$FLAKE_DIR/flake.nix"
  if [ -n "$(jq -r '.previous_lock // ""' "$PENDING")" ]; then
    jq -r '.previous_lock' "$PENDING" > "$FLAKE_DIR/flake.lock"
  fi
  result rolled_back "$1"
  rm -f "$PENDING"
  if [ "$(readlink -f /run/current-system)" != "$prev" ]; then
    log "rebooting into $prev"
    systemctl reboot
  fi
  exit 0
}

# Services take a while after boot: check until all is well or time is up.
deadline=$(( $(date +%s) + CHECK_SECONDS ))
while :; do
  check
  if [ "${#failures[@]}" -eq 0 ]; then
    result ok ""
    rm -f "$PENDING"
    log "update to $to checked and kept"
    exit 0
  fi
  if [ "${failures[0]}" = "the new system did not boot" ] || [ "$(date +%s)" -ge "$deadline" ]; then
    reason=$(printf "%s; " "${failures[@]}"); reason=${reason%; }
    rollback "$reason"
  fi
  sleep 15
done
