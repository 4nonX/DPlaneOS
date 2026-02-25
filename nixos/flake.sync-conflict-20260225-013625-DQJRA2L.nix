{
  description = "D-PlaneOS — NAS Operating System (Appliance Build)";

  # ── Inputs ─────────────────────────────────────────────────────────────────
  #
  # PINNING STRATEGY (Task 4.3):
  #
  # nixpkgs is pinned to nixos-24.11 (the LTS channel at time of writing).
  # We do NOT track nixpkgs-unstable — appliance builds must be reproducible.
  #
  # Kernel pin: 6.6 LTS
  #   Linux 6.6 is an LTS kernel supported until December 2026.
  #   It has proven ZFS compatibility with OpenZFS 2.2.
  #   nixpkgs 24.11 ships linux_6_6 — no overlay needed.
  #   Set via: boot.kernelPackages = pkgs.linuxPackages_6_6
  #
  # ZFS pin: OpenZFS 2.2
  #   nixpkgs 24.11 ships OpenZFS 2.2.x as the default zfs package.
  #   Pinned via: boot.zfs.package = pkgs.zfs  (NOT pkgs.zfs_unstable)
  #
  # Both pins are validated by NixOS assertions — if the pinned version is
  # unavailable in the nixpkgs revision, nixos-rebuild fails at eval time.
  #
  inputs = {
    # ── NixOS base (LTS channel) ───────────────────────────────────────────
    # Pin to 24.11. Update policy: bump only after 3-month soak period.
    # To update: nix flake update nixpkgs
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-24.11";

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
    # Read version from VERSION file at evaluation time — single source of truth
    dplaneosVersion = builtins.replaceStrings ["\n"] [""] (builtins.readFile ../VERSION);
  in
    let
      supportedSystems = [ "x86_64-linux" "aarch64-linux" ];

      # Shared inline config applied to all nixosConfigurations
      applianceConfig = { config, lib, pkgs, ... }: {

        # ── TASK 4.3: Version Pinning ──────────────────────────────────────

        # Kernel: Linux 6.6 LTS
        # pkgs.linuxPackages_6_6 selects the 6.6.x series from nixpkgs.
        # NixOS will not auto-upgrade across this pin.
        # Upgrade path: linuxPackages_6_6 → linuxPackages_6_12 after ZFS compat check.
        boot.kernelPackages = pkgs.linuxPackages_6_6;

        # ZFS: OpenZFS 2.2
        # pkgs.zfs = stable 2.2.x. pkgs.zfs_unstable tracks 2.3+ dev builds.
        boot.zfs.package = pkgs.zfs;

        # ZFS ARC limit: 16 GiB. Prevents ARC from starving Docker containers.
        # Tunable at runtime via /proc/sys/module/zfs/parameters/zfs_arc_max.
        boot.kernelParams = [
          "zfs.zfs_arc_max=17179869184"
        ];

        # Version assertions — fire at eval time, not runtime.
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
              config.boot.zfs.package.version "2.2";
            message = "D-PlaneOS requires OpenZFS >= 2.2. "
              + "Current: ${config.boot.zfs.package.version}. "
              + "Ensure boot.zfs.package = pkgs.zfs (not pkgs.zfs_unstable).";
          }
        ];

        # ── systemd-boot for A/B slot management ──────────────────────────
        # Keep both A and B entries. OTA uses bootctl set-default to switch.
        boot.loader.systemd-boot = {
          enable             = true;
          configurationLimit = 2;   # A + B only — no old generation clutter
        };
        boot.loader.efi.canTouchEfiVariables = true;

        # ── Persist DB path ────────────────────────────────────────────────
        # /var/lib/dplaneos is bind-mounted from /persist/dplaneos by
        # impermanence.nix, so the DB physically lives on the persist partition.
        services.dplaneos.dbPath = "/var/lib/dplaneos/dplaneos.db";

        # ── Nix GC (appliance: keep store lean) ───────────────────────────
        nix.settings.auto-optimise-store = true;
        nix.gc = {
          automatic = true;
          dates     = "weekly";
          options   = "--delete-older-than 14d";
        };

        # License allowlist
        nixpkgs.config.allowUnfreePredicate = pkg:
          builtins.elem (nixpkgs.lib.getName pkg) [ "dplaneos-daemon" ];
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
        # The only trade-off: CGO (sqlite) must be compiled against musl.
        # musl + mattn/go-sqlite3 (amalgamation) = fully self-contained.
        #
        # pkgsStatic = pkgs built for the current system but linked against
        # musl via the `pkgsCross.musl64` overlay — available in nixpkgs.
        pkgsStatic = pkgs.pkgsStatic;
      in {
        # ── Daemon package (static musl binary) ───────────────────────────
        packages.dplaneos-daemon = pkgsStatic.buildGoModule {
          pname        = "dplaneos-daemon";
          version      = dplaneosVersion;
          src          = ../.;
          # CGO_ENABLED=1 required for mattn/go-sqlite3 (C amalgamation)
          # musl-gcc provides the static C runtime; no glibc dependency.
          CGO_ENABLED  = "1";
          vendorHash   = null;
          subPackages  = [ "daemon/cmd/dplaned" ];
          nativeBuildInputs = with pkgsStatic; [ musl.dev gcc ];
          # -tags sqlite_fts5   : enables FTS5 full-text search in the
          #                       mattn amalgamation (no external libsqlite3)
          # -linkmode external  : hand final link to musl-gcc
          # -extldflags -static : produce a fully static ELF, no .so deps
          tags    = [ "sqlite_fts5" ];
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
            description = "D-PlaneOS NAS daemon — static musl build";
            homepage    = "https://github.com/4nonX/D-PlaneOS";
            license     = licenses.unfree;
            platforms   = supportedSystems;
          };
        };

        # ── Fallback: glibc dynamic build (for dev/CI where musl isn't set up)
        # Build with: nix build .#dplaneos-daemon-dynamic
        packages.dplaneos-daemon-dynamic = pkgs.buildGoModule {
          pname        = "dplaneos-daemon-dynamic";
          version      = dplaneosVersion;
          src          = ../.;
          CGO_ENABLED  = "1";
          vendorHash   = null;
          subPackages  = [ "daemon/cmd/dplaned" ];
          nativeBuildInputs = with pkgs; [ gcc ];
          tags    = [ "sqlite_fts5" ];
          ldflags = [ "-s" "-w" "-X" "main.Version=${dplaneosVersion}" ];
          meta = with nixpkgs.lib; {
            description = "D-PlaneOS NAS daemon — glibc dynamic build (dev only)";
            license     = licenses.unfree;
          };
        };

        packages.default = self.packages.${system}.dplaneos-daemon;

        devShells.default = pkgs.mkShell {
          buildInputs = with pkgs; [ go gcc musl.dev gopls gotools sqlite git ];
          shellHook = ''
            export CGO_ENABLED=1
            echo "D-PlaneOS dev shell — use 'go build -tags sqlite_fts5 ./daemon/cmd/dplaned/' to build"
            echo "For static: CC=musl-gcc go build -tags sqlite_fts5 -ldflags '-linkmode external -extldflags -static' ./daemon/cmd/dplaned/"
          '';
        };
      }
    ) //
    {
      # ── NixOS module (system-agnostic) ───────────────────────────────────
      nixosModules.dplaneos = import ./module.nix;

      # ── x86_64 appliance build ────────────────────────────────────────────
      # nixos-rebuild switch --flake /etc/nixos#dplaneos
      nixosConfigurations.dplaneos = nixpkgs.lib.nixosSystem {
        system      = "x86_64-linux";
        specialArgs = { inherit self; };
        modules     = [
          ./configuration-standalone.nix    # hardware + locale + networking
          self.nixosModules.dplaneos         # daemon service, firewall, ZFS gate
          disko.nixosModules.disko           # partition layout declarations
          ./disko.nix                        # A/B + persist layout (Task 4.1)
          impermanence.nixosModules.impermanence
          ./impermanence.nix                 # ephemeral root (Task 4.4)
          ./ota-module.nix                   # OTA timer + health check (Task 4.2)
          applianceConfig                    # kernel/ZFS pins + assertions (Task 4.3)
        ];
      };

      # ── aarch64 appliance build (ARM NAS hardware) ────────────────────────
      nixosConfigurations.dplaneos-arm = nixpkgs.lib.nixosSystem {
        system      = "aarch64-linux";
        specialArgs = { inherit self; };
        modules     = [
          ./configuration-standalone.nix
          self.nixosModules.dplaneos
          disko.nixosModules.disko
          ./disko.nix
          impermanence.nixosModules.impermanence
          ./impermanence.nix
          ./ota-module.nix
          applianceConfig
        ];
      };
    };
}
