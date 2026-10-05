#!/bin/bash
# Design 0001, Phase 2 exit test: two nodes with their own databases.
#   pair (with merge preview) -> partition (firewall) -> change both sides
#   -> both keep working (isolated) -> reconnect -> different changes merge,
#   the same share changed on both sides is a conflict -> resolve -> detach.
#
# Both daemons run on this runner. Each reaches the other through a socat
# relay port (19000 -> node A, 19001 -> node B); the partition drops those
# ports with iptables, while the test still talks to each node directly.
set -e
echo 'Defaults env_keep += "PATH"' | sudo tee /etc/sudoers.d/ci-path >/dev/null
export PATH="/usr/sbin:/usr/bin:/sbin:/bin:$PATH"
: "${DATABASE_DSN:?DATABASE_DSN not set}"
DSN_A="$DATABASE_DSN"
DSN_B="${DATABASE_DSN%/*}/dplaneos_b?sslmode=disable"
export PGPASSWORD=dplaneos
psql -h localhost -U dplaneos -d dplaneos -c "CREATE DATABASE dplaneos_b" >/dev/null

echo "--- ZFS pool shared by both nodes (same host) ---"
sudo truncate -s 512M /tmp/sync-vdisk.img
LOOP=$(sudo losetup --find --show /tmp/sync-vdisk.img)
sudo mkdir -p /mnt/syncpool
sudo zpool create -f -m /mnt/syncpool syncpool "$LOOP"
sudo zfs create syncpool/data

for n in a b; do
  printf 'version: "6"\n' > /tmp/$n-state.yaml
done

start_node() { # name port dsn
  sudo ./dplaned-ci --listen "127.0.0.1:$2" --db-dsn "$3" --gitops-state "/tmp/$1-state.yaml" \
    --smb-conf "/tmp/$1-smb.conf" > "/tmp/dplaned-$1.log" 2>&1 &
  echo $!
}
PID_A=$(start_node a 9000 "$DSN_A")
PID_B=$(start_node b 9001 "$DSN_B")
socat TCP-LISTEN:19000,fork,reuseaddr,bind=127.0.0.1 TCP:127.0.0.1:9000 & RELAY_A=$!
socat TCP-LISTEN:19001,fork,reuseaddr,bind=127.0.0.1 TCP:127.0.0.1:9001 & RELAY_B=$!
cleanup() {
  sudo iptables -D INPUT -p tcp --dport 19000 -j DROP 2>/dev/null || true
  sudo iptables -D INPUT -p tcp --dport 19001 -j DROP 2>/dev/null || true
  sudo kill "$PID_A" "$PID_B" 2>/dev/null || true
  kill "$RELAY_A" "$RELAY_B" 2>/dev/null || true
  sudo zpool destroy syncpool 2>/dev/null || true
  sudo losetup -d "$LOOP" 2>/dev/null || true
}
trap cleanup EXIT

for port in 9000 9001; do
  for _ in $(seq 1 40); do curl -s "http://127.0.0.1:$port/health" >/dev/null && break; sleep 0.5; done
  if ! curl -s "http://127.0.0.1:$port/health" >/dev/null; then
    echo "::error::node on port $port did not come up"; cat /tmp/dplaned-a.log /tmp/dplaned-b.log; exit 1
  fi
done

CI_PASS="CiAdmin1!Test"
CI_HASH=$(python3 -c "import bcrypt; print(bcrypt.hashpw(b'$CI_PASS', bcrypt.gensalt(rounds=10)).decode())")
for db in dplaneos dplaneos_b; do
  psql -h localhost -U dplaneos -d $db -c "UPDATE users SET password_hash='$CI_HASH', active=1, role='admin', must_change_password=0 WHERE username='admin';" >/dev/null
done

# ── helpers ───────────────────────────────────────────────────────────────────
PASS=0; FAIL=0; FAILURES=""
ok()   { printf "  \033[32m✓\033[0m %s\n" "$1"; PASS=$((PASS+1)); }
fail() { FAIL=$((FAIL+1)); FAILURES="$FAILURES\n  ✗ $1"; echo "  ✗ $1 (last response: $(head -c 300 /tmp/last_resp.json 2>/dev/null))"; }
check() { if eval "$2"; then ok "$1"; else fail "$1"; fi; }

declare -A SESSION CSRF
login() { # node port
  local resp
  resp=$(curl -s --max-time 15 -X POST "http://127.0.0.1:$2/api/auth/login" -H "Content-Type: application/json" \
    -d "{\"username\":\"admin\",\"password\":\"$CI_PASS\"}")
  SESSION[$1]=$(echo "$resp" | python3 -c "import sys,json; print(json.load(sys.stdin).get('session_id',''))")
  CSRF[$1]=$(curl -s "http://127.0.0.1:$2/api/csrf" -H "X-Session-ID: ${SESSION[$1]}" | python3 -c "import sys,json; print(json.load(sys.stdin).get('csrf_token',''))")
}
port() { [ "$1" = a ] && echo 9000 || echo 9001; }
api() { # node method path [data]
  local args=(-s --max-time 40 -X "$2" "http://127.0.0.1:$(port "$1")$3" -H "X-Session-ID: ${SESSION[$1]}"
    -H "X-CSRF-Token: ${CSRF[$1]}" -H "X-User: admin" -H "Content-Type: application/json")
  [ -n "${4:-}" ] && args+=(-d "$4")
  curl "${args[@]}" > /tmp/last_resp.json || echo '{"error":"request failed"}' > /tmp/last_resp.json
  cat /tmp/last_resp.json
}
jq_() { python3 -c "import json,sys; d=json.load(open('/tmp/last_resp.json')); print($1)"; }
share_id() { # node name
  api "$1" GET /api/shares >/dev/null
  jq_ "next((s['id'] for s in d.get('shares',[]) if s['name']=='$2'), '')"
}
create_share() { # node name comment
  api "$1" POST /api/shares "{\"action\":\"create\",\"name\":\"$2\",\"path\":\"/mnt/syncpool/data\",\"comment\":\"$3\"}" >/dev/null
}
set_comment() { # node name comment
  api "$1" POST /api/shares "{\"action\":\"update\",\"id\":$(share_id "$1" "$2"),\"comment\":\"$3\"}" >/dev/null
}
capture() { api "$1" POST /api/config/capture '{}' >/dev/null; }
sync_now() { api "$1" POST /api/config/sync/now '{}' >/dev/null; }
status_field() { api "$1" GET /api/config/sync/status >/dev/null; jq_ "d['status']['$2']"; }
smb_has() { grep -A6 "^\[$2\]" "/tmp/$1-smb.conf" 2>/dev/null | grep -q "${3:-}"; }

set +e
login a 9000; login b 9001
check "Logged in on both nodes" '[ -n "${SESSION[a]}" ] && [ -n "${SESSION[b]}" ]'

echo "::group::Pair with merge preview"
create_share a common "v1"
capture a; capture b
check "Both nodes start standalone" '[ "$(status_field a mode)" = standalone ] && [ "$(status_field b mode)" = standalone ]'
api a POST /api/config/peers/token '{}' >/dev/null
TOKEN=$(jq_ "d.get('token','')")
check "Node A issues a join code" '[[ "$TOKEN" == dpj_* ]]'
api b POST /api/config/peers/preview "{\"url\":\"http://127.0.0.1:19000\",\"token\":\"$TOKEN\"}" >/dev/null
check "Preview: share 'common' would be added on B" \
  '[ "$(jq_ "next((i[\"action\"] for i in d[\"preview\"][\"items\"] if i[\"kind\"]==\"share\" and i[\"key\"]==\"common\"), \"\")")" = add ]'
check "Preview changed nothing on B" '! smb_has b common'
api b POST /api/config/peers/join "{\"url\":\"http://127.0.0.1:19000\",\"token\":\"$TOKEN\",\"self_url\":\"http://127.0.0.1:19001\"}" >/dev/null
check "B joins A" '[ "$(jq_ "d.get(\"success\")")" = True ]'
api b POST /api/config/peers/preview "{\"url\":\"http://127.0.0.1:19000\",\"token\":\"$TOKEN\"}" >/dev/null
check "The join code works only once" '[ "$(jq_ "d.get(\"success\")")" != True ]'
sync_now b; sync_now a; sync_now b
check "Share 'common' arrived on B (smb.conf)" 'smb_has b common "comment = v1"'
check "Both nodes connected" '[ "$(status_field a mode)" = connected ] && [ "$(status_field b mode)" = connected ]'
echo "::endgroup::"

echo "::group::Partition and change both sides"
sudo iptables -I INPUT -p tcp --dport 19000 -j DROP
sudo iptables -I INPUT -p tcp --dport 19001 -j DROP
set_comment a common "changed on A"; create_share a only-a "from A"; capture a
set_comment b common "changed on B"; create_share b only-b "from B"; capture b
check "A keeps working while cut off (its smb.conf updated)" 'smb_has a only-a && smb_has a common "changed on A"'
check "B keeps working while cut off (its smb.conf updated)" 'smb_has b only-b && smb_has b common "changed on B"'
sync_now a; sync_now b
check "A reports isolated" '[ "$(status_field a mode)" = isolated ]'
check "B reports isolated with changes waiting" '[ "$(status_field b mode)" = isolated ] && [ "$(status_field b waiting)" -gt 0 ]'
echo "::endgroup::"

echo "::group::Reconnect and merge"
sudo iptables -D INPUT -p tcp --dport 19000 -j DROP
sudo iptables -D INPUT -p tcp --dport 19001 -j DROP
for _ in 1 2 3; do sync_now a; sync_now b; done
check "A received B's new share" 'smb_has a only-b "from B"'
check "B received A's new share" 'smb_has b only-a "from A"'
check "Both connected again" '[ "$(status_field a mode)" = connected ] && [ "$(status_field b mode)" = connected ]'
check "The share changed on both sides was not overwritten on A" 'smb_has a common "changed on A"'
check "The share changed on both sides was not overwritten on B" 'smb_has b common "changed on B"'
api b GET /api/config/conflicts >/dev/null
CONFLICT=$(jq_ "next((c['id'] for c in d['conflicts'] if c['kind']=='share' and c['key']=='common'), '')")
check "B shows the conflict for share 'common'" '[ -n "$CONFLICT" ]'
check "The conflict shows both comments" \
  '[ "$(jq_ "[(c[\"changes\"]) for c in d[\"conflicts\"] if c[\"key\"]==\"common\"][0]" | grep -c "changed on")" -ge 1 ]'
api a GET /api/config/conflicts >/dev/null
check "A shows the same conflict" '[ -n "$(jq_ "next((c[\"id\"] for c in d[\"conflicts\"] if c[\"key\"]==\"common\"), \"\")")" ]'
echo "::endgroup::"

echo "::group::Resolve"
api b POST "/api/config/conflicts/$CONFLICT/resolve" '{"choice":"remote"}' >/dev/null
check "Resolve on B: take A's version" '[ "$(jq_ "d.get(\"success\")")" = True ]'
check "B now has A's comment" 'smb_has b common "changed on A"'
for _ in 1 2; do sync_now a; sync_now b; done
check "A keeps its comment" 'smb_has a common "changed on A"'
api a GET /api/config/conflicts >/dev/null
check "The conflict is closed on A too" '[ -z "$(jq_ "next((c[\"id\"] for c in d[\"conflicts\"] if c[\"key\"]==\"common\"), \"\")")" ]'
api b GET /api/config/history?kind=share\&key=common >/dev/null
check "B's history records the merge" '[ "$(jq_ "d[\"revisions\"][0][\"origin\"]")" = merge ]'
echo "::endgroup::"

echo "::group::Detach"
api b POST /api/config/detach '{}' >/dev/null
check "B detaches" '[ "$(jq_ "d.get(\"success\")")" = True ]'
check "B is independent" '[ "$(status_field b mode)" = independent ]'
check "A was told and is standalone" '[ "$(status_field a mode)" = standalone ]'
check "B keeps its configuration" 'smb_has b only-a && smb_has b only-b'
echo "::endgroup::"

echo
echo "  Results: $PASS passed   $FAIL failed"
if [ "$FAIL" -gt 0 ]; then
  echo -e "  FAILED TESTS:$FAILURES"
  echo "::group::node A log"; tail -80 /tmp/dplaned-a.log; echo "::endgroup::"
  echo "::group::node B log"; tail -80 /tmp/dplaned-b.log; echo "::endgroup::"
  exit 1
fi
