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
