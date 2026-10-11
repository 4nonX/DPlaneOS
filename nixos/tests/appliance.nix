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
      fenced.package = daemonPackage;
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
    services.dplaneos.ota.enable = lib.mkForce false;
    # A small VM: PostgreSQL defaults are sized for a NAS.
    services.postgresql.settings = { max_connections = 50; shared_buffers = "64MB"; };
  };

  # Another machine on the same network: what the appliance offers is used
  # from here, through its firewall.
  nodes.client = { pkgs, ... }: {
    environment.systemPackages = [ pkgs.samba pkgs.curl pkgs.openssl ];
    virtualisation.memorySize = 768;
  };

  testScript = ''
    import json, shlex

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
            nas.succeed("echo | openssl s_client -connect localhost:443 2>/dev/null | openssl x509 -noout -subject | grep -q 'CN *= *dplaneos'")

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
            nas.wait_until_succeeds("timedatectl show-timesync -p SystemNTPServers --value | grep -qw 10.0.2.2", timeout=30)

            ok(api("POST", "/api/system/tuning", {"arc_limit_gb": 1, "swappiness": 7}), "tuning")
            assert nas.succeed("cat /proc/sys/vm/swappiness").strip() == "7"
            assert nas.succeed("cat /sys/module/zfs/parameters/zfs_arc_max").strip() == str(1 << 30)

            # A certificate made here and activated is what the web interface serves.
            ok(api("POST", "/api/certs/generate", {"name": "nascert", "cn": "tank-nas.example", "days": 30}), "generate certificate")
            ok(api("POST", "/api/certs/activate", {"name": "nascert"}), "activate certificate")
            nas.wait_until_succeeds("echo | openssl s_client -connect localhost:443 2>/dev/null | openssl x509 -noout -subject | grep -q 'tank-nas.example'", timeout=30)
            client.succeed(FETCH + f"-k https://{NAS}/")

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
            nas.succeed("sshd -T | grep -qx 'passwordauthentication no'")

            # Another port: sshd moves there and the firewall follows.
            ok(api("POST", "/api/system/ssh-daemon", {"port": 2222, "permit_root_login": "no"}), "SSH port")
            client.wait_until_succeeds(SSH + f"-p 2222 admin@{NAS} true", timeout=30)
            client.fail(SSH + f"admin@{NAS} true")
            assert "2222/tcp" in api("GET", "/api/firewall/status")["system_ports"]
            assert refused(api("POST", "/api/system/ssh-daemon", {"port": 2222, "permit_root_login": "sometimes"}))

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
            assert [p["name"] for p in api("GET", "/api/zfs/pools")["pools"]] == ["tank"]
            nas.wait_until_succeeds("exportfs -v | grep -q " + mp, timeout=60)
            nas.succeed("testparm -s 2>/dev/null | grep -q '^\\[data\\]'")
            # The system settings were applied before anything else started.
            assert nas.succeed("hostname").strip() == "tank-nas"
            assert nas.succeed("readlink /etc/localtime").strip() == "/etc/zoneinfo/Europe/Berlin"
            assert nas.succeed("cat /proc/sys/vm/swappiness").strip() == "7"
            nas.wait_until_succeeds("test \"$(cat /sys/module/zfs/parameters/zfs_arc_max)\" = " + str(1 << 30), timeout=60)
            nas.succeed("timedatectl show-timesync -p SystemNTPServers --value | grep -qw 10.0.2.2")
            nas.succeed("echo | openssl s_client -connect localhost:443 2>/dev/null | openssl x509 -noout -subject | grep -q 'tank-nas.example'")
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
            assert api("GET", "/api/zfs/pools")["pools"][0]["health"] == "DEGRADED"
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
