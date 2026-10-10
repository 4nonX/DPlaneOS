# nixos/tests/witness-appliance.nix
# ─────────────────────────────────────────────────────────────────────────────
# The DPlaneOS Witness appliance (nixos/witness.nix, the module inside the
# Raspberry Pi and x86 images) joins a cluster headlessly: a text file on the
# boot partition with the node's address and the one-time code, as a user
# would write it to the SD card. a, b: DPlaneOS nodes; w: the witness.
#
# Run: nix build .#checks.x86_64-linux.witness-appliance -L
# ─────────────────────────────────────────────────────────────────────────────
{ nixpkgs, system ? "x86_64-linux", impermanence, daemonPackage, frontendPackage, ... }:

let
  pkgs = nixpkgs.legacyPackages.${system};
  dplaneNode = hostName: hostId: { lib, ... }: {
    imports = [ ../configuration-live.nix impermanence.nixosModules.impermanence ../module.nix ];
    services.dplaneos = { enable = true; inherit daemonPackage frontendPackage; dbPath = "/var/lib/dplaneos/pgsql"; };
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
  name = "dplaneos-witness-appliance";

  nodes.a = dplaneNode "a" "aaaa0001";
  nodes.b = dplaneNode "b" "bbbb0002";
  nodes.w = { lib, ... }: {
    imports = [ ../witness.nix ];
    networking.hostName = lib.mkForce "w";
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
        return json.loads(m.succeed(cmd))

    def ok(resp, what):
        if resp.get("success") is not True:
            raise Exception(f"{what}: {resp}")
        return resp

    def diag():
        for m in (a, b):
            print(m.execute("journalctl -b --no-pager -u dplaned | grep -i -E 'quorum|error' | tail -n 40; corosync-quorumtool -s 2>&1")[1])
        print(w.execute("dplaneos-witness status; cat /boot/dplaneos-witness.txt.* 2>&1; journalctl -b --no-pager -u dplaneos-witness-autojoin -u corosync-qnetd | tail -n 60")[1])

    try:
        with subtest("Pair a and b, form the cluster"):
            for m in (a, b):
                api(m, "POST", "/api/system/setup-admin", {"username": "admin", "password": "Witness-Test-Pass-1"}, auth=False)
                sid = api(m, "POST", "/api/auth/login", {"username": "admin", "password": "Witness-Test-Pass-1"}, auth=False)["session_id"]
                csrf = json.loads(m.succeed(f"curl -s http://localhost/api/csrf -H 'X-Session-ID: {sid}' -H 'X-User: admin'"))["csrf_token"]
                session[m.name] = (sid, csrf)
            token = ok(api(a, "POST", "/api/config/peers/token", {}), "join code")["token"]
            ok(api(b, "POST", "/api/config/peers/join", {"url": "http://a", "token": token, "self_url": "http://b"}), "join")
            peer_b = api(a, "GET", "/api/config/sync/status")["status"]["peers"][0]["id"]
            s = api(a, "GET", f"/api/quorum/suggest?peer_id={peer_b}")
            ok(api(a, "POST", "/api/quorum/cluster", {"peer_id": peer_b, "local_addr": s["local_addr"], "peer_addr": s["peer_addr"]}), "form cluster")
            a.wait_until_succeeds("(corosync-quorumtool -s || true) | grep -q 'Total votes: *2'", timeout=90)

        with subtest("The witness has no role yet and says how to join"):
            out = w.succeed("dplaneos-witness status")
            assert "Role: none yet" in out, out

        with subtest("Headless join: dplaneos-witness.txt on the boot partition"):
            code = ok(api(a, "POST", "/api/quorum/third-vote/code", {}), "code")["code"]
            w.succeed("mkdir -p /boot")
            w.succeed(f"printf 'node=http://a\r\ncode={code}\r\nmode=qdevice\r\n' > /boot/dplaneos-witness.txt")
            w.succeed("systemctl restart dplaneos-witness-autojoin.service")
            w.wait_until_succeeds("test -f /boot/dplaneos-witness.txt.done", timeout=120)
            w.wait_for_unit("corosync-qnetd.service")
            for m in (a, b):
                m.wait_until_succeeds("(corosync-quorumtool -s || true) | grep -q 'Total votes: *3'", timeout=120)
            # The quorum monitor refreshes every few seconds: poll, do not
            # read its snapshot once right after the vote arrived.
            for _ in range(30):
                if api(a, "GET", "/api/quorum/status")["info"]["auto_failover"] is True:
                    break
                a.sleep(2)
            else:
                raise Exception("auto_failover did not turn on with three votes")
            out = w.succeed("dplaneos-witness status")
            assert "QDevice" in out, out

        with subtest("It survives a reboot of the witness"):
            w.shutdown()
            w.start()
            w.wait_for_unit("corosync-qnetd.service")
            for m in (a, b):
                m.wait_until_succeeds("(corosync-quorumtool -s || true) | grep -q 'Total votes: *3'", timeout=180)
    except Exception:
        diag()
        raise
  '';
}
