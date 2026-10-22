# nixos/tests/ups.nix
# ─────────────────────────────────────────────────────────────────────────────
# UPS (NUT) with the real upsd/upsmon and the dummy-ups driver, whose state
# file stands in for the UPS:
#
#   1. mains lost (OB):   upsmon notifies, the daemon raises ups.onbatt
#   2. mains back (OL):   ups.online
#   3. low battery (LB):  ups.lowbatt, and the machine shuts down, also with
#      the old "hibernate" action (hibernation is impossible with ZFS)
#
# Run: nix build .#checks.x86_64-linux.ups -L
# ─────────────────────────────────────────────────────────────────────────────
{ nixpkgs, system ? "x86_64-linux", daemonPackage, frontendPackage, ... }:

let
  pkgs = nixpkgs.legacyPackages.${system};
  state = "/var/lib/nut/dummy.dev";
in

pkgs.testers.nixosTest {
  name = "dplaneos-ups";

  nodes.machine = { ... }: {
    imports = [ ../module.nix ];
    services.dplaneos = {
      enable = true;
      inherit daemonPackage frontendPackage;
      dbPath = "/var/lib/dplaneos/pgsql";
      ups = {
        enable = true;
        driver = "dummy-ups";
        port = state;
        lowBattery = 20;
        finalDelay = 0;
        action = "hibernate"; # as an older state file may say
      };
    };
    networking.hostId = "dd0e0003";
    systemd.tmpfiles.rules = [ "d /var/lib/nut 0755 root root -" ];
    # dummy-ups reads the state before upsd starts.
    systemd.services.dplaneos-ups-dummy = {
      wantedBy = [ "multi-user.target" ];
      before = [ "upsdrv.service" "upsd.service" ];
      serviceConfig = { Type = "oneshot"; RemainAfterExit = true; };
      script = ''
        printf 'ups.status: OL\nbattery.charge: 100\nups.mfr: Test\nups.model: Dummy\n' > ${state}
        chmod 644 ${state}
      '';
    };
    virtualisation.memorySize = 2048;
  };

  testScript = ''
    def set_status(status, charge):
        machine.succeed(f"printf 'ups.status: {status}\\nbattery.charge: {charge}\\nups.mfr: Test\\nups.model: Dummy\\n' > ${state}.new && mv ${state}.new ${state}")

    machine.start()
    machine.wait_for_unit("dplaned.service")
    machine.wait_for_unit("upsmon.service")
    machine.wait_until_succeeds("upsc ups ups.status | grep -qx OL", timeout=120)
    machine.wait_until_succeeds("test -S /run/dplaneos/ups-events.sock", timeout=300)
    machine.succeed("stat -c %a /run/dplaneos/ups-events.sock | grep -qx 660")

    with subtest("mains lost: on battery alert"):
        set_status("OB", 80)
        machine.wait_until_succeeds("journalctl -u dplaned.service | grep -q 'UPS event: ONBATT on ups'", timeout=120)

    with subtest("mains back: online alert"):
        set_status("OL", 80)
        machine.wait_until_succeeds("journalctl -u dplaned.service | grep -q 'UPS event: ONLINE on ups'", timeout=120)

    with subtest("low battery: alert and shutdown (not hibernate)"):
        set_status("OB LB", 10)
        machine.wait_for_console_text("UPS event: LOWBATT on ups", timeout=120)
        machine.wait_for_shutdown()
  '';
}
