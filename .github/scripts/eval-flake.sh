#!/bin/bash
# Evaluates every flake output without building, one output per Nix process.
# `nix flake check --no-build` does the same in a single process, which keeps
# every evaluated system in memory: with the VM checks that peaked at 14 GB,
# and the 16 GB runner killed it (exit 143).
set -euo pipefail
FLAKE="${1:-.}"
SYSTEM=x86_64-linux

names() { # attribute set -> its names, one per line
  nix eval --raw "$FLAKE#$1" --apply 's: builtins.concatStringsSep "\n" (builtins.attrNames s)'
}
drv() { # attribute path
  echo "evaluating $1"
  nix eval --raw "$FLAKE#$1" > /dev/null
}

# The flake's structure and its modules.
nix flake show "$FLAKE" > /dev/null

# The packages the docs name are there (a shallow merge of the per-system
# outputs once dropped them all).
for p in dplaneos-daemon dplaneos-daemon-cgo dplaneos-frontend iso iso-live default; do
  names "packages.$SYSTEM" | grep -qx "$p" || { echo "ERROR: packages.$SYSTEM.$p is missing"; exit 1; }
done

# The daemon ships with the Go of daemon/go.mod's toolchain line, the one CI
# tests and scans with govulncheck (nixpkgs' Go once lagged and the scan saw
# none of the standard library vulnerabilities in the shipped daemon).
want=$(sed -n 's/^toolchain go//p' "$FLAKE/daemon/go.mod")
for sys in x86_64-linux aarch64-linux; do
  for p in dplaneos-daemon dplaneos-daemon-cgo; do
    got=$(nix eval --raw "$FLAKE#packages.$sys.$p.go.version")
    [ "$got" = "$want" ] || { echo "ERROR: $sys $p is built with Go $got, daemon/go.mod says go$want"; exit 1; }
  done
done
echo "daemon built with Go $want"

# The OS packages come from the pinned nixpkgs: a pin far behind the channel
# ships months of missing security fixes.
# The node of the flake's own nixpkgs input (other inputs bring their own).
node() { nix flake metadata --json "$FLAKE" | python3 -c 'import json,sys; l=json.load(sys.stdin)["locks"]; n=l["nodes"][l["nodes"][l["root"]]["inputs"]["nixpkgs"]]; print(n["locked"]["lastModified"] if sys.argv[1] == "time" else "github:%(owner)s/%(repo)s/%(ref)s" % n["original"])' "$1"; }
pinned=$(node time)
channel=$(nix flake metadata --json "$(node ref)" | python3 -c 'import json,sys; print(json.load(sys.stdin)["lastModified"])')
behind=$(( (channel - pinned) / 86400 ))
[ "$behind" -le 30 ] || { echo "ERROR: the nixpkgs pin is $behind days behind its channel; run: nix flake update nixpkgs"; exit 1; }
echo "nixpkgs pin is $behind days behind its channel"

count=0
for kind in packages checks devShells; do
  list=$(names "$kind.$SYSTEM") # a failure here stops the script (set -e)
  for n in $list; do
    drv "$kind.$SYSTEM.\"$n\".drvPath"
    count=$((count + 1))
  done
done
list=$(names nixosConfigurations)
for n in $list; do
  drv "nixosConfigurations.\"$n\".config.system.build.toplevel.drvPath"
  count=$((count + 1))
done
echo "all $count outputs evaluate"
