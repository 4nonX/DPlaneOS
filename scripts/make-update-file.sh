#!/usr/bin/env bash
# Builds the update file of a release: dplaneos-v<version>-<arch>.update
#
#   make-update-file.sh <system> <version> <out-dir>
#
#   <system>   store path of the NixOS system (config.system.build.toplevel)
#   <version>  the DPlaneOS version it is
#
# Environment:
#   UPDATE_SIGNING_KEY_FILE  Ed25519 private key (PEM) the file is signed
#                            with. Required: an unsigned file is useless,
#                            systems refuse it.
#   UPDATE_PUBLIC_KEY_FILE   public key to check the result against
#                            (default: nixos/update-signing.pub)
#   UPSTREAM_CACHE           binary cache the installed systems fetch
#                            everything else from (default
#                            https://cache.nixos.org). Store paths that are
#                            in it are left out of the file.
#   EXCLUDE_CLOSURE_OF       instead of asking UPSTREAM_CACHE: leave out what
#                            is in the closure of this store path (tests,
#                            where the "old" system is what a machine has)
#
# The file holds the parts of the system that only this project builds (the
# daemon, the web interface, the system configuration) as a Nix binary
# cache; see nixos/dplaneos-update.sh for how it is installed.
set -euo pipefail

system="${1:?usage: make-update-file.sh <system> <version> <out-dir>}"
version="${2:?version}"
out="${3:?out-dir}"
upstream="${UPSTREAM_CACHE:-https://cache.nixos.org}"
key="${UPDATE_SIGNING_KEY_FILE:?UPDATE_SIGNING_KEY_FILE is not set: the update file must be signed}"
pub="${UPDATE_PUBLIC_KEY_FILE:-$(dirname "$0")/../nixos/update-signing.pub}"

[ -x "${system}/bin/switch-to-configuration" ] || { echo "${system} is not a NixOS system" >&2; exit 1; }
arch=$(uname -m)
work=$(mktemp -d)
trap 'rm -rf "${work}"' EXIT
mkdir -p "${out}"

nix-store -qR "${system}" | sort -u > "${work}/closure"

if [ -n "${EXCLUDE_CLOSURE_OF:-}" ]; then
    nix-store -qR "${EXCLUDE_CLOSURE_OF}" | sort -u > "${work}/have"
else
    # Which paths does the upstream cache have? One request per path.
    check() {
        local path="$1" hash
        hash=$(basename "${path}" | cut -c1-32)
        code=$(curl -s -o /dev/null -w '%{http_code}' --retry 5 --retry-all-errors --max-time 30 "${upstream%/}/${hash}.narinfo" || echo 000)
        case "${code}" in
            200) echo "${path}" ;;
            404|403) ;;
            # Anything else is "do not know": put the path in the file
            # rather than risk an update that cannot be completed.
            *) echo "upstream cache answered ${code} for ${path}; including it" >&2 ;;
        esac
    }
    export -f check
    export upstream
    xargs -P 16 -I{} bash -c 'check "$@"' _ {} < "${work}/closure" | sort -u > "${work}/have"
fi

comm -23 "${work}/closure" "${work}/have" > "${work}/include"
count=$(wc -l < "${work}/include")
total=$(wc -l < "${work}/closure")
[ "${count}" -gt 0 ] || { echo "nothing to put in the update file: ${system} is entirely upstream" >&2; exit 1; }
grep -qxF "${system}" "${work}/include" || { echo "the system path itself must be part of the update file" >&2; exit 1; }
echo "update file: ${count} of ${total} store paths (the rest comes from ${upstream})"

mkdir "${work}/payload"
python3 "$(dirname "$0")/write-binary-cache.py" "${work}/payload/cache" < "${work}/include"

cat > "${work}/payload/metadata.json" <<EOF
{
  "version": "${version}",
  "arch": "${arch}",
  "system": "${system}",
  "paths": ${count},
  "built_at": "$(date -u +"%Y-%m-%dT%H:%M:%SZ")"
}
EOF

mkdir "${work}/outer"
tar -cf "${work}/outer/update.tar" -C "${work}/payload" --sort=name --owner=0 --group=0 --numeric-owner metadata.json cache
( cd "${work}/outer" && sha256sum update.tar > update.tar.sha256 )
openssl pkeyutl -sign -inkey "${key}" -rawin -in "${work}/outer/update.tar.sha256" -out "${work}/outer/update.tar.sha256.sig"
# What goes out must be what systems accept.
openssl pkeyutl -verify -pubin -inkey "${pub}" -rawin -in "${work}/outer/update.tar.sha256" -sigfile "${work}/outer/update.tar.sha256.sig" >/dev/null \
    || { echo "the signature does not verify against ${pub}: wrong signing key?" >&2; exit 1; }

file="${out}/dplaneos-v${version}-${arch}.update"
tar -cf "${file}" -C "${work}/outer" --owner=0 --group=0 --numeric-owner update.tar update.tar.sha256 update.tar.sha256.sig
echo "${file} ($(du -h "${file}" | cut -f1))"
