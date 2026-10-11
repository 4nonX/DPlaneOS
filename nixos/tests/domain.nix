# nixos/tests/domain.nix
# ─────────────────────────────────────────────────────────────────────────────
# Membership in an Active Directory domain, end to end.
#
#   dc      a domain controller (Samba AD DC, with the domain's DNS)
#   nas     the appliance, as installed
#   client  another machine on the network
#
# The appliance joins the domain through the API the web interface uses.
# Then a user of the domain opens a share from the client with the domain
# password, the file it writes belongs to that user on the appliance, local
# accounts keep working, and a share can be limited to a group of the
# domain. What cannot work is refused with a reason (wrong password, a
# domain that DNS does not know, a second domain). Leaving the domain puts
# the system back on its local accounts.
#
# Run: nix build .#checks.x86_64-linux.domain -L
# ─────────────────────────────────────────────────────────────────────────────
{ nixpkgs, system ? "x86_64-linux", impermanence, daemonPackage, frontendPackage, ... }:

let
  pkgs = nixpkgs.legacyPackages.${system};
  dataDisk = serial: { size = 512; driveConfig.deviceExtraOpts.serial = serial; };
  realm = "AD.EXAMPLE.TEST";
  domain = "AD";
  dcAdminPass = "Dc-Admin-Pass-1";
in

pkgs.testers.nixosTest {
  name = "dplaneos-domain";

  nodes.dc = { pkgs, lib, ... }: {
    networking.firewall.enable = false;
    networking.domain = "ad.example.test";
    # The domain controller serves the domain's DNS itself.
    services.resolved.enable = false;
    environment.systemPackages = [ pkgs.sambaFull pkgs.dnsutils ];
    systemd.tmpfiles.rules = [
      "d /etc/samba 0755 root root -"
      "d /var/lib/samba 0755 root root -"
      "d /var/lib/samba/private 0700 root root -"
      "d /var/log/samba 0755 root root -"
      "d /var/lock/samba 0755 root root -"
      "d /var/cache/samba 0755 root root -"
      "d /run/samba 0755 root root -"
    ];
    virtualisation.memorySize = 2048;
    virtualisation.cores = 2;
  };

  nodes.nas = { lib, pkgs, ... }: {
    imports = [
      ../configuration-standalone.nix
      ../module.nix
      impermanence.nixosModules.impermanence
      ../impermanence.nix
      ../ota-module.nix
    ];
    services.dplaneos = {
      inherit daemonPackage frontendPackage;
    };
    networking.hostName = lib.mkForce "";
    virtualisation.cores = 2;
    virtualisation.memorySize = 3072;
    virtualisation.emptyDiskImages = [ 2048 (dataDisk "dplanedata0") (dataDisk "dplanedata1") ];
    virtualisation.fileSystems."/persist" = {
      device = "/dev/vdb";
      fsType = "ext4";
      autoFormat = true;
      neededForBoot = true;
    };
    boot.loader.systemd-boot.enable = lib.mkForce false;
    boot.loader.efi.canTouchEfiVariables = lib.mkForce false;
    services.postgresql.settings = { max_connections = 50; shared_buffers = "64MB"; };
  };

  nodes.client = { pkgs, ... }: {
    environment.systemPackages = [ pkgs.samba pkgs.curl ];
    virtualisation.memorySize = 768;
  };

  testScript = ''
    import json, shlex, time

    REALM, DOMAIN = "${realm}", "${domain}"
    DC_ADMIN_PASS = "${dcAdminPass}"
    PASSWORD = "Domain-Test-Pass-1"
    D0 = "/dev/disk/by-id/virtio-dplanedata0"
    D1 = "/dev/disk/by-id/virtio-dplanedata1"
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

    def refused(resp):
        return isinstance(resp, dict) and (resp.get("success") is False or bool(resp.get("error")))

    def login():
        sid = api("POST", "/api/auth/login", {"username": "admin", "password": PASSWORD}, auth=False)["session_id"]
        csrf = json.loads(nas.succeed(f"curl -s http://localhost/api/csrf -H 'X-Session-ID: {sid}' -H 'X-User: admin'"))["csrf_token"]
        session.update(sid=sid, csrf=csrf)

    def job(job_id, timeout=600):
        deadline = time.time() + timeout
        while time.time() < deadline:
            j = api("GET", f"/api/jobs/{job_id}")
            if j["status"] != "running":
                return j
            time.sleep(3)
        raise Exception(f"job {job_id} did not finish")

    def membership(action, name, body):
        return job(ok(api("POST", f"/api/ldap/domains/{name}/{action}", body), action)["job_id"])

    def address(m):
        return m.succeed("ip -4 -o addr show dev eth1 | awk '{print $4}' | cut -d/ -f1 | head -n1").strip()

    def diag():
        print(nas.execute("journalctl -b --no-pager -u dplaned | grep -i -E 'AD:|samba|smb|winbind|error' | tail -n 60")[1])
        print(nas.execute("journalctl -b --no-pager -u samba-winbindd -u samba-smbd | tail -n 60")[1])
        print(nas.execute("cat /etc/samba/smb.conf; resolvectl status 2>&1 | head -n 30; wbinfo -t 2>&1; net ads info 2>&1")[1])
        print(dc.execute("journalctl --no-pager -u samba-dc | tail -n 40")[1])

    start_all()

    try:
        with subtest("A domain with two users and a group"):
            dc.wait_for_unit("multi-user.target")
            DC = address(dc)
            dc.succeed("rm -f /etc/samba/smb.conf")
            dc.succeed(
                f"samba-tool domain provision --realm={REALM} --domain={DOMAIN} --server-role=dc "
                f"--dns-backend=SAMBA_INTERNAL --adminpass={DC_ADMIN_PASS} --host-ip={DC} "
                "--option='interfaces = lo eth1' --option='bind interfaces only = yes' "
                "--option='dns forwarder = 127.0.0.2' >&2")
            dc.succeed("systemd-run --unit=samba-dc --property=Restart=no samba --foreground --no-process-group")
            dc.wait_for_open_port(389)
            dc.wait_for_open_port(88)
            dc.wait_until_succeeds(f"dig +short @{DC} _ldap._tcp.ad.example.test SRV | grep -q dc", timeout=120)
            dc.succeed("samba-tool user create alice 'Alice-Dom-Pass-1' >&2")
            dc.succeed("samba-tool user create bob 'Bob-Dom-Pass-1' >&2")
            dc.succeed("samba-tool group add nas-team >&2 && samba-tool group addmembers nas-team alice >&2")

        with subtest("The appliance: admin, pool, a share, a local user"):
            nas.wait_for_unit("dplaned.service")
            nas.wait_until_succeeds("curl -sf http://localhost/api/system/status >/dev/null", timeout=300)
            api("POST", "/api/system/setup-admin", {"username": "admin", "password": PASSWORD}, auth=False)
            login()
            NAS = address(nas)
            ok(api("POST", "/api/system/pool/create", {"name": "tank", "topology": {"data": [{"type": "mirror", "disks": [D0, D1]}]}}), "create pool")
            ok(api("POST", "/api/zfs/datasets", {"name": "tank/data"}), "create dataset")
            mp = nas.succeed("zfs get -H -o value mountpoint tank/data").strip()
            nas.succeed(f"echo kept > {mp}/file")
            ok(api("POST", "/api/shares", {"action": "create", "name": "data", "path": mp}), "create share")
            ok(api("POST", "/api/rbac/users", {"action": "create", "username": "local1", "password": "Local-User-Pass-1", "role": "user", "confirm_password": PASSWORD}), "create user")
            nas.wait_until_succeeds("pdbedit -L | grep -q '^local1:'", timeout=60)
            client.wait_for_unit("multi-user.target")
            client.succeed(f"smbclient //{NAS}/data -U local1%Local-User-Pass-1 -c 'get file /tmp/f0' && grep -qx kept /tmp/f0")
            st = ok(api("GET", "/api/directory/status"), "status")["data"]
            assert st["domain_joined"] is False and st["supported"] is True, st

        with subtest("What cannot work is refused, and leaves the system as it was"):
            ok(api("POST", "/api/ldap/domains", {"name": DOMAIN, "realm": REALM, "server": "", "idmap_backend": "rid", "idmap_low": 100000, "idmap_high": 199999}), "register domain")
            # The appliance does not use the domain's DNS yet.
            j = membership("join", DOMAIN, {"username": "Administrator", "password": DC_ADMIN_PASS})
            assert j["status"] == "failed" and "DNS" in j["error"], j
            ok(api("PUT", "/api/system/network", {"action": "set_dns", "nameservers": [DC], "search": ["ad.example.test"]}), "set DNS")
            nas.wait_until_succeeds("resolvectl query --type=SRV _ldap._tcp.ad.example.test | grep -q dc", timeout=60)
            # Wrong password.
            j = membership("join", DOMAIN, {"username": "Administrator", "password": "not-the-password"})
            assert j["status"] == "failed" and "did not accept" in j["error"], j
            # Nothing changed: still standalone, the local user still gets in.
            assert "security = ads" not in nas.succeed("cat /etc/samba/smb.conf").lower()
            assert ok(api("GET", "/api/directory/status"), "status")["data"]["domain_joined"] is False
            client.succeed(f"smbclient //{NAS}/data -U local1%Local-User-Pass-1 -c ls")

        with subtest("Join"):
            j = membership("join", DOMAIN, {"username": "Administrator", "password": DC_ADMIN_PASS})
            assert j["status"] == "done", j
            print("\n".join(j.get("logs") or []))
            st = ok(api("GET", "/api/directory/status"), "status")["data"]
            assert st["domain_joined"] is True and st["realm"] == REALM and st["trust_ok"] is True, st
            nas.succeed("wbinfo -t")
            nas.succeed("net ads testjoin")
            conf = nas.succeed("testparm -s 2>/dev/null")
            assert "security = ADS" in conf and f"realm = {REALM}" in conf and f"workgroup = {DOMAIN}" in conf, conf
            # The domain knows the appliance.
            dc.succeed("samba-tool computer list | grep -i '^DPLANEOS\\$'")
            ds = ok(api("GET", "/api/ldap/domains"), "domains")["data"]
            assert len(ds) == 1 and ds[0]["domain_joined"] is True and ds[0]["kinit_ok"] is True, ds
            chk = api("GET", f"/api/ldap/domains/{DOMAIN}/status")
            assert chk["joined"] is True and chk.get("trust_ok") is True, chk
            # The password of the account that joined is nowhere on the system.
            nas.fail(f"grep -rF '{DC_ADMIN_PASS}' /var/lib/dplaneos /etc/samba /var/lib/samba 2>/dev/null")
            nas.fail(f"journalctl -b --no-pager | grep -F '{DC_ADMIN_PASS}'")

        with subtest("Users of the domain are users of the system"):
            nas.wait_until_succeeds("getent passwd alice | grep -q .", timeout=120)
            uid = int(nas.succeed("id -u alice").strip())
            assert 100000 <= uid <= 199999, uid
            assert "nas-team" in nas.succeed("id -Gn alice")
            assert nas.succeed("getent passwd alice | cut -d: -f7").strip().endswith("nologin")
            # A local account of the same name would win; local accounts are untouched.
            assert int(nas.succeed("id -u local1").strip()) < 3000

        with subtest("A domain user opens the share from another machine"):
            client.wait_until_succeeds(
                f"smbclient //{NAS}/data -U '{DOMAIN}\\alice%Alice-Dom-Pass-1' -c 'put /etc/os-release from-alice.txt; get file /tmp/f1' && grep -qx kept /tmp/f1",
                timeout=120)
            assert nas.succeed(f"stat -c %U:%G {mp}/from-alice.txt").strip() == "alice:users"
            client.fail(f"smbclient //{NAS}/data -U '{DOMAIN}\\alice%wrong' -c ls")
            client.fail(f"smbclient //{NAS}/data -U '{DOMAIN}\\nobody-there%x' -c ls")
            # Local accounts keep working next to the domain.
            client.succeed(f"smbclient //{NAS}/data -U 'DPLANEOS\\local1%Local-User-Pass-1' -c ls")

        with subtest("A share for one group of the domain"):
            nas.succeed(f"mkdir -p {mp}/team && chown root:users {mp}/team && chmod 2775 {mp}/team")
            ok(api("POST", "/api/shares", {"action": "create", "name": "team", "path": f"{mp}/team", "valid_users": "@nas-team"}), "create group share")
            client.wait_until_succeeds(f"smbclient //{NAS}/team -U '{DOMAIN}\\alice%Alice-Dom-Pass-1' -c 'put /etc/os-release a.txt'", timeout=60)
            client.fail(f"smbclient //{NAS}/team -U '{DOMAIN}\\bob%Bob-Dom-Pass-1' -c ls")
            client.succeed(f"smbclient //{NAS}/data -U '{DOMAIN}\\bob%Bob-Dom-Pass-1' -c ls")

        with subtest("A system is a member of one domain"):
            ok(api("POST", "/api/ldap/domains", {"name": "PARTNER", "realm": "PARTNER.EXAMPLE.TEST", "server": "", "idmap_backend": "rid", "idmap_low": 200000, "idmap_high": 299999}), "register a trusted domain")
            j = membership("join", "PARTNER", {"username": "Administrator", "password": "x"})
            assert j["status"] == "failed" and "one domain" in j["error"], j
            # It has an ID range of its own all the same.
            assert "idmap config PARTNER : range = 200000-299999" in nas.succeed("cat /etc/samba/smb.conf")
            assert refused(api("DELETE", f"/api/ldap/domains/{DOMAIN}")), "a joined domain was removed"
            ok(api("DELETE", "/api/ldap/domains/PARTNER"), "remove the trusted domain")

        with subtest("The membership survives a restart"):
            nas.shutdown()
            nas.start()
            nas.wait_for_unit("dplaned.service")
            nas.wait_until_succeeds("curl -sf http://localhost/api/system/status >/dev/null", timeout=300)
            login()
            nas.wait_until_succeeds("wbinfo -t", timeout=180)
            client.wait_until_succeeds(f"smbclient //{NAS}/data -U '{DOMAIN}\\alice%Alice-Dom-Pass-1' -c 'get from-alice.txt /tmp/f2'", timeout=180)
            assert nas.succeed(f"stat -c %U {mp}/from-alice.txt").strip() == "alice"

        with subtest("Leave: back to local accounts"):
            j = membership("leave", DOMAIN, {"username": "Administrator", "password": "not-the-password"})
            assert j["status"] == "failed", j
            assert ok(api("GET", "/api/directory/status"), "status")["data"]["domain_joined"] is True
            j = membership("leave", DOMAIN, {"username": "Administrator", "password": DC_ADMIN_PASS})
            assert j["status"] == "done", j
            st = ok(api("GET", "/api/directory/status"), "status")["data"]
            assert st["domain_joined"] is False, st
            conf = nas.succeed("testparm -s 2>/dev/null")
            assert "security = ADS" not in conf and "workgroup = WORKGROUP" in conf, conf
            dc.fail("samba-tool computer list | grep -i '^DPLANEOS\\$'")
            client.wait_until_succeeds(f"smbclient //{NAS}/data -U local1%Local-User-Pass-1 -c ls", timeout=60)
            client.fail(f"smbclient //{NAS}/data -U '{DOMAIN}\\alice%Alice-Dom-Pass-1' -c ls")
            failed = nas.succeed("systemctl --failed --no-legend --plain | awk '{print $1}'").split()
            assert failed == [], f"failed units: {failed}"

        with subtest("No daemon panics, security refusals or security warnings"):
            bad = nas.execute("journalctl -b --no-pager -u dplaned | grep -E 'panic:|SECURITY WARNING|security refusal' || true")[1].strip()
            assert bad == "", bad
    except Exception:
        diag()
        raise
  '';
}
