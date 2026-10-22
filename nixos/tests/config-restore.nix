# nixos/tests/config-restore.nix
# ─────────────────────────────────────────────────────────────────────────────
# Configuration backup and restore through the API, with the real daemon,
# PostgreSQL and file locations:
#
#   1. a user (database) and a snapshot schedule (file) are set up
#   2. the configuration is downloaded (GET /api/system/config/backup)
#   3. the user is removed, another one added, the schedule changed
#   4. the backup is uploaded (POST /api/system/config/restore): the daemon
#      restarts and applies it before its migrations
#   5. the old session no longer works; after a new login the user and the
#      schedule are back, the later user is gone, the result is "ok", and the
#      configuration from before the restore can be downloaded
#   6. a damaged upload is refused and nothing is staged
#   7. undo: the before-restore backup, placed among the pool's stored
#      backups, is listed and restored; the later state is back
#   8. console recovery (dplaneos-recovery's daemon calls): a password reset
#      replaces the old password, a weak one is refused, 2FA can be turned off
#
# Run: nix build .#checks.x86_64-linux.config-restore -L
# ─────────────────────────────────────────────────────────────────────────────
{ nixpkgs, system ? "x86_64-linux", daemonPackage, frontendPackage, ... }:

let
  pkgs = nixpkgs.legacyPackages.${system};
in

pkgs.testers.nixosTest {
  name = "dplaneos-config-restore";

  nodes.machine = { ... }: {
    imports = [ ../module.nix ];
    services.dplaneos = {
      enable = true;
      inherit daemonPackage frontendPackage;
      dbPath = "/var/lib/dplaneos/pgsql";
    };
    networking.hostId = "dd0e0002";
    virtualisation.memorySize = 2048;
    virtualisation.cores = 2;
  };

  testScript = ''
    import json, shlex

    PASSWORD = "Restore-Test-Pass-1"
    session = {}

    def api(method, path, body=None, auth=True):
        cmd = f"curl -s --max-time 120 -X {method} http://localhost{path} -H 'Content-Type: application/json'"
        if auth:
            cmd += f" -H 'X-Session-ID: {session['sid']}' -H 'X-User: admin' -H 'X-CSRF-Token: {session['csrf']}'"
        if body is not None:
            cmd += " -d " + shlex.quote(json.dumps(body))
        out = machine.succeed(cmd)
        try:
            return json.loads(out)
        except Exception:
            raise Exception(f"{method} {path}: not JSON: {out[:500]}")

    def ok(resp, what):
        if resp.get("success") is not True:
            raise Exception(f"{what}: {resp}")
        return resp

    def login():
        sid = api("POST", "/api/auth/login", {"username": "admin", "password": PASSWORD}, auth=False)["session_id"]
        csrf = json.loads(machine.succeed(f"curl -s http://localhost/api/csrf -H 'X-Session-ID: {sid}' -H 'X-User: admin'"))["csrf_token"]
        session.update(sid=sid, csrf=csrf)

    def auth_headers():
        return f"-H 'X-Session-ID: {session['sid']}' -H 'X-User: admin' -H 'X-CSRF-Token: {session['csrf']}'"

    def users():
        return sorted(u["username"] for u in ok(api("GET", "/api/rbac/users"), "users")["users"])

    machine.start()
    machine.wait_for_unit("dplaned.service")
    machine.wait_until_succeeds("curl -sf http://localhost/api/system/status >/dev/null", timeout=180)
    api("POST", "/api/system/setup-admin", {"username": "admin", "password": PASSWORD}, auth=False)
    login()
    machine.succeed("truncate -s 256M /var/tmp/tank.img && zpool create -f tank /var/tmp/tank.img")

    with subtest("configuration at backup time"):
        ok(api("POST", "/api/rbac/users", {"action": "create", "username": "carol", "email": "carol@t.lan",
                                           "password": "Carol-Pass-12345", "role": "user", "confirm_password": PASSWORD}), "create carol")
        ok(api("POST", "/api/snapshots/schedules", [{"dataset": "tank", "frequency": "daily", "retention": 7, "enabled": True}]), "schedule")
        assert "carol" in users()

    with subtest("download the backup"):
        machine.succeed(f"curl -sf -o /var/tmp/config.tar.gz {auth_headers()} 'http://localhost/api/system/config/backup?secrets=1'")
        listing = machine.succeed("tar -tzf /var/tmp/config.tar.gz")
        for want in ["manifest.json", "db.dump", "snapshot-schedules.json", "secrets.key", "audit.key"]:
            assert want in listing, f"{want} missing from the backup:\n{listing}"

    with subtest("changes after the backup"):
        carol_id = next(u["id"] for u in ok(api("GET", "/api/rbac/users"), "users")["users"] if u["username"] == "carol")
        ok(api("POST", "/api/rbac/users", {"action": "delete", "id": carol_id, "confirm_password": PASSWORD}), "delete carol")
        ok(api("POST", "/api/rbac/users", {"action": "create", "username": "dave", "email": "dave@t.lan",
                                           "password": "Dave-Pass-123456", "role": "user", "confirm_password": PASSWORD}), "create dave")
        ok(api("POST", "/api/snapshots/schedules", []), "clear schedules")
        assert "carol" not in users() and "dave" in users()

    with subtest("a damaged upload is refused and nothing is staged"):
        machine.succeed("head -c 2000 /var/tmp/config.tar.gz > /var/tmp/broken.tar.gz")
        out = machine.succeed(f"curl -s {auth_headers()} -F backup=@/var/tmp/broken.tar.gz http://localhost/api/system/config/restore")
        assert json.loads(out).get("success") is not True, out
        assert ok(api("GET", "/api/system/config/restore"), "status")["pending"] is False

    with subtest("restore: the daemon restarts and applies it"):
        old_sid = session["sid"]
        out = machine.succeed(f"curl -s {auth_headers()} -F backup=@/var/tmp/config.tar.gz -F secrets=1 http://localhost/api/system/config/restore")
        assert json.loads(out).get("success") is True, out
        machine.wait_until_succeeds("journalctl -u dplaned.service | grep -q 'CONFIG RESTORE: applied'", timeout=180)
        machine.wait_until_succeeds("curl -sf http://localhost/api/system/status >/dev/null", timeout=180)
        # sessions of the backup and of before the restore are gone
        code = machine.succeed(f"curl -s -o /dev/null -w '%{{http_code}}' -H 'X-Session-ID: {old_sid}' -H 'X-User: admin' http://localhost/api/rbac/users").strip()
        assert code == "401", f"old session still valid: {code}"
        login()
        u = users()
        assert "carol" in u and "dave" not in u, u
        sched = ok(api("GET", "/api/snapshots/schedules"), "schedules")
        assert any(s.get("dataset") == "tank" for s in sched["schedules"]), sched
        st = ok(api("GET", "/api/system/config/restore"), "status")
        assert st["last_result"]["status"] == "ok" and st["previous_available"] is True and st["pending"] is False, st
        machine.succeed(f"curl -sf -o /var/tmp/before.tar.gz {auth_headers()} http://localhost/api/system/config/backup/previous")
        machine.succeed("tar -tzf /var/tmp/before.tar.gz | grep -q db.dump")

    with subtest("undo: restore the before-restore backup from the pool's stored backups"):
        machine.succeed("mkdir -p /tank/.dplaneos-config && cp /var/tmp/before.tar.gz /tank/.dplaneos-config/dplaneos-config-machine-20260101-000000.tar.gz")
        stored = ok(api("GET", "/api/system/config/backups"), "stored backups")["backups"]
        assert [b["name"] for b in stored] == ["dplaneos-config-machine-20260101-000000.tar.gz"] and stored[0]["pool"] == "tank", stored
        ok(api("POST", "/api/system/config/backups/tank/dplaneos-config-machine-20260101-000000.tar.gz/restore", {"secrets": True}), "restore stored")
        machine.wait_until_succeeds("journalctl -u dplaned.service | grep -c 'CONFIG RESTORE: applied' | grep -qx 2", timeout=180)
        machine.wait_until_succeeds("curl -sf http://localhost/api/system/status >/dev/null", timeout=180)
        machine.fail("journalctl -u dplaned.service | grep -q 'CONFIG RESTORE: restart failed'")
        login()
        u = users()
        assert "dave" in u and "carol" not in u, u
        assert ok(api("GET", "/api/snapshots/schedules"), "schedules")["schedules"] == [], "schedule of before the restore not back"
    with subtest("console recovery: reset a password, turn off 2FA (dplaneos-recovery)"):
        # The menu runs these with the package's environment; run them the same way.
        # writeShellApplication sets runtimeEnv as `NAME=value` + `export NAME`
        env = machine.succeed(r"grep -E '^(DPLANED|DPLANEOS_DSN|DPLANEOS_SECRETS_KEY)=' $(readlink -f $(command -v dplaneos-recovery))").strip().replace("\n", " ")
        assert env.count("=") >= 3 and "DPLANED=/" in env, env
        rec = f"env {env} sh -c '\"$DPLANED\" -db-dsn \"$DPLANEOS_DSN\" -secrets-key \"$DPLANEOS_SECRETS_KEY\" \"$@\"' recover"
        listing = machine.succeed(f"{rec} -recover-list-users")
        assert "admin\tadmin\tactive" in listing, listing
        machine.fail(f"printf 'weak\n' | {rec} -recover-reset-password admin")
        machine.succeed(f"printf 'Recovered-Pass-1!\n' | {rec} -recover-reset-password admin")
        r = api("POST", "/api/auth/login", {"username": "admin", "password": PASSWORD}, auth=False)
        assert r.get("success") is not True, f"the old password still works: {r}"
        r = api("POST", "/api/auth/login", {"username": "admin", "password": "Recovered-Pass-1!"}, auth=False)
        assert r.get("success") is True and r.get("session_id"), r
        machine.succeed(f"{rec} -recover-disable-2fa admin")
        machine.fail(f"{rec} -recover-reset-password nobody </dev/null")

  '';
}
