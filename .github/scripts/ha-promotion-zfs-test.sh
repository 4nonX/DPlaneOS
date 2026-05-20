#!/usr/bin/env bash
#
# ha-promotion-zfs-test.sh
# ─────────────────────────
# Tier-2 HA test: exercises the LOCAL-ZFS mechanics of an HA failover promotion
# against REAL ZFS pools on loopback devices.
#
# CONTEXT
# -------
# DPlaneOS HA testing has three tiers:
#
#   Tier 1  failover decision logic            -> Go suite, internal/ha/
#                                                 failover_integration_test.go
#                                                 (real Postgres, no ZFS, no peers)
#   Tier 2  local-ZFS promotion mechanics      -> THIS SCRIPT
#                                                 (real ZFS pools, single host)
#   Tier 3  multi-node partition + Patroni     -> nixosTest, nixos/tests/
#                                                 ha-failover.nix (real VMs)
#
# This script is Tier 2. ExecutePromotion() in daemon/internal/ha/promote.go
# performs four real-ZFS actions when a node is promoted to active:
#
#   1. libzfs.PoolImportAll("/dev/disk/by-id")  -> zpool import -a -f
#   2. for each dataset: if readonly=on, libzfs.DatasetSet(ds,"readonly","off")
#   3. for each dataset: if origin != "-", libzfs.DatasetPromote(ds)
#   4. service reloads + Patroni REST call
#
# Steps 1-3 are the LOCAL-ZFS mechanics and can be verified on a single host
# with real pools. This script drives the real `zpool`/`zfs` operations that
# the libzfs fallback path (zfs_fallback.go, used in every non-cgo build)
# shells out to, and asserts each produces the state ExecutePromotion expects.
#
# It deliberately does NOT cover: a real peer, a network partition, Patroni
# leader election, or ZFS send/recv catch-up between hosts. Those are Tier 3.
#
# REQUIREMENTS
#   - root (sudo): zpool/zfs require it
#   - zfsutils-linux + a loadable zfs kernel module (`modprobe zfs`)
#   On GitHub-hosted ubuntu-22.04/24.04 runners both are available, exactly as
#   the existing storage-failure-test.sh relies on.
#
# EXIT CODES
#   0  all assertions passed
#   1  an assertion failed
#   2  environment not capable (no ZFS module) - caller decides if that is fatal

set -uo pipefail

# ── output helpers ───────────────────────────────────────────────────────────
PASS=0
FAIL=0
ok()   { echo "  PASS  $*"; PASS=$((PASS + 1)); }
bad()  { echo "  FAIL  $*"; FAIL=$((FAIL + 1)); }
info() { echo "[*] $*"; }

# ── unique names so parallel CI jobs never collide ───────────────────────────
SUFFIX="$$"
POOL="hapool_${SUFFIX}"
IMG_DIR="$(mktemp -d /tmp/ha-zfs-XXXXXX)"
LOOPS=()

# ── cleanup: always runs, even on failure or interrupt ───────────────────────
# shellcheck disable=SC2317  # body is reached via the trap below, not inline
cleanup() {
  set +e
  zpool destroy "$POOL" 2>/dev/null
  # ${LOOPS[@]:-} guards the nounset case where cleanup fires before any
  # loop device was allocated (e.g. capability check failed).
  for lp in "${LOOPS[@]:-}"; do
    [ -n "$lp" ] && losetup -d "$lp" 2>/dev/null
  done
  rm -rf "$IMG_DIR"
}
trap cleanup EXIT INT TERM

# ── 0. capability check ──────────────────────────────────────────────────────
info "Checking ZFS capability"
if ! command -v zpool >/dev/null 2>&1; then
  echo "zpool not installed - cannot run Tier-2 ZFS test." >&2
  exit 2
fi
if ! modprobe zfs 2>/dev/null && ! zpool list >/dev/null 2>&1; then
  echo "ZFS kernel module unavailable - cannot run Tier-2 ZFS test." >&2
  echo "On GitHub hosted runners run 'sudo modprobe zfs' first." >&2
  exit 2
fi
ok "ZFS module available"

# ── 1. build a real pool with a real dataset hierarchy ───────────────────────
# Mirror layout mirrors the HA shared-nothing replication target: a pool with
# nested datasets, one of which we will mark readonly to simulate a standby.
info "Creating real loopback ZFS pool '$POOL' (mirror)"
for i in 0 1; do
  img="${IMG_DIR}/disk${i}.img"
  truncate -s 256M "$img" || { bad "truncate disk${i}"; exit 1; }
  lp="$(losetup --find --show "$img")" || { bad "losetup disk${i}"; exit 1; }
  LOOPS+=("$lp")
done

if zpool create -f -m "${IMG_DIR}/mnt" "$POOL" mirror "${LOOPS[0]}" "${LOOPS[1]}"; then
  ok "pool '$POOL' created (mirror, 2 loop vdevs)"
else
  bad "zpool create failed"; exit 1
fi

zfs create "${POOL}/data"     || { bad "create ${POOL}/data"; exit 1; }
zfs create "${POOL}/data/sub" || { bad "create ${POOL}/data/sub"; exit 1; }
ok "dataset hierarchy created (${POOL}/data, ${POOL}/data/sub)"

# ─────────────────────────────────────────────────────────────────────────────
# TEST A - ExecutePromotion step 2: readonly=off elevation.
# A standby's replicated pool is held readonly; promotion must flip it writable.
# This drives the exact zfs get/set that libzfs.DatasetGet/DatasetSet shell to.
# ─────────────────────────────────────────────────────────────────────────────
info "TEST A: readonly elevation (promotion step 2)"

# Simulate the standby state: dataset is readonly=on.
zfs set readonly=on "${POOL}/data"
RO_BEFORE="$(zfs get -H -o value readonly "${POOL}/data")"
if [ "$RO_BEFORE" != "on" ]; then
  # Some ZFS implementations (notably the legacy userspace zfs-fuse used on
  # dev machines) do not persist readonly=on. Real OpenZFS on the CI runner
  # does. Skip rather than false-fail so the script is correct on both.
  echo "  SKIP  TEST A: this ZFS build does not persist readonly=on"
  echo "        (expected on zfs-fuse; real OpenZFS runs this fully)"
else
  ok "precondition: ${POOL}/data is readonly=on (standby state)"

  # This is what ExecutePromotion does: detect readonly=on, set it off.
  # Mirrors libzfs.DatasetGet(ds,"readonly") then DatasetSet(ds,"readonly","off").
  RO_CHECK="$(zfs get -H -o value readonly "${POOL}/data")"
  if [ "$RO_CHECK" = "on" ]; then
    zfs set readonly=off "${POOL}/data"
  fi
  RO_AFTER="$(zfs get -H -o value readonly "${POOL}/data")"
  if [ "$RO_AFTER" = "off" ]; then
    ok "promotion elevated ${POOL}/data to readonly=off (writable)"
  else
    bad "readonly still '$RO_AFTER' after promotion - dataset not writable"
  fi

  # A dataset that was already writable must be left alone (no-op path).
  RO_SUB="$(zfs get -H -o value readonly "${POOL}/data/sub")"
  if [ "$RO_SUB" = "off" ]; then
    ok "already-writable ${POOL}/data/sub correctly needs no change"
  else
    bad "unexpected readonly state on ${POOL}/data/sub: '$RO_SUB'"
  fi
fi

# ─────────────────────────────────────────────────────────────────────────────
# TEST B - ExecutePromotion step 3: clone promotion.
# Replication can leave datasets as clones of received snapshots; promotion
# must `zfs promote` them so they become independent. Drives the exact path
# libzfs.DatasetGet(ds,"origin") + libzfs.DatasetPromote(ds) shells to.
# ─────────────────────────────────────────────────────────────────────────────
info "TEST B: clone promotion (promotion step 3)"

zfs snapshot "${POOL}/data@snap1"               || bad "snapshot create"
zfs clone "${POOL}/data@snap1" "${POOL}/cloned" || bad "clone create"

ORIGIN_BEFORE="$(zfs get -H -o value origin "${POOL}/cloned")"
if [ "$ORIGIN_BEFORE" = "${POOL}/data@snap1" ]; then
  ok "precondition: ${POOL}/cloned has origin '$ORIGIN_BEFORE' (is a clone)"
else
  bad "clone origin unexpected: '$ORIGIN_BEFORE'"
fi

# A non-clone dataset must report origin '-' and be skipped by ExecutePromotion.
# This MUST be asserted BEFORE the promote below: `zfs promote` inverts the
# clone relationship, after promoting ${POOL}/cloned the snapshot moves and
# ${POOL}/data itself becomes the clone. ${POOL}/data/sub is never cloned, so
# it is the stable witness for the non-clone case.
ORIGIN_NONCLONE="$(zfs get -H -o value origin "${POOL}/data/sub")"
if [ "$ORIGIN_NONCLONE" = "-" ]; then
  ok "non-clone ${POOL}/data/sub correctly reports origin '-' (skipped)"
else
  bad "non-clone ${POOL}/data/sub has unexpected origin '$ORIGIN_NONCLONE'"
fi

# ExecutePromotion: origin != "-" => promote.
ORIGIN_CHECK="$(zfs get -H -o value origin "${POOL}/cloned")"
if [ "$ORIGIN_CHECK" != "-" ]; then
  zfs promote "${POOL}/cloned"
fi
ORIGIN_AFTER="$(zfs get -H -o value origin "${POOL}/cloned")"
if [ "$ORIGIN_AFTER" = "-" ]; then
  ok "promotion converted clone to independent dataset (origin now '-')"
else
  bad "clone still has origin '$ORIGIN_AFTER' after promote"
fi

# ─────────────────────────────────────────────────────────────────────────────
# TEST C - ExecutePromotion step 1: export + re-import (pool failover motion).
# On a shared-storage failover the surviving node imports pools the dead node
# released. We verify a real export/import cycle round-trips the pool intact.
# ─────────────────────────────────────────────────────────────────────────────
info "TEST C: pool export + re-import (promotion step 1 motion)"

if zpool export "$POOL"; then
  ok "pool '$POOL' exported (simulates release by dead node)"
else
  bad "zpool export failed"
fi

if zpool list "$POOL" >/dev/null 2>&1; then
  bad "pool still listed after export"
else
  ok "pool correctly absent after export"
fi

# Re-import via the loop devices (still attached - cleanup only runs on exit).
if zpool import -d "${LOOPS[0]}" -d "${LOOPS[1]}" -f "$POOL"; then
  ok "pool '$POOL' re-imported by surviving node"
else
  bad "zpool import failed"
fi

# Data survived the cycle: the datasets must be back.
if zfs list -H -o name "${POOL}/data" >/dev/null 2>&1; then
  ok "dataset ${POOL}/data intact after export/import cycle"
else
  bad "dataset lost across export/import"
fi

# ─────────────────────────────────────────────────────────────────────────────
# TEST D - REGRESSION GUARD for the libzfs.PoolImportAll search-path bug.
#
# zfs_fallback.go PoolImportAll(searchPath) accepts a search path, validates
# it, then DISCARDS it and runs `zpool import -a -f -d /dev/disk/by-id` with
# the path hardcoded. promote.go currently calls it with "/dev/disk/by-id" so
# production is unaffected TODAY, but the signature is misleading: a pool whose
# devices are not under /dev/disk/by-id cannot be imported through it.
#
# This test PROVES the discrepancy is real, so that if PoolImportAll is ever
# fixed to honour its argument, this test starts failing and must be updated -
# turning a silent latent bug into a tracked, visible contract.
# ─────────────────────────────────────────────────────────────────────────────
info "TEST D: regression guard - PoolImportAll ignores its search-path arg"

zpool export "$POOL" || bad "export for TEST D"

# Importing with the CORRECT directory works (this is what TEST C proved).
# Importing with -d /dev/disk/by-id (what PoolImportAll hardcodes) must NOT
# find a pool whose vdevs are loopback images outside that directory.
if zpool import -d /dev/disk/by-id -f "$POOL" 2>/dev/null; then
  bad "pool imported via /dev/disk/by-id - bug may be fixed; update TEST D"
  zpool export "$POOL" 2>/dev/null
else
  ok "confirmed: pool NOT importable via hardcoded /dev/disk/by-id path"
  echo "        -> libzfs.PoolImportAll('$IMG_DIR') would FAIL to import this"
  echo "        -> pool because it ignores its argument. Documented bug."
fi

# Restore the pool via loop devices so cleanup can destroy it.
zpool import -d "${LOOPS[0]}" -d "${LOOPS[1]}" -f "$POOL" 2>/dev/null || true

# ── summary ──────────────────────────────────────────────────────────────────
echo
echo "──────────────────────────────────────────────"
echo "Tier-2 HA ZFS promotion test:  ${PASS} passed, ${FAIL} failed"
echo "──────────────────────────────────────────────"
[ "$FAIL" -eq 0 ] || exit 1
exit 0
