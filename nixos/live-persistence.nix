# D-PlaneOS Live Boot: Persistence Layer
# ─────────────────────────────────────────────────────────────────────────────
# The live system runs from memory: nothing it is told is kept.
#
#   - Root (/) is tmpfs: everything is lost at shutdown
#   - /persist is tmpfs too; the directories the modules expect to be
#     persistent are bound from there, so that the same modules work here
#     and on an installed system
#
# The pools on the machine's disks are not part of this: what is written to
# them stays. To keep settings, install the system.
#
# (An earlier version mounted a USB drive labelled "dplane-persist" and
# linked a directory on it that nothing used: the settings were lost all the
# same. That is gone, with the promise.)

{ config, lib, pkgs, ... }:

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
  # These directories live in the tmpfs /persist.

  environment.persistence."/persist" = {
    # Directories that must be persistent across sessions
    directories = [
      # User/group database (impermanence requires this for stable UIDs/GIDs)
      # Without this, users assigned ephemeral IDs will be reassigned on reboot
      "/var/lib/nixos"

      # System state (created by services at boot)
      "/var/cache"
      "/var/log"
    ];

    # Critical files
    files = [
      # Machine identity (systemd requires this as a FILE, not a directory)
      "/etc/machine-id"

      # SSH host keys (if user wants to preserve identity)
      # Note: /etc/ssh is typically read-only from squashfs;
      # this is a fallback for future install-to-disk scenarios
      # "/etc/ssh/ssh_host_rsa_key"
      # "/etc/ssh/ssh_host_ed25519_key"
    ];
  };

  # ── Mark the system as live (for debugging and monitoring) ───────────────
  environment.etc."dplaneos-live/mode".text = "live-boot";
}
