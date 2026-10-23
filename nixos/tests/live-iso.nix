# nixos/tests/live-iso.nix
# ─────────────────────────────────────────────────────────────────────────────
# The live ISO image boots into D-PlaneOS: the machine starts from the CD
# (UEFI), runs from the ISO's squashfs, and the daemon, the web interface and
# the database come up. live-boot.nix tests the live configuration in a test
# VM; this test boots the image that is released (until 16.0.1 that image
# was a plain NixOS installer CD, which the configuration test could not see).
#
# The image is the released one plus the test driver's backdoor
# (test-instrumentation.nix), with faster squashfs compression.
#
# Run: nix build .#checks.x86_64-linux.live-iso -L
# ─────────────────────────────────────────────────────────────────────────────
{ nixpkgs, system ? "x86_64-linux", isoSystem, ... }:

let
  pkgs = nixpkgs.legacyPackages.${system};
  lib  = pkgs.lib;

  iso = (isoSystem.extendModules {
    modules = [
      "${nixpkgs}/nixos/modules/testing/test-instrumentation.nix"
      { isoImage.squashfsCompression = "zstd -Xcompression-level 3"; }
    ];
  }).config.system.build.isoImage;

  qemu-common = import "${nixpkgs}/nixos/lib/qemu-common.nix" { inherit (pkgs) lib stdenv; };
  startCommand = lib.concatStringsSep " " [
    (qemu-common.qemuBinary pkgs.qemu_test)
    "-m 4096 -smp 2"
    "-netdev user,id=net0 -device virtio-net-pci,netdev=net0"
    "-drive if=pflash,format=raw,unit=0,readonly=on,file=${pkgs.OVMF.firmware}"
    "-drive if=pflash,format=raw,unit=1,readonly=on,file=${pkgs.OVMF.variables}"
    "-cdrom ${iso}/iso/${iso.isoName}"
  ];
in

pkgs.testers.nixosTest {
  name = "dplaneos-live-iso";
  nodes = { };

  testScript = ''
    import json

    machine = create_machine("${startCommand}")
    machine.start()

    with subtest("the ISO boots and runs from the CD"):
        machine.wait_for_unit("multi-user.target", timeout=1800)
        machine.succeed("findmnt -n -o FSTYPE /nix/.ro-store | grep -qx squashfs")
        machine.succeed("findmnt -n -o FSTYPE / | grep -qx tmpfs")
        machine.succeed("grep -q '\"mode\":\"live-boot\"' /etc/dplaneos-live/info.json")

    with subtest("D-PlaneOS comes up: database, daemon, web interface"):
        machine.wait_for_unit("postgresql.service", timeout=600)
        machine.wait_for_unit("dplaned.service", timeout=600)
        machine.wait_until_succeeds("curl -sf http://localhost/api/system/status >/dev/null", timeout=600)
        machine.succeed("curl -sf http://localhost/ | grep -qi '<title>'")
        machine.succeed("test -e /run/dplane-persist-ephemeral")

    with subtest("the admin is set up and logs in"):
        body = json.dumps({"username": "admin", "password": "Live-Iso-Pass-1"})
        code = machine.succeed(f"curl -s -o /dev/null -w '%{{http_code}}' -X POST -H 'Content-Type: application/json' -d '{body}' http://localhost/api/system/setup-admin").strip()
        assert code == "200", f"setup-admin: {code}"
        out = json.loads(machine.succeed(f"curl -s -X POST -H 'Content-Type: application/json' -d '{body}' http://localhost/api/auth/login"))
        assert out.get("session_id"), f"login: {out}"

    with subtest("no unit failed during the boot"):
        failed = machine.succeed("systemctl list-units --failed --plain --no-legend").strip()
        assert failed == "", failed
        bad = machine.succeed("journalctl -b --no-pager | grep -E 'Activation script snippet .* failed' || true").strip()
        assert bad == "", bad

    machine.shutdown()
  '';
}
