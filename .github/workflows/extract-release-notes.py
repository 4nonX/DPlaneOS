import re, os

version = os.environ["GITHUB_REF_NAME"]   # e.g. "v3.3.1"
repo    = os.environ["GITHUB_REPOSITORY"]

with open("CHANGELOG.md") as f:
    changelog = f.read()

# Match "## v3.3.1" or "## 3.3.1" - CHANGELOG may include or omit the v prefix
bare = version.lstrip("v")
pattern = r"(## v?" + re.escape(bare) + r"[ \t].*?)(?=\n## |\Z)"
match = re.search(pattern, changelog, re.DOTALL)

if match:
    notes = match.group(1).strip()
else:
    notes = (
        "See [CHANGELOG.md](https://github.com/" + repo + "/blob/main/CHANGELOG.md) "
        "for release notes."
    )

notes += (
    "\n\n---\n\n"
    "## Installation\n\n"
    "### Debian / Ubuntu\n"
    "```bash\n"
    "tar xzf dplaneos-" + version + ".tar.gz\n"
    "cd dplaneos-" + version + "\n"
    "sudo bash install.sh\n"
    "```\n\n"
    "### NixOS\n"
    "```bash\n"
    "tar xzf dplaneos-" + version + ".tar.gz\n"
    "cd dplaneos-" + version + "/nixos\n"
    "sudo bash setup-nixos.sh\n"
    "sudo nixos-rebuild switch\n"
    "```\n\n"
    "### Verify checksum\n"
    "```bash\n"
    "sha256sum -c dplaneos-" + version + ".tar.gz.sha256\n"
    "```\n"
)

with open("/tmp/release-notes.md", "w") as f:
    f.write(notes)
print("Extracted", len(notes), "chars for", version)

