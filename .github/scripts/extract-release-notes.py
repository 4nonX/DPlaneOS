import sys
import os
import re

def extract_notes(version, changelog_path):
    if not os.path.exists(changelog_path):
        print(f"Error: Changelog not found at {changelog_path}")
        sys.exit(1)

    with open(changelog_path, 'r', encoding='utf-8') as f:
        content = f.read()

    # Look for ## vX.Y.Z or ## X.Y.Z
    # Escaping the dot for regex and matching until the next ## or end of file
    pattern = rf'## v?{re.escape(version)}.*?\n(.*?)(?=\n## |$)'
    match = re.search(pattern, content, re.DOTALL)

    if not match:
        print(f"No release notes found for version {version}")
        return ""

    notes = match.group(1).strip()
    
    # Add a footer with installation instructions
    footer = (
        "\n\n---\n\n### Installation\n\n"
        f"- **New install:** write `dplaneos-v{version}-installer-amd64.iso` (or `-arm64`) to a USB stick and boot it in UEFI mode; "
        "see the [Installation Guide](https://github.com/4nonX/DPlaneOS/blob/main/docs/admin/INSTALLATION-GUIDE.md).\n"
        f"- **Try it without installing:** `dplaneos-v{version}-live-x86_64.iso` (or `-arm64`).\n"
        "- **Update an installed system:** System › System Updates; see "
        "[Updates](https://github.com/4nonX/DPlaneOS/blob/main/docs/admin/OTA-UPDATES.md)."
    )
    
    return notes + footer

if __name__ == "__main__":
    if len(sys.argv) < 3:
        print("Usage: extract-release-notes.py <version> <changelog_path>")
        sys.exit(1)

    version = sys.argv[1]
    changelog_path = sys.argv[2]

    notes = extract_notes(version, changelog_path)
    print(notes)
