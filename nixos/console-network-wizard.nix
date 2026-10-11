# An IPv4 address from the physical console, for a system that got none (no
# DHCP server on the network): `dplaneos-console-net` asks for interface,
# address and gateway and sets them until the next restart, enough to reach
# the web interface and make the setting there.
#
# A while after the start, a system without any IPv4 address says so on the
# console. That check looks at the system's own interfaces: it does not
# contact anything, and it does not hold up the start.

{ config, lib, pkgs, ... }:

let
  cfg = config.services.dplaneos.consoleNetworkWizard;
  dplaneosConsoleNet = pkgs.writeShellApplication {
    name = "dplaneos-console-net";
    runtimeInputs = with pkgs; [ gum iproute2 coreutils util-linux ];
    text = ''
      set -euo pipefail
      if [ "$(id -u)" -ne 0 ]; then
        echo "Run as root: sudo dplaneos-console-net"
        exit 1
      fi
      gum style --bold --foreground 212 "DPlaneOS - emergency IPv4 (console)"
      gum style --width 72 "Use this if the system got no address and you cannot open the web interface. The address is set until the next restart: sign in to the web interface and enter the same values under Network to keep them."
      shopt -s nullglob
      ifaces=()
      for f in /sys/class/net/*; do
        n="''${f##*/}"
        [ "$n" = "lo" ] && continue
        ifaces+=("$n")
      done
      IFACE=""
      if [ "''${#ifaces[@]}" -gt 0 ]; then
        IFACE=$(gum choose --header "Network interface" "''${ifaces[@]}")
      fi
      [ -n "$IFACE" ] || exit 1
      CIDR=$(gum input --placeholder "IPv4 address in CIDR notation (required)")
      [ -n "$CIDR" ] || exit 1
      GW=$(gum input --placeholder "Default gateway IPv4 address (required)")
      [ -n "$GW" ] || exit 1
      ip link set "$IFACE" up || true
      ip addr flush dev "$IFACE" || true
      ip addr add "$CIDR" dev "$IFACE"
      ip route replace default via "$GW" dev "$IFACE" || ip route add default via "$GW" dev "$IFACE"
      gum style --foreground 46 "Set. Open http://''${CIDR%%/*}/ in a browser on the same network."
    '';
  };
in {
  options.services.dplaneos.consoleNetworkWizard = {
    enable = lib.mkOption {
      type = lib.types.bool;
      default = true;
      description = ''
        Physical-console network bootstrap: install `dplaneos-console-net` and optional DHCP-fail hint.
        Disable on headless-only systems if you never want wall(1) messages.
      '';
    };
    bootHintDelaySec = lib.mkOption {
      type = lib.types.int;
      default = 120;
      description = ''
        Seconds after the start before the system looks whether it has an
        IPv4 address (DHCP may take a while).
      '';
    };
    enableBootHint = lib.mkOption {
      type = lib.types.bool;
      default = true;
      description = "Say on the console when the system has no IPv4 address.";
    };
  };

  config = lib.mkIf (config.services.dplaneos.enable && cfg.enable) {
    environment.systemPackages = [ dplaneosConsoleNet ];

    # A timer, not a service that sleeps: the start of the system does not
    # wait for it.
    systemd.timers.dplaneos-console-net-hint = lib.mkIf cfg.enableBootHint {
      description = "DPlaneOS: look for an IPv4 address a while after the start";
      wantedBy = [ "timers.target" ];
      timerConfig.OnBootSec = "${toString cfg.bootHintDelaySec}s";
    };
    systemd.services.dplaneos-console-net-hint = lib.mkIf cfg.enableBootHint {
      description = "DPlaneOS: say on the console when the system has no IPv4 address";
      serviceConfig = {
        Type = "oneshot";
        ExecStart = "${pkgs.writeShellScript "dplaneos-console-net-hint" ''
          set -eu
          # Any address that is not the loopback's or a link-local one.
          if ${pkgs.iproute2}/bin/ip -4 -o addr show scope global | ${pkgs.gnugrep}/bin/grep -q inet; then
            exit 0
          fi
          msg="DPlaneOS: this system has no IPv4 address (no DHCP server answered). At this console, sign in and run: sudo dplaneos-console-net"
          echo "$msg"
          echo "$msg" | ${pkgs.util-linux}/bin/wall || true
          echo "$msg" > /dev/console 2>/dev/null || true
        ''}";
      };
    };
  };
}
