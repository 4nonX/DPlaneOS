# nixos/tests/quorum.nix
# ─────────────────────────────────────────────────────────────────────────────
# Design 0001 phase 3a exit test: cluster quorum and the third vote, end to
# end through the API, on three VMs:
#
#   a, b  DPlaneOS nodes (same configuration as the live-boot test)
#   w     a plain Linux machine with corosync-qnetd installed, standing in for
#         a Raspberry Pi or VM; it is enrolled with the one-line command the
#         GUI shows (witness-setup.sh), no SSH involved
#
# Steps: pair a and b (Configuration Sync), form the cluster from a, check
# two-node quorum and that automatic failover is reported off; add the third
# vote; check three votes and failover reported on; cut b off the network:
# a keeps quorum with the third vote, b loses it; reconnect; remove the third
# vote and the cluster.
#
# Run: nix build .#checks.x86_64-linux.quorum -L
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
    virtualisation.memorySize = 2048;
    virtualisation.diskImage = null;
  };
in

pkgs.testers.nixosTest {
  name = "dplaneos-quorum";

  nodes.a = dplaneNode "a" "aaaa0001";
  nodes.b = dplaneNode "b" "bbbb0002";

  # A generic Linux host with corosync-qnetd "installed by its package
  # manager": the package plus a corosync-qnetd unit, as distributions ship it.
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
    # The witness's firewall must allow TCP 5403. The script opens it in ufw
    # and firewalld; on other firewalls (like this one) it is the admin's step,
    # and the enrollment warns when the port is not reachable.
    networking.firewall.enable = true;
    networking.firewall.allowedTCPPorts = [ 5403 ];
    virtualisation.memorySize = 512;
  };

  testScript = ''
    import json, shlex

    start_all()
    for m in (a, b):
        m.wait_for_unit("dplaned.service")
        m.wait_until_succeeds("curl -sf http://localhost/api/system/status >/dev/null", timeout=180)
    w.wait_for_unit("multi-user.target")

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

    def diag():
        for m in (a, b):
            print(m.execute("journalctl -b --no-pager -u dplaned -u dplaneos-corosync -u dplaneos-qdevice | tail -n 120")[1])
            print(m.execute("corosync-quorumtool -s 2>&1; cat /var/lib/dplaneos/corosync/corosync.conf 2>&1")[1])
        print(w.execute("journalctl -b --no-pager -u corosync-qnetd | tail -n 60; corosync-qnetd-tool -l 2>&1")[1])

    try:
        with subtest("Log in on both nodes"):
            for m in (a, b):
                api(m, "POST", "/api/system/setup-admin", {"username": "admin", "password": "Quorum-Test-Pass-1"}, auth=False)
                r = api(m, "POST", "/api/auth/login", {"username": "admin", "password": "Quorum-Test-Pass-1"}, auth=False)
                sid = r["session_id"]
                csrf = json.loads(m.succeed(f"curl -s http://localhost/api/csrf -H 'X-Session-ID: {sid}' -H 'X-User: admin'"))["csrf_token"]
                session[m.name] = (sid, csrf)

        with subtest("Pair the nodes"):
            token = ok(api(a, "POST", "/api/config/peers/token", {}), "join code")["token"]
            ok(api(b, "POST", "/api/config/peers/join", {"url": "http://a", "token": token, "self_url": "http://b"}), "join")
            peers = api(a, "GET", "/api/config/sync/status")["status"]["peers"]
            assert len(peers) == 1, peers
            peer_b = peers[0]["id"]

        with subtest("Form the cluster with suggested addresses"):
            s = api(a, "GET", f"/api/quorum/suggest?peer_id={peer_b}")
            assert s.get("local_addr") and s.get("peer_addr"), s
            ok(api(a, "POST", "/api/quorum/cluster", {"peer_id": peer_b, "local_addr": s["local_addr"], "peer_addr": s["peer_addr"]}), "form cluster")
            for m in (a, b):
                m.wait_until_succeeds("corosync-quorumtool -s | grep -q 'Quorate: *Yes'", timeout=90)
                m.wait_until_succeeds("corosync-quorumtool -s | grep -q 'Total votes: *2'", timeout=60)
            st = api(a, "GET", "/api/quorum/status")
            assert st["configured"] and st["status"]["quorate"], st
            assert "2Node" in (st["status"]["flags"] or []), st["status"]
            assert st["info"]["auto_failover"] is False and "third vote" in st["info"]["auto_failover_reason"], st["info"]

        with subtest("Add the third vote with the one-line command"):
            code = ok(api(a, "POST", "/api/quorum/third-vote/code", {}), "third-vote code")["code"]
            out = w.succeed(f"curl -fsS http://a/api/quorum/witness-setup.sh | sh -s -- http://a {code} 2>&1")
            print(out)
            assert "Done" in out, out
            assert "cannot reach" not in out, out
            for m in (a, b):
                m.wait_until_succeeds("corosync-quorumtool -s | grep -q 'Flags:.*Qdevice'", timeout=90)
                m.wait_until_succeeds("corosync-quorumtool -s | grep -q 'Total votes: *3'", timeout=90)
            st = api(a, "GET", "/api/quorum/status")
            assert st["info"]["expected_votes"] == 3 and st["info"]["auto_failover"] is True, st["info"]
            assert st["cluster"]["qdevice"]["host"], st["cluster"]

        with subtest("The code works only once"):
            w.fail(f"curl -fsS http://a/api/quorum/witness-setup.sh | sh -s -- http://a {code} 2>&1")

        with subtest("Cut b off: a keeps quorum through the third vote, b loses it"):
            b.block()
            a.wait_until_succeeds("corosync-quorumtool -s | grep -q 'Total votes: *2'", timeout=120)
            a.succeed("corosync-quorumtool -s | grep -q 'Quorate: *Yes'")
            b.wait_until_succeeds("corosync-quorumtool -s | grep -q 'Quorate: *No'", timeout=120)
            st = api(b, "GET", "/api/quorum/status")
            assert st["info"]["auto_failover"] is False, st["info"]
            b.unblock()
            for m in (a, b):
                m.wait_until_succeeds("corosync-quorumtool -s | grep -q 'Total votes: *3'", timeout=180)

        with subtest("Remove the third vote, then the cluster"):
            ok(api(a, "DELETE", "/api/quorum/third-vote"), "remove third vote")
            for m in (a, b):
                m.wait_until_succeeds("corosync-quorumtool -s | grep -q 'Expected votes: *2'", timeout=90)
            ok(api(a, "DELETE", "/api/quorum/cluster"), "remove cluster")
            for m in (a, b):
                m.wait_until_fails("systemctl is-active dplaneos-corosync", timeout=60)
            assert api(b, "GET", "/api/quorum/status")["configured"] is False
    except Exception:
        diag()
        raise
  '';
}
