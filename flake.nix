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
      applianceConfig = { config, lib, pkgs, ... }: {
        boot.kernelPackages = pkgs.linuxPackages_6_12;
        boot.zfs.package = pkgs.zfs;
        boot.kernelParams = [ "zfs.zfs_arc_max=17179869184" ];
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
    flake-utils.lib.eachSystem supportedSystems (system:
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

        # Live boot integration test (Tier 2).
        # Validates that D-PlaneOS boots from live ISO, daemon starts, UI accessible,
        # ZFS auto-import works, and system is ephemeral. Run with:
        #   nix build .#checks.x86_64-linux.live-boot -L
        checks.live-boot = import ./nixos/tests/live-boot.nix {
          inherit nixpkgs system impermanence;
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

        devShells.default = pkgs.mkShell {
          buildInputs = with pkgs; [ go gcc musl.dev gopls gotools postgresql git ];
          shellHook = ''
            export CGO_ENABLED=1
            echo "DPlaneOS dev shell"
          '';
        };
      }
    ) //
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

      # Live boot ISO configurations
      nixosConfigurations.iso-live = let
        system = "x86_64-linux";
      in nixpkgs.lib.nixosSystem {
        inherit system;
        specialArgs = {
          inherit self;
          targetSystem = self.nixosConfigurations.dplaneos-live.config.system.build.toplevel;
        };
        modules = [
          ./nixos/live-iso.nix
          { environment.etc."dplaneos-live/VERSION".text = dplaneosVersion; }
        ];
      };

      nixosConfigurations.iso-live-arm = let
        system = "aarch64-linux";
      in nixpkgs.lib.nixosSystem {
        inherit system;
        specialArgs = {
          inherit self;
          targetSystem = self.nixosConfigurations.dplaneos-live-arm.config.system.build.toplevel;
        };
        modules = [
          ./nixos/live-iso.nix
          { environment.etc."dplaneos-live/VERSION".text = dplaneosVersion; }
        ];
      };

      # Live boot ISO packages (artifacts)
      packages.x86_64-linux.iso-live  = self.nixosConfigurations.iso-live.config.system.build.isoImage;
      packages.aarch64-linux.iso-live  = self.nixosConfigurations.iso-live-arm.config.system.build.isoImage;
    };
}
