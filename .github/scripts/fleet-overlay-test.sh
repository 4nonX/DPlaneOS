#!/bin/bash
# Design 0001, Phase 6 exit test: a fleet manager and five remotes, each a
# daemon with its own database.
#   manager role -> register five remotes with their API tokens -> inventory
#   -> fleet-wide user and group rolled out canary first -> a local change on
#   a remote is kept as a proposal when the fleet's version wins -> an
#   unreachable remote pauses the rollout, continue finishes it -> the manager
#   goes down and every remote keeps working on its own.
set -e
echo 'Defaults env_keep += "PATH"' | sudo tee /etc/sudoers.d/ci-path >/dev/null
export PATH="/usr/sbin:/usr/bin:/sbin:/bin:$PATH"
: "${DATABASE_DSN:?DATABASE_DSN not set}"
export PGPASSWORD=dplaneos
NODES="m r1 r2 r3 r4 r5"
declare -A PORT=([m]=9100 [r1]=9101 [r2]=9102 [r3]=9103 [r4]=9104 [r5]=9105)
declare -A PID
MDB=$(basename "${DATABASE_DSN%%\?*}")
dbname() { [ "$1" = m ] && echo "$MDB" || echo "dplaneos_$1"; }
dsn() { echo "${DATABASE_DSN%/*}/$(dbname "$1")?sslmode=disable"; }
for n in r1 r2 r3 r4 r5; do
  psql -h localhost -U dplaneos -d "$MDB" -c "CREATE DATABASE $(dbname $n)" >/dev/null
done

start_node() { # name
  printf 'version: "6"\n' > "/tmp/fl-$1-state.yaml"
  sudo ./dplaned-ci --listen "127.0.0.1:${PORT[$1]}" --db-dsn "$(dsn "$1")" --gitops-state "/tmp/fl-$1-state.yaml" \
    --smb-conf "/tmp/fl-$1-smb.conf" -secrets-key "/tmp/fl-$1-secrets.key" >> "/tmp/dplaned-fl-$1.log" 2>&1 &
  PID[$1]=$!
}
wait_up() { # name
  for _ in $(seq 1 60); do curl -s "http://127.0.0.1:${PORT[$1]}/health" >/dev/null && return 0; sleep 0.5; done
  echo "::error::node $1 did not come up"; cat "/tmp/dplaned-fl-$1.log"; exit 1
}
stop_node() { sudo kill "${PID[$1]}" 2>/dev/null || true; for _ in $(seq 1 20); do curl -s "http://127.0.0.1:${PORT[$1]}/health" >/dev/null || return 0; sleep 0.5; done; }
for n in $NODES; do start_node $n; done
cleanup() { for n in $NODES; do sudo kill "${PID[$n]}" 2>/dev/null || true; done; }
trap cleanup EXIT
for n in $NODES; do wait_up $n; done

CI_PASS="CiAdmin1!Test"
CI_HASH=$(python3 -c "import bcrypt; print(bcrypt.hashpw(b'$CI_PASS', bcrypt.gensalt(rounds=10)).decode())")
for n in $NODES; do
  psql -h localhost -U dplaneos -d "$(dbname $n)" -c "UPDATE users SET password_hash='$CI_HASH', active=1, role='admin', must_change_password=0 WHERE username='admin';" >/dev/null
done

# ── helpers ───────────────────────────────────────────────────────────────────
PASS=0; FAIL=0; FAILURES=""
ok()   { printf "  \033[32m✓\033[0m %s\n" "$1"; PASS=$((PASS+1)); }
fail() { FAIL=$((FAIL+1)); FAILURES="$FAILURES\n  ✗ $1"; echo "  ✗ $1 (last response: $(head -c 400 /tmp/last_resp.json 2>/dev/null))"; }
check() { if eval "$2"; then ok "$1"; else fail "$1"; fi; }
# eventually "desc" "condition" seconds
eventually() { local end=$((SECONDS + $3)); while [ $SECONDS -lt $end ]; do eval "$2" && { ok "$1"; return; }; sleep 2; done; fail "$1"; }

declare -A SESSION CSRF TOKEN
login() { # node
  local resp
  resp=$(curl -s --max-time 15 -X POST "http://127.0.0.1:${PORT[$1]}/api/auth/login" -H "Content-Type: application/json" \
    -d "{\"username\":\"admin\",\"password\":\"$CI_PASS\"}")
  SESSION[$1]=$(echo "$resp" | python3 -c "import sys,json; print(json.load(sys.stdin).get('session_id',''))")
  CSRF[$1]=$(curl -s "http://127.0.0.1:${PORT[$1]}/api/csrf" -H "X-Session-ID: ${SESSION[$1]}" | python3 -c "import sys,json; print(json.load(sys.stdin).get('csrf_token',''))")
}
api() { # node method path [data]
  local args=(-s --max-time 60 -X "$2" "http://127.0.0.1:${PORT[$1]}$3" -H "X-Session-ID: ${SESSION[$1]}"
    -H "X-CSRF-Token: ${CSRF[$1]}" -H "X-User: admin" -H "Content-Type: application/json")
  [ -n "${4:-}" ] && args+=(-d "$4")
  curl "${args[@]}" > /tmp/last_resp.json || echo '{"error":"request failed"}' > /tmp/last_resp.json
  cat /tmp/last_resp.json
}
jq_() { python3 -c "import json,sys; d=json.load(open('/tmp/last_resp.json')); print($1)"; }
sql() { psql -h localhost -U dplaneos -d "$(dbname "$1")" -tAc "$2"; }
user_email() { sql "$1" "SELECT COALESCE(email,'') FROM users WHERE username='$2'"; }
users_named() { sql "$1" "SELECT COUNT(*) FROM users WHERE username='$2'"; }
groups_named() { sql "$1" "SELECT COUNT(*) FROM groups WHERE name='$2'"; }
group_gid() { sql "$1" "SELECT gid FROM groups WHERE name='$2'"; }
last_origin() { sql "$1" "SELECT origin FROM config_revisions WHERE resource_kind='$2' AND resource_key='$3' ORDER BY id DESC LIMIT 1"; }
rollout_state() { api m GET /api/fleet >/dev/null; jq_ "next((r['state'] for r in d['rollouts'] if r['name']=='$1'), '')"; }
remote_id() { api m GET /api/fleet >/dev/null; jq_ "next((r['id'] for r in d['remotes'] if r['name']=='$1'), '')"; }

set +e
for n in $NODES; do login $n; done
check "Logged in on all six nodes" '[ -n "${SESSION[m]}" ] && [ -n "${SESSION[r5]}" ]'
for n in r1 r2 r3 r4 r5; do
  api $n POST /api/auth/tokens '{"action":"create","name":"fleet","scopes":"admin"}' >/dev/null
  TOKEN[$n]=$(jq_ "d.get('token','')")
done
check "Each remote issued an admin API token" '[[ "${TOKEN[r1]}" == dpl_* ]] && [[ "${TOKEN[r5]}" == dpl_* ]]'

echo "::group::Manager role and inventory"
api m GET /api/fleet >/dev/null
check "The role is off by default" '[ "$(jq_ "d[\"settings\"][\"enabled\"]")" = False ]'
api m PUT /api/fleet/settings '{"enabled":true,"name":"acme"}' >/dev/null
check "Manager role switched on" '[ "$(jq_ "d.get(\"success\")")" = True ]'
api m POST /api/fleet/remotes "{\"name\":\"bad\",\"url\":\"http://127.0.0.1:${PORT[r1]}\",\"token\":\"dpl_wrong\",\"labels\":{}}" >/dev/null
check "A wrong token is refused when adding" '[ "$(jq_ "d.get(\"success\")")" != True ]'
for n in r1 r2 r3 r4 r5; do
  label=$([ $n = r1 ] && echo '{"tier":"canary"}' || echo '{"site":"lab"}')
  api m POST /api/fleet/remotes "{\"name\":\"$n\",\"url\":\"http://127.0.0.1:${PORT[$n]}\",\"token\":\"${TOKEN[$n]}\",\"labels\":$label}" >/dev/null
done
api m POST /api/fleet/poll '{}' >/dev/null
check "Five remotes registered and reporting" '[ "$(jq_ "sum(1 for r in d[\"remotes\"] if r.get(\"status\") and not r.get(\"last_error\"))")" = 5 ]'
check "Inventory: remotes report standalone mode and their node id" \
  '[ "$(jq_ "all(r[\"status\"][\"mode\"]==\"standalone\" and r[\"status\"][\"node_id\"] for r in d[\"remotes\"])")" = True ]'
api r2 GET /api/fleet/node-status >/dev/null
check "A remote answers its own inventory report" '[ "$(jq_ "d[\"status\"][\"name\"] != \"\"")" = True ]'
echo "::endgroup::"

echo "::group::Fleet-wide user and group, canary first"
api m PUT /api/fleet/resources '{"kind":"user","key":"carol","payload":{"username":"carol","email":"carol@acme.lan","role":"user","active":true}}' >/dev/null
check "Fleet user carol stored" '[ "$(jq_ "d.get(\"success\")")" = True ]'
api m PUT /api/fleet/resources '{"kind":"group","key":"staff","payload":{"name":"staff","gid":3100,"description":"fleet staff","members":["carol"]}}' >/dev/null
check "Fleet group staff stored" '[ "$(jq_ "d.get(\"success\")")" = True ]'
api m PUT /api/fleet/resources '{"kind":"share","key":"media","payload":{"name":"media","path":"/mnt/x"}}' >/dev/null
check "Shares cannot be fleet-wide" '[ "$(jq_ "d.get(\"success\")")" != True ]'
check "Nothing reached a remote before a rollout" '[ -z "$(user_email r1 carol)" ] && [ "$(users_named r1 carol)" = 0 ]'
R1=$(remote_id r1); REST=$(jq_ "[r['id'] for r in d['remotes'] if r['name']!='r1']")
api m POST /api/fleet/rollouts "{\"name\":\"staff\",\"resources\":[{\"kind\":\"user\",\"key\":\"carol\"},{\"kind\":\"group\",\"key\":\"staff\"}],
  \"waves\":[{\"name\":\"canary\",\"remotes\":[$R1]},{\"name\":\"rest\",\"remotes\":$REST}],\"soak_secs\":20,\"auto_advance\":true}" >/dev/null
check "Rollout created" '[ "$(jq_ "d.get(\"success\")")" = True ]'
eventually "The canary got carol" '[ "$(user_email r1 carol)" = carol@acme.lan ]' 40
check "The other remotes wait during the soak time" '[ "$(users_named r4 carol)" = 0 ]'
eventually "Rollout finished" '[ "$(rollout_state staff)" = done ]' 120
for n in r1 r2 r3 r4 r5; do
  check "$n has carol and group staff" '[ "$(user_email '$n' carol)" = carol@acme.lan ] && [ "$(group_gid '$n' staff)" = 3100 ]'
done
api r3 GET /api/fleet >/dev/null
check "A remote shows its fleet membership" '[ "$(jq_ "d[\"membership\"][\"fleet\"]")" = acme ]'
check "The fleet's change is in the remote's revision log as overlay" \
  '[ "$(last_origin r3 user carol)" = overlay ]'
echo "::endgroup::"

echo "::group::Local change on a remote, then the fleet changes the same user"
sql r3 "UPDATE users SET email='carol@r3.lan' WHERE username='carol'" >/dev/null
api r3 POST /api/config/capture '{}' >/dev/null
api m PUT /api/fleet/resources '{"kind":"user","key":"carol","payload":{"username":"carol","email":"carol@new.acme.lan","role":"user","active":true}}' >/dev/null
api m POST /api/fleet/rollouts "{\"name\":\"address\",\"resources\":[{\"kind\":\"user\",\"key\":\"carol\"}],\"waves\":[{\"name\":\"all\",\"remotes\":$(api m GET /api/fleet >/dev/null; jq_ "[r['id'] for r in d['remotes']]")}],\"soak_secs\":0,\"auto_advance\":true}" >/dev/null
eventually "Rollout 'address' finished" '[ "$(rollout_state address)" = done ]' 90
check "The fleet's version won on r3" '[ "$(user_email r3 carol)" = carol@new.acme.lan ]'
api r3 GET /api/config/conflicts >/dev/null
check "r3's own change is kept as a proposal" '[ "$(jq_ "any(p[\"payload\"].get(\"email\")==\"carol@r3.lan\" for p in d[\"proposals\"])")" = True ]'
echo "::endgroup::"

echo "::group::An unreachable remote pauses the rollout"
stop_node r5
api m PUT /api/fleet/resources '{"kind":"group","key":"ops","payload":{"name":"ops","gid":3200,"description":"","members":[]}}' >/dev/null
api m POST /api/fleet/rollouts "{\"name\":\"ops\",\"resources\":[{\"kind\":\"group\",\"key\":\"ops\"}],\"waves\":[{\"name\":\"all\",\"remotes\":$(api m GET /api/fleet >/dev/null; jq_ "[r['id'] for r in d['remotes']]")}],\"soak_secs\":0,\"auto_advance\":true}" >/dev/null
eventually "Rollout 'ops' paused" '[ "$(rollout_state ops)" = paused ]' 120
check "The reachable remotes got group ops" '[ "$(groups_named r1 ops)" = 1 ] && [ "$(groups_named r4 ops)" = 1 ]'
api m GET /api/fleet >/dev/null
check "The pause says which remote failed" '[ "$(jq_ "\"r5\" in next(r[\"message\"] for r in d[\"rollouts\"] if r[\"name\"]==\"ops\")")" = True ]'
start_node r5; wait_up r5
ID=$(jq_ "next(r['id'] for r in d['rollouts'] if r['name']=='ops')")
api m POST "/api/fleet/rollouts/$ID/continue" '{}' >/dev/null
eventually "Continue finishes the rollout" '[ "$(rollout_state ops)" = done ]' 60
check "r5 got group ops after continue" '[ "$(groups_named r5 ops)" = 1 ]'
echo "::endgroup::"

echo "::group::The manager goes down; the remotes do not care"
stop_node m
for n in r1 r2 r3 r4 r5; do login $n; done
check "Login still works on every remote" '[ -n "${SESSION[r1]}" ] && [ -n "${SESSION[r5]}" ]'
api r2 POST /api/rbac/users "{\"action\":\"create\",\"username\":\"dave\",\"password\":\"DaveLocal1!pw\",\"email\":\"dave@r2.lan\",\"role\":\"user\",\"confirm_password\":\"$CI_PASS\"}" >/dev/null
check "A remote keeps changing its own configuration" '[ "$(user_email r2 dave)" = dave@r2.lan ]'
api r4 GET /api/fleet/node-status >/dev/null
check "A remote's inventory report still answers" '[ "$(jq_ "d.get(\"success\")")" = True ]'
check "Fleet users stay on the remotes" '[ "$(user_email r5 carol)" = carol@new.acme.lan ]'
api r1 GET /api/config/sync/status >/dev/null
check "Shortly after, the remote does not report the manager unreachable yet" '[ "$(jq_ "d[\"fleet\"][\"unreachable\"]")" = False ]'
sql r1 "UPDATE fleet_membership SET last_contact_at = NOW() - INTERVAL '6 minutes'" >/dev/null # five minutes of silence
api r1 GET /api/config/sync/status >/dev/null
check "After five minutes of silence the remote reports the manager unreachable" '[ "$(jq_ "d[\"fleet\"][\"unreachable\"]")" = True ]'
start_node m; wait_up m; login m
api m POST /api/fleet/poll '{}' >/dev/null
check "The manager picks the inventory up again after restart" '[ "$(jq_ "sum(1 for r in d[\"remotes\"] if not r.get(\"last_error\"))")" = 5 ]'
api r1 GET /api/config/sync/status >/dev/null
check "The remote sees its manager again" '[ "$(jq_ "d[\"fleet\"][\"unreachable\"]")" = False ] && [ "$(jq_ "d[\"fleet\"][\"fleet\"]")" = acme ]'
echo "::endgroup::"

echo ""
echo "Fleet overlay: $PASS passed, $FAIL failed"
if [ $FAIL -gt 0 ]; then
  printf "%b\n" "$FAILURES"
  for n in $NODES; do echo "--- $n ---"; tail -40 "/tmp/dplaned-fl-$n.log"; done
  exit 1
fi
