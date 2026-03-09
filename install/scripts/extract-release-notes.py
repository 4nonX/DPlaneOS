#!/usr/bin/env python3
"""
Extract release notes for a specific version from CHANGELOG.md.

Usage: python3 extract-release-notes.py <version> <changelog>
  version:   e.g. 4.1.1 or v4.1.1 (v prefix optional)
  changelog: path to CHANGELOG.md

Supports heading formats:
  ## v4.1.1 (2026-03-09) — "Design System"
  ## [4.1.1] — 2026-03-09 — "Design System"   (legacy, still matched)

Exits 0 and prints notes to stdout.
Exits 1 if version not found.

Appends standard installation/upgrade instructions.
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
        with open(changelog_path, 'r', encoding='utf-8') as f:
            content = f.read()
    except OSError as e:
        print(f"Cannot read {changelog_path}: {e}", file=sys.stderr)
        sys.exit(1)

    escaped = re.escape(version)

    # Match both formats:
    #   ## v4.1.1 (2026-03-09) — "Design System"
    #   ## [4.1.1] — 2026-03-09 — "Design System"
    heading_re = re.compile(
        r'^##\s+(?:v|\[)' + escaped + r'[\] ][^\n]*\n',
        re.MULTILINE
    )

    match = heading_re.search(content)
    if not match:
        print(f"Version {version} not found in {changelog_path}", file=sys.stderr)
        sys.exit(1)

    # Capture the heading line as the release title (strip the leading ## )
    heading_line = match.group().strip()
    title = re.sub(r'^##\s+', '', heading_line)

    # Find content up to the next ## heading
    next_heading = re.search(r'^##\s+', content[match.end():], re.MULTILINE)
    end = match.end() + next_heading.start() if next_heading else len(content)

    notes = content[match.start():end].strip()

    # Append installation instructions
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
