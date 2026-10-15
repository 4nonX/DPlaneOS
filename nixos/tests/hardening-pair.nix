# nixos/tests/hardening-pair.nix
# ─────────────────────────────────────────────────────────────────────────────
# Design 0001 phase 7: a pair with shared storage and a third vote under slow
# network, a short split and power loss (failure semantics, section 5.11).
#
#   a, b  DPlaneOS nodes on a shared disk, softdog watchdog; a owns the group
#   w     plain Linux witness (corosync-qnetd)
#
#   1. Slow network (300 ms ± 100 ms, 5 % loss): no takeover, quorum holds,
#      configuration still reaches the other node.
#   2. A split shorter than the fencing delay: the owner keeps serving, the
#      standby does nothing, both nodes report "isolated" and keep accepting
#      changes; after the split the changes of both sides are on both.
#   3. The standby loses power: the owner keeps the group; the standby comes
#      back as standby and catches up.
#   4. The witness loses power: two of three votes keep quorum; no takeover;
#      the witness votes again after its restart.
#   5. Both nodes lose power: the owner imports the pool again (once the other
#      node confirms it does not use it), the data and the configuration are
#      intact, the epoch is unchanged.
#
# The owner losing power alone is failover.nix.
# Run: nix build .#checks.x86_64-linux.hardening-pair -L
# ─────────────────────────────────────────────────────────────────────────────
{ nixpkgs, system ? "x86_64-linux", impermanence, daemonPackage, frontendPackage, ... }:

let
  pkgs = nixpkgs.legacyPackages.${system};
  sharedImage = "/tmp/dplaneos-hardening-pair-disk.img";
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
  name = "dplaneos-hardening-pair";

  nodes.a = dplaneNode "a" "aaaa0001";
  nodes.b = dplaneNode "b" "bbbb0002";
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

    with open("${sharedImage}", "wb") as f:
        f.truncate(1 << 30)

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

    def mode(m):
        return api(m, "GET", "/api/config/sync/status")["status"]["mode"]

    def has_user(m, name):
        return any(u["username"] == name for u in api(m, "GET", "/api/rbac/users")["users"])

    def create_user(m, name):
        ok(api(m, "POST", "/api/rbac/users", {"action": "create", "username": name, "email": f"{name}@test.lan", "password": "User-Pass-12345",
                                              "role": "user", "confirm_password": PASSWORD}), f"{m.name}: create {name}")

    def quorate(m):
        return m.execute("(corosync-quorumtool -s || true) | grep -q 'Quorate: *Yes'")[0] == 0

    def owner_unchanged(why):
        ga, gb = group(a), group(b)
        assert ga["owner"] == node_a and ga["epoch"] == 1 and ga["can_write"], (why, ga)
        assert gb["owner"] == node_a and gb["epoch"] == 1, (why, gb)
        a.succeed("zpool list tank")
        b.fail("zpool list tank")

    def watch_no_takeover(seconds, why):
        # The standby must not start a takeover at any time during the period.
        end = time.time() + seconds
        while time.time() < end:
            fo = (group(b) or {}).get("failover", {})
            assert fo.get("action") not in ("wait", "take_over", "takeover"), (why, fo)
            b.fail("zpool list tank")
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
            print(m.execute("journalctl -b --no-pager -u dplaned | grep -i -E 'group|watchdog|zpool|failover|quorum|sync|conflict|error' | grep -v -E ' (GET|POST) ' | tail -n 80")[1])
            print(m.execute("(corosync-quorumtool -s || true); zpool status 2>&1; tc qdisc show dev eth1")[1])

    try:
        with subtest("Pair, cluster with a third vote, watchdog, shared group owned by a"):
            for m in (a, b):
                login(m)
                m.succeed("sudo -u postgres psql -d dplaneos -c \"INSERT INTO ha_watchdog_config (id, enable, device, timeout_secs, pet_interval_sec) VALUES (1, true, '/dev/watchdog', ${toString watchdogTimeout}, 5) ON CONFLICT (id) DO UPDATE SET enable = true, timeout_secs = ${toString watchdogTimeout}, pet_interval_sec = 5\"")
                m.succeed("systemctl restart dplaned")
                up(m)
                m.wait_until_succeeds("journalctl -u dplaned | grep -q 'HA WATCHDOG: opened'", timeout=60)
                login(m)
            token = ok(api(a, "POST", "/api/config/peers/token", {}), "join code")["token"]
            ok(api(b, "POST", "/api/config/peers/join", {"url": "http://a", "token": token, "self_url": "http://b"}), "join")
            # Each node set up its own admin: settle that conflict with a's version.
            def conflicts():
                sync(b, a, b)
                return [(m, c) for m in (a, b) for c in api(m, "GET", "/api/config/conflicts")["conflicts"]]
            for m, c in wait_for(conflicts, "the admin conflict from pairing", 60):
                assert (c["kind"], c["key"]) == ("user", "admin"), c
                wait_for(lambda: api(m, "POST", f"/api/config/conflicts/{c['id']}/resolve", {"choice": "local" if m is a else "remote"}).get("success"),
                         f"{m.name}: keep a's admin", 60)
            wait_for(lambda: not conflicts(), "no conflict after pairing", 60)
            login(b)
            sa = api(a, "GET", "/api/config/sync/status")["status"]
            peer_b, node_a = sa["peers"][0]["id"], sa["node_id"]
            s = api(a, "GET", f"/api/quorum/suggest?peer_id={peer_b}")
            ok(api(a, "POST", "/api/quorum/cluster", {"peer_id": peer_b, "local_addr": s["local_addr"], "peer_addr": s["peer_addr"]}), "form cluster")
            code = ok(api(a, "POST", "/api/quorum/third-vote/code", {}), "third-vote code")["code"]
            w.succeed(f"curl -fsS http://a/api/quorum/witness-setup.sh | sh -s -- http://a {code}")
            for m in (a, b):
                wait_for(lambda: api(m, "GET", "/api/ha/protection")["auto_failover"], f"{m.name}: automatic failover on", 120)
            a.succeed("zpool create -f -o multihost=on -m /mnt/tank tank /dev/disk/by-id/virtio-dplshared")
            a.succeed("zfs create tank/data && echo hello > /mnt/tank/data/file")
            ok(api(a, "POST", "/api/groups", {"name": "data", "topology": "shared", "pools": ["tank"], "candidates": [peer_b]}), "create group")
            wait_for(lambda: group(b), "b receives the group", 60)
            owner_unchanged("after setup")

        with subtest("1. Slow network: no takeover, quorum holds, configuration still arrives"):
            for m in (a, b):
                m.succeed("tc qdisc add dev eth1 root netem delay 300ms 100ms loss 5%")
            create_user(a, "carol")
            wait_for(lambda: (sync(b, a), has_user(b, "carol"))[1], "carol reaches b over the slow link", 180)
            watch_no_takeover(90, "slow network")
            assert quorate(a) and quorate(b), "quorum holds on a slow link"
            a.succeed("echo slow >> /mnt/tank/data/file")
            owner_unchanged("slow network")
            for m in (a, b):
                m.succeed("tc qdisc del dev eth1 root")

        with subtest("2. A split shorter than the fencing delay: owner serves, standby waits, both keep working"):
            of_a, of_b = addrs(b, "a"), addrs(a, "b")
            drop(a, of_b)
            drop(b, of_a)
            split_at = time.time()
            b.wait_until_succeeds("(corosync-quorumtool -s || true) | grep -q 'Quorate: *No'", timeout=60)
            assert quorate(a), "a keeps quorum through the witness"
            create_user(a, "dave")
            create_user(b, "erin")
            wait_for(lambda: (sync(a, b), mode(a) == "isolated" and mode(b) == "isolated")[1], "both nodes report isolated", 60)
            a.succeed("echo split >> /mnt/tank/data/file")
            remaining = ${toString watchdogTimeout} - (time.time() - split_at) - 10
            watch_no_takeover(max(remaining, 5), "short split")
            drop(a, of_b, on=False)
            drop(b, of_a, on=False)
            b.wait_until_succeeds("(corosync-quorumtool -s || true) | grep -q 'Quorate: *Yes'", timeout=120)
            wait_for(lambda: (sync(a, b, a), all(has_user(m, u) for m in (a, b) for u in ("dave", "erin")))[1],
                     "the changes of both sides are on both nodes", 180)
            wait_for(lambda: mode(a) == "connected" and mode(b) == "connected", "both connected again", 60)
            assert not api(a, "GET", "/api/config/conflicts")["conflicts"], "different changes merge without a conflict"
            a.succeed("grep -q split /mnt/tank/data/file")
            owner_unchanged("short split")

        with subtest("3. The standby loses power: the owner keeps the group; the standby catches up"):
            b.crash()
            a.wait_until_succeeds("(corosync-quorumtool -s || true) | grep -q 'Quorate: *Yes'", timeout=60)
            create_user(a, "frank")
            a.succeed("echo standby-down >> /mnt/tank/data/file")
            ga = group(a)
            assert ga["owner"] == node_a and ga["epoch"] == 1 and ga["can_write"], ga
            b.start()
            up(b)
            login(b)
            wait_for(lambda: (sync(b), has_user(b, "frank"))[1], "b catches up after its restart", 120)
            gb = wait_for(lambda: group(b), "b knows the group", 60)
            assert gb["role"] == "standby", gb
            owner_unchanged("standby power loss")

        with subtest("4. The witness loses power: two votes keep quorum; it votes again after restart"):
            w.crash()
            time.sleep(15)
            assert quorate(a) and quorate(b), "a and b keep quorum without the witness"
            watch_no_takeover(30, "witness down")
            w.start()
            w.wait_for_unit("multi-user.target")
            w.succeed("systemctl is-active corosync-qnetd || systemctl start corosync-qnetd")
            wait_for(lambda: api(a, "GET", "/api/cluster/map")["map"]["quorum"]["qdevice_voting"], "the witness votes again", 120)
            owner_unchanged("witness power loss")

        with subtest("5. Both nodes lose power: the owner takes its pool back, nothing lost"):
            create_user(a, "grace")
            wait_for(lambda: (sync(b), has_user(b, "grace"))[1], "grace on both before the power loss", 60)
            a.crash()
            b.crash()
            a.start()
            b.start()
            for m in (a, b):
                up(m)
                login(m)
            a.wait_until_succeeds("zpool list tank", timeout=240)
            b.fail("zpool list tank")
            a.wait_until_succeeds("grep -q standby-down /mnt/tank/data/file", timeout=60)
            wait_for(lambda: group(a)["can_write"], "a serves again", 120)
            for m in (a, b):
                for u in ("carol", "dave", "erin", "frank", "grace"):
                    assert has_user(m, u), f"{m.name} lost {u}"
            owner_unchanged("power loss of both nodes")

        with subtest("No daemon panics, security refusals or security warnings"):
            for m in (a, b):
                bad = m.execute("journalctl --no-pager -u dplaned | grep -E 'panic:|SECURITY WARNING|security refusal' || true")[1].strip()
                assert bad == "", f"{m.name}: {bad}"
    except Exception:
        diag()
        raise
  '';
}
