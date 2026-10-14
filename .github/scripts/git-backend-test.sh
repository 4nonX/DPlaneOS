#!/bin/bash
# Design 0001, phase 5 exit test: the Git backend against a real Git server
# (Gitea, the same API as Forgejo), running as a service container.
#
#   direct policy: a change in the web UI is committed to the branch
#   a change made in the repository waits for approval, then is applied
#   the result of the apply is reported as a commit status
#   pull-request policy: a change opens a pull request; merged, it counts
#   the empty-state guard holds a commit that removes every user; Revert
#   writes the last good version back
#
# Needs: GITEA_CLI (the gitea command, e.g. "docker exec -u git <container> gitea"),
# DATABASE_DSN, ./dplaned-ci.
set -e
echo 'Defaults env_keep += "PATH"' | sudo tee /etc/sudoers.d/ci-path >/dev/null
export PATH="/usr/sbin:/usr/bin:/sbin:/bin:$PATH"
: "${DATABASE_DSN:?DATABASE_DSN not set}"
: "${GITEA_CLI:?GITEA_CLI not set}"
GITEA=http://localhost:3000
GIT_USER=ci
GIT_PASS='CiGit-Pass-12345'
CI_PASS="CiAdmin1!Test"

PASS=0; FAIL=0; FAILURES=""
ok()   { printf "  \033[32m✓\033[0m %s\n" "$1"; PASS=$((PASS+1)); }
fail() { FAIL=$((FAIL+1)); FAILURES="$FAILURES\n  ✗ $1"; echo "  ✗ $1 (last response: $(head -c 400 /tmp/last_resp.json 2>/dev/null))"; }
check() { if eval "$2"; then ok "$1"; else fail "$1"; fi; }
# py EXPR: evaluate a Python expression over the last response (d).
py() { python3 -c "import json; d = json.load(open('/tmp/last_resp.json')); print($1)"; }

echo "--- Gitea: admin user, token, repository ---"
for _ in $(seq 1 60); do curl -sf "$GITEA/api/v1/version" >/dev/null && break; sleep 2; done
$GITEA_CLI admin user create --admin --username "$GIT_USER" --password "$GIT_PASS" \
  --email ci@example.lan --must-change-password=false >/dev/null
TOKEN=$(curl -sf -u "$GIT_USER:$GIT_PASS" -X POST "$GITEA/api/v1/users/$GIT_USER/tokens" -H 'Content-Type: application/json' \
  -d '{"name":"dplaneos","scopes":["write:repository","write:user","write:issue"]}' | python3 -c "import sys,json; print(json.load(sys.stdin)['sha1'])")
curl -sf -H "Authorization: token $TOKEN" -X POST "$GITEA/api/v1/user/repos" -H 'Content-Type: application/json' \
  -d '{"name":"nas","auto_init":true,"default_branch":"main"}' >/dev/null

gitea() { # method path [data]
  local args=(-s -X "$1" "$GITEA/api/v1$2" -H "Authorization: token $TOKEN" -H 'Content-Type: application/json')
  [ -n "${3:-}" ] && args+=(-d "$3")
  curl "${args[@]}" > /tmp/last_resp.json
}
gitea_file() { # path: the file on main ("" if absent)
  gitea GET "/repos/$GIT_USER/nas/contents/$1?ref=main"
  py "__import__('base64').b64decode(d['content']).decode() if 'content' in d else ''"
}
gitea_put() { # path content message: commit a file on main as someone else would; prints the commit
  gitea GET "/repos/$GIT_USER/nas/contents/$1?ref=main"
  local sha body
  sha=$(py "d.get('sha', '')")
  body=$(python3 -c "import json, base64, sys; print(json.dumps({'content': base64.b64encode(sys.argv[1].encode()).decode(), 'sha': sys.argv[2], 'message': sys.argv[3], 'branch': 'main'}))" "$2" "$sha" "$3")
  gitea PUT "/repos/$GIT_USER/nas/contents/$1" "$body"
  py "d['commit']['sha']"
}

echo "--- dplaned ---"
printf 'version: "6"\n' > /tmp/git-state.yaml
sudo ./dplaned-ci --listen "127.0.0.1:9000" --db-dsn "$DATABASE_DSN" --gitops-state /tmp/git-state.yaml \
  --smb-conf /tmp/git-smb.conf -secrets-key /tmp/git-secrets.key > /tmp/dplaned-git.log 2>&1 &
PID=$!
trap 'sudo kill $PID 2>/dev/null || true' EXIT
for _ in $(seq 1 40); do curl -s http://127.0.0.1:9000/health >/dev/null && break; sleep 0.5; done
CI_HASH=$(python3 -c "import bcrypt; print(bcrypt.hashpw(b'$CI_PASS', bcrypt.gensalt(rounds=10)).decode())")
psql "$DATABASE_DSN" -c \
  "UPDATE users SET password_hash='$CI_HASH', active=1, role='admin', must_change_password=0 WHERE username='admin';" >/dev/null
SESSION=$(curl -s -X POST http://127.0.0.1:9000/api/auth/login -H 'Content-Type: application/json' \
  -d "{\"username\":\"admin\",\"password\":\"$CI_PASS\"}" | python3 -c "import sys,json; print(json.load(sys.stdin)['session_id'])")
CSRF=$(curl -s http://127.0.0.1:9000/api/csrf -H "X-Session-ID: $SESSION" | python3 -c "import sys,json; print(json.load(sys.stdin)['csrf_token'])")

api() { # method path [data]
  local args=(-s --max-time 120 -X "$1" "http://127.0.0.1:9000$2" -H "X-Session-ID: $SESSION" -H "X-CSRF-Token: $CSRF"
    -H "X-User: admin" -H "Content-Type: application/json")
  [ -n "${3:-}" ] && args+=(-d "$3")
  curl "${args[@]}" > /tmp/last_resp.json || echo '{"error":"request failed"}' > /tmp/last_resp.json
}
succeeded() { [ "$(py "d.get('success')")" = "True" ]; }
settings() { # policy auto_apply prune
  python3 - "$@" <<PY
import json, sys
print(json.dumps({'enabled': True, 'branch': 'main', 'layout': 'single', 'state_path': 'state.yaml',
  'remotes': [{'url': '$GITEA/$GIT_USER/nas.git', 'fetch_cred': 'forge', 'push_cred': 'forge'}],
  'categories': {'identity': True}, 'policy': sys.argv[1], 'provider': 'gitea',
  'provider_api_url': '$GITEA/api/v1', 'provider_repo': '$GIT_USER/nas', 'provider_cred': 'forge',
  'auto_apply': sys.argv[2] == 'true', 'prune': sys.argv[3] == 'true', 'interval_secs': 3600}))
PY
}
user_field() { # username field
  api GET /api/rbac/users
  py "next((u['$2'] for u in d['users'] if u['username'] == '$1'), '')"
}
sync_now() { api POST /api/config/capture '{}'; api POST /api/gitops/sync '{}'; }
SCOPE="next(x for x in d['status']['scopes'] if x['scope'] == 'all')"
scope_counts() { api GET /api/gitops/status; py "' '.join(str(len(($SCOPE)[k])) for k in ('push', 'pull', 'conflicts'))"; }
scope_pull_ids() { api GET /api/gitops/status; py "','.join(c['id'] for c in ($SCOPE)['pull'])"; }
hold_reason() { api GET /api/gitops/status; py "next((h['reason'] for h in d['holds'] if h['scope'] == 'all'), '')"; }

echo "--- 1. Direct policy: a change in the web UI is committed ---"
api POST /api/git-sync/credentials "{\"name\":\"forge\",\"host\":\"localhost\",\"auth_type\":\"token\",\"username\":\"$GIT_USER\",\"token\":\"$TOKEN\"}"
api PUT /api/gitops/settings "$(settings direct false false)"
check "Git settings saved" 'succeeded'
api POST /api/rbac/users "{\"action\":\"create\",\"username\":\"bob\",\"email\":\"bob@start.lan\",\"password\":\"Bob-Pass-12345\",\"role\":\"user\",\"confirm_password\":\"$CI_PASS\"}"
sync_now
FILE=$(gitea_file state.yaml)
check "state.yaml on the branch lists bob" 'echo "$FILE" | grep -q "username: \"bob\""'
check "the password is a reference, never the hash" 'echo "$FILE" | grep -q "secret://user/bob/password" && ! echo "$FILE" | grep -q "[$]2[aby][$]"'
COUNTS=$(scope_counts)
check "the scope is in step" '[ "$COUNTS" = "0 0 0" ]'

echo "--- 2. A change made in the repository waits for approval ---"
EDIT=$(gitea_put state.yaml "$(echo "$FILE" | sed 's/bob@start.lan/bob@repo.lan/')" "bob's address changed in the repository")
sync_now
PULL=$(scope_pull_ids)
check "the change waits (automatic apply is off)" '[ "$PULL" = "user/bob" ]'
EMAIL=$(user_field bob email)
check "bob is not changed yet" '[ "$EMAIL" = "bob@start.lan" ]'
api POST /api/gitops/approve "{\"scope\":\"all\",\"sha\":\"$EDIT\"}"
check "approved: applied" 'succeeded'
EMAIL=$(user_field bob email)
check "bob has the repository's address" '[ "$EMAIL" = "bob@repo.lan" ]'
gitea GET "/repos/$GIT_USER/nas/commits/$EDIT/statuses"
STATUS=$(py "next((s['status'] for s in d if s['context'] == 'dplaneos'), '')")
check "the commit shows the apply as a status" '[ "$STATUS" = "success" ]'

echo "--- 3. Pull-request policy ---"
api PUT /api/gitops/settings "$(settings pull_request false false)"
api GET /api/rbac/users
BOB_ID=$(py "next(u['id'] for u in d['users'] if u['username'] == 'bob')")
api POST /api/rbac/users "{\"action\":\"update\",\"id\":$BOB_ID,\"email\":\"bob@pr.lan\"}"
sync_now
gitea GET "/repos/$GIT_USER/nas/pulls?state=open"
PR=$(py "next((p['number'] for p in d if p['head']['ref'] == 'dplaneos/state'), '')")
check "a pull request from dplaneos/state is open" '[ -n "$PR" ]'
FILE=$(gitea_file state.yaml)
check "the branch itself is unchanged" '! echo "$FILE" | grep -q "bob@pr.lan"'
gitea POST "/repos/$GIT_USER/nas/pulls/$PR/merge" '{"Do":"merge"}'
sync_now
FILE=$(gitea_file state.yaml)
check "after the merge the branch has the change" 'echo "$FILE" | grep -q "bob@pr.lan"'
COUNTS=$(scope_counts)
check "and the scope is in step" '[ "$COUNTS" = "0 0 0" ]'

echo "--- 4. Empty-state guard and Revert ---"
api PUT /api/gitops/settings "$(settings direct true true)"
gitea_put state.yaml 'version: "1"' "remove everything" >/dev/null
sync_now
REASON=$(hold_reason)
check "the scope is on hold (empty-state guard)" 'echo "$REASON" | grep -q "empty-state guard"'
EMAIL=$(user_field bob email)
check "no user was deleted" '[ "$EMAIL" = "bob@pr.lan" ]'
api POST /api/gitops/holds/all/revert '{}'
check "Revert succeeded" 'succeeded'
FILE=$(gitea_file state.yaml)
check "the branch has the users again" 'echo "$FILE" | grep -q "username: \"bob\""'
REASON=$(hold_reason)
check "the hold is cleared" '[ -z "$REASON" ]'

echo ""
echo "Git backend: $PASS passed, $FAIL failed"
if [ "$FAIL" -gt 0 ]; then
  echo -e "Failures:$FAILURES"
  echo "--- dplaned log ---"; grep -i -E "git|gitops" /tmp/dplaned-git.log | tail -n 80
  exit 1
fi
