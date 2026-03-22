# dplane-generated.nix
# ──────────────────────────────────────────────────────────────────────────────
# D-PlaneOS v5.0 - JSON-to-Nix Bridge
#
# THIS FILE IS STATIC. The D-PlaneOS daemon never modifies it.
# Installed once by setup-nixos.sh. Do not edit manually.
#
# Architecture:
#   The daemon writes /var/lib/dplaneos/dplane-state.json (pure JSON).
#   This file reads that JSON at nixos-rebuild eval time via builtins.fromJSON.
#   The .nix file contains ZERO interpolated or generated syntax - no Surgeon,
#   no string templates, no injection risk of any kind.
#
# On nixos-rebuild, NixOS evaluates this file and applies:
#   - Hostname, timezone, DNS, NTP
#   - Static IPs, bond interfaces, VLANs
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

  # ── Network helpers ─────────────────────────────────────────────────────────

  # Build systemd.network.networks attrset from network_statics.
  # Each key in s.network_statics becomes a "20-<iface>-static" network unit.
  mkStaticNetworks = statics:
    lib.mapAttrs' (iface: cfg: {
      name  = "20-${iface}-static";
      value = {
        matchConfig.Name    = iface;
        networkConfig.DHCP  = "no";
        address             = [ cfg.cidr ];
        routes = lib.optionals (cfg.gateway != "") [{
          routeConfig.Gateway = cfg.gateway;
        }];
      };
    }) statics;

  # Build systemd.network.netdevs and .networks attrsets from network_bonds.
  # Returns { netdevs = { ... }; networks = { ... }; }
  mkBondNetdevs = bonds:
    lib.foldlAttrs (acc: name: cfg: {
      netdevs  = acc.netdevs // {
        "10-${name}" = {
          netdevConfig = { Name = name; Kind = "bond"; };
          bondConfig.Mode = cfg.mode;
        };
      };
      networks = acc.networks // lib.listToAttrs (lib.imap0 (i: slave: {
        name  = "2${toString (0 + i)}-${slave}-bond";
        value = {
          matchConfig.Name   = slave;
          networkConfig.Bond = name;
        };
      }) cfg.slaves);
    }) { netdevs = {}; networks = {}; } bonds;

  # Build systemd.network.netdevs and .networks from network_vlans.
  mkVLANNetdevs = vlans:
    lib.foldlAttrs (acc: ifName: cfg: {
      netdevs  = acc.netdevs // {
        "10-${ifName}" = {
          netdevConfig = { Name = ifName; Kind = "vlan"; };
          vlanConfig.Id = cfg.vid;
        };
      };
      networks = acc.networks // {
        "10-${cfg.parent}-vlan" = {
          matchConfig.Name        = cfg.parent;
          networkConfig.VLAN      = [ ifName ];
        };
      };
    }) { netdevs = {}; networks = {}; } vlans;

  # Compute all network units
  staticNets  = mkStaticNetworks (s.network_statics or {});
  bondResult  = mkBondNetdevs    (s.network_bonds   or {});
  vlanResult  = mkVLANNetdevs    (s.network_vlans   or {});

in
{
  # ── System ──────────────────────────────────────────────────────────────────
  networking.hostName    = s.hostname or config.networking.hostName;
  time.timeZone          = s.timezone or config.time.timeZone;

  # ── DNS / NTP ───────────────────────────────────────────────────────────────
  networking.nameservers        = s.dns_servers or [];
  services.timesyncd.servers    = s.ntp_servers or [];

  # ── Firewall ────────────────────────────────────────────────────────────────
  # Only override if the daemon has explicitly set these lists.
  # If absent, NixOS keeps whatever configuration.nix declared.
  networking.firewall.allowedTCPPorts = lib.mkIf (s ? firewall_tcp) s.firewall_tcp;
  networking.firewall.allowedUDPPorts = lib.mkIf (s ? firewall_udp) s.firewall_udp;

  # ── Network interfaces ──────────────────────────────────────────────────────
  systemd.network.enable   = true;
  systemd.network.networks = staticNets // bondResult.networks // vlanResult.networks;
  systemd.network.netdevs  = bondResult.netdevs // vlanResult.netdevs;

  # ── Samba global settings ────────────────────────────────────────────────────
  # These feed into modules/samba.nix via the services.dplaneos.samba options.
  # Only applied when samba is enabled (samba.nix config = lib.mkIf cfg.enable {...})
  services.dplaneos.samba = lib.mkIf (s ? samba_workgroup) {
    workgroup         = s.samba_workgroup      or "WORKGROUP";
    serverString      = s.samba_server_string  or "D-PlaneOS NAS";
    timeMachine       = s.samba_time_machine   or false;
    allowGuest        = s.samba_allow_guest    or false;
    extraGlobalConfig = s.samba_extra_global   or "";
  };

  # ── High Availability ────────────────────────────────────────────────────────
  services.dplaneos.ha.enable = s.ha_enable or false;
}
