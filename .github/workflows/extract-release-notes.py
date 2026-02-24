import re, os, sys

version = os.environ["GITHUB_REF_NAME"]
repo    = os.environ["GITHUB_REPOSITORY"]

with open("CHANGELOG.md") as f:
    changelog = f.read()

pattern = r"(## " + re.escape(version) + r" .*?)(?=\n## v|\Z)"
match = re.search(pattern, changelog, re.DOTALL)

if match:
    notes = match.group(1).strip()
else:
    notes = "See [CHANGELOG.md](https://github.com/" + repo + "/blob/main/CHANGELOG.md) for release notes."

notes += (
    "\n\n---\n\n"
    "**Installation:**\n"
    "```bash\n"
    "tar xzf dplaneos-" + version + ".tar.gz\n"
    "cd dplaneos-" + version + "\n"
    "sudo make install\n"
    "sudo systemctl start dplaned\n"
    "```\n\n"
    "**Verify checksum:**\n"
    "```bash\n"
    "sha256sum -c dplaneos-" + version + ".tar.gz.sha256\n"
    "```\n"
)

with open("/tmp/release-notes.md", "w") as f:
    f.write(notes)
print("Extracted", len(notes), "chars for", version)
