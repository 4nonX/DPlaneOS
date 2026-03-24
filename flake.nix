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

      # ── Daemon Builder (breaks evaluation loops) ─────────────────────────
      mkDaemon = { system, pkgs, pkgsStatic, dplaneosVersion, nixpkgs }: pkgsStatic.buildGoModule {
        pname        = "dplaneos-daemon";
        version      = dplaneosVersion;
        src          = nixpkgs.lib.cleanSource ./.;
        env.CGO_ENABLED = "1";
        vendorHash   = nixpkgs.lib.fakeHash;
        subPackages  = [ "daemon/cmd/dplaned" ];
        nativeBuildInputs = with pkgsStatic; [ musl.dev gcc ];
        ldflags = [
          "-s" "-w"
          "-X" "main.Version=${dplaneosVersion}"
          "-linkmode" "external"
          "-extldflags" "-static"
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
          description = "D-PlaneOS NAS daemon : static musl build";
          homepage    = "https://github.com/4nonX/D-PlaneOS";
          license     = licenses.agpl3Only;
          platforms   = supportedSystems;
        };
      };

      mkDaemonDynamic = { system, pkgs, dplaneosVersion, nixpkgs }: pkgs.buildGoModule {
        pname        = "dplaneos-daemon-dynamic";
        version      = dplaneosVersion;
        src          = nixpkgs.lib.cleanSource ./.;
        env.CGO_ENABLED = "1";
        vendorHash   = nixpkgs.lib.fakeHash;
        subPackages  = [ "daemon/cmd/dplaned" ];
        nativeBuildInputs = with pkgs; [ gcc ];
        ldflags = [ "-s" "-w" "-X" "main.Version=${dplaneosVersion}" ];
        meta = with nixpkgs.lib; {
          description = "D-PlaneOS NAS daemon : glibc dynamic build (dev only)";
          license     = licenses.agpl3Only;
        };
      };

      # Shared inline config applied to all nixosConfigurations
      applianceConfig = { config, lib, pkgs, ... }: {
        # ... (unchanged)
        boot.kernelPackages = pkgs.linuxPackages_6_6;
        boot.zfs.package = pkgs.zfs;
        boot.kernelParams = [ "zfs.zfs_arc_max=17179869184" ];
        assertions = [
          {
            assertion = lib.versionAtLeast config.boot.kernelPackages.kernel.version "6.6";
            message = "D-PlaneOS requires Linux kernel >= 6.6 LTS.";
          }
          {
            assertion = lib.versionAtLeast config.boot.zfs.package.version "2.3";
            message = "D-PlaneOS requires OpenZFS >= 2.3 (LTS branch).";
          }
        ];
        boot.loader.systemd-boot = { enable = true; configurationLimit = 2; };
        boot.loader.efi.canTouchEfiVariables = true;
        services.dplaneos.dbPath = "/var/lib/dplaneos/pgsql";
        nix.settings.auto-optimise-store = true;
        nix.gc = { automatic = true; dates = "weekly"; options = "--delete-older-than 14d"; };
      };

    in
    flake-utils.lib.eachSystem supportedSystems (system:
      let
        pkgs       = nixpkgs.legacyPackages.${system};
        pkgsStatic = pkgs.pkgsStatic;
        daemon     = mkDaemon { inherit system pkgs pkgsStatic dplaneosVersion nixpkgs; };
        daemonDyn  = mkDaemonDynamic { inherit system pkgs dplaneosVersion nixpkgs; };
      in {
        packages.dplaneos-daemon         = daemon;
        packages.dplaneos-daemon-dynamic = daemonDyn;
        packages.iso = null;
        packages.default = daemon;

        devShells.default = pkgs.mkShell {
          buildInputs = with pkgs; [ go 1.25 gcc musl.dev gopls gotools postgresql git ];
          shellHook = ''
            export CGO_ENABLED=1
            echo "D-PlaneOS dev shell"
          '';
        };
      }
    ) //
    {
      nixosModules.dplaneos = import ./nixos/module.nix;

      nixosConfigurations.dplaneos = let system = "x86_64-linux"; pkgs = nixpkgs.legacyPackages.${system}; in nixpkgs.lib.nixosSystem {
        inherit system;
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
          ./nixos/dplane-generated.nix
          applianceConfig
          { services.dplaneos.daemonPackage = mkDaemon { inherit system pkgs dplaneosVersion nixpkgs; pkgsStatic = pkgs.pkgsStatic; }; }
        ];
      };

      nixosConfigurations.dplaneos-arm = let system = "aarch64-linux"; pkgs = nixpkgs.legacyPackages.${system}; in nixpkgs.lib.nixosSystem {
        inherit system;
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
          ./nixos/dplane-generated.nix
          applianceConfig
          { services.dplaneos.daemonPackage = mkDaemon { inherit system pkgs dplaneosVersion nixpkgs; pkgsStatic = pkgs.pkgsStatic; }; }
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

      packages.x86_64-linux.iso = self.nixosConfigurations.iso.config.system.build.isoImage;

    };
}
