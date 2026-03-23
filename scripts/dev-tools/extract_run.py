import yaml
import sys

def extract_run(workflow_path, job_name, step_name):
    with open(workflow_path, 'r', encoding='utf-8', errors='ignore') as f:
        data = yaml.safe_load(f)
    
    job = data.get('jobs', {}).get(job_name, {})
    steps = job.get('steps', [])
    for step in steps:
        if step.get('name') == step_name:
            return step.get('run', '')
    return None

if __name__ == "__main__":
    wp, jn, sn = sys.argv[1:4]
    print(extract_run(wp, jn, sn))
