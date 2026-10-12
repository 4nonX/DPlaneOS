# nixos/tests/appliance.nix
# ─────────────────────────────────────────────────────────────────────────────
# The installed appliance, end to end: the configuration the installer ISO
# puts on disk (configuration-standalone.nix, module.nix, impermanence.nix,
# ota-module.nix), on a disk that survives reboots, with /persist as its own
# file system. Nothing is overridden that an installation would not have:
# no host name, no host ID, no interface names.
#
# It sets the system up through the API the way the web interface does,
# reboots, and checks that everything is still there and working.
#
# Run: nix build .#checks.x86_64-linux.appliance -L
# ─────────────────────────────────────────────────────────────────────────────
{ nixpkgs, system ? "x86_64-linux", impermanence, daemonPackage, frontendPackage, ... }:

let
  pkgs = nixpkgs.legacyPackages.${system};
  dataDisk = serial: { size = 512; driveConfig.deviceExtraOpts.serial = serial; };
in

pkgs.testers.nixosTest {
  name = "dplaneos-appliance";

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

    # Like an installation: NixOS owns neither the host name nor the host ID.
    networking.hostName = lib.mkForce "";

    virtualisation.cores = 2;
    virtualisation.memorySize = 3072;
    # vdb: /persist (the installer's persist partition). vdc, vdd: data disks.
    virtualisation.emptyDiskImages = [ 2048 (dataDisk "dplanedata0") (dataDisk "dplanedata1") ];
    virtualisation.fileSystems."/persist" = {
      device = "/dev/vdb";
      fsType = "ext4";
      autoFormat = true;
      neededForBoot = true;
    };
    # The test framework boots the kernel directly.
    boot.loader.systemd-boot.enable = lib.mkForce false;
    boot.loader.efi.canTouchEfiVariables = lib.mkForce false;
    # A small VM: PostgreSQL defaults are sized for a NAS.
    services.postgresql.settings = { max_connections = 50; shared_buffers = "64MB"; };
  };

  # Another machine on the same network: what the appliance offers is used
  # from here, through its firewall.
  nodes.client = { pkgs, ... }: {
    environment.systemPackages = [ pkgs.samba pkgs.curl pkgs.openssl pkgs.nvme-cli pkgs.rclone ];
    # A directory (OpenLDAP) with two people and a group.
    networking.firewall.allowedTCPPorts = [ 389 ];
    services.openldap = {
      enable = true;
      urlList = [ "ldap:///" ];
      settings = {
        children = {
          "cn=schema".includes = [
            "${pkgs.openldap}/etc/schema/core.ldif"
            "${pkgs.openldap}/etc/schema/cosine.ldif"
            "${pkgs.openldap}/etc/schema/inetorgperson.ldif"
          ];
          "olcDatabase={1}mdb".attrs = {
            objectClass = [ "olcDatabaseConfig" "olcMdbConfig" ];
            olcDatabase = "{1}mdb";
            olcDbDirectory = "/var/lib/openldap/db";
            olcSuffix = "dc=example,dc=test";
            olcRootDN = "cn=admin,dc=example,dc=test";
            olcRootPW = "ldapadmin";
            olcAccess = [ "{0}to * by * read" ];
          };
        };
      };
      declarativeContents."dc=example,dc=test" = ''
        dn: dc=example,dc=test
        objectClass: domain
        dc: example

        dn: ou=people,dc=example,dc=test
        objectClass: organizationalUnit
        ou: people

        dn: ou=groups,dc=example,dc=test
        objectClass: organizationalUnit
        ou: groups

        dn: uid=dora,ou=people,dc=example,dc=test
        objectClass: inetOrgPerson
        uid: dora
        cn: Dora Directory
        sn: Directory
        mail: dora@example.test
        userPassword: Dora-Dir-Pass-1

        dn: cn=storage-ops,ou=groups,dc=example,dc=test
        objectClass: groupOfNames
        cn: storage-ops
        member: uid=dora,ou=people,dc=example,dc=test
      '';
    };
    # An iSCSI initiator and an NVMe/TCP host.
    services.openiscsi = { enable = true; name = "iqn.2026-01.test.client:c1"; };
    boot.kernelModules = [ "nvme-tcp" ];
    virtualisation.memorySize = 768;
  };

  testScript = ''
    import json, shlex, time

    PASSWORD = "Appliance-Test-Pass-1"
    USER_PASSWORD = "Alice-Share-Pass-7"
    USER_PASSWORD2 = "Alice-Other-Pass-8"
    SMBCLIENT = "${pkgs.samba}/bin/smbclient"

    def smb(password, commands):
        return nas.succeed(SMBCLIENT + f" //localhost/data -U alice%{password} -c " + shlex.quote(commands))
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
        # Most endpoints answer {"success": true, ...}; a few return the
        # created object. Either way a failure says so.
        if not isinstance(resp, dict) or resp.get("success") is False or resp.get("error"):
            raise Exception(f"{what}: {resp}")
        return resp

    def refused(resp):
        return isinstance(resp, dict) and (resp.get("success") is False or bool(resp.get("error")))

    def login():
        sid = api("POST", "/api/auth/login", {"username": "admin", "password": PASSWORD}, auth=False)["session_id"]
        csrf = json.loads(nas.succeed(f"curl -s http://localhost/api/csrf -H 'X-Session-ID: {sid}' -H 'X-User: admin'"))["csrf_token"]
        session.update(sid=sid, csrf=csrf)

    def wait_up():
        nas.wait_for_unit("multi-user.target")
        nas.wait_for_unit("dplaned.service")
        nas.wait_until_succeeds("curl -sf http://localhost/api/system/status >/dev/null", timeout=180)

    def diag():
        print(nas.execute("systemctl --failed --no-pager; journalctl -b --no-pager -u dplaned -u dplaneos-hostid -u dplaneos-zfs-import "
                          "-u dplaneos-zfs-gate -u zfs-mount -u docker -u systemd-networkd | tail -n 150; "
                          "zpool status; zpool import; ip -br addr; ls -la /etc/systemd/network /var/lib/dplaneos")[1])

    D0 = "/dev/disk/by-id/virtio-dplanedata0"
    D1 = "/dev/disk/by-id/virtio-dplanedata1"

    def nas_address():
        return nas.succeed("ip -4 -o addr show dev eth1 | awk '{print $4}' | cut -d/ -f1 | head -n1").strip()

    def fw_rules():
        return ok(api("GET", "/api/firewall/status"), "firewall status")["rules"]

    def fw_delete(action, port, source):
        for rule in fw_rules():
            if rule["action"] == action and rule["port"] == str(port) and rule["from"] == source:
                ok(api("POST", "/api/firewall/rule", {"action": "delete", "rule_num": rule["id"]}), "delete rule")
                return
        raise Exception(f"no rule {action} {port} from {source}: {fw_rules()}")

    def listener(port):
        # Something that answers on a port no system service uses.
        nas.succeed(f"systemd-run --unit=listener-{port} --collect ${pkgs.python3}/bin/python3 -m http.server {port} --directory /var/empty")
        nas.wait_for_open_port(port)

    FETCH = "curl -sS --fail --max-time 6 -o /dev/null "

    nas.start()
    client.start()
    try:
        with subtest("First boot: the system comes up without any machine-specific setting"):
            wait_up()
            # No unit failed (a placeholder container, a missing device...).
            failed = nas.succeed("systemctl --failed --no-legend --plain | awk '{print $1}'").split()
            assert failed == [], f"failed units: {failed}"
            # DHCP on a wired interface nobody named in the configuration.
            nas.wait_until_succeeds("ip -4 -o addr show dev eth0 | grep -q 'inet 10.0.2.'", timeout=60)

            # /persist is checked at every boot (and found).
            nas.succeed("journalctl -b -u persist-health-check | grep -q 'persist-health-check: OK'")

            assert nas.succeed("hostname").strip() == "dplaneos", "a fresh system is called dplaneos"
            nas.succeed("grep -qx dplaneos /etc/hostname")
            # The web interface over HTTP and, with a certificate of its own, HTTPS;
            # from another machine, through the firewall.
            client.wait_for_unit("multi-user.target")
            NAS = nas_address()
            CLIENT = client.succeed("ip -4 -o addr show dev eth1 | awk '{print $4}' | cut -d/ -f1 | head -n1").strip()
            client.wait_until_succeeds(FETCH + f"http://{NAS}/", timeout=60)
            client.succeed(FETCH + f"-k https://{NAS}/")
            client.fail(FETCH + f"https://{NAS}/")  # self-signed: not trusted
            # What the browser is told: scripts from this system only, no
            # framing by other sites; on the pages and on API answers alike.
            for path in ("/", "/api/system/status"):
                head = client.succeed(f"curl -sk --max-time 30 -D - -o /dev/null https://{NAS}{path}").lower()
                assert "content-security-policy: default-src 'self'; script-src 'self';" in head, head
                assert "x-content-type-options: nosniff" in head and "x-frame-options: sameorigin" in head, head
            # The pages load nothing from anywhere else.
            page = client.succeed(f"curl -sk --max-time 30 https://{NAS}/")
            assert "http://" not in page and "https://" not in page, page
            nas.succeed("echo | ${pkgs.openssl}/bin/openssl s_client -connect localhost:443 2>/dev/null | ${pkgs.openssl}/bin/openssl x509 -noout -subject | grep -q 'CN *= *dplaneos'")

        with subtest("A ZFS host ID of its own"):
            hostid = nas.succeed("hostid").strip()
            assert hostid != "00000000" and len(hostid) == 8, hostid
            assert nas.succeed("cat /proc/sys/kernel/spl/hostid").strip().lstrip("0") in ("", hostid.lstrip("0")), "kernel and userland disagree"

        with subtest("Set up: admin, pool with multihost, dataset, share, export, snapshot schedule"):
            api("POST", "/api/system/setup-admin", {"username": "admin", "password": PASSWORD}, auth=False)
            login()
            ok(api("POST", "/api/system/pool/create", {"name": "tank", "topology": {"data": [{"type": "mirror", "disks": [D0, D1]}]}}), "create pool")
            # multihost needs a non-zero host ID that is this system's own.
            nas.succeed("zpool set multihost=on tank")
            ok(api("POST", "/api/zfs/datasets", {"name": "tank/data"}), "create dataset")
            mp = nas.succeed("zfs get -H -o value mountpoint tank/data").strip()
            nas.succeed(f"echo kept > {mp}/file")
            ok(api("POST", "/api/shares", {"action": "create", "name": "data", "path": mp}), "create share")
            ok(api("POST", "/api/nfs/exports", {"path": mp, "clients": "10.0.2.0/24", "options": "rw,sync,no_subtree_check,root_squash", "enabled": True}), "create export")
            nas.wait_until_succeeds("exportfs -v | grep -q " + mp, timeout=30)
            nas.succeed("testparm -s 2>/dev/null | grep -q '^\\[data\\]'")

        with subtest("A user of the web interface signs in to the share over SMB"):
            ok(api("POST", "/api/rbac/users", {"action": "create", "username": "alice", "password": USER_PASSWORD, "role": "user", "confirm_password": PASSWORD}), "create user")
            ok(api("POST", "/api/rbac/groups", {"action": "create", "name": "family", "members": ["alice"], "confirm_password": PASSWORD}), "create group")
            nas.wait_until_succeeds("pdbedit -L | grep -q '^alice:'", timeout=60)
            nas.wait_until_succeeds("id -nG alice | grep -qw family", timeout=60)
            assert nas.succeed("id -gn alice").strip() == "users"
            # No shell: the account is for file services only.
            assert nas.succeed("getent passwd alice | cut -d: -f7").strip().endswith("nologin")
            smb(USER_PASSWORD, "put /etc/os-release from-alice.txt; get file /tmp/got")
            nas.succeed("grep -qx kept /tmp/got")
            assert nas.succeed(f"stat -c %U:%G {mp}/from-alice.txt").strip() == "alice:users"
            # From another machine, through the firewall.
            client.succeed(f"smbclient //{NAS}/data -U alice%{USER_PASSWORD} -c 'get file /tmp/got-remote' && grep -qx kept /tmp/got-remote")
            client.fail(f"smbclient //{NAS}/data -U alice%wrong-password -c ls")
            # Without a guest share nobody gets in without a password.
            client.fail(f"smbclient //{NAS}/data -N -c ls")
            # A share with guest access is open to everyone; the others stay closed.
            nas.succeed(f"mkdir -p {mp}/pub && echo hello > {mp}/pub/readme && chmod -R a+rX {mp}/pub")
            ok(api("POST", "/api/shares", {"action": "create", "name": "public", "path": f"{mp}/pub", "guest_ok": True, "read_only": True}), "create guest share")
            client.wait_until_succeeds(f"smbclient //{NAS}/public -N -c 'get readme /tmp/readme' && grep -qx hello /tmp/readme", timeout=30)
            client.fail(f"smbclient //{NAS}/data -N -c ls")
            # Workgroup and description are settings, not part of the system image.
            assert nas.succeed("readlink -f /etc/samba/smb.conf").strip() == "/var/lib/dplaneos/smb-shares.conf"
            ok(api("POST", "/api/smb/settings", {"workgroup": "office", "server_string": "Team files"}), "SMB settings")
            nas.wait_until_succeeds("testparm -s 2>/dev/null | grep -q 'workgroup = OFFICE'", timeout=30)
            assert refused(api("POST", "/api/smb/settings", {"workgroup": "not a workgroup!"}))
            client.wait_until_succeeds(f"smbclient -L //{NAS} -U alice%{USER_PASSWORD} 2>/dev/null | grep -q 'Team files'", timeout=60)
            # The admin who set the system up has file access too.
            nas.wait_until_succeeds(SMBCLIENT + f" //localhost/data -U admin%{PASSWORD} -c ls", timeout=60)
            nas.fail(SMBCLIENT + " //localhost/data -U alice%wrong-password -c ls")
            nas.fail(SMBCLIENT + " //localhost/data -U nosuchuser%x -c ls")
            # A new password replaces the old one.
            alice = [u for u in api("GET", "/api/rbac/users")["users"] if u["username"] == "alice"][0]
            assert alice["file_access"] == "ready", alice
            ok(api("POST", "/api/rbac/users", {"action": "update", "id": alice["id"], "password": USER_PASSWORD2, "confirm_password": PASSWORD}), "change password")
            nas.wait_until_succeeds(SMBCLIENT + f" //localhost/data -U alice%{USER_PASSWORD2} -c ls", timeout=60)
            nas.fail(SMBCLIENT + f" //localhost/data -U alice%{USER_PASSWORD} -c ls")
            alice_uid = nas.succeed("id -u alice").strip()

        with subtest("The same user signs in over FTP"):
            cfg = api("GET", "/api/ftp/config")["config"]
            cfg.update(enabled=True, mode="ftp", root_dir=mp, allowed_users=["alice"])
            resp = ok(api("PUT", "/api/ftp/config", cfg), "enable FTP")
            assert not resp.get("warning"), resp
            nas.wait_for_open_port(21)
            FTP = f"curl -sS --fail --max-time 30 --user alice:{USER_PASSWORD2} ftp://localhost/"
            assert nas.succeed(FTP + "file").strip() == "kept"
            nas.succeed(FTP + " -T /etc/os-release")
            assert nas.succeed(f"stat -c %U:%G {mp}/os-release").strip() == "alice:users"
            # From another machine: the firewall opened the control port and the
            # passive range when FTP was switched on.
            assert client.succeed(f"curl -sS --fail --max-time 30 --user alice:{USER_PASSWORD2} ftp://{NAS}/file").strip() == "kept"
            # Restricted to the folder.
            nas.fail(FTP + "../../etc/passwd")
            nas.fail("curl -sS --fail --max-time 30 --user alice:wrong ftp://localhost/file")
            # Not in the list of allowed users.
            nas.fail(f"curl -sS --fail --max-time 30 --user admin:{PASSWORD} ftp://localhost/file")
            # Without a folder the service cannot be enabled.
            bad = dict(cfg, root_dir="")
            assert refused(api("PUT", "/api/ftp/config", bad))

        with subtest("Directory users: sync, group roles, first sign-in, scheduled sync"):
            client.wait_for_unit("openldap.service")
            def try_login(user, password):
                return api("POST", "/api/auth/login", {"username": user, "password": password}, auth=False)
            def attempt_from_client(user, password):
                body = shlex.quote(json.dumps({"username": user, "password": password}))
                return json.loads(client.succeed(f"curl -sk --max-time 60 -X POST https://{NAS}/api/auth/login -H 'Content-Type: application/json' -d {body}"))
            def refused_login(user, password):
                # Sign-ins that must fail are made from the other machine:
                # repeated failures make that address wait before its next
                # attempt (and only that address). The wait is sat out, so
                # that the answer is about the user and the password.
                for _ in range(60):
                    res = attempt_from_client(user, password)
                    if "Too many" not in str(res.get("error", "")):
                        return not res.get("session_id")
                    time.sleep(2)
                raise Exception("still made to wait after two minutes")
            def ldap_person(uid):
                ldif = (f"dn: uid={uid},ou=people,dc=example,dc=test\nobjectClass: inetOrgPerson\nuid: {uid}\n"
                        f"cn: {uid} person\nsn: person\nmail: {uid}@example.test\nuserPassword: {uid}-Dir-Pass-1\n")
                client.succeed(f"ldapadd -x -H ldap://localhost -D cn=admin,dc=example,dc=test -w ldapadmin <<'EOF'\n{ldif}EOF")
            directory = {"enabled": 1, "server": CLIENT, "port": 389, "use_tls": 0, "provider_type": "openldap",
                         "bind_dn": "cn=admin,dc=example,dc=test", "bind_password": "ldapadmin", "base_dn": "dc=example,dc=test",
                         "user_filter": "(&(objectClass=inetOrgPerson)(uid={username}))", "user_id_attr": "uid",
                         "user_name_attr": "cn", "user_email_attr": "mail",
                         "group_base_dn": "ou=groups,dc=example,dc=test", "group_filter": "(&(objectClass=groupOfNames)(member={user_dn}))",
                         "group_member_attr": "member", "jit_provisioning": 0, "default_role": "viewer", "sync_interval": 0, "timeout": 10}
            ok(api("POST", "/api/ldap/config", directory), "directory settings")
            ok(api("POST", "/api/ldap/test", {}), "directory test")
            ok(api("POST", "/api/ldap/mappings", {"ldap_group": "storage-ops", "role_name": "operator"}), "group mapping")
            # Unknown here until the first sync.
            assert refused_login("dora", "Dora-Dir-Pass-1")
            res = ok(api("POST", "/api/ldap/sync", {}), "sync")
            assert res["data"]["users_created"] == 1, res
            assert try_login("dora", "Dora-Dir-Pass-1").get("session_id"), "a synced directory user cannot sign in"
            assert refused_login("dora", "wrong")
            # The daemon knows which machine that was (it is behind the web
            # server, which has to tell it).
            nas.succeed(f"journalctl -u dplaned --no-pager | grep -F 'AUTH FAIL' | grep -F 'from {CLIENT}'")
            users = {u["username"]: u for u in api("GET", "/api/rbac/users")["users"]}
            assert users["dora"]["source"] == "ldap" and users["dora"]["file_access"] == "directory", users["dora"]
            dora_roles = nas.succeed("runuser -u postgres -- psql -d dplaneos -tAc \"SELECT r.name FROM user_roles ur JOIN roles r ON r.id = ur.role_id "
                                     "JOIN users u ON u.id = ur.user_id WHERE u.username = 'dora'\"").split()
            assert dora_roles == ["operator"], dora_roles

            # Somebody new in the directory: no account here, no sign-in ...
            ldap_person("jit")
            assert refused_login("jit", "jit-Dir-Pass-1")
            # ... unless accounts are created at the first sign-in.
            ok(api("POST", "/api/ldap/config", dict(directory, jit_provisioning=1)), "first sign-in creates accounts")
            assert refused_login("jit", "not-the-password")
            assert "jit" not in {u["username"] for u in api("GET", "/api/rbac/users")["users"]}, "an account was created for a wrong password"
            assert try_login("jit", "jit-Dir-Pass-1").get("session_id"), "first sign-in of a directory user failed"
            assert refused_login("nobody-at-all", "x")

            # The scheduled sync finds the next one by itself.
            ldap_person("sched")
            ok(api("POST", "/api/ldap/config", dict(directory, sync_interval=60)), "scheduled sync")
            nas.wait_until_succeeds("runuser -u postgres -- psql -d dplaneos -tAc \"SELECT 1 FROM users WHERE username = 'sched' AND source = 'ldap'\" | grep -qx 1", timeout=200)
            ok(api("POST", "/api/ldap/config", dict(directory, enabled=0)), "directory off")
            assert refused_login("dora", "Dora-Dir-Pass-1"), "sign-in although the directory is switched off"

            # Many failures from one address: that address has to wait, the
            # others do not.
            assert refused_login("admin", "guess")
            assert "Too many" in str(attempt_from_client("admin", "guess").get("error")), "no waiting time after repeated failures"
            assert try_login("admin", PASSWORD).get("session_id"), "failed sign-ins from another machine locked the administrator out"

        with subtest("System settings take effect at once"):
            ok(api("POST", "/api/system/settings", {"hostname": "tank-nas", "timezone": "Europe/Berlin"}), "host name and time zone")
            assert nas.succeed("hostname").strip() == "tank-nas"
            assert nas.succeed("readlink /etc/localtime").strip() == "/etc/zoneinfo/Europe/Berlin"
            assert nas.succeed("date +%Z").strip() in ("CET", "CEST")
            # A value the system does not take is refused, not stored.
            assert refused(api("POST", "/api/system/settings", {"timezone": "Mars/Olympus"}))
            assert nas.succeed("readlink /etc/localtime").strip() == "/etc/zoneinfo/Europe/Berlin"
            assert refused(api("POST", "/api/system/settings", {"hostname": "bad name!"}))
            assert nas.succeed("hostname").strip() == "tank-nas"

            ok(api("POST", "/api/system/ntp", {"servers": ["10.0.2.2"]}), "NTP servers")
            # (The time service itself is switched off in test machines; its
            # configuration is what can be checked here.)
            nas.succeed("grep -qx 'NTP=10.0.2.2' /run/systemd/timesyncd.conf.d/10-dplane.conf")

            ok(api("POST", "/api/system/tuning", {"arc_limit_gb": 1, "swappiness": 7}), "tuning")
            assert nas.succeed("cat /proc/sys/vm/swappiness").strip() == "7"
            assert nas.succeed("cat /sys/module/zfs/parameters/zfs_arc_max").strip() == str(1 << 30)

            # A certificate made here and activated is what the web interface serves.
            ok(api("POST", "/api/certs/generate", {"name": "nascert", "cn": "tank-nas.example", "days": 30}), "generate certificate")
            ok(api("POST", "/api/certs/activate", {"name": "nascert"}), "activate certificate")
            listed = ok(api("GET", "/api/certs/list"), "list certificates")
            assert listed["active_cert"] == "nascert", listed
            mine = [c for c in listed["certs"] if c["name"] == "nascert"][0]
            assert mine["subject"] == "tank-nas.example" and mine["issuer"] == "Self-signed" and mine["expires"].startswith("20"), mine
            nas.wait_until_succeeds("echo | ${pkgs.openssl}/bin/openssl s_client -connect localhost:443 2>/dev/null | ${pkgs.openssl}/bin/openssl x509 -noout -subject | grep -q 'tank-nas.example'", timeout=30)
            client.succeed(FETCH + f"-k https://{NAS}/")

        with subtest("iSCSI, NVMe-oF and S3: used from another machine"):
            IQN = "iqn.2026-01.test.nas:lun0"
            NQN = "nqn.2026-01.test.nas:nv0"
            nas.succeed("zfs create -V 64M tank/lun0 && zfs create -V 64M tank/nv0 && udevadm settle")
            nas.wait_until_succeeds("test -b /dev/zvol/tank/lun0 && test -b /dev/zvol/tank/nv0", timeout=30)

            # iSCSI: a target for one initiator.
            client.fail(f"timeout 5 bash -c 'echo > /dev/tcp/{NAS}/3260'")
            ok(api("POST", "/api/iscsi/targets", {"iqn": IQN, "zvol": "tank/lun0"}), "create iSCSI target")
            ok(api("POST", "/api/iscsi/acls", {"target_iqn": IQN, "initiator_iqn": "iqn.2026-01.test.client:c1"}), "allow the initiator")
            assert "3260/tcp" in api("GET", "/api/firewall/status")["system_ports"]
            client.wait_for_unit("iscsid.service")
            client.wait_until_succeeds(f"iscsiadm -m discovery -t sendtargets -p {NAS} | grep -q {IQN}", timeout=60)
            client.succeed(f"iscsiadm -m node -T {IQN} -p {NAS} --login")
            client.wait_until_succeeds("test -b /dev/disk/by-path/*iscsi*lun-0", timeout=30)
            ISCSI_DEV = client.succeed("readlink -f /dev/disk/by-path/*iscsi*lun-0").strip()
            ISCSI_ID = client.succeed("cat /sys/block/%s/device/wwid" % ISCSI_DEV.split("/")[-1]).strip()
            assert ISCSI_ID, "the iSCSI disk has no identity"
            listed = api("GET", "/api/iscsi/targets")["targets"]
            assert len(listed) == 1 and listed[0]["active"] and listed[0]["sessions"] == 1 and listed[0]["size"] == 64 << 20, listed
            client.succeed(f"echo written-over-iscsi | dd of={ISCSI_DEV} oflag=direct conv=sync bs=512 count=1 2>/dev/null")
            nas.wait_until_succeeds("dd if=/dev/zvol/tank/lun0 bs=512 count=1 2>/dev/null | grep -q written-over-iscsi", timeout=30)
            # Cluster software on the initiators reserves its disks; the
            # target has to keep those reservations.
            client.succeed(f"${pkgs.sg3_utils}/bin/sg_persist --out --register --param-sark=0xabc123 {ISCSI_DEV}")
            assert "0xabc123" in client.succeed(f"${pkgs.sg3_utils}/bin/sg_persist --in --read-keys {ISCSI_DEV}")
            client.succeed(f"${pkgs.sg3_utils}/bin/sg_persist --out --register --param-rk=0xabc123 {ISCSI_DEV}")
            client.succeed(f"iscsiadm -m node -T {IQN} -p {NAS} --logout")

            # NVMe over TCP.
            client.fail(f"timeout 5 bash -c 'echo > /dev/tcp/{NAS}/4420'")
            ok(api("POST", "/api/nvmet/targets", {"subsystem_nqn": NQN, "zvol": "tank/nv0", "allow_any_host": True}), "create NVMe-oF export")
            assert "4420/tcp" in api("GET", "/api/firewall/status")["system_ports"]
            client.wait_until_succeeds(f"nvme connect -t tcp -a {NAS} -s 4420 -n {NQN}", timeout=60)
            client.wait_until_succeeds("ls /dev/disk/by-id/ | grep -q nvme-", timeout=30)
            NVME_DEV = client.succeed("nvme list -o json | ${pkgs.jq}/bin/jq -r '.Devices[0].DevicePath'").strip()
            NVME_ID = client.succeed("cat /sys/block/%s/uuid" % NVME_DEV.split("/")[-1]).strip()
            assert NVME_ID and NVME_ID != "00000000-0000-0000-0000-000000000000", NVME_ID
            assert api("GET", "/api/nvmet/targets")["targets"][0]["active"] is True
            # A second export does not disturb the first one's initiator.
            nas.succeed("zfs create -V 16M tank/nv1 && udevadm settle")
            nas.wait_until_succeeds("test -b /dev/zvol/tank/nv1", timeout=30)
            ok(api("POST", "/api/nvmet/targets", {"subsystem_nqn": NQN + "-second", "zvol": "tank/nv1", "allow_any_host": True}), "second NVMe-oF export")
            assert client.succeed("cat /sys/class/nvme/nvme0/state").strip() == "live"
            assert "reconnect" not in client.succeed("dmesg | grep -i nvme || true").lower()
            ok(api("DELETE", "/api/nvmet/targets?subsystem_nqn=" + NQN + "-second"), "remove the second export")
            nas.succeed("zfs destroy tank/nv1")  # nothing holds the volume any more
            assert client.succeed("cat /sys/class/nvme/nvme0/state").strip() == "live"
            client.succeed(f"echo written-over-nvme | dd of={NVME_DEV} oflag=direct conv=sync bs=512 count=1 2>/dev/null")
            nas.wait_until_succeeds("dd if=/dev/zvol/tank/nv0 bs=512 count=1 2>/dev/null | grep -q written-over-nvme", timeout=30)
            client.succeed(f"nvme disconnect -n {NQN}")

            # S3: buckets and objects, with a real client; they are files in the dataset.
            st = ok(api("GET", "/api/s3/status"), "S3 status")
            assert st["installed"] is True and st["active"] is False, st
            assert refused(api("POST", "/api/s3/start", {})), "started without settings"
            nas.succeed(f"mkdir -p {mp}/s3")
            assert refused(api("PUT", "/api/s3/config", {"root_user": "s3admin", "root_password": "S3-Test-Pass-9", "volume_path": "/etc"}))
            ok(api("PUT", "/api/s3/config", {"root_user": "s3admin", "root_password": "S3-Test-Pass-9", "volume_path": f"{mp}/s3", "api_port": 9000}), "S3 settings")
            ok(api("POST", "/api/s3/start", {}), "start S3")
            client.wait_until_succeeds(FETCH + f"http://{NAS}:9000/health", timeout=90)
            def rclone(secret, args):
                return ("RCLONE_CONFIG_NAS_TYPE=s3 RCLONE_CONFIG_NAS_PROVIDER=Other RCLONE_CONFIG_NAS_ACCESS_KEY_ID=s3admin "
                        f"RCLONE_CONFIG_NAS_SECRET_ACCESS_KEY={secret} RCLONE_CONFIG_NAS_ENDPOINT=http://{NAS}:9000 "
                        "RCLONE_CONFIG_NAS_FORCE_PATH_STYLE=true rclone --copy-links --retries 1 --low-level-retries 1 --contimeout 10s " + args)
            client.succeed(rclone("S3-Test-Pass-9", "mkdir nas:backups"))
            client.succeed(rclone("S3-Test-Pass-9", "copyto /etc/os-release nas:backups/os-release"))
            client.succeed(rclone("S3-Test-Pass-9", "copyto nas:backups/os-release /tmp/from-s3") + " && cmp /tmp/from-s3 /etc/os-release")
            client.fail(rclone("wrong-secret-key", "lsf nas:backups"))
            client.fail(FETCH + f"http://{NAS}:9000/backups/os-release")  # not without credentials
            nas.succeed(f"cmp {mp}/s3/backups/os-release /etc/os-release")
            # Cloud sync: a task copies a folder into a bucket (this system's
            # own S3 service stands in for the cloud).
            ok(api("POST", "/api/cloud-sync", {"action": "configure", "name": "cloud", "type": "s3", "config": {
                "provider": "Other", "access_key_id": "s3admin", "secret_access_key": "S3-Test-Pass-9", "endpoint": "http://127.0.0.1:9000"}}), "cloud remote")
            assert api("POST", "/api/cloud-sync", {"action": "test", "remote": "cloud"})["connected"] is True
            nas.succeed(f"mkdir -p {mp}/cloudsrc && echo one > {mp}/cloudsrc/a.txt")
            # Never a whole remote, never a folder outside the pools.
            assert refused(api("POST", "/api/cloud-sync/tasks", {"name": "whole", "remote": "cloud", "remote_path": "", "local_path": f"{mp}/cloudsrc"}))
            assert refused(api("POST", "/api/cloud-sync/tasks", {"name": "etc", "remote": "cloud", "remote_path": "backups/etc", "local_path": "/etc"}))
            ok(api("POST", "/api/cloud-sync/tasks", {"name": "offsite", "remote": "cloud", "remote_path": "backups/cloud", "local_path": f"{mp}/cloudsrc", "interval": "manual"}), "cloud sync task")
            task = api("GET", "/api/cloud-sync/tasks")["tasks"][0]

            def run_task(dry=False):
                ok(api("POST", "/api/cloud-sync/tasks/" + task["id"] + "/run", {"dry_run": dry}), "run the task")
                for _ in range(60):
                    jobs = api("GET", "/api/cloud-sync/jobs")["jobs"]
                    if jobs and jobs[0]["status"] != "running":
                        assert jobs[0]["status"] == "done", jobs[0]
                        return
                    time.sleep(2)
                raise Exception("the cloud sync task did not finish")

            run_task(dry=True)
            nas.fail(f"test -e {mp}/s3/backups/cloud/a.txt")  # a dry run transfers nothing
            run_task()
            assert nas.succeed(f"cat {mp}/s3/backups/cloud/a.txt").strip() == "one"
            # Copy never deletes at the destination, and nothing next to the
            # task's folder is touched.
            nas.succeed(f"rm {mp}/cloudsrc/a.txt && echo two > {mp}/cloudsrc/b.txt")
            run_task()
            nas.succeed(f"test -f {mp}/s3/backups/cloud/a.txt && test -f {mp}/s3/backups/cloud/b.txt && cmp {mp}/s3/backups/os-release /etc/os-release")
            done = api("GET", "/api/cloud-sync/tasks")["tasks"][0]
            assert done["last_status"] == "done" and done["last_run"], done
            # A remote that a task uses is not removed from under it.
            assert refused(api("POST", "/api/cloud-sync", {"action": "delete", "remote": "cloud"}))

            # Stopped: the port closes again.
            ok(api("POST", "/api/s3/stop", {}), "stop S3")
            client.fail(f"timeout 5 bash -c 'echo > /dev/tcp/{NAS}/9000'")
            ok(api("POST", "/api/s3/start", {}), "start S3 again")
            client.wait_until_succeeds(FETCH + f"http://{NAS}:9000/health", timeout=90)

        with subtest("UPS: set up in the web interface"):
            UPS_FILE = "/var/lib/dplaneos/nut/test.dev"
            assert refused(api("GET", "/api/system/ups")), "a UPS is reported although none is set up"
            # A simulated UPS: NUT's dummy driver reads its state from a file.
            nas.succeed("mkdir -p /var/lib/dplaneos/nut && printf 'ups.status: OL\nbattery.charge: 90\nups.model: Test UPS\n' > " + UPS_FILE)
            ok(api("POST", "/api/system/ups", {"mode": "driver", "name": "testups", "driver": "dummy-ups", "port": UPS_FILE,
                                              "action": "shutdown", "threshold": 30, "grace": 20}), "set up the UPS")
            nas.wait_until_succeeds("upsc testups@localhost battery.charge 2>/dev/null | grep -qx 90", timeout=90)
            status = ok(api("GET", "/api/system/ups"), "UPS status")["data"]
            assert status["battery_charge"] == "90%" and status["model"] == "Test UPS" and status["status"] == "OL", status
            policy = api("GET", "/api/system/ups/policy")
            assert (policy["mode"], policy["driver"], policy["threshold"], policy["grace"]) == ("driver", "dummy-ups", 30, 20), policy
            assert refused(api("POST", "/api/system/ups", {"mode": "driver", "name": "x y", "driver": "dummy-ups", "port": UPS_FILE}))
            for unit in ("dplaneos-upsdrv", "dplaneos-upsd", "dplaneos-upsmon"):
                nas.succeed(f"systemctl is-active {unit}.service")
            # Not reachable from the network.
            client.fail(f"timeout 5 bash -c 'echo > /dev/tcp/{NAS}/3493'")

        with subtest("SSH: administrators sign in with a key; settings apply at once"):
            SSH = "ssh -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o ConnectTimeout=8 -o BatchMode=yes -i /root/key "
            client.succeed("ssh-keygen -q -t ed25519 -N ''' -f /root/key")
            pub = client.succeed("cat /root/key.pub").strip()
            # Nobody gets in before a key is added.
            client.fail(SSH + f"admin@{NAS} true")
            ok(api("POST", "/api/ssh/keys", {"username": "admin", "label": "test client", "public_key": pub}), "add key")
            client.wait_until_succeeds(SSH + f"admin@{NAS} true", timeout=30)
            assert client.succeed(SSH + f"admin@{NAS} id -un").strip() == "admin"
            # sudo with the password of the web interface.
            assert client.succeed(SSH + f"admin@{NAS} \"echo '{PASSWORD}' | sudo -S -p ''' id -u\"").strip() == "0"
            # The home directory survives system updates.
            assert nas.succeed("getent passwd admin | cut -d: -f6").strip() == "/var/lib/dplaneos/home/admin"
            # A user who is not an administrator has no shell account: no key, no login.
            assert refused(api("POST", "/api/ssh/keys", {"username": "alice", "label": "x", "public_key": pub}))
            client.fail(SSH + f"alice@{NAS} true")
            client.fail(SSH + f"root@{NAS} true")
            # No password sign-in.
            client.fail(f"ssh -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o ConnectTimeout=8 -o BatchMode=yes -o PubkeyAuthentication=no admin@{NAS} true")
            # What sshd itself says it uses (all of it in the log, for when this fails).
            effective = nas.succeed("$(readlink -f $(command -v sshd)) -T 2>&1; cat /var/lib/dplaneos/system/sshd_config.d/*.conf; grep -n -i -E 'Include|PasswordAuth|PermitRoot|KbdInter' /etc/ssh/sshd_config").lower()
            print(effective)
            settings = [l.strip() for l in effective.splitlines()]
            assert "passwordauthentication no" in settings and "permitrootlogin no" in settings, "password or root sign-in is not off"

            # Another port: sshd moves there and the firewall follows.
            ok(api("POST", "/api/system/ssh-daemon", {"port": 2222, "permit_root_login": "no"}), "SSH port")
            client.wait_until_succeeds(SSH + f"-p 2222 admin@{NAS} true", timeout=30)
            client.fail(SSH + f"admin@{NAS} true")
            assert "2222/tcp" in api("GET", "/api/firewall/status")["system_ports"]
            assert refused(api("POST", "/api/system/ssh-daemon", {"port": 2222, "permit_root_login": "sometimes"}))

        with subtest("Replication to a D-PlaneOS system: through an administrator account, without root sign-in"):
            # The target here is this same system, reached like any other:
            # over SSH, as the administrator who was given the source's key.
            nas.succeed("truncate -s 256M /var/lib/replica.img && zpool create replica /var/lib/replica.img")
            key = api("GET", "/api/replication/ssh-pubkey")
            if not key.get("public_key"):
                ok(api("POST", "/api/replication/ssh-keygen", {}), "replication key")
                key = api("GET", "/api/replication/ssh-pubkey")
            ok(api("POST", "/api/ssh/keys", {"username": "admin", "label": "replication from nas", "public_key": key["public_key"]}), "authorize the key on the target")
            peer = ok(api("POST", "/api/replication/remotes", {"name": "self", "host": "127.0.0.1", "user": "admin", "port": 2222}), "add peer")["remote"]
            res = ok(api("POST", f"/api/replication/remotes/{peer['id']}/test", {}), "test peer")
            assert res.get("zfs_ready") is True and res.get("zfs_access") == "sudo", res
            nas.succeed("zfs snapshot tank/data@repl1")
            jid = ok(api("POST", "/api/replication/remote", {"snapshot": "tank/data@repl1", "remote_id": peer["id"], "remote_pool": "replica"}), "replicate")["job_id"]
            nas.wait_until_succeeds("zfs list -H -t snapshot -o name replica/data@repl1", timeout=120)
            assert api("GET", f"/api/jobs/{jid}")["status"] in ("done", "running")
            nas.succeed("grep -qx kept /replica/data/file || (zfs mount replica/data; grep -qx kept /replica/data/file)")
            # An incremental run on top of it.
            nas.succeed(f"echo more > {mp}/second && zfs snapshot tank/data@repl2")
            ok(api("POST", "/api/replication/remote", {"snapshot": "tank/data@repl2", "remote_id": peer["id"], "remote_pool": "replica",
                                                       "incremental": True, "base_snapshot": "tank/data@repl1"}), "replicate incrementally")
            nas.wait_until_succeeds("zfs list -H -t snapshot -o name replica/data@repl2", timeout=120)
            nas.succeed("zpool export replica")

        with subtest("Firewall: rules take effect at once, deny beats allow"):
            listener(8088)
            listener(8089)
            status = ok(api("GET", "/api/firewall/status"), "firewall status")
            assert status["status"] == "active" and status["rules"] == [], status
            for port in ("80/tcp", "443/tcp", "445/tcp", "2222/tcp"):
                assert port in status["system_ports"], (port, status["system_ports"])
            client.fail(FETCH + f"http://{NAS}:8088/")

            ok(api("POST", "/api/firewall/rule", {"action": "allow", "port": "8088", "proto": "tcp"}), "allow 8088")
            client.succeed(FETCH + f"http://{NAS}:8088/")
            client.fail(FETCH + f"http://{NAS}:8089/")

            # Only from one source.
            ok(api("POST", "/api/firewall/rule", {"action": "allow", "port": "8089", "proto": "tcp", "from": "10.99.0.0/24"}), "allow 8089 from elsewhere")
            client.fail(FETCH + f"http://{NAS}:8089/")
            ok(api("POST", "/api/firewall/rule", {"action": "allow", "port": "8089", "proto": "tcp", "from": CLIENT}), "allow 8089 from the client")
            client.succeed(FETCH + f"http://{NAS}:8089/")

            # A deny rule closes a port another rule opens, and one the system opens.
            ok(api("POST", "/api/firewall/rule", {"action": "deny", "port": "8088", "proto": "tcp", "from": CLIENT}), "deny 8088")
            client.fail(FETCH + f"http://{NAS}:8088/")
            ok(api("POST", "/api/firewall/rule", {"action": "deny", "port": "80", "proto": "tcp", "from": CLIENT}), "deny 80")
            client.fail(FETCH + f"http://{NAS}/")
            nas.succeed("curl -sf http://localhost/api/system/status >/dev/null")  # not the machine itself
            assert [r["action"] for r in fw_rules()][:2] == ["deny", "deny"], fw_rules()
            fw_delete("deny", 80, CLIENT)
            fw_delete("deny", 8088, CLIENT)
            client.succeed(FETCH + f"http://{NAS}/")
            client.succeed(FETCH + f"http://{NAS}:8088/")

            # Invalid rules are refused.
            for bad in ({"action": "allow", "port": "http"}, {"action": "allow", "port": "99999"},
                        {"action": "allow", "port": "80", "from": "10.0.0.0/24; reboot"}, {"action": "delete", "rule_num": 99}):
                assert refused(api("POST", "/api/firewall/rule", bad)), bad

            # Off: everything is reachable. On again: as before.
            fw_delete("allow", 8089, CLIENT)
            client.fail(FETCH + f"http://{NAS}:8089/")
            ok(api("POST", "/api/firewall/rule", {"action": "disable"}), "firewall off")
            assert api("GET", "/api/firewall/status")["status"] == "inactive"
            client.succeed(FETCH + f"http://{NAS}:8089/")
            ok(api("POST", "/api/firewall/rule", {"action": "enable"}), "firewall on")
            assert api("GET", "/api/firewall/status")["status"] == "active"
            client.fail(FETCH + f"http://{NAS}:8089/")
            client.succeed(FETCH + f"http://{NAS}:8088/")

        with subtest("Network settings: tried, confirmed or reverted, and nothing left behind"):
            LIVE, KEPT = "/run/systemd/network", "/var/lib/dplaneos/network"
            def addr(dev, cidr):
                return f"ip -4 -o addr show dev {dev} | grep -qw {cidr}"

            # A static address that is not confirmed is undone.
            resp = ok(api("PUT", "/api/system/network", {"action": "configure", "interface": "eth1", "dhcp": False, "address": "10.55.0.2/24"}), "configure eth1")
            assert resp["revert_seconds"] > 0, resp
            nas.succeed(addr("eth1", "10.55.0.2/24"))
            nas.fail(f"test -e {KEPT}/10-dplane-eth1.network")
            nas.wait_until_fails(addr("eth1", "10.55.0.2/24"), timeout=90)
            nas.fail(f"test -e {LIVE}/10-dplane-eth1.network")
            # The interface the web interface is reached on still has its DHCP address.
            nas.succeed("ip -4 -o addr show dev eth0 | grep -q 'inet 10.0.2.'")

            # Confirmed, it is kept.
            ok(api("PUT", "/api/system/network", {"action": "configure", "interface": "eth1", "dhcp": False, "address": "10.55.0.2/24"}), "configure eth1")
            ok(api("POST", "/api/network/confirm", {}), "confirm")
            nas.succeed(f"grep -q 'Address=10.55.0.2/24' {KEPT}/10-dplane-eth1.network {LIVE}/10-dplane-eth1.network")
            # An address networkd cannot set is reported, not offered for confirmation.
            bad = api("PUT", "/api/system/network", {"action": "configure", "interface": "eth1", "dhcp": False, "address": "10.55.0.2/24", "gateway": "not-an-ip"})
            assert refused(bad), bad

            # Two VLANs on the same interface, one with an address.
            ok(api("POST", "/api/network/vlan", {"parent": "eth1", "vlan_id": 100, "ip": "10.77.0.1/24"}), "create VLAN 100")
            ok(api("POST", "/api/network/vlan", {"parent": "eth1", "vlan_id": 200}), "create VLAN 200")
            nas.wait_until_succeeds(addr("eth1.100", "10.77.0.1/24"), timeout=30)
            # One file for eth1: its address and both VLANs.
            eth1 = nas.succeed(f"cat {LIVE}/10-dplane-eth1.network")
            assert "Address=10.55.0.2/24" in eth1 and "VLAN=eth1.100" in eth1 and "VLAN=eth1.200" in eth1, eth1
            nas.wait_until_succeeds(addr("eth1", "10.55.0.2/24"), timeout=30)
            nas.wait_until_succeeds("networkctl status eth1.100 | grep -q 'Network File: /run/systemd/network/10-dplane-eth1.100.network'", timeout=30)

            # Deleting a VLAN removes its device and every trace in the files.
            ok(api("DELETE", "/api/network/vlan", {"interface": "eth1.200"}), "delete VLAN 200")
            nas.fail("ip link show eth1.200")
            nas.fail(f"ls {LIVE} {KEPT} | grep -F 'eth1.200'")
            nas.fail(f"grep -rF 'eth1.200' {LIVE} {KEPT}")
            nas.succeed(addr("eth1.100", "10.77.0.1/24"))

            # DNS servers and a search domain.
            ok(api("PUT", "/api/system/network", {"action": "set_dns", "nameservers": ["10.0.2.3", "9.9.9.9"], "search": ["lan.example"]}), "set DNS")
            nas.wait_until_succeeds("resolvectl dns | grep -F 'Global' | grep -qF '9.9.9.9'", timeout=30)
            nas.succeed("resolvectl domain | grep -F 'Global' | grep -qF 'lan.example'")

        with subtest("Reboot: everything is still there"):
            nas.shutdown()
            nas.start()
            wait_up()
            failed = nas.succeed("systemctl --failed --no-legend --plain | awk '{print $1}'").split()
            assert failed == [], f"failed units after reboot: {failed}"
            assert nas.succeed("hostid").strip() == hostid, "the host ID changed across a reboot"
            nas.wait_until_succeeds("zpool list -H -o name,health | grep -q '^tank.ONLINE'", timeout=60)
            nas.succeed(f"grep -qx kept {mp}/file")
            assert nas.succeed("zpool get -H -o value multihost tank").strip() == "on"
            # Docker was started after the pools were mounted.
            nas.succeed("test \"$(systemctl show -p ActiveEnterTimestampMonotonic --value docker)\" -gt "
                        "\"$(systemctl show -p ExecMainExitTimestampMonotonic --value dplaneos-zfs-gate)\"")
            nas.succeed("test -e /run/dplaneos/zfs-ready")
            login()
            assert [p["name"] for p in api("GET", "/api/zfs/pools")["data"]] == ["tank"]
            nas.wait_until_succeeds("exportfs -v | grep -q " + mp, timeout=60)
            nas.succeed("testparm -s 2>/dev/null | grep -q '^\\[data\\]'")
            # The system settings were applied before anything else started.
            assert nas.succeed("hostname").strip() == "tank-nas"
            assert nas.succeed("readlink /etc/localtime").strip() == "/etc/zoneinfo/Europe/Berlin"
            assert nas.succeed("cat /proc/sys/vm/swappiness").strip() == "7"
            nas.wait_until_succeeds("test \"$(cat /sys/module/zfs/parameters/zfs_arc_max)\" = " + str(1 << 30), timeout=60)
            nas.succeed("grep -qx 'NTP=10.0.2.2' /run/systemd/timesyncd.conf.d/10-dplane.conf")
            nas.succeed("echo | ${pkgs.openssl}/bin/openssl s_client -connect localhost:443 2>/dev/null | ${pkgs.openssl}/bin/openssl x509 -noout -subject | grep -q 'tank-nas.example'")
            nas.succeed("systemctl is-active dplaneos-system-settings.service")
            # The UPS is monitored again.
            nas.wait_until_succeeds("upsc testups@localhost battery.charge 2>/dev/null | grep -qx 90", timeout=90)
            nas.succeed("systemctl is-active dplaneos-upsmon.service")
            # Settings can still be changed after a reboot (the settings file is writable).
            ok(api("POST", "/api/firewall/rule", {"action": "allow", "port": "8090", "proto": "udp"}), "a rule after the reboot")
            fw_delete("allow", 8090, "Anywhere")
            # The firewall rules are in force again.
            listener(8088)
            listener(8089)
            assert [(r["action"], r["port"], r["from"]) for r in fw_rules()] == [("allow", "8089", "10.99.0.0/24"), ("allow", "8088", "Anywhere")], fw_rules()
            nas.succeed("iptables -w -C nixos-fw -p tcp --dport 8088 -j nixos-fw-accept")

            # The network came up as configured (before the database was there).
            nas.succeed("ip -4 -o addr show dev eth1 | grep -qw 10.55.0.2/24")
            nas.succeed("ip -4 -o addr show dev eth1.100 | grep -qw 10.77.0.1/24")
            nas.fail("ip link show eth1.200")
            nas.succeed("ip -4 -o addr show dev eth0 | grep -q 'inet 10.0.2.'")
            nas.succeed("resolvectl dns | grep -F 'Global' | grep -qF '9.9.9.9'")
            nas.succeed("systemctl is-active dplaneos-network.service")
            # Back to DHCP and without VLAN: no file of ours is left.
            ok(api("DELETE", "/api/network/vlan", {"interface": "eth1.100"}), "delete VLAN 100")
            ok(api("PUT", "/api/system/network", {"action": "configure", "interface": "eth1", "dhcp": True}), "eth1 back to DHCP")
            ok(api("POST", "/api/network/confirm", {}), "confirm")
            nas.fail("ip link show eth1.100")
            nas.wait_until_fails("ip -4 -o addr show dev eth1 | grep -qw 10.55.0.2/24", timeout=30)

            # The same account (number, password, group) after the reboot.
            nas.wait_until_succeeds(SMBCLIENT + f" //localhost/data -U alice%{USER_PASSWORD2} -c 'get from-alice.txt /tmp/back'", timeout=90)
            assert nas.succeed("id -u alice").strip() == alice_uid
            nas.succeed("id -nG alice | grep -qw family")
            # eth1 has its own address again: the client reaches the system.
            client.wait_until_succeeds(FETCH + f"http://{NAS}:8088/", timeout=60)
            client.fail(FETCH + f"http://{NAS}:8089/")
            # SSH on its port, with the administrator's key.
            client.wait_until_succeeds(SSH + f"-p 2222 admin@{NAS} true", timeout=60)
            client.fail(SSH + f"admin@{NAS} true")
            # The block targets and S3 are back, with their data.
            client.wait_until_succeeds(f"iscsiadm -m discovery -t sendtargets -p {NAS} | grep -q {IQN}", timeout=120)
            client.succeed(f"iscsiadm -m node -T {IQN} -p {NAS} --login")
            client.wait_until_succeeds("test -b /dev/disk/by-path/*iscsi*lun-0", timeout=30)
            client.succeed("dd if=$(readlink -f /dev/disk/by-path/*iscsi*lun-0) bs=512 count=1 2>/dev/null | grep -q written-over-iscsi")
            # The same disk as before the restart, for the initiator too.
            assert client.succeed("cat /sys/block/$(basename $(readlink -f /dev/disk/by-path/*iscsi*lun-0))/device/wwid").strip() == ISCSI_ID
            client.succeed(f"iscsiadm -m node -T {IQN} -p {NAS} --logout")
            client.wait_until_succeeds(f"nvme connect -t tcp -a {NAS} -s 4420 -n {NQN}", timeout=120)
            client.wait_until_succeeds("nvme list -o json | ${pkgs.jq}/bin/jq -e '.Devices | length > 0'", timeout=30)
            client.succeed("dd if=$(nvme list -o json | ${pkgs.jq}/bin/jq -r '.Devices[0].DevicePath') bs=512 count=1 2>/dev/null | grep -q written-over-nvme")
            assert client.succeed("cat /sys/block/$(basename $(nvme list -o json | ${pkgs.jq}/bin/jq -r '.Devices[0].DevicePath'))/uuid").strip() == NVME_ID
            client.succeed(f"nvme disconnect -n {NQN}")
            # Removed: the ports close and the volumes are free again.
            ok(api("DELETE", "/api/iscsi/targets/" + IQN), "delete the iSCSI target")
            ok(api("DELETE", "/api/nvmet/targets?subsystem_nqn=" + NQN), "remove the NVMe-oF export")
            ports = api("GET", "/api/firewall/status")["system_ports"]
            assert "3260/tcp" not in ports and "4420/tcp" not in ports, ports
            nas.succeed("zfs destroy tank/lun0 && zfs destroy tank/nv0")
            client.wait_until_succeeds(rclone("S3-Test-Pass-9", "copyto nas:backups/os-release /tmp/from-s3-again"), timeout=120)
            client.succeed("cmp /tmp/from-s3-again /etc/os-release")
            # FTP is running again, with the same accounts, and reachable.
            nas.wait_for_open_port(21)
            client.wait_until_succeeds(f"curl -sS --fail --max-time 30 --user alice:{USER_PASSWORD2} ftp://{NAS}/file", timeout=60)
            client.succeed(f"smbclient //{NAS}/data -U alice%{USER_PASSWORD2} -c ls")
            assert nas.succeed(f"curl -sS --fail --max-time 30 --user alice:{USER_PASSWORD2} ftp://localhost/file").strip() == "kept"
            # Deleting the user ends its file access.
            ok(api("POST", "/api/rbac/users", {"action": "delete", "id": alice["id"], "confirm_password": PASSWORD}), "delete user")
            nas.wait_until_fails("getent passwd alice", timeout=60)
            nas.fail(SMBCLIENT + f" //localhost/data -U alice%{USER_PASSWORD2} -c ls")
            nas.fail(f"curl -sS --fail --max-time 30 --user alice:{USER_PASSWORD2} ftp://localhost/file")

        with subtest("A degraded pool does not keep the web interface from starting"):
            nas.succeed(f"zpool offline tank {D1}")
            nas.shutdown()
            nas.start()
            wait_up()
            nas.wait_until_succeeds("zpool list -H -o health tank | grep -qx DEGRADED", timeout=60)
            login()
            assert api("GET", "/api/zfs/pools")["data"][0]["health"] == "DEGRADED"
            nas.succeed(f"zpool online tank {D1}")

        with subtest("UPS: on battery below the configured level, the system shuts down"):
            nas.wait_until_succeeds("upsc testups@localhost ups.status 2>/dev/null | grep -qx OL", timeout=90)
            # On battery, but above the level: nothing happens.
            nas.succeed("printf 'ups.status: OB\nbattery.charge: 60\nups.model: Test UPS\n' > " + UPS_FILE)
            nas.wait_until_succeeds("upsc testups@localhost ups.status 2>/dev/null | grep -qx OB", timeout=60)
            nas.sleep(15)
            nas.succeed("systemctl is-system-running | grep -qv stopping")
            # Below it: low battery, and the system powers off by itself.
            nas.succeed("printf 'ups.status: OB\nbattery.charge: 20\nups.model: Test UPS\n' > " + UPS_FILE)
            nas.wait_for_shutdown()
    except Exception:
        diag()
        raise
  '';
}
