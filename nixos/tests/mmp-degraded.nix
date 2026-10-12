# nixos/tests/mmp-degraded.nix
# ─────────────────────────────────────────────────────────────────────────────
# What ZFS does with a multihost pool that is not whole when the system
# comes back: the facts the import service of DPlaneOS has to work with.
#
# One machine, a mirror on two loop devices, multihost=on. Each case ends
# with an import and reports whether it worked and how long it took:
#
#   A  disk taken offline, pool exported, imported again
#   B  restart without export (power loss), pool whole
#   C  disk taken offline, power loss, both disks present
#   D  disk taken offline, power loss, the offline disk not present
#   E  pool whole, power loss, one disk gone
#   F  disk taken offline, orderly shutdown, both disks present
#
# Run: nix build .#checks.x86_64-linux.mmp-degraded -L
# ─────────────────────────────────────────────────────────────────────────────
{ nixpkgs, system ? "x86_64-linux", ... }:

let
  pkgs = nixpkgs.legacyPackages.${system};
in
pkgs.testers.runNixOSTest {
  name = "dplaneos-mmp-degraded";

  nodes.m = { pkgs, ... }: {
    boot.supportedFilesystems = [ "zfs" ];
    boot.zfs.forceImportRoot = false;
    networking.hostId = "8425e349";
    environment.systemPackages = [ pkgs.util-linux ];
    virtualisation.memorySize = 2048;
  };

  testScript = ''
    A, B = "/dev/loop10", "/dev/loop11"
    results = {}

    def sh(cmd):
        rc, out = m.execute(cmd + " 2>&1")
        print(f"### {cmd}\n### rc={rc}\n{out}")
        return rc, out

    def attach(both=True):
        m.succeed("modprobe zfs; modprobe loop; for n in 10 11; do [ -e /dev/loop$n ] || mknod -m 660 /dev/loop$n b 7 $n; done")
        m.succeed("losetup /dev/loop10 /var/pool/a.img")
        if both:
            m.succeed("losetup /dev/loop11 /var/pool/b.img")

    def try_import(case, extra=""):
        rc, out = sh(f"time zpool import -d /dev/loop10 -d /dev/loop11 {extra} tank")
        health = m.execute("zpool list -H -o health tank 2>&1")[1].strip()
        kernel = m.execute("dmesg | grep -i -E 'zfs|mmp|pool|activity' | tail -n 6")[1]
        results[case] = f"rc={rc} health={health!r} :: {out.strip().splitlines()[0] if out.strip() else ' '}"
        print(f"=== {case}: {results[case]}\n{kernel}")
        return rc

    def whole():
        sh("zpool online tank /dev/loop11")
        m.wait_until_succeeds("zpool list -H -o health tank | grep -qx ONLINE", timeout=120)
        m.succeed("echo data > /tank/file && sync")

    def power_loss():
        m.succeed("sync")
        m.crash()
        m.start()
        m.wait_for_unit("multi-user.target")

    start_all()
    m.wait_for_unit("multi-user.target")
    m.succeed("modprobe zfs; mkdir -p /var/pool; truncate -s 512M /var/pool/a.img /var/pool/b.img")
    attach()
    m.succeed(f"zpool create -o multihost=on -m /tank tank mirror {A} {B}")
    m.succeed("echo data > /tank/file && sync")
    print(m.succeed("zfs version; zpool get -H multihost tank; hostid"))

    with subtest("A: offline, exported, imported"):
        m.succeed(f"zpool offline tank {B}")
        m.succeed("zpool export tank")
        try_import("A offline + export")
        whole()

    with subtest("B: power loss, pool whole"):
        power_loss()
        attach()
        try_import("B power loss, whole")
        whole()

    with subtest("C: offline, power loss, both disks present"):
        m.succeed(f"zpool offline tank {B}")
        power_loss()
        attach()
        if try_import("C offline + power loss, both present") != 0:
            try_import("C2 again, with -f", "-f")
            if m.execute("zpool list tank")[0] != 0:
                m.succeed("losetup -d /dev/loop11")
                try_import("C3 again, offline disk taken away")
                if m.execute("zpool list tank")[0] != 0:
                    try_import("C4 offline disk taken away, with -f", "-f")
                m.execute("losetup /dev/loop11 /var/pool/b.img")
        whole()

    with subtest("D: offline, power loss, the offline disk not present"):
        m.succeed(f"zpool offline tank {B}")
        power_loss()
        attach(both=False)
        try_import("D offline + power loss, offline disk absent")
        m.execute("losetup /dev/loop11 /var/pool/b.img")
        if m.execute("zpool list tank")[0] != 0:
            try_import("D2 both present again")
        whole()

    with subtest("E: whole, power loss, one disk gone"):
        power_loss()
        attach(both=False)
        try_import("E whole + power loss, one disk absent")
        m.execute("losetup /dev/loop11 /var/pool/b.img")
        if m.execute("zpool list tank")[0] == 0:
            whole()
        else:
            try_import("E2 both present again")
            whole()

    with subtest("F: offline, orderly shutdown, both disks present"):
        m.succeed(f"zpool offline tank {B}")
        m.shutdown()
        m.start()
        m.wait_for_unit("multi-user.target")
        attach()
        try_import("F offline + shutdown, both present")

    print("\n=========== RESULTS ===========")
    for case, result in results.items():
        print(f"RESULT {case}: {result}")
  '';
}
