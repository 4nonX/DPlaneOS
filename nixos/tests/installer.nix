# nixos/tests/installer.nix
# ─────────────────────────────────────────────────────────────────────────────
# The installer, end to end:
#
#   1. a UEFI machine with the installer's tools (installer-tools.nix, as on
#      the ISO) and the DPlaneOS system in its store runs install.sh
#      unattended onto an empty 40 GiB disk
#   2. the installed disk is checked: layout, system flake guard, the admin
#      password on /persist
#   3. the disk boots as a new UEFI machine (no installer, no test backdoor)
#      and is checked from outside like a user would: the web interface
#      answers, the admin logs in with the installer's password, and the
#      unauthenticated setup step cannot change it; a pool created through
#      the API on a second disk is there again after a reboot; its journal
#      (read from the installer afterwards) shows no failed unit, and the
#      machine has its own ZFS host ID
#   4. the boot import: it leaves a pool listed as a shared storage group's
#      alone; a pool last used without a host ID (16.0.0 and older) is
#      imported only when this machine's hostname used it
#   5. a disk smaller than the layout needs and a weak password are refused
#
# Run: nix build .#checks.x86_64-linux.installer -L
# ─────────────────────────────────────────────────────────────────────────────
{ nixpkgs, system ? "x86_64-linux", targetSystem, diskoScripts, ... }:

let
  pkgs = nixpkgs.legacyPackages.${system};
  ovmf = pkgs.OVMF.fd;
  qemu = "${pkgs.qemu_test}/bin/qemu-kvm";
  # The ISO registers its store contents; a test VM booted from its disk
  # image does not register additionalPaths, so the test loads them.
  regInfo = pkgs.closureInfo { rootPaths = [ targetSystem ]; };
in

pkgs.testers.nixosTest {
  name = "dplaneos-installer";

  nodes.installer = { lib, ... }: {
    imports = [ ../installer-tools.nix ];
    _module.args = { inherit targetSystem diskoScripts; };
    virtualisation.useBootLoader = true;
    virtualisation.useEFIBoot = true;
    boot.loader.systemd-boot.enable = true;
    boot.loader.efi.canTouchEfiVariables = true;
    # ZFS with host ID 0, as DPlaneOS up to 16.0.0: plays such a machine
    # that last used the pool (the host ID transition below).
    boot.supportedFilesystems = [ "vfat" "ext4" "zfs" ];
    networking.hostId = "00000000";
    boot.zfs.forceImportRoot = false;
    # The system to install, in the installer's store and Nix database (as
    # in the ISO's squashfs).
    virtualisation.additionalPaths = [ targetSystem regInfo ];
    # The disk image of a VM booted through its boot loader does not hold
    # additionalPaths: use the host's store (as the ISO uses its squashfs).
    virtualisation.mountHostNixStore = true;
    virtualisation.diskSize = 24 * 1024;
    virtualisation.emptyDiskImages = [ (40 * 1024) (16 * 1024) ];
    virtualisation.memorySize = 4096;
    virtualisation.cores = 2;
    nix.settings.substituters = lib.mkForce [ ];
  };

  testScript = ''
    import json, shutil, time, urllib.request, urllib.error

    PASSWORD = "Install-Pass-1!"

    installer.start()
    installer.wait_for_unit("multi-user.target")
    installer.succeed("nix-store --load-db < ${regInfo}/registration")
    disks = installer.succeed("lsblk -bn -o NAME,SIZE,PARTLABEL")
    installer.log(disks)

    with subtest("refused: a disk below the layout's minimum, a weak password"):
        installer.fail(f"DPLANEOS_INSTALL_DISK=/dev/vdc DPLANEOS_ADMIN_PASSWORD='{PASSWORD}' bash /etc/dplaneos-install/install.sh")
        installer.fail("DPLANEOS_INSTALL_DISK=/dev/vdb DPLANEOS_ADMIN_PASSWORD=weak bash /etc/dplaneos-install/install.sh")
        installer.succeed("test -z \"$(lsblk -n -o NAME /dev/vdb | tail -n +2)\"")  # nothing partitioned

    with subtest("install unattended onto /dev/vdb"):
        installer.succeed(f"DPLANEOS_INSTALL_DISK=/dev/vdb DPLANEOS_ADMIN_PASSWORD='{PASSWORD}' bash /etc/dplaneos-install/install.sh >&2", timeout=3600)

    with subtest("the installed disk"):
        parts = installer.succeed("lsblk -bn -o PARTLABEL,SIZE /dev/vdb")
        installer.log(parts)
        assert "dplaneos-system" in parts and "dplaneos-persist" in parts and "dplaneos-esp" in parts and "system-b" not in parts, parts
        installer.succeed("grep -q 'throw' /mnt/etc/nixos/configuration.nix")
        installer.succeed("test -s /mnt/etc/nixos/hardware-configuration.nix")
        installer.succeed("grep -q '^\\$2' /mnt/persist/var/lib/dplaneos/.first-boot-password")
        installer.succeed("test \"$(stat -c %a /mnt/persist/var/lib/dplaneos/.first-boot-password)\" = 600")
        installer.succeed("test -e /mnt/boot/EFI/BOOT/BOOTX64.EFI")
        installer.succeed("sync; umount -R /mnt")

    installer.shutdown()

    with subtest("the installed system boots and the admin logs in"):
        disk = installer.state_dir / "empty0.qcow2"
        pool_disk = installer.state_dir / "empty1.qcow2"  # never partitioned: the refused install above
        vars_fd = installer.state_dir / "installed-vars.fd"
        shutil.copy("${ovmf}/FV/OVMF_VARS.fd", vars_fd)
        vars_fd.chmod(0o644)
        target = create_machine(
            "${qemu} -machine q35,accel=kvm:tcg -cpu max -m 3072 -smp 2"
            " -drive if=pflash,format=raw,unit=0,readonly=on,file=${ovmf}/FV/OVMF_CODE.fd"
            f" -drive if=pflash,format=raw,unit=1,file={vars_fd}"
            f" -drive file={disk},if=virtio,format=qcow2"
            f" -drive file={pool_disk},if=none,id=pool,format=qcow2 -device virtio-blk-pci,drive=pool,serial=dplpool"
            " -netdev user,id=n0,hostfwd=tcp:127.0.0.1:18080-:80 -device virtio-net-pci,netdev=n0",
            name="installed",
        )
        def boot_installed():
            target.start()
            # The driver's serial connection counts as input at the systemd-boot
            # menu, which then stops its countdown: choose the default entry.
            target.wait_for_console_text("Boot in", timeout=600)
            target.send_key("ret")
            deadline = time.time() + 2400 # software emulation when the host has no nested KVM
            while True:
                try:
                    code, _ = http("GET", "/api/system/status")
                    if code == 200:
                        return
                except Exception:
                    pass
                if time.time() > deadline:
                    target.screenshot("installed-boot") # emergency mode etc. shows on the screen only
                    raise AssertionError("the installed system's web interface did not come up (screenshot: installed-boot.png)")
                time.sleep(5)

        def http(method, path, body=None, headers=None):
            req = urllib.request.Request("http://127.0.0.1:18080" + path, method=method,
                                         data=None if body is None else json.dumps(body).encode(),
                                         headers={"Content-Type": "application/json", **(headers or {})})
            try:
                with urllib.request.urlopen(req, timeout=120) as r:
                    return r.status, json.loads(r.read() or b"{}")
            except urllib.error.HTTPError as e:
                try:
                    return e.code, json.loads(e.read() or b"{}")
                except Exception:
                    return e.code, {}

        def login():
            code, r = http("POST", "/api/auth/login", {"username": "admin", "password": PASSWORD})
            assert r.get("success") is True and r.get("session_id"), f"login with the installer's password: {code} {r}"
            h = {"X-Session-ID": r["session_id"], "X-User": "admin"}
            code, c = http("GET", "/api/csrf", headers=h)
            h["X-CSRF-Token"] = c.get("csrf_token", "")
            return h

        def pools(h):
            code, r = http("GET", "/api/zfs/pools", headers=h)
            assert code == 200, (code, r)
            return {p.get("name"): p for p in (r.get("pools") or r.get("data") or [])}

        boot_installed()

        code, r = http("POST", "/api/system/setup-admin", {"username": "admin", "password": "Taken-Over-1!"})
        assert r.get("success") is not True, f"the setup step changed the installer's password: {code} {r}"
        h = login()
        code, r = http("GET", "/api/system/updates/nixos", headers=h)
        assert code == 200 and r.get("nixos") is True and r.get("live") is False, r

    with subtest("a pool is imported again after a reboot"):
        code, r = http("POST", "/api/system/pool/create", {"name": "tank", "topology": {"data": [{"type": "stripe", "disks": ["/dev/disk/by-id/virtio-dplpool"]}]}}, headers=h)
        assert code == 200 and r.get("status") == "created", f"pool create: {code} {r}"
        assert "tank" in pools(h), pools(h)
        # ACPI power button: a clean shutdown (pools are not exported by it)
        target.send_monitor_command("system_powerdown")
        target.wait_for_shutdown()
        boot_installed()
        h = login()
        found = pools(h)
        assert "tank" in found, f"the pool is gone after the reboot: {found}"
        target.send_monitor_command("system_powerdown")
        target.wait_for_shutdown()

    with subtest("nothing failed during the installed system's boot"):
        # The installed system has no test backdoor: read its journal from
        # the installer. (A failed unit there went unnoticed: the network
        # configuration was hidden, /persist reported as missing, ...)
        installer.start()
        installer.wait_for_unit("multi-user.target")
        installer.succeed("mount -o ro /dev/disk/by-partlabel/dplaneos-system /mnt")
        journal = "journalctl -D /mnt/var/log/journal/* --no-pager"
        installer.log(installer.succeed(f"{journal} -p warning -o short | grep -v kernel: | tail -60"))
        failed = installer.succeed(f"{journal} -o cat | grep -E '^Failed to start |snippet .* failed|CRITICAL' || true")
        # dockerd also logs optional tools it does not find (nft, mkfs.erofs)
        failed += installer.succeed(f"{journal} -u dplaned -o cat | grep -E 'executable file not found|too many levels of symbolic links' || true")
        assert failed.strip() == "", f"the installed system's first boot:\n{failed}"
        hostname = installer.succeed(f"{journal} -o short -n 1 | awk '{{print $4}}'").strip()
        assert hostname == "dplaneos", hostname

    with subtest("the machine has its own ZFS host ID"):
        installer.succeed("mount -o ro /dev/disk/by-partlabel/dplaneos-persist /mnt/persist")
        persisted = installer.succeed("od -An -tx4 /mnt/persist/etc/hostid | tr -d ' \n'")
        active = installer.succeed("od -An -tx4 /mnt/etc/hostid | tr -d ' \n'")
        assert persisted == active and persisted not in ("", "00000000"), f"host ID: persist {persisted!r}, /etc {active!r}"

    with subtest("the boot leaves the pools of shared storage groups to their owner"):
        # tank still has its cache entry; listed as a shared group's pool
        # (as the daemon writes it), the boot must not import it.
        installer.succeed("umount -R /mnt")
        installer.succeed("mount /dev/disk/by-partlabel/dplaneos-persist /mnt")
        installer.succeed("echo tank > /mnt/var/lib/dplaneos/shared-pools && sync && umount /mnt")
        installer.shutdown()
        boot_installed()
        h = login()
        found = pools(h)
        assert "tank" not in found, f"the boot imported a shared group's pool: {found}"
        target.send_monitor_command("system_powerdown")
        target.wait_for_shutdown()

    def last_used_without_host_id(hostname):
        # The installer, with host ID 0 and the given hostname, imports tank
        # and goes down without exporting it (a machine on 16.0.0 that lost
        # power or was updated with the pool imported).
        installer.start()
        installer.wait_for_unit("multi-user.target")
        # tank is no longer a shared group's pool (the previous subtest)
        installer.succeed("mount /dev/disk/by-partlabel/dplaneos-persist /mnt")
        installer.succeed("rm -f /mnt/var/lib/dplaneos/shared-pools && sync && umount /mnt")
        installer.succeed(f"echo {hostname} > /proc/sys/kernel/hostname")
        installer.succeed("zpool import -f -N -o cachefile=none -d /dev tank && zpool sync tank")
        installer.crash()

    def import_log():
        # The installed system has no test shell: the installer reads the
        # import unit's log of its last boot from its (persistent) journal.
        installer.start()
        installer.wait_for_unit("multi-user.target")
        installer.succeed("mount -o ro /dev/disk/by-partlabel/dplaneos-system /mnt")
        log = installer.succeed("journalctl -D /mnt/var/log/journal -b -u dplaneos-zfs-import --no-pager -o cat")
        installer.succeed("umount /mnt")
        installer.shutdown()
        return log

    with subtest("a pool last used before the machine had its own host ID"):
        last_used_without_host_id("otherbox")
        boot_installed()
        found = pools(login())
        target.send_monitor_command("system_powerdown")
        target.wait_for_shutdown()
        log = import_log()
        assert "tank" not in found, f"imported a pool another machine (no host ID) used last: {found}\n{log}"
        assert "Last accessed by otherbox" in log, f"tank was not refused for the other machine's use:\n{log}"

        last_used_without_host_id("dplaneos")
        boot_installed()
        found = pools(login())
        target.send_monitor_command("system_powerdown")
        target.wait_for_shutdown()
        log = import_log()
        assert "tank" in found, f"the pool this machine used before it had its own host ID is not imported: {found}\n{log}"
  '';
}
