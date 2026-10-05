package configstore

// Three-way merge per resource (Design 0001 section 5.6, ADR-0006).
//
// Every revision names the revision it was made against (base_uid) and, for a
// merge, a second parent (merge_uid). Comparing this node's newest revision of
// a resource (local) with a peer's newest revision (remote) over that ancestry
// gives one of the actions below. Changes to different resources never
// interact; the same resource changed on both sides is a conflict for the
// operator, never resolved silently.

// MergeAction is the outcome of comparing local and remote heads of a resource.
type MergeAction string

const (
	// MergeNone: this node already has the remote revision or is ahead of it.
	MergeNone MergeAction = "none"
	// MergeFastForward: the remote changed the resource since this node's
	// revision; apply it here.
	MergeFastForward MergeAction = "fast_forward"
	// MergeRecord: the remote is ahead but its state equals this node's
	// (or is a deletion of something unknown here); record it, nothing to apply.
	MergeRecord MergeAction = "record"
	// MergeConverged: both sides changed the resource independently to the
	// same state; record a merge revision joining the two lineages.
	MergeConverged MergeAction = "converged"
	// MergeConflict: both sides changed the resource differently.
	MergeConflict MergeAction = "conflict"
)

// Ancestry maps a revision uid to its parents (base, merge). Unknown uids
// have no entry; the walk stops there.
type Ancestry map[string][2]string

// Add registers a revision's parents.
func (a Ancestry) Add(r Revision) {
	a[r.UID] = [2]string{r.BaseUID, r.MergeUID}
}

// IsAncestor reports whether anc is desc or one of its ancestors.
func (a Ancestry) IsAncestor(anc, desc string) bool {
	if anc == "" || desc == "" {
		return false
	}
	seen := map[string]bool{}
	queue := []string{desc}
	for len(queue) > 0 {
		u := queue[0]
		queue = queue[1:]
		if u == anc {
			return true
		}
		if u == "" || seen[u] {
			continue
		}
		seen[u] = true
		if p, ok := a[u]; ok {
			queue = append(queue, p[0], p[1])
		}
	}
	return false
}

// Classify compares this node's head of a resource (nil: never recorded here)
// with a peer's head of the same resource.
func Classify(local, remote *Revision, anc Ancestry) MergeAction {
	if remote == nil {
		return MergeNone
	}
	if local == nil {
		if remote.Payload == nil {
			return MergeRecord // deleted there, never existed here
		}
		return MergeFastForward
	}
	if local.UID == remote.UID || anc.IsAncestor(remote.UID, local.UID) {
		return MergeNone
	}
	same := samePayload(local.Payload, remote.Payload)
	if anc.IsAncestor(local.UID, remote.UID) {
		if same {
			return MergeRecord
		}
		return MergeFastForward
	}
	if same {
		return MergeConverged
	}
	return MergeConflict
}
