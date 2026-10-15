# nixos/tests/hardening-cluster.nix
# ─────────────────────────────────────────────────────────────────────────────
# Design 0001 phase 7: a cluster of three DPlaneOS nodes (three votes, no
# witness) with a shared storage group, under slow network, a minority split
# and power loss of the node that is coordinator and owner (section 5.11).
#
#   a, b, c  DPlaneOS nodes on a shared disk, softdog watchdog
#
#   0. c is added to the cluster through a: a introduces it to b, so every
#      member is paired with every other.
#   1. Slow network (200 ms ± 50 ms, 3 % loss): configuration still reaches
#      every node; no takeover.
#   2. c is cut off (a minority of one): a and b keep quorum, c loses it;
#      both sides keep accepting changes; no takeover; after the split every
#      node has every change.
#   3. a, coordinator and owner, loses power: b becomes coordinator, takes the
#      group over after the fencing delay (b is the first candidate) and c
#      learns the new owner and b's changes directly from b.
#   4. a comes back: it follows the new epoch, coordinates again (lowest node
#      id) and catches up.
#
# Run: nix build .#checks.x86_64-linux.hardening-cluster -L
# ─────────────────────────────────────────────────────────────────────────────
{ nixpkgs, system ? "x86_64-linux", impermanence, daemonPackage, frontendPackage, ... }:

let
  pkgs = nixpkgs.legacyPackages.${system};
  sharedImage = "/tmp/dplaneos-hardening-cluster-disk.img";
  watchdogTimeout = 60;

  dplaneNode = hostName: hostId: { lib, ... }: {
    imports = [
      ../configuration-live.nix
      impermanence.nixosModules.impermanence
      ../module.nix
    ];
    services.dplaneos = {
      enable = true;
      inherit daemonPackage frontendPackage;
      dbPath = "/var/lib/dplaneos/pgsql";
    };
    networking.hostName = lib.mkForce hostName;
    networking.hostId   = lib.mkForce hostId;
    boot.kernelParams = [ ];
    boot.kernelModules = [ "softdog" "sch_netem" ];
    environment.systemPackages = [ pkgs.iproute2 ];
    systemd.services.dplane-zfs-auto-import.enable = lib.mkForce false;
    systemd.services.systemd-random-seed.enable = false;
    virtualisation.cores = 2;
    virtualisation.memorySize = 2048;
    virtualisation.qemu.options = [
      "-drive file=${sharedImage},if=none,id=shared,format=raw,file.locking=off,cache=none"
      "-device virtio-blk-pci,drive=shared,share-rw=on,serial=dplshared"
    ];
  };
in

pkgs.testers.nixosTest {
  name = "dplaneos-hardening-cluster";

  nodes.a = dplaneNode "a" "aaaa0001";
  nodes.b = dplaneNode "b" "bbbb0002";
  nodes.c = dplaneNode "c" "cccc0003";

  testScript = ''
    import json, shlex, time

    with open("${sharedImage}", "wb") as f:
        f.truncate(1 << 30)

    PASSWORD = "Hardening-Pass-1"
    NODES = (a, b, c)
    start_all()

    def up(m):
        m.wait_for_unit("dplaned.service")
        m.wait_until_succeeds("curl -sf http://localhost/api/system/status >/dev/null", timeout=180)

    for m in NODES:
        up(m)

    session = {}

    def api(m, method, path, body=None, auth=True):
        cmd = f"curl -s --max-time 180 -X {method} http://localhost{path} -H 'Content-Type: application/json'"
        if auth:
            sid, csrf = session[m.name]
            cmd += f" -H 'X-Session-ID: {sid}' -H 'X-User: admin' -H 'X-CSRF-Token: {csrf}'"
        if body is not None:
            cmd += " -d " + shlex.quote(json.dumps(body))
        out = m.succeed(cmd)
        try:
            return json.loads(out)
        except Exception:
            raise Exception(f"{m.name} {method} {path}: not JSON: {out[:500]}")

    def ok(resp, what):
        if resp.get("success") is not True:
            raise Exception(f"{what}: {resp}")
        return resp

    def login(m):
        api(m, "POST", "/api/system/setup-admin", {"username": "admin", "password": PASSWORD}, auth=False)
        sid = api(m, "POST", "/api/auth/login", {"username": "admin", "password": PASSWORD}, auth=False)["session_id"]
        csrf = json.loads(m.succeed(f"curl -s http://localhost/api/csrf -H 'X-Session-ID: {sid}' -H 'X-User: admin'"))["csrf_token"]
        session[m.name] = (sid, csrf)

    def wait_for(fn, what, timeout):
        deadline = time.time() + timeout
        while True:
            v = fn()
            if v:
                return v
            assert time.time() < deadline, f"timed out: {what}"
            time.sleep(3)

    def group(m):
        gs = api(m, "GET", "/api/groups")["groups"]
        return gs[0] if gs else None

    def sync(*ms):
        for m in ms:
            api(m, "POST", "/api/config/sync/now", {})

    def status(m):
        return api(m, "GET", "/api/config/sync/status")["status"]

    def has_user(m, name):
        return any(u["username"] == name for u in api(m, "GET", "/api/rbac/users")["users"])

    def create_user(m, name):
        ok(api(m, "POST", "/api/rbac/users", {"action": "create", "username": name, "email": f"{name}@test.lan", "password": "User-Pass-12345",
                                              "role": "user", "confirm_password": PASSWORD}), f"{m.name}: create {name}")

    def everywhere(name, nodes, timeout):
        wait_for(lambda: (sync(*nodes), all(has_user(m, name) for m in nodes))[1], f"{name} on {[m.name for m in nodes]}", timeout)

    def quorate(m):
        return m.execute("(corosync-quorumtool -s || true) | grep -q 'Quorate: *Yes'")[0] == 0

    def watch_no_takeover(seconds, why):
        end = time.time() + seconds
        while time.time() < end:
            for m in (b, c):
                fo = (group(m) or {}).get("failover", {})
                assert fo.get("action") != "takeover", (why, m.name, fo)
                m.fail("zpool list tank")
            time.sleep(5)

    def addrs(m, of):
        # Every address of a host, IPv4 and IPv6 (names resolve to both).
        out = m.succeed(f"getent ahosts {of} | cut -d' ' -f1 | sort -u").split()
        return [a for a in out if a]

    def drop(m, addresses, on=True):
        op = "-I" if on else "-D"
        for ip in addresses:
            tool = "ip6tables" if ":" in ip else "iptables"
            m.succeed(f"{tool} {op} INPUT -s {ip} -j DROP && {tool} {op} OUTPUT -d {ip} -j DROP")

    def cut(m, others):
        ips = [ip for o in others for ip in addrs(m, o.name)]
        drop(m, ips)
        return ips

    def heal(m, ips):
        drop(m, ips, on=False)

    def settle_conflicts(nodes):
        # Each node set up its own admin: keep a's.
        def conflicts():
            sync(*nodes)
            return [(m, x) for m in nodes for x in api(m, "GET", "/api/config/conflicts")["conflicts"]]
        for m, x in wait_for(conflicts, "the admin conflicts from pairing", 90):
            assert (x["kind"], x["key"]) == ("user", "admin"), x
            # An apply may be running on that node (another resolution): try again.
            wait_for(lambda: api(m, "POST", f"/api/config/conflicts/{x['id']}/resolve", {"choice": "local" if m is a else "remote"}).get("success"),
                     f"{m.name}: keep a's admin", 60)
        wait_for(lambda: not conflicts(), "no conflict after pairing", 90)

    def diag():
        for m in NODES:
            print(m.execute("journalctl --no-pager -u dplaned | grep -i -E 'group|watchdog|zpool|failover|quorum|sync|introduc|conflict|error' | grep -v -E ' (GET|POST) ' | tail -n 60")[1])
            print(m.execute("(corosync-quorumtool -s || true); zpool status 2>&1 | head -5")[1])

    try:
        with subtest("0. Three nodes: pair b and c with a, cluster, c introduced to b"):
            for m in NODES:
                login(m)
                m.succeed("sudo -u postgres psql -d dplaneos -c \"INSERT INTO ha_watchdog_config (id, enable, device, timeout_secs, pet_interval_sec) VALUES (1, true, '/dev/watchdog', ${toString watchdogTimeout}, 5) ON CONFLICT (id) DO UPDATE SET enable = true, timeout_secs = ${toString watchdogTimeout}, pet_interval_sec = 5\"")
                m.succeed("systemctl restart dplaned")
                up(m)
                m.wait_until_succeeds("journalctl -u dplaned | grep -q 'HA WATCHDOG: opened'", timeout=60)
                login(m)
            for m in (b, c):
                token = ok(api(a, "POST", "/api/config/peers/token", {}), "join code")["token"]
                ok(api(m, "POST", "/api/config/peers/join", {"url": "http://a", "token": token, "self_url": f"http://{m.name}"}), f"{m.name} joins a")
            settle_conflicts(NODES)
            for m in (b, c):
                login(m)
            peers = {p["name"]: p["id"] for p in status(a)["peers"]}
            node_a = status(a)["node_id"]
            s = api(a, "GET", f"/api/quorum/suggest?peer_id={peers['b']}")
            ok(api(a, "POST", "/api/quorum/cluster", {"peer_id": peers["b"], "local_addr": s["local_addr"], "peer_addr": s["peer_addr"]}), "form cluster")
            s = api(a, "GET", f"/api/quorum/suggest?peer_id={peers['c']}")
            r = ok(api(a, "POST", "/api/quorum/nodes", {"peer_id": peers["c"], "peer_addr": s["peer_addr"]}), "add c")
            assert not r.get("warnings"), r
            c.wait_until_succeeds("systemctl is-active dplaneos-corosync", timeout=60)
            for m in NODES:
                m.wait_until_succeeds("(corosync-quorumtool -s || true) | grep -q 'Total votes: *3'", timeout=120)
            assert {p["name"] for p in status(b)["peers"]} == {"a", "c"}, status(b)["peers"]
            assert {p["name"] for p in status(c)["peers"]} == {"a", "b"}, status(c)["peers"]
            for m in NODES:
                wait_for(lambda: api(m, "GET", "/api/ha/protection")["auto_failover"], f"{m.name}: automatic failover on", 120)
            a.succeed("zpool create -f -o multihost=on -m /mnt/tank tank /dev/disk/by-id/virtio-dplshared")
            a.succeed("zfs create tank/data && echo hello > /mnt/tank/data/file")
            ok(api(a, "POST", "/api/groups", {"name": "data", "topology": "shared", "pools": ["tank"], "candidates": [peers["b"], peers["c"]]}), "create group")
            for m in (b, c):
                wait_for(lambda: group(m), f"{m.name} receives the group", 60)
            assert api(a, "GET", "/api/cluster/map")["map"]["coordinator"] == node_a

        with subtest("1. Slow network: configuration reaches every node; no takeover"):
            for m in NODES:
                m.succeed("tc qdisc add dev eth1 root netem delay 200ms 50ms loss 3%")
            create_user(c, "carol")
            everywhere("carol", NODES, 180)
            watch_no_takeover(60, "slow network")
            assert all(quorate(m) for m in NODES), "quorum holds on a slow network"
            for m in NODES:
                m.succeed("tc qdisc del dev eth1 root")

        with subtest("2. c is cut off: the majority keeps quorum; both sides keep working"):
            ips = cut(c, (a, b))
            cut_c = {m.name: cut(m, (c,)) for m in (a, b)}
            c.wait_until_succeeds("(corosync-quorumtool -s || true) | grep -q 'Quorate: *No'", timeout=60)
            assert quorate(a) and quorate(b), "a and b keep quorum"
            create_user(a, "dave")
            create_user(c, "erin")
            wait_for(lambda: (sync(c), status(c)["mode"] == "isolated")[1], "c reports isolated", 60)
            everywhere("dave", (a, b), 90)
            a.succeed("echo split >> /mnt/tank/data/file")
            watch_no_takeover(30, "minority split")
            heal(c, ips)
            for m in (a, b):
                heal(m, cut_c[m.name])
            c.wait_until_succeeds("(corosync-quorumtool -s || true) | grep -q 'Quorate: *Yes'", timeout=120)
            everywhere("dave", NODES, 180)
            everywhere("erin", NODES, 180)
            assert all(not api(m, "GET", "/api/config/conflicts")["conflicts"] for m in NODES), "different changes merge"

        with subtest("3. a, coordinator and owner, loses power: b coordinates and takes the group over"):
            a.crash()
            for m in (b, c):
                m.wait_until_succeeds("(corosync-quorumtool -s || true) | grep -q 'Quorate: *Yes'", timeout=60)
            wait_for(lambda: api(b, "GET", "/api/cluster/map")["map"]["coordinator"] == peers["b"], "b coordinates", 60)
            started = time.time()
            wait_for(lambda: b.execute("zpool list tank")[0] == 0, "b imports the pool", ${toString (watchdogTimeout + 180)})
            print(f"takeover {time.time() - started:.0f}s after the power loss")
            b.wait_until_succeeds("grep -q split /mnt/tank/data/file", timeout=60)
            c.fail("zpool list tank")
            gc = wait_for(lambda: (lambda g: g if g["owner"] == peers["b"] else None)(group(c)), "c learns the new owner from b", 120)
            assert gc["epoch"] == 2, gc
            create_user(b, "frank")
            everywhere("frank", (b, c), 120)

        with subtest("4. a comes back: follows the new epoch, coordinates again, catches up"):
            a.start()
            up(a)
            login(a)
            ga = wait_for(lambda: (lambda g: g if g and g["epoch"] == 2 else None)(group(a)), "a follows epoch 2", 180)
            assert ga["owner"] == peers["b"] and ga["role"] == "standby", ga
            a.fail("zpool list tank")
            everywhere("frank", NODES, 180)
            wait_for(lambda: api(c, "GET", "/api/cluster/map")["map"]["coordinator"] == node_a, "a coordinates again", 120)

        with subtest("No daemon panics, security refusals or security warnings"):
            for m in NODES:
                bad = m.execute("journalctl --no-pager -u dplaned | grep -E 'panic:|SECURITY WARNING|security refusal' || true")[1].strip()
                assert bad == "", f"{m.name}: {bad}"
    except Exception:
        diag()
        raise
  '';
}
