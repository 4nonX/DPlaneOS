#!/usr/bin/env python3
"""
Extract release notes for a specific version from CHANGELOG.md.

Usage: python3 extract-release-notes.py <version> <changelog>
  version:   e.g. 3.3.1 or v3.3.1 (v prefix optional)
  changelog: path to CHANGELOG.md

Exits 0 and prints notes to stdout.
Exits 1 if version not found.

Also appends standard installation instructions using the correct
command: sudo bash install.sh  (NOT sudo make install — no Makefile exists)
"""

import sys
import re

def main():
    if len(sys.argv) < 3:
        print(f"Usage: {sys.argv[0]} <version> <changelog>", file=sys.stderr)
        sys.exit(1)

    version = sys.argv[1].lstrip('v')
    changelog_path = sys.argv[2]

    try:
        with open(changelog_path, 'r') as f:
            content = f.read()
    except OSError as e:
        print(f"Cannot read {changelog_path}: {e}", file=sys.stderr)
        sys.exit(1)

    # Match heading with or without v prefix: ## v3.3.1 or ## 3.3.1
    # Escaped dots so 3.3.1 doesn't match 3x3x1
    escaped = re.escape(version)
    heading_re = re.compile(
        r'^##\s+v?' + escaped + r'\b[^\n]*\n',
        re.MULTILINE
    )

    match = heading_re.search(content)
    if not match:
        print(f"Version {version} not found in {changelog_path}", file=sys.stderr)
        sys.exit(1)

    start = match.start()
    # Find the next ## heading after this one
    next_heading = re.search(r'^##\s+', content[match.end():], re.MULTILINE)
    if next_heading:
        end = match.end() + next_heading.start()
    else:
        end = len(content)

    notes = content[start:end].strip()

    # Append installation instructions with correct command
    install_block = f"""

---

## Installation

```bash
curl -fsSL https://get.dplaneos.io | sudo bash
```

Or from tarball:

```bash
tar xzf dplaneos-v{version}-linux-amd64.tar.gz
cd dplaneos-v{version}
sudo bash install.sh
```

Upgrade existing install:

```bash
sudo bash install.sh --upgrade
```
"""

    print(notes + install_block)

if __name__ == '__main__':
    main()
