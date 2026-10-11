# nixos/tests/reservations.nix
# ─────────────────────────────────────────────────────────────────────────────
# Disk reservations (SCSI-3 persistent reservations) on shared storage.
#
#   a, b  DPlaneOS nodes, both connected to the same two disks
#   san   the disks: an iSCSI target (LIO), which implements persistent
#         reservations the way dual-ported SAS disks do. The storage network
#         is separate from the cluster network, as SAS cabling is.
#
# What is shown:
#   - the node that serves a shared storage group holds the reservation on
#     every disk of its pool; the other node cannot write to them;
#   - a planned move hands the reservation over;
#   - the case the other protections cannot cover: the owner is alive and
#     writing, but cut off from the cluster. The operator takes the group
#     over on the other node. The disks are taken from the owner first, its
#     writes are refused from that moment, the pool is imported and is
#     intact; the previous owner reports that it was shut out and takes
#     nothing back;
#   - after a restart the previous owner follows the new one.
#
# Run: nix build .#checks.x86_64-linux.reservations -L
# ─────────────────────────────────────────────────────────────────────────────
{ nixpkgs, system ? "x86_64-linux", impermanence, daemonPackage, frontendPackage, ... }:

let
  pkgs = nixpkgs.legacyPackages.${system};
  iqn = "iqn.2026-10.test.dplaneos:shared";

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
    # As on an installed node: only the owner's daemon imports shared pools.
    systemd.services.dplane-zfs-auto-import.enable = lib.mkForce false;
    systemd.services.systemd-random-seed.enable = false;
    # The way to the disks in this test.
    services.openiscsi = { enable = true; name = "iqn.2026-10.test.dplaneos:${hostName}"; };
    # 1: cluster network, 2: storage network.
    virtualisation.vlans = [ 1 2 ];
    virtualisation.cores = 2;
    virtualisation.memorySize = 2048;
  };
in

pkgs.testers.nixosTest {
  name = "dplaneos-reservations";

  nodes.a = dplaneNode "a" "aaaa0001";
  nodes.b = dplaneNode "b" "bbbb0002";
  nodes.san = { pkgs, ... }: {
    virtualisation.vlans = [ 2 ];
    virtualisation.memorySize = 1024;
    boot.kernelModules = [ "configfs" "target_core_mod" "target_core_file" "iscsi_target_mod" ];
    environment.systemPackages = [ pkgs.targetcli-fb ];
    networking.firewall.allowedTCPPorts = [ 3260 ];
    systemd.tmpfiles.rules = [
      "d /etc/target 0755 root root -"
      "d /etc/target/pr 0755 root root -"
      "d /etc/target/alua 0755 root root -"
      "d /var/target 0755 root root -"
      "d /var/target/pr 0755 root root -"
      "d /var/target/alua 0755 root root -"
      "d /srv/disks 0755 root root -"
    ];
  };

  testScript = ''
    import json, shlex, time

    IQN = "${iqn}"
    SG_PERSIST = "${pkgs.sg3_utils}/bin/sg_persist"

    start_all()
    san.wait_for_unit("multi-user.target")
    for m in (a, b):
        m.wait_for_unit("dplaned.service")
        m.wait_until_succeeds("curl -sf http://localhost/api/system/status >/dev/null", timeout=180)

    session = {}

    def api(m, method, path, body=None, auth=True):
        cmd = f"curl -s --max-time 240 -X {method} http://localhost{path} -H 'Content-Type: application/json'"
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
        api(m, "POST", "/api/system/setup-admin", {"username": "admin", "password": "Reserve-Test-Pass-1"}, auth=False)
        sid = api(m, "POST", "/api/auth/login", {"username": "admin", "password": "Reserve-Test-Pass-1"}, auth=False)["session_id"]
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

    def disks(m):
        # the two shared disks on this node (device nodes), in LUN order
        out = m.succeed("for l in 0 1; do readlink -f /dev/disk/by-path/*-iscsi-" + IQN + "-lun-$l; done").split()
        assert len(out) == 2, out
        return out

    def reservation(m):
        st = ok(api(m, "GET", "/api/ha/scsi/status"), "reservation status")
        return st["pools"][0] if st["pools"] else None

    def keys(m, dev):
        # what the disk itself says, asked with another tool
        out = m.succeed(f"{SG_PERSIST} --in --read-keys {dev}")
        return [l.strip() for l in out.splitlines() if l.strip().startswith("0x")]

    def can_write(m, dev):
        # A raw write to the disk, into the 8 MiB ZFS leaves unused at the
        # end of a whole disk (its partition 9), 4 MiB before the end:
        # harmless where it succeeds.
        sectors = int(m.succeed(f"blockdev --getsz {dev}").strip())
        assert sectors > 1000000, sectors
        return m.execute(f"dd if=/dev/zero of={dev} bs=512 count=8 seek={sectors - 8192} oflag=direct conv=fsync 2>&1")[0] == 0

    def diag():
        for m in (a, b):
            print(m.execute("journalctl -b --no-pager -u dplaned | grep -i -E 'group|reserv|zpool|error|shut out' | tail -n 80")[1])
            print(m.execute("timeout 20 zpool status 2>&1; lsblk -o NAME,TYPE,FSTYPE,UUID,TRAN 2>&1")[1])
            for d in m.execute("ls /dev/disk/by-path/ | grep -- '-lun-[01]$'")[1].split():
                print(m.execute(f"{SG_PERSIST} --in --read-keys /dev/disk/by-path/{d} 2>&1; {SG_PERSIST} --in --read-reservation /dev/disk/by-path/{d} 2>&1")[1])
        print(san.execute("dmesg | tail -n 40")[1])

    try:
        with subtest("Two disks, reachable from both nodes"):
            san.succeed("truncate -s 1G /srv/disks/d0.img /srv/disks/d1.img")
            san.succeed("targetcli /backstores/fileio create d0 /srv/disks/d0.img")
            san.succeed("targetcli /backstores/fileio create d1 /srv/disks/d1.img")
            san.succeed(f"targetcli /iscsi create {IQN}")
            san.succeed(f"targetcli /iscsi/{IQN}/tpg1/luns create /backstores/fileio/d0")
            san.succeed(f"targetcli /iscsi/{IQN}/tpg1/luns create /backstores/fileio/d1")
            for n in ("a", "b"):
                san.succeed(f"targetcli /iscsi/{IQN}/tpg1/acls create iqn.2026-10.test.dplaneos:{n}")
            san.succeed(f"targetcli /iscsi/{IQN}/tpg1 set attribute authentication=0 demo_mode_write_protect=0")
            for m in (a, b):
                m.wait_for_unit("iscsid.service")
                m.wait_until_succeeds(f"iscsiadm -m discovery -t sendtargets -p san | grep -q {IQN}", timeout=90)
                m.succeed(f"iscsiadm -m node -T {IQN} -p san --login")
                m.wait_until_succeeds(f"test -b /dev/disk/by-path/*-iscsi-{IQN}-lun-0 && test -b /dev/disk/by-path/*-iscsi-{IQN}-lun-1", timeout=60)
                assert keys(m, disks(m)[0]) == [], "a new disk has registrations"

        with subtest("Log in, pair, form the cluster"):
            for m in (a, b):
                login(m)
            token = ok(api(a, "POST", "/api/config/peers/token", {}), "join code")["token"]
            ok(api(b, "POST", "/api/config/peers/join", {"url": "http://a", "token": token, "self_url": "http://b"}), "join")
            peer_b = api(a, "GET", "/api/config/sync/status")["status"]["peers"][0]["id"]
            peer_a = api(b, "GET", "/api/config/sync/status")["status"]["peers"][0]["id"]
            s = api(a, "GET", f"/api/quorum/suggest?peer_id={peer_b}")
            ok(api(a, "POST", "/api/quorum/cluster", {"peer_id": peer_b, "local_addr": s["local_addr"], "peer_addr": s["peer_addr"]}), "form cluster")
            for m in (a, b):
                m.wait_until_succeeds("(corosync-quorumtool -s || true) | grep -q 'Quorate: *Yes'", timeout=90)

        with subtest("The probe says the disks have reservations"):
            da = disks(a)
            a.succeed(f"zpool create -f -o multihost=on -m /mnt/tank tank mirror {da[0]} {da[1]}")
            a.succeed("zfs create tank/data && echo hello > /mnt/tank/data/file")
            probe = ok(api(a, "POST", "/api/ha/scsi/probe", {}), "probe")
            assert probe["all_supported"] is True and probe["device_count"] == 2, probe
            assert keys(a, da[0]) == [], "the probe left its test key behind"

        with subtest("A shared group: the owner holds the disks, the other node cannot write to them"):
            ok(api(a, "POST", "/api/groups", {"name": "data", "topology": "shared", "pools": ["tank"], "candidates": [peer_b]}), "create group")
            wait_for(lambda: group(b), "b receives the group", 60)
            ra = reservation(a)
            assert ra["state"] == "reserved" and ra["mine"] and [d["state"] for d in ra["disks"]] == ["held", "held"], ra
            rb = wait_for(lambda: (lambda r: r if r and r["state"] == "reserved" else None)(reservation(b)), "b sees the reservation", 60)
            assert not rb["mine"] and all(d["state"] == "foreign" and d["holder_node"] == "a" for d in rb["disks"]), rb
            # The disks agree: one key each, and it is the one a reports.
            key_a = ok(api(a, "GET", "/api/ha/scsi/status"), "status")["key"]
            for dev in da:
                assert keys(a, dev) == ["0x" + key_a.lstrip("0")] or keys(a, dev) == ["0x" + key_a], (keys(a, dev), key_a)
                res = a.succeed(f"{SG_PERSIST} --in --read-reservation {dev}")
                assert "Write Exclusive, registrants only" in res, res
            db = disks(b)
            assert can_write(a, da[0]) and can_write(a, da[1])
            assert not can_write(b, db[0]) and not can_write(b, db[1]), "the node that does not serve the group can write to its disks"
            prot = api(a, "GET", "/api/ha/protection")
            layer = [l for l in prot["layers"] if l["name"].startswith("Disk reservations")][0]
            assert layer["state"] == "ok", layer

        with subtest("Planned move: the reservation goes with the pool"):
            ok(api(a, "POST", "/api/groups/data/move", {"target": peer_b}), "move")
            b.wait_until_succeeds("grep -q hello /mnt/tank/data/file", timeout=60)
            rb = reservation(b)
            assert rb["state"] == "reserved" and rb["mine"] and [d["state"] for d in rb["disks"]] == ["held", "held"], rb
            assert can_write(b, db[0]) and not can_write(a, da[0])
            wait_for(lambda: (lambda g: g and g["epoch"] == 2)(group(a)), "a knows the move", 60)
            ra = reservation(a)
            assert ra["state"] == "reserved" and not ra["mine"] and all(d["holder_node"] == "b" for d in ra["disks"]), ra
            # ... and back.
            ok(api(b, "POST", "/api/groups/data/move", {"target": peer_a}), "move back")
            a.wait_until_succeeds("grep -q hello /mnt/tank/data/file", timeout=60)
            assert can_write(a, da[0]) and not can_write(b, db[0])
            wait_for(lambda: (lambda g: g and g["epoch"] == 3)(group(b)), "b knows the move back", 60)

        with subtest("The owner is cut off but alive and writing: a takeover shuts it out"):
            # Something that keeps writing to the pool on a.
            a.succeed("systemd-run --unit=writer --collect sh -c 'while true; do date >> /mnt/tank/data/log; sync; sleep 1; done'")
            a.wait_until_succeeds("test -s /mnt/tank/data/log", timeout=30)
            a.block()   # the cluster network only: a still reaches the disks
            wait_for(lambda: "manually" in ((group(b) or {}).get("failover", {}).get("reason") or ""), "b: the owner is gone, takeover is manual", 120)
            assert a.succeed("zpool list -H -o health tank").strip() == "ONLINE"
            assert can_write(a, da[0]), "a lost the disks before anybody took them"

            started = time.time()
            ok(api(b, "POST", "/api/groups/data/takeover", {"confirm_owner_off": True}), "takeover")
            print(f"takeover took {time.time() - started:.0f}s")
            b.wait_until_succeeds("grep -q hello /mnt/tank/data/file", timeout=60)
            gb = group(b)
            assert gb["owner"] == peer_b and gb["epoch"] == 4 and gb["can_write"], gb
            rb = reservation(b)
            assert rb["state"] == "reserved" and rb["mine"] and [d["state"] for d in rb["disks"]] == ["held", "held"], rb
            for dev in db:
                assert len(keys(b, dev)) == 1, keys(b, dev)

            # a is still running. The disks refuse it:
            assert not can_write(a, da[0]) and not can_write(a, da[1]), "the previous owner can still write to the disks"
            # ... so its pool stops, instead of two nodes writing to one pool.
            a.wait_until_succeeds("timeout 20 zpool status tank 2>&1 | grep -q -E 'SUSPENDED|UNAVAIL|FAULTED'", timeout=180)
            # It says what happened, and takes nothing back.
            a.wait_until_succeeds("journalctl -u dplaned | grep -q 'has been shut out'", timeout=120)
            time.sleep(40)   # more than one of its checks
            for dev in db:
                assert len(keys(b, dev)) == 1, f"the previous owner registered again: {keys(b, dev)}"
            assert not can_write(a, da[0])

            # What b serves is intact, and b keeps writing to it.
            b.succeed("echo after-takeover > /mnt/tank/data/file2 && sync")
            b.succeed("zpool scrub -w tank")
            status = b.succeed("zpool status tank")
            assert "errors: No known data errors" in status and "state: ONLINE" in status, status
            lines = int(b.succeed("wc -l < /mnt/tank/data/log").strip())
            assert lines > 0, "what the previous owner wrote before the takeover is gone"

        with subtest("After a restart the previous owner follows the new one"):
            a.crash()
            a.start()
            try:
                a.unblock()
            except Exception:
                pass
            a.wait_for_unit("dplaned.service")
            a.wait_until_succeeds("curl -sf http://localhost/api/system/status >/dev/null", timeout=180)
            login(a)
            wait_for(lambda: (lambda g: g and g["epoch"] == 4)(group(a)), "a adopts epoch 4", 180)
            assert group(a)["role"] == "standby", group(a)
            a.fail("zpool list tank")
            b.succeed("zpool list tank && grep -q after-takeover /mnt/tank/data/file2")
            for dev in db:
                assert len(keys(b, dev)) == 1, keys(b, dev)
            rb = reservation(b)
            assert rb["state"] == "reserved" and rb["mine"], rb

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
