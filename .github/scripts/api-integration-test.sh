#!/bin/bash
set -e

BASE="http://127.0.0.1:9000"
PASS=0; FAIL=0; FAILURES=""

# Helper functions
ok()   { echo "  ✓ $1"; PASS=$((PASS+1)); }
fail() { echo "  ✗ $1"; FAIL=$((FAIL+1)); FAILURES="${FAILURES}\n  ✗ $1"; }

assert_json() {
  local label="$1" resp="$2" key="$3" expected="${4:-}"
  if echo "$resp" | python3 -c "
import sys,json
try: d=json.load(sys.stdin)
except: sys.exit(1)
val=d
for k in '$key'.split('.'): val=val.get(k) if isinstance(val,dict) else None
sys.exit(0 if (str(val).lower()=='$expected'.lower() if '$expected' else bool(val)) else 1)
" 2>/dev/null; then ok "$label"; else fail "$label  (got: $(echo "$resp" | head -c 200))"; fi
}

assert_shape() {
  local label="$1" resp="$2" arr_key="$3"; shift 3
  local required_keys=("$@")
  echo "$resp" | python3 -c "
import sys, json
try:
  d = json.load(sys.stdin)
except:
  print('JSON parse failed'); sys.exit(1)
if not d.get('success'):
  print('success!=true, error=' + str(d.get('error',''))); sys.exit(1)
arr = d.get('$arr_key')
if not isinstance(arr, list):
  print('$arr_key is not a list, got: ' + type(arr).__name__); sys.exit(1)
if len(arr) == 0:
  sys.exit(0)
first = arr[0]
missing = [k for k in $(printf "'%s'," "${required_keys[@]}" | sed 's/,$//' | sed \"s/'[^']*'/&/g\" | python3 -c \"import sys; parts=sys.stdin.read().split(','); print('[' + ','.join(repr(p.strip('\\\"\\'')) for p in parts if p.strip()) + ']')\") if k not in first]
if missing:
  print('first element missing keys: ' + str(missing)); sys.exit(1)
" 2>/dev/null && ok "$label" || fail "$label  (got: $(echo "$resp" | head -c 200))"
}

assert_array() {
  local label="$1" resp="$2" key="$3"
  echo "$resp" | python3 -c "
import sys,json
try: d=json.load(sys.stdin)
except: sys.exit(1)
val=d
for k in '$key'.split('.'): val=val.get(k) if isinstance(val,dict) else None
sys.exit(0 if isinstance(val,list) else 1)
" 2>/dev/null && ok "$label" || fail "$label  (expected array, got: $(echo "$resp" | python3 -c \"import sys,json; d=json.load(sys.stdin); print(type(d.get('$key')).__name__)\" 2>/dev/null))"
}

api() {
  local method="$1" path="$2" body="${3:-}"
  local args=(-sf --max-time 15 -X "$method" "$BASE$path"
              -H "X-Session-ID: $SESSION" -H "X-User: admin"
              -H "Content-Type: application/json")
  [ -n "$body" ] && args+=(-d "$body")
  curl "${args[@]}" 2>/dev/null || echo '{"_err":true}'
}

# ── 1. PRE-AUTH ──────
CODE=$(curl -s -o /dev/null -w "%{http_code}" "$BASE/api/system/status")
[ "$CODE" = "200" ] && ok "GET /api/system/status" || fail "GET /api/system/status ($CODE)"
CODE=$(curl -s -o /dev/null -w "%{http_code}" -X POST "$BASE/api/system/setup-admin" -H "Content-Type: application/json" -d "{\"username\":\"admin\",\"password\":\"$CI_PASS\"}")
[ "$CODE" = "200" ] || [ "$CODE" = "403" ] && ok "POST /api/system/setup-admin" || fail "POST /api/system/setup-admin ($CODE)"
CODE=$(curl -s -o /dev/null -w "%{http_code}" -X POST "$BASE/api/system/setup-complete" -H "Content-Type: application/json" -d '{}')
[ "$CODE" = "200" ] || [ "$CODE" = "403" ] && ok "POST /api/system/setup-complete" || fail "POST /api/system/setup-complete ($CODE)"
CODE=$(curl -s -o /dev/null -w "%{http_code}" "$BASE/health"); [ "$CODE" = "200" ] && ok "GET /health" || fail "GET /health ($CODE)"

# ── 2. AUTH ──────
LOGIN_HTTP=$(curl -s -w "\n%{http_code}" -X POST $BASE/api/auth/login -H "Content-Type: application/json" -d "{\"username\":\"admin\",\"password\":\"$CI_PASS\"}")
LOGIN=$(echo "$LOGIN_HTTP" | sed '$d')
SESSION=$(echo "$LOGIN" | python3 -c "import sys,json; print(json.load(sys.stdin).get('session_id',''))" 2>/dev/null)
[ -n "$SESSION" ] && ok "Login succeeds" || fail "Login failed"

# ── 3. ZFS ──────
POOLS=$(api GET /api/zfs/pools)
assert_json "ZFS pools success" "$POOLS" "success" "true"
assert_array "ZFS pools: data is array" "$POOLS" "data"
api POST /api/zfs/datasets '{"name":"testpool/ci-test","compression":"lz4"}' > /dev/null
sudo zfs list testpool/ci-test &>/dev/null && ok "Dataset exists after create" || fail "Dataset missing"

# ── 4. SMB ──────
api POST /api/shares '{"action":"create","name":"ci-share","path":"/tmp","read_only":true,"guest_ok":false}' > /dev/null
SHARES=$(api GET /api/shares)
echo "$SHARES" | grep -q "ci-share" && ok "SMB share created" || fail "SMB share missing"

# ── 5. FILE MANAGER ──────
api POST /api/files/write '{"path":"/tmp/ci-hello.txt","content":"hello ci"}' > /dev/null
READ=$(api GET '/api/files/read?path=/tmp/ci-hello.txt')
echo "$READ" | grep -q "hello ci" && ok "File write/read round-trip" || fail "File read-back mismatch"

# ── 6. V6 FEATURES ──────
AUDIT_VERIFY=$(api GET /api/system/audit/verify-chain)
assert_json "Audit chain verified" "$AUDIT_VERIFY" "valid" "true"

# ── LOGOUT ──────
api POST /api/auth/logout > /dev/null
AUTHED=$(curl -sf $BASE/api/auth/check -H "X-Session-ID: $SESSION" | python3 -c "import sys,json; print(json.load(sys.stdin).get('authenticated'))" 2>/dev/null)
[ "$AUTHED" = "False" ] || [ "$AUTHED" = "false" ] && ok "Session dead after logout" || fail "Session still valid"

# ── SUMMARY ──────
echo "Results: ✓ $PASS passed   ✗ $FAIL failed"
if [ "$FAIL" -gt 0 ]; then printf "Failures:$FAILURES\n"; exit 1; fi
