# nixos/tests/failover.nix
# ─────────────────────────────────────────────────────────────────────────────
# Design 0001 phase 3c exit test: automatic failover of a storage group with
# the ADR-0009 baseline (third vote + watchdog self-fencing), no power fencing.
#
#   a, b  DPlaneOS nodes on a shared disk, softdog watchdog enabled
#   w     plain Linux witness (corosync-qnetd), enrolled with the one-line command
#
# a owns the group. a is cut off from the network: b keeps quorum through
# the third vote, a loses it and stops resetting its watchdog (logged). a is
# then crashed, as its watchdog would reset it. b waits the fencing delay
# (watchdog timeout + margin), takes the group over (epoch 2, import -f past
# multihost) and serves the data. a boots again, cannot import the pool
# (multihost: b holds it) and follows the new epoch by pulling.
#
# Run: nix build .#checks.x86_64-linux.failover -L
# ─────────────────────────────────────────────────────────────────────────────
{ nixpkgs, system ? "x86_64-linux", impermanence, daemonPackage, frontendPackage, ... }:

let
  pkgs = nixpkgs.legacyPackages.${system};
  sharedImage = "/tmp/dplaneos-failover-disk.img";
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
    boot.kernelModules = [ "softdog" ];
    systemd.services.systemd-random-seed.enable = false;
    virtualisation.cores = 2;
    virtualisation.memorySize = 2048;
    virtualisation.diskImage = null;
    virtualisation.qemu.options = [
      "-drive file=${sharedImage},if=none,id=shared,format=raw,file.locking=off,cache=none"
      "-device virtio-blk-pci,drive=shared,share-rw=on,serial=dplshared"
    ];
  };
in

pkgs.testers.nixosTest {
  name = "dplaneos-failover";

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

    start_all()
    for m in (a, b):
        m.wait_for_unit("dplaned.service")
        m.wait_until_succeeds("curl -sf http://localhost/api/system/status >/dev/null", timeout=180)
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
        api(m, "POST", "/api/system/setup-admin", {"username": "admin", "password": "Failover-Test-Pass-1"}, auth=False)
        sid = api(m, "POST", "/api/auth/login", {"username": "admin", "password": "Failover-Test-Pass-1"}, auth=False)["session_id"]
        csrf = json.loads(m.succeed(f"curl -s http://localhost/api/csrf -H 'X-Session-ID: {sid}' -H 'X-User: admin'"))["csrf_token"]
        session[m.name] = (sid, csrf)

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
        for m in (b,):
            print(m.execute("journalctl -b --no-pager -u dplaned | grep -i -E 'group|watchdog|zpool|error|failover' | tail -n 80")[1])
            print(m.execute("(corosync-quorumtool -s || true); zpool status 2>&1")[1])

    try:
        with subtest("Cluster with a third vote; watchdog enabled on both nodes"):
            for m in (a, b):
                login(m)
                m.succeed("sudo -u postgres psql -d dplaneos -c \"INSERT INTO ha_watchdog_config (id, enable, device, timeout_secs, pet_interval_sec) VALUES (1, true, '/dev/watchdog', ${toString watchdogTimeout}, 5) ON CONFLICT (id) DO UPDATE SET enable = true, timeout_secs = ${toString watchdogTimeout}, pet_interval_sec = 5\"")
                m.succeed("systemctl restart dplaned")
                m.wait_until_succeeds("curl -sf http://localhost/api/system/status >/dev/null", timeout=120)
                m.wait_until_succeeds("journalctl -u dplaned | grep -q 'HA WATCHDOG: opened'", timeout=60)
                login(m)
            token = ok(api(a, "POST", "/api/config/peers/token", {}), "join code")["token"]
            ok(api(b, "POST", "/api/config/peers/join", {"url": "http://a", "token": token, "self_url": "http://b"}), "join")
            peer_b = api(a, "GET", "/api/config/sync/status")["status"]["peers"][0]["id"]
            s = api(a, "GET", f"/api/quorum/suggest?peer_id={peer_b}")
            ok(api(a, "POST", "/api/quorum/cluster", {"peer_id": peer_b, "local_addr": s["local_addr"], "peer_addr": s["peer_addr"]}), "form cluster")
            code = ok(api(a, "POST", "/api/quorum/third-vote/code", {}), "third-vote code")["code"]
            w.succeed(f"curl -fsS http://a/api/quorum/witness-setup.sh | sh -s -- http://a {code}")
            for m in (a, b):
                wait_for(lambda: api(m, "GET", "/api/ha/protection")["auto_failover"], f"{m.name}: automatic failover on", 120)

        with subtest("Pool with multihost; group owned by a"):
            a.succeed("zpool create -f -o multihost=on -m /mnt/tank tank /dev/disk/by-id/virtio-dplshared")
            a.succeed("zfs create tank/data && echo hello > /mnt/tank/data/file")
            ok(api(a, "POST", "/api/groups", {"name": "data", "topology": "shared", "pools": ["tank"], "candidates": [peer_b]}), "create group")
            wait_for(lambda: group(b), "b receives the group", 60)
            prot = api(a, "GET", "/api/ha/protection")
            print(json.dumps(prot, indent=1))
            assert any(l["name"] == "ZFS multihost" and l["state"] == "ok" for l in prot["layers"]), prot

        with subtest("a is cut off: b keeps quorum, a stops resetting its watchdog"):
            a.block()
            b.wait_until_succeeds("(corosync-quorumtool -s || true) | grep -q 'Quorate: *Yes'", timeout=60)
            a.wait_until_succeeds("journalctl -u dplaned | grep -q 'lost quorum - not resetting the watchdog'", timeout=60)
            gb = wait_for(lambda: (lambda g: g if g["failover"].get("action") == "wait" else None)(group(b)), "b waits the fencing delay", 60)
            print(gb["failover"])
            b.fail("zpool list tank")

        with subtest("a is reset (as by its watchdog); b takes over after the fencing delay"):
            a.crash()
            started = time.time()
            wait_for(lambda: b.execute("zpool list tank")[0] == 0, "b imports the pool", ${toString (watchdogTimeout + 120)})
            took = time.time() - started
            print(f"takeover {took:.0f}s after the reset")
            # The import returns before the datasets are mounted.
            b.wait_until_succeeds("grep -q hello /mnt/tank/data/file", timeout=60)
            gb = group(b)
            assert gb["owner"] == peer_b and gb["epoch"] == 2 and gb["can_write"], gb

        with subtest("a boots again: cannot import the pool and follows the new epoch"):
            a.start()
            try:
                a.unblock()
            except Exception:
                pass
            a.wait_for_unit("dplaned.service")
            a.wait_until_succeeds("curl -sf http://localhost/api/system/status >/dev/null", timeout=180)
            login(a)
            wait_for(lambda: (lambda g: g and g["epoch"] == 2)(group(a)), "a adopts epoch 2", 120)
            ga = group(a)
            assert ga["role"] == "standby", ga
            a.fail("zpool list tank")
            b.succeed("zpool list tank")

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
