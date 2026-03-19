import sys
import os
import re

def extract_notes(version, changelog_path):
    if not os.path.exists(changelog_path):
        return f"Changelog not found at {changelog_path}"
    
    with open(changelog_path, 'r', encoding='utf-8') as f:
        content = f.read()

    # Match ## v6.0.2 (2026-03-09) - "Deterministic Integrity"
    # or ## 6.0.2
    pattern = rf'## v?{re.escape(version)}.*?\n(.*?)(?=\n## |$)'
    match = re.search(pattern, content, re.DOTALL)
    
    if not match:
        return f"Release notes for version {version} not found."
    
    notes = match.group(1).strip()
    
    # Append common installation instructions
    notes += "\n\n---\n\n### Installation\n"
    notes += "Upgrade existing install:\n"
    notes += "```bash\nsudo bash install.sh --upgrade\n```\n"
    notes += "Fresh install:\n"
    notes += "```bash\ncurl -s https://get.dplaneos.com | sudo bash\n```\n"
    
    return notes

if __name__ == "__main__":
    if len(sys.argv) < 3:
        print("Usage: python3 extract-release-notes.py <version> <changelog_path>")
        sys.exit(1)
    
    ver = sys.argv[1].lstrip('v')
    path = sys.argv[2]
    print(extract_notes(ver, path))
