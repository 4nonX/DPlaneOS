# nixos/tests/ad-join.nix
# ─────────────────────────────────────────────────────────────────────────────
# Active Directory membership against a real domain controller:
#
#   dc   Samba AD DC (CORP.TEST, NetBIOS CORP) with user alice
#   nas  DPlaneOS with the AD settings as they are once the AD configuration
#        is saved and applied (security = ads, realm, workgroup, KDC)
#
#   1. POST /api/directory/join with the domain administrator joins the
#      domain (clock checked against the DC, Samba configuration checked)
#   2. the trust works (wbinfo -t) and AD users resolve on the NAS
#      (getent passwd alice: winbind NSS), so shares can use them
#   3. a join with a wrong password is refused with a clear error
#
# Run: nix build .#checks.x86_64-linux.ad-join -L
# ─────────────────────────────────────────────────────────────────────────────
{ nixpkgs, system ? "x86_64-linux", daemonPackage, frontendPackage, ... }:

let
  pkgs = nixpkgs.legacyPackages.${system};
  adminPass = "Admin-Pass-123!";
in

pkgs.testers.nixosTest {
  name = "dplaneos-ad-join";

  nodes.dc = { pkgs, ... }: {
    networking.enableIPv6 = false; # the DC listens on IPv4 (bind interfaces only)
    networking.domain = "corp.test";
    networking.firewall.enable = false;
    environment.systemPackages = [ pkgs.sambaFull ];
    systemd.services.samba-dc = {
      wantedBy = [ "multi-user.target" ];
      after = [ "network-online.target" ];
      wants = [ "network-online.target" ];
      path = [ pkgs.sambaFull pkgs.iproute2 pkgs.gawk pkgs.coreutils ];
      preStart = ''
        # samba exits at startup without these (ntp_signd is fatal)
        mkdir -p /var/log/samba /var/lib/samba/private /run/samba
        install -d -m 0750 /var/lib/samba/ntp_signd
        if [ ! -f /var/lib/samba-dc/private/sam.ldb ]; then
          ip=$(ip -4 -o addr show eth1 | awk '{print $4}' | cut -d/ -f1)
          samba-tool domain provision --targetdir=/var/lib/samba-dc \
            --realm=CORP.TEST --domain=CORP --server-role=dc --dns-backend=SAMBA_INTERNAL \
            --adminpass='${adminPass}' --host-ip="$ip" \
            --option="interfaces=lo eth1" --option="bind interfaces only=yes"
        fi
      '';
      serviceConfig.ExecStart = "${pkgs.sambaFull}/bin/samba -F -s /var/lib/samba-dc/etc/smb.conf";
      serviceConfig.TimeoutStartSec = 900; # provisioning takes minutes in a VM
    };
    virtualisation.memorySize = 1536;
  };

  nodes.nas = { nodes, ... }: {
    imports = [ ../module.nix ];
    services.dplaneos = {
      enable = true;
      inherit daemonPackage frontendPackage;
      dbPath = "/var/lib/dplaneos/pgsql";
      samba = {
        enable = true;
        securityMode = "ads";
        realm = "CORP.TEST";
        workgroup = "CORP";
        domainController = "dc.corp.test";
      };
    };
    networking.hostId = "dd0e0004";
    networking.enableIPv6 = false;
    networking.nameservers = [ nodes.dc.networking.primaryIPAddress ];
    networking.extraHosts = "${nodes.dc.networking.primaryIPAddress} dc.corp.test dc";
    virtualisation.memorySize = 2048;
  };

  testScript = ''
    import json, shlex

    PASSWORD = "Ad-Test-Pass-1!"
    session = {}

    def api(method, path, body=None, auth=True):
        cmd = f"curl -s --max-time 180 -X {method} http://localhost{path} -H 'Content-Type: application/json'"
        if auth:
            cmd += f" -H 'X-Session-ID: {session['sid']}' -H 'X-User: admin' -H 'X-CSRF-Token: {session['csrf']}'"
        if body is not None:
            cmd += " -d " + shlex.quote(json.dumps(body))
        return json.loads(nas.succeed(cmd))

    start_all()
    dc.wait_for_unit("samba-dc.service")
    dc.wait_until_succeeds("samba-tool user list -s /var/lib/samba-dc/etc/smb.conf | grep -qi administrator", timeout=300)
    dc.wait_for_open_port(389, timeout=600)
    dc.wait_for_open_port(88, timeout=600)
    dc.succeed("samba-tool user create alice 'Alice-Pass-123!' -s /var/lib/samba-dc/etc/smb.conf")

    nas.wait_for_unit("dplaned.service")
    nas.wait_until_succeeds("curl -sf http://localhost/api/system/status >/dev/null", timeout=300)
    nas.wait_until_succeeds("host -t SRV _ldap._tcp.corp.test || getent hosts dc.corp.test", timeout=120)
    api("POST", "/api/system/setup-admin", {"username": "admin", "password": PASSWORD}, auth=False)
    sid = api("POST", "/api/auth/login", {"username": "admin", "password": PASSWORD}, auth=False)["session_id"]
    csrf = json.loads(nas.succeed(f"curl -s http://localhost/api/csrf -H 'X-Session-ID: {sid}' -H 'X-User: admin'"))["csrf_token"]
    session.update(sid=sid, csrf=csrf)

    with subtest("a wrong password is refused"):
        r = api("POST", "/api/directory/join", {"username": "Administrator", "password": "Wrong-Pass-1!", "domain": "CORP.TEST", "domain_controller": "dc.corp.test"})
        assert r.get("success") is not True, r
        assert r.get("code") in ("invalid_credentials", "kerberos_auth_failed"), f"refused for the wrong reason: {r}"
        nas.log(f"wrong password: {r}")

    with subtest("join the domain through the API"):
        r = api("POST", "/api/directory/join", {"username": "Administrator", "password": "${adminPass}", "domain": "CORP.TEST", "domain_controller": "dc.corp.test"})
        assert r.get("success") is True, r

    with subtest("trust and AD users on the NAS"):
        nas.wait_until_succeeds("wbinfo -t", timeout=120)
        nas.wait_until_succeeds("wbinfo -u | grep -qi alice", timeout=120)
        nas.wait_until_succeeds("getent passwd alice", timeout=120)
        st = api("GET", "/api/directory/status")
        nas.log(f"status: {st}")
  '';
}
