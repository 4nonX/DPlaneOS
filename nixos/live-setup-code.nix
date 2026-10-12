# DPlaneOS live image: the setup code
# ─────────────────────────────────────────────────────────────────────────────
# The live system has no administrator until someone creates one in the web
# interface, and by then it has the pools of the machine imported. Whoever
# creates the administrator must be the one who started the machine: the web
# interface asks for a code that is shown on the console (at the login
# banner, and by "cat /run/dplaneos/setup-code"). It changes at every start.
#
# An installed system does not use this: its administrator comes from the
# installer.
{ lib, pkgs, ... }:
{
  systemd.services.dplaneos-setup-code = {
    description = "Setup code for the first sign-in";
    wantedBy    = [ "multi-user.target" ];
    before      = [ "dplaned.service" "getty.target" ];
    serviceConfig = { Type = "oneshot"; RemainAfterExit = true; };
    path = [ pkgs.coreutils ];
    script = ''
      mkdir -p /run/dplaneos
      code=$(od -An -N4 -tu4 /dev/urandom | tr -d ' ')
      code=$(printf '%08d' $((code % 100000000)))
      umask 077
      printf '%s-%s\n' "''${code:0:4}" "''${code:4:4}" > /run/dplaneos/setup-code
    '';
  };
  # On the serial console and before anyone logs in, too.
  services.getty.helpLine = lib.mkAfter ''
    The setup code for the web interface: cat /run/dplaneos/setup-code
  '';
}
