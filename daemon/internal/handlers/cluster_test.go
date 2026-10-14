package handlers

import (
	"errors"
	"strings"
	"testing"
	"time"

	"dplaned/internal/configstore"
	"dplaned/internal/groups"
	"dplaned/internal/quorum"
)

func TestBuildMap(t *testing.T) {
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	ok := now.Add(-90 * time.Second)
	sync := &configstore.SyncStatus{Mode: "connected", Conflicts: 1, Pending: 2, Peers: []configstore.PeerStatus{
		{Peer: configstore.Peer{ID: "kb", Name: "nas2"}, Reachable: true, Waiting: 3},
	}}
	cfg := &quorum.Config{ClusterName: "dplane-x", Nodes: []quorum.Node{
		{ID: 1, Name: "nas1", Addr: "10.0.0.1", NodeKey: "ka"},
		{ID: 2, Name: "nas2", Addr: "10.0.0.2", NodeKey: "kb"},
		{ID: 3, Name: "pi", Addr: "10.0.0.9", Voter: true},
	}}
	st := quorum.Status{Running: true, Quorate: true, TotalVotes: 3,
		Members: []quorum.Member{{NodeID: 1}, {NodeID: 3}}}
	in := quorum.Info{Configured: true, Quorate: true, ExpectedVotes: 3, AutoFailover: true,
		Online: []string{"ka"}, Coordinator: "ka", Term: 7}
	gs := []groups.GroupStatus{{
		Group: groups.Group{Name: "data", Topology: groups.Replicated, Owner: "ka", Epoch: 4,
			Candidates: []string{"ka", "kb"}, Pools: []groups.PoolRef{{Name: "tank"}}, IntervalSecs: 300},
		Status:      groups.Status{Role: groups.RoleOwner, CanWrite: true},
		Replication: []groups.ReplState{{Direction: "out", Peer: "kb", Pool: "tank", LastOKAt: &ok}, {Direction: "in", Peer: "kb", Pool: "x"}},
	}}
	cm := buildMap("ka", "nas1", sync, cfg, st, in, gs, map[string]bool{"kb": true}, 1, now)

	if len(cm.Nodes) != 3 {
		t.Fatalf("nodes: %+v", cm.Nodes)
	}
	self, peer, voter := cm.Nodes[0], cm.Nodes[1], cm.Nodes[2]
	if !self.Self || !self.Coordinator || !self.Online || !self.InCluster || self.Addr != "10.0.0.1" {
		t.Errorf("self: %+v", self)
	}
	// nas2 is paired and reachable, but not in this node's partition, and in maintenance.
	if peer.Name != "nas2" || !peer.Paired || !peer.Reachable || peer.Online || !peer.Maintenance || peer.Waiting != 3 {
		t.Errorf("peer: %+v", peer)
	}
	if !voter.Voter || !voter.Online || voter.Key != "" || voter.Coordinator {
		t.Errorf("voter: %+v", voter)
	}
	if cm.Coordinator != "ka" || cm.Term != 7 || !cm.Quorum.Configured || cm.Quorum.TotalVotes != 3 {
		t.Errorf("quorum: %+v", cm.Quorum)
	}
	if cm.Conflicts != 1 || cm.Pending != 2 || cm.Proposals != 1 {
		t.Errorf("counts: %+v", cm)
	}
	g := cm.Groups[0]
	if !g.Serving || len(g.Replication) != 1 || g.Replication[0].LagSecs != 90 || g.Problems == nil {
		t.Errorf("group: %+v", g)
	}
}

func TestBuildMapStandalone(t *testing.T) {
	cm := buildMap("ka", "nas1", &configstore.SyncStatus{Mode: "standalone"}, nil, quorum.Status{}, quorum.Info{}, nil, nil, 0, time.Now())
	if len(cm.Nodes) != 1 || !cm.Nodes[0].Self || cm.Quorum.Configured || cm.Groups == nil {
		t.Fatalf("standalone: %+v", cm)
	}
}

func TestQDeviceTest(t *testing.T) {
	prev := qdeviceDial
	defer func() { qdeviceDial = prev }()
	cfg := &quorum.Config{ClusterName: "x", QDevice: &quorum.QDevice{Host: "10.0.0.9", Algorithm: "ffsplit"}}

	var dialed string
	qdeviceDial = func(addr string) error { dialed = addr; return nil }
	if r := testQDevice(cfg, quorum.Status{QDeviceAlive: true}); !r.OK || dialed != "10.0.0.9:5403" {
		t.Fatalf("voting: %+v (dialed %q)", r, dialed)
	}
	if r := testQDevice(cfg, quorum.Status{}); r.OK || !strings.Contains(r.Detail, "does not vote") {
		t.Fatalf("reachable, not voting: %+v", r)
	}
	qdeviceDial = func(string) error { return errors.New("connection refused") }
	if r := testQDevice(cfg, quorum.Status{QDeviceAlive: true}); r.OK || !strings.Contains(r.Detail, "5403") {
		t.Fatalf("unreachable: %+v", r)
	}
	if r := testQDevice(nil, quorum.Status{}); r.OK {
		t.Fatalf("no cluster: %+v", r)
	}
}
