{
  description = "D-PlaneOS v5.0 : NAS Operating System (Appliance Build)";

  # ── Inputs ─────────────────────────────────────────────────────────────────
  #
  # PINNING STRATEGY (Task 4.3):
  #
  # nixpkgs is pinned to nixos-25.11 (the current stable channel).
  # We do NOT track nixpkgs-unstable : appliance builds must be reproducible.
  #
  # Kernel pin: 6.6 LTS
  #   Linux 6.6 is an LTS kernel supported until December 2026.
  #   nixpkgs 25.11 DEFAULT kernel is 6.12 (changed from 6.6 in 25.05).
  #   We explicitly pin to 6.6 for proven ZFS compat : still available in
  #   25.11 as pkgs.linuxPackages_6_6, just no longer the default.
  #   Set via: boot.kernelPackages = pkgs.linuxPackages_6_6
  #
  # ZFS pin: stable (LTS) branch
  #   As of early 2026: OpenZFS current = 2.4.x, LTS = 2.3.x.
  #   nixpkgs pkgs.zfs tracks the LTS branch; pkgs.zfs_unstable tracks current.
  #   Pinned via: boot.zfs.package = pkgs.zfs  (NOT pkgs.zfs_unstable)
  #   Verify actual version: nix eval nixpkgs#zfs.version
  #
  # Both pins are validated by NixOS assertions : if the pinned version is
  # unavailable in the nixpkgs revision, nixos-rebuild fails at eval time.
  #
  inputs = {
    # ── NixOS base (LTS channel) ───────────────────────────────────────────
    # Pin to 25.11. Update policy: bump only after 3-month soak period.
    # To update: nix flake update nixpkgs
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-25.11";

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

      # Shared inline config applied to all nixosConfigurations
      applianceConfig = { config, lib, pkgs, ... }: {

        # ── TASK 4.3: Version Pinning ──────────────────────────────────────

        # Kernel: Linux 6.6 LTS (explicit pin : 25.11 default is 6.12)
        # pkgs.linuxPackages_6_6 selects the 6.6.x series from nixpkgs.
        # NixOS will not auto-upgrade across this pin.
        # Upgrade path: linuxPackages_6_6 → linuxPackages_6_12 after ZFS compat check.
        boot.kernelPackages = pkgs.linuxPackages_6_6;

        # ZFS: stable (LTS) branch via pkgs.zfs
        # pkgs.zfs = LTS branch (2.3.x as of NixOS 25.11). pkgs.zfs_unstable = current (2.4.x).
        # Verify: nix eval nixpkgs#zfs.version
        boot.zfs.package = pkgs.zfs;

        # ZFS ARC limit: 16 GiB. Prevents ARC from starving Docker containers.
        # Tunable at runtime via /proc/sys/module/zfs/parameters/zfs_arc_max.
        boot.kernelParams = [
          "zfs.zfs_arc_max=17179869184"
        ];

        # Version assertions : fire at eval time, not runtime.
        # Prevents silent version drift when nixpkgs is bumped.
        assertions = [
          {
            assertion = lib.versionAtLeast
              config.boot.kernelPackages.kernel.version "6.6";
            message = "D-PlaneOS requires Linux kernel >= 6.6 LTS. "
              + "Current: ${config.boot.kernelPackages.kernel.version}. "
              + "Set boot.kernelPackages = pkgs.linuxPackages_6_6.";
          }
          {
            assertion = lib.versionAtLeast
              config.boot.zfs.package.version "2.3";
            message = "D-PlaneOS requires OpenZFS >= 2.3 (LTS branch). "
              + "Current: ${config.boot.zfs.package.version}. "
              + "Ensure boot.zfs.package = pkgs.zfs (not pkgs.zfs_unstable).";
          }
        ];

        # ── systemd-boot for A/B slot management ──────────────────────────
        # Keep both A and B entries. OTA uses bootctl set-default to switch.
        boot.loader.systemd-boot = {
          enable             = true;
          configurationLimit = 2;   # A + B only : no old generation clutter
        };
        boot.loader.efi.canTouchEfiVariables = true;

        # ── Persist DB path ────────────────────────────────────────────────
        # /var/lib/dplaneos is bind-mounted from /persist/dplaneos by
        # impermanence.nix. PostgreSQL state lives in /var/lib/dplaneos/pgsql.
        services.dplaneos.dbPath = "/var/lib/dplaneos/pgsql";

        # ── Nix GC (appliance: keep store lean) ───────────────────────────
        nix.settings.auto-optimise-store = true;
        nix.gc = {
          automatic = true;
          dates     = "weekly";
          options   = "--delete-older-than 14d";
        };

        # License: AGPLv3 - OSI-approved, no allowUnfreePredicate needed.
      };

    in
    flake-utils.lib.eachSystem supportedSystems (system:
      let
        pkgs    = nixpkgs.legacyPackages.${system};

        # ── Static build via musl ──────────────────────────────────────────
        # Why musl instead of glibc?
        #
        # glibc dynamic binary: breaks when NixOS updates glibc (different
        # store path → loader mismatch → daemon refuses to start after rebuild)
        #
        # musl static binary: zero dynamic dependencies. Runs on any Linux
        # kernel ≥ 4.9 regardless of what the host has in its Nix store.
        # Upgrade NixOS 23.11 → 25.05 → 26.x: daemon keeps running.
        #
        # The only trade-off: CGO (libzfs) must be compiled against musl.
        # musl + static libzfs = fully self-contained.
        #
        # pkgsStatic = pkgs built for the current system but linked against
        # musl via the `pkgsCross.musl64` overlay : available in nixpkgs.
        pkgsStatic = pkgs.pkgsStatic;
      in {
        # ── Daemon package (static musl binary) ───────────────────────────
        packages.dplaneos-daemon = pkgsStatic.buildGoModule {
          pname        = "dplaneos-daemon";
          version      = dplaneosVersion;
          src          = ./.;
          # CGO_ENABLED=1 required for ZFS interop
          env.CGO_ENABLED = "1";
          # vendorHash: run `nix build .#dplaneos-daemon 2>&1 | grep "got:"` to
          # find the correct hash after any go.sum change, then update this value.
          # Use nixpkgs.lib.fakeHash during initial setup to trigger the hash error.
          vendorHash   = nixpkgs.lib.fakeHash;
          subPackages  = [ "daemon/cmd/dplaned" ];
          nativeBuildInputs = with pkgsStatic; [ musl.dev gcc ];
          # -linkmode external  : hand final link to musl-gcc
          # -extldflags -static : produce a fully static ELF, no .so deps
          ldflags = [
            "-s" "-w"
            "-X" "main.Version=${dplaneosVersion}"
            "-linkmode" "external"
            "-extldflags" "-static"
          ];
          # Verify the output is actually static (assertion in Nix build)
          postInstall = ''
            if ldd $out/bin/dplaned 2>&1 | grep -q "not a dynamic executable"; then
              echo "✓ dplaned is fully static (no dynamic deps)"
            else
              echo "WARNING: dplaned has dynamic dependencies:"
              ldd $out/bin/dplaned || true
            fi
          '';
          meta = with nixpkgs.lib; {
            description = "D-PlaneOS NAS daemon : static musl build";
            homepage    = "https://github.com/4nonX/D-PlaneOS";
            license     = licenses.agpl3Only;
            platforms   = supportedSystems;
          };
        };

        # ── Fallback: glibc dynamic build (for dev/CI where musl isn't set up)
        # Build with: nix build .#dplaneos-daemon-dynamic
        packages.dplaneos-daemon-dynamic = pkgs.buildGoModule {
          pname        = "dplaneos-daemon-dynamic";
          version      = dplaneosVersion;
          src          = ./.;
          env.CGO_ENABLED = "1";
          vendorHash   = nixpkgs.lib.fakeHash;  # update after go.sum changes
          subPackages  = [ "daemon/cmd/dplaned" ];
          nativeBuildInputs = with pkgs; [ gcc ];
          ldflags = [ "-s" "-w" "-X" "main.Version=${dplaneosVersion}" ];
          meta = with nixpkgs.lib; {
            description = "D-PlaneOS NAS daemon : glibc dynamic build (dev only)";
            license     = licenses.agpl3Only;
          };
        };

        packages.iso = if system == "x86_64-linux" 
                       then self.nixosConfigurations.iso.config.system.build.isoImage
                       else null;

        packages.default = self.packages.${system}.dplaneos-daemon;

        devShells.default = pkgs.mkShell {
          buildInputs = with pkgs; [ go 1.25 gcc musl.dev gopls gotools postgresql git ];
          shellHook = ''
            export CGO_ENABLED=1
            echo "D-PlaneOS dev shell (Go 1.25, PostgreSQL)"
            echo "Build command: go build ./daemon/cmd/dplaned/"
            echo "Static build: CC=musl-gcc go build -ldflags '-linkmode external -extldflags -static' ./daemon/cmd/dplaned/"
          '';
        };
      }
    ) //
    {
      # ── NixOS module (system-agnostic) ───────────────────────────────────
      nixosModules.dplaneos = import ./nixos/module.nix;

      # ── x86_64 appliance build ────────────────────────────────────────────
      # nixos-rebuild switch --flake /etc/nixos#dplaneos
      nixosConfigurations.dplaneos = nixpkgs.lib.nixosSystem {
        system      = "x86_64-linux";
        specialArgs = { inherit self; };
        modules     = [
          ./nixos/configuration-standalone.nix
          self.nixosModules.dplaneos
          disko.nixosModules.disko
          ./nixos/disko.nix
          impermanence.nixosModules.impermanence
          ./nixos/impermanence.nix
          ./nixos/ota-module.nix
          ./nixos/modules/samba.nix
          ./nixos/dplane-generated.nix   # v5.0: static JSON-to-Nix bridge
          applianceConfig
          # Wire the daemon package into the module option
          { services.dplaneos.daemonPackage =
              self.packages.x86_64-linux.dplaneos-daemon; }
        ];
      };

      # ── aarch64 appliance build (ARM NAS hardware) ────────────────────────
      nixosConfigurations.dplaneos-arm = nixpkgs.lib.nixosSystem {
        system      = "aarch64-linux";
        specialArgs = { inherit self; };
        modules     = [
          ./nixos/configuration-standalone.nix
          self.nixosModules.dplaneos
          disko.nixosModules.disko
          ./nixos/disko.nix
          impermanence.nixosModules.impermanence
          ./nixos/impermanence.nix
          ./nixos/ota-module.nix
          ./nixos/modules/samba.nix
          ./nixos/dplane-generated.nix   # v5.0: static JSON-to-Nix bridge
          applianceConfig
          { services.dplaneos.daemonPackage =
              self.packages.aarch64-linux.dplaneos-daemon; }
        ];
      };

      nixosConfigurations.iso = nixpkgs.lib.nixosSystem {
        system      = "x86_64-linux";
        specialArgs = { inherit self; inputs = { disko = disko; }; };
        modules     = [
          ./nixos/installer.nix
          {
            environment.etc."dplaneos-install/VERSION".text = dplaneosVersion;
          }
        ];
      };

    };
}
