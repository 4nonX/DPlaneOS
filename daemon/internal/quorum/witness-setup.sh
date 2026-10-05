#!/bin/sh
# DPlaneOS third vote (corosync-qnetd) for any Linux machine:
# a Raspberry Pi, a small VM, an existing server or a cloud instance.
#
# Shown ready to paste in System > High Availability > Add a third vote:
#   curl -fsSk https://<node>/api/quorum/witness-setup.sh | sudo sh -s -- https://<node> <code>
#
# What it does: installs corosync-qnetd with the system's package manager if
# needed, creates its certificate authority, starts the service, opens TCP
# 5403 in ufw/firewalld if one is active, then registers with the cluster
# using the one-time code (the cluster's certificate is signed here; no SSH
# or passwords are involved). Safe to run again.
set -eu

NODE="${1:-}"
CODE="${2:-}"
PORT=5403
DB=/etc/corosync/qnetd/nssdb

say() { printf '\033[1m==> %s\033[0m\n' "$*"; }
die() { printf '\033[31mError: %s\033[0m\n' "$*" >&2; exit 1; }

[ -n "$NODE" ] && [ -n "$CODE" ] || die "usage: sh witness-setup.sh https://<node> <code>"
[ "$(id -u)" = 0 ] || die "run as root (sudo)"
command -v curl >/dev/null 2>&1 || die "curl is required"

if ! command -v corosync-qnetd >/dev/null 2>&1; then
    say "Installing corosync-qnetd"
    if command -v apt-get >/dev/null 2>&1; then
        apt-get update -qq && DEBIAN_FRONTEND=noninteractive apt-get install -y -qq corosync-qnetd
    elif command -v dnf >/dev/null 2>&1; then
        dnf install -y -q corosync-qnetd
    elif command -v yum >/dev/null 2>&1; then
        yum install -y -q corosync-qnetd
    elif command -v zypper >/dev/null 2>&1; then
        zypper --non-interactive install corosync-qnetd
    else
        die "no supported package manager found; install corosync-qnetd, then run this again"
    fi
fi

if [ ! -f "$DB/qnetd-cacert.crt" ]; then
    say "Creating the certificate authority"
    corosync-qnetd-certutil -i >/dev/null
fi

say "Starting corosync-qnetd"
systemctl enable corosync-qnetd >/dev/null 2>&1 || true
systemctl restart corosync-qnetd || die "corosync-qnetd did not start (journalctl -u corosync-qnetd)"

if command -v ufw >/dev/null 2>&1 && ufw status 2>/dev/null | grep -q "Status: active"; then
    say "Opening TCP $PORT in ufw"
    ufw allow "$PORT/tcp" >/dev/null
fi
if command -v firewall-cmd >/dev/null 2>&1 && firewall-cmd --state >/dev/null 2>&1; then
    say "Opening TCP $PORT in firewalld"
    firewall-cmd --quiet --permanent --add-port="$PORT/tcp" && firewall-cmd --quiet --reload
    OPENED=1
fi
if [ -z "${OPENED:-}" ] && ! (command -v ufw >/dev/null 2>&1 && ufw status 2>/dev/null | grep -q "Status: active"); then
    if (command -v nft >/dev/null 2>&1 && nft list ruleset 2>/dev/null | grep -q "policy drop") ||
       (command -v iptables >/dev/null 2>&1 && iptables -S INPUT 2>/dev/null | grep -q "^-P INPUT DROP"); then
        say "Note: a firewall is active that this script does not manage; allow incoming TCP $PORT"
    fi
fi

# The address the cluster nodes reach this machine at: the source address of
# the route towards the node.
HOST=$(printf '%s' "$NODE" | sed -E 's#^[a-zA-Z]+://##; s#^\[##; s#[]:/].*$##')
IP=$(getent ahostsv4 "$HOST" 2>/dev/null | awk 'NR==1 {print $1}')
[ -n "$IP" ] || IP="$HOST"
ADDR=$(ip -o route get "$IP" 2>/dev/null | sed -n 's/.* src \([^ ]*\).*/\1/p' | head -n1)
[ -n "$ADDR" ] || die "cannot determine this machine's address towards $HOST"

TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT

post() { # path body-file -> prints response body, fails on HTTP errors
    code=$(curl -sSk -o "$TMP/resp" -w '%{http_code}' -X POST \
        -H "X-DPlane-Quorum-Code: $CODE" -H "Content-Type: text/plain" \
        --data-binary "@$2" "$NODE$1") || die "cannot reach $NODE"
    [ "$code" = 200 ] || die "$(cat "$TMP/resp") (HTTP $code)"
    cat "$TMP/resp"
}

say "Registering with $NODE (this machine: $ADDR)"
post /api/quorum/enroll/ca "$DB/qnetd-cacert.crt" > "$TMP/step1"
CLUSTER=$(sed -n 's/^cluster: //p' "$TMP/step1" | head -n1)
[ -n "$CLUSTER" ] || die "unexpected answer from $NODE"
# The certificate request is binary (DER) and travels base64-encoded.
sed -n '2,$p' "$TMP/step1" | base64 -d > "$TMP/node.crq" 2>/dev/null || die "invalid certificate request from $NODE"
[ -s "$TMP/node.crq" ] || die "empty certificate request from $NODE"

say "Signing the certificate of cluster $CLUSTER"
CRT="$DB/cluster-$CLUSTER.crt"
rm -f "$CRT"
# corosync-qnetd-certutil does not stop when a tool fails: check its output.
corosync-qnetd-certutil -s -c "$TMP/node.crq" -n "$CLUSTER" >"$TMP/sign.log" 2>&1 || true
[ -s "$CRT" ] || die "signing failed: $(cat "$TMP/sign.log")"
base64 "$CRT" > "$TMP/crt.b64"
post "/api/quorum/enroll/cert?address=$ADDR" "$TMP/crt.b64" > "$TMP/step2"
if grep -q '^warning:' "$TMP/step2"; then
    printf '\033[33m%s\033[0m\n' "$(sed -n 's/^warning: //p' "$TMP/step2")" >&2
fi

say "Done. This machine is now the third vote of cluster $CLUSTER."
echo "    The cluster shows it under System > High Availability within a few seconds."
echo "    Keep this machine running and reachable on TCP $PORT from all cluster nodes."
