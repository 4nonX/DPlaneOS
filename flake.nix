{
  description = "DPlaneOS v10.0 : NAS Operating System (Appliance Build)";

  # ── Inputs ─────────────────────────────────────────────────────────────────
  #
  # PINNING STRATEGY (Task 4.3):
  #
  # nixpkgs is pinned to nixos-26.05 (the current stable channel).
  # We do NOT track nixpkgs-unstable : appliance builds must be reproducible.
  #
  # Kernel pin: 6.12 LTS
  #   Linux 6.12 is the LTS kernel in nixpkgs 26.05 and the default.
  #   Linux 6.6 LTS reaches end-of-life December 2026 and has been retired.
  #   OpenZFS 2.3.x is validated against 6.12; all ZFS features remain stable.
  #   Set via: boot.kernelPackages = pkgs.linuxPackages_6_12
  #
  # ZFS pin: stable (LTS) branch
  #   As of mid 2026: OpenZFS current = 2.4.x, LTS = 2.3.x.
  #   nixpkgs pkgs.zfs tracks the LTS branch; pkgs.zfs_unstable tracks current.
  #   Pinned via: boot.zfs.package = pkgs.zfs  (NOT pkgs.zfs_unstable)
  #   Verify actual version: nix eval nixpkgs#zfs.version
  #
  # Both pins are validated by NixOS assertions : if the pinned version is
  # unavailable in the nixpkgs revision, nixos-rebuild fails at eval time.
  #
  inputs = {
    # ── NixOS base (stable channel) ───────────────────────────────────────
    # Pin to 26.05. Update policy: bump only after 3-month soak period.
    # To update: nix flake update nixpkgs
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-26.05";
    
    flake-utils.url = "github:numtide/flake-utils";

    # ── disko: declarative disk partitioning (Task 4.1) ──────────────────
    disko = {
      url    = "github:nix-community/disko";
      inputs.nixpkgs.follows = "nixpkgs";
    };

    # ── nixos-impermanence (Task 4.4) ─────────────────────────────────────
    impermanence.url = "github:nix-community/impermanence";
  };

  outputs = { self, nixpkgs, flake-utils, disko, impermanence }:
  let
    # Read version from VERSION file at evaluation time : single source of truth
    dplaneosVersion = builtins.replaceStrings ["\n"] [""] (builtins.readFile ./VERSION);
  in
    let
      supportedSystems = [ "x86_64-linux" "aarch64-linux" ];

      # ── Daemon Builder (breaks evaluation loops) ─────────────────────────
      mkDaemon = { system, pkgs, pkgsStatic, dplaneosVersion, nixpkgs }: pkgsStatic.buildGoModule {
        pname        = "dplaneos-daemon";
        version      = dplaneosVersion;
        src          = nixpkgs.lib.cleanSource ./daemon;
        env.CGO_ENABLED = "0";
        vendorHash   = null;
        subPackages  = [ "cmd/dplaned" "cmd/dplane-fenced" ];
        nativeBuildInputs = [];
        ldflags = [
          "-s" "-w"
          "-X" "main.Version=${dplaneosVersion}"
        ];
        postInstall = ''
          if ldd $out/bin/dplaned 2>&1 | grep -q "not a dynamic executable"; then
            echo "✓ dplaned is fully static (no dynamic deps)"
          else
            echo "WARNING: dplaned has dynamic dependencies:"
            ldd $out/bin/dplaned || true
          fi
        '';
        meta = with nixpkgs.lib; {
          description = "DPlaneOS NAS daemon : static musl build";
          homepage    = "https://github.com/4nonX/DPlaneOS";
          license     = licenses.agpl3Only;
          platforms   = supportedSystems;
        };
      };

      mkDaemonDynamic = { system, pkgs, dplaneosVersion, nixpkgs }: pkgs.buildGoModule {
        pname        = "dplaneos-daemon-dynamic";
        version      = dplaneosVersion;
        src          = nixpkgs.lib.cleanSource ./daemon;
        env.CGO_ENABLED = "0";
        vendorHash   = null;
        subPackages  = [ "cmd/dplaned" "cmd/dplane-fenced" ];
        nativeBuildInputs = [];
        ldflags = [ "-s" "-w" "-X" "main.Version=${dplaneosVersion}" ];
        meta = with nixpkgs.lib; {
          description = "DPlaneOS NAS daemon : glibc dynamic build (dev only)";
          license     = licenses.agpl3Only;
        };
      };

      # mkDaemonCGO: glibc build with CGO_ENABLED=1 and libzfs headers.
      #
      # This variant activates the daemon/internal/libzfs/zfs_cgo.go code path,
      # which calls libzfs directly via cgo instead of spawning ZFS subprocesses.
      # It produces a dynamically-linked ELF that requires libzfs.so at runtime.
      # NixOS wraps the binary in a derivation that sets LD_LIBRARY_PATH so
      # the runtime dependency is satisfied without /etc/ld.so.conf changes.
      #
      # Build inputs:
      #   pkgs.zfs.dev       - provides libzfs.h and other headers (dev output)
      #   pkgs.zfs           - provides libzfs.so, libzfs_core.so at runtime
      #   pkgs.gcc           - C compiler for cgo
      #
      # To build manually: nix build .#dplaneos-daemon-cgo
      mkDaemonCGO = { system, pkgs, dplaneosVersion, nixpkgs }: pkgs.buildGoModule {
        pname        = "dplaneos-daemon-cgo";
        version      = dplaneosVersion;
        src          = nixpkgs.lib.cleanSource ./daemon;
        env.CGO_ENABLED = "1";
        vendorHash   = null;
        subPackages  = [ "cmd/dplaned" "cmd/dplane-fenced" ];
        nativeBuildInputs = with pkgs; [ gcc pkg-config ];
        buildInputs  = with pkgs; [ zfs.dev zfs ];
        # CGO bypasses the GCC wrapper and does not see NIX_CFLAGS_COMPILE.
        # Set PKG_CONFIG_PATH explicitly so #cgo pkg-config: libzfs (in
        # zfs_cgo.go) can find libzfs.pc regardless of setup-hook ordering.
        # CGO_CFLAGS is a belt-and-suspenders fallback for the include path.
        preBuild = ''
          export PKG_CONFIG_PATH="${pkgs.zfs.dev}/lib/pkgconfig:${pkgs.zfs.dev}/share/pkgconfig:$PKG_CONFIG_PATH"
          export CGO_CFLAGS="-I${pkgs.zfs.dev}/include"
          export CGO_LDFLAGS="-L${pkgs.zfs}/lib"
        '';
        ldflags = [ "-s" "-w" "-X" "main.Version=${dplaneosVersion}" ];
        tags = [ "libzfs" ];
        meta = with nixpkgs.lib; {
          description = "DPlaneOS NAS daemon : glibc + libzfs cgo build";
          license     = licenses.agpl3Only;
          platforms   = [ "x86_64-linux" "aarch64-linux" ];
        };
      };

      # Package the pre-built frontend (app/ is the vite build output committed to git).
      # nginx serves from the resulting Nix store path so the content is immutable
      # and atomically replaced on every nixos-rebuild switch.
      mkFrontend = { pkgs }: pkgs.runCommand "dplaneos-frontend-${dplaneosVersion}" {} ''
        cp -a ${./app}/. $out
      '';

      # Shared inline config applied to all nixosConfigurations
      # Where the system comes from (/etc/dplaneos-source.json; not under
      # /etc/dplaneos, which is bind-mounted from /persist): the daemon writes
      # /etc/nixos/flake.nix from it (rebuilds use the same release plus the
      # machine's hardware configuration) and updates move it to a newer
      # release. Without it a rebuild evaluated NixOS's stock
      # /etc/nixos/configuration.nix.
      # The installer's partitioning scripts, built with the ISO. disko's CLI
      # evaluated the layout at install time with `import <nixpkgs>`, which
      # the offline ISO does not have (and it would have built the script
      # without substituters). The scripts partition the device
      # /dev/dplaneos-install-target, which install.sh links to the chosen
      # disk; one script per system partition size.
      installerDisko = system: let
        script = size: (nixpkgs.lib.nixosSystem {
          inherit system;
          modules = [
            disko.nixosModules.disko
            ./nixos/disko.nix
            ({ lib, ... }: {
              disko.devices.disk.os.device = lib.mkForce "/dev/dplaneos-install-target";
              disko.devices.disk.os.content.partitions.system-a.size = lib.mkForce size;
              system.stateVersion = "26.05";
            })
          ];
        }).config.system.build.diskoScript;
      in { "40G" = script "40G"; "24G" = script "24G"; };

      sourceModule = configuration: live: {
        environment.etc."dplaneos-source.json".text = builtins.toJSON {
          flake = "github:4nonX/DPlaneOS";
          rev = self.rev or null; # null when built from a tree with uncommitted changes
          version = dplaneosVersion;
          inherit configuration live;
        };
      };

      applianceConfig = { config, lib, pkgs, ... }: {
        boot.kernelPackages = pkgs.linuxPackages_6_12;
        boot.zfs.package = pkgs.zfs;
        # mkDefault: configuration-standalone.nix sets the same limit with its
        # console options; both together put it on the command line twice.
        boot.kernelParams = lib.mkDefault [ "zfs.zfs_arc_max=17179869184" ];
        assertions = [
          {
            assertion = lib.versionAtLeast config.boot.kernelPackages.kernel.version "6.12";
            message = "DPlaneOS requires Linux kernel >= 6.12 LTS. Linux 6.6 LTS reached end-of-life December 2026.";
          }
          {
            assertion = lib.versionAtLeast config.boot.zfs.package.version "2.3";
            message = "DPlaneOS requires OpenZFS >= 2.3 (LTS branch).";
          }
        ];
        boot.loader.systemd-boot = { enable = true; configurationLimit = 2; };
        boot.loader.efi.canTouchEfiVariables = true;
        services.dplaneos.dbPath = "/var/lib/dplaneos/pgsql";
        nix.settings.auto-optimise-store = true;
        nix.gc = { automatic = true; dates = "weekly"; options = "--delete-older-than 14d"; };

        # python3.11-3.11.15-doc fails to build in nixpkgs 26.05 (Sphinx 9.1
        # + docutils 0.22.4 incompatibility). The doc derivation's store path
        # is baked into the system-path dependency graph by the time overlays
        # run, so a nixpkgs overlay cannot intercept it. Forcing
        # extraOutputsToInstall to empty is the only reliable way to prevent
        # any doc output from being pulled into the system-path closure.
        # An appliance NAS has no use for in-closure HTML documentation.
        environment.extraOutputsToInstall = lib.mkForce [];
        documentation.nixos.enable = false;
      };

    in
    # recursiveUpdate: the per-system packages (daemon, frontend) and the ISO
    # and witness images below are both under packages.<system>; a plain //
    # replaced the first set with the second.
    nixpkgs.lib.recursiveUpdate (flake-utils.lib.eachSystem supportedSystems (system:
      let
        pkgs       = nixpkgs.legacyPackages.${system};
        pkgsStatic = pkgs.pkgsStatic;
        daemon     = mkDaemon { inherit system pkgs pkgsStatic dplaneosVersion nixpkgs; };
        daemonDyn  = mkDaemonDynamic { inherit system pkgs dplaneosVersion nixpkgs; };
        frontend   = mkFrontend { inherit pkgs; };
      in {
        packages.dplaneos-daemon         = daemon;
        packages.dplaneos-daemon-dynamic = daemonDyn;
        packages.dplaneos-daemon-cgo     = mkDaemonCGO { inherit system pkgs dplaneosVersion nixpkgs; };
        packages.dplaneos-frontend       = frontend;
        packages.default = daemon;

        # Live boot integration test (Tier 2): the live configuration in a test
        # VM. The daemon starts, the UI answers, ZFS auto-import works, the
        # system is ephemeral and the persistence drive keeps state. Run with:
        #   nix build .#checks.x86_64-linux.live-boot -L
        checks.live-boot = import ./nixos/tests/live-boot.nix {
          inherit nixpkgs system impermanence;
          daemonPackage = daemon;
          frontendPackage = frontend;
        };

        # The live ISO image itself boots (UEFI, from the CD) into D-PlaneOS.
        #   nix build .#checks.x86_64-linux.live-iso -L
        checks.live-iso = import ./nixos/tests/live-iso.nix {
          inherit nixpkgs system;
          isoSystem = self.nixosConfigurations.${if system == "aarch64-linux" then "iso-live-arm" else "iso-live"};
        };

        # Updates: the check after the reboot keeps a working update and goes
        # back to the previous generation after a broken one.
        #   nix build .#checks.x86_64-linux.update -L
        checks.update = import ./nixos/tests/update.nix {
          inherit nixpkgs system;
          daemonPackage = daemon;
          frontendPackage = frontend;
        };

        # The installer: unattended install onto an empty disk, then the
        # installed disk boots (UEFI) and the admin logs in over HTTP.
        #   nix build .#checks.x86_64-linux.installer -L
        checks.installer = import ./nixos/tests/installer.nix {
          inherit nixpkgs system;
          targetSystem = self.nixosConfigurations.dplaneos-for-iso.config.system.build.toplevel;
          diskoScripts = installerDisko system;
        };

        # Active Directory: join a Samba AD DC through the API, AD users resolve.
        #   nix build .#checks.x86_64-linux.ad-join -L
        checks.ad-join = import ./nixos/tests/ad-join.nix {
          inherit nixpkgs system;
          daemonPackage = daemon;
          frontendPackage = frontend;
        };

        # UPS: NUT events become alerts; low battery shuts the machine down.
        #   nix build .#checks.x86_64-linux.ups -L
        checks.ups = import ./nixos/tests/ups.nix {
          inherit nixpkgs system;
          daemonPackage = daemon;
          frontendPackage = frontend;
        };

        # Configuration backup and restore through the API (daemon restart).
        #   nix build .#checks.x86_64-linux.config-restore -L
        checks.config-restore = import ./nixos/tests/config-restore.nix {
          inherit nixpkgs system;
          daemonPackage = daemon;
          frontendPackage = frontend;
        };

        # Cluster quorum and third vote (Design 0001 phase 3a): three VMs.
        #   nix build .#checks.x86_64-linux.quorum -L
        checks.quorum = import ./nixos/tests/quorum.nix {
          inherit nixpkgs system impermanence;
          daemonPackage = daemon;
          frontendPackage = frontend;
        };

        # Storage groups and epochs on a shared disk (Design 0001 phase 3b).
        #   nix build .#checks.x86_64-linux.groups -L
        checks.groups = import ./nixos/tests/groups.nix {
          inherit nixpkgs system impermanence;
          daemonPackage = daemon;
          frontendPackage = frontend;
        };

        # Automatic failover of a storage group (Design 0001 phase 3c).
        #   nix build .#checks.x86_64-linux.failover -L
        checks.failover = import ./nixos/tests/failover.nix {
          inherit nixpkgs system impermanence;
          daemonPackage = daemon;
          frontendPackage = frontend;
        };

        # Replicated storage groups (Design 0001 phase 3c).
        #   nix build .#checks.x86_64-linux.replicated -L
        checks.replicated = import ./nixos/tests/replicated.nix {
          inherit nixpkgs system impermanence;
          daemonPackage = daemon;
          frontendPackage = frontend;
        };

        # The third vote from anything: a voter (corosync-only machine) and a
        # third DPlaneOS member (Design 0001 phase 3).
        #   nix build .#checks.x86_64-linux.votes -L
        checks.votes = import ./nixos/tests/votes.nix {
          inherit nixpkgs system impermanence;
          daemonPackage = daemon;
          frontendPackage = frontend;
        };

        # A full setup through the API, conflict rules and maintenance
        # (Design 0001 phase 4).
        #   nix build .#checks.x86_64-linux.setup -L
        checks.setup = import ./nixos/tests/setup.nix {
          inherit nixpkgs system impermanence;
          daemonPackage = daemon;
          frontendPackage = frontend;
        };

        # Hardening (Design 0001 phase 7): slow network, splits and power loss.
        #   nix build .#checks.x86_64-linux.hardening-{pair,replicated,cluster,standalone} -L
        checks.hardening-pair = import ./nixos/tests/hardening-pair.nix {
          inherit nixpkgs system impermanence;
          daemonPackage = daemon;
          frontendPackage = frontend;
        };
        checks.hardening-replicated = import ./nixos/tests/hardening-replicated.nix {
          inherit nixpkgs system impermanence;
          daemonPackage = daemon;
          frontendPackage = frontend;
        };
        checks.hardening-cluster = import ./nixos/tests/hardening-cluster.nix {
          inherit nixpkgs system impermanence;
          daemonPackage = daemon;
          frontendPackage = frontend;
        };
        checks.hardening-standalone = import ./nixos/tests/hardening-standalone.nix {
          inherit nixpkgs system impermanence;
          daemonPackage = daemon;
          frontendPackage = frontend;
        };

        # The DPlaneOS Witness appliance joins headlessly.
        #   nix build .#checks.x86_64-linux.witness-appliance -L
        checks.witness-appliance = import ./nixos/tests/witness-appliance.nix {
          inherit nixpkgs system impermanence;
          daemonPackage = daemon;
          frontendPackage = frontend;
        };

        # Installs before 16.0 mount the partition labels ESP/system-a/persist
        # (nixos/disko.nix, legacyPartitionLabels). Checked at evaluation.
        checks.partition-labels = let
          installed = self.nixosConfigurations.${if system == "aarch64-linux" then "dplaneos-arm" else "dplaneos"};
          legacy    = installed.extendModules { modules = [ { services.dplaneos.legacyPartitionLabels = true; } ]; };
          devices   = c: map (m: c.config.fileSystems.${m}.device) [ "/" "/boot" "/persist" ];
          want      = l: map (n: "/dev/disk/by-partlabel/${n}") l;
        in
          assert devices installed == want [ "dplaneos-system" "dplaneos-esp" "dplaneos-persist" ];
          assert devices legacy    == want [ "system-a" "ESP" "persist" ];
          pkgs.runCommand "dplaneos-partition-labels" { } "touch $out";

        devShells.default = pkgs.mkShell {
          buildInputs = with pkgs; [ go gcc musl.dev gopls gotools postgresql git ];
          shellHook = ''
            export CGO_ENABLED=1
            echo "DPlaneOS dev shell"
          '';
        };
      }
    ))
    {
      nixosModules.dplaneos         = import ./nixos/module.nix;

      nixosConfigurations.dplaneos = let system = "x86_64-linux"; pkgs = nixpkgs.legacyPackages.${system}; in nixpkgs.lib.nixosSystem {
        inherit system pkgs;
        specialArgs = { inherit self; };
        modules     = [
          ./nixos/configuration-standalone.nix
          self.nixosModules.dplaneos
          disko.nixosModules.disko
          ./nixos/disko.nix
          impermanence.nixosModules.impermanence
          ./nixos/impermanence.nix
          ./nixos/ota-module.nix
          ./nixos/dplane-generated.nix
          applianceConfig
          (sourceModule "dplaneos" false)
          (let d = mkDaemonCGO { inherit system pkgs dplaneosVersion nixpkgs; };
               f = mkFrontend { inherit pkgs; };
           in {
            services.dplaneos.daemonPackage    = d;
            services.dplaneos.fenced.package   = d;
            services.dplaneos.frontendPackage  = f;
          })
        ];
      };

      nixosConfigurations.dplaneos-arm = let system = "aarch64-linux"; pkgs = nixpkgs.legacyPackages.${system}; in nixpkgs.lib.nixosSystem {
        inherit system pkgs;
        specialArgs = { inherit self; };
        modules     = [
          ./nixos/configuration-standalone.nix
          self.nixosModules.dplaneos
          disko.nixosModules.disko
          ./nixos/disko.nix
          impermanence.nixosModules.impermanence
          ./nixos/impermanence.nix
          ./nixos/ota-module.nix
          ./nixos/dplane-generated.nix
          applianceConfig
          (sourceModule "dplaneos-arm" false)
          (let d = mkDaemonCGO { inherit system pkgs dplaneosVersion nixpkgs; };
               f = mkFrontend { inherit pkgs; };
           in {
            services.dplaneos.daemonPackage    = d;
            services.dplaneos.fenced.package   = d;
            services.dplaneos.frontendPackage  = f;
          })
          # Hard override: ensure no x86_64-only Intel packages are evaluated for aarch64.
          # intel-media-driver, intel-compute-runtime, and intel-microcode all declare
          # meta.platforms = ["x86_64-linux"] and fail at eval time on aarch64.
          ({ lib, ... }: {
            hardware.graphics.extraPackages       = nixpkgs.lib.mkForce [];
            hardware.cpu.intel.updateMicrocode    = nixpkgs.lib.mkForce false;
          })
        ];
      };

      # ── ISO Installer Configurations ─────────────────────────────────────
      # Built as separate calls to nixosSystem so the target system toplevel
      # can be passed via specialArgs without triggering evaluation loops.
      # Each arch bakes its own NAS closure into the ISO for offline install.

      # dplaneos-for-iso: identical to dplaneos but strips the large Intel-only
      # extras (intel-media-driver ~100 MB, intel-compute-runtime ~300 MB) that
      # push the x86_64 squashfs over the GitHub Actions disk limit.
      # The running system (dplaneos) keeps them; users who need VA-API/OpenCL
      # on an offline-installed system can add them via nixos-rebuild.
      nixosConfigurations.dplaneos-for-iso = let
        system = "x86_64-linux";
        pkgs   = nixpkgs.legacyPackages.${system};
      in nixpkgs.lib.nixosSystem {
        inherit system pkgs;
        specialArgs = { inherit self; };
        modules = [
          ./nixos/configuration-standalone.nix
          self.nixosModules.dplaneos
          disko.nixosModules.disko
          ./nixos/disko.nix
          impermanence.nixosModules.impermanence
          ./nixos/impermanence.nix
          ./nixos/ota-module.nix
          ./nixos/dplane-generated.nix
          applianceConfig
          (sourceModule "dplaneos" false) # installs rebuild as dplaneos
          (let d = mkDaemonCGO { inherit system pkgs dplaneosVersion nixpkgs; };
               f = mkFrontend { inherit pkgs; };
           in {
            services.dplaneos.daemonPackage   = d;
            services.dplaneos.fenced.package  = d;
            services.dplaneos.frontendPackage = f;
          })
          # Strip packages that bloat the squashfs but are not needed to install
          # or run the core NAS stack. They can be fetched post-install via
          # nixos-rebuild once the system is on the network.
          #
          # intel-media-driver  (~100 MB): VA-API hardware video transcoding.
          #   Needed for Jellyfin/Plex hardware transcode; NOT needed for ZFS/SMB/NFS/iSCSI/HA.
          # intel-compute-runtime (~300 MB): OpenCL GPU compute.
          #   Needed for GPU compute workloads; NOT needed for GPU passthrough (VFIO) or NAS core.
          #
          # NOTE: hardware.cpu.intel.updateMicrocode is intentionally NOT stripped.
          # CPU microcode updates (Spectre/Meltdown/Raptor Lake errata) are small and
          # security-critical; they belong in the offline-installed system.
          ({ lib, ... }: {
            hardware.graphics.extraPackages = nixpkgs.lib.mkForce [];
          })
        ];
      };

      nixosConfigurations.iso = let
        system = "x86_64-linux";
      in nixpkgs.lib.nixosSystem {
        inherit system;
        specialArgs = {
          inherit self;
          targetSystem = self.nixosConfigurations.dplaneos-for-iso.config.system.build.toplevel;
          diskoScripts = installerDisko system;
        };
        modules = [
          ./nixos/installer.nix
          { environment.etc."dplaneos-install/VERSION".text = dplaneosVersion; }
        ];
      };

      nixosConfigurations.iso-arm = let
        system = "aarch64-linux";
      in nixpkgs.lib.nixosSystem {
        inherit system;
        specialArgs = {
          inherit self;
          targetSystem = self.nixosConfigurations.dplaneos-arm.config.system.build.toplevel;
          diskoScripts = installerDisko system;
        };
        modules = [
          ./nixos/installer.nix
          { environment.etc."dplaneos-install/VERSION".text = dplaneosVersion; }
        ];
      };

      packages.x86_64-linux.iso  = self.nixosConfigurations.iso.config.system.build.isoImage;
      packages.aarch64-linux.iso  = self.nixosConfigurations.iso-arm.config.system.build.isoImage;

      # ── Live Boot Configurations ──────────────────────────────────────────
      # D-PlaneOS running directly from USB without installation.
      # Reuses impermanence module for ephemeral root + optional USB persistence.

      nixosConfigurations.dplaneos-live = let
        system = "x86_64-linux";
        pkgs   = nixpkgs.legacyPackages.${system};
      in nixpkgs.lib.nixosSystem {
        inherit system pkgs;
        specialArgs = { inherit self; };
        modules = [
          ./nixos/configuration-live.nix
          impermanence.nixosModules.impermanence
          applianceConfig
          (sourceModule "dplaneos-live" true)
          (let d = mkDaemonCGO { inherit system pkgs dplaneosVersion nixpkgs; };
               f = mkFrontend { inherit pkgs; };
           in {
            services.dplaneos.daemonPackage    = d;
            services.dplaneos.fenced.package   = d;
            services.dplaneos.frontendPackage  = f;
          })
        ];
      };

      nixosConfigurations.dplaneos-live-arm = let
        system = "aarch64-linux";
        pkgs   = nixpkgs.legacyPackages.${system};
      in nixpkgs.lib.nixosSystem {
        inherit system pkgs;
        specialArgs = { inherit self; };
        modules = [
          ./nixos/configuration-live.nix
          impermanence.nixosModules.impermanence
          applianceConfig
          (sourceModule "dplaneos-live-arm" true)
          (let d = mkDaemonCGO { inherit system pkgs dplaneosVersion nixpkgs; };
               f = mkFrontend { inherit pkgs; };
           in {
            services.dplaneos.daemonPackage    = d;
            services.dplaneos.fenced.package   = d;
            services.dplaneos.frontendPackage  = f;
          })
          ({ lib, ... }: {
            hardware.graphics.extraPackages       = nixpkgs.lib.mkForce [];
            hardware.cpu.intel.updateMicrocode    = nixpkgs.lib.mkForce false;
          })
        ];
      };

      # Live boot ISO configurations: the live system itself, as an ISO
      # (nixos/live-iso.nix). checks.live-iso boots the image.
      nixosConfigurations.iso-live = self.nixosConfigurations.dplaneos-live.extendModules {
        modules = [
          ./nixos/live-iso.nix
          { environment.etc."dplaneos-live/VERSION".text = dplaneosVersion; }
        ];
      };

      nixosConfigurations.iso-live-arm = self.nixosConfigurations.dplaneos-live-arm.extendModules {
        modules = [
          ./nixos/live-iso.nix
          { environment.etc."dplaneos-live/VERSION".text = dplaneosVersion; }
        ];
      };

      # Live boot ISO packages (artifacts)
      # ── DPlaneOS Witness (third vote appliance, docs/admin/THIRD-VOTE.md) ──
      nixosConfigurations.witness-pi = nixpkgs.lib.nixosSystem {
        system = "aarch64-linux";
        modules = [ ./nixos/witness-image-pi.nix ];
      };
      nixosConfigurations.witness-x86 = nixpkgs.lib.nixosSystem {
        system = "x86_64-linux";
        modules = [ ./nixos/witness-image-x86.nix ];
      };
      packages.aarch64-linux.witness-sd-aarch64 = self.nixosConfigurations.witness-pi.config.system.build.sdImage;
      packages.x86_64-linux.witness-image-x86_64 = self.nixosConfigurations.witness-x86.config.system.build.witnessRaw;
      packages.x86_64-linux.witness-qcow2-x86_64 = self.nixosConfigurations.witness-x86.config.system.build.witnessQcow2;

      packages.x86_64-linux.iso-live  = self.nixosConfigurations.iso-live.config.system.build.isoImage;
      packages.aarch64-linux.iso-live  = self.nixosConfigurations.iso-live-arm.config.system.build.isoImage;
    };
}
