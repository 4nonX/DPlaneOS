# nixos/tests/install.nix
# ─────────────────────────────────────────────────────────────────────────────
# The installer, end to end: a machine with an empty disk runs install.sh
# (as the ISO does, without questions), then the machine is started from
# that disk, through its boot loader, with nothing but what the installer
# left there.
#
# Checked on the installed system: it boots, the partitions are where they
# belong, the administrator signs in with the password given to the
# installer, the system is generation 1 of its profile and can check for
# updates, and what is configured survives a restart.
#
# Run: nix build .#checks.x86_64-linux.install -L
# ─────────────────────────────────────────────────────────────────────────────
{ nixpkgs, system ? "x86_64-linux", applianceModules, ... }:

let
  pkgs = nixpkgs.legacyPackages.${system};

  # The system the installer installs: the appliance, plus what lets the
  # test talk to it after it has booted from its own disk.
  target = nixpkgs.lib.nixosSystem {
    inherit system pkgs;
    modules = applianceModules ++ [
      "${nixpkgs}/nixos/modules/testing/test-instrumentation.nix"
      ({ lib, ... }: {
        # A small VM: PostgreSQL defaults are sized for a NAS.
        services.postgresql.settings = { max_connections = 50; shared_buffers = "64MB"; };
      })
    ];
  };
  targetSystem = target.config.system.build.toplevel;

  # Installer and installed system use the same disk.
  common = {
    virtualisation.diskImage = "./target.qcow2";
    virtualisation.diskSize = 40 * 1024;
    virtualisation.cores = 2;
    virtualisation.memorySize = 3072;
  };

  PASSWORD = "Installer-Test-Pass-1";

in

pkgs.testers.nixosTest {
  name = "dplaneos-install";

  nodes.installer = { pkgs, ... }: {
    imports = [
      common
      ../installer-common.nix
      "${nixpkgs}/nixos/tests/common/auto-format-root-device.nix"
    ];
    dplaneos.installer.targetSystem = targetSystem;
    environment.etc."dplaneos-install/VERSION".text = "test";

    # The installer's own root is a small second disk, so that the disk to
    # install on is the first one, during and after the installation.
    virtualisation.emptyDiskImages = [ 2048 ];
    virtualisation.rootDevice = "/dev/vdb";

    # The system to install is in this machine's store, as on the ISO.
    virtualisation.additionalPaths = [ targetSystem ];
    boot.supportedFilesystems = [ "vfat" "ext4" ];
  };

  # Only starts the disk the installer wrote, with UEFI firmware.
  nodes.target = {
    imports = [ common ];
    virtualisation.useBootLoader = true;
    virtualisation.useEFIBoot = true;
    virtualisation.useDefaultFilesystems = false;
    virtualisation.efi.keepVariables = false;
    virtualisation.fileSystems."/" = {
      device = "/dev/disk/by-label/not-used-the-installed-system-has-its-own";
      fsType = "ext4";
    };
  };

  testScript = ''
    import json, shlex

    PASSWORD = "${PASSWORD}"
    INSTALL = ("DPLANEOS_INSTALL_UNATTENDED=1 DPLANEOS_INSTALL_ASSUME_UEFI=1 DPLANEOS_INSTALL_PASSWORD_FILE=/tmp/password "
               "DPLANEOS_INSTALL_DISK={} bash /etc/dplaneos-install/install.sh 2>&1")
    session = {}

    def api(method, path, body=None, auth=True):
        cmd = f"curl -s --max-time 180 -X {method} http://localhost{path} -H 'Content-Type: application/json'"
        if auth:
            cmd += f" -H 'X-Session-ID: {session['sid']}' -H 'X-User: admin' -H 'X-CSRF-Token: {session['csrf']}'"
        if body is not None:
            cmd += " -d " + shlex.quote(json.dumps(body))
        out = target.succeed(cmd)
        try:
            return json.loads(out)
        except Exception:
            raise Exception(f"{method} {path}: not JSON: {out[:500]}")

    def login(password=PASSWORD):
        resp = api("POST", "/api/auth/login", {"username": "admin", "password": password}, auth=False)
        if not resp.get("session_id"):
            raise Exception(f"login: {resp}")
        sid = resp["session_id"]
        csrf = json.loads(target.succeed(f"curl -s http://localhost/api/csrf -H 'X-Session-ID: {sid}' -H 'X-User: admin'"))["csrf_token"]
        session.update(sid=sid, csrf=csrf)

    def wait_up():
        target.wait_for_unit("multi-user.target")
        target.wait_for_unit("dplaned.service")
        target.wait_until_succeeds("curl -sf http://localhost/api/system/status >/dev/null", timeout=300)

    installer.start()
    installer.wait_for_unit("multi-user.target")
    installer.succeed("udevadm settle")

    with subtest("The installer refuses what cannot work"):
        installer.succeed("printf weak > /tmp/password")
        out = installer.fail(INSTALL.format("/dev/vda"))
        assert "Password must" in out, out
        installer.succeed(f"printf '%s' '{PASSWORD}' > /tmp/password")
        # Its own disk (in use, and too small).
        out = installer.fail(INSTALL.format("/dev/vdb"))
        assert "cannot be used" in out, out
        out = installer.fail(INSTALL.format("/dev/nosuchdisk"))
        assert "not a disk" in out, out
        # Nothing was written.
        installer.fail("lsblk -n -o TYPE /dev/vda | grep -q part")

    with subtest("Installation on the empty disk"):
        print(installer.succeed(INSTALL.format("/dev/vda")))
        labels = installer.succeed("lsblk -n -o PARTLABEL /dev/vda").split()
        assert labels == ["ESP", "system", "persist"], labels
        # Nothing is left mounted; the machine can be switched off.
        installer.fail("findmnt -n /mnt")
        installer.shutdown()

    target.start()
    try:
        with subtest("The installed system starts from its disk"):
            wait_up()
            assert target.succeed("findmnt -n -o PARTLABEL /").strip() == "system"
            assert target.succeed("findmnt -n -o PARTLABEL /persist").strip() == "persist"
            assert target.succeed("findmnt -n -o PARTLABEL /boot").strip() == "ESP"
            # Its state lives on the persistent partition.
            assert target.succeed("findmnt -n -o SOURCE /var/lib/dplaneos").strip().startswith("/dev/")
            target.succeed("test -d /persist/var/lib/dplaneos/pgsql")
            failed = target.succeed("systemctl --failed --no-legend --plain | awk '{print $1}'").split()
            assert failed == [], f"failed units: {failed}"
            assert target.succeed("hostname").strip() == "dplaneos"
            target.succeed("journalctl -b -u persist-health-check | grep -q 'persist-health-check: OK'")

        with subtest("The administrator signs in with the password given to the installer"):
            login()
            # The hash the installer left was used and is gone.
            target.fail("test -e /var/lib/dplaneos/.first-boot-password")
            # Nobody else can claim the system through the setup dialog.
            resp = api("POST", "/api/system/setup-admin", {"username": "admin", "password": "Another-Pass-2"}, auth=False)
            assert resp.get("success") is not True, resp
            bad = api("POST", "/api/auth/login", {"username": "admin", "password": "Another-Pass-2"}, auth=False)
            assert not bad.get("session_id"), bad

        with subtest("The system knows its version and can be updated"):
            assert target.succeed("readlink /nix/var/nix/profiles/system").strip() == "system-1-link"
            st = api("GET", "/api/system/update/status")
            assert st["supported"] is True and st["can_verify"] is True, st
            assert st["running_version"] == st["installed_version"] != "unknown", st
            assert [g["generation"] for g in st["generations"]] == [1], st
            target.succeed("bootctl status | grep -q systemd-boot")

        with subtest("What is configured survives a restart"):
            resp = api("POST", "/api/system/settings", {"hostname": "installed-nas"})
            assert resp.get("success") is True, resp
            host_id = target.succeed("hostid").strip()
            machine_id = target.succeed("cat /etc/machine-id").strip()
            ssh_key = target.succeed("cat /var/lib/dplaneos/ssh/ssh_host_ed25519_key.pub").strip()
            target.shutdown()
            target.start()
            wait_up()
            assert target.succeed("hostname").strip() == "installed-nas"
            assert target.succeed("hostid").strip() == host_id
            assert target.succeed("cat /etc/machine-id").strip() == machine_id
            assert target.succeed("cat /var/lib/dplaneos/ssh/ssh_host_ed25519_key.pub").strip() == ssh_key
            login()
            failed = target.succeed("systemctl --failed --no-legend --plain | awk '{print $1}'").split()
            assert failed == [], f"failed units after restart: {failed}"
    except Exception:
        print(target.execute("systemctl --failed --no-pager; journalctl -b --no-pager -u dplaned -u postgresql -u dplaneos-system-settings | tail -n 120; "
                             "findmnt; ls -la /var/lib/dplaneos /persist /persist/var/lib/dplaneos")[1])
        raise
  '';
}
