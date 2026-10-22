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
#      unauthenticated setup step cannot change it; its journal (read from
#      the installer afterwards) shows no failed unit
#   4. a disk smaller than the layout needs and a weak password are refused
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
    boot.supportedFilesystems = [ "vfat" "ext4" ];
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
        vars_fd = installer.state_dir / "installed-vars.fd"
        shutil.copy("${ovmf}/FV/OVMF_VARS.fd", vars_fd)
        vars_fd.chmod(0o644)
        target = create_machine(
            "${qemu} -machine q35,accel=kvm:tcg -cpu max -m 3072 -smp 2"
            " -drive if=pflash,format=raw,unit=0,readonly=on,file=${ovmf}/FV/OVMF_CODE.fd"
            f" -drive if=pflash,format=raw,unit=1,file={vars_fd}"
            f" -drive file={disk},if=virtio,format=qcow2"
            " -netdev user,id=n0,hostfwd=tcp:127.0.0.1:18080-:80 -device virtio-net-pci,netdev=n0",
            name="installed",
        )
        target.start()
        # The driver's serial connection counts as input at the systemd-boot
        # menu, which then stops its countdown: choose the default entry.
        target.wait_for_console_text("Boot in", timeout=600)
        target.send_key("ret")

        def http(method, path, body=None, headers=None):
            req = urllib.request.Request("http://127.0.0.1:18080" + path, method=method,
                                         data=None if body is None else json.dumps(body).encode(),
                                         headers={"Content-Type": "application/json", **(headers or {})})
            try:
                with urllib.request.urlopen(req, timeout=10) as r:
                    return r.status, json.loads(r.read() or b"{}")
            except urllib.error.HTTPError as e:
                try:
                    return e.code, json.loads(e.read() or b"{}")
                except Exception:
                    return e.code, {}

        deadline = time.time() + 2400 # software emulation when the host has no nested KVM
        while True:
            try:
                code, _ = http("GET", "/api/system/status")
                if code == 200:
                    break
            except Exception:
                pass
            if time.time() > deadline:
                target.screenshot("installed-boot") # emergency mode etc. shows on the screen only
                raise AssertionError("the installed system's web interface did not come up (screenshot: installed-boot.png)")
            time.sleep(5)

        code, r = http("POST", "/api/system/setup-admin", {"username": "admin", "password": "Taken-Over-1!"})
        assert r.get("success") is not True, f"the setup step changed the installer's password: {code} {r}"
        code, r = http("POST", "/api/auth/login", {"username": "admin", "password": PASSWORD})
        assert r.get("success") is True and r.get("session_id"), f"login with the installer's password: {code} {r}"
        sid = r["session_id"]
        code, r = http("GET", "/api/system/updates/nixos", headers={"X-Session-ID": sid, "X-User": "admin"})
        assert code == 200 and r.get("nixos") is True and r.get("live") is False, r
        # ACPI power button: a clean shutdown writes the journal to the disk
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
  '';
}
