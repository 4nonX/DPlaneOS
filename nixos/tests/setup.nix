# nixos/tests/setup.nix
# ─────────────────────────────────────────────────────────────────────────────
# Design 0001 phase 4 exit test: a full setup through the API, in the order
# of the guided setup, then what the phase adds on top of it.
#
#   a, b  DPlaneOS nodes on a shared disk, softdog watchdog
#   w     plain Linux witness (corosync-qnetd), enrolled with the one-line command
#
#   1. watchdog on both nodes (POST /api/ha/watchdog/configure)
#   2. pair, form the cluster, add the third vote
#   3. safety check of the third vote; the cluster map agrees on the coordinator
#   4. shared storage group owned by a
#   5. default conflict rule: a and b are split (both still reach w, so only
#      a's half is quorate and has a coordinator); the same user is changed on
#      both; after the split the coordinator's version wins on both nodes and
#      b keeps its change as a proposal, which it applies again
#   6. maintenance mode on a moves the group to b; a is no move target
#
# Run: nix build .#checks.x86_64-linux.setup -L
# ─────────────────────────────────────────────────────────────────────────────
{ nixpkgs, system ? "x86_64-linux", impermanence, daemonPackage, frontendPackage, ... }:

let
  pkgs = nixpkgs.legacyPackages.${system};
  sharedImage = "/tmp/dplaneos-setup-disk.img";

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
    boot.kernelModules = [ "softdog" ];
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
  name = "dplaneos-setup";

  nodes.a = dplaneNode "a" "aaaa0101";
  nodes.b = dplaneNode "b" "bbbb0102";
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

    PASSWORD = "Setup-Test-Pass-1"
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

    def cmap(m):
        return ok(api(m, "GET", "/api/cluster/map"), f"{m.name}: cluster map")["map"]

    def user(m, name):
        us = api(m, "GET", "/api/rbac/users")
        for u in us["users"]:
            if u["username"] == name:
                return u
        return None

    def sync(*ms):
        for m in ms:
            api(m, "POST", "/api/config/sync/now", {})

    def addr(m, of):
        return m.succeed(f"getent ahostsv4 {of} | head -1 | cut -d' ' -f1").strip()

    def diag():
        for m in (a, b):
            print(m.execute("journalctl -b --no-pager -u dplaned | grep -i -E 'group|sync|conflict|quorum|error|maintenance' | tail -n 80")[1])
            print(m.execute("(corosync-quorumtool -s || true)")[1])

    try:
        with subtest("1. Watchdog on both nodes through the API"):
            for m in (a, b):
                login(m)
                prot = api(m, "GET", "/api/ha/protection")
                assert prot["fencing_ok"] is False, prot
                api(m, "POST", "/api/ha/watchdog/configure", {"enable": True, "device": "/dev/watchdog", "timeout_secs": 60, "pet_interval_sec": 5})
                wait_for(lambda: api(m, "GET", "/api/ha/protection")["fencing_ok"], f"{m.name}: fencing configured", 60)

        with subtest("2. Pair, form the cluster, add the third vote"):
            token = ok(api(a, "POST", "/api/config/peers/token", {}), "join code")["token"]
            preview = ok(api(b, "POST", "/api/config/peers/preview", {"url": "http://a", "token": token}), "merge preview")["preview"]
            assert preview["name"] == "a", preview
            ok(api(b, "POST", "/api/config/peers/join", {"url": "http://a", "token": token, "self_url": "http://b"}), "join")
            sa = api(a, "GET", "/api/config/sync/status")["status"]
            peer_b, node_a = sa["peers"][0]["id"], sa["node_id"]
            s = api(a, "GET", f"/api/quorum/suggest?peer_id={peer_b}")
            ok(api(a, "POST", "/api/quorum/cluster", {"peer_id": peer_b, "local_addr": s["local_addr"], "peer_addr": s["peer_addr"]}), "form cluster")
            code = ok(api(a, "POST", "/api/quorum/third-vote/code", {}), "third-vote code")["code"]
            w.succeed(f"curl -fsS http://a/api/quorum/witness-setup.sh | sh -s -- http://a {code}")
            for m in (a, b):
                wait_for(lambda: api(m, "GET", "/api/ha/protection")["auto_failover"], f"{m.name}: automatic failover on", 120)

        with subtest("3. Safety check; both nodes agree on the coordinator"):
            for m in (a, b):
                r = ok(api(m, "POST", "/api/cluster/test/qdevice", {}), "qdevice test")["result"]
                assert r["ok"], f"{m.name}: {r}"
            ma, mb = cmap(a), cmap(b)
            print(json.dumps(ma, indent=1))
            # a was formed with corosync node id 1: it coordinates.
            assert ma["coordinator"] == node_a and mb["coordinator"] == node_a, (ma["coordinator"], mb["coordinator"])
            assert ma["term"] > 0 and ma["quorum"]["qdevice_voting"], ma
            assert [n["name"] for n in ma["nodes"] if n["coordinator"]] == ["a"], ma["nodes"]

        with subtest("4. Shared storage group owned by a"):
            a.succeed("zpool create -f -o multihost=on -m /mnt/tank tank /dev/disk/by-id/virtio-dplshared")
            ok(api(a, "POST", "/api/groups", {"name": "data", "topology": "shared", "pools": ["tank"], "candidates": [peer_b]}), "create group")
            g = wait_for(lambda: next((g for g in cmap(b)["groups"] if g["name"] == "data"), None), "b knows the group", 60)
            assert g["owner"] == node_a and g["candidates"] == [node_a, peer_b] and g["epoch"] == 1, g

        with subtest("5. Same user changed on both halves of a split: the coordinator's version wins"):
            ok(api(a, "POST", "/api/rbac/users", {"action": "create", "username": "bob", "email": "bob@start.lan", "password": "Bob-Pass-12345", "role": "user", "confirm_password": PASSWORD}), "create bob")
            sync(a, b)
            wait_for(lambda: user(b, "bob"), "b has bob", 90)
            # Split a from b; both still reach the witness, which votes for a's half.
            ip_a, ip_b = addr(b, "a"), addr(a, "b")
            a.succeed(f"iptables -I INPUT -s {ip_b} -j DROP && iptables -I OUTPUT -d {ip_b} -j DROP")
            b.succeed(f"iptables -I INPUT -s {ip_a} -j DROP && iptables -I OUTPUT -d {ip_a} -j DROP")
            b.wait_until_succeeds("(corosync-quorumtool -s || true) | grep -q 'Quorate: *No'", timeout=90)
            a.wait_until_succeeds("(corosync-quorumtool -s || true) | grep -q 'Quorate: *Yes'", timeout=90)
            assert cmap(b).get("coordinator") in (None, ""), "b has no quorum: nobody coordinates there"
            assert cmap(a)["coordinator"] == node_a
            ok(api(a, "POST", "/api/rbac/users", {"action": "update", "id": user(a, "bob")["id"], "email": "bob@coordinator.lan"}), "a changes bob")
            ok(api(b, "POST", "/api/rbac/users", {"action": "update", "id": user(b, "bob")["id"], "email": "bob@isolated.lan"}), "b changes bob")
            for m in (a, b):
                api(m, "POST", "/api/config/capture", {})
            cur_b = next(i for i in api(b, "GET", "/api/config/current")["items"] if i["kind"] == "user" and i["key"] == "bob")
            assert cur_b["not_pulled_by"] == ["a"], cur_b
            # Heal the split.
            a.succeed(f"iptables -D INPUT -s {ip_b} -j DROP && iptables -D OUTPUT -d {ip_b} -j DROP")
            b.succeed(f"iptables -D INPUT -s {ip_a} -j DROP && iptables -D OUTPUT -d {ip_a} -j DROP")
            b.wait_until_succeeds("(corosync-quorumtool -s || true) | grep -q 'Quorate: *Yes'", timeout=120)

            def settled():
                sync(a, b)
                return (user(a, "bob")["email"] == "bob@coordinator.lan" and user(b, "bob")["email"] == "bob@coordinator.lan"
                        and not api(a, "GET", "/api/config/conflicts")["conflicts"] and not api(b, "GET", "/api/config/conflicts")["conflicts"])
            wait_for(settled, "the coordinator's version on both nodes, no open conflict", 180)
            props_b = api(b, "GET", "/api/config/conflicts")["proposals"]
            assert [(p["kind"], p["key"]) for p in props_b] == [("user", "bob")], props_b
            assert props_b[0]["payload"]["email"] == "bob@isolated.lan", props_b
            assert api(a, "GET", "/api/config/conflicts")["proposals"] == [], "the winning node keeps no proposal"
            cur_a = next(i for i in api(a, "GET", "/api/config/current")["items"] if i["kind"] == "user" and i["key"] == "bob")
            assert "coordinator" in cur_a.get("authority", ""), cur_a

            # b applies its change again: it becomes the current version everywhere.
            ok(api(b, "POST", f"/api/config/proposals/{props_b[0]['id']}/apply", {}), "apply proposal")
            assert api(b, "GET", "/api/config/conflicts")["proposals"] == []
            wait_for(lambda: (sync(a, b), user(a, "bob")["email"] == "bob@isolated.lan")[1], "a takes the applied proposal", 120)

        with subtest("6. Maintenance on a moves the group to b"):
            r = ok(api(a, "POST", "/api/cluster/maintenance", {"reason": "firmware"}), "enter maintenance")
            assert [(mv["group"], mv["result"], mv["target"]) for mv in r["moves"]] == [("data", "moved", peer_b)], r
            b.wait_until_succeeds("zpool list tank", timeout=120)
            a.fail("zpool list tank")
            mb = wait_for(lambda: (lambda m: m if any(n["name"] == "a" and n["maintenance"] for n in m["nodes"]) else None)(cmap(b)), "b sees a in maintenance", 60)
            g = next(g for g in mb["groups"] if g["name"] == "data")
            assert g["owner"] == peer_b and g["epoch"] == 2 and g["serving"], g
            moved = api(b, "POST", "/api/groups/data/move", {"target": node_a})
            assert moved["success"] is False and "maintenance" in moved["error"], moved
            ok(api(a, "DELETE", "/api/cluster/maintenance"), "end maintenance")
            wait_for(lambda: not any(n["maintenance"] for n in cmap(b)["nodes"]), "b sees a out of maintenance", 60)

        with subtest("No daemon panics, security refusals or security warnings"):
            for m in (a, b):
                bad = m.execute("journalctl -b --no-pager -u dplaned | grep -E 'panic:|SECURITY WARNING|security refusal' || true")[1].strip()
                assert bad == "", f"{m.name}: {bad}"
    except Exception:
        diag()
        raise
  '';
}
