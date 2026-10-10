# nixos/tests/votes.nix
# ─────────────────────────────────────────────────────────────────────────────
# Design 0001 phase 3: the third vote from "anything", end to end through the
# API and the one-line setup command:
#
#   a, b, c  DPlaneOS nodes
#   v        a plain Linux machine with corosync installed (standing in for a
#            Raspberry Pi or mini PC), joined as a voter: a full corosync
#            member that runs no DPlaneOS and pulls its configuration
#
# Steps: pair a with b and c; form the cluster a+b (two_node); v joins as a
# voter with the command the GUI shows: three votes, automatic failover on,
# no two_node; cut b off: a keeps quorum with v. Add c as a DPlaneOS member:
# v's sync timer picks up the new member list. Remove the voter.
#
# Run: nix build .#checks.x86_64-linux.votes -L
# ─────────────────────────────────────────────────────────────────────────────
{ nixpkgs, system ? "x86_64-linux", impermanence, daemonPackage, frontendPackage, ... }:

let
  pkgs = nixpkgs.legacyPackages.${system};

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
    systemd.services.systemd-random-seed.enable = false;
    virtualisation.cores = 2;
    virtualisation.memorySize = 1792;
    virtualisation.diskImage = null;
  };
in

pkgs.testers.nixosTest {
  name = "dplaneos-votes";

  nodes.a = dplaneNode "a" "aaaa0001";
  nodes.b = dplaneNode "b" "bbbb0002";
  nodes.c = dplaneNode "c" "cccc0003";

  # A generic Linux host with corosync "installed by its package manager":
  # the package plus a corosync unit, as distributions ship it.
  nodes.v = { pkgs, lib, ... }: {
    environment.systemPackages = with pkgs; [ corosync curl iproute2 gawk gnused diffutils openssl (lib.getBin glibc) ];
    systemd.services.corosync = {
      description = "Corosync Cluster Engine (distribution unit)";
      after = [ "network-online.target" ];
      unitConfig.ConditionPathExists = "/etc/corosync/corosync.conf";
      path = [ pkgs.corosync ];
      serviceConfig = {
        ExecStart = "${pkgs.corosync}/bin/corosync -f";
        StateDirectory = "corosync";
        LogsDirectory = "cluster";
      };
    };
    networking.firewall.allowedUDPPorts = [ 5405 ];
    virtualisation.memorySize = 512;
  };

  testScript = ''
    import json, shlex

    start_all()
    for m in (a, b, c):
        m.wait_for_unit("dplaned.service")
        m.wait_until_succeeds("curl -sf http://localhost/api/system/status >/dev/null", timeout=180)
    v.wait_for_unit("multi-user.target")

    session = {}

    def api(m, method, path, body=None, auth=True):
        cmd = f"curl -s --max-time 120 -X {method} http://localhost{path} -H 'Content-Type: application/json'"
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

    def votes(m, n):
        m.wait_until_succeeds(f"(corosync-quorumtool -s || true) | grep -q 'Total votes: *{n}'", timeout=120)

    def diag():
        for m in (a, b, c):
            print(m.execute("journalctl -b --no-pager -u dplaned -u dplaneos-corosync | grep -i -E 'quorum|corosync|voter|error' | tail -n 60")[1])
            print(m.execute("corosync-quorumtool -s 2>&1")[1])
        print(v.execute("journalctl -b --no-pager -u corosync -u dplaneos-voter-sync | tail -n 60; cat /etc/corosync/corosync.conf")[1])

    try:
        with subtest("Log in; pair a with b and with c"):
            for m in (a, b, c):
                api(m, "POST", "/api/system/setup-admin", {"username": "admin", "password": "Votes-Test-Pass-1"}, auth=False)
                r = api(m, "POST", "/api/auth/login", {"username": "admin", "password": "Votes-Test-Pass-1"}, auth=False)
                sid = r["session_id"]
                csrf = json.loads(m.succeed(f"curl -s http://localhost/api/csrf -H 'X-Session-ID: {sid}' -H 'X-User: admin'"))["csrf_token"]
                session[m.name] = (sid, csrf)
            for m in (b, c):
                token = ok(api(a, "POST", "/api/config/peers/token", {}), "join code")["token"]
                ok(api(m, "POST", "/api/config/peers/join", {"url": "http://a", "token": token, "self_url": f"http://{m.name}"}), "join")
            peers = {p["name"]: p["id"] for p in api(a, "GET", "/api/config/sync/status")["status"]["peers"]}
            assert set(peers) == {"b", "c"}, peers

        with subtest("Two nodes: two_node, no automatic failover"):
            s = api(a, "GET", f"/api/quorum/suggest?peer_id={peers['b']}")
            ok(api(a, "POST", "/api/quorum/cluster", {"peer_id": peers["b"], "local_addr": s["local_addr"], "peer_addr": s["peer_addr"]}), "form cluster")
            for m in (a, b):
                votes(m, 2)
            st = api(a, "GET", "/api/quorum/status")
            assert "2Node" in (st["status"]["flags"] or []) and not st["info"]["auto_failover"], st

        with subtest("A Pi-like machine joins as voter with the one-line command"):
            code = ok(api(a, "POST", "/api/quorum/third-vote/code", {}), "code")["code"]
            out = v.succeed(f"curl -fsS http://a/api/quorum/witness-setup.sh | sh -s -- --voter http://a {code} 2>&1")
            print(out)
            assert "voter of cluster" in out, out
            for m in (a, b):
                votes(m, 3)
            v.wait_until_succeeds("systemctl is-active corosync")
            v.succeed("systemctl list-timers | grep -q dplaneos-voter-sync")
            st = api(a, "GET", "/api/quorum/status")
            names = {n["name"]: n for n in st["cluster"]["nodes"]}
            assert names["v"].get("voter") is True and "2Node" not in (st["status"]["flags"] or []), st
            assert st["info"]["auto_failover"] is True, st["info"]
            # The code works only once.
            v.fail(f"curl -fsS http://a/api/quorum/witness-setup.sh | sh -s -- --voter http://a {code}")

        with subtest("b is cut off: a keeps quorum with the voter"):
            b.block()
            a.wait_until_succeeds("(corosync-quorumtool -s || true) | grep -q 'Quorate: *Yes'", timeout=60)
            b.wait_until_succeeds("(corosync-quorumtool -s || true) | grep -q 'Quorate: *No'", timeout=60)
            b.unblock()
            for m in (a, b):
                votes(m, 3)

        with subtest("c joins as a DPlaneOS member; the voter picks up the new member list"):
            s = api(a, "GET", f"/api/quorum/suggest?peer_id={peers['c']}")
            ok(api(a, "POST", "/api/quorum/nodes", {"peer_id": peers["c"], "peer_addr": s["peer_addr"]}), "add c")
            c.wait_until_succeeds("systemctl is-active dplaneos-corosync", timeout=60)
            # The voter pulls within a minute and reloads corosync.
            v.succeed("systemctl start dplaneos-voter-sync.service")
            v.wait_until_succeeds(f"grep -q 'ring0_addr: {s['peer_addr']}' /etc/corosync/corosync.conf", timeout=120)
            for m in (a, b, c):
                votes(m, 4)

        with subtest("Remove the voter"):
            ok(api(a, "DELETE", "/api/quorum/nodes/v"), "remove voter")
            for m in (a, b, c):
                votes(m, 3)
            st = api(a, "GET", "/api/quorum/status")
            assert "v" not in {n["name"] for n in st["cluster"]["nodes"]}, st
            # Its token no longer works.
            v.succeed("systemctl start dplaneos-voter-sync.service")
            v.succeed("grep -q 'name: v' /etc/corosync/corosync.conf")  # kept, nothing pulled

        with subtest("No daemon panics, security refusals or security warnings"):
            for m in (a, b, c):
                bad = m.execute("journalctl -b --no-pager -u dplaned | grep -E 'panic:|SECURITY WARNING|security refusal' || true")[1].strip()
                assert bad == "", f"{m.name}: {bad}"
    except Exception:
        diag()
        raise
  '';
}
