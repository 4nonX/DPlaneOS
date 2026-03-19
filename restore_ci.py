import subprocess
import yaml
import os

def get_git_file(rev, path):
    result = subprocess.run(['git', 'show', f'{rev}:{path}'], capture_output=True, text=True, encoding='utf-8')
    if result.returncode != 0:
        # Try without encoding (might be binary or different)
        result = subprocess.run(['git', 'show', f'{rev}:{path}'], capture_output=True)
        return result.stdout.decode('utf-8', errors='ignore')
    return result.stdout

def extract_step_run(yml_content, job_name, step_name):
    data = yaml.safe_load(yml_content)
    steps = data.get('jobs', {}).get(job_name, {}).get('steps', [])
    for s in steps:
        if s.get('name') == step_name:
            return s.get('run', '')
    return None

# 1. API Integration Test (validate.yml)
v_yml = get_git_file('a1e08fb', '.github/workflows/validate.yml')
api_run = extract_step_run(v_yml, 'validate', 'API Integration Suite')
with open('.github/scripts/api-integration-test.sh', 'w', encoding='utf-8', newline='\n') as f:
    f.write("#!/bin/bash\nset -e\n")
    f.write(api_run)

# 2. Fleet Integration Test (fleet-install.yml)
fi_yml = get_git_file('a1e08fb', '.github/workflows/fleet-install.yml')
# The original step name was "Full Install & Fleet Suite"
fleet_run = extract_step_run(fi_yml, 'integration', 'Full Install & Fleet Suite')
with open('.github/scripts/fleet-integration-test.sh', 'w', encoding='utf-8', newline='\n') as f:
    f.write("#!/bin/bash\nset -e\n")
    f.write(fleet_run)

# 3. Extract Release Notes (r.txt logic was already correct but I'll fix the URL)
# I'll manually write this one to fix the URL as requested.
notes_py = """import sys
import os
import re

def extract_notes(version, changelog_path):
    if not os.path.exists(changelog_path):
        return f"Changelog not found at {changelog_path}"
    
    with open(changelog_path, 'r', encoding='utf-8') as f:
        content = f.read()

    # Match ## v6.0.2 (2026-03-09) - "Deterministic Integrity"
    # Use re.DOTALL to match across newlines in the capture group
    pattern = rf'## v?{re.escape(version)}.*?\\n(.*?)(?=\\n## |$)'
    match = re.search(pattern, content, re.DOTALL)
    
    if not match:
        return f"Release notes for version {version} not found."
    
    notes = match.group(1).strip()
    
    # Corrected installation instructions URL
    notes += "\\n\\n---\\n\\n### Installation\\n"
    notes += "Upgrade existing install:\\n"
    notes += "```bash\\nsudo bash install.sh --upgrade\\n```\\n"
    notes += "Fresh install:\\n"
    notes += "```bash\\ncurl -s https://get.d-net.me | sudo bash\\n```\\n"
    
    return notes

if __name__ == "__main__":
    if len(sys.argv) < 3:
        print("Usage: python3 extract-release-notes.py <version> <changelog_path>")
        sys.exit(1)
    
    ver = sys.argv[1].lstrip('v')
    path = sys.argv[2]
    print(extract_notes(ver, path))
"""
with open('.github/scripts/extract-release-notes.py', 'w', encoding='utf-8', newline='\n') as f:
    f.write(notes_py)
