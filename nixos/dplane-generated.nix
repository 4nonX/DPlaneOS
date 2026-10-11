# dplane-generated.nix
# ──────────────────────────────────────────────────────────────────────────────
# DPlaneOS v5.0 - JSON-to-Nix Bridge
#
# THIS FILE IS STATIC. The DPlaneOS daemon never modifies it.
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

in
{
  # ── SSH Daemon ───────────────────────────────────────────────────────────────
  services.openssh.ports           = lib.mkIf (s ? ssh_port) [ s.ssh_port ];
  services.openssh.settings.PasswordAuthentication =
    lib.mkIf (s ? ssh_password_auth) s.ssh_password_auth;
  services.openssh.settings.PermitRootLogin =
    lib.mkIf (s ? ssh_permit_root_login) s.ssh_permit_root_login;

  # ── UPS shutdown policy (UPS page; hardware in services.dplaneos.ups) ────────
  services.dplaneos.ups.lowBattery = lib.mkIf (s ? ups_low_battery) s.ups_low_battery;
  services.dplaneos.ups.finalDelay = lib.mkIf (s ? ups_final_delay) s.ups_final_delay;
  services.dplaneos.ups.action     = lib.mkIf (s ? ups_action) s.ups_action;
}
