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
      ../dplane-generated.nix
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
        if resp.get("success") is not True:
            raise Exception(f"{what}: {resp}")
        return resp

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

    nas.start()
    try:
        with subtest("First boot: the system comes up without any machine-specific setting"):
            wait_up()
            # No unit failed (a placeholder container, a missing device...).
            failed = nas.succeed("systemctl --failed --no-legend --plain | awk '{print $1}'").split()
            assert failed == [], f"failed units: {failed}"
            # DHCP on a wired interface nobody named in the configuration.
            nas.wait_until_succeeds("ip -4 -o addr show dev eth0 | grep -q 'inet 10.0.2.'", timeout=60)

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
            # Restricted to the folder.
            nas.fail(FTP + "../../etc/passwd")
            nas.fail(f"curl -sS --fail --max-time 30 --user alice:wrong ftp://localhost/file")
            # Not in the list of allowed users.
            nas.fail(f"curl -sS --fail --max-time 30 --user admin:{PASSWORD} ftp://localhost/file")
            # Without a folder the service cannot be enabled.
            bad = dict(cfg, root_dir="")
            assert api("PUT", "/api/ftp/config", bad).get("success") is not True

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
            # The same account (number, password, group) after the reboot.
            nas.wait_until_succeeds(SMBCLIENT + f" //localhost/data -U alice%{USER_PASSWORD2} -c 'get from-alice.txt /tmp/back'", timeout=90)
            assert nas.succeed("id -u alice").strip() == alice_uid
            nas.succeed("id -nG alice | grep -qw family")
            # FTP is running again, with the same accounts.
            nas.wait_for_open_port(21)
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
    except Exception:
        diag()
        raise
  '';
}
