# nixos/tests/replicated.nix
# ─────────────────────────────────────────────────────────────────────────────
# Design 0001 phase 3c: replicated storage groups. Two DPlaneOS nodes, each
# with its own disk and its own pool "tank" (any drives; here virtio).
#
#   1. a owns a replicated group; "Replicate now" streams zfs send over the
#      paired-node channel; b's copy is read-only.
#   2. Planned move to b: final replication, b writable, a read-only, epoch 2.
#   3. b replicates back to a.
#   4. Split: a's copy is changed behind the system's back; the next
#      replication is refused (never overwritten silently) and a shows it;
#      after "Discard the changes here" replication rolls a back.
#
# Run: nix build .#checks.x86_64-linux.replicated -L
# ─────────────────────────────────────────────────────────────────────────────
{ nixpkgs, system ? "x86_64-linux", impermanence, daemonPackage, frontendPackage, ... }:

let
  pkgs = nixpkgs.legacyPackages.${system};

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
    systemd.services.systemd-random-seed.enable = false;
    virtualisation.cores = 2;
    virtualisation.memorySize = 2048;
    virtualisation.diskImage = null;
    virtualisation.emptyDiskImages = [ { size = 1024; driveConfig.deviceExtraOpts.serial = serial; } ];
  };
in

pkgs.testers.nixosTest {
  name = "dplaneos-replicated";

  nodes.a = dplaneNode "a" "aaaa0001" "dpldiska";
  nodes.b = dplaneNode "b" "bbbb0002" "dpldiskb";

  testScript = ''
    import json, shlex, time

    start_all()
    for m in (a, b):
        m.wait_for_unit("dplaned.service")
        m.wait_until_succeeds("curl -sf http://localhost/api/system/status >/dev/null", timeout=180)

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

    def group(m):
        gs = api(m, "GET", "/api/groups")["groups"]
        return gs[0] if gs else None

    def wait_for(fn, what, timeout):
        deadline = time.time() + timeout
        while True:
            v = fn()
            if v:
                return v
            assert time.time() < deadline, f"timed out: {what}"
            time.sleep(3)

    def diag():
        for m in (a, b):
            print(m.execute("journalctl -b --no-pager -u dplaned | grep -i -E 'group|zfs|repl|error' | tail -n 80")[1])
            print(m.execute("zfs list -t all -r tank 2>&1; zfs get -r readonly tank 2>&1 | head -20")[1])

    try:
        with subtest("Pair, form the cluster, a pool on each node"):
            for m in (a, b):
                api(m, "POST", "/api/system/setup-admin", {"username": "admin", "password": "Repl-Test-Pass-1"}, auth=False)
                sid = api(m, "POST", "/api/auth/login", {"username": "admin", "password": "Repl-Test-Pass-1"}, auth=False)["session_id"]
                csrf = json.loads(m.succeed(f"curl -s http://localhost/api/csrf -H 'X-Session-ID: {sid}' -H 'X-User: admin'"))["csrf_token"]
                session[m.name] = (sid, csrf)
            token = ok(api(a, "POST", "/api/config/peers/token", {}), "join code")["token"]
            ok(api(b, "POST", "/api/config/peers/join", {"url": "http://a", "token": token, "self_url": "http://b"}), "join")
            peer_b = api(a, "GET", "/api/config/sync/status")["status"]["peers"][0]["id"]
            s = api(a, "GET", f"/api/quorum/suggest?peer_id={peer_b}")
            ok(api(a, "POST", "/api/quorum/cluster", {"peer_id": peer_b, "local_addr": s["local_addr"], "peer_addr": s["peer_addr"]}), "form cluster")
            a.succeed("zpool create -f -m /mnt/tank tank /dev/disk/by-id/virtio-dpldiska && zfs create tank/data && echo one > /mnt/tank/data/file1")
            b.succeed("zpool create -f -m /mnt/tank tank /dev/disk/by-id/virtio-dpldiskb")

        with subtest("Replicated group on a; replicate now; b's copy is read-only"):
            ok(api(a, "POST", "/api/groups", {"name": "data", "topology": "replicated", "pools": ["tank"],
                                              "candidates": [peer_b], "interval_secs": 3600, "auto_failover": False}), "create group")
            wait_for(lambda: group(b), "b receives the group", 60)
            ok(api(a, "POST", "/api/groups/data/replicate", {}), "replicate")
            b.wait_until_succeeds("grep -q one /mnt/tank/data/file1", timeout=60)
            b.fail("touch /mnt/tank/data/x")
            ga = group(a)
            assert any(r["direction"] == "out" and r["last_ok_at"] for r in ga["replication"]), ga

        with subtest("Planned move to b: final replication, b writable, a read-only"):
            a.succeed("echo two > /mnt/tank/data/file2")
            ok(api(a, "POST", "/api/groups/data/move", {"target": peer_b}), "move")
            b.wait_until_succeeds("grep -q two /mnt/tank/data/file2", timeout=60)
            b.succeed("touch /mnt/tank/data/written-on-b")
            a.fail("touch /mnt/tank/data/x")
            gb = group(b)
            assert gb["owner"] == peer_b and gb["epoch"] == 2 and gb["role"] == "owner", gb

        with subtest("b replicates back to a"):
            b.succeed("echo three > /mnt/tank/data/file3")
            ok(api(b, "POST", "/api/groups/data/replicate", {}), "replicate from b")
            a.wait_until_succeeds("grep -q three /mnt/tank/data/file3", timeout=60)

        with subtest("Split: a's copy changed; replication refused until discarded"):
            a.succeed("zfs set readonly=off tank && echo rogue > /mnt/tank/data/rogue && sync")
            r = api(b, "POST", "/api/groups/data/replicate", {})
            assert r.get("success") is not True and "changes" in r.get("error", ""), r
            a.succeed("test -f /mnt/tank/data/rogue")
            ga = group(a)
            assert any("refused here" in p for p in ga["problems"]), ga
            ok(api(a, "POST", "/api/groups/data/discard-divergent", {}), "discard")
            ok(api(b, "POST", "/api/groups/data/replicate", {}), "replicate after discard")
            a.wait_until_fails("test -f /mnt/tank/data/rogue", timeout=60)
            a.succeed("grep -q three /mnt/tank/data/file3")
            a.fail("touch /mnt/tank/data/x")
            assert group(a)["problems"] == [], group(a)

        with subtest("No daemon panics, security refusals or security warnings"):
            for m in (a, b):
                print(m.execute("journalctl -b --no-pager -u dplaned | grep -i -E ' (warn|warning|error|failed)' | tail -n 60")[1])
                bad = m.execute("journalctl -b --no-pager -u dplaned | grep -E 'panic:|SECURITY WARNING|security refusal' || true")[1].strip()
                assert bad == "", f"{m.name}: {bad}"
    except Exception:
        diag()
        raise
  '';
}
