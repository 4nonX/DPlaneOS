# dplane-generated.nix
# ──────────────────────────────────────────────────────────────────────────────
# DPlaneOS v5.0 - JSON-to-Nix Bridge
#
# THIS FILE IS STATIC. The DPlaneOS daemon never modifies it.
# A module of the release (flake.nix). Do not edit on a system.
#
# Architecture:
#   The daemon writes /var/lib/dplaneos/dplane-state.json (pure JSON).
#   This file reads that JSON at nixos-rebuild eval time via builtins.fromJSON.
#   The .nix file contains ZERO interpolated or generated syntax - no Surgeon,
#   no string templates, no injection risk of any kind.
#
# On nixos-rebuild, NixOS evaluates this file and applies:
#   - Hostname, timezone, NTP
#   - Firewall port lists
#   - Samba global settings
#
# All keys use `s.key or default` guards so missing JSON keys fall through to
# the NixOS defaults defined in configuration.nix. No key is ever required.
# ──────────────────────────────────────────────────────────────────────────────

{ config, lib, pkgs, ... }:

let
  stateFile = /var/lib/dplaneos/dplane-state.json;

  # Read the JSON state file if it exists.
  # If the file is missing (fresh install before first daemon write), return an
  # empty attrset so all `s.key or default` guards produce their defaults.
  s = if builtins.pathExists stateFile
        then builtins.fromJSON (builtins.readFile stateFile)
        else {};

  # Values set in the web UI win over the defaults of the configuration and
  # the modules (priority 90; lib.mkForce in a local module still wins). At
  # the same priority (as before) NixOS refused to evaluate: setting the
  # hostname, time zone, SSH options, Samba workgroup or a sysctl in the UI
  # made every rebuild fail with "conflicting definition values".
  ov = lib.mkOverride 90;

  # A pure evaluation cannot read the state file (pathExists is false), so a
  # rebuild without --impure silently drops every setting made in the UI.
  pure = !(builtins ? currentTime);

in
{
  warnings = lib.optional pure
    "DPlaneOS: pure evaluation, settings made in the web UI (/var/lib/dplaneos/dplane-state.json) are not applied. On an installed system rebuild with --impure (image and CI builds can ignore this).";

  # ── System ──────────────────────────────────────────────────────────────────
  networking.hostName    = lib.mkIf (s ? hostname) (ov s.hostname);
  time.timeZone          = lib.mkIf (s ? timezone) (ov s.timezone);

  # ── NTP ─────────────────────────────────────────────────────────────────────
  # DNS servers, addresses, bonds and VLANs are not here: the daemon writes
  # them as networkd files (/etc/systemd/network/05-dplane-*,
  # /etc/systemd/resolved.conf.d/50-dplane.conf), applied without a rebuild.
  services.timesyncd.servers    = s.ntp_servers or [];

  # ── Firewall ────────────────────────────────────────────────────────────────
  # Only override if the daemon has explicitly set these lists.
  # If absent, NixOS keeps whatever configuration.nix declared.
  networking.firewall.allowedTCPPorts = lib.mkIf (s ? firewall_tcp) s.firewall_tcp;
  networking.firewall.allowedUDPPorts = lib.mkIf (s ? firewall_udp) s.firewall_udp;

  # ── Network interfaces (networkd; files written by the daemon) ──────────────
  systemd.network.enable   = true;

  # ── Samba global settings ────────────────────────────────────────────────────
  # These feed into modules/samba.nix via the services.dplaneos.samba options.
  # Only applied when samba is enabled (samba.nix config = lib.mkIf cfg.enable {...})
  services.dplaneos.samba = lib.mkIf (s ? samba_workgroup) {
    workgroup         = ov (s.samba_workgroup      or "WORKGROUP");
    serverString      = ov (s.samba_server_string  or "DPlaneOS NAS");
    timeMachine       = ov (s.samba_time_machine   or false);
    allowGuest        = ov (s.samba_allow_guest    or false);
    extraGlobalConfig = ov (s.samba_extra_global   or "");
    securityMode      = ov (s.samba_security_mode  or "user");
    realm             = ov (s.samba_realm          or null);
    domainController  = ov (s.samba_domain_controller or null);
    # Multi-forest IDMAP: list of { name, backend, low, high } attrsets.
    # samba.nix translates each entry into "idmap config <name> : ..." lines.
    idmapDomains      = ov (s.idmap_domains        or []);
  };

  # ── SSH Daemon ───────────────────────────────────────────────────────────────
  services.openssh.ports           = lib.mkIf (s ? ssh_port) (ov [ s.ssh_port ]);
  services.openssh.settings.PasswordAuthentication =
    lib.mkIf (s ? ssh_password_auth) (ov s.ssh_password_auth);
  services.openssh.settings.PermitRootLogin =
    lib.mkIf (s ? ssh_permit_root_login) (ov s.ssh_permit_root_login);

  # ── Kernel tuning (Settings → Tuning) ────────────────────────────────────────
  # mkAfter: a later zfs.zfs_arc_max on the command line wins over the default.
  boot.kernelParams = lib.mkIf (s ? zfs_arc_max)
    (lib.mkAfter [ "zfs.zfs_arc_max=${toString s.zfs_arc_max}" ]);
  boot.kernel.sysctl = lib.mapAttrs (_: ov) (s.sysctl or {});

  # ── Web UI TLS (Certificates → Activate) ─────────────────────────────────────
  services.nginx.virtualHosts."_" = lib.mkIf (s ? tls_cert && s ? tls_key) {
    addSSL            = true;
    sslCertificate    = s.tls_cert;
    sslCertificateKey = s.tls_key;
  };

  # ── UPS shutdown policy (UPS page; hardware in services.dplaneos.ups) ────────
  services.dplaneos.ups.lowBattery = lib.mkIf (s ? ups_low_battery) s.ups_low_battery;
  services.dplaneos.ups.finalDelay = lib.mkIf (s ? ups_final_delay) s.ups_final_delay;
  services.dplaneos.ups.action     = lib.mkIf (s ? ups_action) s.ups_action;
}
