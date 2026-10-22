# nixos/tests/update.nix
# ─────────────────────────────────────────────────────────────────────────────
# Updates: the check after the reboot and the automatic way back
# (ota-module.nix, dplaneos-update-health.sh).
#
# The machine boots through systemd-boot from its own disk, so boot entries,
# the system profile and reboots are real. Two specialisations stand in for
# releases: "good" (a working system) and "broken" (no web server). Each
# update is staged the way the daemon stages it (new generation as the boot
# default, update-pending.json, new flake.nix), then the machine reboots:
#
#   1. good:   the check keeps it (result ok, flake kept, no pending file)
#   2. broken: the check fails, makes the previous generation the boot
#              default, restores the flake and reboots; the machine comes
#              back on the good system (result rolled_back with the reason)
#
# Run: nix build .#checks.x86_64-linux.update -L
# ─────────────────────────────────────────────────────────────────────────────
{ nixpkgs, system ? "x86_64-linux", daemonPackage, frontendPackage, ... }:

let
  pkgs = nixpkgs.legacyPackages.${system};
in

pkgs.testers.nixosTest {
  name = "dplaneos-update";

  nodes.machine = { lib, ... }: {
    imports = [ ../module.nix ../ota-module.nix ];
    services.dplaneos = {
      enable = true;
      inherit daemonPackage frontendPackage;
      dbPath = "/var/lib/dplaneos/pgsql";
      ota.healthCheckDelay = "5s";
      ota.checkSeconds = 45;
    };
    networking.hostId = "dd0e0001";
    environment.etc."dplaneos-variant".text = "base";
    specialisation.good.configuration = {
      environment.etc."dplaneos-variant".text = lib.mkForce "good";
    };
    specialisation.broken.configuration = {
      environment.etc."dplaneos-variant".text = lib.mkForce "broken";
      systemd.services.nginx.enable = lib.mkForce false; # no web server
    };
    virtualisation.useBootLoader = true;
    virtualisation.useEFIBoot = true;
    boot.loader.systemd-boot.enable = true;
    boot.loader.efi.canTouchEfiVariables = true;
    virtualisation.memorySize = 2048;
    virtualisation.cores = 2;
  };

  testScript = ''
    import json

    def ready():
        machine.wait_for_unit("multi-user.target")
        machine.wait_for_unit("dplaned.service")
        # the check runs once per boot (timer, 5 s after boot)
        machine.wait_until_succeeds("journalctl -b -u dplaneos-ota-health.service | grep -qE 'dplaneos-update-health: (no update|update to|going back)'", timeout=300)
        machine.log(machine.succeed("readlink -f /run/current-system; cat /etc/dplaneos-variant; journalctl -b -u dplaneos-ota-health.service --no-pager | grep dplaneos-update-health || true"))

    def stage(variant, to_version):
        """Stage an update the way the daemon does."""
        prev = machine.succeed("readlink -f /run/current-system").strip()
        new = machine.succeed(f"readlink -f {base}/specialisation/{variant}").strip()
        machine.succeed(f"nix-env --profile /nix/var/nix/profiles/system --set {new}")
        machine.succeed(f"{new}/bin/switch-to-configuration boot")
        machine.log(machine.succeed("cat /boot/loader/loader.conf; ls /boot/loader/entries; bootctl list --no-pager 2>&1 | head -40; ls -l /nix/var/nix/profiles/"))
        old_flake = machine.succeed("cat /etc/nixos/flake.nix").strip()
        pending = {
            "previous_system": prev,
            "previous_flake": old_flake + "\n",
            "previous_lock": "",
            "from_version": "1.0.0",
            "to_version": to_version,
            "pools": [],
            "smb_shares": 0,
        }
        machine.succeed(f"echo {json.dumps(json.dumps(pending))} > /var/lib/dplaneos/update-pending.json")
        machine.succeed(f"echo '# flake of {to_version}' > /etc/nixos/flake.nix")
        return prev

    def result():
        return json.loads(machine.succeed("cat /var/lib/dplaneos/update-result.json"))

    machine.start()
    ready()
    base = machine.succeed("readlink -f /run/current-system").strip()
    machine.succeed("mkdir -p /etc/nixos && echo '# flake of 1.0.0' > /etc/nixos/flake.nix")
    machine.succeed("test \"$(cat /etc/dplaneos-variant)\" = base")

    with subtest("no update pending: the check does nothing"):
        machine.succeed("journalctl -b -u dplaneos-ota-health | grep -q 'no update waiting'")
        machine.fail("test -e /var/lib/dplaneos/update-result.json")

    with subtest("a working update is kept"):
        stage("good", "2.0.0")
        machine.shutdown()
        machine.start()
        ready()
        machine.succeed("test \"$(cat /etc/dplaneos-variant)\" = good")
        r = result()
        assert r["status"] == "ok" and r["to_version"] == "2.0.0", r
        machine.fail("test -e /var/lib/dplaneos/update-pending.json")
        machine.succeed("grep -q 'flake of 2.0.0' /etc/nixos/flake.nix")

    with subtest("a broken update goes back to the previous generation"):
        good = stage("broken", "3.0.0")
        machine.shutdown()
        machine.start()
        # broken boots, the check fails after checkSeconds and reboots the machine
        machine.wait_for_console_text("rebooting into")
        machine.wait_for_shutdown()
        machine.start()
        machine.wait_for_unit("multi-user.target")
        machine.wait_for_unit("nginx.service")
        machine.succeed("test \"$(cat /etc/dplaneos-variant)\" = good")
        assert machine.succeed("readlink -f /run/current-system").strip() == good
        r = result()
        assert r["status"] == "rolled_back" and r["to_version"] == "3.0.0", r
        assert "nginx" in r["reason"], r
        machine.fail("test -e /var/lib/dplaneos/update-pending.json")
        machine.succeed("grep -q 'flake of 2.0.0' /etc/nixos/flake.nix")
        # the restored generation stays the boot default
        machine.shutdown()
        machine.start()
        machine.wait_for_unit("multi-user.target")
        machine.succeed("test \"$(cat /etc/dplaneos-variant)\" = good")
  '';
}
