package handlers

import (
	"database/sql"
	"strings"

	"dplaned/internal/configstore"
	"dplaned/internal/gitsync"
	"dplaned/internal/groups"
	"dplaned/internal/quorum"
)

// GitScopeOwner decides whether this node writes a scope to the Git
// repository (Design 0001 section 5.8: the scope's Git owner):
//
//   - a node's own scope: the node itself;
//   - a storage group: its current owner (the epoch holder);
//   - the cluster scope: the cluster coordinator; between paired nodes
//     without a cluster, the paired node with the lowest node id;
//   - the single-file layout (standalone): this node.
func GitScopeOwner(db *sql.DB, mon *quorum.Monitor, scope string) (bool, string) {
	self, err := configstore.NodeID(db)
	if err != nil {
		return false, err.Error()
	}
	switch {
	case scope == gitsync.ScopeAll:
		return true, ""
	case strings.HasPrefix(scope, "node:"):
		if strings.TrimPrefix(scope, "node:") == configstore.NodeName() {
			return true, ""
		}
		return false, "another node's own settings"
	case strings.HasPrefix(scope, "group:"):
		g, err := groups.Get(db, strings.TrimPrefix(scope, "group:"))
		if err != nil {
			return false, "unknown storage group"
		}
		if g.Owner == self {
			return true, ""
		}
		return false, "written by the group's owner"
	case scope == gitsync.ScopeCluster:
		var in quorum.Info
		if mon != nil {
			in = mon.Info()
		}
		peers, _ := configstore.Peers(db)
		return clusterScopeOwner(self, in, peers)
	}
	return false, "unknown scope"
}

func clusterScopeOwner(self string, in quorum.Info, peers []configstore.Peer) (bool, string) {
	if in.Configured {
		switch {
		case in.Coordinator == self:
			return true, ""
		case in.Coordinator == "":
			return false, "no quorum: no node writes the cluster scope until the cluster is quorate"
		}
		return false, "written by the cluster coordinator"
	}
	for _, p := range peers {
		if p.ID < self {
			return false, "written by the paired node with the lowest node id"
		}
	}
	return true, ""
}
