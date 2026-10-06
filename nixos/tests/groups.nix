# nixos/tests/groups.nix
# ─────────────────────────────────────────────────────────────────────────────
# Design 0001 phase 3b exit test: storage groups and epochs on shared storage.
#
# Two DPlaneOS nodes attached to the same virtual disk (one image, shared
# read-write, like a SAS/SATA JBOD cabled to both). Steps: form the cluster,
# create a pool with ZFS multihost protection, create a shared storage group
# on a; b receives it. Planned move to b: a exports, b imports, epoch 2.
# a cannot import the pool while b holds it (multihost). A node that missed
# the move (a's copy wound back to epoch 1) learns the new owner by pulling.
# Move back to a: epoch 3.
#
# Run: nix build .#checks.x86_64-linux.groups -L
# ─────────────────────────────────────────────────────────────────────────────
{ nixpkgs, system ? "x86_64-linux", impermanence, daemonPackage, frontendPackage, ... }:

let
  pkgs = nixpkgs.legacyPackages.${system};
  sharedImage = "/tmp/dplaneos-shared-disk.img";

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
    # The shared disk: same image on both VMs, no image locking, shared
    # read-write; the serial gives /dev/disk/by-id/virtio-dplshared.
    virtualisation.qemu.options = [
      "-drive file=${sharedImage},if=none,id=shared,format=raw,file.locking=off,cache=none"
      "-device virtio-blk-pci,drive=shared,share-rw=on,serial=dplshared"
    ];
  };
in

pkgs.testers.nixosTest {
  name = "dplaneos-groups";

  nodes.a = dplaneNode "a" "aaaa0001";
  nodes.b = dplaneNode "b" "bbbb0002";

  testScript = ''
    import json, shlex

    with open("${sharedImage}", "wb") as f:
        f.truncate(1 << 30)

    start_all()
    for m in (a, b):
        m.wait_for_unit("dplaned.service")
        m.wait_until_succeeds("curl -sf http://localhost/api/system/status >/dev/null", timeout=180)

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

    def group(m):
        gs = api(m, "GET", "/api/groups")["groups"]
        return gs[0] if gs else None

    def diag():
        for m in (a, b):
            print(m.execute("journalctl -b --no-pager -u dplaned | grep -i -E 'group|zpool|error' | tail -n 80")[1])
            print(m.execute("zpool status 2>&1; zpool import -d /dev/disk/by-id 2>&1 | head -20")[1])

    try:
        with subtest("Log in, pair, form the cluster"):
            for m in (a, b):
                api(m, "POST", "/api/system/setup-admin", {"username": "admin", "password": "Groups-Test-Pass-1"}, auth=False)
                sid = api(m, "POST", "/api/auth/login", {"username": "admin", "password": "Groups-Test-Pass-1"}, auth=False)["session_id"]
                csrf = json.loads(m.succeed(f"curl -s http://localhost/api/csrf -H 'X-Session-ID: {sid}' -H 'X-User: admin'"))["csrf_token"]
                session[m.name] = (sid, csrf)
            token = ok(api(a, "POST", "/api/config/peers/token", {}), "join code")["token"]
            ok(api(b, "POST", "/api/config/peers/join", {"url": "http://a", "token": token, "self_url": "http://b"}), "join")
            peer_b = api(a, "GET", "/api/config/sync/status")["status"]["peers"][0]["id"]
            s = api(a, "GET", f"/api/quorum/suggest?peer_id={peer_b}")
            ok(api(a, "POST", "/api/quorum/cluster", {"peer_id": peer_b, "local_addr": s["local_addr"], "peer_addr": s["peer_addr"]}), "form cluster")
            for m in (a, b):
                m.wait_until_succeeds("(corosync-quorumtool -s || true) | grep -q 'Quorate: *Yes'", timeout=90)

        with subtest("Both nodes see the shared disk; pool with multihost on a"):
            for m in (a, b):
                m.wait_until_succeeds("test -b /dev/disk/by-id/virtio-dplshared", timeout=30)
            a.succeed("zpool create -f -o multihost=on -m /mnt/tank tank /dev/disk/by-id/virtio-dplshared")
            a.succeed("zfs create tank/data && echo hello > /mnt/tank/data/file")

        with subtest("Create a shared storage group on a; b receives it"):
            ok(api(a, "POST", "/api/groups", {"name": "data", "topology": "shared", "pools": ["tank"], "candidates": [peer_b]}), "create group")
            g = group(a)
            assert g["owner"] != peer_b and g["epoch"] == 1 and g["role"] == "owner" and g["can_write"], g
            b.wait_until_succeeds("curl -s http://localhost/api/groups -H 'X-Session-ID: %s' -H 'X-User: admin' | grep -q '\"name\":\"data\"'" % session["b"][0], timeout=60)
            gb = group(b)
            assert gb["role"] == "standby" and gb["epoch"] == 1 and gb["problems"] == [], gb

        with subtest("Planned move to b: a exports, b imports, epoch 2"):
            ok(api(a, "POST", "/api/groups/data/move", {"target": peer_b}), "move")
            b.succeed("zpool list tank")
            b.succeed("grep -q hello /mnt/tank/data/file")
            a.fail("zpool list tank")
            ga, gb = group(a), group(b)
            assert ga["epoch"] == 2 and gb["epoch"] == 2 and gb["owner"] == peer_b, (ga, gb)
            assert gb["role"] == "owner" and gb["can_write"] and ga["role"] == "standby", (ga, gb)

        with subtest("a cannot import the pool while b holds it (multihost)"):
            guid = b.succeed("zpool get -H -o value guid tank").strip()
            a.fail(f"zpool import -d /dev/disk/by-id {guid}")
            b.succeed("zpool list tank")

        with subtest("A node that missed the move learns the new owner by pulling"):
            a.succeed("sudo -u postgres psql -d dplaneos -c \"UPDATE storage_groups SET epoch = 1, owner = (SELECT value FROM settings WHERE key = 'config_node_id') WHERE name = 'data'\"")
            assert group(a)["epoch"] == 1
            a.wait_until_succeeds("sudo -u postgres psql -d dplaneos -tAc \"SELECT epoch FROM storage_groups WHERE name = 'data'\" | grep -qx 2", timeout=60)
            ga = group(a)
            assert ga["owner"] == peer_b and ga["role"] == "standby", ga
            a.fail("zpool list tank")

        with subtest("Move back to a: epoch 3"):
            ok(api(b, "POST", "/api/groups/data/move", {"target": group(b)["candidates"][0]}), "move back")
            a.succeed("zpool list tank && grep -q hello /mnt/tank/data/file")
            b.fail("zpool list tank")
            assert group(a)["epoch"] == 3 and group(b)["epoch"] == 3
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
