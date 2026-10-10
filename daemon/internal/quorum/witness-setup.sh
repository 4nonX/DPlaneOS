#!/bin/sh
# DPlaneOS third vote for any Linux machine: a Raspberry Pi, a mini PC, a
# small VM, an existing server or a cloud instance.
#
# Shown ready to paste in System > High Availability > Add a third vote:
#
#   QDevice (default): runs corosync-qnetd, a lightweight vote server that
#   can serve several clusters and tolerates a slow or distant link.
#     curl -fsSk https://<node>/api/quorum/witness-setup.sh | sudo sh -s -- https://<node> <code>
#
#   Voter: runs corosync itself and becomes a full member of the cluster
#   (one vote, never owns storage). Needs a low-latency LAN link.
#     curl -fsSk https://<node>/api/quorum/witness-setup.sh | sudo sh -s -- --voter https://<node> <code>
#
# Both install their package with the system's package manager if needed,
# open the port in ufw/firewalld if one is active, and register with the
# cluster using the one-time code (no SSH, no passwords). Safe to run again.
set -eu

MODE=qdevice
if [ "${1:-}" = "--voter" ]; then MODE=voter; shift; fi
NODE="${1:-}"
CODE="${2:-}"

say() { printf '\033[1m==> %s\033[0m\n' "$*"; }
warn() { printf '\033[33m%s\033[0m\n' "$*" >&2; }
die() { printf '\033[31mError: %s\033[0m\n' "$*" >&2; exit 1; }

[ -n "$NODE" ] && [ -n "$CODE" ] || die "usage: sh witness-setup.sh [--voter] https://<node> <code>"
[ "$(id -u)" = 0 ] || die "run as root (sudo)"
command -v curl >/dev/null 2>&1 || die "curl is required"

install_pkg() { # package command
    command -v "$2" >/dev/null 2>&1 && return 0
    say "Installing $1"
    if command -v apt-get >/dev/null 2>&1; then
        apt-get update -qq && DEBIAN_FRONTEND=noninteractive apt-get install -y -qq "$1"
    elif command -v dnf >/dev/null 2>&1; then
        dnf install -y -q "$1"
    elif command -v yum >/dev/null 2>&1; then
        yum install -y -q "$1"
    elif command -v zypper >/dev/null 2>&1; then
        zypper --non-interactive install "$1"
    elif command -v apk >/dev/null 2>&1; then
        apk add --quiet "$1"
    else
        die "no supported package manager found; install $1, then run this again"
    fi
    command -v "$2" >/dev/null 2>&1 || die "$1 was installed but $2 is not available"
}

firewall_open() { # port/proto
    if command -v ufw >/dev/null 2>&1 && ufw status 2>/dev/null | grep -q "Status: active"; then
        say "Opening $1 in ufw"
        ufw allow "$1" >/dev/null
        return
    fi
    if command -v firewall-cmd >/dev/null 2>&1 && firewall-cmd --state >/dev/null 2>&1; then
        say "Opening $1 in firewalld"
        firewall-cmd --quiet --permanent --add-port="$1" && firewall-cmd --quiet --reload
        return
    fi
    if (command -v nft >/dev/null 2>&1 && nft list ruleset 2>/dev/null | grep -q "policy drop") ||
       (command -v iptables >/dev/null 2>&1 && iptables -S INPUT 2>/dev/null | grep -q "^-P INPUT DROP"); then
        warn "Note: a firewall is active that this script does not manage; allow incoming $1"
    fi
}

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

# ── Voter: a corosync member ──────────────────────────────────────────────────
if [ "$MODE" = voter ]; then
    install_pkg corosync corosync
    command -v corosync-cfgtool >/dev/null 2>&1 || die "corosync-cfgtool is missing"
    if ! corosync -v 2>/dev/null | grep -q "version '3"; then
        warn "corosync $(corosync -v 2>/dev/null | sed -n "s/.*version '\([^']*\)'.*/\1/p") found; the cluster runs corosync 3 (knet). Use Debian 11+, Ubuntu 22.04+, Raspberry Pi OS 11+ or Fedora 34+."
    fi
    NAME=$(hostname -s | tr '[:upper:]' '[:lower:]' | tr -c 'a-z0-9_\n-' '-' | cut -c1-31)

    # Pin the node's TLS key for later pulls (TOFU, protected by the code now).
    PIN=""
    case "$NODE" in
        https://*)
            if command -v openssl >/dev/null 2>&1; then
                HP=$(printf '%s' "$NODE" | sed -E 's#^https://##; s#/.*$##')
                case "$HP" in *:*) ;; *) HP="$HP:443" ;; esac
                PIN=$(openssl s_client -connect "$HP" </dev/null 2>/dev/null | openssl x509 -pubkey -noout 2>/dev/null |
                    openssl pkey -pubin -outform der 2>/dev/null | openssl dgst -sha256 -binary | base64)
            fi
            [ -n "$PIN" ] || warn "Cannot pin the node's TLS key (openssl missing); the voter trusts any certificate when it pulls."
            ;;
        *) warn "The node is reached over plain HTTP: the voter's configuration (with the cluster key) travels unencrypted. Prefer https:// on an untrusted network." ;;
    esac

    say "Joining the cluster at $NODE as voter $NAME ($ADDR)"
    : > "$TMP/empty"
    post "/api/quorum/voter/join?name=$NAME&address=$ADDR" "$TMP/empty" > "$TMP/join"
    CLUSTER=$(sed -n 's/^cluster: //p' "$TMP/join" | head -n1)
    TOKEN=$(sed -n 's/^token: //p' "$TMP/join" | head -n1)
    [ -n "$CLUSTER" ] && [ -n "$TOKEN" ] || die "unexpected answer from $NODE"

    install -d -m 0755 /etc/corosync
    sed -n 's/^authkey: //p' "$TMP/join" | base64 -d > "$TMP/authkey" || die "invalid cluster key from $NODE"
    install -m 0400 "$TMP/authkey" /etc/corosync/authkey
    sed '1,/^---$/d' "$TMP/join" > /etc/corosync/corosync.conf
    umask 077
    cat > /etc/dplaneos-voter.conf <<EOF
URL='$NODE'
NAME='$NAME'
TOKEN='$TOKEN'
PIN='$PIN'
EOF
    umask 022

    # Pull the configuration every minute (nodes added or removed, key changes).
    SYNC=/usr/local/sbin/dplaneos-voter-sync
    mkdir -p /usr/local/sbin 2>/dev/null || SYNC=/var/lib/dplaneos-voter/sync
    mkdir -p "$(dirname "$SYNC")"
    cat > "$SYNC" <<'EOF'
#!/bin/sh
# Pulls this voter's corosync configuration from the DPlaneOS node it joined.
set -eu
. /etc/dplaneos-voter.conf
T=$(mktemp -d); trap 'rm -rf "$T"' EXIT
if [ -n "$PIN" ]; then set -- --pinnedpubkey "sha256//$PIN"; else set --; fi
curl -fsSk --max-time 20 "$@" -H "X-DPlane-Voter: $NAME" -H "X-DPlane-Voter-Token: $TOKEN" \
    "$URL/api/quorum/voter/config" -o "$T/cfg" || exit 0
grep -q '^---$' "$T/cfg" || exit 0
sed '1,/^---$/d' "$T/cfg" > "$T/conf"
sed -n 's/^authkey: //p' "$T/cfg" | base64 -d > "$T/key" 2>/dev/null || exit 0
[ -s "$T/conf" ] && [ -s "$T/key" ] || exit 0
if ! cmp -s "$T/key" /etc/corosync/authkey; then
    install -m 0400 "$T/key" /etc/corosync/authkey
    install -m 0644 "$T/conf" /etc/corosync/corosync.conf
    logger -t dplaneos-voter "cluster key changed: restarting corosync"
    systemctl restart corosync
elif ! cmp -s "$T/conf" /etc/corosync/corosync.conf; then
    install -m 0644 "$T/conf" /etc/corosync/corosync.conf
    logger -t dplaneos-voter "cluster configuration changed: reloading corosync"
    corosync-cfgtool -R >/dev/null || systemctl restart corosync
fi
EOF
    chmod 0755 "$SYNC"
    UNITS=/etc/systemd/system
    if ! touch "$UNITS/.dplaneos-probe" 2>/dev/null; then
        UNITS=/run/systemd/system
        warn "$UNITS is used for the sync timer (/etc/systemd/system is read-only here): it does not survive a reboot."
    fi
    rm -f /etc/systemd/system/.dplaneos-probe
    cat > "$UNITS/dplaneos-voter-sync.service" <<'EOF'
[Unit]
Description=DPlaneOS voter: pull the cluster configuration
After=network-online.target
[Service]
Type=oneshot
ExecStart=@SYNC@
EOF
    sed -i "s#@SYNC@#$SYNC#" "$UNITS/dplaneos-voter-sync.service"
    cat > "$UNITS/dplaneos-voter-sync.timer" <<'EOF'
[Unit]
Description=DPlaneOS voter: pull the cluster configuration every minute
[Timer]
OnBootSec=30s
OnUnitActiveSec=60s
[Install]
WantedBy=timers.target
EOF

    firewall_open 5405/udp
    say "Starting corosync"
    systemctl daemon-reload
    systemctl enable corosync >/dev/null 2>&1 || true
    systemctl restart corosync || die "corosync did not start (journalctl -u corosync)"
    systemctl enable --now dplaneos-voter-sync.timer >/dev/null

    say "Done. This machine is now a voter of cluster $CLUSTER."
    echo "    The cluster shows it under System > High Availability within a few seconds."
    echo "    Keep it running on the same network as the nodes (UDP 5405, low latency)."
    exit 0
fi

# ── QDevice: corosync-qnetd ───────────────────────────────────────────────────
PORT=5403
DB=/etc/corosync/qnetd/nssdb
install_pkg corosync-qnetd corosync-qnetd

if [ ! -f "$DB/qnetd-cacert.crt" ]; then
    say "Creating the certificate authority"
    corosync-qnetd-certutil -i >/dev/null
fi

# Started, not restarted: a running qnetd may already serve other clusters,
# and a restart would drop their vote for a moment.
systemctl enable corosync-qnetd >/dev/null 2>&1 || true
if ! systemctl is-active --quiet corosync-qnetd; then
    say "Starting corosync-qnetd"
    systemctl start corosync-qnetd || die "corosync-qnetd did not start (journalctl -u corosync-qnetd)"
fi
firewall_open "$PORT/tcp"

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
    warn "$(sed -n 's/^warning: //p' "$TMP/step2")"
fi

say "Done. This machine is now the third vote of cluster $CLUSTER."
echo "    The cluster shows it under System > High Availability within a few seconds."
echo "    Keep this machine running and reachable on TCP $PORT from all cluster nodes."
