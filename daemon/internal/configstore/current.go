package configstore

import (
	"database/sql"
	"fmt"
	"sort"
	"time"
)

// Effective configuration and its source (Design 0001 section 5.10,
// "Configuration": source of each effective setting; local overrides marked).

// CurrentItem is the current revision of one resource and where it came from.
type CurrentItem struct {
	Kind       string    `json:"kind"`
	Key        string    `json:"key"`
	Scope      string    `json:"scope"` // node, group, cluster
	ScopeID    string    `json:"scope_id,omitempty"`
	RevisionID int64     `json:"revision_id"`
	Origin     string    `json:"origin"`
	OriginNode string    `json:"origin_node"`
	Author     string    `json:"author,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
	// Exchanged: the resource is kept in step with paired nodes.
	Exchanged bool `json:"exchanged"`
	// NotPulledBy are the paired nodes that have not taken this node's
	// version yet: until they do it exists only here (a local override while
	// the node is isolated).
	NotPulledBy []string `json:"not_pulled_by,omitempty"`
	// Authority is what the default conflict rule compares ("" = none).
	Authority string `json:"authority,omitempty"`
	Conflict  bool   `json:"conflict"` // an open conflict concerns it
	Proposal  bool   `json:"proposal"` // an open proposal concerns it
}

// Current lists the current revision of every existing resource.
func Current(db *sql.DB) ([]CurrentItem, error) {
	latest, err := Latest(db)
	if err != nil {
		return nil, err
	}
	peers, err := loadPeers(db, "")
	if err != nil {
		return nil, err
	}
	flagged := func(q string) (map[string]bool, error) {
		out := map[string]bool{}
		rows, err := db.Query(q)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		for rows.Next() {
			var k, key string
			if err := rows.Scan(&k, &key); err != nil {
				return nil, err
			}
			out[k+"/"+key] = true
		}
		return out, rows.Err()
	}
	conflicts, err := flagged(`SELECT resource_kind, resource_key FROM config_conflicts WHERE status = 'open'`)
	if err != nil {
		return nil, err
	}
	proposals, err := flagged(`SELECT resource_kind, resource_key FROM config_proposals WHERE status = 'open'`)
	if err != nil {
		return nil, err
	}
	out := make([]CurrentItem, 0, len(latest))
	for id, r := range latest {
		if r.Payload == nil {
			continue // deleted
		}
		it := CurrentItem{Kind: r.Kind, Key: r.Key, Scope: r.Scope, ScopeID: r.ScopeID, RevisionID: r.ID,
			Origin: r.Origin, OriginNode: r.OriginNode, Author: r.Author, CreatedAt: r.CreatedAt,
			Exchanged: SyncKinds[r.Kind] && r.Scope != ScopeNode, Conflict: conflicts[id], Proposal: proposals[id]}
		if it.Exchanged && r.Origin != OriginPeer {
			for _, p := range peers {
				if r.ID > p.ServedCursor {
					name := p.Name
					if name == "" {
						name = p.ID
					}
					it.NotPulledBy = append(it.NotPulledBy, name)
				}
			}
		}
		switch {
		case r.Epoch > 0 && r.Scope == ScopeGroup:
			it.Authority = fmt.Sprintf("storage group owner, epoch %d", r.Epoch)
		case r.Epoch > 0 && r.Scope == ScopeCluster:
			it.Authority = fmt.Sprintf("cluster coordinator, term %d", r.Epoch)
		}
		out = append(out, it)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Kind != out[j].Kind {
			return out[i].Kind < out[j].Kind
		}
		return out[i].Key < out[j].Key
	})
	return out, nil
}
