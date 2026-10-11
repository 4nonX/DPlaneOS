#!/usr/bin/env python3
"""Writes store paths into a Nix binary cache directory, one by one.

    write-binary-cache.py <cache-dir> < list-of-store-paths

"nix copy" refuses to put a path into a binary cache unless everything it
refers to is in that cache too. An update file holds only the paths the
public cache does not have (the rest is fetched from there), so its cache
is written here: a .narinfo and a compressed NAR per path, in the format
every Nix reads.
"""
import hashlib
import os
import subprocess
import sys


def out(*cmd):
    return subprocess.run(cmd, check=True, capture_output=True, text=True).stdout.strip()


def main():
    cache = sys.argv[1]
    os.makedirs(os.path.join(cache, "nar"), exist_ok=True)
    with open(os.path.join(cache, "nix-cache-info"), "w") as f:
        f.write("StoreDir: /nix/store\nWantMassQuery: 1\nPriority: 30\n")

    paths = [line.strip() for line in sys.stdin if line.strip()]
    for path in paths:
        if not path.startswith("/nix/store/") or "/" in path[len("/nix/store/"):]:
            sys.exit(f"not a store path: {path}")
        name = os.path.basename(path)
        nar_hash = out("nix-store", "--query", "--hash", path)       # sha256:<base32>
        nar_size = out("nix-store", "--query", "--size", path)
        refs = [os.path.basename(r) for r in out("nix-store", "--query", "--references", path).split()]
        deriver = out("nix-store", "--query", "--deriver", path)

        tmp = os.path.join(cache, "nar", name + ".tmp")
        dump = subprocess.Popen(["nix-store", "--dump", path], stdout=subprocess.PIPE)
        compress = subprocess.Popen(["zstd", "-q", "-10", "-T0", "-o", tmp, "-f"], stdin=dump.stdout)
        dump.stdout.close()
        if compress.wait() != 0 or dump.wait() != 0:
            sys.exit(f"could not pack {path}")

        digest = hashlib.sha256()
        size = 0
        with open(tmp, "rb") as f:
            for block in iter(lambda: f.read(1 << 20), b""):
                digest.update(block)
                size += len(block)
        file_hash = digest.hexdigest()
        url = f"nar/{file_hash}.nar.zst"
        os.replace(tmp, os.path.join(cache, url))

        lines = [
            f"StorePath: {path}",
            f"URL: {url}",
            "Compression: zstd",
            f"FileHash: sha256:{file_hash}",
            f"FileSize: {size}",
            f"NarHash: {nar_hash}",
            f"NarSize: {nar_size}",
            "References: " + " ".join(refs),
        ]
        if deriver.startswith("/nix/store/"):
            lines.append(f"Deriver: {os.path.basename(deriver)}")
        with open(os.path.join(cache, name[:32] + ".narinfo"), "w") as f:
            f.write("\n".join(lines) + "\n")
    print(f"{len(paths)} store paths written to {cache}")


if __name__ == "__main__":
    main()
