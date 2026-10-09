# nixos/tests/ha-migrate.nix
# ─────────────────────────────────────────────────────────────────────────────
# Design 0001 phase 3e exit test: an HA pair moves off the shared Patroni
# database.
#
#   a, b  DPlaneOS nodes with services.dplaneos.ha (Patroni, etcd, HAProxy,
#         keepalived holding 192.168.1.100), attached to one shared disk
#   w     the etcd witness (patroni-witness.nix)
#
# The pool is created on a. The migration is started on a from the API; each
# node applies a NixOS configuration without Patroni (here: a specialisation,
# since a VM test cannot run nixos-rebuild), keeps its copy of the database,
# gets its own configuration identity and is paired with the other node; a
# forms the Corosync cluster and creates the storage group with the floating
# address from keepalived. A planned move to b then proves the new stack.
#
# Run: nix build .#checks.x86_64-linux.ha-migrate -L
# ─────────────────────────────────────────────────────────────────────────────
{ nixpkgs, system ? "x86_64-linux", impermanence, daemonPackage, frontendPackage, ... }:

let
  pkgs = nixpkgs.legacyPackages.${system};
  sharedImage = "/tmp/dplaneos-migrate-disk.img";
  ipA = "192.168.1.1";
  ipB = "192.168.1.2";
  ipW = "192.168.1.3";

  dplaneNode = hostName: hostId: localIP: peerIP: role: { lib, ... }: {
    imports = [
      ../configuration-live.nix
      impermanence.nixosModules.impermanence
      ../module.nix
    ];
    services.dplaneos = {
      enable = true;
      inherit daemonPackage frontendPackage;
      dbPath = "/var/lib/dplaneos/pgsql";
      # An HA pair shares one secrets key (32 bytes).
      secrets.keyFile = "${pkgs.writeText "dplaneos-test-secrets-key" "0123456789abcdef0123456789abcdef"}";
      ha = {
        enable = lib.mkForce true; # dplane-generated.nix sets it from the (absent) state file
        inherit role;
        localAddress = localIP;
        peerAddress = peerIP;
        witnessAddress = ipW;
        etcdEndpoints = [ "http://${localIP}:2379" "http://${peerIP}:2379" "http://${ipW}:2379" ];
        virtualIP = "192.168.1.100";
        interface = "eth1";
      };
    };
    # After the migration: no Patroni, the database stays (what the daemon's
    # NixOS state produces through dplane-generated.nix on a real node).
    specialisation.split.configuration = {
      services.dplaneos.ha.enable = lib.mkOverride 40 false;
      services.dplaneos.database.fromPatroni = true;
    };
    # nixos-rebuild cannot run in a VM test: the daemon's switch goes to the
    # specialisation instead.
    systemd.services.dplaneos-apply-config.script = lib.mkForce ''
      exec /run/booted-system/specialisation/split/bin/switch-to-configuration switch
    '';
    networking.hostName = lib.mkForce hostName;
    networking.hostId   = lib.mkForce hostId;
    networking.firewall.allowedTCPPorts = [ 2379 2380 5000 5432 8008 ];
    networking.firewall.extraCommands = "iptables -A nixos-fw -p vrrp -j nixos-fw-accept";
    systemd.services.dplane-zfs-auto-import.enable = lib.mkForce false;
    boot.kernelParams = [ ];
    systemd.services.systemd-random-seed.enable = false;
    virtualisation.cores = 2;
    virtualisation.memorySize = 2560;
    virtualisation.qemu.options = [
      "-drive file=${sharedImage},if=none,id=shared,format=raw,file.locking=off,cache=none"
      "-device virtio-blk-pci,drive=shared,share-rw=on,serial=dplshared"
    ];
  };
in

pkgs.testers.nixosTest {
  name = "dplaneos-ha-migrate";

  nodes.a = dplaneNode "a" "aaaa0001" ipA ipB "primary";
  nodes.b = dplaneNode "b" "bbbb0002" ipB ipA "secondary";
  nodes.w = { ... }: {
    imports = [ ../patroni-witness.nix ];
    virtualisation.memorySize = 512;
    services.dplaneos.ha.witness = {
      enable = true;
      localAddress = ipW;
      nodeAAddress = ipA;
      nodeBAddress = ipB;
    };
  };

  testScript = ''
    import json, shlex, time

    with open("${sharedImage}", "wb") as f:
        f.truncate(1 << 30)

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
        api(m, "POST", "/api/system/setup-admin", {"username": "admin", "password": "Migrate-Test-Pass-1"}, auth=False)
        sid = api(m, "POST", "/api/auth/login", {"username": "admin", "password": "Migrate-Test-Pass-1"}, auth=False)["session_id"]
        csrf = json.loads(m.succeed(f"curl -s http://localhost/api/csrf -H 'X-Session-ID: {sid}' -H 'X-User: admin'"))["csrf_token"]
        session[m.name] = (sid, csrf)

    def wait_for(fn, what, timeout):
        deadline = time.time() + timeout
        while True:
            try:
                v = fn()
            except Exception as e:
                v = None
                print(f"{what}: {e}")
            if v:
                return v
            assert time.time() < deadline, f"timed out: {what}"
            time.sleep(3)

    def diag():
        for m in (a, b):
            for u in ("dplaned", "dplaneos-apply-config", "dplaneos-patroni-adopt", "dplaneos-patroni-split", "postgresql", "patroni"):
                print(m.execute(f"journalctl -b --no-pager -u {u} | tail -n 40")[1])
            print(m.execute("curl -s http://localhost:8008/cluster; zpool status 2>&1; ip -o addr show dev eth1")[1])

    def group(m):
        gs = api(m, "GET", "/api/groups")["groups"]
        return gs[0] if gs else None

    start_all()
    try:
        with subtest("Patroni pair with a witness; dplaned on both nodes uses the shared database"):
            w.wait_for_unit("etcd.service")
            for m in (a, b):
                m.wait_for_unit("patroni.service")
            a.wait_until_succeeds("curl -sf http://localhost:8008/cluster | grep -q leader", timeout=240)
            a.wait_until_succeeds("curl -s http://localhost:8008/cluster | grep -q -E 'streaming|running.*replica|replica.*running'", timeout=240)
            for m in (a, b):
                m.wait_for_unit("dplaned.service")
                m.wait_until_succeeds("curl -sf http://localhost/api/system/status >/dev/null", timeout=240)
            login(a)
            login(b)  # the admin exists once, in the shared database
            a.wait_until_succeeds("ip -o addr show dev eth1 | grep -q 192.168.1.100", timeout=60)

        with subtest("A pool on the shared disk, imported on a"):
            a.succeed("zpool create -f -o multihost=on -O mountpoint=/mnt/tank tank /dev/disk/by-id/virtio-dplshared")
            a.succeed("zfs create tank/data && echo before > /mnt/tank/data/file")

        with subtest("Preflight on a: pools, floating address from keepalived"):
            st = api(a, "GET", "/api/ha/split")
            assert st["shared_database"], st
            pf = st["preflight"]
            print(pf)
            assert pf["problems"] == [], pf["problems"]
            assert "tank" in pf["pools"], pf
            assert pf["address"] == "192.168.1.100/24" and pf["interface"] == "eth1", pf

        with subtest("Start the migration on a; both nodes leave Patroni"):
            ok(api(a, "POST", "/api/ha/split", {"group_name": "data", "topology": "shared", "pools": ["tank"],
                                               "address": pf["address"], "interface": pf["interface"]}), "start migration")
            for m in (a, b):
                m.wait_until_succeeds("systemctl is-active postgresql.service", timeout=600)
                m.wait_until_fails("systemctl is-active patroni.service", timeout=60)
                m.wait_until_succeeds("systemctl is-active dplaneos-patroni-split.service", timeout=120)
                m.wait_until_succeeds("curl -sf http://localhost/api/system/status >/dev/null", timeout=240)
            for u in ("etcd", "haproxy", "keepalived"):
                for m in (a, b):
                    m.fail(f"systemctl is-active {u}.service")

        with subtest("Each node has its own database, its own identity, and is paired with the other"):
            ida = api(a, "GET", "/api/config/sync/status")["status"]
            idb = api(b, "GET", "/api/config/sync/status")["status"]
            print(ida, idb)
            assert ida["node_id"] != idb["node_id"], (ida, idb)
            assert [p["id"] for p in ida["peers"]] == [idb["node_id"]], ida
            assert [p["id"] for p in idb["peers"]] == [ida["node_id"]], idb

        with subtest("a forms the cluster and creates the storage group with the floating address"):
            wait_for(lambda: (api(a, "GET", "/api/ha/split").get("plan") or {}).get("state") == "finished", "migration finished", 300)
            for m in (a, b):
                m.wait_until_succeeds("corosync-quorumtool -s | grep -q -E 'Quorate:\\s+Yes'", timeout=120)
            ga = group(a)
            assert ga["name"] == "data" and ga["topology"] == "shared" and ga["can_write"], ga
            assert ga["address"] == "192.168.1.100/24", ga
            a.wait_until_succeeds("ip -o addr show dev eth1 | grep -q 192.168.1.100/24", timeout=60)
            wait_for(lambda: (group(b) or {}).get("role") == "standby", "b sees the group", 120)
            a.succeed("grep -q before /mnt/tank/data/file")

        with subtest("The new stack works: planned move to b"):
            peer_b = api(a, "GET", "/api/config/sync/status")["status"]["peers"][0]["id"]
            ok(api(a, "POST", "/api/groups/data/move", {"target": peer_b}), "move")
            b.succeed("zpool list tank && grep -q before /mnt/tank/data/file")
            a.fail("zpool list tank")
            b.wait_until_succeeds("ip -o addr show dev eth1 | grep -q 192.168.1.100/24", timeout=60)
            a.fail("ip -o addr show dev eth1 | grep -q 192.168.1.100/24")

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
