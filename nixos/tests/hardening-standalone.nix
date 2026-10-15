# nixos/tests/hardening-standalone.nix
# ─────────────────────────────────────────────────────────────────────────────
# Design 0001 phase 7: a standalone node losing power (section 5.11).
#
#   1. Power loss right after a burst of changes through the API (users, a
#      share, a dataset): after the boot every change the API confirmed is
#      there, in the database and in smb.conf, and the revision log matches
#      the live system (a capture finds nothing new).
#   2. Power loss while the pool is being written: the pool imports, a scrub
#      finds no errors, and data synced before the power loss is intact.
#   3. Power loss after a rollback from the history: the rolled-back state
#      is what the node boots with.
#
# The other failures of a standalone node concern its links: the Git remote
# (.github/scripts/git-backend-test.sh) and the fleet manager
# (.github/scripts/fleet-overlay-test.sh).
#
# Run: nix build .#checks.x86_64-linux.hardening-standalone -L
# ─────────────────────────────────────────────────────────────────────────────
{ nixpkgs, system ? "x86_64-linux", impermanence, daemonPackage, frontendPackage, ... }:

let
  pkgs = nixpkgs.legacyPackages.${system};
in

pkgs.testers.nixosTest {
  name = "dplaneos-hardening-standalone";

  nodes.n = { lib, ... }: {
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
    networking.hostName = lib.mkForce "n";
    networking.hostId   = lib.mkForce "aaaa0009";
    boot.kernelParams = [ ];
    systemd.services.systemd-random-seed.enable = false;
    virtualisation.cores = 2;
    virtualisation.memorySize = 2048;
    virtualisation.emptyDiskImages = [ { size = 1024; driveConfig.deviceExtraOpts.serial = "dpldisk"; } ];
  };

  testScript = ''
    import json, shlex

    PASSWORD = "Hardening-Pass-1"
    session = {}

    def up():
        n.wait_for_unit("dplaned.service")
        n.wait_until_succeeds("curl -sf http://localhost/api/system/status >/dev/null", timeout=180)

    def api(method, path, body=None, auth=True):
        cmd = f"curl -s --max-time 180 -X {method} {shlex.quote('http://localhost' + path)} -H 'Content-Type: application/json'"
        if auth:
            sid, csrf = session["n"]
            cmd += f" -H 'X-Session-ID: {sid}' -H 'X-User: admin' -H 'X-CSRF-Token: {csrf}'"
        if body is not None:
            cmd += " -d " + shlex.quote(json.dumps(body))
        out = n.succeed(cmd)
        try:
            return json.loads(out)
        except Exception:
            raise Exception(f"{method} {path}: not JSON: {out[:500]}")

    def ok(resp, what):
        if resp.get("success") is not True:
            raise Exception(f"{what}: {resp}")
        return resp

    def login():
        api("POST", "/api/system/setup-admin", {"username": "admin", "password": PASSWORD}, auth=False)
        sid = api("POST", "/api/auth/login", {"username": "admin", "password": PASSWORD}, auth=False)["session_id"]
        csrf = json.loads(n.succeed(f"curl -s http://localhost/api/csrf -H 'X-Session-ID: {sid}' -H 'X-User: admin'"))["csrf_token"]
        session["n"] = (sid, csrf)

    def sql(q):
        return n.succeed(f"sudo -u postgres psql -d dplaneos -tAc {shlex.quote(q)}").strip()

    def revisions():
        return int(sql("SELECT COUNT(*) FROM config_revisions"))

    def power_loss():
        n.crash()
        n.start()
        up()
        login()

    def log_matches_live(why):
        # A capture records what differs between the live system and the
        # revision log; after a consistent recovery there is nothing.
        before = revisions()
        api("POST", "/api/config/capture", {})
        after = revisions()
        new = sql(f"SELECT string_agg(resource_kind || '/' || resource_key, ', ') FROM (SELECT resource_kind, resource_key FROM config_revisions ORDER BY id DESC LIMIT {after - before}) x") if after > before else ""
        assert after == before, f"{why}: the revision log missed {new}"

    def diag():
        print(n.execute("journalctl --no-pager -u dplaned | grep -i -E 'config|capture|revision|error|panic' | grep -v -E ' (GET|POST) ' | tail -n 80")[1])

    start_all()
    up()
    try:
        with subtest("Setup: a pool and a dataset"):
            login()
            n.succeed("zpool create -f -m /mnt/tank tank /dev/disk/by-id/virtio-dpldisk && zfs create tank/media")
            api("POST", "/api/config/capture", {})

        with subtest("1. Power loss right after a burst of changes"):
            for i in range(5):
                ok(api("POST", "/api/rbac/users", {"action": "create", "username": f"user{i}", "email": f"u{i}@test.lan",
                                                   "password": "User-Pass-12345", "role": "user", "confirm_password": PASSWORD}), f"user{i}")
            ok(api("POST", "/api/shares", {"action": "create", "name": "media", "path": "/mnt/tank/media", "comment": "before the power loss"}), "share")
            ok(api("POST", "/api/rbac/users", {"action": "create", "username": "last", "email": "last@test.lan",
                                               "password": "User-Pass-12345", "role": "user", "confirm_password": PASSWORD}), "last user")
            power_loss()
            for u in [f"user{i}" for i in range(5)] + ["last"]:
                assert sql(f"SELECT COUNT(*) FROM users WHERE username = '{u}'") == "1", f"{u} lost"
            assert sql("SELECT COUNT(*) FROM smb_shares WHERE name = 'media'") == "1", "share lost"
            n.wait_until_succeeds("grep -q '^\\[media\\]' /var/lib/dplaneos/smb-shares.conf", timeout=60)
            log_matches_live("after the power loss")
            assert sql("SELECT COUNT(*) FROM config_revisions WHERE resource_kind = 'share' AND resource_key = 'media' AND payload IS NOT NULL") != "0"

        with subtest("2. Power loss while the pool is written"):
            n.succeed("dd if=/dev/urandom of=/mnt/tank/media/kept bs=1M count=32 status=none && sha256sum /mnt/tank/media/kept > /root/kept.sum && sync")
            n.succeed("(dd if=/dev/urandom of=/mnt/tank/media/inflight bs=1M count=512 status=none &) ; sleep 2")
            power_loss()
            n.wait_until_succeeds("zpool list tank", timeout=120)
            n.succeed("zpool scrub -w tank")
            st = n.succeed("zpool status -x tank")
            assert "is healthy" in st or "no known data errors" in st, st
            n.succeed("cd / && sha256sum -c /root/kept.sum")
            log_matches_live("after a power loss during writes")

        with subtest("3. Power loss after a rollback from the history"):
            share_id = sql("SELECT id FROM smb_shares WHERE name = 'media'")
            ok(api("POST", "/api/shares", {"action": "update", "id": int(share_id), "comment": "changed"}), "change the share")
            api("POST", "/api/config/capture", {})
            hist = api("GET", "/api/config/history?kind=share&key=media")
            revs = hist["revisions"]
            older = next(r for r in revs if (r.get("payload") or {}).get("comment") == "before the power loss")
            ok(api("POST", "/api/config/rollback", {"revision_id": older["id"]}), "rollback")
            assert sql("SELECT comment FROM smb_shares WHERE name = 'media'") == "before the power loss"
            power_loss()
            assert sql("SELECT comment FROM smb_shares WHERE name = 'media'") == "before the power loss", "the rollback survives the power loss"
            log_matches_live("after a power loss following a rollback")

        with subtest("No daemon panics, security refusals or security warnings"):
            bad = n.execute("journalctl --no-pager -u dplaned | grep -E 'panic:|SECURITY WARNING|security refusal' || true")[1].strip()
            assert bad == "", bad
    except Exception:
        diag()
        raise
  '';
}
