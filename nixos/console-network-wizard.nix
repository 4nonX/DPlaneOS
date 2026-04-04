# Interactive static IPv4 on the physical console when DHCP does not provide connectivity.
# Installs `dplaneos-console-net` (gum TUI) and an optional boot-time wall hint.

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
      gum style --bold --foreground 212 "D-PlaneOS — emergency IPv4 (console)"
      gum style --width 72 "Use this if DHCP failed and you cannot open the web UI. Applies now with ip(8); persist the same values in Settings → Network after login."
      IFACE=$(gum choose --header "Network interface" $(ls /sys/class/net | grep -v '^lo$' || true))
      [ -n "$IFACE" ] || exit 1
      CIDR=$(gum input --placeholder "IPv4 address in CIDR notation (required)")
      [ -n "$CIDR" ] || exit 1
      GW=$(gum input --placeholder "Default gateway IPv4 address (required)")
      [ -n "$GW" ] || exit 1
      ip link set "$IFACE" up || true
      ip addr flush dev "$IFACE" || true
      ip addr add "$CIDR" dev "$IFACE"
      ip route replace default via "$GW" dev "$IFACE" || ip route add default via "$GW" dev "$IFACE"
      if command -v resolvectl >/dev/null 2>&1; then
        resolvectl dns "$IFACE" 1.1.1.1 8.8.8.8 2>/dev/null || true
      fi
      gum style --foreground 46 "Applied. Try: ping -c2 1.1.1.1  then open the UI in your browser."
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
      default = 180;
      description = ''
        Seconds after boot before checking outbound IPv4; if check fails, broadcast a wall hint
        to all logged-in users to run `sudo dplaneos-console-net`.
      '';
    };
    enableBootHint = lib.mkOption {
      type = lib.types.bool;
      default = true;
      description = "Run outbound connectivity check and wall(1) hint when ping fails.";
    };
  };

  config = lib.mkIf (config.services.dplaneos.enable && cfg.enable) {
    environment.systemPackages = [ dplaneosConsoleNet ];

    systemd.services.dplaneos-console-net-hint = lib.mkIf cfg.enableBootHint {
      description = "D-PlaneOS: hint for console static IP if no default IPv4";
      wantedBy = [ "multi-user.target" ];
      after = [ "network.target" ];
      serviceConfig = {
        Type = "oneshot";
        RemainAfterExit = true;
      };
      script = ''
        set -eu
        delay=${toString cfg.bootHintDelaySec}
        echo "dplaneos-console-net-hint: waiting ''${delay}s then probing connectivity..."
        sleep "$delay"
        if ${lib.getBin pkgs.iputils}/bin/ping -c1 -W4 1.1.1.1 >/dev/null 2>&1; then
          exit 0
        fi
        msg="D-PlaneOS: no outbound IPv4 detected. At the physical console run: sudo dplaneos-console-net"
        echo "$msg" | ${lib.getBin pkgs.util-linux}/bin/wall || true
      '';
    };
  };
}
