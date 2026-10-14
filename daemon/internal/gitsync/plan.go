package gitsync

import (
	"sort"

	"dplaned/internal/configstore"
)

// Three-way comparison of one scope (Design 0001 section 5.6: one merge
// engine for peers and Git).
//
// For every resource of the scope: base is its version at the last commit
// this node synchronised with, local its current version in this node's
// store, remote its version in the newest commit. Versions are compared by
// Hash (secrets replaced by references, so they never count).

// Action for one resource.
type Action string

const (
	InSync   Action = "in_sync"  // local and remote are the same
	Push     Action = "push"     // only this node changed it: goes to the repository
	Pull     Action = "pull"     // only the repository changed it: comes to this node
	Conflict Action = "conflict" // both changed it differently: the operator decides
)

// Base is a resource's version at the synchronised commit.
type Base struct {
	UID  string // this store's revision that version corresponds to
	Hash string
}

// Classify decides one resource. Hashes are "" for absent or deleted.
func Classify(base *Base, localHash, remoteHash string) Action {
	b := ""
	if base != nil {
		b = base.Hash
	}
	switch {
	case localHash == remoteHash:
		return InSync
	case remoteHash == b:
		return Push
	case localHash == b:
		return Pull
	}
	return Conflict
}

// Change is a resource whose repository version differs from this node's.
type Change struct {
	ID      string               `json:"id"` // kind/key
	Kind    string               `json:"kind"`
	Key     string               `json:"key"`
	Action  Action               `json:"action"`
	Remote  configstore.Resource `json:"-"`        // repository version (secrets: this node's), nil payload = deleted there
	BaseUID string               `json:"-"`        // revision of the base version
	Delete  bool                 `json:"deletion"` // the repository deletes it
}

// ScopePlan is the comparison of one scope.
type ScopePlan struct {
	InSync    []string `json:"in_sync"`
	Push      []string `json:"push"`      // resources to write to the repository
	Pull      []Change `json:"pull"`      // changes from the repository
	Conflicts []Change `json:"conflicts"` // changed on both sides
	// EmptyKinds trip the empty-state guard: the repository removes every
	// resource of these kinds that this node has.
	EmptyKinds []string `json:"empty_kinds,omitempty"`
}

// Compare builds the plan of one scope. local holds this node's current
// revisions of the scope's resources (deleted ones included, nil payload);
// remote the resources of the newest commit as written in the file; base
// the synchronised versions by resource id.
func Compare(local map[string]configstore.Revision, remote []configstore.Resource, base map[string]Base) ScopePlan {
	plan := ScopePlan{InSync: []string{}, Push: []string{}, Pull: []Change{}, Conflicts: []Change{}}
	remoteByID := map[string]configstore.Resource{}
	for _, r := range remote {
		remoteByID[r.ID()] = r
	}
	ids := map[string]bool{}
	for id := range local {
		ids[id] = true
	}
	for id := range remoteByID {
		ids[id] = true
	}
	for id := range base {
		ids[id] = true
	}
	sorted := make([]string, 0, len(ids))
	for id := range ids {
		sorted = append(sorted, id)
	}
	sort.Strings(sorted)

	localCount, remoteCount := map[string]int{}, map[string]int{}
	for _, id := range sorted {
		l, hasLocal := local[id]
		var lp map[string]any
		lHash := ""
		if hasLocal && l.Payload != nil {
			lp = l.Payload
			lHash = Hash(configstore.Resource{Kind: l.Kind, Key: l.Key, Payload: l.Payload})
			localCount[l.Kind]++
		}
		rRes, hasRemote := remoteByID[id]
		rHash := ""
		if hasRemote {
			rRes = FromGit(rRes, lp)
			rHash = Hash(rRes)
			remoteCount[rRes.Kind]++
		}
		var b *Base
		if bb, ok := base[id]; ok {
			b = &bb
		}
		act := Classify(b, lHash, rHash)
		switch act {
		case InSync:
			plan.InSync = append(plan.InSync, id)
		case Push:
			plan.Push = append(plan.Push, id)
		default:
			c := Change{ID: id, Action: act}
			if hasRemote {
				c.Remote, c.Kind, c.Key = rRes, rRes.Kind, rRes.Key
			} else {
				// Deleted in the repository: describe it from this node's version.
				c.Remote = configstore.Resource{Kind: l.Kind, Key: l.Key, Scope: l.Scope, ScopeID: l.ScopeID}
				c.Kind, c.Key, c.Delete = l.Kind, l.Key, true
			}
			if b != nil {
				c.BaseUID = b.UID
			}
			if act == Pull && hasLocal {
				// This node's version is the base content-wise: building on its
				// revision makes the change a fast-forward in the merge engine.
				c.BaseUID = l.UID
			}
			if act == Pull {
				plan.Pull = append(plan.Pull, c)
			} else {
				plan.Conflicts = append(plan.Conflicts, c)
			}
		}
	}
	for kind, n := range localCount {
		if n > 0 && remoteCount[kind] == 0 {
			// Only a guard when the repository is what removes them (they were
			// in the synchronised commit), not when they are new on this node.
			removed := 0
			for _, c := range plan.Pull {
				if c.Kind == kind && c.Delete {
					removed++
				}
			}
			if removed == n {
				plan.EmptyKinds = append(plan.EmptyKinds, kind)
			}
		}
	}
	sort.Strings(plan.EmptyKinds)
	return plan
}
