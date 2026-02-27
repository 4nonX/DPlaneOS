package gitops

// docker_state.go — Desired state types, YAML parser, and validation for the
// Docker GitOps subsystem.
//
// State machine (mirrors ZFS GitOps):
//
//	Git repo ──► parse state.yaml (containers: section) ──► DesiredDockerState
//	              │
//	              ▼
//	     Validate (image names, restart policies, port syntax)
//	              │
//	              ▼
//	    Read live Docker API ──► LiveDockerState
//	              │
//	              ▼
//	        DiffEngine ──► []DockerDiffItem  (SAFE | BLOCKED | CREATE | MODIFY | NOP)
//	              │
//	              ▼
//	       SafeApply  (transactional; BLOCKED items halt plan)
//	              │
//	              ▼
//	      DriftDetector (background