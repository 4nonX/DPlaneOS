# nixos/tests/hardening-replicated.nix
# ─────────────────────────────────────────────────────────────────────────────
# Design 0001 phase 7: a pair with a replicated storage group (each node its
# own disk), a third vote and automatic failover switched on for the group,
# under slow network, a short split and power loss (section 5.11).
#
#   a, b  DPlaneOS nodes, each with its own pool "tank", softdog watchdog
#   w     plain Linux witness (corosync-qnetd)
#
#   1. Slow network (300 ms ± 100 ms, 5 % loss): replication still completes,
#      no promotion.
#   2. A split shorter than the fencing delay: no promotion; replication works
#      again afterwards.
#   3. The owner loses power: b promotes its copy after the fencing delay
#      (epoch 2, writable), with the data of the last replication; what a
#      wrote after it is not on b.
#   4. a comes back: it follows epoch 2, its copy is read-only, and b's
#      replication to it is refused because a's copy has data b does not have
#      (both copies kept) until the operator discards a's changes. They are
#      kept on b as a separate copy, replicated back to a, and deleted on
#      every node when the owner deletes it.
#
# Run: nix build .#checks.x86_64-linux.hardening-replicated -L
# ─────────────────────────────────────────────────────────────────────────────
{ nixpkgs, system ? "x86_64-linux", impermanence, daemonPackage, frontendPackage, ... }:

let
  pkgs = nixpkgs.legacyPackages.${system};
  watchdogTimeout = 60;

  dplaneNode = hostName: hostId: serial: { lib, ... }: {
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
    systemd.services.systemd-random-seed.enable = false;
    virtualisation.cores = 2;
    virtualisation.memorySize = 2048;
    virtualisation.emptyDiskImages = [ { size = 1024; driveConfig.deviceExtraOpts.serial = serial; } ];
  };
in

pkgs.testers.nixosTest {
  name = "dplaneos-hardening-replicated";

  nodes.a = dplaneNode "a" "aaaa0001" "dpldiska";
  nodes.b = dplaneNode "b" "bbbb0002" "dpldiskb";
  nodes.w = { pkgs, lib, ... }: {
    environment.systemPackages = with pkgs; [ corosync-qdevice nss.tools curl iproute2 gawk gnused (lib.getBin glibc) ];
    systemd.services.corosync-qnetd = {
      description = "Corosync Qdevice Network daemon (distribution unit)";
      after = [ "network-online.target" ];
      path = with pkgs; [ corosync-qdevice nss.tools ];
      serviceConfig = {
        ExecStart = "${pkgs.bash}/bin/bash -c 'exec corosync-qnetd -f'";
        RuntimeDirectory = "corosync-qnetd";
      };
    };
    networking.firewall.allowedTCPPorts = [ 5403 ];
    virtualisation.memorySize = 512;
  };

  testScript = ''
    import json, shlex, time

    PASSWORD = "Hardening-Pass-1"
    start_all()

    def up(m):
        m.wait_for_unit("dplaned.service")
        m.wait_until_succeeds("curl -sf http://localhost/api/system/status >/dev/null", timeout=180)

    for m in (a, b):
        up(m)
    w.wait_for_unit("multi-user.target")

    session = {}

    def api(m, method, path, body=None, auth=True):
        cmd = f"curl -s --max-time 300 -X {method} http://localhost{path} -H 'Content-Type: application/json'"
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

    def watch_no_promotion(seconds, why):
        end = time.time() + seconds
        while time.time() < end:
            gb = group(b)
            assert gb["owner"] == node_a and gb["epoch"] == 1, (why, gb)
            assert gb.get("failover", {}).get("action") != "takeover", (why, gb)
            b.fail("touch /mnt/tank/data/must-not-write")
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

    def diag():
        for m in (a, b):
            print(m.execute("journalctl --no-pager -u dplaned | grep -i -E 'group|repl|watchdog|failover|quorum|error' | grep -v -E ' (GET|POST) ' | tail -n 80")[1])
            print(m.execute("(corosync-quorumtool -s || true); zfs list -t all -r tank 2>&1 | tail -20; zfs get -r readonly tank 2>&1 | head")[1])

    try:
        with subtest("Pair, cluster with a third vote, watchdog, replicated group owned by a"):
            for m in (a, b):
                login(m)
                m.succeed("sudo -u postgres psql -d dplaneos -c \"INSERT INTO ha_watchdog_config (id, enable, device, timeout_secs, pet_interval_sec) VALUES (1, true, '/dev/watchdog', ${toString watchdogTimeout}, 5) ON CONFLICT (id) DO UPDATE SET enable = true, timeout_secs = ${toString watchdogTimeout}, pet_interval_sec = 5\"")
                m.succeed("systemctl restart dplaned")
                up(m)
                m.wait_until_succeeds("journalctl -u dplaned | grep -q 'HA WATCHDOG: opened'", timeout=60)
                login(m)
            token = ok(api(a, "POST", "/api/config/peers/token", {}), "join code")["token"]
            ok(api(b, "POST", "/api/config/peers/join", {"url": "http://a", "token": token, "self_url": "http://b"}), "join")
            def conflicts():
                sync(b, a, b)
                return [(m, c) for m in (a, b) for c in api(m, "GET", "/api/config/conflicts")["conflicts"]]
            wait_for(conflicts, "the admin conflict from pairing", 60)
            # Resolving one side can close the other (the result is adopted
            # there), and a node may be busy applying: each round, read the
            # open conflicts again and resolve one.
            def resolve_one():
                open_ = conflicts()
                for m, c in open_:
                    assert (c["kind"], c["key"]) == ("user", "admin"), c
                    api(m, "POST", f"/api/config/conflicts/{c['id']}/resolve", {"choice": "local" if m is a else "remote"})
                    break
                return not open_
            wait_for(resolve_one, "no conflict after pairing", 120)
            login(b)
            sa = api(a, "GET", "/api/config/sync/status")["status"]
            peer_b, node_a = sa["peers"][0]["id"], sa["node_id"]
            s = api(a, "GET", f"/api/quorum/suggest?peer_id={peer_b}")
            ok(api(a, "POST", "/api/quorum/cluster", {"peer_id": peer_b, "local_addr": s["local_addr"], "peer_addr": s["peer_addr"]}), "form cluster")
            code = ok(api(a, "POST", "/api/quorum/third-vote/code", {}), "third-vote code")["code"]
            w.succeed(f"curl -fsS http://a/api/quorum/witness-setup.sh | sh -s -- http://a {code}")
            for m in (a, b):
                wait_for(lambda: api(m, "GET", "/api/ha/protection")["auto_failover"], f"{m.name}: automatic failover on", 120)
            a.succeed("zpool create -f -m /mnt/tank tank /dev/disk/by-id/virtio-dpldiska && zfs create tank/data && echo one > /mnt/tank/data/file1")
            b.succeed("zpool create -f -m /mnt/tank tank /dev/disk/by-id/virtio-dpldiskb")
            ok(api(a, "POST", "/api/groups", {"name": "data", "topology": "replicated", "pools": ["tank"],
                                              "candidates": [peer_b], "interval_secs": 3600, "auto_failover": True}), "create group")
            wait_for(lambda: group(b), "b receives the group", 60)
            ok(api(a, "POST", "/api/groups/data/replicate", {}), "first replication")
            b.wait_until_succeeds("grep -q one /mnt/tank/data/file1", timeout=60)

        with subtest("1. Slow network: replication completes, no promotion"):
            for m in (a, b):
                m.succeed("tc qdisc add dev eth1 root netem delay 300ms 100ms loss 5%")
            a.succeed("dd if=/dev/urandom of=/mnt/tank/data/blob bs=1M count=16 status=none && sha256sum /mnt/tank/data/blob | cut -d' ' -f1 > /tmp/blob.sum")
            ok(api(a, "POST", "/api/groups/data/replicate", {}), "replicate over the slow link")
            want = a.succeed("cat /tmp/blob.sum").strip()
            b.wait_until_succeeds(f"sha256sum /mnt/tank/data/blob | grep -q {want}", timeout=120)
            watch_no_promotion(60, "slow network")
            for m in (a, b):
                m.succeed("tc qdisc del dev eth1 root")

        with subtest("2. A split shorter than the fencing delay: no promotion"):
            of_a, of_b = addrs(b, "a"), addrs(a, "b")
            drop(a, of_b)
            drop(b, of_a)
            split_at = time.time()
            b.wait_until_succeeds("(corosync-quorumtool -s || true) | grep -q 'Quorate: *No'", timeout=60)
            a.succeed("echo during-split > /mnt/tank/data/file2")
            watch_no_promotion(max(${toString watchdogTimeout} - (time.time() - split_at) - 10, 5), "short split")
            drop(a, of_b, on=False)
            drop(b, of_a, on=False)
            b.wait_until_succeeds("(corosync-quorumtool -s || true) | grep -q 'Quorate: *Yes'", timeout=120)
            wait_for(lambda: api(a, "POST", "/api/groups/data/replicate", {}).get("success"), "replication works again", 120)
            b.wait_until_succeeds("grep -q during-split /mnt/tank/data/file2", timeout=60)

        with subtest("3. The owner loses power: b promotes its copy after the fencing delay"):
            a.succeed("echo replicated > /mnt/tank/data/file3")
            ok(api(a, "POST", "/api/groups/data/replicate", {}), "last replication before the power loss")
            b.wait_until_succeeds("grep -q replicated /mnt/tank/data/file3", timeout=60)
            a.succeed("echo late > /mnt/tank/data/late && sync")  # never replicated
            a.crash()
            started = time.time()
            gb = wait_for(lambda: (lambda g: g if g["owner"] == peer_b else None)(group(b)), "b promotes its copy", ${toString (watchdogTimeout + 180)})
            print(f"promotion {time.time() - started:.0f}s after the power loss")
            assert gb["epoch"] == 2, gb
            b.succeed("touch /mnt/tank/data/written-on-b")
            b.succeed("grep -q replicated /mnt/tank/data/file3")
            b.fail("test -e /mnt/tank/data/late")

        with subtest("4. a comes back: follows epoch 2; the split is kept until the operator decides"):
            a.start()
            up(a)
            login(a)
            ga = wait_for(lambda: (lambda g: g if g and g["epoch"] == 2 else None)(group(a)), "a follows epoch 2", 180)
            assert ga["owner"] == peer_b and ga["role"] != "owner", ga
            a.wait_until_fails("touch /mnt/tank/data/x", timeout=60)
            r = api(b, "POST", "/api/groups/data/replicate", {})
            assert r.get("success") is not True and "changes" in r.get("error", ""), r
            a.succeed("grep -q late /mnt/tank/data/late")  # a's copy is kept
            assert any("refused here" in p for p in group(a)["problems"]), group(a)
            r = ok(api(a, "POST", "/api/groups/data/discard-divergent", {}), "discard a's changes, kept on the owner")
            assert len(r["kept"]) == 1 and r["kept"][0].startswith("tank/dplane-split-"), r
            kept = r["kept"][0].split("/", 1)[1]
            # The kept copy is on the owner: a's late file, beside b's data.
            b.succeed(f"zfs mount tank/{kept}/data 2>/dev/null || true; grep -q late /mnt/tank/{kept}/data/late")
            ok(api(b, "POST", "/api/groups/data/replicate", {}), "replicate b to a")
            a.wait_until_succeeds("test -e /mnt/tank/data/written-on-b", timeout=60)
            a.fail("test -e /mnt/tank/data/late")
            assert group(a)["problems"] == [], group(a)
            # Replication carried the kept copy back: both nodes keep it.
            a.succeed(f"zfs list tank/{kept}/data")
            assert any(s["name"] == kept for s in group(a)["splits"]) and any(s["name"] == kept for s in group(b)["splits"]), group(b)
            # Deleting it is the owner's: replication then removes it everywhere.
            r = api(a, "DELETE", f"/api/groups/data/splits/tank/{kept}")
            assert r.get("success") is not True, "only the owner deletes kept copies"
            ok(api(b, "DELETE", f"/api/groups/data/splits/tank/{kept}"), "delete the kept copy on the owner")
            ok(api(b, "POST", "/api/groups/data/replicate", {}), "replicate after deleting")
            a.wait_until_fails(f"zfs list tank/{kept}", timeout=60)

        with subtest("No daemon panics, security refusals or security warnings"):
            for m in (a, b):
                bad = m.execute("journalctl --no-pager -u dplaned | grep -E 'panic:|SECURITY WARNING|security refusal' || true")[1].strip()
                assert bad == "", f"{m.name}: {bad}"
    except Exception:
        diag()
        raise
  '';
}
