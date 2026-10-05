# End-to-end smoke test, run inside the live-boot VM against the real daemon,
# ZFS, Samba, Avahi and PostgreSQL. Uses the API the way the UI does.
#
# Disks: four virtio disks with serials dplaneci0..dplaneci3, so udev creates
# real /dev/disk/by-id/virtio-* links (pool creation requires by-id paths).
#
# Every step prints "ok: ..." or exits non-zero with the HTTP status and body.

BASE=http://localhost
SESSION=""
CSRF=""
CODE=""
RESP=/tmp/smoke-resp.json

call() { # method path [json]  -> sets CODE, body in $RESP
  local method=$1 path=$2 data=${3:-}
  local args=(-sS -o "$RESP" -w '%{http_code}' -X "$method" "$BASE$path" -H 'Content-Type: application/json')
  if [[ -n $SESSION ]]; then args+=(-H "X-Session-ID: $SESSION" -H "X-User: admin"); fi
  if [[ -n $CSRF ]]; then args+=(-H "X-CSRF-Token: $CSRF"); fi
  if [[ -n $data ]]; then args+=(-d "$data"); fi
  CODE=$(curl "${args[@]}")
}

expect() { # want method path [json]
  local want=$1
  shift
  call "$@"
  if [[ $CODE != "$want" ]]; then
    echo "FAIL: $1 $2 -> HTTP $CODE (want $want): $(head -c 600 "$RESP")"
    exit 1
  fi
  echo "ok: $1 $2 -> $CODE"
}

check() { # description command...
  local desc=$1
  shift
  if ! "$@"; then
    echo "FAIL: $desc"
    exit 1
  fi
  echo "ok: $desc"
}

json() { jq -r "$1" "$RESP"; }

contains() { grep -qi -- "$2" <<<"$1"; }   # text pattern (case-insensitive)
lacks() { ! grep -qi -- "$2" <<<"$1"; }

retry() { # tries command... (1s apart)
  local tries=$1
  shift
  for _ in $(seq "$tries"); do
    if "$@"; then return 0; fi
    sleep 1
  done
  return 1
}

# ── Login ──────────────────────────────────────────────────────────────────
check "API reachable" retry 60 curl -sf "$BASE/api/system/status" -o /dev/null
expect 200 POST /api/system/setup-admin '{"username":"admin","password":"Smoke-Test-Pass-1"}'
expect 200 POST /api/auth/login '{"username":"admin","password":"Smoke-Test-Pass-1"}'
SESSION=$(json .session_id)
expect 200 GET /api/csrf
CSRF=$(json .csrf_token)
check "session and CSRF token issued" test -n "$SESSION" -a -n "$CSRF" -a "$SESSION" != null -a "$CSRF" != null

# ── Disks ──────────────────────────────────────────────────────────────────
udevadm settle
D=()
for i in 0 1 2 3; do D+=("/dev/disk/by-id/virtio-dplaneci$i"); done
check "by-id links for the four test disks" test -e "${D[0]}" -a -e "${D[1]}" -a -e "${D[2]}" -a -e "${D[3]}"
expect 200 GET /api/system/disks
check "disk API lists the test disks as free" \
  test "$(jq '[.disks[] | select(.by_id_path | test("dplaneci")) | select(.in_use == false)] | length' "$RESP")" = 4

# ── Pool: automatic-layout request shape (topology.data) ───────────────────
expect 200 POST /api/system/pool/create \
  "{\"name\":\"smoke\",\"topology\":{\"data\":[{\"type\":\"mirror\",\"disks\":[\"${D[0]}\",\"${D[1]}\"]}]}}"
check "pool smoke is a mirror" contains "$(zpool status smoke)" mirror-0

# ── Expand pool: add a second mirror vdev ──────────────────────────────────
expect 200 POST /api/zfs/pool/add-vdev \
  "{\"pool\":\"smoke\",\"vdev_type\":\"mirror\",\"disks\":[\"${D[2]}\",\"${D[3]}\"]}"
check "pool smoke has two mirror vdevs" contains "$(zpool status smoke)" mirror-1
expect 409 POST /api/zfs/pool/add-vdev "{\"pool\":\"smoke\",\"vdev_type\":\"mirror\",\"disks\":[\"${D[0]}\",\"${D[1]}\"]}"

# ── Dataset preset: SMB share (case-insensitive, POSIX ACLs) ───────────────
expect 200 POST /api/zfs/datasets '{"name":"smoke/share","mountpoint":"/smoke/share","compression":"lz4","atime":"off","recordsize":"128K","xattr":"sa","acltype":"posix","casesensitivity":"insensitive"}'
check "dataset create reported success" test "$(json .success)" = true
check "casesensitivity=insensitive" test "$(zfs get -H -o value casesensitivity smoke/share)" = insensitive
check "acltype=posix" test "$(zfs get -H -o value acltype smoke/share)" = posix
check "xattr=sa" test "$(zfs get -H -o value xattr smoke/share)" = sa
expect 400 POST /api/zfs/datasets '{"name":"smoke/bad","casesensitivity":"maybe"}'
expect 400 POST /api/zfs/datasets '{"name":"smoke/bad","acltype":"nfsv4"}'

# ── Snapshots and rollback safety levels ───────────────────────────────────
echo one > /smoke/share/file.txt
expect 200 POST /api/zfs/snapshots '{"dataset":"smoke/share","name":"s1"}'
echo two > /smoke/share/file.txt
expect 200 POST /api/zfs/snapshots '{"dataset":"smoke/share","name":"s2"}'
expect 409 POST /api/zfs/snapshots/rollback '{"snapshot":"smoke/share@s1","mode":"safe"}'
check "safe rollback kept s2" zfs list -H smoke/share@s2
expect 400 POST /api/zfs/snapshots/rollback '{"snapshot":"smoke/share@s1","mode":"yolo"}'
expect 200 POST /api/zfs/snapshots/rollback '{"snapshot":"smoke/share@s1","mode":"destroy_newer"}'
check "destroy_newer removed s2" lacks "$(zfs list -H -t snapshot -o name -r smoke/share)" '@s2'
check "data rolled back to s1" test "$(cat /smoke/share/file.txt)" = one

# ── Export, scan, import with rename ───────────────────────────────────────
zpool export smoke
expect 200 GET /api/zfs/pool/importable
GUID=$(jq -r '.pools[] | select(.name == "smoke") | .guid' "$RESP")
check "importable list contains smoke ($GUID)" test -n "$GUID"
check "smoke does not need -f" test "$(jq -r '.pools[] | select(.name == "smoke") | .needs_force' "$RESP")" = false
expect 400 POST /api/zfs/pool/import '{"guid":"not-a-guid"}'
expect 200 POST /api/zfs/pool/import "{\"guid\":\"$GUID\",\"new_name\":\"smoke2\"}"
check "imported as smoke2" zpool list -H smoke2
check "dataset came back with the pool" test "$(zfs get -H -o value casesensitivity smoke2/share)" = insensitive

# ── SMB share with per-share options ───────────────────────────────────────
expect 400 POST /api/shares '{"action":"create","name":"bad","path":"/smoke2/share","hosts_allow":"1.2.3.4;rm"}'
expect 200 POST /api/shares '{"action":"create","name":"tm","path":"/smoke2/share","time_machine":true,"time_machine_quota":"100G","shadow_copy":true,"hosts_allow":"10.0.0.0/8"}'
SHARE_ID=$(json .id)
expect 200 POST /api/shares '{"action":"create","name":"plain","path":"/smoke2/share"}'

SHARES_CONF=/var/lib/dplaneos/smb-shares.conf
CONF=$(cat "$SHARES_CONF")
check "daemon wrote $SHARES_CONF" contains "$CONF" '^\[tm\]'
check "shares file sets no global keys of its own" lacks "$CONF" '^ *(security|workgroup|map to guest) *='
check "shares file re-opens [global] at the end" test "$(tail -n 1 "$SHARES_CONF")" = "[global]"

section() { awk -v s="[$1]" '$0 == s { f = 1; next } /^\[/ { f = 0 } f' /tmp/testparm.txt; }
if ! testparm -s > /tmp/testparm.txt 2>/tmp/testparm.err; then
  cat /tmp/testparm.err
  echo "FAIL: testparm"
  exit 1
fi
TM=$(section tm)
PLAIN=$(section plain)
GLOBAL=$(section global)
check "testparm: [tm] is a Time Machine target" contains "$TM" 'fruit:time machine = yes'
check "testparm: [tm] size cap" contains "$TM" 'fruit:time machine max size = 100G'
check "testparm: [tm] shadow copies" contains "$TM" 'shadow_copy2'
check "testparm: [tm] hosts allow" contains "$TM" 'hosts allow = 10.0.0.0/8'
check "testparm: [plain] loads fruit" contains "$PLAIN" 'catia fruit streams_xattr'
check "testparm: [plain] is no Time Machine target" lacks "$PLAIN" 'time machine'
# "server string" sorts after "include" in the NixOS-rendered [global], so it
# only stays global because the shares file re-opens [global] at its end.
check "testparm: NixOS globals after the include stay global" contains "$GLOBAL" 'server string = '
check "testparm: no global parameter landed in a share" lacks "$(cat /tmp/testparm.err)" 'Global parameter'

AVAHI=/etc/avahi/services/dplaneos-timemachine.service
check "Time Machine Bonjour file names share tm" grep -q 'adVN=tm' "$AVAHI"
# Capture first: with pipefail, grep -q closing the pipe early would fail avahi-browse.
adisk_published() { contains "$(avahi-browse -tpr _adisk._tcp 2>/dev/null || true)" 'adVN=tm'; }
check "avahi publishes _adisk for tm" retry 15 adisk_published

# Edit the share (the UI sends the id); turn Time Machine off.
expect 200 POST /api/shares "{\"action\":\"update\",\"id\":$SHARE_ID,\"time_machine\":false,\"time_machine_quota\":\"\"}"
expect 200 GET /api/shares/list
check "share list reflects the edit" test "$(jq -r '.shares[] | select(.name == "tm") | .time_machine' "$RESP")" = false
testparm -s > /tmp/testparm.txt 2>/dev/null
check "testparm: [tm] no longer a Time Machine target" lacks "$(section tm)" 'time machine'

# ── Schedules: timers are installed and actually run (token, PATH, hook) ────
expect 200 POST /api/snapshots/schedules '[{"dataset":"smoke2/share","frequency":"daily","retention":5,"enabled":true}]'
SNAP_UNIT=dplaneos-snap-smoke2-share-daily
check "snapshot timer active" systemctl is-active --quiet "$SNAP_UNIT.timer"
check "snapshot unit ExecStart is absolute" contains "$(systemctl cat "$SNAP_UNIT.service")" 'ExecStart=/'
check "snapshot timer service runs" systemctl start "$SNAP_UNIT.service"
check "timer run created an auto-daily snapshot" contains "$(zfs list -H -t snapshot -o name -r smoke2/share)" '@auto-daily'

expect 200 POST /api/zfs/scrub/schedule '[{"pool":"smoke2","interval":"weekly","day":0,"hour":3}]'
check "scrub timer active" systemctl is-active --quiet dplaneos-scrub-smoke2.timer
check "scrub timer service runs" systemctl start dplaneos-scrub-smoke2.service
check "scrub ran on smoke2" contains "$(zpool status smoke2)" 'scrub'

# ── Feature flags (Settings → Features) ────────────────────────────────────
expect 200 GET /api/system/features
check "features listed" test "$(jq '.features | length' "$RESP")" -ge 6
expect 200 POST /api/system/features/ses_enclosure/enable '{"new_state":"beta"}'
expect 200 GET /api/system/features
check "feature state persisted" test "$(jq -r '.features[] | select(.id == "ses_enclosure") | .state' "$RESP")" = beta
check "feature state stored in PostgreSQL" \
  test "$(runuser -u postgres -- psql -d dplaneos -tAc "SELECT state FROM feature_flags WHERE id = 'ses_enclosure'")" = beta

# ── Migrations moved into the embedded set ─────────────────────────────────
check "operation_journal and audit_events exist" \
  test "$(runuser -u postgres -- psql -d dplaneos -tAc "SELECT count(*) FROM information_schema.tables WHERE table_name IN ('operation_journal','audit_events')")" = 2
runuser -u postgres -- psql -d dplaneos -v ON_ERROR_STOP=1 -qc \
  "INSERT INTO audit_events (event_type, component, status) VALUES ('smoke', 'ci', 'success')"
check "audit_events HMAC trigger fills hmac (pgcrypto)" \
  test -n "$(runuser -u postgres -- psql -d dplaneos -tAc "SELECT hmac FROM audit_events WHERE event_type = 'smoke'")"

echo "SMOKE TEST PASSED"
