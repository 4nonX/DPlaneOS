# nixos/tests/update.nix
# ─────────────────────────────────────────────────────────────────────────────
# System updates, end to end, on a machine that boots through its boot loader
# from its own disk (like an installed system):
#
#   - an update file that is not signed with the release key is refused
#   - a release is found, downloaded and installed from the web interface's
#     API; the new version starts on trial and is kept when it is healthy
#   - a version that breaks a service is removed again by itself and the
#     machine returns to the version before
#   - a version that never starts leaves the machine on the version before
#   - returning to the previous version by hand
#
# The update files are built the way the release pipeline builds them
# (scripts/make-update-file.sh), by a second machine that also plays the
# release server.
#
# Run: nix build .#checks.x86_64-linux.update -L
# ─────────────────────────────────────────────────────────────────────────────
{ nixpkgs, system ? "x86_64-linux", daemonPackage, frontendPackage, ... }:

let
  pkgs = nixpkgs.legacyPackages.${system};

  # A release key for this test only.
  testKey = pkgs.runCommand "dplaneos-test-update-key" { nativeBuildInputs = [ pkgs.openssl ]; } ''
    mkdir $out
    openssl genpkey -algorithm ed25519 -out $out/key.pem
    openssl pkey -in $out/key.pem -pubout -out $out/pub.pem
    openssl genpkey -algorithm ed25519 -out $out/other-key.pem
  '';

  # The installed system, in three versions.
  nasConfig = version: extra: { lib, pkgs, ... }: {
    imports = [ ../configuration-standalone.nix ../module.nix ../ota-module.nix extra ];
    services.dplaneos = {
      inherit daemonPackage frontendPackage;
      fenced.package = daemonPackage;
      ota = {
        inherit version;
        publicKeyFile = "${testKey}/pub.pem";
        releaseURL = "http://releases/latest.json";
        # No internet here: the release machine also plays the public cache
        # (an empty one: everything the old system lacks is in the update file).
        upstreamCache = "http://releases/upstream/";
        healthCheckDelay = "20s";
        healthTimeout = 120;
      };
    };
    networking.hostName = lib.mkForce "nas";

    # Boot through systemd-boot from the machine's own disk and Nix store.
    virtualisation.useBootLoader = true;
    virtualisation.useEFIBoot = true;
    boot.loader.systemd-boot.enable = true;
    boot.loader.efi.canTouchEfiVariables = true;
    virtualisation.diskSize = 12 * 1024;
    virtualisation.cores = 2;
    virtualisation.memorySize = 3072;
    services.postgresql.settings = { max_connections = 50; shared_buffers = "64MB"; };
    nix.settings.experimental-features = [ "nix-command" ];
  };

in

pkgs.testers.nixosTest {
  name = "dplaneos-update";

  nodes.nas = nasConfig "1.0.0" { };
  # Never started: these are the systems the updates install.
  nodes.nasV2 = nasConfig "2.0.0" { };
  nodes.nasV3 = nasConfig "3.0.0" ({ lib, ... }: {
    # A release that breaks the web server.
    systemd.services.nginx.enable = lib.mkForce false;
  });

  nodes.releases = { nodes, pkgs, ... }: {
    networking.firewall.allowedTCPPorts = [ 80 ];
    services.nginx = {
      enable = true;
      virtualHosts.default = { default = true; root = "/srv/www"; };
    };
    systemd.tmpfiles.rules = [ "d /srv/www 0755 root root -" ];
    environment.systemPackages = [ pkgs.openssl pkgs.curl pkgs.gnutar pkgs.jq ];
    nix.settings.experimental-features = [ "nix-command" ];
    virtualisation.memorySize = 2048;
    # The systems are in this machine's store (it "built" them).
    virtualisation.additionalPaths = [
      nodes.nas.system.build.toplevel
      nodes.nasV2.system.build.toplevel
      nodes.nasV3.system.build.toplevel
    ];
  };

  testScript = { nodes, ... }:
    let
      v1 = nodes.nas.system.build.toplevel;
      v2 = nodes.nasV2.system.build.toplevel;
      v3 = nodes.nasV3.system.build.toplevel;
    in ''
    import json, shlex

    PASSWORD = "Update-Test-Pass-1"
    session = {}

    def api(method, path, body=None, auth=True):
        cmd = f"curl -s --max-time 180 -X {method} http://localhost{path} -H 'Content-Type: application/json'"
        if auth:
            cmd += f" -H 'X-Session-ID: {session['sid']}' -H 'X-User: admin' -H 'X-CSRF-Token: {session['csrf']}'"
        if body is not None:
            cmd += " -d " + shlex.quote(json.dumps(body))
        out = nas.succeed(cmd)
        try:
            return json.loads(out)
        except Exception:
            raise Exception(f"{method} {path}: not JSON: {out[:500]}")

    def ok(resp, what):
        if not isinstance(resp, dict) or resp.get("success") is False or resp.get("error"):
            raise Exception(f"{what}: {resp}")
        return resp

    def login():
        sid = api("POST", "/api/auth/login", {"username": "admin", "password": PASSWORD}, auth=False)["session_id"]
        csrf = json.loads(nas.succeed(f"curl -s http://localhost/api/csrf -H 'X-Session-ID: {sid}' -H 'X-User: admin'"))["csrf_token"]
        session.update(sid=sid, csrf=csrf)

    def wait_up():
        nas.wait_for_unit("multi-user.target")
        nas.wait_for_unit("dplaned.service")
        nas.wait_until_succeeds("curl -sf http://localhost/api/system/status >/dev/null", timeout=240)
        login()

    def status():
        return ok(api("GET", "/api/system/update/status"), "update status")

    def tool_status():
        # Without the web server (a broken release has none).
        return json.loads(nas.succeed("dplaneos-update status"))

    def versions(st):
        return [g["version"] for g in st["generations"]]

    def job(job_id, timeout=900):
        import time
        deadline = time.time() + timeout
        while time.time() < deadline:
            j = api("GET", f"/api/jobs/{job_id}")
            if j["status"] != "running":
                return j
            time.sleep(3)
        raise Exception(f"job {job_id} did not finish")

    def publish(version, system, key="/root/keys/key.pem"):
        # What the release pipeline does: build the update file, announce it.
        releases.succeed(
            f"UPDATE_SIGNING_KEY_FILE={key} UPDATE_PUBLIC_KEY_FILE={key}.pub EXCLUDE_CLOSURE_OF=${v1} "
            f"bash ${../../scripts/make-update-file.sh} {system} {version} /srv/www >&2"
        )
        name = f"dplaneos-v{version}-x86_64.update"
        size = int(releases.succeed(f"stat -c %s /srv/www/{name}").strip())
        latest = {"tag_name": f"v{version}", "body": f"Notes for {version}", "html_url": "http://releases/",
                  "published_at": "2026-01-01T00:00:00Z",
                  "assets": [{"name": name, "browser_download_url": f"http://releases/{name}", "size": size}]}
        releases.succeed("cat > /srv/www/latest.json <<'EOF'\n" + json.dumps(latest) + "\nEOF")
        return name

    def restart_into_trial():
        # The machine restarts by itself (API); QEMU ends, we start it again.
        ok(api("POST", "/api/system/reboot", {}), "restart")
        nas.wait_for_shutdown()
        nas.start()

    def diag():
        print(nas.execute("cat /var/lib/dplaneos/updates/update.log; cat /var/lib/dplaneos/updates/pending; bootctl list --no-pager; "
                          "ls -la /nix/var/nix/profiles/; journalctl -b --no-pager -u dplaneos-update-confirm -u dplaned | tail -n 80")[1])

    releases.start()
    nas.start()
    releases.wait_for_unit("nginx.service")
    # For make-update-file.sh: the public half of each key next to it.
    releases.succeed("mkdir -p /root/keys && for k in key other-key; do cp ${testKey}/$k.pem /root/keys/; "
                     "openssl pkey -in /root/keys/$k.pem -pubout -out /root/keys/$k.pem.pub; done")
    # The "public cache": a valid binary cache with nothing in it.
    releases.succeed("mkdir -p /srv/www/upstream && printf 'StoreDir: /nix/store\\n' > /srv/www/upstream/nix-cache-info")

    try:
        with subtest("An installed system: version 1.0.0, booted through the boot loader"):
            nas.wait_for_unit("multi-user.target")
            nas.wait_for_unit("dplaned.service")
            nas.wait_until_succeeds("curl -sf http://localhost/api/system/status >/dev/null", timeout=240)
            api("POST", "/api/system/setup-admin", {"username": "admin", "password": PASSWORD}, auth=False)
            login()
            nas.succeed("bootctl status | grep -q systemd-boot")
            # The installer leaves the system as generation 1 of its profile.
            nas.succeed("test -e /nix/var/nix/profiles/system || nix-env -p /nix/var/nix/profiles/system --set $(readlink -f /run/current-system)")
            st = status()
            assert st["supported"] and st["can_verify"], st
            assert st["running_version"] == "1.0.0" and versions(st) == ["1.0.0"] and st["pending"] is None, st

        with subtest("No release yet, then a release for another kind of machine: nothing to install"):
            releases.succeed("echo '{\"tag_name\": \"v1.0.0\", \"assets\": []}' > /srv/www/latest.json")
            res = ok(api("POST", "/api/system/update/check", {}), "check")
            assert res["available"] is False and "up to date" in res["message"], res
            releases.succeed("echo '{\"tag_name\": \"v2.0.0\", \"assets\": [{\"name\": \"dplaneos-v2.0.0-riscv64.update\", \"browser_download_url\": \"http://releases/x\", \"size\": 1}]}' > /srv/www/latest.json")
            res = ok(api("POST", "/api/system/update/check", {}), "check")
            assert res["available"] is False and "x86_64" in res["message"], res
            assert api("POST", "/api/system/update/install", {}).get("success") is not True

        with subtest("An update file that is not signed with the release key is refused"):
            name = publish("2.0.0", "${v2}", key="/root/keys/other-key.pem")
            res = ok(api("POST", "/api/system/update/check", {}), "check")
            assert res["available"] is True and res["latest_version"] == "2.0.0", res
            j = job(ok(api("POST", "/api/system/update/install", {}), "install")["job_id"])
            assert j["status"] == "failed" and "signature" in j["error"], j
            st = status()
            assert st["pending"] is None and versions(st) == ["1.0.0"], st
            nas.fail("test -e ${v2}")
            # A correctly signed file that was changed afterwards.
            name = publish("2.0.0", "${v2}", key="/root/keys/key.pem")
            nas.succeed(f"curl -sf -o /tmp/good.update http://releases/{name}")
            nas.succeed("mkdir /tmp/t && tar -xf /tmp/good.update -C /tmp/t && echo x >> /tmp/t/update.tar && "
                        "tar -cf /tmp/tampered.update -C /tmp/t update.tar update.tar.sha256 update.tar.sha256.sig")
            assert "damaged" in nas.fail("dplaneos-update verify /tmp/tampered.update 2>&1")
            assert "not a DPlaneOS update file" in nas.fail("dplaneos-update verify /etc/hostname 2>&1")
            assert json.loads(nas.succeed("dplaneos-update verify /tmp/good.update 2>/dev/null"))["version"] == "2.0.0"

        with subtest("Update to 2.0.0: installed next to the running version, started on trial, kept"):
            j = job(ok(api("POST", "/api/system/update/install", {}), "install")["job_id"])
            assert j["status"] == "done" and j["result"]["version"] == "2.0.0", j
            st = status()
            assert st["running_version"] == "1.0.0" and st["pending"]["version"] == "2.0.0" and st["pending"]["running"] is False, st
            assert versions(st) == ["1.0.0", "2.0.0"], st
            # A second installation while one is on trial is refused.
            assert api("POST", "/api/system/update/install", {}).get("success") is not True
            # The default boot entry is still the running version.
            entries = json.loads(nas.succeed("bootctl list --json=short"))
            default = [e["id"] for e in entries if e.get("isDefault")]
            assert default == ["nixos-generation-1.conf"], entries

            restart_into_trial()
            wait_up()
            st = status()
            assert st["running_version"] == "2.0.0" and st["pending"]["running"] is True, st
            # The check runs by itself (timer) and keeps the new version.
            nas.wait_until_fails("test -e /var/lib/dplaneos/updates/pending", timeout=180)
            st = status()
            assert st["pending"] is None and st["last_result"]["outcome"] == "installed" and st["last_result"]["version"] == "2.0.0", st
            assert st["installed_version"] == "2.0.0" and versions(st) == ["1.0.0", "2.0.0"], st

            # It is the default now: another restart stays on 2.0.0.
            nas.shutdown()
            nas.start()
            wait_up()
            assert status()["running_version"] == "2.0.0"
            res = ok(api("POST", "/api/system/update/check", {}), "check")
            assert res["available"] is False, res

        with subtest("A release that breaks a service is removed again by itself"):
            publish("3.0.0", "${v3}")
            assert ok(api("POST", "/api/system/update/check", {}), "check")["latest_version"] == "3.0.0"
            j = job(ok(api("POST", "/api/system/update/install", {}), "install")["job_id"])
            assert j["status"] == "done", j
            restart_into_trial()
            # 3.0.0 runs, without web server; the check notices, restores
            # 2.0.0 and restarts the machine.
            nas.wait_for_unit("multi-user.target")
            assert nas.succeed("cat /run/current-system/dplaneos-version").strip() == "3.0.0"
            nas.wait_for_shutdown()
            nas.start()
            wait_up()
            st = status()
            assert st["running_version"] == "2.0.0" and st["installed_version"] == "2.0.0" and st["pending"] is None, st
            assert st["last_result"]["outcome"] == "reverted" and "nginx" in st["last_result"]["message"], st
            assert versions(st) == ["1.0.0", "2.0.0"], st
            nas.fail("ls /boot/loader/entries | grep -q generation-3")
            # And stays there.
            nas.shutdown()
            nas.start()
            wait_up()
            assert status()["running_version"] == "2.0.0"

        with subtest("A version that never starts leaves the machine on the version before"):
            j = job(ok(api("POST", "/api/system/update/install", {}), "install")["job_id"])
            assert j["status"] == "done", j
            # As if the trial start had hung and the machine was power cycled:
            # the boot loader has used up its one start of the new version.
            nas.succeed("bootctl set-oneshot '''")
            nas.crash()
            nas.start()
            wait_up()
            assert status()["running_version"] == "2.0.0"
            nas.wait_until_fails("test -e /var/lib/dplaneos/updates/pending", timeout=180)
            st = status()
            assert st["last_result"]["outcome"] == "reverted" and "did not start" in st["last_result"]["message"], st
            assert st["installed_version"] == "2.0.0" and versions(st) == ["1.0.0", "2.0.0"], st

        with subtest("Returning to the previous version by hand"):
            ok(api("POST", "/api/system/update/revert", {}), "revert")
            nas.wait_for_shutdown()
            nas.start()
            wait_up()
            st = status()
            assert st["running_version"] == "1.0.0" and st["installed_version"] == "1.0.0", st
    except Exception:
        diag()
        raise
  '';
}
