package handlers

import (
	"strings"
	"testing"

	"dplaned/internal/configstore"
)

// Where a recorded change applies, and whether the other nodes have it.
func TestRevisionReach(t *testing.T) {
	b := configstore.Peer{ID: "b", Name: "nas-b", ServedCursor: 40}
	c := configstore.Peer{ID: "c", Name: "nas-c", ServedCursor: 10}
	rev := func(id int64, scope, kind, origin string) configstore.Revision {
		return configstore.Revision{ID: id, Scope: scope, Kind: kind, Origin: origin}
	}
	for _, tc := range []struct {
		name    string
		r       configstore.Revision
		peers   []configstore.Peer
		reach   string
		waiting []string
	}{
		{"host name", rev(50, configstore.ScopeNode, configstore.KindSystem, "gui"), []configstore.Peer{b}, "node", nil},
		{"a stack is not exchanged", rev(50, configstore.ScopeGroup, configstore.KindStack, "gui"), []configstore.Peer{b}, "node", nil},
		{"from the other node", rev(50, configstore.ScopeCluster, configstore.KindUser, "peer"), []configstore.Peer{b}, "received", nil},
		{"no other node", rev(50, configstore.ScopeCluster, configstore.KindUser, "gui"), nil, "alone", nil},
		{"fetched by all", rev(5, configstore.ScopeCluster, configstore.KindUser, "gui"), []configstore.Peer{b, c}, "shared", nil},
		{"one is behind", rev(30, configstore.ScopeCluster, configstore.KindUser, "gui"), []configstore.Peer{b, c}, "waiting", []string{"nas-c"}},
		{"made while cut off", rev(50, configstore.ScopeGroup, configstore.KindShare, "gui"), []configstore.Peer{b, c}, "waiting", []string{"nas-b", "nas-c"}},
	} {
		reach, waiting := revisionReach(tc.r, tc.peers)
		if reach != tc.reach || strings.Join(waiting, ",") != strings.Join(tc.waiting, ",") {
			t.Errorf("%s: got %s %v, want %s %v", tc.name, reach, waiting, tc.reach, tc.waiting)
		}
	}
}
