# nixos/tests/mmp-degraded.nix
# ─────────────────────────────────────────────────────────────────────────────
# What ZFS does with a multihost pool that is not whole when the system
# comes back: the facts the import service of DPlaneOS has to work with.
#
# A mirror on two loop devices, multihost=on, on two machines that differ in
# the ZFS release. Each case ends with an import and reports whether it
# worked.
#
# Run: nix build .#checks.x86_64-linux.mmp-degraded -L
# ─────────────────────────────────────────────────────────────────────────────
{ nixpkgs, system ? "x86_64-linux", ... }:

let
  pkgs = nixpkgs.legacyPackages.${system};
  machine = zfsPackage: { pkgs, lib, ... }: {
    boot.supportedFilesystems = [ "zfs" ];
    boot.zfs.forceImportRoot = false;
    boot.zfs.package = lib.mkIf (zfsPackage != null) zfsPackage;
    networking.hostId = "8425e349";
    environment.systemPackages = [ pkgs.util-linux ];
    virtualisation.memorySize = 2048;
  };
in
pkgs.testers.runNixOSTest {
  name = "dplaneos-mmp-degraded";

  nodes.m = machine null;
  nodes.old = machine (pkgs.zfs_2_3 or null);

  testScript = ''
    A, B = "/dev/loop10", "/dev/loop11"
    DEVS = "-d /dev/loop10 -d /dev/loop11"
    results = []

    def sh(m, cmd):
        rc, out = m.execute(cmd + " 2>&1")
        print(f"### [{m.name}] {cmd}\n### rc={rc}\n{out}")
        return rc, out

    def attach(m, both=True):
        m.succeed("modprobe zfs; modprobe loop; for n in 10 11; do [ -e /dev/loop$n ] || mknod -m 660 /dev/loop$n b 7 $n; done")
        m.succeed("losetup /dev/loop10 /var/pool/a.img")
        if both:
            m.succeed("losetup /dev/loop11 /var/pool/b.img")

    def imported(m):
        return m.execute("zpool list tank")[0] == 0

    def attempt(m, case, cmd):
        m.execute("dmesg -C")
        rc, out = sh(m, cmd)
        health = m.execute("zpool list -H -o health tank 2>&1")[1].strip()
        first = out.strip().splitlines()[0] if out.strip() else ""
        results.append(f"[{m.name}] {case}: rc={rc} health={health!r} :: {first}")
        print("=== " + results[-1])
        print(m.execute("dmesg | tail -n 12")[1])
        return imported(m)

    def power_loss(m):
        m.succeed("sync")
        m.crash()
        m.start()
        m.wait_for_unit("multi-user.target")

    def create(m):
        m.wait_for_unit("multi-user.target")
        m.succeed("modprobe zfs; mkdir -p /var/pool; truncate -s 512M /var/pool/a.img /var/pool/b.img")
        attach(m)
        m.succeed(f"zpool create -o multihost=on -m /tank tank mirror {A} {B}")
        m.succeed("echo data > /tank/file && sync")
        print(m.succeed("zfs version; zpool get -H multihost tank; hostid"))

    def recreate(m):
        m.execute("zpool destroy tank; losetup -d /dev/loop10; losetup -d /dev/loop11; rm -f /var/pool/*.img")
        m.succeed("truncate -s 512M /var/pool/a.img /var/pool/b.img")
        attach(m)
        m.succeed(f"zpool create -f -o multihost=on -m /tank tank mirror {A} {B}")
        m.succeed("echo data > /tank/file && sync")

    start_all()

    with subtest("The ZFS release before: offline, power loss"):
        create(old)
        old.succeed(f"zpool offline tank {B}")
        power_loss(old)
        attach(old)
        attempt(old, "offline + power loss", f"zpool import {DEVS} tank")

    with subtest("Offline, power loss: ways out"):
        create(m)
        m.succeed(f"zpool offline tank {B}")
        power_loss(m)
        attach(m)
        ways = [
            ("plain", f"zpool import {DEVS} tank"),
            ("read-only", f"zpool import {DEVS} -o readonly=on tank"),
            ("-o multihost=off", f"zpool import {DEVS} -o multihost=off tank"),
            ("more intervals", f"echo 40 > /sys/module/zfs/parameters/zfs_multihost_import_intervals; zpool import {DEVS} tank"),
            ("rewind -F", f"zpool import {DEVS} -F tank"),
            ("-f -F -X", f"zpool import {DEVS} -f -F -X tank"),
        ]
        for name, cmd in ways:
            if attempt(m, "offline + power loss, " + name, cmd):
                print(m.execute("zpool status tank; cat /tank/file")[1])
                break
        print(m.execute("grep . /sys/module/zfs/parameters/zfs_multihost_*; zdb -l /dev/loop10 | head -n 60; zdb -l /dev/loop11 | head -n 40")[1])

    with subtest("A disk that is gone (not taken offline), power loss"):
        recreate(m)
        power_loss(m)
        attach(m, both=False)
        attempt(m, "whole + power loss, one disk absent", "zpool import -d /dev/loop10 tank")

    with subtest("A disk that failed while running (faulted, not offline), power loss"):
        recreate(m)
        # The disk disappears under the pool: writes to it fail, ZFS faults it.
        m.execute("dd if=/dev/zero of=/var/pool/b.img bs=1M count=512 conv=notrunc; zpool scrub tank; sleep 8; zpool status tank")
        print(m.execute("zpool status tank")[1])
        power_loss(m)
        attach(m)
        attempt(m, "disk overwritten while running + power loss", f"zpool import {DEVS} tank")

    with subtest("Offline, orderly shutdown"):
        recreate(m)
        m.succeed(f"zpool offline tank {B}")
        m.shutdown()
        m.start()
        m.wait_for_unit("multi-user.target")
        attach(m)
        attempt(m, "offline + orderly shutdown", f"zpool import {DEVS} tank")

    with subtest("Offline for a moment, online again, power loss"):
        recreate(m)
        m.succeed(f"zpool offline tank {B}; zpool online tank {B}")
        m.wait_until_succeeds("zpool list -H -o health tank | grep -qx ONLINE", timeout=120)
        power_loss(m)
        attach(m)
        attempt(m, "offline then online + power loss", f"zpool import {DEVS} tank")

    print("\n=========== RESULTS ===========")
    for line in results:
        print("RESULT " + line)
  '';
}
