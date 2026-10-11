# nixos/samba-package.nix
# ─────────────────────────────────────────────────────────────────────────────
# The Samba of the appliance: with Active Directory support.
#
# The Samba in nixpkgs is built without LDAP and with that without ADS: it
# serves shares to local accounts, but cannot become a member of a domain
# (`net ads join` answers "ADS support not compiled in"). The full build in
# nixpkgs (sambaFull) can, and brings a domain controller, Ceph, CUPS and
# more with it: too much for an installation image.
#
# This build is not in the public binary cache; CI keeps it in its own
# (.github/actions/samba-ads).
# ─────────────────────────────────────────────────────────────────────────────
pkgs: pkgs.samba.override { enableLDAP = true; }
