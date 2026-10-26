# D-PlaneOS Live Boot Integration Test
# ─────────────────────────────────────────────────────────────────────────────
# VM-based test that validates live boot functionality:
#   1. System boots from live ISO
#   2. D-PlaneOS daemon starts and UI is accessible
#   3. ZFS pools are auto-discovered and imported
#   4. Docker engine is operational
#   5. Root filesystem is ephemeral (tmpfs)
#   6. A drive labelled dplane-persist keeps the database, the settings and
#      the network files across reboots
#   7. System gracefully handles shutdown
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
  # migrations). Shellchecked at build time.
  smokeTest = pkgs.writeShellApplication {
    name = "dplaneos-smoke";
    runtimeInputs = with pkgs; [ curl jq gawk avahi iproute2 util-linux ];
    text = builtins.readFile ./live-boot-smoke.sh;
  };

  # A virtio disk with a serial, so udev creates /dev/disk/by-id/virtio-<serial>.
  ciDisk = serial: { size = 512; driveConfig.deviceExtraOpts.serial = serial; };
in

pkgs.testers.nixosTest {
  name = "dplaneos-live-boot";

  nodes.liveSystem = { config, pkgs, lib, self, ... }: {
    imports = [
      ../configuration-live.nix
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
    # The fifth becomes the dplane-persist drive in the persistence step.
    virtualisation.emptyDiskImages = map ciDisk [ "dplaneci0" "dplaneci1" "dplaneci2" "dplaneci3" "dplpersist" ];
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
        headers = liveSystem.succeed("curl -sI http://localhost/").lower()
        for h in ("content-security-policy:", "x-frame-options:", "x-content-type-options:"):
            assert h in headers, f"missing {h} in: {headers}"

    # ── Step 10a: Disk events reach the daemon (hot-swap) ────────────────────
    @test_step("Disk events reach the daemon")
    def check_disk_events():
        dev = liveSystem.succeed("readlink -f /dev/disk/by-id/virtio-dplaneci3").strip()
        liveSystem.succeed(f"udevadm trigger --action=add --settle {dev}")
        liveSystem.wait_until_succeeds(f"journalctl -u dplaned --no-pager | grep -q 'DISK EVENT: added {dev} '", timeout=60)

    # ── Step 10b: End-to-end smoke test against real ZFS, Samba and Avahi ────
    @test_step("End-to-end smoke test")
    def smoke_test():
        status, output = liveSystem.execute("${smokeTest}/bin/dplaneos-smoke 2>&1")
        print(output)
        if status != 0:
            dump_diagnostics()
            raise Exception(f"smoke test failed (exit {status})")

    # ── Step 11: Verify system can reach network (DHCP) ──────────────────────
    @test_step("Verify networking")
    def check_networking():
        # Get IP address
        ip_output = liveSystem.succeed("ip addr show | grep 'inet ' | head -1")
        print(f"Network config: {ip_output}")

    # ── Step 12: The dplane-persist drive keeps the daemon's state ───────────
    def reboot_live():
        liveSystem.shutdown()
        liveSystem.start()
        try:
            liveSystem.wait_for_unit("dplaned.service")
            liveSystem.wait_until_succeeds("curl -sf http://localhost/api/system/status >/dev/null", timeout=180)
        except Exception:
            dump_diagnostics()
            print(liveSystem.execute("journalctl -b --no-pager -u dplane-link-persist -u dplane-persist-network")[1])
            raise

    def post(path, body, headers=""):
        return liveSystem.succeed(f"curl -s -o /dev/null -w '%{{http_code}}' -X POST http://localhost{path} -H 'Content-Type: application/json' {headers} -d '{body}'").strip()

    def session(password):
        import json
        sid = json.loads(liveSystem.succeed(f"curl -s -X POST http://localhost/api/auth/login -H 'Content-Type: application/json' -d '{{\"username\":\"admin\",\"password\":\"{password}\"}}'"))["session_id"]
        csrf = json.loads(liveSystem.succeed(f"curl -s http://localhost/api/csrf -H 'X-Session-ID: {sid}' -H 'X-User: admin'"))["csrf_token"]
        return f"-H 'X-Session-ID: {sid}' -H 'X-User: admin' -H 'X-CSRF-Token: {csrf}'"

    @test_step("Persistence drive keeps the daemon's state")
    def check_persistence():
        # No labelled drive at the first boot: state in RAM.
        liveSystem.succeed("test -e /run/dplane-persist-ephemeral")
        liveSystem.succeed("${pkgs.e2fsprogs}/bin/mkfs.ext4 -q -L dplane-persist /dev/disk/by-id/virtio-dplpersist")

        reboot_live()
        liveSystem.succeed("test -e /run/dplane-persist-available")
        for d in ["/var/lib/dplaneos", "/var/lib/dplaneos/pgsql", "/etc/dplaneos", "/etc/target"]:
            fs = liveSystem.succeed(f"findmnt -n -o FSTYPE --target {d}").strip()
            assert fs == "ext4", f"{d} is on {fs}, not on the drive"
        assert post("/api/system/setup-admin", '{"username":"admin","password":"Persist-Pass-1"}') == "200"
        # A network change made in the UI (a VLAN, confirmed).
        auth = session("Persist-Pass-1")
        assert post("/api/network/vlan", '{"parent":"eth0","vlan_id":42,"ip":"10.0.42.1/24"}', auth) == "200", "create a VLAN"
        assert post("/api/network/confirm", '{}', auth) == "200", "confirm the VLAN"
        liveSystem.wait_until_succeeds("ip link show eth0.42", timeout=60)
        liveSystem.wait_until_succeeds("grep -qs 'Id=42' /mnt/usb-persist/dplaneos-state/network/05-dplane-eth0.42.netdev", timeout=30)

        reboot_live()
        assert post("/api/system/setup-admin", '{"username":"admin","password":"Someone-Else-1"}') == "409", "the admin from the last boot is gone"
        assert post("/api/auth/login", '{"username":"admin","password":"Persist-Pass-1"}') == "200", "login with the password from the last boot"
        liveSystem.succeed("grep -qs 'Id=42' /etc/systemd/network/05-dplane-eth0.42.netdev")
        liveSystem.wait_until_succeeds("ip -4 addr show eth0.42 | grep -q 10.0.42.1/24", timeout=60)

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
    check_disk_events()
    smoke_test()
    check_networking()
    check_persistence()
    shutdown()

    print("\n╔════════════════════════════════════════════════════════════════╗")
    print("║                  ✓ ALL TESTS PASSED                           ║")
    print("╚════════════════════════════════════════════════════════════════╝")
  '';
}
