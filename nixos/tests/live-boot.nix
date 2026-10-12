# D-PlaneOS Live Boot Integration Test
# ─────────────────────────────────────────────────────────────────────────────
# VM-based test that validates live boot functionality:
#   1. System boots from live ISO
#   2. D-PlaneOS daemon starts and UI is accessible
#   3. ZFS pools are auto-discovered and imported
#   4. Docker engine is operational
#   5. Root filesystem is ephemeral (tmpfs)
#   6. System gracefully handles shutdown
#
# Run locally:
#   nix build .#checks.x86_64-linux.live-boot -L
#
# Expected runtime: ~2-3 minutes per test, 120 second timeout

{ nixpkgs, system ? "x86_64-linux", impermanence, daemonPackage ? null, frontendPackage ? null, ... }:

let
  pkgs = nixpkgs.legacyPackages.${system};

  # End-to-end API smoke test (pool, vdev, dataset preset, snapshots and
  # rollback, export/import, SMB shares with testparm, Time Machine Bonjour,
  # feature flags, migrations). Shellchecked at build time.
  smokeTest = pkgs.writeShellApplication {
    name = "dplaneos-smoke";
    runtimeInputs = with pkgs; [ curl jq gawk avahi iproute2 util-linux ];
    text = builtins.readFile ./live-boot-smoke.sh;
  };

  hotplugImage = "/tmp/dplaneos-hotplug-disk.img";
  sataImage = "/tmp/dplaneos-sata-disk.img";

  # What the daemon must be able to do as a service: run with the capability
  # set of dplaned.service. Each line is something a feature needs (file
  # manager and backups in folders owned by users, disk power management,
  # reservation probes, the floating address announcement).
  capabilityProbes = pkgs.writeShellScript "dplaneos-capability-probes" ''
    sata=$1
    failed=0
    probe() { # name command...  (must succeed)
      local name=$1 out status
      shift
      out=$("$@" 2>&1)
      status=$?
      if [ "$status" = 0 ]; then echo "PROBE [ok] $name"; else echo "PROBE [FAIL $status] $name"; failed=1; fi
      echo "$out" | head -n 12 | sed 's/^/      /'
    }
    permitted() { # name command...  (may fail, but not for lack of privilege)
      local name=$1 out
      shift
      out=$("$@" 2>&1)
      if echo "$out" | grep -qi "not permitted\|permission denied"; then
        echo "PROBE [FAIL] $name"; failed=1
      else
        echo "PROBE [ok] $name"
      fi
      echo "$out" | head -n 6 | sed 's/^/      /'
    }
    probe "SMART on a SATA disk (ATA pass-through)" smartctl -H -i "$sata"
    probe "disk power state (hdparm)" hdparm -C "$sata"
    probe "create a file in a folder owned by a user (mode 755)" touch /mnt/captest/userdir/new
    probe "create a folder in a folder owned by a user" mkdir /mnt/captest/userdir/newdir
    probe "delete a file in a folder owned by a user" rm /mnt/captest/src/sub/f
    probe "rsync -a of a tree owned by a user" rsync -a /mnt/captest/src/ /mnt/captest/dst/
    probe "the copy keeps the owner" test "$(stat -c %u /mnt/captest/dst/sub)" = 1000
    probe "gratuitous ARP (arping)" arping -U -c 1 -I eth0 "$(ip -4 -o addr show dev eth0 | awk '{print $4}' | cut -d/ -f1 | head -n1)"
    probe "signal a process of another user" kill -0 "$(pgrep -u postgres | head -n1)"
    probe "run a command as another user" runuser -u nobody -- id
    probe "nft list tables" nft list tables
    probe "dmidecode" dmidecode -t system
    permitted "SCSI persistent reservation query" sg_persist --in -k "$sata"
    permitted "load a kernel module" modprobe dummy
    exit $failed
  '';

  # A virtio disk with a serial, so udev creates /dev/disk/by-id/virtio-<serial>.
  ciDisk = serial: { size = 512; driveConfig.deviceExtraOpts.serial = serial; };
in

pkgs.testers.nixosTest {
  name = "dplaneos-live-boot";

  nodes.liveSystem = { config, pkgs, lib, self, ... }: {
    imports = [
      ../configuration-live.nix
      ../live-setup-code.nix
      impermanence.nixosModules.impermanence
      ../module.nix
    ];

    # Provide daemon and frontend packages (normally from applianceConfig in flake.nix)
    services.dplaneos = {
      enable = true;
      daemonPackage = daemonPackage;
      frontendPackage = frontendPackage;
      dbPath = "/var/lib/dplaneos/pgsql";
    };

    # Test environment: disable serial console noise
    boot.kernelParams = [ ];

    # For testing: ensure deterministic network config
    networking.useDHCP = true;

    # Reduce boot time in test environment
    systemd.services.systemd-random-seed.enable = false;

    # VM resources
    virtualisation.cores = 4;
    virtualisation.memorySize = 2048;  # 2GB for ZFS ARC + daemon

    # Boot with a tmpfs root like the live ISO (live-persistence.nix). The test
    # framework otherwise replaces fileSystems."/" with a persistent ext4 disk
    # image, and the ephemeral-root check would test the harness, not live boot.
    virtualisation.diskImage = null;

    # Four 512 MiB data disks with serials (two mirrors in the smoke test).
    # Pool creation only accepts /dev/disk/by-id paths, which need a serial.
    virtualisation.emptyDiskImages = map ciDisk [ "dplaneci0" "dplaneci1" "dplaneci2" "dplaneci3" ];

    # A SCSI controller to hot-plug a disk into (works on every machine
    # type), and one SATA disk: QEMU's emulated SATA disk answers SMART, so
    # the SMART path through ATA pass-through is tested.
    virtualisation.qemu.options = [
      "-device virtio-scsi-pci,id=hotscsi"
      "-drive file=${sataImage},if=none,id=sata0,format=raw,cache=unsafe"
      "-device ahci,id=ahci0"
      "-device ide-hd,drive=sata0,bus=ahci0.0,serial=dplanesata"
    ];
  };

  testScript = ''
    import time

    def test_step(name):
        """Decorator for test steps"""
        def decorator(func):
            def wrapper(*args, **kwargs):
                print(f"\n[TEST] {name}")
                start = time.time()
                try:
                    result = func(*args, **kwargs)
                    elapsed = time.time() - start
                    print(f"✓ {name} ({elapsed:.1f}s)")
                    return result
                except Exception as e:
                    print(f"✗ {name} FAILED: {e}")
                    raise
            return wrapper
        return decorator

    # ── Step 1: System boot ──────────────────────────────────────────────────
    def dump_diagnostics():
        """Print why dplaned did not come up. The serial console only carries
        kernel messages, so without this the CI log has no daemon output."""
        units = "dplaned postgresql postgresql-setup dplaneos-zfs-gate dplane-zfs-auto-import nginx samba-smbd avahi-daemon zfs-zed"
        for cmd in [
            f"systemctl status --no-pager --lines=0 {units} 2>&1",
            "journalctl -b --no-pager -o short-monotonic -u dplaned -u postgresql -u postgresql-setup -u dplaneos-zfs-gate -u samba-smbd -u avahi-daemon -u zfs-zed -u 'dplaneos-snap-*' -t dplaneos-zed 2>&1 | tail -n 200",
            "ls -la /run/postgresql /run/dplaneos 2>&1",
        ]:
            print(f"\n----- {cmd}")
            print(liveSystem.execute(cmd)[1])

    @test_step("Boot live system")
    def boot():
        for image in ("${hotplugImage}", "${sataImage}"):
            with open(image, "wb") as f:
                f.truncate(256 * 1024 * 1024)
        liveSystem.start()
        liveSystem.wait_for_unit("multi-user.target")
        try:
            liveSystem.wait_for_unit("dplaned.service")
        except Exception:
            dump_diagnostics()
            raise
        time.sleep(2)  # Allow daemon to fully initialize

    # ── Step 2: Verify daemon is running ─────────────────────────────────────
    @test_step("Verify daemon process")
    def check_daemon():
        liveSystem.succeed("systemctl is-active dplaned.service")
        output = liveSystem.succeed("ps aux | grep -E 'dplaned|dplane'")
        print(f"Daemon processes: {output}")

    # ── Step 3: Verify UI is accessible ──────────────────────────────────────
    # nginx (module.nix) serves the UI on port 80 and proxies /api to the daemon socket.
    @test_step("Verify UI on port 80")
    def check_ui():
        try:
            liveSystem.wait_for_open_port(80, timeout=120)
        except Exception:
            dump_diagnostics()
            raise
        response = liveSystem.succeed("curl -sf http://localhost/ 2>&1 | head -20")
        assert "<!DOCTYPE" in response or "html" in response.lower(), "UI HTML not found"

    # ── Step 4: Check ZFS module is loaded ───────────────────────────────────
    @test_step("Verify ZFS kernel module")
    def check_zfs_module():
        liveSystem.succeed("lsmod | grep zfs")
        liveSystem.succeed("${pkgs.zfs}/bin/zfs version")

    # ── Step 5: Check ZFS auto-import service status ────────────────────────
    @test_step("Verify ZFS auto-import service")
    def check_zfs_import_service():
        liveSystem.wait_for_unit("dplane-zfs-auto-import.service")
        liveSystem.succeed("systemctl is-active dplane-zfs-auto-import.service")

    # ── Step 6: Verify root is ephemeral (tmpfs) ─────────────────────────────
    @test_step("Verify ephemeral root filesystem")
    def check_ephemeral_root():
        # Root is tmpfs and /var is a directory on it, not a separate mount,
        # so ask which filesystem each path resolves to.
        root_fs = liveSystem.succeed("findmnt -n -o FSTYPE --target /").strip()
        var_fs = liveSystem.succeed("findmnt -n -o FSTYPE --target /var").strip()
        print(f"Filesystem of /: {root_fs}, of /var: {var_fs}")
        assert root_fs == "tmpfs", f"Expected / to be tmpfs, got: {root_fs}"
        assert var_fs == "tmpfs", f"Expected /var to be on tmpfs, got: {var_fs}"

    # ── Step 7: Check machine-id (ephemeral but preserved) ────────────────────
    @test_step("Verify machine identity")
    def check_machine_id():
        machine_id = liveSystem.succeed("cat /etc/machine-id")
        assert len(machine_id.strip()) > 0, "machine-id should not be empty"

    # ── Step 8: Verify Docker is available ───────────────────────────────────
    @test_step("Verify Docker engine")
    def check_docker():
        # Docker service should be available in live environment
        output = liveSystem.succeed("docker --version 2>&1")
        assert "Docker" in output or "docker" in output.lower(), f"Docker not found: {output}"

    # ── Step 9: Check daemon logs for errors ─────────────────────────────────
    @test_step("Check daemon logs")
    def check_daemon_logs():
        logs = liveSystem.succeed("journalctl -u dplaned.service -n 30")
        print(f"Recent daemon logs:\n{logs}")

        # Check for critical errors (warnings are OK)
        assert "FATAL" not in logs, "Fatal errors found in daemon logs"
        assert "panic" not in logs.lower(), "Panic found in daemon logs"

    # ── Step 10: API health check ────────────────────────────────────────────
    @test_step("Check daemon API")
    def check_api_health():
        # Try to access a known API endpoint (if it exists)
        # For MVP, just verify HTTP connectivity
        response = liveSystem.succeed("curl -s -o /dev/null -w '%{http_code}' http://localhost/")
        http_code = response.strip()
        assert http_code.startswith("2") or http_code.startswith("3"), \
            f"Expected 2xx/3xx response, got {http_code}"

    # ── Step 10b: End-to-end smoke test against real ZFS, Samba and Avahi ────
    @test_step("End-to-end smoke test")
    def smoke_test():
        status, output = liveSystem.execute("${smokeTest}/bin/dplaneos-smoke 2>&1")
        print(output)
        if status != 0:
            dump_diagnostics()
            raise Exception(f"smoke test failed (exit {status})")

    # ── Step 10c: Disk hot-plug ──────────────────────────────────────────────
    HOT = "/dev/disk/by-id/scsi-0QEMU_QEMU_HARDDISK_dplanehot"

    def registry(where):
        """Number of disk registry rows of the hot-plug disk matching where."""
        sql = f"SELECT count(*) FROM disk_registry WHERE by_id_path LIKE '%dplanehot' AND {where}"
        return f'test "$(runuser -u postgres -- psql -d dplaneos -tAc "{sql}")" = 1'

    def monitor(cmd):
        out = liveSystem.send_monitor_command(cmd)
        assert "rror" not in out, f"QEMU monitor: {cmd}: {out}"

    def plug():
        monitor("drive_add 0 file=${hotplugImage},if=none,id=hot,format=raw,cache=unsafe")
        monitor("device_add scsi-hd,drive=hot,bus=hotscsi.0,serial=dplanehot,id=hotdev")

    def unplug():
        monitor("device_del hotdev")
        liveSystem.wait_until_fails(f"test -e {HOT}", timeout=60)

    def journal_count(text):
        return int(liveSystem.succeed(f"journalctl -u dplaned --no-pager | grep -c '{text}' || true").strip())

    @test_step("Disk hot-plug: the daemon notices, and a known pool returns with its disk")
    def hotplug():
        try:
            plug()
            liveSystem.wait_until_succeeds(f"test -b {HOT}", timeout=60)
            liveSystem.wait_until_succeeds("journalctl -u dplaned --no-pager | grep -q 'DISK EVENT: added /dev/sd'", timeout=60)
            liveSystem.wait_until_succeeds(registry("removed_at IS NULL"), timeout=60)

            # A pool on the new disk: the registry learns which pool the disk is in.
            liveSystem.succeed(f"zpool create -f -m /mnt/hotpool hotpool {HOT} && echo kept > /mnt/hotpool/file")
            liveSystem.wait_until_succeeds(registry("pool_name = 'hotpool'"), timeout=180)

            # Unplugged cleanly and plugged back in: the pool is imported again.
            liveSystem.succeed("zpool export hotpool")
            removed = journal_count("DISK EVENT: removed /dev/sd")
            unplug()
            liveSystem.wait_until_succeeds(
                f"test $(journalctl -u dplaned --no-pager | grep -c 'DISK EVENT: removed /dev/sd') -gt {removed}", timeout=60)
            liveSystem.wait_until_succeeds(registry("removed_at IS NOT NULL AND pool_name = 'hotpool'"), timeout=60)
            liveSystem.fail("zpool list -H -o name | grep -qx hotpool")
            plug()
            liveSystem.wait_until_succeeds("zpool list -H -o name | grep -qx hotpool", timeout=120)
            liveSystem.succeed("grep -qx kept /mnt/hotpool/file")
            liveSystem.succeed("zpool destroy hotpool")
            unplug()
        except Exception:
            print(liveSystem.execute("journalctl -u dplaned --no-pager | grep -E 'DISK|REGISTRY' | tail -n 60; zpool import; zpool list; ls -l /dev/disk/by-id; "
                                     "runuser -u postgres -- psql -d dplaneos -c 'SELECT dev_name, by_id_path, pool_name, removed_at FROM disk_registry'")[1])
            raise

    # ── Step 10d: What the daemon's service context can do ───────────────────
    @test_step("The daemon's service may do what its features need")
    def capability_probes():
        show = "systemctl show dplaned --value -p "
        caps = liveSystem.succeed(show + "CapabilityBoundingSet").strip()
        ambient = liveSystem.succeed(show + "AmbientCapabilities").strip()
        sata = liveSystem.succeed("readlink -f /dev/disk/by-id/ata-QEMU_HARDDISK_dplanesata").strip()
        liveSystem.succeed("mkdir -p /mnt/captest/userdir /mnt/captest/src/sub && echo x > /mnt/captest/src/sub/f "
                           "&& echo y > /mnt/captest/src/sub/g "
                           "&& chown -R 1000:1000 /mnt/captest/userdir /mnt/captest/src && chmod -R go-w /mnt/captest")
        status, out = liveSystem.execute(
            "systemd-run --wait --pipe --quiet --collect -p NoNewPrivileges=yes "
            f"-p CapabilityBoundingSet='{caps}' -p AmbientCapabilities='{ambient}' "
            f"-E PATH=\"$(systemctl show dplaned -p Environment --value | tr ' ' '\\n' | sed -n 's/^PATH=//p'):/run/current-system/sw/bin\" "
            f"${capabilityProbes} {sata} 2>&1")
        print(out)
        assert status == 0, "the daemon's service lacks a privilege one of its features needs (see PROBE lines)"

    # ── Step 11: Verify system can reach network (DHCP) ──────────────────────
    @test_step("Verify networking")
    def check_networking():
        # Get IP address
        ip_output = liveSystem.succeed("ip addr show | grep 'inet ' | head -1")
        print(f"Network config: {ip_output}")

    # ── Step 12: The state of the live system is in memory ───────────────────
    @test_step("State is in memory")
    def check_persistence():
        assert liveSystem.succeed("findmnt -n -o FSTYPE /persist").strip() == "tmpfs"
        assert liveSystem.succeed("cat /etc/dplaneos-live/mode").strip() == "live-boot"

    # ── Step 13: Shutdown and verify clean exit ──────────────────────────────
    @test_step("Clean shutdown")
    def shutdown():
        liveSystem.shutdown()
        time.sleep(1)

    # ── Execute all tests ────────────────────────────────────────────────────
    print("\n╔════════════════════════════════════════════════════════════════╗")
    print("║         D-PlaneOS Live Boot Integration Test Suite            ║")
    print("╚════════════════════════════════════════════════════════════════╝")

    boot()
    check_daemon()
    check_ui()
    check_zfs_module()
    check_zfs_import_service()
    check_ephemeral_root()
    check_machine_id()
    check_docker()
    check_daemon_logs()
    check_api_health()
    smoke_test()
    hotplug()
    capability_probes()
    check_networking()
    check_persistence()
    shutdown()

    print("\n╔════════════════════════════════════════════════════════════════╗")
    print("║                  ✓ ALL TESTS PASSED                           ║")
    print("╚════════════════════════════════════════════════════════════════╝")
  '';
}
