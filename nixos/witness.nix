# nixos/witness.nix
# ─────────────────────────────────────────────────────────────────────────────
# DPlaneOS Witness: a small appliance that provides the third vote of a
# DPlaneOS cluster (docs/admin/THIRD-VOTE.md), as a QDevice (corosync-qnetd)
# or as a voter (corosync member). Built into ready-to-flash images:
#   nix build .#witness-sd-aarch64     Raspberry Pi 3/4 SD card image
#   nix build .#witness-image-x86_64   raw disk image (USB/SSD) and qcow2 (VMs)
#
# Joining a cluster, any of:
#   - headless: put dplaneos-witness.txt on the boot partition before the
#     first boot (node=..., code=..., mode=qdevice|voter); it joins on its own
#   - console: the wizard on tty1 asks for the same
#   - SSH: `dplaneos-witness join [--voter] https://<node> <code>`
#
# It uses the same setup script as any Linux machine (witness-setup.sh); the
# services it needs are declared here, so nothing is installed at runtime.
# ─────────────────────────────────────────────────────────────────────────────
{ config, lib, pkgs, ... }:

let
  setupScript = ../daemon/internal/quorum/witness-setup.sh;
  tools = with pkgs; [ corosync corosync-qdevice nss.tools curl openssl iproute2 gawk gnused gnugrep coreutils diffutils util-linux systemd (lib.getBin glibc) hostname ];

  witnessCli = pkgs.writeShellApplication {
    name = "dplaneos-witness";
    runtimeInputs = tools ++ [ pkgs.bash ];
    text = ''
      usage() {
        cat <<'EOF'
      dplaneos-witness status                         what this witness does now
      dplaneos-witness join [--voter] <node> <code>   join a cluster (QDevice by default)
      dplaneos-witness reset                          leave: stop the services and forget the configuration
      dplaneos-witness wizard                         interactive setup (console)
      EOF
      }

      status() {
        echo "Addresses:"; ip -o -4 addr show scope global | awk '{print "  " $2 ": " $4}'
        if [ -f /etc/dplaneos-voter.conf ]; then
          echo "Role: voter (corosync member)"
          corosync-quorumtool -s 2>/dev/null | sed 's/^/  /' || echo "  corosync is not running"
        elif [ -f /etc/corosync/qnetd/nssdb/qnetd-cacert.crt ]; then
          echo "Role: QDevice (corosync-qnetd)"
          corosync-qnetd-tool -l 2>/dev/null | sed 's/^/  /' || echo "  corosync-qnetd is not running"
        else
          echo "Role: none yet. Run 'dplaneos-witness wizard', or 'dplaneos-witness join https://<node> <code>'."
        fi
      }

      join() {
        DPLANEOS_WITNESS_IMAGE=1 sh ${setupScript} "$@"
      }

      reset() {
        systemctl stop dplaneos-voter-sync.timer corosync corosync-qnetd 2>/dev/null || true
        rm -rf /etc/dplaneos-voter.conf /var/lib/dplaneos-voter /etc/corosync/corosync.conf /etc/corosync/authkey /etc/corosync/qnetd
        echo "This witness no longer votes for any cluster. Remove it from the cluster's member list (or remove the QDevice) on a node."
      }

      wizard() {
        clear || true
        echo "DPlaneOS Witness"
        echo "================"
        status
        echo
        echo "On a cluster node, open System > High Availability > Add a third vote."
        echo "It shows the node's address and a one-time code."
        echo
        printf "Role: [1] QDevice (recommended for two nodes)  [2] Voter (full member, same LAN)  [Enter] skip: "
        read -r role || return 0
        case "$role" in 1|"q"*) mode="" ;; 2|"v"*) mode="--voter" ;; *) return 0 ;; esac
        printf "Node address (e.g. https://nas1.lan): "; read -r node
        printf "Code (dpq_...): "; read -r code
        [ -n "$node" ] && [ -n "$code" ] || { echo "Both are needed."; return 1; }
        # shellcheck disable=SC2086
        join $mode "$node" "$code" || { echo "Joining failed (see above)."; return 1; }
        echo; status
      }

      case "''${1:-}" in
        status) status ;;
        join) shift; join "$@" ;;
        reset) reset ;;
        wizard) wizard ;;
        *) usage; exit 1 ;;
      esac
    '';
  };

  # First boot without a screen: join from dplaneos-witness.txt on the boot
  # partition (FAT, writable from any computer).
  autojoin = pkgs.writeShellApplication {
    name = "dplaneos-witness-autojoin";
    runtimeInputs = tools ++ [ witnessCli ];
    text = ''
      f=""
      for p in /boot/firmware/dplaneos-witness.txt /boot/dplaneos-witness.txt; do
        [ -f "$p" ] && { f="$p"; break; }
      done
      [ -n "$f" ] || exit 0
      val() { sed -n "s/^[[:space:]]*$1[[:space:]]*=[[:space:]]*//p" "$f" | tr -d '\r' | head -n1; }
      node=$(val node); code=$(val code); mode=$(val mode); host=$(val hostname)
      if [ -z "$node" ] || [ -z "$code" ]; then
        echo "dplaneos-witness.txt needs node= and code=" | tee "$f.error"; exit 0
      fi
      [ -n "$host" ] && hostname "$host"
      args=()
      [ "$mode" = voter ] && args+=(--voter)
      # The network may need a moment after boot; the code is valid for 30 minutes.
      for _ in $(seq 1 60); do
        if dplaneos-witness join "''${args[@]}" "$node" "$code" > "$f.log" 2>&1; then
          mv "$f" "$f.done"; rm -f "$f.error"; exit 0
        fi
        grep -q "invalid, expired or already used" "$f.log" && break
        sleep 10
      done
      cp "$f.log" "$f.error"; exit 0
    '';
  };
in
{
  networking.hostName = lib.mkDefault "dplane-witness";
  networking.firewall.allowedTCPPorts = [ 5403 ];  # corosync-qnetd (QDevice)
  networking.firewall.allowedUDPPorts = [ 5405 ];  # corosync (voter)
  networking.useDHCP = lib.mkDefault true;

  environment.systemPackages = tools ++ [ witnessCli ];

  # QDevice: the vote server. Runs once its certificate authority exists
  # (created by the setup script).
  systemd.services.corosync-qnetd = {
    description = "Corosync QDevice network daemon (DPlaneOS Witness)";
    wantedBy = [ "multi-user.target" ];
    after = [ "network-online.target" ];
    wants = [ "network-online.target" ];
    unitConfig.ConditionPathExists = "/etc/corosync/qnetd/nssdb/qnetd-cacert.crt";
    path = with pkgs; [ corosync-qdevice nss.tools ];
    serviceConfig = {
      ExecStart = "${pkgs.corosync-qdevice}/bin/corosync-qnetd -f";
      RuntimeDirectory = "corosync-qnetd";
      Restart = "on-failure";
    };
  };

  # Voter: a corosync member. Runs once it has a configuration.
  systemd.services.corosync = {
    description = "Corosync cluster engine (DPlaneOS Witness voter)";
    wantedBy = [ "multi-user.target" ];
    after = [ "network-online.target" ];
    wants = [ "network-online.target" ];
    unitConfig.ConditionPathExists = "/etc/corosync/corosync.conf";
    serviceConfig = {
      ExecStart = "${pkgs.corosync}/bin/corosync -f";
      StateDirectory = "corosync";
      LogsDirectory = "cluster";
      Restart = "on-failure";
    };
  };
  systemd.services.dplaneos-voter-sync = {
    description = "DPlaneOS voter: pull the cluster configuration";
    unitConfig.ConditionPathExists = "/etc/dplaneos-voter.conf";
    serviceConfig = { Type = "oneshot"; ExecStart = "/var/lib/dplaneos-voter/sync"; };
  };
  systemd.timers.dplaneos-voter-sync = {
    wantedBy = [ "timers.target" ];
    timerConfig = { OnBootSec = "30s"; OnUnitActiveSec = "60s"; };
  };

  systemd.services.dplaneos-witness-autojoin = {
    description = "DPlaneOS Witness: join a cluster from dplaneos-witness.txt on the boot partition";
    wantedBy = [ "multi-user.target" ];
    after = [ "network-online.target" ];
    wants = [ "network-online.target" ];
    serviceConfig = { Type = "oneshot"; ExecStart = "${autojoin}/bin/dplaneos-witness-autojoin"; };
  };

  # Console: the wizard on tty1 until a role is configured, then the status.
  services.getty.autologinUser = lib.mkDefault "root";
  programs.bash.loginShellInit = ''
    if [ "$(tty)" = /dev/tty1 ]; then
      if [ ! -f /etc/dplaneos-voter.conf ] && [ ! -f /etc/corosync/qnetd/nssdb/qnetd-cacert.crt ]; then
        dplaneos-witness wizard || true
      else
        dplaneos-witness status
      fi
    fi
  '';

  # SSH with keys only; a key can be put on the boot partition
  # (dplaneos-witness-ssh.pub) and is installed at boot.
  services.openssh = {
    enable = true;
    settings = { PasswordAuthentication = false; PermitRootLogin = "prohibit-password"; };
  };
  systemd.services.dplaneos-witness-sshkey = {
    description = "DPlaneOS Witness: install an SSH key from the boot partition";
    wantedBy = [ "multi-user.target" ];
    before = [ "sshd.service" ];
    serviceConfig.Type = "oneshot";
    script = ''
      for p in /boot/firmware/dplaneos-witness-ssh.pub /boot/dplaneos-witness-ssh.pub; do
        if [ -f "$p" ]; then
          install -d -m 0700 /root/.ssh
          tr -d '\r' < "$p" >> /root/.ssh/authorized_keys
          chmod 0600 /root/.ssh/authorized_keys
          mv "$p" "$p.installed"
        fi
      done
    '';
  };

  services.timesyncd.enable = lib.mkDefault true;
  documentation.enable = lib.mkDefault false;
  system.stateVersion = "26.05";
}
