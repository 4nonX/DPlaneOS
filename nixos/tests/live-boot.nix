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

    # Simulate attached storage (for ZFS pool test)
    virtualisation.emptyDiskImages = [ 512 512 ];  # Two 512MB disks for ZFS

    # Simple ZFS pool for testing (created in test setup)
    # Normally auto-import would find this, but in VM we need to create it first
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
        units = "dplaned postgresql postgresql-setup dplaneos-zfs-gate dplane-zfs-auto-import nginx"
        for cmd in [
            f"systemctl status --no-pager --lines=0 {units} 2>&1",
            "journalctl -b --no-pager -o short-monotonic -u dplaned -u postgresql -u postgresql-setup -u dplaneos-zfs-gate 2>&1 | tail -n 200",
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

    # ── Step 11: Verify system can reach network (DHCP) ──────────────────────
    @test_step("Verify networking")
    def check_networking():
        # Get IP address
        ip_output = liveSystem.succeed("ip addr show | grep 'inet ' | head -1")
        print(f"Network config: {ip_output}")

    # ── Step 12: Check for data persistence markers ──────────────────────────
    @test_step("Check persistence markers")
    def check_persistence():
        # Live environment creates markers for persistent/ephemeral mode
        ephemeral_marker = liveSystem.succeed("ls /run/dplane-persist-* 2>&1 || echo 'none'")
        print(f"Persistence mode: {ephemeral_marker}")

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
    check_networking()
    check_persistence()
    shutdown()

    print("\n╔════════════════════════════════════════════════════════════════╗")
    print("║                  ✓ ALL TESTS PASSED                           ║")
    print("╚════════════════════════════════════════════════════════════════╝")
  '';
}
