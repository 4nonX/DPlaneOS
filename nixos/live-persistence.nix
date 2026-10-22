# D-PlaneOS Live Boot: Persistence Layer
# ─────────────────────────────────────────────────────────────────────────────
# Configure ephemeral root with optional persistence to external storage.
#
# By default:
#   - Root (/) is tmpfs: everything lost on shutdown
#   - System state (/var) is tmpfs
#   - Logs (/var/log) are tmpfs
#   - Tmp (/tmp) is tmpfs
#
# Optional persistence: an ext4 drive labelled "dplane-persist" that is
# attached at boot holds the daemon's state (dplane-link-persist below):
#   /var/lib/dplaneos   database, settings, secrets key, firewall rules
#   /etc/dplaneos       daemon configuration
#   /etc/target         iSCSI targets
#   network/            the 05-dplane-* files of /etc/systemd/network
# Without the drive the system runs from RAM as before.

{ config, lib, pkgs, ... }:

let
  # Directories kept on the drive, bind-mounted over the RAM copies.
  persistedDirs = [ "/var/lib/dplaneos" "/etc/dplaneos" "/etc/target" ];
  usb   = "/mnt/usb-persist";
  state = "${usb}/dplaneos-state";
in
{
  # ── Root filesystem: ephemeral tmpfs for live boot ───────────────────────
  # Live boot runs entirely in RAM. Root is tmpfs (or overlay on squashfs).
  fileSystems."/" = {
    fsType = "tmpfs";
    options = [ "size=50%" ];
  };

  # ── Ephemeral /persist directory (tmpfs for daemon state) ──────────────────
  # Live boot always uses tmpfs for /persist (no persistent partition).
  fileSystems."/persist" = {
    fsType = "tmpfs";
    neededForBoot = true;
    options = [
      "size=2G"          # Limit tmpfs to 2GB (sufficient for daemon state)
      "mode=755"         # Standard permissions
    ];
  };

  # ── Declare persistent directories (impermanence) ────────────────────────
  # /persist is tmpfs here: these survive only until shutdown.
  environment.persistence."/persist" = {
    directories = [
      # User/group database (impermanence requires this for stable UIDs/GIDs)
      "/var/lib/nixos"
      "/var/cache"
      "/var/log"
    ];
    files = [
      # Machine identity (systemd requires this as a FILE, not a directory)
      "/etc/machine-id"
    ];
  };

  # ── The persistence drive ────────────────────────────────────────────────
  # Until 16.0 the drive was mounted and a link /var/lib/dplaneos-persist
  # made to it, which nothing used: the database and settings stayed in RAM
  # with the drive attached. Now the drive's directories are bind-mounted
  # over the state directories before anything reads them (firewall rules,
  # networkd, PostgreSQL, the daemon).
  systemd.services.dplane-link-persist = {
    description = "Keep D-PlaneOS state on the dplane-persist drive (if attached)";
    path = [ pkgs.coreutils pkgs.util-linux pkgs.systemd ];

    unitConfig.DefaultDependencies = false;
    after     = [ "local-fs.target" "systemd-udev-trigger.service" ];
    before    = [ "network-pre.target" "dplaneos-firewall.service" "systemd-networkd.service"
                  "postgresql.service" "dplaned.service" "shutdown.target" ];
    conflicts = [ "shutdown.target" ];
    wantedBy  = [ "multi-user.target" "postgresql.service" "dplaned.service" ];

    serviceConfig = {
      Type = "oneshot";
      RemainAfterExit = true;
    };

    script = ''
      dev=/dev/disk/by-label/dplane-persist
      # USB drives can take a few seconds to appear.
      for _ in $(seq 1 20); do [ -e "$dev" ] && break; sleep 0.5; done
      if [ ! -e "$dev" ]; then
        echo "no drive labelled dplane-persist: state is kept in RAM until shutdown"
        touch /run/dplane-persist-ephemeral
        exit 0
      fi

      mkdir -p ${usb}
      mountpoint -q ${usb} || mount -o noatime "$dev" ${usb}
      ${lib.concatMapStrings (d: ''
        mkdir -p "${state}${d}" "${d}"
        mountpoint -q "${d}" || mount --bind "${state}${d}" "${d}"
      '') persistedDirs}
      # Owners and modes of the state directories (first use of a drive).
      systemd-tmpfiles --create ${lib.concatMapStringsSep " " (d: "--prefix=${d}") persistedDirs}

      # networkd files: copied, not mounted (a mount over the directory
      # hides the NixOS files in it); saved back by dplane-persist-network.
      mkdir -p ${state}/network
      for f in ${state}/network/05-dplane-*; do
        if [ -e "$f" ]; then cp -a "$f" /etc/systemd/network/; fi
      done

      echo "state is kept on $dev"
      touch /run/dplane-persist-available
    '';
  };

  # Saves the daemon's network files to the drive whenever they change.
  systemd.paths.dplane-persist-network = {
    wantedBy = [ "multi-user.target" ];
    after    = [ "dplane-link-persist.service" ];
    unitConfig.ConditionPathExists = "/run/dplane-persist-available";
    pathConfig.PathChanged = "/etc/systemd/network";
  };
  systemd.services.dplane-persist-network = {
    description = "Save D-PlaneOS network settings to the dplane-persist drive";
    unitConfig.ConditionPathExists = "/run/dplane-persist-available";
    serviceConfig.Type = "oneshot";
    path = [ pkgs.coreutils ];
    script = ''
      mkdir -p ${state}/network
      rm -f ${state}/network/05-dplane-*
      for f in /etc/systemd/network/05-dplane-*; do
        if [ -e "$f" ]; then cp -a "$f" ${state}/network/; fi
      done
      sync -f ${state}/network
    '';
  };

  # ── Mark system as live boot (useful for debugging/monitoring) ──────────
  environment.etc."dplaneos-live/mode".text = "live-boot";
  environment.etc."dplaneos-live/persist-status".source = "/run/dplane-persist-available";

  systemd.services.dplaned = lib.mkIf (config.services.dplaneos.enable or false) {
    after = [ "dplane-link-persist.service" ];
  };
  systemd.services.postgresql.after = [ "dplane-link-persist.service" ];
}
